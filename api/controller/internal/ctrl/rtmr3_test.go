package ctrl

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/0glabs/0g-serving-broker/common/attest"
	commonconfig "github.com/0glabs/0g-serving-broker/common/config"
	"github.com/0glabs/0g-serving-broker/common/log"
	"github.com/0glabs/0g-serving-broker/controller/internal/attestproxy"
	"github.com/0glabs/0g-serving-broker/controller/internal/docker"
	"github.com/0glabs/0g-serving-broker/inference/config"
)

// opLog is one ordered record of everything a change did, with the RTMR3 emits
// and the docker calls interleaved in the order they happened. The ordering is
// the whole subject of these tests, so the two cannot be recorded separately.
type opLog struct {
	mu  sync.Mutex
	ops []string
}

func (l *opLog) add(op string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.ops = append(l.ops, op)
}

func (l *opLog) all() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.ops...)
}

// indexOf returns the position of the first op with the given prefix, or -1.
func (l *opLog) indexOf(prefix string) int {
	for i, op := range l.all() {
		if strings.HasPrefix(op, prefix) {
			return i
		}
	}
	return -1
}

// fakeEmitter stands in for dstack. err makes the refusal to record testable,
// which is the branch that has to leave every container alone.
type fakeEmitter struct {
	log *opLog
	err error
}

// Honours the context, as the real client does: the dstack SDK builds every call with
// http.NewRequestWithContext, so an expired one fails before anything is written. A
// fake that ignored it would let a restore inheriting a dead context look fine here.
func (e *fakeEmitter) EmitEvent(ctx context.Context, event string, payload []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	e.log.add("emit " + event + " " + string(payload))
	return e.err
}

