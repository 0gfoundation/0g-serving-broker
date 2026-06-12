package ctrl

import (
	"context"
	"encoding/json"
	stderrors "errors"
	"testing"
)

// ==========================================================================
// billSpeechToTextByTokens gate
//
// Until issue #530 lands a per-row billing-unit discriminator, the tokens
// path is fail-closed behind cfg.AllowTokenBilledSpeechToText. Operators
// who flip this flag accept the analytics-corruption risk knowingly.
// ==========================================================================

func TestBillSpeechToTextByTokens_GatedByDefault(t *testing.T) {
	c := &Ctrl{allowTokenBilledSTT: false}
	usage := &SpeechToTextUsage{Type: "tokens", InputTokens: 100}
	err := c.billSpeechToTextByTokens(context.Background(), usage, "1", "1", "hash")
	// Sentinel so callers can branch via errors.Is rather than substring-match.
	// Handler paths use this to route gated requests into the word-count fallback
	// (we already streamed the transcription to the user — refusing to bill at
	// all would be free GPU time for the operator).
	if !stderrors.Is(err, ErrTokenBilledSpeechToTextGated) {
		t.Errorf("expected ErrTokenBilledSpeechToTextGated, got: %v", err)
	}
}

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
		// String-encoded numbers — observed from a whisper backend that
		// quotes verbose_json's duration; accept the same shape here so a
		// provider quoting usage.seconds doesn't kill the struct unmarshal.
		{"string seconds", `{"type":"duration","seconds":"207"}`, 207},
		{"string fractional seconds", `{"type":"duration","seconds":"206.34125"}`, 206.34125},
		{"empty string is zero", `{"type":"duration","seconds":""}`, 0},
		{"null is zero", `{"type":"duration","seconds":null}`, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var u SpeechToTextUsage
			if err := json.Unmarshal([]byte(tt.raw), &u); err != nil {
				t.Fatalf("unmarshal %s: %v", tt.raw, err)
			}
			if float64(u.Seconds) != tt.wantSeconds {
				t.Errorf("Seconds = %v, want %v", u.Seconds, tt.wantSeconds)
			}
		})
	}
}

