package ctrl

import (
	"testing"
)

func TestClassifyAudioStatus(t *testing.T) {
	tests := []struct {
		status string
		want   audioBillingAction
	}{
		{status: "completed", want: audioActionBillNow},
		{status: "queued", want: audioActionDeferToPoll},
		{status: "in_progress", want: audioActionDeferToPoll},
		{status: "failed", want: audioActionSkipFailed},

		// An absent or unrecognized status bills NOW. That is how an adaptor which
		// blocks until completion and returns the finished result synchronously looks;
		// deferring it instead would hand the scheduler a job no poll will ever
		// resolve, and the request would sit on its reserve until it timed out.
		{status: "", want: audioActionBillNow},
		{status: "succeeded", want: audioActionBillNow},
		{status: "COMPLETED", want: audioActionBillNow},
	}

	for _, tt := range tests {
		t.Run("status="+tt.status, func(t *testing.T) {
			if got := classifyAudioStatus(tt.status); got != tt.want {
				t.Errorf("classifyAudioStatus(%q) = %v, want %v", tt.status, got, tt.want)
			}
		})
	}
}

func TestResolveAudioBillingSourceOrder(t *testing.T) {
	const reserve = 120

	tests := []struct {
		name       string
		body       string
		wantSecs   int64
		wantSource audioQuantitySource
	}{
		{
			name:       "usage wins",
			body:       `{"status":"completed","usage":{"output_audio_seconds":48},"audio":{"duration_seconds":47.2}}`,
			wantSecs:   48,
			wantSource: audioQuantityUsage,
		},
		{
			// The whole point of preferring usage: it is what the vendor CHARGES for,
			// and that exceeds what was produced whenever reference media is billed.
			// Seedance's usage.completion_tokens behaves exactly this way.
			name:       "usage wins even when it exceeds the produced duration",
			body:       `{"status":"completed","usage":{"output_audio_seconds":90},"audio":{"duration_seconds":47}}`,
			wantSecs:   90,
			wantSource: audioQuantityUsage,
		},
		{
			name:       "falls back to duration when usage is absent",
			body:       `{"status":"completed","audio":{"duration_seconds":47.2}}`,
			wantSecs:   48,
			wantSource: audioQuantityDuration,
		},
		{
			name:       "falls back to duration when usage is present but unusable",
			body:       `{"status":"completed","usage":{"output_audio_seconds":0},"audio":{"duration_seconds":12}}`,
			wantSecs:   12,
			wantSource: audioQuantityDuration,
		},
		{
			name:       "falls back to the reserve when neither is usable",
			body:       `{"status":"completed"}`,
			wantSecs:   reserve,
			wantSource: audioQuantityReserve,
		},
		{
			// The adaptor could not measure a `pcm` body and reported nothing. Over-bills
			// deliberately — the alternative is a confidently wrong number.
			name:       "reserve covers a response with an empty usage block",
			body:       `{"status":"completed","usage":{},"audio":{}}`,
			wantSecs:   reserve,
			wantSource: audioQuantityReserve,
		},
		{
			name:       "an unparseable body falls back to the reserve",
			body:       `not json`,
			wantSecs:   reserve,
			wantSource: audioQuantityReserve,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			secs, source := resolveAudioBilling(parseAudioResponseFields([]byte(tt.body)), reserve)
			if secs != tt.wantSecs {
				t.Errorf("seconds = %d, want %d", secs, tt.wantSecs)
			}
			if source != tt.wantSource {
				t.Errorf("source = %q, want %q", source, tt.wantSource)
			}
		})
	}
}

// Rounded UP everywhere: the fee is charged in whole seconds, so truncating bills
// less than was produced.
func TestResolveAudioBillingRoundsUp(t *testing.T) {
	tests := []struct {
		raw  string
		want int64
	}{
		{raw: "47.2", want: 48},
		{raw: "47.0", want: 47},
		{raw: "0.1", want: 1},
		{raw: "1", want: 1},
		{raw: "0.0001", want: 1},
	}
	for _, tt := range tests {
		t.Run(tt.raw, func(t *testing.T) {
			body := `{"status":"completed","usage":{"output_audio_seconds":` + tt.raw + `}}`
			secs, source := resolveAudioBilling(parseAudioResponseFields([]byte(body)), 120)
			if source != audioQuantityUsage {
				t.Fatalf("source = %q, want usage", source)
			}
			if secs != tt.want {
				t.Errorf("seconds = %d, want %d", secs, tt.want)
			}
		})
	}
}