// Every docker endpoint the two change paths touch, each one logging the write it
// performs.
//
// Both managed containers are always present, because UpdateImages now refuses to
// proceed unless each resolves by its exact name. The upgrade tests instead fail the
// *second* container create — the event container's — which is past everything they
// assert and short of the contract sync, which would need a chain to talk to.
func fakeChangeDaemon(t *testing.T, l *opLog, pullBody string) *docker.Client {
	t.Helper()

	creates := 0

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/_ping"):
			w.Header().Set("Api-Version", "1.47")
		case strings.Contains(r.URL.Path, "/images/create"):
			l.add("pull")
			_, _ = w.Write([]byte(pullBody))
		case strings.HasSuffix(r.URL.Path, "/containers/json"):
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"Id": brokerID, "Names": []string{"/0g-serving-provider-broker"}},
				{"Id": eventID, "Names": []string{"/0g-serving-provider-event"}},
				{"Id": selfID, "Names": []string{"/0g-controller"}},
			})
		case strings.HasSuffix(r.URL.Path, "/containers/create"):
			creates++
			if creates == 1 {
				l.add("create broker")
				_ = json.NewEncoder(w).Encode(map[string]any{"Id": "beef" + strings.Repeat("0", 60)})
				return
			}
			l.add("create event refused")
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]any{"message": "no space left on device"})
		case strings.HasSuffix(r.URL.Path, "/stop"):
			l.add("stop " + containerOf(r.URL.Path, "/stop"))
			w.WriteHeader(http.StatusNoContent)
		case strings.HasSuffix(r.URL.Path, "/restart"):
			l.add("restart " + containerOf(r.URL.Path, "/restart"))
			w.WriteHeader(http.StatusNoContent)
		case strings.HasSuffix(r.URL.Path, "/start"):
			l.add("start")
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodDelete:
			l.add("remove")
			w.WriteHeader(http.StatusNoContent)
		case strings.HasSuffix(r.URL.Path, "/json"):
			// Serves both the image inspect after a pull and the container
			// inspect before a recreate; the two responses do not overlap.
			_ = json.NewEncoder(w).Encode(map[string]any{
				"Id":          brokerID,
				"Name":        "/0g-serving-provider-broker",
				"RepoDigests": []string{"ghcr.io/0gfoundation/0g-serving-broker@" + testDigest},
				"Created":     "2026-01-01T00:00:00Z",
				"Config":      map[string]any{"Image": prevRef},
				"State":       map[string]any{"Status": "running"},
				"NetworkSettings": map[string]any{
					"Networks": map[string]any{"default": map[string]any{}},
				},
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)

	c, err := docker.NewClient(config.ControllerConfig{
		Docker: config.DockerConfig{Host: srv.URL, APIVersion: "1.47"},
	})
	if err != nil {
		t.Fatalf("building docker client: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })

	return c
}

// fakeEmptyDaemon serves a container list with no broker in it, which is what an
// abort that removed it and could not create a replacement leaves behind.
func fakeEmptyDaemon(t *testing.T, l *opLog) *docker.Client {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/_ping"):
			w.Header().Set("Api-Version", "1.47")
		case strings.HasSuffix(r.URL.Path, "/containers/json"):
			_ = json.NewEncoder(w).Encode([]map[string]any{})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)

	c, err := docker.NewClient(config.ControllerConfig{
		Docker: config.DockerConfig{Host: srv.URL, APIVersion: "1.47"},
	})
	if err != nil {
		t.Fatalf("building docker client: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func containerOf(path, suffix string) string {
	parts := strings.Split(strings.TrimSuffix(path, suffix), "/")
	switch parts[len(parts)-1] {
	case brokerID:
		return "broker"
	case eventID:
		return "event"
	default:
		return parts[len(parts)-1]
	}
}

const (
	brokerID   = "aaaa111111111111111111111111111111111111111111111111111111111111"
	eventID    = "bbbb222222222222222222222222222222222222222222222222222222222222"
	selfID     = "cccc333333333333333333333333333333333333333333333333333333333333"
	selfHost   = "cccc33333333"
	testDigest = "sha256:1111111111111111111111111111111111111111111111111111111111111111"
	prevDigest = "sha256:9999999999999999999999999999999999999999999999999999999999999999"
	imageRepo  = "ghcr.io/0gfoundation/0g-serving-broker"
	// What the broker container is currently on, digest-pinned as a verifiable
	// deployment must be. The abort paths read it back off the container.
	prevRef = imageRepo + "@" + prevDigest
	okPull  = `{"status":"Status: Downloaded newer image for 0g-serving-broker"}`
)

func testLogger(t *testing.T) log.Logger {
	t.Helper()
	l, err := log.GetLogger(&commonconfig.LoggerConfig{Format: "text", Level: "error"})
	if err != nil {
		t.Fatalf("building logger: %v", err)
	}
	return l
}

// testSigner is what fakeDeriver answers, so a test can assert on the whole record.
const testSigner = "0x1111111111111111111111111111111111111111"

// fakeDeriver stands in for the guest agent's key derivation. err makes a derivation that cannot
// answer testable, which has to abort the upgrade rather than record a digest with nothing bound
// to it; block makes it hang until its context is done, which is how the socket behaves when the
// guest agent is wedged — the same failure that most plausibly caused the upgrade to need
// restoring in the first place.
type fakeDeriver struct {
	log   *opLog
	err   error
	block bool
	seen  []string
}

func (d *fakeDeriver) SignerAddress(ctx context.Context, digest string) (string, error) {
	d.seen = append(d.seen, digest)
	if d.log != nil {
		d.log.add("derive " + digest)
	}
	if d.block {
		<-ctx.Done()
		return "", ctx.Err()
	}
	if d.err != nil {
		return "", d.err
	}
	return testSigner, nil
}

// imageRecord is the payload the recorder writes: the reference, then the address of the key
// derived from that image.
func imageRecord(ref string) string { return ref + " " + testSigner }

func newChangeCtrl(t *testing.T, l *opLog, emitErr error, configFile string, pullBody string) *Ctrl {
	t.Helper()
	return newChangeCtrlWithDeriver(t, l, emitErr, configFile, pullBody, &fakeDeriver{log: l})
}

func newChangeCtrlWithDeriver(t *testing.T, l *opLog, emitErr error, configFile string, pullBody string, deriver SignerDeriver) *Ctrl {
	t.Helper()
	t.Cleanup(docker.SetHostnameForTests(selfHost))
	t.Setenv(attestproxy.SocketEnvVar, "/var/run/zg-tee/tee.sock")
	return &Ctrl{
		config: config.ControllerConfig{
			ImageRepo:  imageRepo,
			ConfigFile: configFile,
		},
		dockerClient: fakeChangeDaemon(t, l, pullBody),
		emitter:      &fakeEmitter{log: l, err: emitErr},
		deriver:      deriver,
		logger:       testLogger(t),
	}
}

// The load-bearing invariant for the config path: the change is in RTMR3 before it
// happens, and the broker restarts within the same call so the record reaches the
// quote it serves.
//
// A broker seals its quote when it starts — it caches the whole
// quote/event-log/tcb_info triple for its lifetime — so an event emitted while it
// runs reaches no reader until it restarts. Nothing in the type system says so.
func TestConfigChangeIsRecordedBeforeItHappens(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("service:\n  name: before\n"), 0o644); err != nil {
		t.Fatalf("seeding the config file: %v", err)
	}

	l := &opLog{}
	c := newChangeCtrl(t, l, nil, path, okPull)

	const content = "service:\n  name: after\n"
	if err := c.ApplyCoreConfig(context.Background(), content); err != nil {
		t.Fatalf("ApplyCoreConfig() = %v, want nil", err)
	}

	sum := sha256.Sum256([]byte(content))
	wantEmit := "emit " + attest.EventConfigUpdate + " " + hex.EncodeToString(sum[:])
	ops := l.all()
	if len(ops) == 0 || ops[0] != wantEmit {
		t.Fatalf("ops = %v, want %q first", ops, wantEmit)
	}
	// The restart is what publishes the record. A path that emitted and did not
	// restart would leave users reading a quote taken before the change.
	if l.indexOf("restart broker") < 0 {
		t.Errorf("ops = %v, want the broker restarted in the same call", ops)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the config file back: %v", err)
	}
	if string(got) != content {
		t.Errorf("config file = %q, want %q", got, content)
	}
}

// A refusal to record leaves the deployment exactly as it was — including the
// config file, whose old content is what the still-running containers are on.
func TestConfigChangeAbortsWhenItCannotBeRecorded(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	const before = "service:\n  name: before\n"
	if err := os.WriteFile(path, []byte(before), 0o644); err != nil {
		t.Fatalf("seeding the config file: %v", err)
	}

	l := &opLog{}
	c := newChangeCtrl(t, l, errors.New("dstack.sock: connection refused"), path, okPull)

	if err := c.ApplyCoreConfig(context.Background(), "service:\n  name: after\n"); err == nil {
		t.Fatal("ApplyCoreConfig() = nil, want an error when the change cannot be recorded")
	}

	if got := l.indexOf("restart"); got >= 0 {
		t.Errorf("ops = %v, want no container touched", l.all())
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the config file back: %v", err)
	}
	if string(got) != before {
		t.Errorf("config file = %q, want the original %q", got, before)
	}
}

// The image record sits immediately before the step that changes which image the
// broker runs — after the pull and after the containers are stopped, before the
// create.
//
// Not at the top of the call, which is where it started out. A quote is sealed when a
// broker process starts, so the ledger only has to be true at those instants; a
// record written before the pull leaves that whole unbounded, caller-influenced wait
// as a window in which the still-running broker can be restarted and seal a quote
// naming an image the pull had not even fetched. Recorded here, no broker is in a
// state that can start: it has just been stopped, docker's restart policy does not
// fire on a deliberate stop, and the start/stop/restart routes are behind the same
// lock this call holds.
func TestImageChangeIsRecordedOnceNoBrokerIsRunning(t *testing.T) {
	l := &opLog{}
	c := newChangeCtrl(t, l, nil, "", okPull)

	// Fails at the end, on the absent event container — past everything asserted
	// here, and short of the contract sync, which needs a chain.
	if _, err := c.UpdateImages(context.Background(), testDigest); err == nil {
		t.Fatal("UpdateImages() = nil, want the event container recreate to fail")
	}

	ops := l.all()
	emit := l.indexOf("emit " + attest.EventImageUpdate + " " + imageRecord(imageRepo+"@"+testDigest))
	if emit < 0 {
		t.Fatalf("ops = %v, want the image change recorded", ops)
	}

	// The record must not exist while a broker is alive that is not on the new image.
	// A quote can be taken by whatever is currently running — the broker holds
	// dstack.sock and report_data carries no nonce, so a quote it collects can be
	// replayed forever — and that process is the one under suspicion. So the pull and
	// the stop, which leave the old image running, both precede the record.
	for _, before := range []string{"pull", "stop broker"} {
		if i := l.indexOf(before); i < 0 || i > emit {
			t.Errorf("ops = %v, want %q before the record at %d", ops, before, emit)
		}
	}
	// And the create, which is the first step that changes the image, follows it: a
	// change must not happen unrecorded.
	for _, after := range []string{"remove", "create broker"} {
		if i := l.indexOf(after); i < 0 || i < emit {
			t.Errorf("ops = %v, want %q after the record at %d", ops, after, emit)
		}
	}
}

// A record that cannot be written leaves the broker running, not stopped: nothing was
// recorded, so the ledger is still truthful, and a dstack hiccup must not become an
// outage.
func TestFailedRecordRestartsTheBroker(t *testing.T) {
	l := &opLog{}
	c := newChangeCtrl(t, l, errors.New("dstack.sock: connection refused"), "", okPull)

	result, err := c.UpdateImages(context.Background(), testDigest)
	if err == nil {
		t.Fatal("UpdateImages() = nil, want an error when the change cannot be recorded")
	}
	if result == nil || result.Success {
		t.Errorf("UpdateImages() = %+v, want a failed result", result)
	}

	ops := l.all()
	if l.indexOf("start") < 0 {
		t.Errorf("ops = %v, want the broker started again after the failed record", ops)
	}
	// Nothing was created, so no image changed.
	for _, write := range []string{"remove", "create"} {
		if l.indexOf(write) >= 0 {
			t.Errorf("ops = %v, want no container recreated", ops)
		}
	}
}

// A pull failure is the failure an attacker can summon at will — ask to upgrade to a
// well-formed digest the registry cannot serve — and it now happens before anything
// is recorded, so there is nothing to overstate and nothing to restore.
//
// This is what killed the two-call downgrade: install an older published image, then
// "upgrade" to the current digest with an unpullable one, and the ledger used to name
// the digest a verifier is looking for while the superseded image served traffic.
func TestFailedPullRecordsNothing(t *testing.T) {
	l := &opLog{}
	c := newChangeCtrl(t, l, nil, "", `{"errorDetail":{"message":"manifest unknown"}}`)

	if _, err := c.UpdateImages(context.Background(), testDigest); err == nil {
		t.Fatal("UpdateImages() = nil, want the pull to fail")
	}

	// The derivation runs first — deliberately, so an unreachable guest agent costs an untouched
	// deployment rather than a pull — but it writes nothing. What matters is that no record and no
	// container operation happened.
	for _, op := range l.all() {
		if op != "pull" && !strings.HasPrefix(op, "derive ") {
			t.Errorf("ops = %v, want only the derivation and the pull", l.all())
			break
		}
	}
	if l.indexOf("pull") < 0 {
		t.Errorf("ops = %v, want the pull to have been attempted", l.all())
	}
}

// The record must bind a signer, so a derivation that cannot answer has to abort the upgrade —
// before anything is pulled, stopped or recreated.
//
// Recording the digest without an address would not be the lenient option. A reader refuses such
// a record, so the deployment would go down just the same, except after the upgrade instead of
// before it and with the ledger permanently carrying a claim nothing can check.
func TestUpgradeRefusesWhenTheSignerCannotBeDerived(t *testing.T) {
	l := &opLog{}
	c := newChangeCtrlWithDeriver(t, l, nil, "", okPull, &fakeDeriver{log: l, err: errors.New("dstack.sock: connection refused")})

	if _, err := c.UpdateImages(context.Background(), testDigest); err == nil {
		t.Fatal("UpdateImages() = nil, want a refusal when the signer cannot be derived")
	}
	for _, op := range l.all() {
		if !strings.HasPrefix(op, "derive ") {
			t.Errorf("ops = %v, want nothing but the failed derivation — no pull, no stop, no record", l.all())
			break
		}
	}
}

// The restore record must bind a signer too, derived from the image the broker is RUNNING rather
// than the one the failed upgrade asked for — naming the requested one is precisely the stale
// overstatement this exists to correct.
func TestRestoreBindsTheSignerOfTheRunningImage(t *testing.T) {
	l := &opLog{}
	c := newChangeCtrl(t, l, nil, "", okPull)

	if err := c.restoreImageRecord(context.Background()); err != nil {
		t.Fatalf("restoreImageRecord() = %v", err)
	}
	want := "emit " + attest.EventImageUpdate + " " + imageRecord(imageRepo+"@"+prevDigest)
	if ops := l.all(); len(ops) == 0 || ops[len(ops)-1] != want {
		t.Errorf("ops = %v, want the last to be %q", ops, want)
	}
}

// The derivation and the emit talk to the same socket, so a derivation that hangs must not eat
// the whole restore budget and leave the corrective emit with none — the stale record would then
// stand as the ledger's last word, which is the failure this function exists to prevent.
func TestRestoreKeepsBudgetForTheEmitWhenTheDerivationHangs(t *testing.T) {
	l := &opLog{}
	c := newChangeCtrlWithDeriver(t, l, nil, "", okPull, &fakeDeriver{log: l, block: true})

	ctx, cancel := context.WithTimeout(context.Background(), restoreTimeout)
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- c.restoreImageRecord(ctx) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("restoreImageRecord() = %v, want it to record something despite the hang", err)
		}
	case <-time.After(restoreTimeout):
		t.Fatal("restoreImageRecord did not return within the restore budget")
	}

	var emitted string
	for _, op := range l.all() {
		if strings.HasPrefix(op, "emit "+attest.EventImageUpdate+" ") {
			emitted = strings.TrimPrefix(op, "emit "+attest.EventImageUpdate+" ")
		}
	}
	if emitted == "" {
		t.Fatalf("ops = %v, want the corrective record to have been emitted", l.all())
	}
	if strings.Contains(emitted, "@") {
		t.Errorf("recorded %q, want a payload that pins no digest", emitted)
	}
}

