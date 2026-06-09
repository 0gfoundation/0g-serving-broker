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
		t.Errorf("Seconds = %v, want 207", u.Seconds)
	}
	if u.InputTokens != 0 || u.OutputTokens != 0 {
		t.Errorf("token counts = (%d, %d), want (0, 0)", u.InputTokens, u.OutputTokens)
	}
}

// TestSpeechToTextUsage_DecodeFractionalSeconds is a regression guard.
// The original struct typed Seconds as int, so Go's encoding/json rejected
// any JSON number with a decimal point ("207.5" or even "207.0") and the
// whole response failed to decode, sending the request down the word-count
// fallback path — which silently billed 0 for whisper services (OutputPrice
// is typically 0). Switching to float64 lets us accept either shape and
// round at the bill site.
func TestSpeechToTextUsage_DecodeFractionalSeconds(t *testing.T) {
	tests := []struct {
		name        string
		raw         string
		wantSeconds float64
	}{
		{"integer seconds", `{"type":"duration","seconds":207}`, 207},
		{"fractional seconds", `{"type":"duration","seconds":207.5}`, 207.5},
		// Python's json.dumps(207.0) emits this — Go's int field rejected it.
		{"whole-number float", `{"type":"duration","seconds":207.0}`, 207},
		{"sub-second", `{"type":"duration","seconds":0.4}`, 0.4},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var u SpeechToTextUsage
			if err := json.Unmarshal([]byte(tt.raw), &u); err != nil {
				t.Fatalf("unmarshal %s: %v", tt.raw, err)
			}
			if u.Seconds != tt.wantSeconds {
				t.Errorf("Seconds = %v, want %v", u.Seconds, tt.wantSeconds)
			}
		})
	}
}

// TestBillableSeconds covers the round-to-nearest-second helper used by
// every billing/metric/limiting call site. Round-to-nearest matches OpenAI's
// "billed to the nearest second" semantic, with a 1-second floor for any
// positive input — without that floor, a 0.4s clip would pass
// hasBillableUsage (Seconds > 0) and then bill 0, recreating the zero-fee
// bug class this PR exists to fix.
func TestBillableSeconds(t *testing.T) {
	tests := []struct {
		name  string
		usage *SpeechToTextUsage
		want  int
	}{
		{"nil", nil, 0},
		{"zero", &SpeechToTextUsage{Seconds: 0}, 0},
		{"negative clamped to zero", &SpeechToTextUsage{Seconds: -5}, 0},
		{"whole second", &SpeechToTextUsage{Seconds: 207}, 207},
		{"rounds down", &SpeechToTextUsage{Seconds: 207.4}, 207},
		{"rounds up", &SpeechToTextUsage{Seconds: 207.5}, 208},
		// Floor-positive — anything > 0 bills at least 1 second so the
		// admission gate and the math agree on billability.
		{"sub-half-second floored to 1", &SpeechToTextUsage{Seconds: 0.4}, 1},
		{"barely above zero floored to 1", &SpeechToTextUsage{Seconds: 0.001}, 1},
		{"half-second rounds to 1", &SpeechToTextUsage{Seconds: 0.5}, 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := billableSeconds(tt.usage); got != tt.want {
				t.Errorf("billableSeconds(%+v) = %d, want %d", tt.usage, got, tt.want)
			}
		})
	}
}

