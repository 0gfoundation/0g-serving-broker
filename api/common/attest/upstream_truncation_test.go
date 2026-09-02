package attest

import (
	"strings"
	"testing"
)

// truncationFixture is a correct two-member payload. The tests below take every prefix
// of it, which is the cheapest way to ask "what does a payload that stopped early
// parse as" without picking the prefixes by hand — and the dangerous ones are not the
// obviously broken ones.
const truncationFixture = "count=2\nengine1 http://engine-1:8000/v1\nvendor https://vendor.example/v1 openrouter"

// No prefix of a valid payload may parse as a set of a different size.
//
// This is the property the count header exists for. Before the header, 40 of the 76
// prefixes of the member block parsed cleanly, and the worst was not a mangled URL but
// "engine1 http://engine-1:8000/v1\n" — a complete, internally consistent set of one
// in-CVM engine with the external vendor silently gone, reported as known, with a valid
// hash and no change line, because a shorter record is not a broken one.
//
// It does not take an attacker. A writer renders this payload from its config, and
// before the header the grammar gave it no way to spell "one member I could not
// establish", so a writer that gave up mid-build emitted exactly those bytes. The count
// closes that: the writer takes n from its config before it renders anything, so a
// build that falls short says so.
func TestNoPrefixOfAPayloadParsesAsADifferentSet(t *testing.T) {
	if _, err := parseUpstreamSet(truncationFixture); err != nil {
		t.Fatalf("the whole payload must parse, or this test proves nothing: %v", err)
	}
	for i := 0; i < len(truncationFixture); i++ {
		members, err := parseUpstreamSet(truncationFixture[:i])
		if err != nil {
			continue
		}
		if len(members) != 2 {
			t.Errorf("prefix[:%d] = %q parses as a set of %d", i, truncationFixture[:i], len(members))
		}
	}
}

// What the count does not catch, measured rather than assumed — and why that is not a
// framing problem.
//
// A prefix that keeps both member lines but cuts into the last one still tallies, so it
// parses. What it parses to is a real destination that is not the recorded one: the URL
// can be cut back to a single-character host ("https://v"), which is a perfectly valid
// base, and the identity can be cut off entirely.
//
// No framing check closes that, and adding one would be theatre:
//
//   - A digest of the member block guards nothing real. Whoever writes the payload also
//     writes the digest, so it stops no attacker; and against accident there is no
//     channel to protect, because the bytes that extend RTMR3 and the bytes stored in
//     the event log are the same bytes. A payload that was already short when the writer
//     sent it is recorded as short, digest and all.
//   - More to the point, a member with a short URL is indistinguishable from a member
//     whose CONFIGURED URL is short. The record's job is to say what the config permits;
//     if the config holds the wrong destination, the record is honestly wrong, and that
//     is misconfiguration rather than truncation. Nothing in an encoding can tell the
//     two apart.
//
// So the count closes the failure that framing CAN close — a writer emitting fewer
// members than its config holds — and this test exists to keep the boundary between the
// two visible, so a later reader does not mistake the residual for an oversight.
//
// What limits the residual is outside this package: an external destination with no
// identity is a reading the caller must already refuse, and a member whose URL moved is
// reported through UpstreamChanges the next time a good record lands.
func TestWhatTheCountDoesNotCatch(t *testing.T) {
	recorded := strings.Split(truncationFixture, "\n")
	var survivors int
	for i := 0; i < len(truncationFixture); i++ {
		members, err := parseUpstreamSet(truncationFixture[:i])
		if err != nil {
			continue
		}
		survivors++
		// The other test's property, restated so this one cannot pass by weakening it.
		if len(members) != 2 {
			t.Fatalf("prefix[:%d] parses as a set of %d, which the count should have refused", i, len(members))
		}
		// Every survivor must differ from the recorded set ONLY in its last member. A
		// prefix that parsed with the first member altered would mean the tally is not
		// doing what the reasoning above assumes.
		if members[0].Name+" "+members[0].URL != recorded[1] {
			t.Errorf("prefix[:%d] parses with the first member as %q, want %q", i, members[0], recorded[1])
		}
		// And the last member is only ever a truncation of the recorded one, never
		// something else entirely.
		last := members[1].Name + " " + members[1].URL
		if members[1].Identity != "" {
			last += " " + members[1].Identity
		}
		if !strings.HasPrefix(recorded[2], last) {
			t.Errorf("prefix[:%d] parses its last member as %q, which is not a prefix of the recorded %q", i, last, recorded[2])
		}
	}
	if survivors == 0 {
		t.Fatal("no prefix parses, so either the residual is now closed or the fixture changed; update the reasoning above rather than deleting the test")
	}
}

// The count is a number the described party chose, so nothing may be sized from it.
//
// It is validated only for being non-negative and is checked against the members at the END
// of the parse, so a pre-allocation taken from it is an allocation of any size the writer
// likes — from a payload of sixteen bytes, for a record that was always going to be refused.
// Every verifier and SDK client that resolves such a CVM would get an out-of-memory or a
// panic in makeslice instead of an answer, which turns "this record is unreadable" (a fact
// the reader is designed to report) into "the reader cannot run".
//
// How the regression manifests, measured rather than predicted: sizing from the count again
// does NOT produce a recoverable panic on these inputs. It produces `fatal error: runtime:
// out of memory` and the test binary dies. That is caught — the run fails — but it is worth
// stating, because the recover() below will not be what catches it, and because it is exactly
// the production symptom: the reader does not report "this record is unreadable", it stops
// existing. The recover() is kept for the platforms and element sizes where the runtime
// refuses the capacity instead (`makeslice: cap out of range`), so the assertion holds either
// way.
//
// In the fixed state none of these allocates anything: the tally refuses them.
func TestTheMemberCountNeverSizesAnAllocation(t *testing.T) {
	for _, payload := range []string{
		"count=9223372036854775807\nengine1 http://engine-1:8000/v1",
		"count=9223372036854775807",
		// Just inside the range Atoi accepts, and far outside anything a set can hold.
		"count=4611686018427387904\nengine1 http://engine-1:8000/v1",
		// And the shape without members at all, so the empty-set path is covered too.
		"count=2147483647",
	} {
		t.Run(payload[:min(len(payload), 40)], func(t *testing.T) {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("parseUpstreamSet panicked on %q: %v — something is sized from the payload's own count", payload, r)
				}
			}()
			members, err := parseUpstreamSet(payload)
			if err == nil {
				t.Fatalf("parseUpstreamSet(%q) = %+v, want the tally to refuse it", payload, members)
			}
			if !strings.Contains(err.Error(), "is not the set it lists") {
				t.Errorf("parseUpstreamSet(%q) = %v, want the tally check to be what refuses it", payload, err)
			}
		})
	}
}

// A count past what Atoi can represent is refused before anything else looks at it.
func TestAnUnrepresentableMemberCountIsRefused(t *testing.T) {
	for _, payload := range []string{
		"count=99999999999999999999999999",
		"count=9223372036854775808", // MaxInt64 + 1
		"count=-1",
		"count=1e9",
		"count=0x10",
	} {
		got, err := parseUpstreamSet(payload)
		if err == nil {
			t.Errorf("parseUpstreamSet(%q) = %+v, want a refusal", payload, got)
			continue
		}
		if !strings.Contains(err.Error(), "does not name a member count") {
			t.Errorf("parseUpstreamSet(%q) = %v, want the header check to refuse it", payload, err)
		}
	}
}
