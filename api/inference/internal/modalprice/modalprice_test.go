package modalprice

import (
	"math/big"
	"testing"
)

// Ported from the 0g-router's backend/pkg/modalprice/modalprice_test.go
// (34270244) with the same numbers; FromPricingJSON is router-only.

func omniShape(t *testing.T) Pricing {
	t.Helper()
	p, err := Parse(map[string]string{"image": "11/6", "video": "11/6", "audio": "79/9"},
		map[string]string{"text_only_input": "69/127", "audio": "626/127"})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func ratEq(t *testing.T, want, got *big.Rat, msg string) {
	t.Helper()
	if (want == nil) != (got == nil) || (want != nil && want.Cmp(got) != 0) {
		t.Errorf("%s: want %v, got %v", msg, want, got)
	}
}

func TestParse(t *testing.T) {
	p := omniShape(t)
	ratEq(t, big.NewRat(11, 6), p.Input[Image], "image")
	ratEq(t, big.NewRat(79, 9), p.Input[Audio], "audio")
	ratEq(t, big.NewRat(69, 127), p.TextOnlyOutput, "text_only_input")
	ratEq(t, big.NewRat(626, 127), p.AudioOutput, "output audio")
	if p.Empty() {
		t.Error("omni shape reported empty")
	}

	for name, c := range map[string][2]map[string]string{
		"unknown input modality":    {{"text": "2"}, nil},
		"unknown output key":        {nil, {"video": "2"}},
		"input below 1x":            {{"image": "1/2"}, nil},
		"text-only output above 1x": {nil, {"text_only_input": "3/2"}},
		"above cap":                 {{"audio": "101"}, nil},
		"zero denominator":          {{"audio": "3/0"}, nil},
		"not a number":              {{"audio": "x"}, nil},
	} {
		if _, err := Parse(c[0], c[1]); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	empty, err := (*Published)(nil).Parse()
	if err != nil || !empty.Empty() {
		t.Errorf("nil Published: %v, empty=%v", err, empty.Empty())
	}
}

func TestParse_StrictSpelling(t *testing.T) {
	for _, bad := range []string{" 11/6", "11 / 6", "+2", "2.5", "0/1", "1/0", "011/6", "1/", "/6"} {
		if _, err := Parse(map[string]string{"image": bad}, nil); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if _, err := Parse(map[string]string{"image": "2"}, nil); err != nil {
		t.Error(err)
	}
}

// Factors against usage measured on staging 2026-10-23 and Alibaba's
// mainland list for qwen3-omni-flash (¥ per 1M tokens: text 1.8, image/video
// 3.3, audio 15.8; output 12.7, 6.9 text-only, audio 62.6).
func TestFactors(t *testing.T) {
	omni := omniShape(t)
	fee := func(u Usage) *big.Rat { // in ¥ per 1M at prompt 1.8, completion 12.7
		in, out := Factors(omni, u)
		if in == nil {
			in = big.NewRat(1, 1)
		}
		if out == nil {
			out = big.NewRat(1, 1)
		}
		f := new(big.Rat).Mul(big.NewRat(18*u.PromptTokens, 10), in)
		return f.Add(f, new(big.Rat).Mul(big.NewRat(127*u.CompletionTokens, 10), out))
	}
	vendor := func(text, img, vid, aud, out, audOut int64, textOnly bool) *big.Rat {
		f := big.NewRat(18*text+33*(img+vid)+158*aud, 10)
		switch {
		case audOut > 0:
			return f.Add(f, big.NewRat(626*audOut, 10))
		case textOnly:
			return f.Add(f, big.NewRat(69*out, 10))
		}
		return f.Add(f, big.NewRat(127*out, 10))
	}

	// image: prompt 37 = 27 image + 10 text, 2 output tokens.
	ratEq(t, vendor(10, 27, 0, 0, 2, 0, false), fee(Usage{PromptTokens: 37, CompletionTokens: 2, Split: true, ImageTokens: 27}), "image")
	// video with its audio track: 2552 video + 65 audio + 7 text.
	ratEq(t, vendor(7, 0, 2552, 65, 2, 0, false), fee(Usage{PromptTokens: 2624, CompletionTokens: 2, Split: true, VideoTokens: 2552, AudioTokens: 65}), "video")
	// text-only input: the output discount.
	ratEq(t, vendor(195, 0, 0, 0, 35, 0, true), fee(Usage{PromptTokens: 195, CompletionTokens: 35, Split: true}), "text-only")
	// audio output (27 audio + 8 text): only the audio is billed, and the
	// text-only discount does not stack on it.
	ratEq(t, vendor(195, 0, 0, 0, 0, 27, true), fee(Usage{PromptTokens: 195, CompletionTokens: 35, Split: true, AudioOutputTokens: 27}), "audio output")
	// no split reported: flat both ways (no discount, no media premium).
	in, out := Factors(omni, Usage{PromptTokens: 100, CompletionTokens: 10})
	ratEq(t, nil, in, "no split in")
	ratEq(t, nil, out, "no split out")
	// media reported with no prompt tokens still rules out the text-only discount.
	_, out = Factors(omni, Usage{CompletionTokens: 10, Split: true, ImageTokens: 5})
	ratEq(t, nil, out, "media without prompt tokens")
	// a split that overshoots the prompt is scaled into it.
	in, _ = Factors(omni, Usage{PromptTokens: 10, Split: true, AudioTokens: 20})
	ratEq(t, big.NewRat(79, 9), in, "overshoot")

	// Without output.audio, audio output tokens are ordinary completion tokens
	// (and the input here is multimodal, so no discount).
	inputOnly, err := Parse(map[string]string{"audio": "90/11"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, out = Factors(inputOnly, Usage{PromptTokens: 50, CompletionTokens: 30, Split: true, AudioTokens: 10, AudioOutputTokens: 20})
	ratEq(t, nil, out, "audio output without output.audio")
}

func TestMaxFactors(t *testing.T) {
	in, out := omniShape(t).MaxFactors()
	ratEq(t, big.NewRat(79, 9), in, "max in")
	ratEq(t, big.NewRat(626, 127), out, "max out")
	in, out = Pricing{}.MaxFactors()
	ratEq(t, big.NewRat(1, 1), in, "flat in")
	ratEq(t, big.NewRat(1, 1), out, "flat out")
}
