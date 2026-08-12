// Package attest reads what a dstack CVM reports it is running out of its
// attestation report, and is explicit about which parts of that report are binding.
//
// A signed TDX quote carries measurements of what booted. It does not carry what
// changed afterwards — an image upgrade performed inside the TEE leaves the boot
// measurements untouched. What it does carry is RTMR3, which the controller extends
// with one event per change before making it (see controller/internal/ctrl). RTMR3 is
// append-only, so those events cannot be edited or dropped, and the quote's signature
// covers the resulting value.
//
// # What this does not establish, and who must establish it
//
// RTMR3 says what was written, not who wrote it. dstack serves EmitEvent on
// /var/run/dstack.sock from the same unauthenticated handler as GetQuote, binds that
// socket 0777, and restricts neither the event name nor the payload. Any container that can
// reach it can append any record.
//
// So in a deployment that mounts that socket into the broker, a provider running a modified
// broker image can append its own zg-image-update naming any digest and take a quote over
// it. That quote is genuine: the replay matches, the compose hash matches, the signature
// verifies. A challenge nonce does not help — it defeats replay of an old quote, not forgery
// of a new one.
//
// A deployment that keeps the socket with the controller alone does not have that problem,
// and there the record carries a signature of a kind: the address of the signing key derived
// from the image it names, which ResolveRunningState requires the quote's report_data to
// match. That works only because the broker cannot derive keys itself — the controller
// withholds GetKey — so it is the confinement that makes the binding meaningful, not the
// other way round.
//
// **A DigestSourceEvent answer is therefore only as good as the deployment's confinement
// of RTMR3 writers, and this package cannot check that.** It is a property of the compose
// file, and the caller already holds the means to settle it: RunningState.ComposeHash is
// the hash of the compose the CVM actually booted, bound to the quote by hardware. Compare
// it against the hash of a compose you published and reviewed, and you know — by reading a
// document you control — whether anything upgradeable can write the ledger. That is the
// same discipline as the expected-digest set: the answer comes from software the user
// installed, never from the party being checked.
//
// Do not expect this package to derive it from app_compose instead. That was tried and
// reverted: the mount shapes that reach a socket are an open set (a whole-directory bind,
// a named volume with a bind driver_opt, a YAML alias, extends:, an interpolation that
// splits the path — app_compose stores the compose text uninterpolated), and the fields
// that would identify the broker are the wrong ones (the controller locates containers by
// container_name, and in this project the controller shares the broker's image string and
// differs only by command:). A parser that answers "probably not" here is worse than no
// parser, because it reads as a guarantee.
//
// So: a deployment that keeps that socket away from the broker — routing its quotes through
// the controller, whose image compose_hash pins and which cannot upgrade itself — gets a
// DigestSourceEvent answer that is proof of what is running. One that does not gets an audit
// record of what the controller did. Which of the two a caller is holding is decided by the
// compose file behind RunningState.ComposeHash, and by nothing this package can see.
// doc/attestation-trust-chain.md lists what that review must cover.
//
// # Scope
//
// This package holds the authoritative implementation of the replay, because the
// events are produced in this repository: the format and the reader have to
// evolve together or one silently stops explaining the other.
//
// It deliberately does not verify the quote's DCAP signature, and does not compare
// anything against an expected value. Those are the caller's: the signature needs
// Intel's collateral, and "is this the digest we published" needs a list of
// digests that must come from software the *user* installed, never from the party
// being checked.
package attest

import (
	"crypto/sha512"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

// RuntimeEventType marks the RTMR3 entries an application wrote, as opposed to
// the TCG entries the firmware wrote into RTMR0-2.
const RuntimeEventType uint32 = 0x08000001

// rtmr3Index is the register an application extends. RTMR0-2 are the firmware's.
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

// Digest is the measurement of one event:
//
//	SHA384( LE32(RuntimeEventType) ‖ ":" ‖ Event ‖ ":" ‖ Payload )
//
// The length prefix is explicitly little-endian. dstack writes it with Rust's
// to_ne_bytes() — native order, which on the x86_64 hosts this runs on is little.
// A reader that assumed big-endian would compute a digest that never matches, and
// nothing in the format would say why.
func (e RuntimeEvent) Digest() [48]byte {
	var tag [4]byte
	binary.LittleEndian.PutUint32(tag[:], RuntimeEventType)

	h := sha512.New384()
	h.Write(tag[:])
	h.Write([]byte(":"))
	h.Write([]byte(e.Event))
	h.Write([]byte(":"))
	h.Write(e.Payload)

	var digest [48]byte
	copy(digest[:], h.Sum(nil))
	return digest
}

// ReplayRTMR3 folds events into RTMR3 the way the hardware does, starting from
// the 48 zero bytes RTMR3 holds at reset:
//
//	mr = SHA384( mr ‖ event.Digest() )
//
// The result equalling the RTMR3 in a signed quote is what turns an event log —
// which arrives over plain HTTP from the party being checked — into something
// worth reading. Any event added, removed, reordered or altered changes it.
func ReplayRTMR3(events []RuntimeEvent) [48]byte {
	var mr [48]byte
	for _, event := range events {
		digest := event.Digest()

		h := sha512.New384()
		h.Write(mr[:])
		h.Write(digest[:])
		copy(mr[:], h.Sum(nil))
	}
	return mr
}

// RuntimeEvents pulls the runtime events out of a GetQuote event_log, in the
// order the log carries them.
//
// Entries the firmware wrote are skipped: only RuntimeEventType extends RTMR3
// with an application's own name and payload.
//
// The digest each entry declares is ignored, not checked. It is not an
// independent statement about anything — ReplayRTMR3 recomputes every digest from
// the name and payload, so an entry whose declared digest disagrees changes
// nothing, and an entry whose name or payload was altered fails the comparison
// against the quote's RTMR3. Checking it separately would only report the same
// tampering twice.
func RuntimeEvents(eventLogJSON []byte) ([]RuntimeEvent, error) {
	var entries []TdxEvent
	if err := json.Unmarshal(eventLogJSON, &entries); err != nil {
		return nil, fmt.Errorf("parsing event log: %w", err)
	}

	events := make([]RuntimeEvent, 0, len(entries))
	for i, entry := range entries {
		// Both, because dstack's own reader partitions by register: an entry carrying the
		// runtime type on another IMR belongs to another register's replay, and folding it
		// into this one turns a legitimate log into a bare "the replay does not match" with
		// nothing saying why.
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
