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

// An unreadable upstream record makes the SET unknown without taking the rest of the
// answer down with it. Two things are being asserted, and both matter:
//
//   - the set does not degrade to "the members that parsed" — a partial set
//     understates where plaintext can go;
//   - which image the broker runs still resolves, because it rests on other records
//     and an append-only log gives an upstream record no corrective successor.
func TestResolveReportsAnUnknownUpstreamSet(t *testing.T) {
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
			name:    "an add whose identity is malformed",
			event:   RuntimeEvent{Event: EventUpstreamAdd, Payload: []byte("engine1 http://engine-1:8000/v1 NotAnIdentity")},
			wantErr: "which is not lowercase alphanumeric",
		},
		{
			name:    "a remove naming something unparseable",
			event:   RuntimeEvent{Event: EventUpstreamRemove, Payload: []byte("Ghost")},
			wantErr: "which is not a lowercase alphanumeric name",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			events := append(bootEvents(),
				RuntimeEvent{Event: EventUpstreamAdd, Payload: []byte("good http://engine-1:8000/v1")},
				tt.event,
				RuntimeEvent{Event: EventImageUpdate, Payload: imageRecordPayload("ghcr.io/0gfoundation/0g-serving-broker@" + upgradeDigest)},
			)
			state, err := resolve(t, pinnedCompose(t), events)
			if err != nil {
				t.Fatalf("ResolveRunningState() = %v, want the rest of the answer to survive", err)
			}
			if state.UpstreamsErr == nil {
				t.Fatal("want UpstreamsErr set, got nil")
			}
			if !strings.Contains(state.UpstreamsErr.Error(), tt.wantErr) {
				t.Fatalf("UpstreamsErr = %v, want it to contain %q", state.UpstreamsErr, tt.wantErr)
			}
			if state.Upstreams != nil {
				t.Errorf("Upstreams = %+v, want nil when the set is unknown — a partial set understates where plaintext can go", state.Upstreams)
			}
			if !state.UpstreamsRecorded {
				t.Error("UpstreamsRecorded = false, want true: records did appear, they just could not be replayed")
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

// Once unknown, a later well-formed record must not repair the set: the earlier
// members are still unreadable, so reporting anything would report a subset.
func TestResolveKeepsAnUnknownUpstreamSetUnknown(t *testing.T) {
	events := append(bootEvents(),
		RuntimeEvent{Event: EventUpstreamAdd, Payload: []byte("broken")},
		RuntimeEvent{Event: EventUpstreamAdd, Payload: []byte("good http://engine-1:8000/v1")},
	)
	state, err := resolve(t, pinnedCompose(t), events)
	if err != nil {
		t.Fatalf("ResolveRunningState() = %v", err)
	}
	if state.UpstreamsErr == nil {
		t.Fatal("a later good record cleared UpstreamsErr")
	}
	if state.Upstreams != nil {
		t.Fatalf("Upstreams = %+v, want nil", state.Upstreams)
	}
}

// A log that added upstreams and then withdrew them all is a bound of zero, and must
// be distinguishable from a log that never mentioned upstreams — those two are the
// states that would otherwise derive the same key.
func TestResolveDistinguishesEmptiedFromUnrecorded(t *testing.T) {
	emptied, err := resolve(t, pinnedCompose(t), append(bootEvents(),
		RuntimeEvent{Event: EventUpstreamAdd, Payload: []byte("engine1 http://engine-1:8000/v1")},
		RuntimeEvent{Event: EventUpstreamRemove, Payload: []byte("engine1")},
	))
	if err != nil {
		t.Fatalf("ResolveRunningState() = %v", err)
	}
	if !emptied.UpstreamsRecorded {
		t.Fatal("UpstreamsRecorded = false after records appeared")
	}
	if emptied.Upstreams != nil {
		t.Fatalf("Upstreams = %+v, want nil for an emptied set", emptied.Upstreams)
	}
	emptiedHash, err := emptied.UpstreamSetHash()
	if err != nil {
		t.Fatalf("an emptied set must still hash: %v", err)
	}

	unrecorded, err := resolve(t, pinnedCompose(t), bootEvents())
	if err != nil {
		t.Fatalf("ResolveRunningState() = %v", err)
	}
	if unrecorded.UpstreamsRecorded {
		t.Fatal("UpstreamsRecorded = true with no records")
	}
	if _, err := unrecorded.UpstreamSetHash(); err == nil {
		t.Fatalf("an unrecorded set must not hash, got %q", emptiedHash)
	}
}
