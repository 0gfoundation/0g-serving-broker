package attest

import (
	"strings"
	"testing"
)

// upstream_test.go exercises the replay in isolation. These go through
// ResolveRunningState, so they also cover the dispatch loop, the interaction with the
// other record types, and the fact that an upstream record does not disturb them.
func TestResolveReplaysUpstreamRecords(t *testing.T) {
	compose := pinnedCompose(t)
	events := append(bootEvents(),
		RuntimeEvent{Event: EventUpstreamAdd, Payload: []byte("engine1 http://engine-1:8000/v1")},
		RuntimeEvent{Event: EventUpstreamAdd, Payload: []byte("vendor https://vendor.example/v1 0xabc")},
		RuntimeEvent{Event: EventImageUpdate, Payload: imageRecordPayload("ghcr.io/0gfoundation/0g-serving-broker@" + upgradeDigest)},
		RuntimeEvent{Event: EventUpstreamAdd, Payload: []byte("engine2 http://engine-2:8000/v1")},
		RuntimeEvent{Event: EventUpstreamRemove, Payload: []byte("vendor")},
	)

	state, err := resolve(t, compose, events)
	if err != nil {
		t.Fatalf("ResolveRunningState() = %v", err)
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
	// An upstream record must not disturb what the other records established.
	if state.BrokerDigest != upgradeDigest {
		t.Errorf("BrokerDigest = %q, want %q", state.BrokerDigest, upgradeDigest)
	}
	if state.DigestSource != DigestSourceEvent {
		t.Errorf("DigestSource = %q, want %q", state.DigestSource, DigestSourceEvent)
	}
}

// No records at all must leave the field nil rather than empty, because nil is what
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
}

// One unreadable upstream record fails the whole resolution. Unlike an image record,
// where only the last matters, the set is cumulative — reporting the members that
// happened to parse would understate where plaintext can go.
func TestResolveRefusesAnUnreadableUpstreamRecord(t *testing.T) {
	for _, tt := range []struct {
		name    string
		event   RuntimeEvent
		wantErr string
	}{
		{
			name:    "a malformed add",
			event:   RuntimeEvent{Event: EventUpstreamAdd, Payload: []byte("engine1")},
			wantErr: "want a name, a base URL",
		},
		{
			name:    "an add naming a non-URL",
			event:   RuntimeEvent{Event: EventUpstreamAdd, Payload: []byte("engine1 not-a-url")},
			wantErr: "not http or https",
		},
		{
			name:    "a remove of something never added",
			event:   RuntimeEvent{Event: EventUpstreamRemove, Payload: []byte("ghost")},
			wantErr: "which no zg-upstream-add record added",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			events := append(bootEvents(),
				RuntimeEvent{Event: EventUpstreamAdd, Payload: []byte("good http://engine-1:8000/v1")},
				tt.event,
			)
			_, err := resolve(t, pinnedCompose(t), events)
			if err == nil {
				t.Fatal("want an error, got none")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("want an error containing %q, got %v", tt.wantErr, err)
			}
		})
	}
}
