package attest

import (
	"strings"
	"testing"
)

// The fixture for the self-deployed case: a compose declaring two engine containers
// beside the broker and the database, one pinned by digest and one only by tag.
const engineDigest = "sha256:" + "444444444444444444444444444444444444444444444444444444444444abcd"

func composeWithEngines(t *testing.T) string {
	t.Helper()
	return composeManifest(t, "services:\n"+
		"  mysql:\n    image: mysql@sha256:"+strings.Repeat("9", 64)+"\n"+
		"  engine-1:\n    image: ghcr.io/example/engine@"+engineDigest+"\n"+
		"  engine-2:\n    image: ghcr.io/example/engine:0.5.11\n"+
		"  "+brokerService+":\n    image: ghcr.io/0gfoundation/0g-serving-broker@"+bootDigest+"\n")
}

// The unit of the thing: which members the compose can speak about. Kept separate from
// the resolver tests because the rule — match on the host, not on the name — is the
// whole security argument, and it is easier to state one input at a time.
func TestClassifyUpstreams(t *testing.T) {
	services := map[string]string{
		"engine-1":   "ghcr.io/example/engine@" + engineDigest,
		"engine-2":   "ghcr.io/example/engine:0.5.11",
		"Engine-Cap": "ghcr.io/example/engine@" + engineDigest,
	}
	tests := []struct {
		name        string
		in          Upstream
		wantService string
		wantImage   string
	}{
		{
			name:        "a host that is a declared service",
			in:          Upstream{Name: "a", URL: "http://engine-1:8000/v1"},
			wantService: "engine-1",
			wantImage:   "ghcr.io/example/engine@" + engineDigest,
		},
		{
			// Whether the reference pins a digest is the caller's call, not this
			// function's — a tag is a truthful answer to what the compose says.
			name:        "a declared service pinned only by tag",
			in:          Upstream{Name: "a", URL: "http://engine-2:8000/v1"},
			wantService: "engine-2",
			wantImage:   "ghcr.io/example/engine:0.5.11",
		},
		{
			name: "an external host",
			in:   Upstream{Name: "a", URL: "https://vendor.example/v1", Identity: "openrouter"},
		},
		{
			// The point of matching on the host. Whoever writes the record chooses the
			// name, so a name-based match would let an external destination be called
			// after an in-CVM container and pass for one.
			name: "a name that impersonates a service while the host is external",
			in:   Upstream{Name: "engine-1", URL: "https://vendor.example/v1"},
		},
		{
			// An IP bypasses the compose's service DNS, so the compose cannot say which
			// container it addresses — even if that address happens to be a container's.
			name: "an IP literal host",
			in:   Upstream{Name: "a", URL: "http://10.0.0.5:8000/v1"},
		},
		{
			// DNS is case-insensitive and validUpstreamURL forces a lowercase host, so a
			// service the compose spells with capitals still has to be findable — and the
			// field reports the compose's spelling, not the lowercased lookup key.
			name:        "a service the compose spells with capitals",
			in:          Upstream{Name: "a", URL: "http://engine-cap:8000/v1"},
			wantService: "Engine-Cap",
			wantImage:   "ghcr.io/example/engine@" + engineDigest,
		},
		{
			name: "a host that is a service name with a port that is not the service's",
			// The port is which port on that container; the compose says nothing about it
			// here, and the classification is about the host.
			in:          Upstream{Name: "a", URL: "http://engine-1:9999/v1"},
			wantService: "engine-1",
			wantImage:   "ghcr.io/example/engine@" + engineDigest,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := classifyUpstreams([]Upstream{tt.in}, services)
			if len(got) != 1 {
				t.Fatalf("classifyUpstreams() returned %d members, want 1", len(got))
			}
			if got[0].ComposeService != tt.wantService {
				t.Errorf("ComposeService = %q, want %q", got[0].ComposeService, tt.wantService)
			}
			if got[0].PinnedImage != tt.wantImage {
				t.Errorf("PinnedImage = %q, want %q", got[0].PinnedImage, tt.wantImage)
			}
			// The three record-derived fields must come through untouched — this function
			// describes the destination, it does not restate it.
			if got[0].Name != tt.in.Name || got[0].URL != tt.in.URL || got[0].Identity != tt.in.Identity {
				t.Errorf("the record's own fields changed: %+v, want %+v", got[0], tt.in)
			}
		})
	}
}