// When the truth cannot be established the record must name no digest, so a reader
// refuses rather than believing the record it replaces.
//
// The earlier version returned an error here instead, reasoning that a broker whose
// reference carries no digest is in a deployment attest cannot verify anyway. That is
// wrong, and it was exploitable: the compose-pinned fallback is consulted only when
// there is no image record at all, so leaving the stale one turned an unverifiable
// deployment into a verifiably wrong one.
func TestRestoreFailsClosedWhenTheImageIsUnknown(t *testing.T) {
	l := &opLog{}
	// No broker in the container list, so nothing can be read back.
	c := &Ctrl{
		config:       config.ControllerConfig{ImageRepo: imageRepo},
		dockerClient: fakeEmptyDaemon(t, l),
		emitter:      &fakeEmitter{log: l},
		logger:       testLogger(t),
	}

	if err := c.restoreImageRecord(context.Background()); err != nil {
		t.Fatalf("restoreImageRecord() = %v, want it to record something", err)
	}

	ops := l.all()
	if len(ops) != 1 {
		t.Fatalf("ops = %v, want exactly one record", ops)
	}
	// attest.ResolveRunningState refuses a payload that pins no digest, which is the
	// whole point: refusing beats believing the record being replaced.
	payload := strings.TrimPrefix(ops[0], "emit "+attest.EventImageUpdate+" ")
	if strings.Contains(payload, "@") {
		t.Errorf("recorded %q, want a payload that pins no digest", payload)
	}
}

