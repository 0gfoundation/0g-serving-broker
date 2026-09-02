package attest

import (
	"encoding/json"
	"strings"
	"testing"
)

// upstreamSet(members...) builds an EventUpstreamSet record the way a writer would.
func upstreamSetEvent(lines ...string) RuntimeEvent {
	if len(lines) == 0 {
		return RuntimeEvent{Event: EventUpstreamSet, Payload: []byte(emptyUpstreamSet)}
	}
	return RuntimeEvent{Event: EventUpstreamSet, Payload: []byte(strings.Join(lines, "\n"))}
}

// upstream_test.go exercises one payload in isolation. These go through
// ResolveRunningState, so they cover the dispatch loop, last-wins across records, and
// the fact that an upstream record does not disturb the other record types.
func TestResolveReadsTheLastUpstreamRecord(t *testing.T) {
	compose := pinnedCompose(t)
	events := append(bootEvents(),
		upstreamSetEvent("engine1 http://engine-1:8000/v1", "vendor https://vendor.example/v1 openrouter"),
		RuntimeEvent{Event: EventImageUpdate, Payload: imageRecordPayload("ghcr.io/0gfoundation/0g-serving-broker@" + upgradeDigest)},
		upstreamSetEvent("engine1 http://engine-1:8000/v1", "engine2 http://engine-2:8000/v1"),
	)

	state, err := resolve(t, compose, events)
	if err != nil {
		t.Fatalf("ResolveRunningState() = %v", err)
	}
	if state.UpstreamsState != UpstreamsKnown {
		t.Fatalf("UpstreamsState = %q (%s), want %q", state.UpstreamsState, state.UpstreamsErr, UpstreamsKnown)
	}
	want := []Upstream{
		{Name: "engine1", URL: "http://engine-1:8000/v1"},
		{Name: "engine2", URL: "http://engine-2:8000/v1"},
	}
	if len(state.Upstreams) != len(want) {
		t.Fatalf("Upstreams = %+v, want %+v", state.Upstreams, want)
	}
	for i := range want {
		if state.Upstreams[i] != want[i] {
			t.Errorf("Upstreams[%d] = %+v, want %+v", i, state.Upstreams[i], want[i])
		}
	}
	// The vendor is gone from the set, so the only place a reader learns it was ever
	// permitted is the change log.
	if len(state.UpstreamChanges) != 2 {
		t.Fatalf("UpstreamChanges = %q, want the added engine and the withdrawn vendor", state.UpstreamChanges)
	}
	// An upstream record must not disturb what the other records established.
	if state.BrokerDigest != upgradeDigest {
		t.Errorf("BrokerDigest = %q, want %q", state.BrokerDigest, upgradeDigest)
	}
	if state.DigestSource != DigestSourceEvent {
		t.Errorf("DigestSource = %q, want %q", state.DigestSource, DigestSourceEvent)
	}
}

// No record at all must leave the field nil rather than empty, because nil is what
// tells a caller the set bounds nothing. Every deployment in production today is in
// this state.
func TestResolveWithoutUpstreamRecords(t *testing.T) {
	state, err := resolve(t, pinnedCompose(t), bootEvents())
	if err != nil {
		t.Fatalf("ResolveRunningState() = %v", err)
	}
	if state.Upstreams != nil {
		t.Fatalf("Upstreams = %+v, want nil when nothing was recorded", state.Upstreams)
	}
	if state.UpstreamsState != UpstreamsUnrecorded {
		t.Fatalf("UpstreamsState = %q, want %q", state.UpstreamsState, UpstreamsUnrecorded)
	}
	if _, err := state.UpstreamSetHash(); err == nil {
		t.Fatal("UpstreamSetHash() answered for an unrecorded set")
	}
}

// An explicit "none" is a bound of zero, and it must be distinguishable from a log
// that never mentioned upstreams — those two are the states that would otherwise
// derive the same signing key.
func TestResolveDistinguishesTheEmptySetFromNoRecord(t *testing.T) {
	emptied, err := resolve(t, pinnedCompose(t), append(bootEvents(), upstreamSetEvent()))
	if err != nil {
		t.Fatalf("ResolveRunningState() = %v", err)
	}
	if emptied.UpstreamsState != UpstreamsKnown {
		t.Fatalf("UpstreamsState = %q (%s), want %q after a record said none", emptied.UpstreamsState, emptied.UpstreamsErr, UpstreamsKnown)
	}
	if emptied.Upstreams != nil {
		t.Fatalf("Upstreams = %+v, want nil for an empty set", emptied.Upstreams)
	}
	if _, err := emptied.UpstreamSetHash(); err != nil {
		t.Fatalf("an explicitly empty set must hash: %v", err)
	}

	unrecorded, err := resolve(t, pinnedCompose(t), bootEvents())
	if err != nil {
		t.Fatalf("ResolveRunningState() = %v", err)
	}
	if _, err := unrecorded.UpstreamSetHash(); err == nil {
		t.Fatal("an unrecorded set must not hash")
	}
}