// TestFlexFloat64_RejectsGarbage pins the fail-closed side of flexFloat64:
// a non-numeric or non-finite string must error so the response routes
// through the parse-failure recovery (subtitle timeline → estimator) rather
// than decoding to a bogus value. "+Inf" in particular would otherwise pass
// hasBillableUsage and clamp to the 99-hour cap — a max-fee charge from a
// garbage value.
func TestFlexFloat64_RejectsGarbage(t *testing.T) {
	for _, raw := range []string{
		`{"seconds":"abc"}`,
		`{"seconds":"+Inf"}`,
		`{"seconds":"NaN"}`,
		`{"seconds":true}`,
	} {
		var u SpeechToTextUsage
		if err := json.Unmarshal([]byte(raw), &u); err == nil {
			t.Errorf("unmarshal %s: expected error, got Seconds=%v", raw, u.Seconds)
		}
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
		// 99-hour cap, aligned with the subtitle lane's two-digit hours
		// limit: an anomalous provider-reported duration (usage.seconds or
		// verbose_json's duration field) must not bill an unbounded fee.
		{"at the cap", &SpeechToTextUsage{Seconds: 99 * 3600}, 99 * 3600},
		{"above the cap clamped", &SpeechToTextUsage{Seconds: 1e12}, 99 * 3600},
		// Pre-clamp, float→int of a value beyond int64 range was
		// platform-dependent (negative on amd64 → floored to 1 second, a
		// near-free request instead of an overcharge).
		{"extreme float clamped", &SpeechToTextUsage{Seconds: 1e308}, 99 * 3600},
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
		u := &SpeechToTextUsage{Type: "duration", Seconds: flexFloat64(sec)}
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
// Non-JSON / usage-less response_format recovery
//
// whisper supports response_format=json/verbose_json/text/srt/vtt, but only
// json reliably carries a usage block. Before this recovery existed,
// verbose_json (usage absent on some providers), srt, vtt and text all fell
// through to the word-count fallback, which bills OutputPrice × estimate —
// 0 for whisper services, whose per-second price lives in InputPrice. srt
// and vtt carry a timeline, verbose_json carries a top-level duration field;
// both are recovered into a duration usage. Plain text stays on the
// fallback (no timing signal).
// ==========================================================================

func TestSubtitleDurationSeconds(t *testing.T) {
	tests := []struct {
		name        string
		body        string
		wantSeconds float64
		wantOK      bool
	}{
		{
			"srt two cues",
			"1\r\n00:00:00,000 --> 00:00:04,000\r\nHello there.\r\n\r\n2\r\n00:00:04,500 --> 00:03:27,250\r\nGeneral Kenobi.\r\n",
			207.25, true,
		},
		{
			"vtt with header",
			"WEBVTT\n\n00:00.000 --> 00:04.000\nHello there.\n\n00:04.500 --> 03:27.500\nGeneral Kenobi.\n",
			207.5, true,
		},
		{
			"vtt hour form with cue settings",
			"WEBVTT\n\n01:00:00.000 --> 01:00:05.000 align:start position:0%\nstill going\n",
			3605, true,
		},
		{"plain text", "Hello there. General Kenobi.", 0, false},
		{"empty", "", 0, false},
		{
			// A transcript that merely mentions an arrow must not bill: the
			// text after "-->" is not a parseable timestamp.
			"arrow in prose",
			"the sign said exit --> turn left here",
			0, false,
		},
		{
			// Reviewer probe: prose mentioning a full "T1 --> T2" range must
			// not bill either — the start side is validated as a timestamp
			// at the head of the line, and "skip from 01:02.000" is not one.
			"timestamp range in prose",
			"skip from 01:02.000 --> 03:27.250 in the video",
			0, false,
		},
		{
			// Reviewer probe: the streaming path runs recovery over bodies
			// of JSON chunks whenever no usage arrived; a delta whose text
			// mentions a cue range must not synthesize a charge (the JSON
			// prefix before the arrow fails the start-timestamp check).
			"timestamp range inside JSON delta",
			`{"type":"transcript.text.delta","delta":"see 01:02.500 --> 03:27.250 in the clip"}`,
			0, false,
		},
		{
			// Malformed cue line: parser rejects rather than guessing.
			"garbage timestamp",
			"1\n00:00:00,000 --> not:a:time\nwords\n",
			0, false,
		},
		{
			// Reviewer regression: a trailing prose "-->" line must not void
			// the valid cue before it — max parsed end wins, not last line.
			"prose arrow after valid cue",
			"1\n00:00:00,000 --> 00:03:27,250\nthe sign said exit --> turn left\n",
			207.25, true,
		},
		{
			// Reordered/corrupted tail: max end wins over a later, smaller cue.
			"out-of-order cues take max",
			"1\n00:00:00,000 --> 00:03:27,250\nlate\n\n2\n00:00:00,000 --> 00:01:00,000\nearly\n",
			207.25, true,
		},
		{
			// A single zero-length cue is well-formed but carries no
			// billable duration.
			"zero-length cue not billable",
			"1\n00:00:00,000 --> 00:00:00,000\nblip\n",
			0, false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := subtitleDurationSeconds(tt.body)
			if ok != tt.wantOK || got != tt.wantSeconds {
				t.Errorf("subtitleDurationSeconds() = (%v, %v), want (%v, %v)",
					got, ok, tt.wantSeconds, tt.wantOK)
			}
		})
	}
}

func TestParseSubtitleTimestamp(t *testing.T) {
	tests := []struct {
		name        string
		ts          string
		wantSeconds float64
		wantOK      bool
	}{
		{"srt comma millis", "00:03:27,250", 207.25, true},
		{"vtt dot millis", "00:03:27.250", 207.25, true},
		{"vtt short form", "03:27.500", 207.5, true},
		{"hours", "01:00:05.000", 3605, true},
		// ok reports well-formedness only: cue start times legitimately
		// begin at zero, so the parser accepts it; subtitleDurationSeconds
		// enforces billability (> 0) on the recovered end time.
		{"zero is well-formed", "00:00:00,000", 0, true},
		{"single component", "207", 0, false},
		{"four components", "01:02:03:04", 0, false},
		{"negative component", "00:-1:00", 0, false},
		// strconv.ParseFloat alone accepts all of these; the digit-only
		// component check must reject them so a garbage line containing
		// "-->" cannot produce a bogus (potentially huge) charge.
		{"scientific notation", "12:1e9", 0, false},
		{"explicit plus sign", "00:+1:00", 0, false},
		{"infinite component", "00:00:Inf", 0, false},
		{"nan component", "00:NaN:00", 0, false},
		{"hex prefix", "0x1:00", 0, false},
		// Grammar bounds: minutes/seconds < 60 (hours of HH:MM:SS exempt),
		// fraction only on the seconds component, dot needs digits both sides.
		{"seconds component over 59", "00:00:75,000", 0, false},
		{"minutes component over 59", "00:75:00,000", 0, false},
		{"short form minutes over 59", "75:00.000", 0, false},
		{"hours over 59 allowed", "60:00:00,000", 216000, true},
		{"two digit hours allowed", "99:00:00,000", 356400, true},
		// Hours cap: one corrupted cue line must not be able to bill a fee
		// that drains the user's whole locked balance.
		{"three digit hours rejected", "100:00:00,000", 0, false},
		{"absurd hours rejected", "999999999:00:00,000", 0, false},
		{"fraction on minutes component", "00:1.5:00", 0, false},
		{"bare leading dot", "00:00:.5", 0, false},
		{"bare trailing dot", "00:00:5.", 0, false},
		{"empty component", "00::05", 0, false},
		{"empty", "", 0, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := parseSubtitleTimestamp(tt.ts)
			if ok != tt.wantOK || got != tt.wantSeconds {
				t.Errorf("parseSubtitleTimestamp(%q) = (%v, %v), want (%v, %v)",
					tt.ts, got, ok, tt.wantSeconds, tt.wantOK)
			}
		})
	}
}

