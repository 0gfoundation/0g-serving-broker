package ctrl

import (
	"encoding/json"
	"testing"
)

// ==========================================================================
// SpeechToTextUsage JSON decoding
//
// Both upstream usage shapes must round-trip cleanly so the dispatch sees
// real data — if the struct silently drops a field, the downstream
// classifier degenerates into the zero-fee bug PR #523 fixed.
// ==========================================================================

func TestSpeechToTextUsage_DecodeWhisperShape(t *testing.T) {
	// What whisper-1 / whisper-large-v3 returns. The fact this is the
	// shape that triggered the original zero-fee bug is why this test
	// exists — if the JSON tag on Seconds ever drifts, billing goes
	// silent again.
	raw := []byte(`{"type":"duration","seconds":207}`)
	var u SpeechToTextUsage
	if err := json.Unmarshal(raw, &u); err != nil {
		t.Fatalf("unmarshal whisper usage: %v", err)
	}
	if u.Type != "duration" {
		t.Errorf("Type = %q, want %q", u.Type, "duration")
	}
	if u.Seconds != 207 {
		t.Errorf("Seconds = %d, want 207", u.Seconds)
	}
	if u.InputTokens != 0 || u.OutputTokens != 0 {
		t.Errorf("token counts = (%d, %d), want (0, 0)", u.InputTokens, u.OutputTokens)
	}
}

func TestSpeechToTextUsage_DecodeGPT4OTranscribeShape(t *testing.T) {
	raw := []byte(`{
		"type":"tokens",
		"total_tokens":149,
		"input_tokens":149,
		"input_token_details":{"text_tokens":6,"audio_tokens":143},
		"output_tokens":0
	}`)
	var u SpeechToTextUsage
	if err := json.Unmarshal(raw, &u); err != nil {
		t.Fatalf("unmarshal gpt-4o-transcribe usage: %v", err)
	}
	if u.Type != "tokens" {
		t.Errorf("Type = %q, want %q", u.Type, "tokens")
	}
	if u.InputTokens != 149 {
		t.Errorf("InputTokens = %d, want 149", u.InputTokens)
	}
	if u.OutputTokens != 0 {
		t.Errorf("OutputTokens = %d, want 0", u.OutputTokens)
	}
	if u.InputTokenDetails.AudioTokens != 143 {
		t.Errorf("AudioTokens = %d, want 143", u.InputTokenDetails.AudioTokens)
	}
	if u.InputTokenDetails.TextTokens != 6 {
		t.Errorf("TextTokens = %d, want 6", u.InputTokenDetails.TextTokens)
	}
}

// ==========================================================================
// isDurationUsage
//
// Single source of truth for dispatch. Every row here represents a real
// or plausible upstream response shape — losing any of them means a
// regression in billing accuracy.
// ==========================================================================

func TestIsDurationUsage(t *testing.T) {
	tests := []struct {
		name  string
		usage *SpeechToTextUsage
		want  bool
	}{
		{"nil", nil, false},
		{"empty", &SpeechToTextUsage{}, false},
		{
			"explicit duration",
			&SpeechToTextUsage{Type: "duration", Seconds: 207},
			true,
		},
		{
			"explicit duration with zero seconds",
			&SpeechToTextUsage{Type: "duration", Seconds: 0},
			true, // type wins; hasBillableUsage filters the zero case
		},
		{
			"explicit tokens",
			&SpeechToTextUsage{Type: "tokens", InputTokens: 100},
			false,
		},
		{
			// The regression case fixed mid-review of PR #523: a
			// non-conforming provider that omits the type field but
			// populates seconds must still be classified as duration.
			"missing type with seconds",
			&SpeechToTextUsage{Seconds: 30},
			true,
		},
		{
			"missing type with only input tokens",
			&SpeechToTextUsage{InputTokens: 50},
			false,
		},
		{
			// Hypothetical mixed response: explicit type="tokens" must
			// override any seconds field. Trust the discriminator.
			"explicit tokens overrides stray seconds",
			&SpeechToTextUsage{Type: "tokens", Seconds: 50, InputTokens: 100},
			false,
		},
		{
			"unknown type with seconds falls to duration",
			&SpeechToTextUsage{Type: "future-mode", Seconds: 10},
			true,
		},
		{
			"unknown type with tokens falls to tokens",
			&SpeechToTextUsage{Type: "future-mode", InputTokens: 10},
			false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isDurationUsage(tt.usage); got != tt.want {
				t.Errorf("isDurationUsage() = %v, want %v", got, tt.want)
			}
		})
	}
}

// ==========================================================================
// hasBillableUsage
//
// Admission gate for the accurate-billing path. Anything that returns
// false here falls through to the word-count fallback, so getting this
// wrong either bills zero (false negative) or feeds zero counters into
// the billing helpers (false positive).
// ==========================================================================

