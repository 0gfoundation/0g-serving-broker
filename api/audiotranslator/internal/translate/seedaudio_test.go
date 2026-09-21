package translate

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/0glabs/0g-serving-broker/audiotranslator/internal/seedaudio"
)

func f64(v float64) *float64 { return &v }

// The single most important mapping in the integration. The vendor returns two
// durations that DIVERGE under speed adjustment, and its reference names
// original_duration as the billing figure twice. Reading `duration` instead would
// bill a speed:2.0 request for the clip the listener hears rather than the audio
// the model produced — roughly half.
func TestBillableSecondsPrefersOriginalDuration(t *testing.T) {
	resp := seedaudio.CreateResponse{
		Duration:         json.Number("24"), // what a 2x-speed listener hears
		OriginalDuration: json.Number("48"), // what the model produced — bills
	}
	got, source, ok := BillableSeconds(resp)
	if !ok || got != 48 {
		t.Fatalf("got %v (ok=%v), want 48 — duration must never win over original_duration", got, ok)
	}
	if source != DurationSourceOriginal {
		t.Errorf("source = %q, want %q", source, DurationSourceOriginal)
	}
}

// Falling back to `duration` beats falling through to the broker's reserved
// ceiling, which over-bills by construction.
func TestBillableSecondsFallsBackToDuration(t *testing.T) {
	got, source, ok := BillableSeconds(seedaudio.CreateResponse{Duration: json.Number("12.5")})
	if !ok || got != 12.5 {
		t.Fatalf("got %v (ok=%v), want 12.5", got, ok)
	}
	// The source is what makes this fallback visible. It still populates the
	// duration header, so without it the broker bills normally and every
	// fallback metric on both hops stays green while speed-adjusted requests
	// are under-billed.
	if source != DurationSourcePostProcessed {
		t.Errorf("source = %q, want %q — the caller logs on this value", source, DurationSourcePostProcessed)
	}
	if _, source, ok := BillableSeconds(seedaudio.CreateResponse{}); ok || source != DurationSourceNone {
		t.Errorf("an empty response reported ok=%v source=%q", ok, source)
	}
	for _, bad := range []string{"0", "-5", "abc"} {
		if _, _, ok := BillableSeconds(seedaudio.CreateResponse{OriginalDuration: json.Number(bad), Duration: json.Number(bad)}); ok {
			t.Errorf("%q was accepted as a duration", bad)
		}
	}
}

// OpenAI's speed is a MULTIPLIER (1.0 unchanged); the vendor's speech_rate is a
// percentage OFFSET (0 unchanged). Forwarding the multiplier verbatim would read
// 2.0 as "+2%" — imperceptible where the caller asked for double speed.
func TestSpeedBecomesAPercentageOffset(t *testing.T) {
	tests := []struct {
		name  string
		speed *float64
		want  *int
	}{
		{name: "2x → +100", speed: f64(2.0), want: intp(100)},
		{name: "0.5x → -50", speed: f64(0.5), want: intp(-50)},
		{name: "1.5x → +50", speed: f64(1.5), want: intp(50)},
		{name: "1.0 is unchanged → omitted", speed: f64(1.0), want: nil},
		{name: "absent → omitted", speed: nil, want: nil},
		// Beyond the vendor's range: clamped, not rejected. The vendor caps the
		// effect anyway, and refusing would fail a request it would have served.
		{name: "10x clamps to +100", speed: f64(10), want: intp(100)},
		{name: "0.01x clamps to -50", speed: f64(0.01), want: intp(-50)},
		{name: "zero is nonsense → omitted", speed: f64(0), want: nil},
		{name: "negative is nonsense → omitted", speed: f64(-1), want: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := ToCreateRequest(SpeechRequest{Input: "hi", Speed: tt.speed})
			var got *int
			if out.AudioConfig != nil {
				got = out.AudioConfig.SpeechRate
			}
			switch {
			case tt.want == nil && got != nil:
				t.Errorf("speech_rate = %d, want omitted", *got)
			case tt.want != nil && got == nil:
				t.Errorf("speech_rate omitted, want %d", *tt.want)
			case tt.want != nil && *got != *tt.want:
				t.Errorf("speech_rate = %d, want %d", *got, *tt.want)
			}
		})
	}
}

