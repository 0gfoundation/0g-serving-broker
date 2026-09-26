package ctrl

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/0glabs/0g-serving-broker/controller/internal/docker"
	"github.com/0glabs/0g-serving-broker/inference/config"
)

// splitImageDaemon serves a broker and a controller running DIFFERENT digests — the
// state after `redeploy.sh --digest` hot-switched the broker.
func splitImageDaemon(t *testing.T, brokerDigest, controllerDigest string) *docker.Client {
	t.Helper()
	images := map[string]string{
		brokerID: imageRepo + "@" + brokerDigest,
		selfID:   imageRepo + "@" + controllerDigest,
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/_ping"):
			w.WriteHeader(http.StatusOK)
		case strings.HasSuffix(r.URL.Path, "/containers/json"):
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"Id": brokerID, "Names": []string{"/" + containerBroker}},
				{"Id": selfID, "Names": []string{"/" + containerController}},
			})
		case strings.HasSuffix(r.URL.Path, "/json"):
			for id, ref := range images {
				if strings.Contains(r.URL.Path, "/containers/"+id+"/") {
					_ = json.NewEncoder(w).Encode(map[string]any{
						"Id": id, "Image": "sha256:" + strings.Repeat("e", 64),
						"Config": map[string]any{"Image": ref},
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
	c, err := docker.NewClient(config.ControllerConfig{Docker: config.DockerConfig{Host: srv.URL, APIVersion: "1.47"}})
	if err != nil {
		t.Fatalf("building docker client: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
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
	c.dockerClient = splitImageDaemon(t, testDigest, prevDigest)

	err := c.ApplyCoreConfig(context.Background(), "service:\n  model: after\nfutureFeature: 1\n")
	if _, ok := err.(*InvalidConfigError); !ok {
		t.Fatalf("ApplyCoreConfig() = %v, want an InvalidConfigError (400)", err)
	}
	for _, want := range []string{`"futureFeature"`, testDigest, prevDigest, "before switching the image"} {
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

	// And it reaches the operator: the result UpdateImages returns carries it, on the
	// failed path too (this fake cannot recreate the event container, and the handler
	// reports the result either way).
	c.config.ConfigFile = withFuture
	result, _ := c.UpdateImages(context.Background(), testDigest)
	if result == nil || !strings.Contains(result.Warning, `"futureFeature"`) {
		t.Fatalf("UpdateImages result = %+v, want the ignored-keys warning in it", result)
	}
}