// TestEffectiveUsage pins the verbose_json recovery rule: a billable usage
// block always wins; otherwise the top-level duration field is promoted to
// a synthetic duration usage; with neither, the original (possibly nil)
// usage passes through so hasBillableUsage routes to the fallback.
func TestEffectiveUsage(t *testing.T) {
	tests := []struct {
		name string
		resp SpeechToTextResponse
		want *SpeechToTextUsage
	}{
		{
			"billable usage wins over duration field",
			SpeechToTextResponse{
				Duration: 99,
				Usage:    &SpeechToTextUsage{Type: "duration", Seconds: 207},
			},
			&SpeechToTextUsage{Type: "duration", Seconds: 207},
		},
		{
			"token usage wins over duration field",
			SpeechToTextResponse{
				Duration: 99,
				Usage:    &SpeechToTextUsage{Type: "tokens", InputTokens: 149},
			},
			&SpeechToTextUsage{Type: "tokens", InputTokens: 149},
		},
		{
			"verbose_json without usage promotes duration",
			SpeechToTextResponse{Duration: 207.25},
			&SpeechToTextUsage{Type: "duration", Seconds: 207.25},
		},
		{
			"unbillable usage with duration field promotes duration",
			SpeechToTextResponse{Duration: 30, Usage: &SpeechToTextUsage{Type: "duration"}},
			&SpeechToTextUsage{Type: "duration", Seconds: 30},
		},
		{
			"nothing billable passes through",
			SpeechToTextResponse{Text: "hello"},
			nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := effectiveUsage(&tt.resp)
			if tt.want == nil {
				if got != nil {
					t.Fatalf("effectiveUsage() = %+v, want nil", got)
				}
				return
			}
			if got == nil || *got != *tt.want {
				t.Errorf("effectiveUsage() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

// TestSpeechToTextResponse_DecodeVerboseJSON guards the Duration JSON tag.
// verbose_json carries duration at the top level (a float, e.g. 8.47), not
// inside usage — if the tag drifts, verbose_json responses without a usage
// block quietly return to the 0-fee fallback.
func TestSpeechToTextResponse_DecodeVerboseJSON(t *testing.T) {
	raw := []byte(`{
		"task": "transcribe",
		"language": "english",
		"duration": 8.47,
		"text": "Hello there.",
		"segments": [{"id": 0, "start": 0.0, "end": 8.47, "text": "Hello there."}]
	}`)
	var resp SpeechToTextResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal verbose_json response: %v", err)
	}
	if resp.Duration != 8.47 {
		t.Errorf("Duration = %v, want 8.47", resp.Duration)
	}
	if resp.Usage != nil {
		t.Errorf("Usage = %+v, want nil", resp.Usage)
	}
	got := effectiveUsage(&resp)
	if got == nil || !isDurationUsage(got) || got.Seconds != 8.47 {
		t.Errorf("effectiveUsage() = %+v, want duration usage with Seconds=8.47", got)
	}
}

// TestSpeechToTextResponse_DecodeStringDuration covers the originally
// reported provider shape: verbose_json with the top-level duration quoted
// as a string ("206.34125"). Before flexFloat64 this failed the whole
// struct unmarshal and the request fell to the word-count fallback, billing
// 0 for whisper services.
func TestSpeechToTextResponse_DecodeStringDuration(t *testing.T) {
	raw := []byte(`{
		"task": "transcribe",
		"duration": "206.34125",
		"text": "Hello there."
	}`)
	var resp SpeechToTextResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal string-duration response: %v", err)
	}
	if resp.Duration != 206.34125 {
		t.Errorf("Duration = %v, want 206.34125", resp.Duration)
	}
	got := effectiveUsage(&resp)
	if got == nil || !isDurationUsage(got) || got.Seconds != 206.34125 {
		t.Errorf("effectiveUsage() = %+v, want duration usage with Seconds=206.34125", got)
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
