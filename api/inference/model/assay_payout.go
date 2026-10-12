package model

import "time"

// AssayPayout is the broker's per-node payout cumulative (docs/spml-design
// §4 step 9): every settled request's fee accrues onto its serving node's
// cumulative, and the pending covered hashes ride along until the assay has
// acknowledged them via a signed voucher (invoice succeeded). The verifier's
// own cut accrues under the reserved node id "__verifier__".
type AssayPayout struct {
	Node       string `gorm:"type:varchar(64);primaryKey" json:"node"`
	Cumulative string `gorm:"type:varchar(255);not null;default:'0'" json:"cumulative"`
	Epoch      int64  `gorm:"type:bigint;not null;default:0" json:"epoch"`
	// PendingCovered is a JSON array of request hashes settled but not yet
	// acknowledged by the assay (invoice failed / not attempted). Retried on
	// the next settlement cycle; the assay's invoice endpoint is idempotent.
	PendingCovered string     `gorm:"type:text;not null" json:"pendingCovered"`
	Invoiced       bool       `gorm:"type:tinyint(1);not null;default:0" json:"invoiced"`
	UpdatedAt      *time.Time `gorm:"type:datetime" json:"updatedAt"`
}

// VerifierCutNode is the reserved AssayPayout.Node for the assay's own cut.
const VerifierCutNode = "__verifier__"