// An unreadable record makes the set unknown without taking the rest of the answer
// down with it. Three things are asserted, and all three matter:
//
//   - the set does not degrade to "the members that parsed" — a partial set
//     understates where plaintext can go;
//   - it does not fall back to the previous record either, because this record
//     superseded that one and reporting it would be a claim about where plaintext
//     goes now;
//   - which image the broker runs still resolves, because it rests on other records.
func TestResolveReportsAnUnknownUpstreamSet(t *testing.T) {
	for _, tt := range []struct {
		name    string
		event   RuntimeEvent
		wantErr string
	}{
		{
			name:    "a member with no URL",
			event:   upstreamSetEvent("engine1"),
			wantErr: "has 1 fields",
		},
		{
			name:    "a member naming a non-URL",
			event:   upstreamSetEvent("engine1 not-a-url"),
			wantErr: "not http or https",
		},
		{
			name:    "a member whose identity is malformed",
			event:   upstreamSetEvent("engine1 http://engine-1:8000/v1 NotAnIdentity"),
			wantErr: "not lowercase alphanumeric",
		},
		{
			name:    "a name bound twice",
			event:   upstreamSetEvent("a http://x:1/v1", "a http://y:1/v1"),
			wantErr: "twice",
		},
		{
			name:    "an empty payload",
			event:   RuntimeEvent{Event: EventUpstreamSet, Payload: nil},
			wantErr: "names no member",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			events := append(bootEvents(),
				upstreamSetEvent("good http://engine-1:8000/v1"),
				tt.event,
				RuntimeEvent{Event: EventImageUpdate, Payload: imageRecordPayload("ghcr.io/0gfoundation/0g-serving-broker@" + upgradeDigest)},
			)
			state, err := resolve(t, pinnedCompose(t), events)
			if err != nil {
				t.Fatalf("ResolveRunningState() = %v, want the rest of the answer to survive", err)
			}
			if state.UpstreamsState != UpstreamsUnknown {
				t.Fatalf("UpstreamsState = %q, want %q: a record did appear, it just could not be read", state.UpstreamsState, UpstreamsUnknown)
			}
			if !strings.Contains(state.UpstreamsErr, tt.wantErr) {
				t.Fatalf("UpstreamsErr = %q, want it to contain %q", state.UpstreamsErr, tt.wantErr)
			}
			if state.Upstreams != nil {
				t.Errorf("Upstreams = %+v, want nil: neither a partial set nor the superseded one is what is permitted now", state.Upstreams)
			}
			if _, err := state.UpstreamSetHash(); err == nil {
				t.Error("UpstreamSetHash() returned a hash for an unknown set")
			}
			// The point of not failing the call.
			if state.BrokerDigest != upgradeDigest {
				t.Errorf("BrokerDigest = %q, want %q: an upstream record must not cost the answer that does not depend on it", state.BrokerDigest, upgradeDigest)
			}
		})
	}
}

// A later record repairs an unreadable one. This is the property the snapshot
// encoding buys and the per-member encoding could not: RTMR3 only appends, so the
// only available fix is a better record after the bad one, and with a cumulative
// replay there was none.
//
// The repair must not be silent, though — it erases the bad record from both values a
// caller consumes, so the change log has to keep it.
func TestResolveRepairsAnUnreadableUpstreamRecord(t *testing.T) {
	state, err := resolve(t, pinnedCompose(t), append(bootEvents(),
		upstreamSetEvent("engine1 http://engine-1:8000/v1"),
		RuntimeEvent{Event: EventUpstreamSet, Payload: []byte("garbage")},
		upstreamSetEvent("engine1 http://engine-1:8000/v1", "vendor https://vendor.example/v1 openrouter"),
	))
	if err != nil {
		t.Fatalf("ResolveRunningState() = %v", err)
	}
	if state.UpstreamsState != UpstreamsKnown {
		t.Fatalf("UpstreamsState = %q (%s), want %q: a good record after a bad one has to be believable, or one bad write bricks the boot", state.UpstreamsState, state.UpstreamsErr, UpstreamsKnown)
	}
	if state.UpstreamsErr != "" {
		t.Errorf("UpstreamsErr = %q, want it cleared by the repair", state.UpstreamsErr)
	}
	if len(state.Upstreams) != 2 {
		t.Fatalf("Upstreams = %+v, want both members of the last record", state.Upstreams)
	}
	if _, err := state.UpstreamSetHash(); err != nil {
		t.Errorf("UpstreamSetHash() = %v for a repaired set", err)
	}
	var noted bool
	for _, line := range state.UpstreamChanges {
		if strings.Contains(line, "could not be read") {
			noted = true
		}
	}
	if !noted {
		t.Errorf("UpstreamChanges = %q, want a line for the superseded unreadable record: nothing else a caller reads mentions it", state.UpstreamChanges)
	}
}

