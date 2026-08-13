package attest

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
)

const (
	bootDigest    = "sha256:" + "11111111111111111111111111111111111111111111111111111111111111ab"
	upgradeDigest = "sha256:" + "22222222222222222222222222222222222222222222222222222222222222cd"
	brokerService = "0g-serving-provider-broker"
	configSum     = "3333333333333333333333333333333333333333333333333333333333333344"
)

// composeManifest wraps a compose file the way dstack's app_compose does, and
// tcbInfoFor wraps that the way tcb_info does. Built rather than checked in
// because the fallback path the golden report cannot reach — a compose that pins a
// digest — only exists in a synthetic fixture.
func composeManifest(t *testing.T, services string) string {
	t.Helper()
	manifest, err := json.Marshal(map[string]any{
		"manifest_version":    2,
		"name":                "test",
		"docker_compose_file": services,
	})
	if err != nil {
		t.Fatalf("building app_compose: %v", err)
	}
	return string(manifest)
}

func tcbInfoFor(t *testing.T, appCompose string) []byte {
	t.Helper()
	tcbInfo, err := json.Marshal(map[string]any{"app_compose": appCompose})
	if err != nil {
		t.Fatalf("building tcb_info: %v", err)
	}
	return tcbInfo
}

func pinnedCompose(t *testing.T) string {
	t.Helper()
	return composeManifest(t, "services:\n"+
		"  mysql:\n    image: mysql@sha256:"+strings.Repeat("9", 64)+"\n"+
		"  "+brokerService+":\n    image: ghcr.io/0gfoundation/0g-serving-broker@"+bootDigest+"\n")
}

// syntheticVerified builds the VerifiedQuote a caller would hold after dstack-verifier accepted
// a GetQuote triple: the compose hash out of app_info, report_data verbatim, and the event log
// the verifier reported event_log_verified for.
//
// No quote bytes are involved. That is the shape of this package now: the registers, the replay
// and the DCAP chain are the verifier's, and what remains is the zg-* records and the §4.2
// report_data layout, neither of which dstack knows anything about.
func syntheticVerified(t *testing.T, appCompose string, events []RuntimeEvent) VerifiedQuote {
	t.Helper()
	return syntheticVerifiedSigning(t, appCompose, events, testSigner)
}

// testSigner is the address the fixtures' records bind and their report_data names, so the two
// agree unless a test sets out to make them disagree.
//
// Deliberately carries hex letters, so a test that spells it in EIP-55 mixed case exercises the
// case normalisation instead of comparing a string to itself.
const testSigner = "0xabcdef1111111111111111111111111111111111"

// testEncPub is the enclave encryption public key the fixtures' records bind and their
// report_data names — 32 bytes in hex, like a real X25519 public key.
const testEncPub = "beef000000000000000000000000000000000000000000000000000000000123"

// imageRecordPayload is what the controller writes: the reference, then BOTH keys derived from
// that image — the response signer and the enclave encryption public key.
func imageRecordPayload(ref string) []byte {
	return []byte(ref + " " + testSigner + " " + testEncPub)
}

// syntheticVerifiedSigning is syntheticVerified with control over the address report_data names,
// so a test can make the quote and the ledger disagree about who holds the signing key.
func syntheticVerifiedSigning(t *testing.T, appCompose string, events []RuntimeEvent, signer string) VerifiedQuote {
	t.Helper()

	sum := sha256.Sum256([]byte(appCompose))

	rd := make([]byte, reportDataLen)
	raw, err := hex.DecodeString(strings.TrimPrefix(signer, "0x"))
	if err != nil {
		t.Fatalf("decoding the test signer: %v", err)
	}
	copy(rd[reportDataSignerOffset:], raw)
	encRaw, err := hex.DecodeString(testEncPub)
	if err != nil {
		t.Fatalf("decoding the test enc_pub: %v", err)
	}
	copy(rd, encRaw)
	binary.BigEndian.PutUint32(rd[reportDataVersionOffset:], reportDataLayoutVersion)

	log, err := json.Marshal(tdxEventsOf(events))
	if err != nil {
		t.Fatalf("building the event log: %v", err)
	}

	return VerifiedQuote{
		ComposeHash:  hex.EncodeToString(sum[:]),
		ReportData:   rd,
		EventLogJSON: log,
	}
}

// bootEvents is dstack's own sequence, ending at the boundary past which a
// container may write. Truncated to the entries that matter here; the real one is
// in the golden report.
func bootEvents() []RuntimeEvent {
	return []RuntimeEvent{
		{Event: "system-preparing"},
		{Event: "app-id", Payload: []byte{0xde, 0xad}},
		{Event: "compose-hash", Payload: []byte{0xbe, 0xef}},
		{Event: "key-provider", Payload: []byte(`{"name":"kms"}`)},
		{Event: eventSystemReady},
	}
}

