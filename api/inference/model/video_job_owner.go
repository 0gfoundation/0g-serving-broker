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
}