// The rewrite that motivates UpstreamChanges: an external destination becomes one
// that looks like an in-CVM container, so the deployment appears to have kept
// plaintext inside when it did not.
func TestResolveReportsARewrittenUpstream(t *testing.T) {
	state, err := resolve(t, pinnedCompose(t), append(bootEvents(),
		upstreamSetEvent("vendor https://vendor.example/v1 openrouter"),
		upstreamSetEvent("vendor http://engine-1:8000/v1"),
	))
	if err != nil {
		t.Fatalf("ResolveRunningState() = %v", err)
	}
	if len(state.UpstreamChanges) != 1 {
		t.Fatalf("UpstreamChanges = %q, want one entry: last-wins hides the rewrite from Upstreams and the hash", state.UpstreamChanges)
	}
	if !strings.Contains(state.UpstreamChanges[0], "vendor.example") || !strings.Contains(state.UpstreamChanges[0], "engine-1") {
		t.Errorf("UpstreamChanges[0] = %q, want both the old and the new destination", state.UpstreamChanges[0])
	}
	// And the consumed value does show only the final binding, which is why the above
	// has to exist.
	if len(state.Upstreams) != 1 || state.Upstreams[0].URL != "http://engine-1:8000/v1" {
		t.Errorf("Upstreams = %+v, want only the final binding", state.Upstreams)
	}
}

// The change log has to stay quiet through the traffic the design generates, or a
// caller cannot use a non-empty value as a signal at all.
func TestResolveReportsNoChangeForARepeatedRecord(t *testing.T) {
	members := []string{"engine1 http://engine-1:8000/v1", "vendor https://vendor.example/v1 openrouter"}
	state, err := resolve(t, pinnedCompose(t), append(bootEvents(),
		// The first record is the baseline, not a change. The rest are what a writer
		// re-emits on every boot and every reconcile — RTMR3 is cleared while the config
		// survives on disk, so it must re-emit.
		upstreamSetEvent(members...),
		upstreamSetEvent(members...),
		upstreamSetEvent(members...),
	))
	if err != nil {
		t.Fatalf("ResolveRunningState() = %v", err)
	}
	if state.UpstreamChanges != nil {
		t.Fatalf("UpstreamChanges = %q, want nothing: a re-emitted table is not a change", state.UpstreamChanges)
	}
	// Including the very first record, which describes a set that was not there before.
	if len(state.Upstreams) != 2 {
		t.Errorf("Upstreams = %+v, want both members", state.Upstreams)
	}
}

// A round trip must not vanish. The set ends where it started, so Upstreams and the
// hash are identical to a log that never moved — the only evidence that another
// destination was permitted in between is here.
func TestResolveReportsBothLegsOfARoundTrip(t *testing.T) {
	state, err := resolve(t, pinnedCompose(t), append(bootEvents(),
		upstreamSetEvent("a http://x:1/v1"),
		upstreamSetEvent("a http://y:1/v1"),
		upstreamSetEvent("a http://x:1/v1"),
	))
	if err != nil {
		t.Fatalf("ResolveRunningState() = %v", err)
	}
	want := []string{
		"a: http://x:1/v1 (no identity) -> http://y:1/v1 (no identity)",
		"a: http://y:1/v1 (no identity) -> http://x:1/v1 (no identity)",
	}
	if len(state.UpstreamChanges) != len(want) {
		t.Fatalf("UpstreamChanges = %q, want %q", state.UpstreamChanges, want)
	}
	for i := range want {
		if state.UpstreamChanges[i] != want[i] {
			t.Errorf("UpstreamChanges[%d] = %q, want %q", i, state.UpstreamChanges[i], want[i])
		}
	}
}

// The set is history-independent: how it got here must not change what it hashes to,
// or two deployments permitting the same destinations would derive different signing
// keys.
func TestResolveSetHashIgnoresHistory(t *testing.T) {
	final := []string{"a http://x:1/v1", "b http://y:1/v1 openrouter"}
	direct, err := resolve(t, pinnedCompose(t), append(bootEvents(), upstreamSetEvent(final...)))
	if err != nil {
		t.Fatalf("ResolveRunningState() = %v", err)
	}
	viaMoves, err := resolve(t, pinnedCompose(t), append(bootEvents(),
		upstreamSetEvent("a http://q:1/v1"),
		upstreamSetEvent(),
		upstreamSetEvent("b http://y:1/v1 openrouter", "a http://x:1/v1"),
		upstreamSetEvent(final...),
	))
	if err != nil {
		t.Fatalf("ResolveRunningState() = %v", err)
	}
	a, err := direct.UpstreamSetHash()
	if err != nil {
		t.Fatalf("UpstreamSetHash() = %v", err)
	}
	b, err := viaMoves.UpstreamSetHash()
	if err != nil {
		t.Fatalf("UpstreamSetHash() = %v", err)
	}
	if a != b {
		t.Fatalf("history changed the hash: %s vs %s", a, b)
	}
	// And the history is not lost, it is just not in the hash.
	if len(viaMoves.UpstreamChanges) == 0 {
		t.Error("UpstreamChanges = nothing, want the moves that led here")
	}
}

