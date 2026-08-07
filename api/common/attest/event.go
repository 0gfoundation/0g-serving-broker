// Package attest reads what a dstack CVM reports it is running out of its
// attestation report.
//
// A signed TDX quote carries measurements of what booted. It does not carry what
// changed afterwards — an image upgrade performed inside the TEE leaves the boot
// measurements untouched. What it does carry is RTMR3, which the controller extends
// with one event per change before making it (see controller/internal/ctrl). RTMR3 is
// append-only, so those events cannot be edited or dropped, and the quote's signature
// covers the resulting value.
//
// # What this does not establish
//
// RTMR3 says what was written, not who wrote it. dstack serves EmitEvent on
// /var/run/dstack.sock from the same unauthenticated handler as GetQuote, binds that
// socket 0777, and restricts neither the event name nor the payload — and the broker
// must mount it, because it needs GetQuote and DeriveKey. So a provider running a
// modified broker image can append its own zg-image-update naming any digest and then
// take a quote over it.
//
// That quote is genuine: the replay matches, the compose hash matches, the signature
// verifies. No signature on the record could help — the payloads are unauthenticated
// bytes and any container on the socket can derive the same keys. A fresh challenge
// nonce does not help either: it defeats replay of an old quote, not forgery of a new
// one.
//
// So the question is not answered, it is *checked*. Who holds the socket is written in
// the compose file, and compose_hash binds that file to the quote — which makes it an
// authenticated input rather than a claim. ResolveRunningState reads it and refuses an
// event-sourced digest when the broker, or anything sharing the broker's image, can write
// the ledger. What survives is either a digest bound by compose_hash, or a ledger nobody
// upgradeable can write.
//
// Deployments today mount that socket into the broker, because that is how it gets
// GetQuote and DeriveKey — so their event-sourced records are refused, and correctly so.
// Making them readable means giving the broker its quotes some other way (the controller,
// whose image compose_hash pins and which cannot upgrade itself, is the obvious
// candidate). doc/controller-design.md §5.1a carries the current state.
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
		if entry.EventType != RuntimeEventType {
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
