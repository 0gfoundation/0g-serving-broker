package attest

import (
	"encoding/json"
	"strings"
	"testing"
)

// engineSetEvent builds an EventEngineSet record the way a writer would.
func engineSetEvent(lines ...string) RuntimeEvent {
	return RuntimeEvent{Event: EventEngineSet, Payload: []byte(engineSet(lines...))}
}

// engine_test.go exercises one payload. These go through ResolveRunningState, which is
// where the dispatch, last-wins, and the handoff into classifyUpstreams live — and where
// six mutations survived until this file existed, because nothing tested the integration
// at all.
func TestResolveReadsTheLastEngineRecord(t *testing.T) {
	compose := pinnedCompose(t)
	events := append(bootEvents(),
		engineSetEvent(engineLine("first", testEngineImage, "0", "--x")),
		engineSetEvent(
			engineLine("a", testEngineImage, "0", "--x"),
			engineLine("b", testEngineImage, "1", "--y"),
		),
	)

	state, err := resolve(t, compose, events)
	if err != nil {
		t.Fatalf("ResolveRunningState() = %v", err)
	}
	if state.EnginesState != EnginesKnown {
		t.Fatalf("EnginesState = %q (%s), want %q", state.EnginesState, state.EnginesErr, EnginesKnown)
	}
	if len(state.Engines) != 2 {
		t.Fatalf("Engines = %+v, want the LAST record's two", state.Engines)
	}
	for i, want := range []string{"a", "b"} {
		if state.Engines[i].Name != want {
			t.Errorf("Engines[%d].Name = %q, want %q", i, state.Engines[i].Name, want)
		}
	}
}

// No record is not the same answer as an empty one, and neither is the same as
// unreadable. The zero value must read as unrecorded.
func TestResolveDistinguishesTheThreeEngineStates(t *testing.T) {
	compose := pinnedCompose(t)

	for _, tc := range []struct {
		name      string
		events    []RuntimeEvent
		wantState string
		wantLen   int
		wantErr   bool // EnginesErr non-empty
	}{
		{"no record at all", bootEvents(), EnginesUnrecorded, 0, false},
		{"an empty set", append(bootEvents(), engineSetEvent()), EnginesKnown, 0, false},
		{"an unreadable record", append(bootEvents(),
			RuntimeEvent{Event: EventEngineSet, Payload: []byte("garbage")}), EnginesUnknown, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state, err := resolve(t, compose, tc.events)
			if err != nil {
				t.Fatalf("ResolveRunningState() = %v", err)
			}
			if state.EnginesState != tc.wantState {
				t.Errorf("EnginesState = %q, want %q", state.EnginesState, tc.wantState)
			}
			if len(state.Engines) != tc.wantLen {
				t.Errorf("Engines = %+v, want %d", state.Engines, tc.wantLen)
			}
			if (state.EnginesErr != "") != tc.wantErr {
				t.Errorf("EnginesErr = %q, want non-empty = %v", state.EnginesErr, tc.wantErr)
			}
		})
	}
}

// An unreadable engine record must NOT fail the whole call, and must NOT leave the
// engines it superseded standing.
//
// Reported rather than returned, for the reason the upstream case gives: which image the
// broker runs and which keys it holds come from other records, and an unreadable engine
// record must not take them down. Cleared rather than kept, because reporting engines the
// log has moved past would be a claim about what is running now.
func TestResolveReportsAnUnreadableEngineRecordWithoutLosingTheRest(t *testing.T) {
	compose := pinnedCompose(t)
	events := append(bootEvents(),
		engineSetEvent(engineLine("gone", testEngineImage, "0", "--x")),
		RuntimeEvent{Event: EventEngineSet, Payload: []byte("count=1\nnot enough fields")},
	)

	state, err := resolve(t, compose, events)
	if err != nil {
		t.Fatalf("ResolveRunningState() = %v, want the rest of the answer to hold", err)
	}
	if state.EnginesState != EnginesUnknown {
		t.Fatalf("EnginesState = %q, want %q", state.EnginesState, EnginesUnknown)
	}
	if len(state.Engines) != 0 {
		t.Errorf("Engines = %+v, want the superseded set cleared", state.Engines)
	}
	if state.EnginesErr == "" {
		t.Error("EnginesErr is empty, so nothing says why the set is unknown")
	}
	// The rest of the answer still holds, which is the whole point of reporting rather
	// than returning.
	if state.ComposeHash == "" || state.BrokerDigest == "" {
		t.Errorf("the unreadable engine record took the rest down: hash=%q digest=%q", state.ComposeHash, state.BrokerDigest)
	}
}

// A later good record repairs an earlier unreadable one, because only the last record
// decides.
func TestResolveRepairsAnUnreadableEngineRecord(t *testing.T) {
	compose := pinnedCompose(t)
	events := append(bootEvents(),
		RuntimeEvent{Event: EventEngineSet, Payload: []byte("garbage")},
		engineSetEvent(engineLine("fixed", testEngineImage, "0", "--x")),
	)

	state, err := resolve(t, compose, events)
	if err != nil {
		t.Fatalf("ResolveRunningState() = %v", err)
	}
	if state.EnginesState != EnginesKnown || len(state.Engines) != 1 || state.Engines[0].Name != "fixed" {
		t.Errorf("EnginesState=%q Engines=%+v, want the repair to win", state.EnginesState, state.Engines)
	}
	if state.EnginesErr != "" {
		t.Errorf("EnginesErr = %q, want it cleared by the repair", state.EnginesErr)
	}
}