func resolve(t *testing.T, appCompose string, events []RuntimeEvent) (*RunningState, error) {
	t.Helper()
	return ResolveRunningState(syntheticVerified(t, appCompose, events), tcbInfoFor(t, appCompose), brokerService)
}

// tdxEventsOf renders events as the log a GetQuote response carries.
//
// The digest field is left empty: nothing in this package reads it now that the replay is
// dstack-verifier's, and filling it in would only assert this code agrees with itself.
func tdxEventsOf(events []RuntimeEvent) []TdxEvent {
	entries := make([]TdxEvent, 0, len(events)+1)
	// One firmware entry on another register, to keep the filter honest: it must be skipped,
	// or an entry belonging to another register's replay would be read as an application's.
	entries = append(entries, TdxEvent{IMR: 0, EventType: 0x80000009, EventPayload: "0954"})
	for _, event := range events {
		entries = append(entries, TdxEvent{
			IMR:          3,
			EventType:    RuntimeEventType,
			Event:        event.Event,
			EventPayload: hex.EncodeToString(event.Payload),
		})
	}
	return entries
}

// With nothing recorded, what runs is what the deployment booted on — and the
// compose file naming it is trustworthy because it hashed to the compose hash in
// the signed report body. No separately published digest list is involved.
func TestResolveFallsBackToTheComposePin(t *testing.T) {
	compose := pinnedCompose(t)

	state, err := resolve(t, compose, bootEvents())
	if err != nil {
		t.Fatalf("ResolveRunningState() = %v", err)
	}
	if state.BrokerDigest != bootDigest {
		t.Errorf("BrokerDigest = %q, want the compose pin %q", state.BrokerDigest, bootDigest)
	}
	if state.DigestSource != DigestSourceCompose {
		t.Errorf("DigestSource = %q, want %q", state.DigestSource, DigestSourceCompose)
	}
	// Empty, not "unknown": nothing has rewritten the config file since boot, so
	// it is still the one the compose hash covers.
	if state.ConfigSHA256 != "" {
		t.Errorf("ConfigSHA256 = %q, want empty when no config change was recorded", state.ConfigSHA256)
	}
	if got := func() string { sum := sha256.Sum256([]byte(compose)); return hex.EncodeToString(sum[:]) }(); state.ComposeHash != got {
		t.Errorf("ComposeHash = %q, want %q", state.ComposeHash, got)
	}
}

// The last recorded upgrade wins, and it overrides the compose pin. Two of them,
// because taking the first would report a version that stopped running.
func TestResolveTakesTheLastRecordedUpgrade(t *testing.T) {
	compose := pinnedCompose(t)
	superseded := "sha256:" + strings.Repeat("7", 64)

	events := append(bootEvents(),
		RuntimeEvent{Event: EventImageUpdate, Payload: []byte("ghcr.io/0gfoundation/0g-serving-broker@" + superseded)},
		RuntimeEvent{Event: EventConfigUpdate, Payload: []byte(configSum)},
		RuntimeEvent{Event: EventImageUpdate, Payload: []byte("ghcr.io/0gfoundation/0g-serving-broker@" + upgradeDigest)},
	)

	state, err := resolve(t, compose, events)
	if err != nil {
		t.Fatalf("ResolveRunningState() = %v", err)
	}
	if state.BrokerDigest != upgradeDigest {
		t.Errorf("BrokerDigest = %q, want the last recorded %q", state.BrokerDigest, upgradeDigest)
	}
	if state.DigestSource != DigestSourceEvent {
		t.Errorf("DigestSource = %q, want %q", state.DigestSource, DigestSourceEvent)
	}
	if state.ConfigSHA256 != configSum {
		t.Errorf("ConfigSHA256 = %q, want %q", state.ConfigSHA256, configSum)
	}
	if len(state.Events) != len(events) {
		t.Errorf("Events = %d entries, want the full verified sequence of %d", len(state.Events), len(events))
	}
}