// `voice` is a REFERENCE entry at the vendor, not a top-level field — and it must
// not be sent alongside audio clips, since each entry takes exactly one of
// audio_url / audio_data / speaker and a cloning request already said which voice
// it wants.
func TestVoiceMapsToASpeakerReference(t *testing.T) {
	out := ToCreateRequest(SpeechRequest{Input: "hi", Voice: "zh_female_01"})
	if len(out.References) != 1 || out.References[0].Speaker != "zh_female_01" {
		t.Fatalf("references = %+v, want one speaker entry", out.References)
	}

	withClips := ToCreateRequest(SpeechRequest{
		Input:          "@Audio1 hi",
		Voice:          "zh_female_01",
		ReferenceAudio: []string{"https://example.com/a.mp3"},
	})
	for _, ref := range withClips.References {
		if ref.Speaker != "" {
			t.Error("speaker was sent alongside an audio reference; the vendor takes exactly one per entry")
		}
	}
}

// A data: URI carries bytes inline and belongs in audio_data; anything else is a
// URL the vendor fetches.
func TestReferenceRoutingByScheme(t *testing.T) {
	out := ToCreateRequest(SpeechRequest{
		Input:          "@Audio1 @Audio2 hi",
		ReferenceAudio: []string{"https://example.com/a.mp3", "data:audio/mp3;base64,AAAA"},
	})
	if out.References[0].AudioURL == "" || out.References[0].AudioData != "" {
		t.Errorf("an https reference did not become audio_url: %+v", out.References[0])
	}
	if out.References[1].AudioData == "" || out.References[1].AudioURL != "" {
		t.Errorf("a data: reference did not become audio_data: %+v", out.References[1])
	}
}

// opus is the one format whose name differs: OpenAI names the codec, the vendor
// names the container. An unknown value passes through so the vendor's own
// validation decides, rather than silently substituting wav and returning audio
// in a format nobody asked for.
func TestFormatMapping(t *testing.T) {
	for in, want := range map[string]string{
		"mp3": "mp3", "wav": "wav", "pcm": "pcm",
		"opus": "ogg_opus", "ogg_opus": "ogg_opus",
		"flac": "flac", "": "",
	} {
		if got := toVendorFormat(in); got != want {
			t.Errorf("toVendorFormat(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		req     SpeechRequest
		wantErr string
	}{
		{name: "minimal", req: SpeechRequest{Input: "hi"}},
		{name: "three clips is the limit", req: SpeechRequest{Input: "hi", ReferenceAudio: []string{
			"https://a", "https://b", "https://c"}}},

		{name: "empty input", req: SpeechRequest{Input: "  "}, wantErr: "input is required"},
		{name: "over the character limit", req: SpeechRequest{Input: strings.Repeat("x", MaxTextPromptChars+1)}, wantErr: "maximum is 3000"},
		{name: "four clips", req: SpeechRequest{Input: "hi", ReferenceAudio: []string{
			"https://a", "https://b", "https://c", "https://d"}}, wantErr: "maximum is 3"},
		// Refused rather than silently dropping one: a caller billed in full for
		// audio in the wrong voice is worse served than one told why.
		{name: "audio and image together", req: SpeechRequest{Input: "hi",
			ReferenceAudio: []string{"https://a"}, ReferenceImage: "https://b"}, wantErr: "cannot be combined"},
		// A vendor-side handle must never be client-addressable — MiniMax's
		// mm_file:// guard exists because that namespace is multi-tenant for us.
		{name: "a file handle scheme", req: SpeechRequest{Input: "hi",
			ReferenceAudio: []string{"mm_file://someone-elses-upload"}}, wantErr: "must be an http(s) URL"},
		{name: "a non-audio data URI", req: SpeechRequest{Input: "hi",
			ReferenceAudio: []string{"data:image/png;base64,AAAA"}}, wantErr: "must be an http(s) URL"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.req.Validate()
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %v, want one containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestDecodeAudio(t *testing.T) {
	want := []byte("ID3\x04rawaudio")
	got, err := DecodeAudio(seedaudio.CreateResponse{Audio: base64.StdEncoding.EncodeToString(want)})
	if err != nil {
		t.Fatalf("DecodeAudio: %v", err)
	}
	if string(got) != string(want) {
		t.Errorf("decoded %q, want %q", got, want)
	}
	if _, err := DecodeAudio(seedaudio.CreateResponse{Audio: "not base64!!"}); err == nil {
		t.Error("invalid base64 was accepted")
	}
	if _, err := DecodeAudio(seedaudio.CreateResponse{Audio: ""}); err == nil {
		t.Error("empty audio was accepted")
	}
}

func intp(v int) *int { return &v }