// Two services that differ only in case would both answer to one host. Resolving that
// by last-wins would make up which container sees the plaintext, so neither is claimed.
func TestClassifyUpstreamsRefusesAnAmbiguousServiceName(t *testing.T) {
	services := map[string]string{
		"Engine": "ghcr.io/example/one@" + engineDigest,
		"engine": "ghcr.io/example/two@" + engineDigest,
		"safe":   "ghcr.io/example/safe@" + engineDigest,
	}
	got := classifyUpstreams([]Upstream{
		{Name: "a", URL: "http://engine:8000/v1"},
		{Name: "b", URL: "http://safe:8000/v1"},
	}, services)
	if got[0].ComposeService != "" || got[0].PinnedImage != "" {
		t.Errorf("member a = %+v, want no service claimed: two services answer to that host", got[0])
	}
	// And the ambiguity must not spill onto the rest of the set.
	if got[1].ComposeService != "safe" {
		t.Errorf("member b = %+v, want it still classified", got[1])
	}
}

// The input slice is the one the resolver keeps as its comparison baseline, so writing
// through it would be a way for classification to leak into the change log.
func TestClassifyUpstreamsDoesNotWriteThroughItsInput(t *testing.T) {
	in := []Upstream{{Name: "a", URL: "http://engine-1:8000/v1"}}
	out := classifyUpstreams(in, map[string]string{"engine-1": "ghcr.io/example/engine@" + engineDigest})
	if in[0].ComposeService != "" || in[0].PinnedImage != "" {
		t.Fatalf("the input member was modified: %+v", in[0])
	}
	if out[0].ComposeService == "" {
		t.Fatal("the output member was not classified")
	}
}

// End to end: a set holding one self-deployed engine and one external vendor. This is
// the shape the whole change exists for — the two members are indistinguishable in the
// record and have to come out distinguishable here.
func TestResolveClassifiesSelfDeployedUpstreams(t *testing.T) {
	state, err := resolve(t, composeWithEngines(t), append(bootEvents(),
		upstreamSetEvent(
			"local http://engine-1:8000/v1",
			"vendor https://vendor.example/v1 openrouter",
		),
	))
	if err != nil {
		t.Fatalf("ResolveRunningState() = %v", err)
	}
	if len(state.Upstreams) != 2 {
		t.Fatalf("Upstreams = %+v, want both members", state.Upstreams)
	}
	local, vendor := state.Upstreams[0], state.Upstreams[1]
	if local.ComposeService != "engine-1" {
		t.Errorf("the self-deployed member = %+v, want ComposeService engine-1", local)
	}
	if local.PinnedImage != "ghcr.io/example/engine@"+engineDigest {
		t.Errorf("the self-deployed member = %+v, want the compose's pin", local)
	}
	if vendor.ComposeService != "" || vendor.PinnedImage != "" {
		t.Errorf("the external member = %+v, want nothing claimed about it", vendor)
	}
}

// The event path returns from inside its own branch, so a classification placed after
// that branch would silently skip every upgraded deployment — which is the normal case.
func TestResolveClassifiesOnTheEventPathToo(t *testing.T) {
	state, err := resolve(t, composeWithEngines(t), append(bootEvents(),
		RuntimeEvent{Event: EventImageUpdate, Payload: imageRecordPayload("ghcr.io/0gfoundation/0g-serving-broker@" + upgradeDigest)},
		upstreamSetEvent("local http://engine-1:8000/v1"),
	))
	if err != nil {
		t.Fatalf("ResolveRunningState() = %v", err)
	}
	if state.DigestSource != DigestSourceEvent {
		t.Fatalf("DigestSource = %q, want %q — this test is meant to exercise the event path", state.DigestSource, DigestSourceEvent)
	}
	if state.Upstreams[0].ComposeService != "engine-1" {
		t.Errorf("Upstreams[0] = %+v, want it classified on the event path as well", state.Upstreams[0])
	}
}

// Classification must not reach the hash. The compose is already bound to the quote by
// its own hash, and folding it in would make two deployments permitting the same
// destinations disagree because they pin different image versions — so the same set
// would derive two signing keys.
func TestResolveSetHashIgnoresClassification(t *testing.T) {
	member := "local http://engine-1:8000/v1"
	classified, err := resolve(t, composeWithEngines(t), append(bootEvents(), upstreamSetEvent(member)))
	if err != nil {
		t.Fatalf("ResolveRunningState() = %v", err)
	}
	if classified.Upstreams[0].ComposeService == "" {
		t.Fatal("this test needs the member classified to be worth anything")
	}
	// The same record against a compose that declares no such service.
	unclassified, err := resolve(t, pinnedCompose(t), append(bootEvents(), upstreamSetEvent(member)))
	if err != nil {
		t.Fatalf("ResolveRunningState() = %v", err)
	}
	if unclassified.Upstreams[0].ComposeService != "" {
		t.Fatal("this test needs the member unclassified to be worth anything")
	}
	a, err := classified.UpstreamSetHash()
	if err != nil {
		t.Fatalf("UpstreamSetHash() = %v", err)
	}
	b, err := unclassified.UpstreamSetHash()
	if err != nil {
		t.Fatalf("UpstreamSetHash() = %v", err)
	}
	if a != b {
		t.Fatalf("classification changed the set hash: %s vs %s", a, b)
	}
}

