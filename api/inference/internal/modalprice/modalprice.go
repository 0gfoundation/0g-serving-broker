// Package modalprice is the per-modality price shape of a chat endpoint: the
// multiples of its prompt / completion price that image, video and audio input
// tokens, audio output tokens, and the output of a text-only request cost.
// Vendors such as Alibaba price Qwen omni models this way; one flat prompt and
// completion price under-bills media input and audio output and over-bills the
// output of a text-only request.
//
// It is published by the endpoint like cache_token_billing and tiered_pricing:
// pricing.modality_pricing on a broker's /v1/models, the `mod:` tag on a supply
// line. Multipliers are "num/den" strings, e.g. {"input": {"image": "11/6"}}.
//
// This package is the contract the broker's fee and the router's charge must
// both implement, byte for byte — the broker settles with the router on its own
// fee, and the router charges users by this one:
//
//   - Validity (Parse): input keys image / video / audio, each a multiple in
//     [1, 100]; output keys text_only_input in (0, 1] and audio in [1, 100];
//     each "<num>" or "<num>/<den>", positive decimal integers, no sign, no
//     spaces. A shape is also invalid on an endpoint that has cache-token
//     billing (divisor > 1) or tiered pricing. An invalid shape must be refused
//     where it is configured (the broker at config load); the router, seeing
//     one, bills the endpoint flat.
//   - The fee (Factors): see Factors.
//
// Ported from the 0g-router's backend/pkg/modalprice (commit 34270244). Parse,
// ratio and Factors must stay identical to the router's; MaxFactors is
// broker-only (it sizes the on-chain price ceiling).
package modalprice

import (
	"fmt"
	"math/big"
	"regexp"
	"strconv"
	"strings"
)

// Input modality keys and output keys.
const (
	Image         = "image"
	Video         = "video"
	Audio         = "audio"
	TextOnlyInput = "text_only_input"
)

// maxMultiplier caps every multiplier: a typo such as "790/9" must not turn a
// row into a 100x price.
const maxMultiplier = 100

// Pricing is a validated price shape. A nil multiplier means 1x; the zero value
// prices every token flat.
type Pricing struct {
	// Input maps image / video / audio to the multiple of the prompt price an
	// input token of that modality costs (>= 1).
	Input map[string]*big.Rat
	// TextOnlyOutput multiplies the completion price when the request's input
	// carried no image, video or audio token (<= 1).
	TextOnlyOutput *big.Rat
	// AudioOutput multiplies the completion price for audio output tokens; when
	// a response carries any, its text output tokens are not billed (>= 1).
	AudioOutput *big.Rat
}

// Empty reports whether p prices nothing differently from the flat price.
func (p Pricing) Empty() bool {
	return len(p.Input) == 0 && p.TextOnlyOutput == nil && p.AudioOutput == nil
}

// Parse validates the published shape: input keys image / video / audio,
// output keys text_only_input / audio, each a positive "num/den" within
// [1, 100] (text_only_input within (0, 1]).
func Parse(input, output map[string]string) (Pricing, error) {
	var p Pricing
	for k, v := range input {
		if k != Image && k != Video && k != Audio {
			return Pricing{}, fmt.Errorf("modality_pricing.input: unknown modality %q", k)
		}
		r, err := ratio(v, true)
		if err != nil {
			return Pricing{}, fmt.Errorf("modality_pricing.input.%s: %w", k, err)
		}
		if p.Input == nil {
			p.Input = map[string]*big.Rat{}
		}
		p.Input[k] = r
	}
	for k, v := range output {
		switch k {
		case TextOnlyInput:
			r, err := ratio(v, false)
			if err != nil {
				return Pricing{}, fmt.Errorf("modality_pricing.output.%s: %w", k, err)
			}
			p.TextOnlyOutput = r
		case Audio:
			r, err := ratio(v, true)
			if err != nil {
				return Pricing{}, fmt.Errorf("modality_pricing.output.%s: %w", k, err)
			}
			p.AudioOutput = r
		default:
			return Pricing{}, fmt.Errorf("modality_pricing.output: unknown key %q", k)
		}
	}
	return p, nil
}

// ratioPattern is the only accepted spelling: "<num>" or "<num>/<den>".
var ratioPattern = regexp.MustCompile(`^[1-9][0-9]{0,8}(/[1-9][0-9]{0,8})?$`)

// ratio parses "num/den" (or a bare integer) into a positive rational that is
// >= 1 when atLeastOne, else <= 1, and never above maxMultiplier.
func ratio(s string, atLeastOne bool) (*big.Rat, error) {
	if !ratioPattern.MatchString(s) {
		return nil, fmt.Errorf("%q is not <num> or <num>/<den> (positive integers, no spaces)", s)
	}
	num, den, ok := strings.Cut(s, "/")
	if !ok {
		den = "1"
	}
	n, _ := strconv.ParseInt(num, 10, 64)
	d, _ := strconv.ParseInt(den, 10, 64)
	r := big.NewRat(n, d)
	one := big.NewRat(1, 1)
	switch {
	case r.Cmp(big.NewRat(maxMultiplier, 1)) > 0:
		return nil, fmt.Errorf("%q is above %dx", s, maxMultiplier)
	case atLeastOne && r.Cmp(one) < 0:
		return nil, fmt.Errorf("%q is below 1x", s)
	case !atLeastOne && r.Cmp(one) > 0:
		return nil, fmt.Errorf("%q is above 1x", s)
	}
	return r, nil
}

