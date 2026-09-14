package ctrl

import (
	"encoding/json"
	"net/http"
	"testing"
)

// The header is the whole billing channel for this modality: OpenAI's
// /v1/audio/speech returns raw audio bytes, and every billing path here reads a
// parsed JSON body, so a quantity has nowhere else to live.
func TestAudioDurationHeaderParsing(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want int64
		ok   bool
	}{
		{name: "whole seconds", raw: "48", want: 48, ok: true},
		// Seed Audio reports original_duration as a float.
		{name: "fractional rounds up", raw: "47.2", want: 48, ok: true},
		{name: "a whole number expressed fractionally", raw: "47.0", want: 47, ok: true},
		{name: "sub-second rounds to one", raw: "0.4", want: 1, ok: true},
		{name: "surrounding whitespace", raw: "  48  ", want: 48, ok: true},

		{name: "absent", raw: "", ok: false},
		{name: "zero", raw: "0", ok: false},
		{name: "negative", raw: "-5", ok: false},
		{name: "not a number", raw: "abc", ok: false},
		{name: "unit suffix", raw: "48s", ok: false},
		{name: "absurd", raw: "1e300", ok: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := ceilPositiveAudioSeconds(json.Number(trimForHeader(tt.raw)))
			if ok != tt.ok {
				t.Fatalf("parsed %q: ok = %v, want %v", tt.raw, ok, tt.ok)
			}
			if ok && got != tt.want {
				t.Errorf("parsed %q = %d, want %d", tt.raw, got, tt.want)
			}
		})
	}
}

// trimForHeader mirrors what resolveAudioSpeechSeconds does to the raw header
// before parsing it, so the table above exercises the real path.
func trimForHeader(s string) string {
	start, end := 0, len(s)
	for start < end && (s[start] == ' ' || s[start] == '\t') {
		start++
	}
	for end > start && (s[end-1] == ' ' || s[end-1] == '\t') {
		end--
	}
	return s[start:end]
}

// The vendor reports two durations and they differ under speed adjustment. Its
// reference names original_duration as the billing figure twice over, so an adaptor
// that populated the header from `duration` would bill a sped-up request for the
// clip the listener hears rather than the audio the model produced. This pins the
// broker's half of that contract: whatever the header says IS the bill.
func TestAudioDurationHeaderIsTakenAtFaceValue(t *testing.T) {
	h := http.Header{}
	h.Set(AudioDurationHeader, "48.0")
	got, ok := ceilPositiveAudioSeconds(json.Number(h.Get(AudioDurationHeader)))
	if !ok || got != 48 {
		t.Fatalf("header 48.0 resolved to %d (ok=%v), want 48", got, ok)
	}

	// A post-processed duration of 24s on a 2x-speed request must NOT be what
	// reaches this function — that is the adaptor's job — but if it does, the broker
	// bills it without second-guessing. The guard against that lives in the adaptor,
	// and this test exists so the split is explicit rather than assumed.
	h.Set(AudioDurationHeader, "24")
	got, ok = ceilPositiveAudioSeconds(json.Number(h.Get(AudioDurationHeader)))
	if !ok || got != 24 {
		t.Fatalf("header 24 resolved to %d (ok=%v); the broker does not reinterpret the header", got, ok)
	}
}

// The header name is part of the adaptor contract. Changing it silently breaks
// billing on every request — the fallback fires, every caller is charged the
// ceiling, and nothing else complains.
func TestAudioDurationHeaderName(t *testing.T) {
	if AudioDurationHeader != "X-0G-Audio-Duration-Seconds" {
		t.Errorf("AudioDurationHeader = %q; changing it breaks billing against every deployed adaptor", AudioDurationHeader)
	}
}
