package attest

import (
	"fmt"
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
//	v3  no hint, strings.SplitSeq       — 5.7 KB for the same payload. But the REFUSAL
//	                                     messages still quoted their input with %q, and a
//	                                     payload with no newline is one line: measured, 4
//	                                     MiB in produced 51 MB allocated and an 8.4 MB
//	                                     error string, which resolve.go then retains in
//	                                     UpstreamsErr and copies into UpstreamChanges.
//	v4  a per-line length cap           — one guard, so every %q downstream of a line is
//	                                     bounded without each having to remember to
//	                                     truncate.
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
const (
	manyValidMembersHonestCount = "many valid members, honest count"
	manyValidMembersLyingCount  = "many valid members, count says zero"
)

// manyValidMembers builds a payload of distinct, well-formed member lines — the only shape
// that reaches the member loop, since every other refusal fires on the first line or two.
// Distinct names matter: repeating one is caught by the duplicate check at member two, which
// is why an earlier attempt at this fixture measured nothing.
func manyValidMembers(t *testing.T, honest bool) string {
	t.Helper()
	var body strings.Builder
	for i := 0; body.Len() < 4<<20; i++ {
		fmt.Fprintf(&body, "n%d http://x:1/v1\n", i)
	}
	count := 0
	if honest {
		count = strings.Count(body.String(), "\n")
	}
	return fmt.Sprintf("%s%d\n%s", upstreamCountPrefix, count, body.String())
}

func TestParseAllocationDoesNotScaleWithThePayload(t *testing.T) {
	const budget = 1 << 20 // 1 MiB, against 4 MiB payloads

	// Both shapes, because the two mistakes had different worst cases and the first version
	// of this test happened to miss the second entirely — its fixture began with a header
	// line, so the payload never reached a message that quotes it.
	for _, tt := range []struct {
		name    string
		payload string
	}{
		{
			// One line per byte: the worst case for anything sized per line.
			name:    "many lines",
			payload: strings.Repeat("\n", 4<<20),
		},
		{
			// No newline at all, so the whole payload is ONE line — the worst case for
			// anything that quotes a line, which every refusal below does with %q.
			name:    "one enormous line",
			payload: strings.Repeat("a", 4<<20),
		},
		{
			// And the same, past the header, so the member-line messages are exercised
			// rather than the header one.
			name:    "one enormous member line",
			payload: "count=1\n" + strings.Repeat("a", 4<<20),
		},
		{
			// Whitespace-separated, so strings.Fields yields a huge field: the shape that
			// reaches the messages quoting a NAME or an identity rather than the line.
			name:    "one enormous field",
			payload: "count=1\nname " + strings.Repeat("a", 4<<20),
		},
		{
			// Every line VALID, and the count honest about how many there are — the only
			// shape that reaches the member loop at all. The four above refuse on the
			// first line or two, which is why they missed this: the loop built every
			// member and the tally refused afterwards, for 143 MB on 4 MiB.
			name:    manyValidMembersHonestCount,
			payload: manyValidMembers(t, true),
		},
		{
			// The same, with the count claiming zero. The count was validated before the
			// loop, so from the second member the record is already refused — and it still
			// cost the same 143 MB to find that out.
			name:    manyValidMembersLyingCount,
			payload: manyValidMembers(t, false),
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var before, after runtime.MemStats
			runtime.GC()
			runtime.ReadMemStats(&before)
			if _, err := parseUpstreamSet(tt.payload); err == nil {
				t.Fatal("this payload parsed as a set, so the budget below is measuring the wrong path")
			}
			runtime.ReadMemStats(&after)

			allocated := after.TotalAlloc - before.TotalAlloc
			if allocated > budget {
				t.Errorf("parsing a %d-byte payload allocated %d bytes (%.1fx), want under %d: something is sized from the payload rather than from what was validated",
					len(tt.payload), allocated, float64(allocated)/float64(len(tt.payload)), budget)
			}
		})
	}
}
