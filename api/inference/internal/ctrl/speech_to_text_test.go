package ctrl

import (
	"encoding/json"
	"testing"
)

func TestSpeechBillableInputTokens(t *testing.T) {
	cases := []struct {
		name string
		in   SpeechToTextUsage
		want int64
	}{
		{"top-level input tokens", SpeechToTextUsage{InputTokens: 14}, 14},
		{"audio tokens fallback", SpeechToTextUsage{InputTokenDetails: SpeechToTextTokenDetails{AudioTokens: 30}}, 30},
		{"prefers top-level over audio", SpeechToTextUsage{InputTokens: 14, InputTokenDetails: SpeechToTextTokenDetails{AudioTokens: 30}}, 14},
		{"none", SpeechToTextUsage{}, 0},
	}
	for _, c := range cases {
		if got := c.in.billableInputTokens(); got != c.want {
			t.Errorf("%s: billableInputTokens() = %d, want %d", c.name, got, c.want)
		}
	}
}

func TestSpeechDurationSeconds(t *testing.T) {
	cases := []struct {
		name    string
		seconds string
		want    int64
	}{
		{"integer", `12`, 12},
		{"fractional rounds up", `12.1`, 13},
		{"whole float", `5.0`, 5},
		{"zero", `0`, 0},
		{"absent", ``, 0},
		{"negative ignored", `-3`, 0},
	}
	for _, c := range cases {
		u := SpeechToTextUsage{}
		if c.seconds != "" {
			u.Seconds = json.Number(c.seconds)
		}
		if got := u.durationSeconds(); got != c.want {
			t.Errorf("%s: durationSeconds(%q) = %d, want %d", c.name, c.seconds, got, c.want)
		}
	}
}

// TestSpeechDurationUsageUnmarshal verifies a duration-metered whisper response
// (no token fields) parses into a usage that triggers duration billing, not the
// zero-token path that produced router#350.
func TestSpeechDurationUsageUnmarshal(t *testing.T) {
	var u SpeechToTextUsage
	if err := json.Unmarshal([]byte(`{"type":"duration","seconds":7.5}`), &u); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if u.billableInputTokens() != 0 || u.OutputTokens != 0 {
		t.Fatalf("expected zero token counts, got input=%d output=%d", u.billableInputTokens(), u.OutputTokens)
	}
	if got := u.durationSeconds(); got != 8 {
		t.Errorf("durationSeconds = %d, want 8 (ceil 7.5)", got)
	}
}
