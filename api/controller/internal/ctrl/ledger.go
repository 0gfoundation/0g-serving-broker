package ctrl

import (
	"context"
	"fmt"
	"strings"
)

// LedgerEnvVar declares whether this CVM has an RTMR3 ledger the controller can append to.
//
// dstack 0.6 removed EmitEvent for applications ("runtime RTMR3 events are system-owned",
// 0.6 CHANGELOG #1116), so on 0.6 every record this controller writes fails. That used to be
// logged and ignored at boot, and the binding of keys to the upstream set ran only after a
// successful emit — so on 0.6 the keys silently stayed unbound and a change of where plaintext
// may go rotated no signer (doc/design/report-data-v2.md §4.4).
//
// Declared, not detected. dstack's Info carries os_image_hash but no version to branch on,
// and inferring "no ledger" from a failed emit would let any emit failure switch the
// controller into the mode that writes nothing. As a literal in the controller's compose
// environment it is covered by compose_hash, so a verifier sees which mode was chosen.
//
//	unset, "rtmr3"  append every record, and fail on any emit failure (dstack 0.5)
//	"none"          append nothing; bind the keys to the upstream set regardless, and
//	                always record (bind) the set (dstack 0.6)
//
// Anything else refuses to start: this decides whether the controller's records exist.
const LedgerEnvVar = "ZG_CONTROLLER_LEDGER"

type ledgerMode int

const (
	ledgerRTMR3 ledgerMode = iota
	ledgerNone
)

func (m ledgerMode) String() string {
	if m == ledgerNone {
		return "none"
	}
	return "rtmr3"
}

func parseLedger(v string) (ledgerMode, error) {
	switch strings.TrimSpace(v) {
	case "", "rtmr3":
		return ledgerRTMR3, nil
	case "none":
		return ledgerNone, nil
	default:
		return 0, fmt.Errorf("%s=%q: want \"rtmr3\" or \"none\"", LedgerEnvVar, v)
	}
}

// noLedger is the emitter on a CVM without a ledger: every record is accepted and written
// nowhere, so each change path runs as it does on 0.5 — keys derived, bound, checked —
// with nothing appended. What a verifier reads instead is the state committed in the quote
// (report_data v2), not a record.
type noLedger struct{}

func (noLedger) EmitEvent(context.Context, string, []byte) error { return nil }

// recordsUpstreamSet is whether this deployment records — and so binds its keys to — the
// set of destinations plaintext may reach.
//
// On a CVM with a ledger that stays the opt-in controller.recordUpstreamSet, which every
// deployment today has off and turning on rotates the signer. Without a ledger it is not
// optional: recordUpstreamSet comes from the config file, which compose_hash does not
// measure, and with nothing appended there is no other place a change of destinations
// would show. A new deployment on 0.6 has a new signer anyway, so this rotates nothing
// extra (§4.5).
func (c *Ctrl) recordsUpstreamSet() bool {
	return c.config.RecordUpstreamSet || c.ledger == ledgerNone
}
