package model

import "time"

// SpmlPayout is the broker's per-node payout cumulative (docs/spml-design
// §4 step 9): every settled request's fee accrues onto its serving node's
// cumulative, and the pending covered hashes ride along until the spml has
// acknowledged them via a signed voucher (invoice succeeded). The verifier's
// own cut accrues under the reserved node id "__verifier__".
type SpmlPayout struct {
	Node       string `gorm:"type:varchar(64);primaryKey" json:"node"`
	Cumulative string `gorm:"type:varchar(255);not null;default:'0'" json:"cumulative"`
	Epoch      int64  `gorm:"type:bigint;not null;default:0" json:"epoch"`
	// PendingCovered is a JSON array of settled requests not yet acknowledged
	// by the spml (invoice failed / not attempted): {"h": hash, "f": node
	// share} entries (db.CoveredEntry; older rows hold bare hash strings).
	// Retried on the next settlement cycle; the spml's invoice endpoint is
	// idempotent. LONGTEXT: it grows one entry per settled request for as long
	// as the spml is unreachable, and TEXT's 64 KB (~950 hashes) was one busy
	// hour.
	PendingCovered string     `gorm:"type:longtext;not null" json:"pendingCovered"`
	Invoiced       bool       `gorm:"type:tinyint(1);not null;default:0" json:"invoiced"`
	UpdatedAt      *time.Time `gorm:"type:datetime" json:"updatedAt"`
}

// VerifierCutNode is the reserved SpmlPayout.Node for the spml's own cut.
const VerifierCutNode = "__verifier__"
