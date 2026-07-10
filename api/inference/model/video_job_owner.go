package model

// VideoJobOwner maps a provider-assigned video-generation job id to the broker address that
// created it, so GET /videos/{id} and GET /videos/{id}/content (proxy.go's
// AuthRequiredPrefixes passthrough) can verify the caller is the same user who created the job
// before forwarding to the provider — see issue #591. Without this, a valid broker session
// alone (which any registered user has) was enough to read or download any other user's video
// job by its provider-assigned id.
//
// Written unconditionally whenever a video-generation create response carries a job id
// (ctrl.handleVideoGenerationResponse), regardless of billing outcome: sync-completed or
// deferred-to-poll, whitelisted or paying, and even a create response that itself reports
// failed — the client already has this id from the raw create response either way, so
// ownership must exist before it could ever be queried. This intentionally does NOT reuse
// VideoPollJob (created only for the non-terminal/deferred case) or Request (never created for
// whitelisted traffic, and carries no provider job id for the synchronous case) — neither
// covers all four combinations on its own.
type VideoJobOwner struct {
	Model
	ID            uint64 `gorm:"primaryKey;autoIncrement" json:"-"`
	ProviderJobID string `gorm:"type:varchar(255);not null;uniqueIndex" json:"-"`
	// UserAddress is deliberately NOT indexed: every current query (GetVideoJobOwner,
	// DeleteExpiredVideoJobOwners) filters on ProviderJobID or CreatedAt, never on this column
	// alone — add an index here only once a real query needs it (e.g. an admin "list jobs by
	// user" endpoint).
	UserAddress string `gorm:"type:varchar(255);not null" json:"-"`
	// Upstream is the billing counterparty that served this job (same convention as
	// Request.Upstream / HourlyUsageStat.Upstream — the provider identity string, or "self"
	// for decentralized providers). Purely informational for now, deliberately NOT part of any
	// key: GetVideoJobOwner is looked up by ProviderJobID alone because that's the only thing
	// an incoming GET /videos/{id} request actually carries — there is no upstream context
	// available at lookup time to combine it with (you'd need to already know the upstream to
	// query by it, but the lookup is what tells you the upstream). So ProviderJobID must stay
	// globally unique regardless of how many upstreams exist, not just "for now." Upstream is
	// recorded here in case a future multi-upstream routing layer needs to know which vendor a
	// given job id belongs to (e.g. to route a status check to the right target).
	Upstream string `gorm:"type:varchar(64);not null;default:''" json:"-"`
}