// tcb_info is the one input this package still anchors itself, and each rejection here is a way
// a provider could otherwise describe itself.
//
// What is NOT here any more: an event log that does not replay to the quote's registers, and an
// event dropped from the log. Both used to be caught in this function and are now
// dstack-verifier's — its event_log_verified is exactly that check, over all four registers
// rather than only rtmr3, and it comes with the OS image hash and the TCB status that a replay
// says nothing about. Passing a log the verifier did not vouch for is a lie this package cannot
// detect, which is why VerifiedQuote's doc says constructing it is an assertion.
func TestResolveRejectsUnanchoredInputs(t *testing.T) {
	compose := pinnedCompose(t)
	goodEvents := append(bootEvents(),
		RuntimeEvent{Event: EventImageUpdate, Payload: imageRecordPayload("ghcr.io/x@" + upgradeDigest)})
	v := syntheticVerified(t, compose, goodEvents)

	// The one that actually pins the anchor: no record, so the answer comes from app_compose,
	// and the substituted compose DOES pin a digest. Remove the hash check and this resolves
	// successfully to the impostor's digest instead of refusing — which is the whole attack,
	// and which an assertion on "did it error" cannot see, because a compose naming a tag
	// errors either way for an unrelated reason.
	t.Run("a substituted compose that would otherwise resolve", func(t *testing.T) {
		boot := syntheticVerified(t, compose, bootEvents())
		impostor := composeManifest(t, "services:\n  "+brokerService+":\n    image: evil@"+upgradeDigest+"\n")

		state, err := ResolveRunningState(boot, tcbInfoFor(t, impostor), brokerService)
		if err == nil {
			t.Fatalf("resolved to %+v from a compose file this quote's compose hash does not cover", state)
		}
		if !strings.Contains(err.Error(), "app_compose") {
			t.Errorf("error %q does not say the compose file is not this quote's", err)
		}
	})

	t.Run("an app_compose that does not hash to the verified compose hash", func(t *testing.T) {
		// Substituting a compose file would let a provider claim its deployment pins digests it
		// does not. The verifier reports the compose hash out of the signed report body but
		// never sees app_compose, so this is ours and only ours.
		other := composeManifest(t, "services:\n  "+brokerService+":\n    image: evil@"+upgradeDigest+"\n")
		if _, err := ResolveRunningState(v, tcbInfoFor(t, other), brokerService); err == nil {
			t.Error("ResolveRunningState() = nil, want the compose hash mismatch to be fatal")
		}
	})

	t.Run("a tcb_info with no app_compose", func(t *testing.T) {
		if _, err := ResolveRunningState(v, []byte(`{}`), brokerService); err == nil {
			t.Error("ResolveRunningState() = nil, want a missing app_compose to be fatal")
		}
	})
}

// Fail closed on our own namespace, open on everyone else's. The asymmetry is the
// decision worth pinning: skipping an unknown zg- event would let a future one
// carrying real meaning pass unread by an old reader.
func TestResolveIsClosedToUnknownOwnEvents(t *testing.T) {
	compose := pinnedCompose(t)

	t.Run("an unknown zg- event is fatal", func(t *testing.T) {
		events := append(bootEvents(), RuntimeEvent{Event: "zg-something-new", Payload: []byte("x")})
		if _, err := resolve(t, compose, events); err == nil {
			t.Error("ResolveRunningState() = nil, want an unknown zg- event to be fatal")
		}
	})

	t.Run("an unknown foreign event is skipped", func(t *testing.T) {
		// dstack and other components write into the same register; refusing
		// theirs would make this reader break on deployments it has no business
		// having an opinion about.
		events := append(bootEvents(), RuntimeEvent{Event: "some-other-component", Payload: []byte("x")})
		state, err := resolve(t, compose, events)
		if err != nil {
			t.Fatalf("ResolveRunningState() = %v, want a foreign event to be skipped", err)
		}
		if state.BrokerDigest != bootDigest {
			t.Errorf("BrokerDigest = %q, want %q", state.BrokerDigest, bootDigest)
		}
	})
}

// The boundary. dstack's own reader stops at system-ready when it reads boot facts,
// because past it an application can write any name — including one of dstack's.
func TestResolveNeedsTheSystemReadyBoundary(t *testing.T) {
	compose := pinnedCompose(t)

	t.Run("a log without system-ready is refused", func(t *testing.T) {
		truncated := []RuntimeEvent{{Event: "system-preparing"}, {Event: "app-id", Payload: []byte{0x01}}}
		if _, err := resolve(t, compose, truncated); err == nil {
			t.Error("ResolveRunningState() = nil, want a log with no boundary to be refused")
		}
	})

	t.Run("a zg- event before system-ready is refused", func(t *testing.T) {
		// Impossible from the controller, which is a container and does not run
		// until afterwards. Reading it as ours is how a forged boot-time event
		// would be believed.
		forged := append([]RuntimeEvent{
			{Event: "system-preparing"},
			{Event: EventImageUpdate, Payload: []byte("ghcr.io/x@" + upgradeDigest)},
		}, RuntimeEvent{Event: eventSystemReady})
		if _, err := resolve(t, compose, forged); err == nil {
			t.Error("ResolveRunningState() = nil, want a zg- event in the boot prefix to be refused")
		}
	})
}

