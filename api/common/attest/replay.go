package attest

import (
	"crypto/sha512"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

// RTMRLen is the width of a TDX measurement register and of the SHA-384 that extends it.
const RTMRLen = 48

// QuoteRTMR3Offset is where RTMR3 sits in a TDX v4 quote: 48 bytes of header, then the TD
// report body, in which RTMR3 is at 472. report_data follows at 520 + 48 = 568.
const QuoteRTMR3Offset = 48 + 472

// EventDigest is the digest dstack extends RTMR3 with for one runtime event:
//
//	sha384(event_type as 4 little-endian bytes ‖ ":" ‖ event ‖ ":" ‖ payload)
//
// (cc-eventlog: `sha384(event_type.to_ne_bytes() || b":" || event || b":" || event_payload)`;
// every CVM this is read from is x86-64, so native order is little-endian.) It is computed
// here rather than read from the entry because the entry's own digest field is not an
// independent statement about anything — and because dstack's GetQuote omits it for RTMR3
// entries altogether, serving the payload instead (and, for the firmware's RTMR0-2 entries,
// the digest and not the payload). A log in that form replays only if the reader recomputes.
func EventDigest(eventType uint32, event string, payload []byte) [RTMRLen]byte {
	h := sha512.New384()
	var ty [4]byte
	binary.LittleEndian.PutUint32(ty[:], eventType)
	h.Write(ty[:])
	h.Write([]byte(":"))
	h.Write([]byte(event))
	h.Write([]byte(":"))
	h.Write(payload)
	var out [RTMRLen]byte
	copy(out[:], h.Sum(nil))
	return out
}

// ReplayRTMR3 folds every RTMR3 entry of a GetQuote event_log into the register value the
// quote must carry: starting from 48 zero bytes, rtmr = sha384(rtmr ‖ digest) per entry, in
// log order, with each digest recomputed by EventDigest.
//
// This is the anchor that makes any zg- record worth reading. The event log and tcb_info
// arrive from the provider, unsigned; only the quote is signed, and the quote names RTMR3.
// A log whose replay does not equal the quote's RTMR3 is not the log the CVM wrote, and
// "the last record" in it means nothing. ResolveRunningState does not call this: it takes a
// log the caller has already anchored, whether through dstack-verifier's event_log_verified
// or through this function against the quote bytes.
//
// A reference implementation, not the only acceptable one. A verifier that does not want to
// depend on the party it verifies should implement these two functions from
// doc/rtmr3-records.md and check its result against the vectors under testdata/vectors.
func ReplayRTMR3(eventLogJSON []byte) ([RTMRLen]byte, error) {
	var entries []TdxEvent
	if err := json.Unmarshal(eventLogJSON, &entries); err != nil {
		return [RTMRLen]byte{}, fmt.Errorf("parsing event log: %w", err)
	}
	var mr [RTMRLen]byte
	for i, e := range entries {
		if e.IMR != rtmr3Index {
			continue
		}
		payload, err := hex.DecodeString(e.EventPayload)
		if err != nil {
			return [RTMRLen]byte{}, fmt.Errorf("event log entry %d (%q): decoding payload: %w", i, e.Event, err)
		}
		d := EventDigest(e.EventType, e.Event, payload)
		h := sha512.New384()
		h.Write(mr[:])
		h.Write(d[:])
		copy(mr[:], h.Sum(nil))
	}
	return mr, nil
}

// RTMR3FromQuote reads RTMR3 out of raw TDX v4 quote bytes.
func RTMR3FromQuote(quote []byte) ([RTMRLen]byte, error) {
	var out [RTMRLen]byte
	if len(quote) < QuoteRTMR3Offset+RTMRLen {
		return out, fmt.Errorf("quote is %d bytes, too short to hold RTMR3 at %d", len(quote), QuoteRTMR3Offset)
	}
	copy(out[:], quote[QuoteRTMR3Offset:QuoteRTMR3Offset+RTMRLen])
	return out, nil
}
