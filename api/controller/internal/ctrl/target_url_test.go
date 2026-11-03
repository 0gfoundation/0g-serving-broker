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

// brokerEnvDaemon is a docker daemon whose broker container runs with env.
func brokerEnvDaemon(t *testing.T, env []string) *docker.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/_ping"):
			w.Header().Set("Api-Version", "1.47")
		case strings.HasSuffix(r.URL.Path, "/containers/json"):
			_ = json.NewEncoder(w).Encode([]map[string]any{{"Id": "brokercontainerid", "Names": []string{"/" + containerBroker}, "State": "running"}})
		case strings.Contains(r.URL.Path, "/containers/"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"Id": "brokercontainerid", "Name": "/" + containerBroker,
				"State":  map[string]any{"Status": "running"},
				"Config": map[string]any{"Env": env},
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	dc, err := docker.NewClient(config.ControllerConfig{Docker: config.DockerConfig{Host: srv.URL, APIVersion: "1.47"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = dc.Close() })
	return dc
}

func targetURLCtrl(t *testing.T, brokerEnv []string, fileTarget string) (*Ctrl, *opLog) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("service:\n  targetUrl: "+fileTarget+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.ServiceFromYAML([]byte("service:\n  targetUrl: " + fileTarget + "\n"))
	if err != nil {
		t.Fatal(err)
	}
	l := &opLog{}
	return &Ctrl{
		config:       config.ControllerConfig{RecordUpstreamSet: true, ConfigFile: path},
		fullConfig:   &config.Config{Service: *cfg},
		dockerClient: brokerEnvDaemon(t, brokerEnv),
		emitter:      &fakeEmitter{log: l},
		logger:       testLogger(t),
	}, l
}

// The broker forwards to its TARGET_URL, which overrides the file's targetUrl. The set has to
// name that destination, not the file's — the controller's own environment usually has no
// TARGET_URL at all.
func TestTheSetNamesTheBrokersTargetURL(t *testing.T) {
	t.Setenv(config.TargetURLEnvVar, "") // restored after; the code under test sets it
	c, l := targetURLCtrl(t, []string{"TARGET_URL=http://sglang:30000/v1"}, "http://vllm:8000/v1")

	if err := c.RecordUpstreamSet(context.Background()); err != nil {
		t.Fatalf("RecordUpstreamSet() = %v", err)
	}
	got := emitted(l)
	if len(got) != 1 || !strings.Contains(got[0], "http://sglang:30000/v1") || strings.Contains(got[0], "vllm") {
		t.Fatalf("recorded %q, want the broker's TARGET_URL and not the file's targetUrl", got)
	}
	// And later computations (config changes) see it too.
	svc, err := config.ServiceFromYAML([]byte("service:\n  targetUrl: http://other:1/v1\n"))
	if err != nil || svc.TargetURL != "http://sglang:30000/v1" {
		t.Errorf("a later load resolves targetUrl to %q, want the broker's TARGET_URL", svc.TargetURL)
	}
}

// A TARGET_URL on the controller that the broker does not have must not decide the set: the
// broker runs on the file's targetUrl.
func TestTheControllersOwnTargetURLDoesNotDecideTheSet(t *testing.T) {
	t.Setenv(config.TargetURLEnvVar, "http://controller-only:1/v1")
	c, l := targetURLCtrl(t, nil, "http://vllm:8000/v1")
	c.fullConfig.Service.TargetURL = "http://controller-only:1/v1" // as the startup load left it

	if err := c.RecordUpstreamSet(context.Background()); err != nil {
		t.Fatalf("RecordUpstreamSet() = %v", err)
	}
	got := emitted(l)
	if len(got) != 1 || !strings.Contains(got[0], "http://vllm:8000/v1") || strings.Contains(got[0], "controller-only") {
		t.Fatalf("recorded %q, want the file's targetUrl", got)
	}
}

// With no broker container nothing forwards, and refusing to start would leave no controller
// to recreate it: the set is computed from the controller's own view.
func TestNoBrokerContainerDoesNotStopTheController(t *testing.T) {
	t.Setenv(config.TargetURLEnvVar, "")
	c, l := targetURLCtrl(t, nil, "http://vllm:8000/v1")
	c.dockerClient = emptyDaemon(t)

	if err := c.RecordUpstreamSet(context.Background()); err != nil {
		t.Fatalf("RecordUpstreamSet() = %v, want the set recorded from the controller's own view", err)
	}
	if got := emitted(l); len(got) != 1 || !strings.Contains(got[0], "http://vllm:8000/v1") {
		t.Fatalf("recorded %q", got)
	}
}

// Whitespace around the broker's value is not part of the destination, as on the broker.
func TestTheBrokersTargetURLIsTrimmed(t *testing.T) {
	t.Setenv(config.TargetURLEnvVar, "")
	c, l := targetURLCtrl(t, []string{"TARGET_URL= http://sglang:30000/v1 "}, "http://vllm:8000/v1")
	if err := c.RecordUpstreamSet(context.Background()); err != nil {
		t.Fatalf("RecordUpstreamSet() = %v", err)
	}
	if got := emitted(l); len(got) != 1 || !strings.Contains(got[0], " http://sglang:30000/v1") || strings.Contains(got[0], "/v1 ") {
		t.Fatalf("recorded %q, want the trimmed URL", got)
	}
}

func emptyDaemon(t *testing.T) *docker.Client { return daemonWith(t, "[]") }

// A neighbour whose name contains the broker's is not the broker.
func TestANeighbourIsNotTheBroker(t *testing.T) {
	t.Setenv(config.TargetURLEnvVar, "")
	c, l := targetURLCtrl(t, nil, "http://vllm:8000/v1")
	c.dockerClient = daemonWith(t, `[{"Id":"dbcontainerid","Names":["/`+containerBroker+`-db"],"State":"running"}]`)

	if err := c.RecordUpstreamSet(context.Background()); err != nil {
		t.Fatalf("RecordUpstreamSet() = %v", err)
	}
	if got := emitted(l); len(got) != 1 || !strings.Contains(got[0], "http://vllm:8000/v1") {
		t.Fatalf("recorded %q", got)
	}
}

func daemonWith(t *testing.T, list string) *docker.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/_ping"):
			w.Header().Set("Api-Version", "1.47")
		case strings.HasSuffix(r.URL.Path, "/containers/json"):
			_, _ = w.Write([]byte(list))
		case strings.Contains(r.URL.Path, "/containers/"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"Id": "dbcontainerid", "Name": "/" + containerBroker + "-db",
				"State":  map[string]any{"Status": "running"},
				"Config": map[string]any{"Env": []string{"TARGET_URL=http://elsewhere:1/v1"}},
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	dc, err := docker.NewClient(config.ControllerConfig{Docker: config.DockerConfig{Host: srv.URL, APIVersion: "1.47"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = dc.Close() })
	return dc
}
