package model

import "time"

// VideoPollStatus represents the lifecycle state of a VideoPollJob.
type VideoPollStatus string

const (
	// VideoPollStatusPending is claimable by the next scheduler scan once NextPollAt elapses.
	VideoPollStatusPending VideoPollStatus = "pending"
	// VideoPollStatusPolling is a claimed row awaiting one poll round-trip. A row stuck in
	// this state past its (lease-extended) NextPollAt is claimable again — see
	// db.ClaimDueVideoPollJobs and docs/design/video-generation-async-billing.md.
	VideoPollStatusPolling VideoPollStatus = "polling"
	// VideoPollStatusCompleted means the provider reported a terminal "completed" status and
	// the actual-duration fee has been written to the linked Request row.
	VideoPollStatusCompleted VideoPollStatus = "completed"
	// VideoPollStatusFailed means the provider reported a terminal "failed" status. Nothing
	// is billed.
	VideoPollStatusFailed VideoPollStatus = "failed"
	// VideoPollStatusTimedOut means ExpiresAt passed before a terminal state was observed.
	// This is a genuine accounting gap candidate — the provider may have delivered a video
	// the broker never billed for — and is logged loudly, not silently dropped.
	VideoPollStatusTimedOut VideoPollStatus = "timed_out"
)

// VideoPollJob tracks a single POST /videos call that returned a non-terminal
// (queued/in_progress) status, so the broker's background scheduler can poll it to
// completion and bill the actual delivered duration instead of the requested one.
//
// See docs/design/video-generation-async-billing.md for the full design. One row per
// create call that did not already carry the finished result.
type VideoPollJob struct {
	Model
	ID            uint64 `gorm:"primaryKey;autoIncrement" json:"id"`
	ProviderJobID string `gorm:"type:varchar(255);not null;index" json:"providerJobId"`
	// RequestHash links back to the Request row created before dispatch (proxy.go's
	// non-whitelisted path always creates one, with Fee/OutputCount at their zero values
	// for video-generation — see the design doc's "already exists" note). One video job,
	// one request: unique so a job can never be double-registered against the same request.
	RequestHash string `gorm:"type:varchar(255);not null;uniqueIndex" json:"requestHash"`
	// PollURL is the fully-resolved GET URL, captured at create time so the scheduler does
	// not need to reconstruct routing decisions later.
	PollURL string `gorm:"type:text;not null" json:"pollUrl"`
	// RequestBody is the original client request bytes, needed at completion time by
	// resolveVideoBilling's request-duration fallback and by TEE signing (the signature
	// binds request+response hashes).
	RequestBody []byte `gorm:"type:mediumblob" json:"-"`
	// RequestContentType is the client's original Content-Type (multipart boundary or
	// application/json), needed to re-parse RequestBody the same way the create path did.
	RequestContentType string `gorm:"type:varchar(255)" json:"-"`
	// OutputPrice is the price snapshot at request time (matches the sync path's semantics:
	// the price in effect when the request was accepted, not whatever is current when the
	// job happens to complete).
	OutputPrice string `gorm:"type:varchar(255);not null" json:"-"`
	// ChatKey is the TEE signature-lookup handle already generated and returned to the
	// client via the create response's ZG-Res-Key header (empty when the service doesn't
	// sign, e.g. TargetSeparated). The scheduler re-signs under this SAME key once the real
	// content is known, overwriting the placeholder signature made over the queued-status
	// body — see ctrl.signChatWithKey.
	ChatKey string `gorm:"type:varchar(64)" json:"-"`
	// ResolvedModel is the multi-model pricing key resolved at create time (from the real
	// gin.Context set by the routing layer). Captured here because the background
	// scheduler has no HTTP request to resolve it from later — see
	// Ctrl.videoOutputUnits / resolveModelPricing, which require it on a *gin.Context.
	// Empty for single-model services, which don't need it.
	ResolvedModel string `gorm:"type:varchar(255)" json:"-"`
	// MetricModel is the bounded label captured at create time for Prometheus metrics,
	// mirroring ResolvedModel's reasoning: metricModel() also expects a *gin.Context.
	MetricModel string `gorm:"type:varchar(255)" json:"-"`
	// IsWhitelisted marks a job created for whitelisted (unbilled) traffic. Whitelisted
	// requests create no Request row (see proxy.go), so RequestHash here is just a unique
	// nonce with nothing to reference — completion for these jobs writes to the
	// hourly_usage_stat reconciliation rollup (recordWhitelistedUsage) instead of a Request
	// row, and deliberately writes it only ONCE, at resolution time, rather than eagerly at
	// create time: HourlyUsageStat is a pre-aggregated rollup keyed in part by RateClass, so
	// an eager "unresolved" write followed by a "corrected" write would require moving a unit
	// of count from one aggregate row to another rather than just updating a value in place.
	// Writing once, only once the real outcome (completed/failed/timed_out) is known, avoids
	// that entirely. See docs/design/video-generation-async-billing.md.
	IsWhitelisted bool `gorm:"type:tinyint(1);not null;default:0" json:"-"`

	// Status is the leading column of idx_status_next_poll_at (see NextPollAt) — every hot
	// query filters on Status, so no separate single-column index is needed on top of that
	// composite; a standalone index here would just be redundant write overhead.
	Status VideoPollStatus `gorm:"type:varchar(16);not null;default:'pending';index:idx_status_next_poll_at,priority:1" json:"status"`
	// Attempts counts poll round-trips so far; informational only (a fixed poll interval is
	// used, not backoff — see the design doc).
	Attempts int `gorm:"type:int;not null;default:0" json:"attempts"`
	// NextPollAt is when a scheduler worker may next claim this row. Claiming sets it to
	// now()+leaseWindow so a crashed worker's claim becomes reclaimable automatically once
	// the lease elapses, without a separate crash-recovery pass.
	//
	// Composite-indexed with Status (idx_status_next_poll_at) rather than each getting its
	// own single-column index: db.ClaimDueVideoPollJobs' hot query
	// (WHERE status IN (...) AND next_poll_at <= ? ORDER BY next_poll_at ASC) filters on
	// both and sorts on this column, which only a composite index serves efficiently — two
	// single-column indexes force MySQL to either pick one and filter the rest by row lookup,
	// or index-merge, and can't use either index for the ORDER BY. Every current query
	// against NextPollAt already includes Status in the same WHERE, so the composite fully
	// subsumes what the old standalone index provided.
	NextPollAt time.Time `gorm:"type:datetime;not null;index:idx_status_next_poll_at,priority:2" json:"nextPollAt"`
	// ExpiresAt is the hard ceiling (created_at + MaxPollDuration). Past this the job is
	// marked timed_out regardless of provider state.
	ExpiresAt    time.Time `gorm:"type:datetime;not null;index" json:"expiresAt"`
	ErrorMessage string    `gorm:"type:text" json:"errorMessage,omitempty"`
}