// The handoff: a destination pointing at a RECORDED container must classify, and must
// carry ImageSourceRecord so a caller can tell it from a compose service.
//
// This is the property the whole change exists for. Without it a dynamically created
// engine reads as an external vendor — plaintext goes to a container inside the CVM and
// the verifier cannot see that.
func TestResolveClassifiesADestinationBackedByARecordedEngine(t *testing.T) {
	compose := pinnedCompose(t)
	events := append(bootEvents(),
		engineSetEvent(engineLine("dsv4flash", testEngineImage, "7", "--tp 1")),
		upstreamSetEvent("dsv4flash http://dsv4flash:8000/v1"),
	)

	state, err := resolve(t, compose, events)
	if err != nil {
		t.Fatalf("ResolveRunningState() = %v", err)
	}
	if len(state.Upstreams) != 1 {
		t.Fatalf("Upstreams = %+v, want one", state.Upstreams)
	}
	got := state.Upstreams[0]
	if got.ComposeService != "dsv4flash" {
		t.Errorf("ComposeService = %q, want the recorded container", got.ComposeService)
	}
	if got.PinnedImage != testEngineImage {
		t.Errorf("PinnedImage = %q, want %q", got.PinnedImage, testEngineImage)
	}
	if got.ImageSource != ImageSourceRecord {
		t.Errorf("ImageSource = %q, want %q: a caller must not read the CVM's claim as hardware-bound", got.ImageSource, ImageSourceRecord)
	}
}

// And the order that matters: the engine record must not be able to describe a
// destination the compose already answers for.
func TestResolveLetsTheComposeWinOverARecordedEngine(t *testing.T) {
	compose := pinnedCompose(t)
	// mysql is a service the pinned compose declares; the record claims the same name.
	events := append(bootEvents(),
		engineSetEvent(engineLine("mysql", testEngineImage, "0", "--x")),
		upstreamSetEvent("m http://mysql:8000/v1"),
	)

	state, err := resolve(t, compose, events)
	if err != nil {
		t.Fatalf("ResolveRunningState() = %v", err)
	}
	if len(state.Upstreams) != 1 {
		t.Fatalf("Upstreams = %+v, want one", state.Upstreams)
	}
	got := state.Upstreams[0]
	if got.ImageSource != ImageSourceCompose {
		t.Fatalf("ImageSource = %q, want %q: the record must not override the compose", got.ImageSource, ImageSourceCompose)
	}
	if got.PinnedImage == testEngineImage {
		t.Error("PinnedImage came from the record, not the compose")
	}
}

// An unreadable engine record must leave a destination backed by a recorded container
// UNCLASSIFIED rather than classified from a stale set — the fail-closed direction, since
// a destination this process cannot name reads as external.
func TestResolveLeavesADestinationUnclassifiedWhenTheEngineRecordIsUnreadable(t *testing.T) {
	compose := pinnedCompose(t)
	events := append(bootEvents(),
		engineSetEvent(engineLine("dsv4flash", testEngineImage, "7", "--tp 1")),
		RuntimeEvent{Event: EventEngineSet, Payload: []byte("garbage")},
		upstreamSetEvent("dsv4flash http://dsv4flash:8000/v1"),
	)

	state, err := resolve(t, compose, events)
	if err != nil {
		t.Fatalf("ResolveRunningState() = %v", err)
	}
	if len(state.Upstreams) != 1 {
		t.Fatalf("Upstreams = %+v, want one", state.Upstreams)
	}
	if got := state.Upstreams[0]; got.ComposeService != "" || got.ImageSource != "" {
		t.Errorf("classified as %q/%q from a superseded record; want unclassified", got.ComposeService, got.ImageSource)
	}
}

// The engine record does not enter UpstreamSetHash. Two deployments permitting the same
// destinations must agree on the hash whatever containers they happen to have created,
// and that hash is destined for the signing key's derivation path.
func TestARecordedEngineDoesNotChangeTheUpstreamSetHash(t *testing.T) {
	compose := pinnedCompose(t)
	upstreams := upstreamSetEvent("dsv4flash http://dsv4flash:8000/v1")

	withEngine, err := resolve(t, compose, append(bootEvents(),
		engineSetEvent(engineLine("dsv4flash", testEngineImage, "7", "--tp 1")), upstreams))
	if err != nil {
		t.Fatalf("with the engine record: %v", err)
	}
	without, err := resolve(t, compose, append(bootEvents(), upstreams))
	if err != nil {
		t.Fatalf("without it: %v", err)
	}

	h1, e1 := withEngine.UpstreamSetHash()
	h2, e2 := without.UpstreamSetHash()
	if e1 != nil || e2 != nil {
		t.Fatalf("hash: %v / %v", e1, e2)
	}
	if h1 != h2 {
		t.Errorf("the engine record changed the set hash: %s vs %s", h1, h2)
	}
}

// The three engine fields survive a JSON round trip, for the reason the upstream fields
// had to: this type is transported, and a state that cannot be decoded is worse than one
// that reports badly.
func TestEngineStateSurvivesJSON(t *testing.T) {
	compose := pinnedCompose(t)
	state, err := resolve(t, compose, append(bootEvents(),
		RuntimeEvent{Event: EventEngineSet, Payload: []byte("garbage")}))
	if err != nil {
		t.Fatalf("ResolveRunningState() = %v", err)
	}
	blob, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("Marshal() = %v", err)
	}
	var back RunningState
	if err := json.Unmarshal(blob, &back); err != nil {
		t.Fatalf("Unmarshal() = %v — the far side cannot read this state at all", err)
	}
	if back.EnginesState != EnginesUnknown {
		t.Errorf("EnginesState = %q after a round trip, want %q", back.EnginesState, EnginesUnknown)
	}
	if back.EnginesErr == "" || !strings.Contains(back.EnginesErr, EventEngineSet) {
		t.Errorf("EnginesErr = %q after a round trip, want the reason preserved", back.EnginesErr)
	}
}
