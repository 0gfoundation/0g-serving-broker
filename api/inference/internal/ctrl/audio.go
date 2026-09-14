package ctrl

import (
	"encoding/json"
	"math"
)

// OpenAI-shaped job status values for an audio-generation create or poll response.
// The vocabulary is deliberately video's, not a second one: the two endpoints have
// the same lifecycle and a reader who knows one should not have to learn another.
const (
	audioStatusQueued     = "queued"
	audioStatusInProgress = "in_progress"
	audioStatusCompleted  = "completed"
	audioStatusFailed     = "failed"
)

// audioBillingAction is what a response's status implies should happen next.
type audioBillingAction int

const (
	// audioActionBillNow covers an explicit "completed" AND the absent/unrecognized
	// case. The latter is how an adaptor that blocks until completion and returns the
	// finished result synchronously looks; treating it as billable keeps that shape
	// working rather than silently deferring a job no poller will ever resolve.
	audioActionBillNow audioBillingAction = iota
	// audioActionDeferToPoll: queued/in_progress — genuinely async, nothing produced
	// yet. Hand it to the background scheduler.
	audioActionDeferToPoll
	// audioActionSkipFailed: nothing was generated, so nothing is billed and the
	// reserve is released.
	audioActionSkipFailed
)

// classifyAudioStatus maps a response's status to the billing action it implies.
// Pure and total: every input, including "", yields a defined action.
func classifyAudioStatus(status string) audioBillingAction {
	switch status {
	case audioStatusFailed:
		return audioActionSkipFailed
	case audioStatusQueued, audioStatusInProgress:
		return audioActionDeferToPoll
	default:
		return audioActionBillNow
	}
}

// audioUsage is the usage block of an audio-generation response.
//
// OutputAudioSeconds is the canonical field and the one the design specifies the
// adaptor emits. It is the VENDOR-REPORTED billable quantity wherever the adaptor
// could obtain one — which matters because the vendor may charge for reference
// media a cloning request supplied, exactly as ByteDance's Seedance bakes
// reference-video duration into usage.completion_tokens. Passing that number
// through is what keeps our charge equal to the invoice; deriving an output-only
// duration ourselves would under-bill precisely the requests that use the feature
// this modality exists for.
type audioUsage struct {
	OutputAudioSeconds json.Number `json:"output_audio_seconds"`
}

// audioDetails is the response's descriptive block. DurationSeconds is a
// SECONDARY source for the billable quantity, not the primary one: it describes
// what was produced, whereas usage describes what is charged for, and those differ
// whenever the vendor bills for input reference media.
type audioDetails struct {
	DurationSeconds json.Number `json:"duration_seconds"`
	Format          string      `json:"format"`
	SampleRate      json.Number `json:"sample_rate"`
}

// audioResponseFields holds the billing-relevant fields of a create or poll
// response. Every snake_case field carries an EXPLICIT json tag: Go's
// case-insensitive unmarshal matching does not cross underscores, so an untagged
// one would silently stay at its zero value — which on this struct means a
// billable quantity of zero.
type audioResponseFields struct {
	ID     string        `json:"id"`
	Status string        `json:"status"`
	Audio  *audioDetails `json:"audio"`
	Usage  *audioUsage   `json:"usage"`
}

// parseAudioResponseFields decodes the billing-relevant fields. A body that does
// not parse yields the zero value rather than an error: every caller's next step
// is to classify a status, and "" classifies as billNow, which is the same answer
// an unparseable body deserves — bill what can be resolved, and fall back loudly
// when nothing can.
func parseAudioResponseFields(body []byte) audioResponseFields {
	var f audioResponseFields
	_ = json.Unmarshal(body, &f)
	return f
}

// audioQuantitySource names where a billable quantity came from, for metrics and
// for the log line on the last-resort path.
type audioQuantitySource string