// TestBillableSeconds_NeverZeroWhenGateAdmitsDuration is the structural
// guard against the regression class this PR exists to fix. The admission
// gate (hasBillableUsage) and the math (billableSeconds) must agree on
// billability: if the gate says "yes, this is billable" for a duration
// usage, the math must produce a non-zero second count, or else we silently
// write a fee=0 row through the accurate-billing path.
//
// The reviewer flagged 0 < seconds < 0.5 as a hole: the gate passes (any
// Seconds > 0), but math.Round produces 0 without the floor.
func TestBillableSeconds_NeverZeroWhenGateAdmitsDuration(t *testing.T) {
	// Span the gap the reviewer flagged and the boundary just above zero.
	subSecondInputs := []float64{0.001, 0.1, 0.4, 0.49, 0.5, 0.51, 0.9, 1.0}
	for _, sec := range subSecondInputs {
		u := &SpeechToTextUsage{Type: "duration", Seconds: sec}
		if !hasBillableUsage(u) {
			t.Fatalf("hasBillableUsage(seconds=%v) = false, want true (regression in admission gate)", sec)
		}
		if !isDurationUsage(u) {
			t.Fatalf("isDurationUsage(seconds=%v) = false, want true", sec)
		}
		if got := billableSeconds(u); got <= 0 {
			t.Errorf("billableSeconds(seconds=%v) = %d, want > 0 (gate admitted but math zero-bills)", sec, got)
		}
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
// classifyUsageForMetrics
//
// The whitelist metrics lane. Must classify identically to the billing
// dispatch — divergence is exactly the bug PR #523's review caught. The
// "agrees with isDurationUsage" test is the structural guarantee.
// Duration-billed usage must surface in the seconds slot, token-billed
// usage in the token slots. Never both.
// ==========================================================================

func TestClassifyUsageForMetrics(t *testing.T) {
	tests := []struct {
		name        string
		usage       *SpeechToTextUsage
		wantSeconds int64
		wantIn      int64
		wantOut     int64
	}{
		{"nil", nil, 0, 0, 0},
		{"empty", &SpeechToTextUsage{}, 0, 0, 0},
		{
			"duration mode routes to seconds slot",
			&SpeechToTextUsage{Type: "duration", Seconds: 207},
			207, 0, 0,
		},
		{
			"tokens mode routes to token slots",
			&SpeechToTextUsage{Type: "tokens", InputTokens: 149, OutputTokens: 0},
			0, 149, 0,
		},
		{
			"tokens mode with output > 0",
			&SpeechToTextUsage{Type: "tokens", InputTokens: 100, OutputTokens: 50},
			0, 100, 50,
		},
		{
			// Mid-review regression: a {seconds:N} response without an
			// explicit type discriminator must land in the seconds slot,
			// not silently zero out as (0,0,0).
			"missing type with seconds routes to seconds",
			&SpeechToTextUsage{Seconds: 30},
			30, 0, 0,
		},
		{
			"missing type with tokens routes to token slots",
			&SpeechToTextUsage{InputTokens: 50, OutputTokens: 10},
			0, 50, 10,
		},
		{
			"mismatched shape blocked at gate",
			// type=duration + seconds=0 + input_tokens populated.
			// hasBillableUsage returns false (strict-by-type), so the
			// classifier emits (0,0,0) and the recorder no-ops.
			&SpeechToTextUsage{Type: "duration", InputTokens: 100},
			0, 0, 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotSeconds, gotIn, gotOut := classifyUsageForMetrics(tt.usage)
			if gotSeconds != tt.wantSeconds || gotIn != tt.wantIn || gotOut != tt.wantOut {
				t.Errorf("classifyUsageForMetrics() = (s=%d, in=%d, out=%d), want (s=%d, in=%d, out=%d)",
					gotSeconds, gotIn, gotOut, tt.wantSeconds, tt.wantIn, tt.wantOut)
			}
		})
	}
}

// TestClassifyUsageForMetrics_AgreesWithIsDurationUsage is the structural
// guard against the dispatch/metrics divergence that motivated PR #523's
// review fix. For every input where isDurationUsage returns true AND the
// usage is billable, the seconds slot must carry the value and both token
// slots must be zero. The inverse holds for tokens mode. No input may set
// both seconds AND token slots — that would double-count.
func TestClassifyUsageForMetrics_AgreesWithIsDurationUsage(t *testing.T) {
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
		seconds, in, out := classifyUsageForMetrics(u)
		if !hasBillableUsage(u) {
			if seconds != 0 || in != 0 || out != 0 {
				t.Errorf("non-billable %+v: classified = (s=%d, in=%d, out=%d), want all-zero", u, seconds, in, out)
			}
			continue
		}
		if seconds > 0 && (in > 0 || out > 0) {
			t.Errorf("double-counted %+v: classified = (s=%d, in=%d, out=%d) populates both lanes", u, seconds, in, out)
		}
		if isDurationUsage(u) {
			wantSeconds := int64(billableSeconds(u))
			if seconds != wantSeconds || in != 0 || out != 0 {
				t.Errorf("duration-classified %+v: got (s=%d, in=%d, out=%d), want (s=%d, 0, 0)",
					u, seconds, in, out, wantSeconds)
			}
		} else {
			if in != int64(u.InputTokens) || out != int64(u.OutputTokens) || seconds != 0 {
				t.Errorf("tokens-classified %+v: got (s=%d, in=%d, out=%d), want (0, %d, %d)",
					u, seconds, in, out, u.InputTokens, u.OutputTokens)
			}
		}
	}
}

