package ctrl

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/0glabs/0g-serving-broker/controller/internal/docker"
	"github.com/0glabs/0g-serving-broker/inference/cmd/validateconfig"
	"github.com/0glabs/0g-serving-broker/inference/config"
)

// validator behaviours for a fake container's 0g-validate-config
const (
	validates  = "pass"
	oldImage   = "old"         // no such applet: exits non-zero and writes nothing
	silentZero = "silent-zero" // exit 0 but no confirmation, like a null docker exit code
)

// wantContent, when set, is what the fake validator requires the staged candidate to be.
var wantContent string

type execResult struct {
	code int
	out  string
}

type guardContainer struct {
	id, name, digest string
	validator        string // validates, oldImage, or "refuse:<reason>"
}

// splitImageDaemon serves a broker, an event container and the controller with the
// given digests — e.g. the state after `redeploy.sh --digest` hot-switched the broker —
// and runs each container's 0g-validate-config as its validator field says: a refusal
// reason comes back on the exec's output stream, as from the real applet (which cannot
// write anything: the config volume is read-only in those containers).
func splitImageDaemon(t *testing.T, cs ...guardContainer) *docker.Client {
	t.Helper()
	var mu sync.Mutex
	execs := map[string]execResult{}
	inspected := map[string]bool{}
	n := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case strings.HasSuffix(r.URL.Path, "/_ping"):
			w.WriteHeader(http.StatusOK)
		case strings.HasSuffix(r.URL.Path, "/containers/json"):
			list := make([]map[string]any, 0, len(cs))
			for _, ct := range cs {
				list = append(list, map[string]any{"Id": ct.id, "Names": []string{"/" + ct.name}})
			}
			_ = json.NewEncoder(w).Encode(list)
		case strings.HasSuffix(r.URL.Path, "/exec") && r.Method == http.MethodPost:
			var body struct{ Cmd []string }
			_ = json.NewDecoder(r.Body).Decode(&body)
			for _, ct := range cs {
				if !strings.Contains(r.URL.Path, "/containers/"+ct.id+"/") {
					continue
				}
				res := execResult{}
				switch {
				case len(body.Cmd) != 3 || body.Cmd[0] != brokerBinary || body.Cmd[1] != "0g-validate-config":
					res = execResult{2, "usage"}
				case ct.validator == oldImage:
					res = execResult{1, "0g-validate-config: applet not found"}
				case strings.HasPrefix(ct.validator, "refuse:"):
					res = execResult{1, strings.TrimPrefix(ct.validator, "refuse:")}
				case ct.validator == silentZero:
					res = execResult{0, ""} // exit 0 without the applet's confirmation
				default:
					// Validate what was actually staged: the pushed content, 0600.
					info, err := os.Stat(body.Cmd[2])
					data, _ := os.ReadFile(body.Cmd[2])
					switch {
					case err != nil:
						res = execResult{1, err.Error()}
					case info.Mode().Perm() != 0o600:
						res = execResult{1, "candidate mode " + info.Mode().Perm().String()}
					case wantContent != "" && string(data) != wantContent:
						res = execResult{1, "candidate is not the pushed content"}
					default:
						res = execResult{0, validateconfig.OK}
					}
				}
				n++
				id := fmt.Sprintf("exec%d", n)
				execs[id] = res
				_ = json.NewEncoder(w).Encode(map[string]any{"Id": id})
				return
			}
			w.WriteHeader(http.StatusNotFound)
		case strings.Contains(r.URL.Path, "/exec/") && strings.HasSuffix(r.URL.Path, "/start"):
			// An attached start: hijack, answer 101, then stream the output as docker's
			// multiplexed frames (stderr) and close — the process has "exited".
			var res execResult
			for id, rr := range execs {
				if strings.Contains(r.URL.Path, "/exec/"+id+"/") {
					res = rr
				}
			}
			_, _ = io.Copy(io.Discard, r.Body) // unread input would turn the close into a reset
			conn, buf, err := w.(http.Hijacker).Hijack()
			if err != nil {
				return
			}
			defer conn.Close()
			_, _ = buf.WriteString("HTTP/1.1 101 UPGRADED\r\nContent-Type: application/vnd.docker.multiplexed-stream\r\nConnection: Upgrade\r\nUpgrade: tcp\r\n\r\n")
			if res.out != "" {
				hdr := make([]byte, 8)
				hdr[0] = 2 // stderr
				binary.BigEndian.PutUint32(hdr[4:], uint32(len(res.out)))
				_, _ = buf.Write(hdr)
				_, _ = buf.WriteString(res.out)
			}
			_ = buf.Flush()
			if tc, ok := conn.(*net.TCPConn); ok {
				_ = tc.CloseWrite() // EOF to the reader, without resetting the connection
			}
		case strings.Contains(r.URL.Path, "/exec/") && strings.HasSuffix(r.URL.Path, "/json"):
			for id, res := range execs {
				if strings.Contains(r.URL.Path, "/exec/"+id+"/") {
					// Still running on the first inspect, so the caller's wait loop is exercised.
					// Like docker, the exit code reads 0 until the process has exited.
					running := !inspected[id]
					inspected[id] = true
					code := res.code
					if running {
						code = 0
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"ID": id, "Running": running, "ExitCode": code})
					return
				}
			}
			w.WriteHeader(http.StatusNotFound)
		case strings.HasSuffix(r.URL.Path, "/json"):
			for _, ct := range cs {
				if strings.Contains(r.URL.Path, "/containers/"+ct.id+"/") {
					_ = json.NewEncoder(w).Encode(map[string]any{
						"Id": ct.id, "Image": "sha256:" + strings.Repeat("e", 64),
						"Config": map[string]any{"Image": imageRepo + "@" + ct.digest},
						"State":  map[string]any{"Status": "running"},
					})
					return
				}
			}
			w.WriteHeader(http.StatusNotFound)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	// tcp://, not http://: an attached exec hijacks the connection, and the docker client
	// dials the hijack by the host's scheme.
	c, err := docker.NewClient(config.ControllerConfig{Docker: config.DockerConfig{Host: "tcp://" + strings.TrimPrefix(srv.URL, "http://"), APIVersion: "1.47"}})
	if err != nil {
		t.Fatalf("building docker client: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func containers(brokerDigest, eventDigest, brokerValidator, eventValidator string) []guardContainer {
	return []guardContainer{
		{brokerID, containerBroker, brokerDigest, brokerValidator},
		{eventID, containerEvent, eventDigest, eventValidator},
		{selfID, "0g-controller", prevDigest, validates},
	}
}

// With the broker on another image, the controller cannot know whether that broker
// ignores keys its own code does not read, or is an older one that decodes strictly
// and would crash-loop on the file after the change is in RTMR3. It refuses — before
// recording anything — and says how to proceed.
func TestConfigChangeRefusesIgnoredKeysWhenTheBrokerRunsAnotherImage(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	const before = "service:\n  model: before\n"
	if err := os.WriteFile(path, []byte(before), 0o644); err != nil {
		t.Fatalf("seeding the config file: %v", err)
	}
	l := &opLog{}
	c := newChangeCtrl(t, l, nil, path, okPull)
	c.dockerClient = splitImageDaemon(t, containers(testDigest, prevDigest, oldImage, validates)...)

	err := c.ApplyCoreConfig(context.Background(), "service:\n  model: after\nfutureFeature: 1\n")
	if _, ok := err.(*InvalidConfigError); !ok {
		t.Fatalf("ApplyCoreConfig() = %v, want an InvalidConfigError (400)", err)
	}
	for _, want := range []string{`"futureFeature"`, testDigest, prevDigest, "applet not found"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q should mention %q", err, want)
		}
	}
	if ops := l.all(); len(ops) != 0 {
		t.Errorf("ops = %v, want nothing recorded: RTMR3 only appends", ops)
	}
	if got, _ := os.ReadFile(path); string(got) != before {
		t.Errorf("config file = %q, want it untouched", got)
	}

	// The same broker/controller split does not block a config with only known keys:
	// the guard is about ignored keys, not about hot-switched images in general.
	if err := c.checkIgnoredKeysAreSafe(context.Background(), "service:\n  model: after\n"); err != nil {
		t.Errorf("known keys only: checkIgnoredKeysAreSafe() = %v, want nil", err)
	}
}