// The whole state has to survive transport, because this type is destined for the SDK.
//
// An earlier version of this test built each case as &RunningState{UpstreamsState: want}
// and so left the reason nil. That hid a real bug: the reason was an `error`, which
// marshals to `{}` and cannot be unmarshalled at all, so the ONE state that carries a
// reason — unknown — failed to decode, and an SDK consumer doing the ordinary
// `if err := json.Unmarshal(...)` would have discarded the whole answer for exactly the
// log a verifier most needs. So each case is now populated the way the resolver
// populates it.
func TestRunningStateSurvivesJSON(t *testing.T) {
	tests := []struct {
		name string
		in   RunningState
	}{
		{name: "unrecorded", in: RunningState{UpstreamsState: UpstreamsUnrecorded}},
		{
			name: "known",
			in: RunningState{
				UpstreamsState:  UpstreamsKnown,
				Upstreams:       []Upstream{{Name: "a", URL: "http://x:1/v1", Identity: "vendor"}},
				UpstreamChanges: []string{"a: http://y:1/v1 (no identity) -> http://x:1/v1 (vendor)"},
			},
		},
		{
			name: "known and empty",
			in:   RunningState{UpstreamsState: UpstreamsKnown},
		},
		{
			name: "unknown carries its reason",
			in:   RunningState{UpstreamsState: UpstreamsUnknown, UpstreamsErr: "payload has 1 fields"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			blob, err := json.Marshal(&tt.in)
			if err != nil {
				t.Fatalf("Marshal() = %v", err)
			}
			var back RunningState
			if err := json.Unmarshal(blob, &back); err != nil {
				t.Fatalf("Unmarshal() = %v — the far side cannot read this state at all", err)
			}
			if back.UpstreamsState != tt.in.UpstreamsState {
				t.Errorf("UpstreamsState = %q, want %q", back.UpstreamsState, tt.in.UpstreamsState)
			}
			if back.UpstreamsErr != tt.in.UpstreamsErr {
				t.Errorf("UpstreamsErr = %q, want %q", back.UpstreamsErr, tt.in.UpstreamsErr)
			}
			if len(back.Upstreams) != len(tt.in.Upstreams) {
				t.Errorf("Upstreams = %+v, want %+v", back.Upstreams, tt.in.Upstreams)
			}
			if len(back.UpstreamChanges) != len(tt.in.UpstreamChanges) {
				t.Errorf("UpstreamChanges = %q, want %q", back.UpstreamChanges, tt.in.UpstreamChanges)
			}
			// The point of the three states: after transport, unknown must still refuse to
			// hash rather than passing for known-and-empty.
			_, hashErr := back.UpstreamSetHash()
			if (hashErr == nil) != (tt.in.UpstreamsState == UpstreamsKnown) {
				t.Errorf("UpstreamSetHash() error = %v for state %q", hashErr, tt.in.UpstreamsState)
			}
		})
	}
}

// An unreadable record must not suppress the change log across itself. Whoever writes
// the records also chooses what garbage to write, so if a bad record reset the
// comparison baseline, appending one before a rewrite would be a way to hide exactly
// the transition this log exists to show.
func TestResolveReportsAMoveStraddlingAnUnreadableRecord(t *testing.T) {
	state, err := resolve(t, pinnedCompose(t), append(bootEvents(),
		upstreamSetEvent("vendor https://vendor.example/v1 openrouter"),
		RuntimeEvent{Event: EventUpstreamSet, Payload: []byte("garbage")},
		upstreamSetEvent("vendor http://engine-1:8000/v1"),
	))
	if err != nil {
		t.Fatalf("ResolveRunningState() = %v", err)
	}
	if state.UpstreamsState != UpstreamsKnown {
		t.Fatalf("UpstreamsState = %q (%s), want %q", state.UpstreamsState, state.UpstreamsErr, UpstreamsKnown)
	}
	var move bool
	for _, line := range state.UpstreamChanges {
		if strings.Contains(line, "vendor.example") && strings.Contains(line, "engine-1") {
			move = true
		}
	}
	if !move {
		t.Fatalf("UpstreamChanges = %q, want the vendor rewrite: an unreadable record in between must not hide it", state.UpstreamChanges)
	}
}