const (
	// audioQuantityUsage: usage.output_audio_seconds — the adaptor's authoritative
	// number. The expected path.
	audioQuantityUsage audioQuantitySource = "usage"
	// audioQuantityDuration: audio.duration_seconds — describes what was produced
	// rather than what is charged for. Correct whenever the vendor bills output
	// only, which is the common case, but it cannot see a reference-media charge.
	audioQuantityDuration audioQuantitySource = "duration"
	// audioQuantityReserve: neither field was usable, so the held ceiling is
	// charged. OVER-bills by construction and must be metered loudly, never logged
	// at Warn and forgotten.
	audioQuantityReserve audioQuantitySource = "reserve"
)

// maxBillableAudioSeconds bounds a duration this path will accept from a response.
// Far above any vendor's per-request ceiling (Seed Audio's is 120), so it only
// trips on a garbage or hostile value — at which point falling through to the
// reserve is correct, because the reserve is a number we chose.
const maxBillableAudioSeconds = 24 * 3600

// resolveAudioBilling returns the billable output duration in whole seconds, and
// where it came from.
//
// The order is deliberate and is the heart of the modality's billing:
//
//  1. usage.output_audio_seconds — what the vendor says it is charging for.
//  2. audio.duration_seconds — what was produced. A correct answer for a vendor
//     that bills output only; blind to a reference-media charge.
//  3. reservedSeconds — the ceiling the gate held.
//
// Note what is NOT in this chain: reading the duration out of the audio container.
// That happens in the ADAPTOR, which is the only component holding the bytes, and
// it is the adaptor's answer that arrives here as (1). So the `pcm` problem — raw
// PCM has no header, and the request carries sample_rate but neither bit depth nor
// channel count, making a derived duration confidently wrong rather than merely
// absent — lives entirely on that side of the boundary. Here it surfaces only as
// "the adaptor reported nothing", which lands on (3).
//
// Rounded UP at every step: the fee is charged in whole seconds, so truncating
// would bill less than was produced.
func resolveAudioBilling(f audioResponseFields, reservedSeconds int64) (int64, audioQuantitySource) {
	if f.Usage != nil {
		if secs, ok := ceilPositiveAudioSeconds(f.Usage.OutputAudioSeconds); ok {
			return secs, audioQuantityUsage
		}
	}
	if f.Audio != nil {
		if secs, ok := ceilPositiveAudioSeconds(f.Audio.DurationSeconds); ok {
			return secs, audioQuantityDuration
		}
	}
	// A non-positive reserve would bill nothing and read as a free request, which is
	// the one answer that hides the problem instead of surfacing it. Floor at 1.
	if reservedSeconds < 1 {
		return 1, audioQuantityReserve
	}
	return reservedSeconds, audioQuantityReserve
}

// ceilPositiveAudioSeconds reads a json.Number into a positive whole-second count,
// reporting whether it produced a usable one.
//
// json.Number, not float64, for the reason seedance/types.go records: a vendor may
// encode a duration as either an integer or a float, and json.Number tolerates both
// where a typed field rejects one of them outright.
//
// A QUOTED numeric ("48") is accepted, and that is the opposite of what the request
// edge does — rawJSONAudioMaxDuration rejects the same spelling. The two differ
// because the reason for strictness does not apply here.
//
// On the request edge the point is agreement: a downstream struct decode rejects a
// quoted value outright, so reading one here would resolve a duration nobody will
// act on. On this side there is no further parser to agree with — the broker is the
// final consumer of the adaptor's own envelope — so the only question is what to do
// with an unambiguous number wearing the wrong type.
//
// Billing it is the better answer, and the fee direction settles it: accepting
// charges for the 48 seconds actually produced, while falling through would charge
// the reserved ceiling. Strictness here would OVER-bill a request whose duration we
// plainly know, to punish a formatting slip by our own sidecar.
func ceilPositiveAudioSeconds(n json.Number) (int64, bool) {
	if n == "" {
		return 0, false
	}
	f, err := n.Float64()
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, false
	}
	if f <= 0 || f > maxBillableAudioSeconds {
		return 0, false
	}
	return int64(math.Ceil(f)), true
}