// A compose whose hash is good but which is not a docker-compose manifest. The three
// cases below differ only in whether there is anything to classify, and that is the
// guard's whole job: it is what keeps a change with no writer from being able to fail a
// call that succeeds today.
func TestResolveFailsOnlyWhenThereIsSomethingToClassify(t *testing.T) {
	// A manifest with no docker_compose_file at all.
	brokenCompose := composeManifest(t, "")

	t.Run("a non-empty set needs the compose", func(t *testing.T) {
		// The image record is what makes this test mean anything. Without it the run takes
		// the compose fallback, which reads the same unreadable compose for its own reason
		// and errors there — so the test would pass with the classification's error
		// handling removed entirely. With it, the event path returns before that fallback,
		// and the only call that can fail is the classification's.
		_, err := resolve(t, brokenCompose, append(bootEvents(),
			RuntimeEvent{Event: EventImageUpdate, Payload: imageRecordPayload("ghcr.io/0gfoundation/0g-serving-broker@" + upgradeDigest)},
			upstreamSetEvent("local http://engine-1:8000/v1"),
		))
		if err == nil {
			t.Fatal("want an error: the ledger names destinations and the compose cannot say which are containers it declares")
		}
		if !strings.Contains(err.Error(), "the ledger records upstreams") {
			t.Errorf("error = %v, want the classification's own error rather than another reader of the same compose", err)
		}
	})

	t.Run("an explicitly empty set does not", func(t *testing.T) {
		state, err := resolve(t, brokenCompose, append(bootEvents(),
			RuntimeEvent{Event: EventImageUpdate, Payload: imageRecordPayload("ghcr.io/0gfoundation/0g-serving-broker@" + upgradeDigest)},
			upstreamSetEvent(),
		))
		if err != nil {
			t.Fatalf("ResolveRunningState() = %v, want an empty set to need nothing from the compose", err)
		}
		if state.UpstreamsState != UpstreamsKnown {
			t.Errorf("UpstreamsState = %q, want %q", state.UpstreamsState, UpstreamsKnown)
		}
	})

	t.Run("no upstream record does not either", func(t *testing.T) {
		// Every live deployment is this one, which is why the guard matters.
		_, err := resolve(t, brokenCompose, append(bootEvents(),
			RuntimeEvent{Event: EventImageUpdate, Payload: imageRecordPayload("ghcr.io/0gfoundation/0g-serving-broker@" + upgradeDigest)},
		))
		if err != nil {
			t.Fatalf("ResolveRunningState() = %v, want a deployment that records no upstream to be unaffected", err)
		}
	})

	t.Run("an unknown set does not either", func(t *testing.T) {
		state, err := resolve(t, brokenCompose, append(bootEvents(),
			RuntimeEvent{Event: EventImageUpdate, Payload: imageRecordPayload("ghcr.io/0gfoundation/0g-serving-broker@" + upgradeDigest)},
			RuntimeEvent{Event: EventUpstreamSet, Payload: []byte("garbage")},
		))
		if err != nil {
			t.Fatalf("ResolveRunningState() = %v, want an unreadable record to leave nothing to classify", err)
		}
		if state.UpstreamsState != UpstreamsUnknown {
			t.Errorf("UpstreamsState = %q, want %q", state.UpstreamsState, UpstreamsUnknown)
		}
	})
}

// A service the compose declares with no image is omitted by PinnedImages, so it comes
// back unclassified. Asserted rather than left to chance because the direction matters:
// an in-CVM container reads as one the compose cannot speak about, which OVERSTATES
// where plaintext can go. That is the safe direction for both plausible caller
// policies — refusing unclassified members, or reporting them as external.
func TestResolveLeavesAnImagelessServiceUnclassified(t *testing.T) {
	compose := composeManifest(t, "services:\n"+
		"  engine-1:\n    build: ./engine\n"+
		"  "+brokerService+":\n    image: ghcr.io/0gfoundation/0g-serving-broker@"+bootDigest+"\n")
	state, err := resolve(t, compose, append(bootEvents(), upstreamSetEvent("local http://engine-1:8000/v1")))
	if err != nil {
		t.Fatalf("ResolveRunningState() = %v", err)
	}
	if state.Upstreams[0].ComposeService != "" {
		t.Errorf("Upstreams[0] = %+v, want nothing claimed: the compose names no image for it", state.Upstreams[0])
	}
}