// An unreadable record is fatal only if it is the LAST one of its kind.
//
// The writer emits exactly such a record when it cannot establish the truth, so that
// a reader refuses rather than believing the record it replaced. But records
// accumulate for a whole TD boot, so treating any of them as fatal would let one
// transient docker error make a CVM permanently unverifiable with no way for a later
// correct upgrade to recover it.
func TestResolveRecoversFromAnEarlierUnreadableRecord(t *testing.T) {
	compose := pinnedCompose(t)

	events := append(bootEvents(),
		// What restoreImageRecord writes when it cannot read the broker: a bare
		// repository, naming no digest.
		RuntimeEvent{Event: EventImageUpdate, Payload: []byte("ghcr.io/0gfoundation/0g-serving-broker")},
		RuntimeEvent{Event: EventConfigUpdate, Payload: []byte("unknown")},
		RuntimeEvent{Event: EventImageUpdate, Payload: []byte("ghcr.io/x@" + upgradeDigest)},
		RuntimeEvent{Event: EventConfigUpdate, Payload: []byte(configSum)},
	)

	state, err := resolve(t, compose, events)
	if err != nil {
		t.Fatalf("ResolveRunningState() = %v, want the later records to supersede", err)
	}
	if state.BrokerDigest != upgradeDigest {
		t.Errorf("BrokerDigest = %q, want %q", state.BrokerDigest, upgradeDigest)
	}
	if state.ConfigSHA256 != configSum {
		t.Errorf("ConfigSHA256 = %q, want %q", state.ConfigSHA256, configSum)
	}
}

// The other half: an unreadable record with nothing after it must NOT fall back to the
// compose pin. An image record exists — it just did not resolve — and answering with
// the digest the deployment booted on would be believing exactly what the writer
// emitted that record to stop.
func TestResolveDoesNotFallBackAfterAnUnreadableRecord(t *testing.T) {
	compose := pinnedCompose(t)

	events := append(bootEvents(),
		RuntimeEvent{Event: EventImageUpdate, Payload: []byte("ghcr.io/0gfoundation/0g-serving-broker")},
	)

	state, err := resolve(t, compose, events)
	if err == nil {
		t.Fatalf("ResolveRunningState() = %+v, want a refusal", state)
	}
	if strings.Contains(err.Error(), bootDigest) {
		t.Errorf("ResolveRunningState() = %v, want it not to answer with the compose pin", err)
	}
}

// A payload that does not parse is refused rather than shrugged at: a caller
// comparing a half-read digest against its expected list would see a mismatch and
// blame the wrong thing.
func TestResolveRejectsMalformedPayloads(t *testing.T) {
	compose := pinnedCompose(t)

	malformed := map[string]RuntimeEvent{
		"image reference with a tag":       {Event: EventImageUpdate, Payload: []byte("ghcr.io/x:latest")},
		"image reference with no digest":   {Event: EventImageUpdate, Payload: []byte("ghcr.io/x@")},
		"image digest of the wrong length": {Event: EventImageUpdate, Payload: []byte("ghcr.io/x@sha256:" + strings.Repeat("a", 63))},
		"uppercase image digest":           {Event: EventImageUpdate, Payload: []byte("ghcr.io/x@sha256:" + strings.Repeat("A", 64))},
		"config sum that is not hex":       {Event: EventConfigUpdate, Payload: []byte(strings.Repeat("z", 64))},
		"config sum with a sha256 prefix":  {Event: EventConfigUpdate, Payload: []byte("sha256:" + strings.Repeat("a", 64))},
	}
	for name, event := range malformed {
		t.Run(name, func(t *testing.T) {
			if _, err := resolve(t, compose, append(bootEvents(), event)); err == nil {
				t.Errorf("ResolveRunningState() = nil, want %s to be refused", name)
			}
		})
	}
}

// A compose file that names the broker by tag cannot say what is running: two pulls
// of a tag can return different images. Refused rather than answered with the tag,
// which is also the state the golden report is in — see the test below.
func TestResolveRefusesATagPinnedCompose(t *testing.T) {
	compose := composeManifest(t, "services:\n  "+brokerService+":\n    image: ghcr.io/0gfoundation/0g-serving-broker:dev1\n")

	_, err := resolve(t, compose, bootEvents())
	if err == nil {
		t.Fatal("ResolveRunningState() = nil, want a tag-pinned compose to be refused")
	}
	if !strings.Contains(err.Error(), "pins no digest") {
		t.Errorf("ResolveRunningState() = %v, want the reason to name the missing digest", err)
	}
}