// Published is the wire form, as a broker's /v1/models pricing block and the
// router's providers.pricing column carry it. It is also the broker's yaml
// shape (service-level and per-model modalityPricing).
type Published struct {
	Input  map[string]string `json:"input,omitempty" yaml:"input,omitempty"`
	Output map[string]string `json:"output,omitempty" yaml:"output,omitempty"`
}

// MaxFactors is the largest in / out Factors can return for p: the greatest
// input multiplier (every prompt token a media token of the dearest modality)
// and output.audio (every completion token audio output). Both are >= 1, since
// every input multiplier and output.audio is >= 1 and text_only_input <= 1.
func (p Pricing) MaxFactors() (in, out *big.Rat) {
	in, out = big.NewRat(1, 1), big.NewRat(1, 1)
	for _, r := range p.Input {
		if r.Cmp(in) > 0 {
			in = r
		}
	}
	if p.AudioOutput != nil && p.AudioOutput.Cmp(out) > 0 {
		out = p.AudioOutput
	}
	return new(big.Rat).Set(in), new(big.Rat).Set(out)
}

// Parse validates the wire form; a nil receiver is the flat price.
func (w *Published) Parse() (Pricing, error) {
	if w == nil {
		return Pricing{}, nil
	}
	return Parse(w.Input, w.Output)
}

// Usage is the token counts the fee is computed from, as the upstream reported
// them (OpenAI chat usage).
type Usage struct {
	PromptTokens, CompletionTokens int64
	// From prompt_tokens_details. Split is true when the upstream reported any
	// of text_tokens / image_tokens / video_tokens / audio_tokens (> 0).
	Split                                 bool
	ImageTokens, VideoTokens, AudioTokens int64
	// completion_tokens_details.audio_tokens.
	AudioOutputTokens int64
}

// Factors is the fee: the multiples of the endpoint's prompt price and
// completion price that this usage's prompt_tokens and completion_tokens bill
// at, so fee = prompt_price × PromptTokens × in + completion_price ×
// CompletionTokens × out (nil = 1x; every other part of the fee — cache,
// tiers, which a shape never combines with — is unchanged). Equivalently:
//
// Input. Without a modality split the input bills flat: the upstream's report
// is the only evidence of what was sent. With one, media = image + video +
// audio tokens (scaled down pro rata to PromptTokens if they exceed it),
// text = PromptTokens − media (never text_tokens, which may be absent), and
// the input fee is prompt_price × (text + Σ media_m × input_m), input_m = 1
// for a modality the shape does not list. Cached prompt tokens are prompt
// tokens like any other.
//
// Output. If AudioOutputTokens > 0 and the shape lists output.audio, the
// output fee is completion_price × audio_out × output.audio (audio_out capped
// at CompletionTokens), and every other completion token — text, reasoning —
// is free; text_only_input does not apply. Without output.audio, audio output
// tokens are ordinary completion tokens. Otherwise, if the split shows no
// media (text-only input) and the shape lists text_only_input, every
// completion token bills at completion_price × text_only_input. Otherwise
// flat.
func Factors(p Pricing, u Usage) (in, out *big.Rat) {
	var media int64
	if u.Split {
		// Counted whenever a split is reported, prompt_tokens or not: any media
		// token rules out the text-only discount.
		media = max(u.ImageTokens, 0) + max(u.VideoTokens, 0) + max(u.AudioTokens, 0)
	}
	if u.Split && u.PromptTokens > 0 {
		media = 0
		weighted := new(big.Rat)
		for m, n := range map[string]int64{Image: u.ImageTokens, Video: u.VideoTokens, Audio: u.AudioTokens} {
			if n <= 0 {
				continue
			}
			media += n
			r := p.Input[m]
			if r == nil {
				r = big.NewRat(1, 1)
			}
			weighted.Add(weighted, new(big.Rat).Mul(r, big.NewRat(n, 1)))
		}
		if media > u.PromptTokens {
			weighted.Mul(weighted, big.NewRat(u.PromptTokens, media))
			media = u.PromptTokens
		}
		weighted.Add(weighted, big.NewRat(u.PromptTokens-media, 1))
		if f := weighted.Quo(weighted, big.NewRat(u.PromptTokens, 1)); f.Cmp(big.NewRat(1, 1)) != 0 {
			in = f
		}
	}
	if u.AudioOutputTokens > 0 && p.AudioOutput != nil && u.CompletionTokens > 0 {
		audio := min(u.AudioOutputTokens, u.CompletionTokens)
		return in, new(big.Rat).Mul(p.AudioOutput, big.NewRat(audio, u.CompletionTokens))
	}
	if u.Split && media == 0 && p.TextOnlyOutput != nil {
		out = new(big.Rat).Set(p.TextOnlyOutput)
	}
	return in, out
}