// Once the broker is on the new image the record is already right, so the later
// failures must NOT append anything — restoring there would replace a true record
// with a false one.
func TestImageChangeAfterTheBrokerMovedDoesNotRestore(t *testing.T) {
	l := &opLog{}
	// withEvent=false, so this fails at the event container — after the broker has
	// been recreated on the new image.
	c := newChangeCtrl(t, l, nil, "", okPull)

	if _, err := c.UpdateImages(context.Background(), testDigest); err == nil {
		t.Fatal("UpdateImages() = nil, want the event container recreate to fail")
	}

	var emits []string
	for _, op := range l.all() {
		if strings.HasPrefix(op, "emit ") {
			emits = append(emits, op)
		}
	}
	if len(emits) != 1 {
		t.Errorf("emits = %v, want only the original record — the broker did reach that image", emits)
	}
}

// Same property for the config path. The recorded hash would otherwise name content
// that was never applied, and the caller chooses what content that is.
func TestFailedConfigWriteRestoresTheRecord(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	const onDisk = "service:\n  name: on-disk\n"
	if err := os.WriteFile(path, []byte(onDisk), 0o644); err != nil {
		t.Fatalf("seeding the config file: %v", err)
	}

	l := &opLog{}
	c := newChangeCtrl(t, l, nil, path, okPull)

	// Called directly: making os.WriteFile fail on demand needs either a
	// root-dependent chmod or a path whose re-read fails too, and neither exercises
	// the restore itself. The wiring from the write's error to here is one line in
	// ApplyCoreConfig.
	cause := errors.New("write /etc/config/config.yaml: read-only file system")
	if err := c.abortConfigChange(context.Background(), cause); !errors.Is(err, cause) {
		t.Errorf("abortConfigChange() = %v, want it to carry the original cause", err)
	}

	sum := sha256.Sum256([]byte(onDisk))
	want := "emit " + attest.EventConfigUpdate + " " + hex.EncodeToString(sum[:])
	if ops := l.all(); len(ops) != 1 || ops[0] != want {
		t.Errorf("ops = %v, want %q — the hash of what is actually on disk", ops, want)
	}
}

