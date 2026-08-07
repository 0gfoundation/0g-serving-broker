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

	commonconfig "github.com/0glabs/0g-serving-broker/common/config"
	"github.com/0glabs/0g-serving-broker/common/log"
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

func (e *fakeEmitter) EmitEvent(_ context.Context, event string, payload []byte) error {
	e.log.add("emit " + event + " " + string(payload))
	return e.err
}

// Every docker endpoint the two change paths touch, each one logging the write it
// performs.
//
// withEvent decides whether the event container exists. The upgrade test leaves it
// out so UpdateImages runs its whole broker half and then stops where it cannot
// resolve that container — past everything being asserted, and short of the
// contract sync, which would need a chain to talk to.
func fakeChangeDaemon(t *testing.T, l *opLog, withEvent bool) *docker.Client {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/_ping"):
			w.Header().Set("Api-Version", "1.47")
		case strings.Contains(r.URL.Path, "/images/create"):
			l.add("pull")
			_, _ = w.Write([]byte(okPull))
		case strings.HasSuffix(r.URL.Path, "/containers/json"):
			list := []map[string]any{
				{"Id": brokerID, "Names": []string{"/0g-serving-provider-broker"}},
				{"Id": selfID, "Names": []string{"/0g-controller"}},
			}
			if withEvent {
				list = append(list, map[string]any{"Id": eventID, "Names": []string{"/0g-serving-provider-event"}})
			}
			_ = json.NewEncoder(w).Encode(list)
		case strings.HasSuffix(r.URL.Path, "/containers/create"):
			l.add("create broker")
			_ = json.NewEncoder(w).Encode(map[string]any{"Id": "beef" + strings.Repeat("0", 60)})
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
				"Config":      map[string]any{"Image": "old:tag"},
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
	okPull     = `{"status":"Status: Downloaded newer image for 0g-serving-broker"}`
)

func testLogger(t *testing.T) log.Logger {
	t.Helper()
	l, err := log.GetLogger(&commonconfig.LoggerConfig{Format: "text", Level: "error"})
	if err != nil {
		t.Fatalf("building logger: %v", err)
	}
	return l
}

func newChangeCtrl(t *testing.T, l *opLog, emitErr error, configFile string, withEvent bool) *Ctrl {
	t.Helper()
	t.Cleanup(docker.SetHostnameForTests(selfHost))
	return &Ctrl{
		config: config.ControllerConfig{
			ImageRepo:  "ghcr.io/0gfoundation/0g-serving-broker",
			ConfigFile: configFile,
		},
		dockerClient: fakeChangeDaemon(t, l, withEvent),
		emitter:      &fakeEmitter{log: l, err: emitErr},
		logger:       testLogger(t),
	}
}

// The load-bearing invariant: a change is in RTMR3 before it happens, and the
// broker restarts within the same call so the record reaches the quote it serves.
//
// The broker takes its quote once at startup and serves it from cache, so an
// event emitted while it runs is invisible to every reader until it restarts.
// Nothing in the type system says so — this is the test that says it.
func TestConfigChangeIsRecordedBeforeItHappens(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("service:\n  name: before\n"), 0o644); err != nil {
		t.Fatalf("seeding the config file: %v", err)
	}

	l := &opLog{}
	c := newChangeCtrl(t, l, nil, path, true)

	const content = "service:\n  name: after\n"
	if err := c.ApplyCoreConfig(context.Background(), content); err != nil {
		t.Fatalf("ApplyCoreConfig() = %v, want nil", err)
	}

	sum := sha256.Sum256([]byte(content))
	wantEmit := "emit zg-config-update " + hex.EncodeToString(sum[:])
	ops := l.all()
	if len(ops) == 0 || ops[0] != wantEmit {
		t.Fatalf("ops = %v, want %q first", ops, wantEmit)
	}
	// The restart is what publishes the record. A path that emitted and did not
	// restart would leave users reading a quote taken before the change.
	if l.indexOf("restart broker") < 0 {
		t.Errorf("ops = %v, want the broker restarted in the same call", ops)
	}

	// Written after the record, not before: the point of the order is that a
	// crash in between leaves a recorded change that did not take effect, never
	// an unrecorded one that did.
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
	c := newChangeCtrl(t, l, errors.New("dstack.sock: connection refused"), path, true)

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

// Same invariant on the upgrade path, which has more ways to get the order wrong:
// the record has to precede the pull as well as the container work, because a
// pull is already a change to the machine's state.
func TestImageChangeIsRecordedBeforeItHappens(t *testing.T) {
	l := &opLog{}
	c := newChangeCtrl(t, l, nil, "", false)

	// Fails at the end, when the absent event container cannot be resolved. That
	// is past everything this test is about, and short of the contract sync, which
	// needs a chain.
	_, err := c.UpdateImages(context.Background(), testDigest)
	if err == nil {
		t.Fatal("UpdateImages() = nil, want the event container to be unresolvable")
	}

	ops := l.all()
	want := "emit zg-image-update ghcr.io/0gfoundation/0g-serving-broker@" + testDigest
	if len(ops) == 0 || ops[0] != want {
		t.Fatalf("ops = %v, want %q first", ops, want)
	}
	// Named individually rather than as "anything after index 0": the pull is the
	// one people read as harmless, and it is not — it changes which images the
	// daemon holds, and it is the step a failed upgrade can stop at.
	for _, op := range []string{"pull", "stop broker", "remove", "create broker"} {
		if l.indexOf(op) <= 0 {
			t.Errorf("ops = %v, want %q after the record", ops, op)
		}
	}
}

func TestImageChangeAbortsWhenItCannotBeRecorded(t *testing.T) {
	l := &opLog{}
	c := newChangeCtrl(t, l, errors.New("dstack.sock: connection refused"), "", false)

	result, err := c.UpdateImages(context.Background(), testDigest)
	if err == nil {
		t.Fatal("UpdateImages() = nil, want an error when the change cannot be recorded")
	}
	// The handler reads a non-nil result as partial progress. There was none, not
	// even a pull.
	if result != nil {
		t.Errorf("UpdateImages() = %+v, want a nil result", result)
	}
	if ops := l.all(); len(ops) != 1 {
		t.Errorf("ops = %v, want the failed record and nothing else", ops)
	}
}

// RTMR3 is a ledger, and the last zg-image-update in it is what a reader believes
// is running. Two upgrades interleaving would leave the last event and the last
// container created coming from different requests, so the second caller is
// refused rather than queued: it has to know its digest is not the one that won.
func TestConcurrentChangesAreRefused(t *testing.T) {
	l := &opLog{}
	c := newChangeCtrl(t, l, nil, filepath.Join(t.TempDir(), "config.yaml"), true)

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
