// Package attest reads the records this project writes into a dstack CVM's RTMR3, and binds
// them to the keys the CVM published.
//
// It is the second half of a verification, not the whole of one. The first half —
// the DCAP signature, the measurement registers, the RTMR3 replay, the OS image hash, the
// ACPI tables, the TCB status — belongs to dstack-verifier, whose /verify endpoint runs the
// same process the dstack KMS runs before it releases a CVM's keys. Which is exactly the bar
// worth meeting: a check weaker than that accepts CVMs the KMS itself would refuse a key to.
//
// # What the caller must have done first
//
// Constructing a VerifiedQuote is the caller asserting all of it. POST the GetQuote triple to
// a dstack-verifier you run yourself, and require:
//
//	is_valid                          the whole verdict
//	details.quote_verified            the DCAP chain
//	details.event_log_verified        the log replays into the quote's registers
//	details.tcb_status == "UpToDate"  and details.advisory_ids empty
//	details.os_image_hash_verified    a published dstack guest image, not one the provider built
//	details.app_info.key_provider_info the KMS root key you trust (hex-encoded JSON; read id)
//
// There is no details.os_image_is_dev: a development guest image is a different published
// os_image_hash, not a flag, so compare that field against the release you expect.
//
// Run your own instance rather than someone else's: calling a verifier you do not control
// moves the trust root onto it, which is the one thing every rule below exists to avoid.
//
// This package cannot check any of that and does not pretend to. Nor does it compare anything
// against an expected value — "is this the digest we published" needs a list that must come
// from software the user installed, never from the party being checked.
//
// # What is left, and why it is here
//
// Two formats dstack knows nothing about, so nobody else will ever read them:
//
//   - the zg-image-update and zg-config-update records, which the controller appends before
//     making a change (see controller/internal/ctrl). dstack-verifier confirms the log
//     replays; it returns dstack's own boot facts and not these.
//   - report_data's 64 bytes, packed by this project as enc_pub ‖ signer_addr ‖ version
//     (0g-pc SPEC §4.2). The verifier hands the bytes back untouched.
//
// And one anchor the verifier cannot supply: its request carries quote, event_log and
// vm_config — never app_compose — so nothing has tied tcb_info to the quote. This package
// requires app_compose to hash to the compose hash the verifier reported out of the signed
// report body, and refuses otherwise.
//
// # What an answer from here does and does not mean
//
// RTMR3 says what was written, not who wrote it. dstack serves EmitEvent from the same
// unauthenticated handler as GetQuote and binds that socket 0777, so in a deployment that
// mounts it into the broker, a modified broker image can append any record and take a genuine
// quote over it — the replay matches, the compose hash matches, the signature verifies.
//
// A deployment that keeps the socket with the controller alone does not have that problem, and
// there the record carries a signature of a kind: the addresses of the signing and encryption
// keys derived from the image it names, which ResolveRunningState requires report_data to
// match. That works only because the broker cannot derive keys itself, so it is the
// confinement that makes the binding meaningful and not the other way round.
//
// Which of the two a caller is holding is decided by the compose file behind
// RunningState.ComposeHash, and by nothing this package can see. Do not expect it to derive
// that from app_compose either: it was tried and reverted, because the mount shapes that reach
// a socket are an open set and the fields that would identify the broker are the wrong ones. A
// parser answering "probably not" there is worse than none, because it reads as a guarantee.
// doc/attestation-trust-chain.md lists what that review must cover.
package attest

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
)

// RuntimeEventType marks the RTMR3 entries an application wrote, as opposed to
// the TCG entries the firmware wrote into RTMR0-2.
const RuntimeEventType uint32 = 0x08000001

// rtmr3Index is the register an application can extend. RTMR0-2 are the
// firmware's, sealed by the time any container runs.
const rtmr3Index uint32 = 3

// TdxEvent is one entry of the event_log a dstack GetQuote response carries.
type TdxEvent struct {
	IMR          uint32 `json:"imr"`
	EventType    uint32 `json:"event_type"`
	Digest       string `json:"digest"`
	Event        string `json:"event"`
	EventPayload string `json:"event_payload"`
}

// RuntimeEvent is a name and payload folded into RTMR3.
type RuntimeEvent struct {
	Event   string
	Payload []byte
}

// RuntimeEvents pulls this project's RTMR3 records out of a GetQuote event_log,
// in the order the log carries them.
//
// The digest each entry declares is ignored, not checked. It is not an
// independent statement about anything: the verifier that reported
// event_log_verified recomputed every digest from the name and payload and
// folded them into the registers the quote signs, so an entry whose declared
// digest disagrees changes nothing, and one whose name or payload was altered
// already failed there. Checking it again would report the same tampering twice.
func RuntimeEvents(eventLogJSON []byte) ([]RuntimeEvent, error) {
	var entries []TdxEvent
	if err := json.Unmarshal(eventLogJSON, &entries); err != nil {
		return nil, fmt.Errorf("parsing event log: %w", err)
	}

	events := make([]RuntimeEvent, 0, len(entries))
	for i, entry := range entries {
		// Both conditions, and the register is the one that matters here. A caller reaches
		// this holding a log the verifier vouched for — but event_log_verified covers all
		// four registers, so an entry carrying the runtime type on IMR 0-2 is exactly as
		// "verified" as one on RTMR3. Only RTMR3 is extendable after boot, so only RTMR3 can
		// hold a record a container wrote; folding one from another register in would let an
		// entry among the firmware's measurements be read as ours.
		if entry.EventType != RuntimeEventType || entry.IMR != rtmr3Index {
			continue
		}
		payload, err := hex.DecodeString(entry.EventPayload)
		if err != nil {
			return nil, fmt.Errorf("event log entry %d (%q): decoding payload: %w", i, entry.Event, err)
		}
		events = append(events, RuntimeEvent{Event: entry.Event, Payload: payload})
	}
	return events, nil
}