// A restore that itself fails must be loud in the returned error, because the ledger
// is then left overstating and only an operator can resolve it. An unreadable file no
// longer reaches this — it records "unknown", which a reader refuses — so the only way
// left is the emit itself failing.
func TestFailedRestoreIsReported(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("service:\n  name: x\n"), 0o644); err != nil {
		t.Fatalf("seeding the config file: %v", err)
	}

	l := &opLog{}
	c := newChangeCtrl(t, l, errors.New("dstack.sock: connection refused"), path, okPull)

	cause := errors.New("original failure")
	err := c.abortConfigChange(context.Background(), cause)
	if !errors.Is(err, cause) {
		t.Errorf("abortConfigChange() = %v, want it to carry the original cause", err)
	}
	if !strings.Contains(err.Error(), "RTMR3 still names") {
		t.Errorf("abortConfigChange() = %v, want it to say the record was left overstating", err)
	}
}

// RTMR3 is a ledger, and the last zg-image-update in it is what a reader believes
// is running. Two upgrades interleaving would leave the last event and the last
// container created coming from different requests, so the second caller is
// refused rather than queued: it has to know its digest is not the one that won.
func TestConcurrentChangesAreRefused(t *testing.T) {
	l := &opLog{}
	c := newChangeCtrl(t, l, nil, filepath.Join(t.TempDir(), "config.yaml"), okPull)

	c.changing.Lock()
	defer c.changing.Unlock()

	if _, err := c.UpdateImages(context.Background(), testDigest); !errors.Is(err, ErrChangeInProgress) {
		t.Errorf("UpdateImages() = %v, want ErrChangeInProgress", err)
	}
	if err := c.ApplyCoreConfig(context.Background(), "service:\n  name: x\n"); !errors.Is(err, ErrChangeInProgress) {
		t.Errorf("ApplyCoreConfig() = %v, want ErrChangeInProgress", err)
	}
	if ops := l.all(); len(ops) != 0 {
		t.Errorf("ops = %v, want nothing recorded or touched", ops)
	}
}