// If the controller cannot read its own container's digest (renamed container, daemon
// error), it cannot compare, and fails closed for ignored keys.
func TestConfigChangeRefusesIgnoredKeysWhenItCannotCompareImages(t *testing.T) {
	l := &opLog{}
	c := newChangeCtrl(t, l, nil, filepath.Join(t.TempDir(), "config.yaml"), okPull)
	c.dockerClient = fakeEmptyDaemon(t, l) // no broker, no controller container

	if err := c.checkIgnoredKeysAreSafe(context.Background(), "futureFeature: 1\n"); err == nil {
		t.Fatal("checkIgnoredKeysAreSafe() = nil, want a refusal when the images cannot be compared")
	}
}

// The reverse path: an image switch while the config on disk carries keys this
// controller's code ignores. Switching to a newer image that reads them is the intended
// workflow, so it is not refused — but a rollback to a strict pre-tolerance image would
// crash-loop, and the operator is told so. The shared fake daemon reports every
// container, the controller included, on prevDigest.
func TestImageSwitchWarnsAboutIgnoredKeysOnDisk(t *testing.T) {
	dir := t.TempDir()
	withFuture := filepath.Join(dir, "future.yaml")
	if err := os.WriteFile(withFuture, []byte("service:\n  model: m\nfutureFeature: 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	known := filepath.Join(dir, "known.yaml")
	if err := os.WriteFile(known, []byte("service:\n  model: m\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	l := &opLog{}
	c := newChangeCtrl(t, l, nil, withFuture, okPull)
	if w := c.ignoredKeysImageWarning(context.Background(), testDigest); !strings.Contains(w, `"futureFeature"`) || !strings.Contains(w, "remove those keys first") {
		t.Errorf("switching to another image with ignored keys on disk: warning = %q, want one naming the key", w)
	}
	if w := c.ignoredKeysImageWarning(context.Background(), prevDigest); w != "" {
		t.Errorf("switching to the controller's own image: warning = %q, want none", w)
	}
	c.config.ConfigFile = known
	if w := c.ignoredKeysImageWarning(context.Background(), testDigest); w != "" {
		t.Errorf("no ignored keys on disk: warning = %q, want none", w)
	}

	// And it reaches the operator: every result UpdateImages returns carries it — here
	// a failed one (this fake cannot recreate the event container), which the handler
	// serialises like a successful one. Refusals after the lock that return no result
	// only log it; the digest-validation and change-in-progress refusals return before
	// it is computed.
	c.config.ConfigFile = withFuture
	result, _ := c.UpdateImages(context.Background(), testDigest)
	if result == nil || !strings.Contains(result.Warning, `"futureFeature"`) {
		t.Fatalf("UpdateImages result = %+v, want the ignored-keys warning in it", result)
	}
}

// The event container restarts onto the same file and decodes it with the same package,
// so it must run the controller's image too.
func TestConfigChangeRefusesIgnoredKeysWhenTheEventServiceRunsAnotherImage(t *testing.T) {
	l := &opLog{}
	c := newChangeCtrl(t, l, nil, filepath.Join(t.TempDir(), "config.yaml"), okPull)
	c.dockerClient = splitImageDaemon(t, containers(prevDigest, testDigest, validates, oldImage)...)
	err := c.checkIgnoredKeysAreSafe(context.Background(), "futureFeature: 1\n")
	if err == nil || !strings.Contains(err.Error(), "the event service runs "+testDigest) {
		t.Fatalf("checkIgnoredKeysAreSafe() = %v, want a refusal naming the event service's image", err)
	}
}

// After pushing a key and then hot-switching the broker to the image that reads it, the
// images differ. The controller then asks the running images themselves: their
// validator judges the keys and the values with the code that will read them. Pushes
// they accept go through — that is the workflow — and anything they refuse, or cannot
// judge, does not.
func TestConfigChangeAsksTheRunningImagesWhenTheyDiffer(t *testing.T) {
	const content = "service:\n  model: m\nfutureFeature:\n  threshold: 7\n"
	cases := []struct {
		name          string
		broker, event string
		wantErr       string // "" = accepted
	}{
		{"both images accept it", validates, validates, ""},
		{"the broker's image refuses the value", "refuse:futureFeature.threshold must be at most 1", validates, "futureFeature.threshold must be at most 1"},
		{"the event service's image refuses it", validates, "refuse:unknown key", "the event service's image refuses it (exit 1): unknown key"},
		{"an image from before the validator", oldImage, validates, "applet not found"},
		{"exit 0 without the applet's confirmation", validates, silentZero, "did not confirm"},
	}
	wantContent = content
	t.Cleanup(func() { wantContent = "" })
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			l := &opLog{}
			c := newChangeCtrl(t, l, nil, filepath.Join(dir, "config.yaml"), okPull)
			c.dockerClient = splitImageDaemon(t, containers(testDigest, testDigest, tc.broker, tc.event)...)
			err := c.checkIgnoredKeysAreSafe(context.Background(), content)
			if tc.wantErr == "" && err != nil {
				t.Fatalf("checkIgnoredKeysAreSafe() = %v, want accepted", err)
			}
			if tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)) {
				t.Fatalf("checkIgnoredKeysAreSafe() = %v, want a refusal mentioning %q", err, tc.wantErr)
			}
			// The staged candidate carries resolved secrets: nothing may be left behind.
			if left, _ := filepath.Glob(filepath.Join(dir, ".candidate-*")); len(left) != 0 {
				t.Errorf("left behind %v", left)
			}
		})
	}
}