func TestResolveNeedsTheBrokerServiceToExist(t *testing.T) {
	compose := composeManifest(t, "services:\n  mysql:\n    image: mysql@sha256:"+strings.Repeat("9", 64)+"\n")

	_, err := resolve(t, compose, bootEvents())
	if err == nil {
		t.Fatal("ResolveRunningState() = nil, want a missing broker service to be refused")
	}
	if !strings.Contains(err.Error(), "mysql") {
		t.Errorf("ResolveRunningState() = %v, want the error to list what the compose does define", err)
	}
}

// The golden report end to end. It gets through the replay, the compose hash and
// the boundary — everything anchored to the quote — and stops only where its
// compose file names the broker by tag.
//
// That is the coverage this fixture can give: it predates the events, and a real
// digest-pinned quote is not something a test can mint. The failure being this one
// and not an earlier one is what says the anchoring steps all passed.
func TestResolveOnTheGoldenReport(t *testing.T) {
	r, tcb := goldenReport(t)
	tcbInfoJSON, err := json.Marshal(map[string]any{"app_compose": tcb.AppCompose})
	if err != nil {
		t.Fatalf("rebuilding tcb_info: %v", err)
	}

	_, err = ResolveRunningState(goldenVerified(t, r, tcb), tcbInfoJSON, brokerService)
	if err == nil {
		t.Fatal("ResolveRunningState() = nil; the golden report's compose names the broker by tag")
	}
	if !strings.Contains(err.Error(), "pins no digest") {
		t.Errorf("ResolveRunningState() = %v, want it to fail only at the compose pin", err)
	}
}

// The compose file the golden report carries, parsed as a real one.
func TestPinnedImagesOnTheGoldenReport(t *testing.T) {
	_, tcb := goldenReport(t)
	tcbInfoJSON, err := json.Marshal(map[string]any{"app_compose": tcb.AppCompose})
	if err != nil {
		t.Fatalf("rebuilding tcb_info: %v", err)
	}

	images, err := PinnedImages(tcbInfoJSON)
	if err != nil {
		t.Fatalf("PinnedImages() = %v", err)
	}
	if got := images[brokerService]; !strings.HasPrefix(got, "ghcr.io/0gfoundation/0g-serving-broker") {
		t.Errorf("PinnedImages()[%q] = %q, want the broker image", brokerService, got)
	}
	if _, ok := images["mysql"]; !ok {
		t.Errorf("PinnedImages() = %v, want every service with an image, not just the broker", images)
	}
}

func TestPinnedImages(t *testing.T) {
	t.Run("a service with no image is omitted", func(t *testing.T) {
		// A build-only service has no reference to report, and an empty string
		// would read as "pinned to nothing".
		compose := composeManifest(t, "services:\n"+
			"  built:\n    build: .\n"+
			"  pulled:\n    image: mysql:8.0\n")

		images, err := PinnedImages(tcbInfoFor(t, compose))
		if err != nil {
			t.Fatalf("PinnedImages() = %v", err)
		}
		if len(images) != 1 || images["pulled"] != "mysql:8.0" {
			t.Errorf("PinnedImages() = %v, want only the service with an image", images)
		}
	})

	// Tags are returned as they are. Whether a reference is good enough is the
	// caller's call — "what does the file say" has an answer here either way.
	t.Run("tags are reported, not rejected", func(t *testing.T) {
		compose := composeManifest(t, "services:\n  x:\n    image: mysql:8.0\n")
		images, err := PinnedImages(tcbInfoFor(t, compose))
		if err != nil {
			t.Fatalf("PinnedImages() = %v", err)
		}
		if images["x"] != "mysql:8.0" {
			t.Errorf("PinnedImages()[x] = %q, want mysql:8.0", images["x"])
		}
	})

	rejected := map[string][]byte{
		"garbage tcb_info":     []byte("not json"),
		"no app_compose":       []byte(`{}`),
		"garbage app_compose":  tcbInfoFor(t, "not json"),
		"no compose file":      tcbInfoFor(t, `{"manifest_version":2}`),
		"garbage compose file": tcbInfoFor(t, composeManifest(t, "services:\n  x:\n   image: [oops\n")),
	}
	for name, tcbInfoJSON := range rejected {
		t.Run(name, func(t *testing.T) {
			if _, err := PinnedImages(tcbInfoJSON); err == nil {
				t.Errorf("PinnedImages(%s) = nil, want an error", name)
			}
		})
	}
}