// An upgrade refuses to run unless both managed containers resolve by their exact
// names.
//
// Lookup falls back to a shortest-substring match. Once a previous attempt removed the
// broker container and failed to create a replacement, the shortest remaining name
// containing "0g-serving-provider-broker" is "0g-serving-provider-broker-db" — the
// database in this project's own compose file. Without this guard a retry stops it,
// records an image change, and recreates the database container running the broker
// image, reporting success.
func TestUpgradeRefusesAContainerItCannotNameExactly(t *testing.T) {
	l := &opLog{}
	t.Cleanup(docker.SetHostnameForTests(selfHost))

	// The broker is gone; only the database is left carrying its name as a prefix.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/_ping"):
			w.Header().Set("Api-Version", "1.47")
		case strings.HasSuffix(r.URL.Path, "/containers/json"):
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"Id": "dddd444444444444444444444444444444444444444444444444444444444444",
					"Names": []string{"/0g-serving-provider-broker-db"}},
				{"Id": eventID, "Names": []string{"/0g-serving-provider-event"}},
				{"Id": selfID, "Names": []string{"/0g-controller"}},
			})
		case strings.HasSuffix(r.URL.Path, "/json"):
			// The inspect GetContainerStatus does after resolving a name. Read-only,
			// so it is not "touching" anything.
			_ = json.NewEncoder(w).Encode(map[string]any{
				"Id":     "dddd444444444444444444444444444444444444444444444444444444444444",
				"Name":   "/0g-serving-provider-broker-db",
				"Config": map[string]any{"Image": "mysql:8.0"},
				"State":  map[string]any{"Status": "running"},
			})
		default:
			l.add("touched " + r.Method + " " + r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	dc, err := docker.NewClient(config.ControllerConfig{
		Docker: config.DockerConfig{Host: srv.URL, APIVersion: "1.47"},
	})
	if err != nil {
		t.Fatalf("building docker client: %v", err)
	}
	t.Cleanup(func() { _ = dc.Close() })

	c := &Ctrl{
		config:       config.ControllerConfig{ImageRepo: imageRepo},
		dockerClient: dc,
		emitter:      &fakeEmitter{log: l},
		logger:       testLogger(t),
	}

	result, err := c.UpdateImages(context.Background(), testDigest)
	var ambiguous *AmbiguousContainerError
	if !errors.As(err, &ambiguous) {
		t.Fatalf("UpdateImages() = %v, want *AmbiguousContainerError", err)
	}
	if ambiguous.Got != "0g-serving-provider-broker-db" {
		t.Errorf("Got = %q, want the database container it would have destroyed", ambiguous.Got)
	}
	// Refused before anything at all: no pull, no record, no container write.
	if result != nil {
		t.Errorf("UpdateImages() = %+v, want a nil result", result)
	}
	if ops := l.all(); len(ops) != 0 {
		t.Errorf("ops = %v, want nothing touched", ops)
	}
}

