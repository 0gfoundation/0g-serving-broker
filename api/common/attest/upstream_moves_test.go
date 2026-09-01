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
			wantMoves: []string{"vendor: https://vendor.example/v1 -> http://engine-1:8000/v1"},
		},
		{
			// The same rewrite, spelled as two records. Equally invisible to the
			// consumed values, so equally reported.
			name:      "remove then add elsewhere is also a move",
			steps:     []string{"add|vendor https://vendor.example/v1", "remove|vendor", "add|vendor http://engine-1:8000/v1"},
			wantMoves: []string{"vendor: https://vendor.example/v1 -> http://engine-1:8000/v1"},
		},
		{
			name:      "adding an identity to a recorded name is a move",
			steps:     []string{"add|a http://x:1/v1", "add|a http://x:1/v1 vendor"},
			wantMoves: []string{"a: http://x:1/v1 -> http://x:1/v1"},
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
			wantMoves: []string{"a: http://x:1/v1 -> http://y:1/v1"},
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

// UpstreamsState has to survive transport, because this type is destined for the SDK
// and an `error` field marshals to `{}` — a verifier on the far side would otherwise
// render an unreplayable log as "records appeared, zero permitted destinations".
func TestUpstreamsStateSurvivesJSON(t *testing.T) {
	for _, want := range []string{UpstreamsUnrecorded, UpstreamsKnown, UpstreamsUnknown} {
		t.Run("state="+want, func(t *testing.T) {
			blob, err := json.Marshal(&RunningState{UpstreamsState: want})
			if err != nil {
				t.Fatalf("Marshal() = %v", err)
			}
			var back RunningState
			if err := json.Unmarshal(blob, &back); err != nil {
				t.Fatalf("Unmarshal() = %v", err)
			}
			if back.UpstreamsState != want {
				t.Fatalf("UpstreamsState = %q after a round trip, want %q", back.UpstreamsState, want)
			}
			// The point: unknown must not be mistaken for known-and-empty on the far side.
			if want == UpstreamsUnknown {
				if _, err := back.UpstreamSetHash(); err == nil {
					t.Fatal("an unknown set hashed after transport")
				}
			}
		})
	}
}
