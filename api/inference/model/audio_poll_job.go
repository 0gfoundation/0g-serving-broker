package model

import "time"

// AudioPollStatus represents the lifecycle state of an AudioPollJob. The values
// mirror VideoPollStatus one-for-one, deliberately: the scheduler mechanics are
// the same, and a reader who knows one should not have to learn a second
// vocabulary to follow the other.
type AudioPollStatus string

const (
	// AudioPollStatusPending is claimable by the next scheduler scan once NextPollAt
	// elapses.
	AudioPollStatusPending AudioPollStatus = "pending"
	// AudioPollStatusPolling is a claimed row awaiting one poll round-trip. A row
	// stuck in this state past its (lease-extended) NextPollAt is claimable again —
	// the lease IS the crash recovery, so no separate recovery pass exists.
	AudioPollStatusPolling AudioPollStatus = "polling"
	// AudioPollStatusCompleted means the provider reported a terminal "completed"
	// status and the actual fee has been written to the linked Request row.
	AudioPollStatusCompleted AudioPollStatus = "completed"
	// AudioPollStatusFailed means the provider reported a terminal "failed" status.
	// Nothing is billed and the reserve is released.
	AudioPollStatusFailed AudioPollStatus = "failed"
	// AudioPollStatusTimedOut means ExpiresAt passed before a terminal state was
	// observed. A genuine accounting gap candidate — the vendor may have produced
	// audio it charged us for and the broker never billed — so it is logged loudly
	// rather than dropped.
	AudioPollStatusTimedOut AudioPollStatus = "timed_out"
)

// AudioPollJob tracks a single audio-generation create that returned a
// non-terminal status, so the background scheduler can poll it to completion and
// bill what the vendor actually reports instead of what was reserved.
//
// See docs/design/seed-audio-generation.md, and video-generation-async-billing.md
// for the scheduler mechanics this reuses wholesale (claim-by-lease, terminal-state
// billing, ZG-Res-Key re-signing).
//
// # Why this is a separate table from VideoPollJob
//
// The two carry almost the same columns, so a `modality` discriminator on one
// table is the obvious suggestion. It is the wrong trade here for two reasons that
// are both about the claim query rather than the schema:
//
//   - The claim is an atomic UPDATE over `status='pending' AND next_poll_at<=now()`
//     and it is the hot path. Sharing a table puts audio and video rows in
//     contention for the same index range for no benefit — neither modality ever
//     wants to claim the other's work.
//   - Their cadences differ by an order of magnitude. Video polls every 10s with a
//     20-minute ceiling, sized for a 1-5 minute render; audio renders in 10-30s.
//     One table means one set of rows tuned for whichever cadence the index was
//     built around.
type AudioPollJob struct {
	Model
	ID uint64 `gorm:"primaryKey;autoIncrement" json:"id"`
	// ProviderJobID is the id the adaptor returned from create, already in the
	// published 36-character contract (translate.EncodeJobID shapes it there).
	ProviderJobID string `gorm:"type:varchar(255);not null;index" json:"providerJobId"`
	// RequestHash links back to the Request row created before dispatch. One audio
	// job, one request: unique, so a job can never be double-registered.
	RequestHash string `gorm:"type:varchar(255);not null;uniqueIndex" json:"requestHash"`
	// PollURL is the fully-resolved GET URL, captured at create time so the
	// scheduler never reconstructs routing decisions later.
	PollURL string `gorm:"type:text;not null" json:"pollUrl"`
	// RequestBody is the original client request bytes, needed at completion for TEE
	// signing (the signature binds request+response hashes).
	RequestBody []byte `gorm:"type:mediumblob" json:"-"`
	// RequestContentType is the client's original Content-Type — multipart boundary
	// or application/json — so RequestBody can be re-read exactly as the create path
	// read it.
	RequestContentType string `gorm:"type:varchar(255)" json:"-"`
	// OutputPrice is the price snapshot at request time, matching the sync path: the
	// price in effect when the request was accepted, not whatever is current when the
	// job happens to finish.
	OutputPrice string `gorm:"type:varchar(255);not null" json:"-"`
	// ReservedSeconds is the bound AudioCreateReserve actually held, stored rather
	// than re-derived.
	//
	// This is the one field with no VideoPollJob counterpart, and it is here because
	// audio has a fallback video does not: when no billable quantity can be resolved
	// (the vendor reports none and the output format is `pcm`, which has no container
	// to measure — see the design doc), the charge falls back to exactly this number.
	// Re-deriving it at completion would mean re-running the request parser and the
	// spec and TRUSTING them to reproduce what was held minutes earlier; storing it
	// makes the hold and the fallback charge equal by construction instead.
	//
	// It is also what the failure and timeout paths release.
	ReservedSeconds int64 `gorm:"type:bigint;not null;default:0" json:"reservedSeconds"`
	// ChatKey is the TEE signature-lookup handle already returned to the client in
	// the create response's ZG-Res-Key header (empty when the service does not sign).
	// The scheduler re-signs under this SAME key once the real result is known,
	// overwriting the placeholder signature made over the queued-status body.
	ChatKey string `gorm:"type:varchar(64)" json:"-"`
	// ResolvedModel is the multi-model pricing key resolved at create time. Captured
	// because the background scheduler has no HTTP request to resolve it from later.
	ResolvedModel string `gorm:"type:varchar(255)" json:"-"`
	// MetricModel is the bounded Prometheus label captured at create time, for the
	// same reason as ResolvedModel.
	MetricModel string `gorm:"type:varchar(255)" json:"-"`
	// IsWhitelisted marks a job created for whitelisted (unbilled) traffic. Such
	// requests create no Request row, so RequestHash is a unique nonce referencing
	// nothing; completion writes to the hourly_usage_stat rollup instead — once, at
	// resolution time, because that rollup is keyed in part by RateClass and an eager
	// write would have to MOVE a unit between aggregate rows rather than update one
	// in place.
	IsWhitelisted bool `gorm:"type:tinyint(1);not null;default:0" json:"-"`

	// Status leads idx_status_next_poll_at (see NextPollAt); every hot query filters
	// on it, so a standalone index here would be redundant write overhead.
	Status AudioPollStatus `gorm:"type:varchar(16);not null;default:'pending';index:idx_audio_status_next_poll_at,priority:1" json:"status"`
	// Attempts counts poll round-trips so far; informational (the interval is fixed,
	// not backoff).
	Attempts int `gorm:"type:int;not null;default:0" json:"attempts"`
	// NextPollAt is when a worker may next claim this row. Claiming sets it to
	// now()+leaseWindow, so a crashed worker's claim becomes reclaimable on its own
	// once the lease elapses — that is the whole of crash recovery.
	//
	// Composite-indexed with Status rather than each getting its own index: the claim
	// query filters on both and sorts on this column, which only a composite serves.
	// Two single-column indexes force MySQL to pick one and filter the rest by row
	// lookup, or index-merge, and neither can serve the ORDER BY.
	NextPollAt time.Time `gorm:"type:datetime;not null;index:idx_audio_status_next_poll_at,priority:2" json:"nextPollAt"`
	// ExpiresAt is the hard ceiling (created_at + MaxPollDuration). Past it the job is
	// marked timed_out regardless of provider state.
	ExpiresAt    time.Time `gorm:"type:datetime;not null;index" json:"expiresAt"`
	ErrorMessage string    `gorm:"type:text" json:"errorMessage,omitempty"`
}