// A restore runs on its own context, not the one whose expiry may be why it is
// running.
//
// The upgrade holds a deadline so it cannot pin the lock forever, and that deadline is
// one of the things that can abort it. A restore inheriting an expired context cannot
// make a single call, which leaves the ledger overstating on precisely the path that
// exists to prevent that — reachable by slow-serving the pull until the deadline lands
// between the record and the recreate.
func TestRestoreRunsOnItsOwnContext(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	const onDisk = "service:\n  name: on-disk\n"
	if err := os.WriteFile(path, []byte(onDisk), 0o644); err != nil {
		t.Fatalf("seeding the config file: %v", err)
	}

	l := &opLog{}
	c := newChangeCtrl(t, l, nil, path, okPull)

	expired, cancel := context.WithCancel(context.Background())
	cancel()

	cause := errors.New("write /etc/config/config.yaml: read-only file system")
	if err := c.abortConfigChange(expired, cause); !errors.Is(err, cause) {
		t.Errorf("abortConfigChange() = %v, want it to carry the original cause", err)
	}

	sum := sha256.Sum256([]byte(onDisk))
	want := "emit " + attest.EventConfigUpdate + " " + hex.EncodeToString(sum[:])
	if ops := l.all(); len(ops) != 1 || ops[0] != want {
		t.Errorf("ops = %v, want %q — the restore must run even on a dead context", ops, want)
	}
}

// A deployment with a controller but no attestation proxy is the one case where recording the
// truth is worse than refusing: the broker derives at a fixed path, so no record this controller
// writes can ever match a quote, and no later record can repair it either.
func TestUpgradeRefusesWithoutTheAttestationProxy(t *testing.T) {
	l := &opLog{}
	c := newChangeCtrl(t, l, nil, "", okPull)
	t.Setenv(attestproxy.SocketEnvVar, "")

	if _, err := c.UpdateImages(context.Background(), testDigest); err == nil {
		t.Fatal("UpdateImages() = nil, want a refusal when the attestation proxy is not served")
	}

	// Before anything is touched: no derivation, no pull, no emit, no container stopped.
	if ops := l.all(); len(ops) != 0 {
		t.Fatalf("refused after acting: %v", ops)
	}
}
