package attest

import (
	"encoding/json"
	"strings"
	"testing"
)

// A rebinding must reach the values a caller consumes. Last-wins is what keeps a boot
// re-emit from bricking the set, and the cost of it is that Upstreams and
// UpstreamSetHash show only the final binding — so the move has to be reported
// separately or it is invisible to everyone who does not walk the raw events.
func TestUpstreamMovesAreReported(t *testing.T) {
	tests := []struct {
		name      string
		steps     []string
		wantMoves []string
	}{
		{
			name:      "a bare re-add to another URL is a move",
			steps:     []string{"add|vendor https://vendor.example/v1", "add|vendor http://engine-1:8000/v1"},
			wantMoves: []string{"vendor: https://vendor.example/v1 (no identity) -> http://engine-1:8000/v1 (no identity)"},
		},
		{
			// The same rewrite, spelled as two records. Equally invisible to the
			// consumed values, so equally reported.
			name:      "remove then add elsewhere is also a move",
			steps:     []string{"add|vendor https://vendor.example/v1", "remove|vendor", "add|vendor http://engine-1:8000/v1"},
			wantMoves: []string{"vendor: https://vendor.example/v1 (no identity) -> http://engine-1:8000/v1 (no identity)"},
		},
		{
			name:      "adding an identity to a recorded name is a move, and the message says so",
			steps:     []string{"add|a http://x:1/v1", "add|a http://x:1/v1 vendor"},
			wantMoves: []string{"a: http://x:1/v1 (no identity) -> http://x:1/v1 (vendor)"},
		},
		{
			// The fail-open direction: an attributable vendor becomes one with no
			// attribution. Printing only URLs made this report two identical halves.
			name:      "dropping an identity is a move the message shows",
			steps:     []string{"add|vendor https://vendor.example/v1 openrouter", "add|vendor https://vendor.example/v1"},
			wantMoves: []string{"vendor: https://vendor.example/v1 (openrouter) -> https://vendor.example/v1 (no identity)"},
		},
		{
			// The case that must NOT be a move, or a writer re-publishing its whole
			// table at boot would make every name look rewritten.
			name:      "re-emitting the identical record is not a move",
			steps:     []string{"add|a http://x:1/v1 vendor", "add|a http://x:1/v1 vendor"},
			wantMoves: nil,
		},
		{
			name:      "a plain add is not a move",
			steps:     []string{"add|a http://x:1/v1", "add|b http://y:1/v1"},
			wantMoves: nil,
		},
		{
			// Comparison is against the first binding of the boot, so returning a name
			// to where it started still counts: it meant something else in between.
			name:      "moving away and back still reports the round trip",
			steps:     []string{"add|a http://x:1/v1", "add|a http://y:1/v1", "add|a http://x:1/v1"},
			wantMoves: []string{"a: http://x:1/v1 (no identity) -> http://y:1/v1 (no identity)"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, err := applyAll(t, tt.steps...)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			got := s.moves()
			if len(got) != len(tt.wantMoves) {
				t.Fatalf("moves = %q, want %q", got, tt.wantMoves)
			}
			for i := range tt.wantMoves {
				if got[i] != tt.wantMoves[i] {
					t.Errorf("moves[%d] = %q, want %q", i, got[i], tt.wantMoves[i])
				}
			}
		})
	}
}

// The rewrite that motivates the field: an external destination becomes one that
// looks like an in-CVM container, so the deployment appears to have kept plaintext
// inside when it did not.
func TestResolveReportsARewrittenUpstream(t *testing.T) {
	state, err := resolve(t, pinnedCompose(t), append(bootEvents(),
		RuntimeEvent{Event: EventUpstreamAdd, Payload: []byte("vendor https://vendor.example/v1 openrouter")},
		RuntimeEvent{Event: EventUpstreamAdd, Payload: []byte("vendor http://engine-1:8000/v1")},
	))
	if err != nil {
		t.Fatalf("ResolveRunningState() = %v", err)
	}
	if len(state.UpstreamMoves) != 1 {
		t.Fatalf("UpstreamMoves = %q, want one entry: last-wins hides the rewrite from Upstreams and the hash", state.UpstreamMoves)
	}
	if !strings.Contains(state.UpstreamMoves[0], "vendor.example") || !strings.Contains(state.UpstreamMoves[0], "engine-1") {
		t.Errorf("UpstreamMoves[0] = %q, want both the old and the new destination", state.UpstreamMoves[0])
	}
	// And the consumed value does show only the final binding, which is why the above
	// has to exist.
	if len(state.Upstreams) != 1 || state.Upstreams[0].URL != "http://engine-1:8000/v1" {
		t.Errorf("Upstreams = %+v, want only the final binding", state.Upstreams)
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
				UpstreamsState: UpstreamsKnown,
				Upstreams:      []Upstream{{Name: "a", URL: "http://x:1/v1", Identity: "vendor"}},
				UpstreamMoves:  []string{"a: http://y:1/v1 (no identity) -> http://x:1/v1 (vendor)"},
			},
		},
		{
			name: "unknown carries its reason",
			in:   RunningState{UpstreamsState: UpstreamsUnknown, UpstreamsErr: "payload has 1 field"},
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
			if len(back.UpstreamMoves) != len(tt.in.UpstreamMoves) {
				t.Errorf("UpstreamMoves = %q, want %q", back.UpstreamMoves, tt.in.UpstreamMoves)
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

// A remove of a name nothing added asserts nothing, so on its own it must not promote
// an unbounded deployment into "recorded, and bounded to nothing" — the strongest
// claim, reachable from the weakest record.
func TestResolveDoesNotPromoteOnALoneRemove(t *testing.T) {
	state, err := resolve(t, pinnedCompose(t), append(bootEvents(),
		RuntimeEvent{Event: EventUpstreamRemove, Payload: []byte("ghost")},
	))
	if err != nil {
		t.Fatalf("ResolveRunningState() = %v", err)
	}
	if state.UpstreamsState != UpstreamsUnrecorded {
		t.Fatalf("UpstreamsState = %q, want %q: a remove that removed nothing established nothing", state.UpstreamsState, UpstreamsUnrecorded)
	}
	if _, err := state.UpstreamSetHash(); err == nil {
		t.Fatal("a lone remove produced a set hash, which the derivation path would bind")
	}
}

// But a bound of zero is still expressible, because that path went through an add.
func TestResolveAllowsAnExplicitEmptySet(t *testing.T) {
	state, err := resolve(t, pinnedCompose(t), append(bootEvents(),
		RuntimeEvent{Event: EventUpstreamAdd, Payload: []byte("engine1 http://engine-1:8000/v1")},
		RuntimeEvent{Event: EventUpstreamRemove, Payload: []byte("engine1")},
	))
	if err != nil {
		t.Fatalf("ResolveRunningState() = %v", err)
	}
	if state.UpstreamsState != UpstreamsKnown {
		t.Fatalf("UpstreamsState = %q, want %q", state.UpstreamsState, UpstreamsKnown)
	}
	if _, err := state.UpstreamSetHash(); err != nil {
		t.Fatalf("an explicitly emptied set must hash: %v", err)
	}
}