// If the controller cannot identify its own container, it cannot compare images and
// fails closed — even with the broker and event service readable.
func TestConfigChangeRefusesIgnoredKeysWithoutItsOwnDigest(t *testing.T) {
	l := &opLog{}
	c := newChangeCtrl(t, l, nil, filepath.Join(t.TempDir(), "config.yaml"), okPull)
	cs := containers(prevDigest, prevDigest, oldImage, oldImage)
	c.dockerClient = splitImageDaemon(t, cs[0], cs[1]) // no container matches our hostname
	if err := c.checkIgnoredKeysAreSafe(context.Background(), "futureFeature: 1\n"); err == nil || !strings.Contains(err.Error(), "own image") {
		t.Fatalf("checkIgnoredKeysAreSafe() = %v, want a refusal naming the controller's own image", err)
	}
}

// Container lookup falls back to a substring match; a neighbour must never stand in for
// the broker when comparing images.
func TestConfigChangeRefusesIgnoredKeysWhenTheBrokerNameOnlyNearlyMatches(t *testing.T) {
	l := &opLog{}
	c := newChangeCtrl(t, l, nil, filepath.Join(t.TempDir(), "config.yaml"), okPull)
	cs := containers(prevDigest, prevDigest, oldImage, oldImage)
	cs[0].name = containerBroker + "-old"
	c.dockerClient = splitImageDaemon(t, cs...)
	if err := c.checkIgnoredKeysAreSafe(context.Background(), "futureFeature: 1\n"); err == nil || !strings.Contains(err.Error(), "resolved to container") {
		t.Fatalf("checkIgnoredKeysAreSafe() = %v, want a refusal for the near-miss name", err)
	}
}