func TestHasBillableUsage(t *testing.T) {
	tests := []struct {
		name  string
		usage *SpeechToTextUsage
		want  bool
	}{
		{"nil", nil, false},
		{"empty", &SpeechToTextUsage{}, false},
		{"empty duration", &SpeechToTextUsage{Type: "duration"}, false},
		{"valid duration", &SpeechToTextUsage{Type: "duration", Seconds: 1}, true},
		{"empty tokens", &SpeechToTextUsage{Type: "tokens"}, false},
		{
			// gpt-4o-transcribe — output_tokens always 0; input_tokens
			// alone is enough to bill.
			"tokens with input only",
			&SpeechToTextUsage{Type: "tokens", InputTokens: 100},
			true,
		},
		{
			"tokens with output only",
			&SpeechToTextUsage{Type: "tokens", OutputTokens: 50},
			true,
		},
		{
			"missing type, seconds populated",
			&SpeechToTextUsage{Seconds: 30},
			true,
		},
		{
			"missing type, tokens populated",
			&SpeechToTextUsage{InputTokens: 30},
			true,
		},
		{
			"missing type, all zero",
			&SpeechToTextUsage{},
			false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := hasBillableUsage(tt.usage); got != tt.want {
				t.Errorf("hasBillableUsage() = %v, want %v", got, tt.want)
			}
		})
	}
}

// ==========================================================================
// usageMetricCounts
//
// The whitelist metrics lane. Must classify identically to the billing
// dispatch — divergence is exactly the bug flagged in review. The
// "agrees with isDurationUsage" test is the structural guarantee.
// ==========================================================================

func TestUsageMetricCounts(t *testing.T) {
	tests := []struct {
		name      string
		usage     *SpeechToTextUsage
		wantIn    int64
		wantOut   int64
	}{
		{"nil", nil, 0, 0},
		{
			"duration mode reports seconds in input slot",
			&SpeechToTextUsage{Type: "duration", Seconds: 207},
			207, 0,
		},
		{
			"tokens mode reports input/output tokens",
			&SpeechToTextUsage{Type: "tokens", InputTokens: 149, OutputTokens: 0},
			149, 0,
		},
		{
			// Regression for the review finding: missing-type +
			// seconds-only must report seconds, matching the billing
			// dispatch instead of falling to the tokens shape (0, 0).
			"missing type with seconds reports as duration",
			&SpeechToTextUsage{Seconds: 30},
			30, 0,
		},
		{
			"missing type with tokens reports as tokens",
			&SpeechToTextUsage{InputTokens: 50, OutputTokens: 10},
			50, 10,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotIn, gotOut := usageMetricCounts(tt.usage)
			if gotIn != tt.wantIn || gotOut != tt.wantOut {
				t.Errorf("usageMetricCounts() = (%d, %d), want (%d, %d)",
					gotIn, gotOut, tt.wantIn, tt.wantOut)
			}
		})
	}
}

// TestUsageMetricCounts_AgreesWithIsDurationUsage is the structural guard
// against the dispatch/metrics divergence that motivated PR #523's review
// fix. For every input where isDurationUsage returns true, the metric
// helper must populate the input slot with seconds (and zero the output
// slot). For tokens-mode inputs it must populate the token counters.
func TestUsageMetricCounts_AgreesWithIsDurationUsage(t *testing.T) {
	usages := []*SpeechToTextUsage{
		{Type: "duration", Seconds: 100},
		{Type: "tokens", InputTokens: 100, OutputTokens: 0},
		{Type: "tokens", InputTokens: 100, OutputTokens: 50},
		{Seconds: 30},
		{InputTokens: 50},
		{Type: "future-mode", Seconds: 5},
		{Type: "future-mode", InputTokens: 5},
	}

	for _, u := range usages {
		gotIn, gotOut := usageMetricCounts(u)
		if isDurationUsage(u) {
			if gotIn != int64(u.Seconds) || gotOut != 0 {
				t.Errorf("duration-classified %+v: metrics = (%d, %d), want (%d, 0)",
					u, gotIn, gotOut, u.Seconds)
			}
		} else {
			if gotIn != int64(u.InputTokens) || gotOut != int64(u.OutputTokens) {
				t.Errorf("tokens-classified %+v: metrics = (%d, %d), want (%d, %d)",
					u, gotIn, gotOut, u.InputTokens, u.OutputTokens)
			}
		}
	}
}

// ==========================================================================
// isSpeechToTextStream
//
// Driven entirely off the raw multipart request body — must not confuse
// a stream=true form field with stray bytes inside a file part.
// ==========================================================================

func TestIsSpeechToTextStream(t *testing.T) {
	ctrl := &Ctrl{logger: testLogger()}

	tests := []struct {
		name string
		body string
		want bool
	}{
		{
			"stream true",
			"--boundary\r\nContent-Disposition: form-data; name=\"stream\"\r\n\r\ntrue\r\n--boundary--",
			true,
		},
		{
			"no stream field",
			"--boundary\r\nContent-Disposition: form-data; name=\"file\"\r\n\r\nbytes\r\n--boundary--",
			false,
		},
		{
			"stream false",
			"--boundary\r\nContent-Disposition: form-data; name=\"stream\"\r\n\r\nfalse\r\n--boundary--",
			false,
		},
		{
			"empty body",
			"",
			false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ctrl.isSpeechToTextStream([]byte(tt.body)); got != tt.want {
				t.Errorf("isSpeechToTextStream() = %v, want %v", got, tt.want)
			}
		})
	}
}
