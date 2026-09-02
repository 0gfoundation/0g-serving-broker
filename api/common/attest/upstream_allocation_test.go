package attest

import (
	"runtime"
	"strings"
	"testing"
)

// What parseUpstreamSet allocates must be bounded by what it VALIDATED, not by what it
// received. This is the third form of one bug, so it is asserted as a budget rather than as
// the absence of any particular mistake.
//
//	v1  make([]Upstream, 0, want)      — sized from the count. "count=1000000000" is 16
//	                                     bytes and asked for a billion Upstreams; measured,
//	                                     a verifier took `fatal error: runtime: out of
//	                                     memory` rather than the refusal the tally owed it.
//	v2  make(..., 0, len(lines))        — sized from the line count, which is an upper bound
//	                                     on members but not a tight one. Measured: a 4 MiB
//	                                     payload of bare newlines allocated 492 MB, 117x.
//	v3  no hint, strings.SplitSeq       — 5.7 KB for the same payload.
//
// The payload is fully controlled by the CVM being described: it hex-decodes out of
// event_payload in the dstack event log, and the RTMR3 replay does not bound its size,
// because the CVM extended the register with those bytes itself. So an unbounded
// allocation here is not a hypothetical — it is the described party choosing how much
// memory every verifier and SDK client spends on it.
//
// The budget is generous on purpose. It is not measuring an exact figure, which would make
// this test a tripwire for unrelated allocation changes; it is asserting that the cost does
// not scale with the payload. Both mistakes above blow through it by two to five orders of
// magnitude.
func TestParseAllocationDoesNotScaleWithThePayload(t *testing.T) {
	const budget = 1 << 20 // 1 MiB, against a 4 MiB payload

	// One line per byte, which is the worst case for anything sized per line, and refused
	// by the tally — so nothing here is even a set.
	payload := "count=1\n" + strings.Repeat("\n", 4<<20)

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	if _, err := parseUpstreamSet(payload); err == nil {
		t.Fatal("a payload of bare newlines parsed as a set")
	}
	runtime.ReadMemStats(&after)

	allocated := after.TotalAlloc - before.TotalAlloc
	if allocated > budget {
		t.Errorf("parsing a %d-byte payload allocated %d bytes (%.1fx), want under %d: something is sized from the payload rather than from what was validated",
			len(payload), allocated, float64(allocated)/float64(len(payload)), budget)
	}
}