// ==========================================================================
// calcDurationFee / calcTokenFees
//
// Pure fee arithmetic, extracted from the bill helpers so we can pin the
// math without spinning up a DB. The bill helpers are now thin glue:
// classifier → math → c.db.Update + monitor + limiter, where every step
// except the DB call is unit-tested here.
// ==========================================================================

func TestCalcDurationFee(t *testing.T) {
	tests := []struct {
		name       string
		inputPrice string
		seconds    int
		want       string
	}{
		{"normal", "100", 207, "20700"},
		{"zero seconds", "100", 0, "0"},
		{"zero price", "0", 207, "0"},
		// Real-world-shaped: per-second price from the chain has 18-decimal
		// fixed-point representation. Verify big.Int multiplication handles
		// the wide range without precision loss.
		{"big price", "70000000000", 207, "14490000000000"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := calcDurationFee(tt.inputPrice, tt.seconds)
			if err != nil {
				t.Fatalf("calcDurationFee: %v", err)
			}
			if got != tt.want {
				t.Errorf("calcDurationFee(%q, %d) = %q, want %q",
					tt.inputPrice, tt.seconds, got, tt.want)
			}
		})
	}
}

func TestCalcDurationFee_BadPrice(t *testing.T) {
	if _, err := calcDurationFee("not-a-number", 100); err == nil {
		t.Error("expected error for non-numeric price")
	}
}

func TestCalcTokenFees(t *testing.T) {
	tests := []struct {
		name        string
		inputPrice  string
		outputPrice string
		inputTok    int
		outputTok   int
		wantIn      string
		wantOut     string
		wantTotal   string
	}{
		{
			name:        "input only (gpt-4o-transcribe shape)",
			inputPrice:  "100",
			outputPrice: "200",
			inputTok:    149,
			outputTok:   0,
			wantIn:      "14900",
			wantOut:     "0",
			wantTotal:   "14900",
		},
		{
			name:        "input and output",
			inputPrice:  "100",
			outputPrice: "200",
			inputTok:    10,
			outputTok:   20,
			wantIn:      "1000",
			wantOut:     "4000",
			wantTotal:   "5000",
		},
		{
			name:        "all zero counts",
			inputPrice:  "100",
			outputPrice: "200",
			inputTok:    0,
			outputTok:   0,
			wantIn:      "0",
			wantOut:     "0",
			wantTotal:   "0",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotIn, gotOut, gotTotal, err := calcTokenFees(tt.inputPrice, tt.outputPrice, tt.inputTok, tt.outputTok)
			if err != nil {
				t.Fatalf("calcTokenFees: %v", err)
			}
			if gotIn != tt.wantIn || gotOut != tt.wantOut || gotTotal != tt.wantTotal {
				t.Errorf("calcTokenFees() = (%q, %q, %q), want (%q, %q, %q)",
					gotIn, gotOut, gotTotal, tt.wantIn, tt.wantOut, tt.wantTotal)
			}
		})
	}
}

func TestCalcTokenFees_BadPrice(t *testing.T) {
	if _, _, _, err := calcTokenFees("bad", "200", 10, 20); err == nil {
		t.Error("expected error for non-numeric input price")
	}
	if _, _, _, err := calcTokenFees("100", "bad", 10, 20); err == nil {
		t.Error("expected error for non-numeric output price")
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
