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
func TestUpstreamChangesAreReported(t *testing.T) {
	tests := []struct {
		name        string
		steps       []string
		wantChanges []string
	}{
		{
			name:        "a bare re-add to another URL is a move",
			steps:       []string{"add|vendor https://vendor.example/v1", "add|vendor http://engine-1:8000/v1"},
			wantChanges: []string{"vendor: https://vendor.example/v1 (no identity) -> http://engine-1:8000/v1 (no identity)"},
		},
		{
			// The same rewrite, spelled as two records. The withdrawal is the transition
			// that happened; the following add is a fresh binding of a name the set no
			// longer held. A reader sees the vendor go away.
			name:        "remove then add elsewhere reports the withdrawal",
			steps:       []string{"add|vendor https://vendor.example/v1", "remove|vendor", "add|vendor http://engine-1:8000/v1"},
			wantChanges: []string{"vendor: https://vendor.example/v1 (no identity) -> withdrawn"},
		},
		{
			// A withdrawal on its own. The remaining set is all in-CVM, which reads as
			// "plaintext never left" unless this line says otherwise.
			name:        "withdrawing a vendor is reported",
			steps:       []string{"add|vendor https://vendor.example/v1 openrouter", "add|engine1 http://engine-1:8000/v1", "remove|vendor"},
			wantChanges: []string{"vendor: https://vendor.example/v1 (openrouter) -> withdrawn"},
		},
		{
			// The noise case: the design requires the writer to re-emit its whole table on
			// each reconcile, so a genuine change must land once, not once per reconcile.
			name:        "re-emitting a changed table repeatedly reports the change once",
			steps:       []string{"add|a http://x:1/v1", "add|a http://y:1/v1", "add|a http://y:1/v1", "add|a http://y:1/v1"},
			wantChanges: []string{"a: http://x:1/v1 (no identity) -> http://y:1/v1 (no identity)"},
		},
		{
			name:        "adding an identity to a recorded name is a move, and the message says so",
			steps:       []string{"add|a http://x:1/v1", "add|a http://x:1/v1 vendor"},
			wantChanges: []string{"a: http://x:1/v1 (no identity) -> http://x:1/v1 (vendor)"},
		},
		{
			// The fail-open direction: an attributable vendor becomes one with no
			// attribution. Printing only URLs made this report two identical halves.
			name:        "dropping an identity is a move the message shows",
			steps:       []string{"add|vendor https://vendor.example/v1 openrouter", "add|vendor https://vendor.example/v1"},
			wantChanges: []string{"vendor: https://vendor.example/v1 (openrouter) -> https://vendor.example/v1 (no identity)"},
		},
		{
			// The case that must NOT be a move, or a writer re-publishing its whole
			// table at boot would make every name look rewritten.
			name:        "re-emitting the identical record is not a move",
			steps:       []string{"add|a http://x:1/v1 vendor", "add|a http://x:1/v1 vendor"},
			wantChanges: nil,
		},
		{
			name:        "a plain add is not a move",
			steps:       []string{"add|a http://x:1/v1", "add|b http://y:1/v1"},
			wantChanges: nil,
		},
		{
			// A round trip must not vanish. Comparing the first binding against the final
			// one would report nothing here, even though Y was permitted in between.
			name:  "moving away and back reports both legs",
			steps: []string{"add|a http://x:1/v1", "add|a http://y:1/v1", "add|a http://x:1/v1"},
			wantChanges: []string{
				"a: http://x:1/v1 (no identity) -> http://y:1/v1 (no identity)",
				"a: http://y:1/v1 (no identity) -> http://x:1/v1 (no identity)",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, err := applyAll(t, tt.steps...)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			got := s.changes()
			if len(got) != len(tt.wantChanges) {
				t.Fatalf("moves = %q, want %q", got, tt.wantChanges)
			}
			for i := range tt.wantChanges {
				if got[i] != tt.wantChanges[i] {
					t.Errorf("moves[%d] = %q, want %q", i, got[i], tt.wantChanges[i])
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
		RuntimeEvent{Event: EventUpstreamSetComplete, Payload: []byte("1")},
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

// Nothing but a matching completeness marker produces a set. These are the ways a log
// can fall short of one, and every one of them must land somewhere that is not
// UpstreamsKnown — because the fail-open reading ("these are all the destinations") is
// available from any prefix of a batch.
func TestResolveNeedsACompletenessMarker(t *testing.T) {
	tests := []struct {
		name      string
		events    []RuntimeEvent
		wantState string
		wantErr   string
	}{
		{
			name:      "a lone remove of a name nothing added",
			events:    []RuntimeEvent{{Event: EventUpstreamRemove, Payload: []byte("ghost")}},
			wantState: UpstreamsIncomplete,
			wantErr:   "batch in progress",
		},
		{
			// The truncation that matters: the writer emits its in-CVM engines, is killed
			// before the external vendor, and a reader that promoted on the first add
			// would report an all-in-CVM set with a valid hash while the config still
			// routes some models outside.
			name: "adds with no marker after them",
			events: []RuntimeEvent{
				{Event: EventUpstreamAdd, Payload: []byte("engine1 http://engine-1:8000/v1")},
				{Event: EventUpstreamAdd, Payload: []byte("engine2 http://engine-2:8000/v1")},
			},
			wantState: UpstreamsIncomplete,
			wantErr:   "batch in progress",
		},
		{
			name: "a marker that lands after the batch reopens",
			events: []RuntimeEvent{
				{Event: EventUpstreamAdd, Payload: []byte("engine1 http://engine-1:8000/v1")},
				{Event: EventUpstreamSetComplete, Payload: []byte("1")},
				{Event: EventUpstreamAdd, Payload: []byte("engine2 http://engine-2:8000/v1")},
			},
			wantState: UpstreamsIncomplete,
			wantErr:   "batch in progress",
		},
		{
			// A count that disagrees means writer and reader do not have the same set,
			// and neither can say which is right.
			name: "a marker whose count disagrees",
			events: []RuntimeEvent{
				{Event: EventUpstreamAdd, Payload: []byte("engine1 http://engine-1:8000/v1")},
				{Event: EventUpstreamSetComplete, Payload: []byte("2")},
			},
			wantState: UpstreamsUnknown,
			wantErr:   "do not have the same set",
		},
		{
			name: "a marker that is not a number",
			events: []RuntimeEvent{
				{Event: EventUpstreamAdd, Payload: []byte("engine1 http://engine-1:8000/v1")},
				{Event: EventUpstreamSetComplete, Payload: []byte("lots")},
			},
			wantState: UpstreamsUnknown,
			wantErr:   "is not a member count",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state, err := resolve(t, pinnedCompose(t), append(bootEvents(), tt.events...))
			if err != nil {
				t.Fatalf("ResolveRunningState() = %v", err)
			}
			if state.UpstreamsState != tt.wantState {
				t.Errorf("UpstreamsState = %q, want %q", state.UpstreamsState, tt.wantState)
			}
			if !strings.Contains(state.UpstreamsErr, tt.wantErr) {
				t.Errorf("UpstreamsErr = %q, want it to contain %q", state.UpstreamsErr, tt.wantErr)
			}
			if _, err := state.UpstreamSetHash(); err == nil {
				t.Error("a log without a matching marker produced a set hash, which the derivation path would bind")
			}
		})
	}
}

// And the marker does its job: a closed batch is a set.
func TestResolveAcceptsAClosedBatch(t *testing.T) {
	state, err := resolve(t, pinnedCompose(t), append(bootEvents(),
		RuntimeEvent{Event: EventUpstreamAdd, Payload: []byte("engine1 http://engine-1:8000/v1")},
		RuntimeEvent{Event: EventUpstreamAdd, Payload: []byte("vendor https://vendor.example/v1 openrouter")},
		RuntimeEvent{Event: EventUpstreamSetComplete, Payload: []byte("2")},
	))
	if err != nil {
		t.Fatalf("ResolveRunningState() = %v", err)
	}
	if state.UpstreamsState != UpstreamsKnown {
		t.Fatalf("UpstreamsState = %q (%s), want %q", state.UpstreamsState, state.UpstreamsErr, UpstreamsKnown)
	}
	if state.UpstreamsErr != "" {
		t.Errorf("UpstreamsErr = %q, want it cleared once the batch closed", state.UpstreamsErr)
	}
	if len(state.Upstreams) != 2 {
		t.Errorf("Upstreams = %+v, want both members", state.Upstreams)
	}
	if _, err := state.UpstreamSetHash(); err != nil {
		t.Errorf("UpstreamSetHash() = %v for a closed batch", err)
	}
}

// But a bound of zero is still expressible, because that path went through an add.
func TestResolveAllowsAnExplicitEmptySet(t *testing.T) {
	state, err := resolve(t, pinnedCompose(t), append(bootEvents(),
		RuntimeEvent{Event: EventUpstreamAdd, Payload: []byte("engine1 http://engine-1:8000/v1")},
		RuntimeEvent{Event: EventUpstreamRemove, Payload: []byte("engine1")},
		RuntimeEvent{Event: EventUpstreamSetComplete, Payload: []byte("0")},
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