// The guard runs under the change lock: while another change holds it, a push is
// refused as in-progress before the guard even looks at the images.
func TestConfigChangeGuardRunsUnderTheLock(t *testing.T) {
	l := &opLog{}
	c := newChangeCtrl(t, l, nil, filepath.Join(t.TempDir(), "config.yaml"), okPull)
	c.dockerClient = fakeEmptyDaemon(t, l) // the guard would refuse with a 400 here
	c.changing.Lock()
	defer c.changing.Unlock()
	if err := c.ApplyCoreConfig(context.Background(), "service:\n  model: m\nfutureFeature: 1\n"); !errors.Is(err, ErrChangeInProgress) {
		t.Fatalf("ApplyCoreConfig() = %v, want ErrChangeInProgress (the guard must run after the lock)", err)
	}
}

// An unreadable controller digest counts as "not the same image": the warning is given.
func TestImageSwitchWarnsWhenItsOwnDigestIsUnreadable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("service:\n  model: m\nfutureFeature: 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	l := &opLog{}
	c := newChangeCtrl(t, l, nil, path, okPull)
	c.dockerClient = fakeEmptyDaemon(t, l)
	if w := c.ignoredKeysImageWarning(context.Background(), prevDigest); !strings.Contains(w, `"futureFeature"`) {
		t.Fatalf("warning = %q, want one naming the key", w)
	}
}