// Values a hostile or broken upstream could send must fall through to the reserve —
// a number we chose — rather than being billed.
func TestResolveAudioBillingRejectsImplausibleValues(t *testing.T) {
	for _, raw := range []string{
		"-5",
		"0",
		"1e300",
		strconvItoa(maxBillableAudioSeconds + 1),
	} {
		t.Run(raw, func(t *testing.T) {
			body := `{"status":"completed","usage":{"output_audio_seconds":` + raw + `}}`
			secs, source := resolveAudioBilling(parseAudioResponseFields([]byte(body)), 120)
			if source != audioQuantityReserve {
				t.Errorf("source = %q, want reserve — %s must not be billed", source, raw)
			}
			if secs != 120 {
				t.Errorf("seconds = %d, want the reserve 120", secs)
			}
		})
	}
}

// A QUOTED numeric IS billed, deliberately — the opposite of what the request edge
// does with the same spelling.
//
// On the request edge strictness buys agreement: a downstream struct decode rejects
// a quoted value, so reading one there would resolve a duration nobody acts on. Here
// the broker is the final consumer of the adaptor's own envelope, so there is no
// parser left to disagree with, and the fee direction settles it — billing the 48
// seconds actually produced beats charging the reserved ceiling to punish a
// formatting slip by our own sidecar.
func TestResolveAudioBillingAcceptsAQuotedNumeric(t *testing.T) {
	secs, source := resolveAudioBilling(parseAudioResponseFields([]byte(`{"usage":{"output_audio_seconds":"48"}}`)), 120)
	if source != audioQuantityUsage || secs != 48 {
		t.Errorf("got %d seconds from %q, want 48 from usage — over-billing the reserve here would punish a formatting slip", secs, source)
	}
}

// A zero or negative reserve would bill nothing and read as a free request — the one
// answer that hides the problem rather than surfacing it.
func TestResolveAudioBillingNeverReturnsZero(t *testing.T) {
	for _, reserve := range []int64{0, -1, -120} {
		secs, source := resolveAudioBilling(parseAudioResponseFields([]byte(`{"status":"completed"}`)), reserve)
		if secs < 1 {
			t.Errorf("reserve %d produced %d seconds; the floor must be 1", reserve, secs)
		}
		if source != audioQuantityReserve {
			t.Errorf("source = %q, want reserve", source)
		}
	}
}

// Go's case-insensitive unmarshal matching does not cross underscores, so an
// untagged snake_case field silently stays at its zero value — which on this struct
// means a billable quantity of zero. seedance/types.go records catching exactly this
// on its usage block, i.e. on the money path.
func TestAudioResponseFieldsHaveExplicitTags(t *testing.T) {
	f := parseAudioResponseFields([]byte(`{
		"id":"v0_abc",
		"status":"completed",
		"usage":{"output_audio_seconds":48},
		"audio":{"duration_seconds":47.2,"format":"mp3","sample_rate":24000}
	}`))

	if f.ID != "v0_abc" || f.Status != "completed" {
		t.Errorf("id/status did not decode: %+v", f)
	}
	if f.Usage == nil || f.Usage.OutputAudioSeconds.String() != "48" {
		t.Errorf("usage.output_audio_seconds did not decode: %+v", f.Usage)
	}
	if f.Audio == nil {
		t.Fatalf("audio block did not decode")
	}
	if f.Audio.DurationSeconds.String() != "47.2" {
		t.Errorf("audio.duration_seconds = %q, want 47.2", f.Audio.DurationSeconds)
	}
	if f.Audio.SampleRate.String() != "24000" {
		t.Errorf("audio.sample_rate = %q, want 24000", f.Audio.SampleRate)
	}
	if f.Audio.Format != "mp3" {
		t.Errorf("audio.format = %q, want mp3", f.Audio.Format)
	}
}

// A vendor may encode a duration as an integer or a float; json.Number tolerates
// both where a typed field would reject one outright.
func TestAudioSecondsAcceptsIntegerAndFloatEncodings(t *testing.T) {
	for _, body := range []string{
		`{"usage":{"output_audio_seconds":48}}`,
		`{"usage":{"output_audio_seconds":48.0}}`,
	} {
		secs, source := resolveAudioBilling(parseAudioResponseFields([]byte(body)), 120)
		if source != audioQuantityUsage || secs != 48 {
			t.Errorf("%s → %d seconds from %q, want 48 from usage", body, secs, source)
		}
	}
}

func strconvItoa(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf []byte
	for n > 0 {
		buf = append([]byte{byte('0' + n%10)}, buf...)
		n /= 10
	}
	if neg {
		return "-" + string(buf)
	}
	return string(buf)
}
