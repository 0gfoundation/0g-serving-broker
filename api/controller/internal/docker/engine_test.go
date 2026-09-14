package docker

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"

	"github.com/0glabs/0g-serving-broker/inference/config"
)

// created is everything the daemon was asked to create, so a test can assert what the
// container would actually be rather than only that the call returned nil.
type created struct {
	name string
	cfg  container.Config
	host container.HostConfig
	net  network.NetworkingConfig
}

// engineDaemon serves the create endpoint and hands back what it was given.
func engineDaemon(t *testing.T) (*Client, *created) {
	t.Helper()

	var got created

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/_ping"):
			w.Header().Set("Api-Version", "1.47")
		case strings.HasSuffix(r.URL.Path, "/containers/create"):
			var body struct {
				container.Config
				HostConfig       container.HostConfig
				NetworkingConfig network.NetworkingConfig
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			got = created{
				name: r.URL.Query().Get("name"),
				cfg:  body.Config,
				host: body.HostConfig,
				net:  body.NetworkingConfig,
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"Id": strings.Repeat("a", 64)})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)

	c, err := NewClient(config.ControllerConfig{
		Docker: config.DockerConfig{Host: srv.URL, APIVersion: "1.47"},
	})
	if err != nil {
		t.Fatalf("building docker client: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c, &got
}

func engineSpec() CreateEngineSpec {
	return CreateEngineSpec{
		Name:  "dsv4flash",
		Image: "lmsysorg/sglang@sha256:" + strings.Repeat("1", 64),
		GPUs:  "6,7",
		Port:  8000,
		Args:  []string{"--model-path", "x", "--port", "8000"},
	}
}

// The refusal that keeps a created engine inside its own boundary. Volumes are the
// controller's to decide and a named volume is all it decides — a source that is a host
// path would let the container read the CVM's filesystem, and the docker socket among
// other things would let it create containers nothing records.
func TestCreateEngineRefusesAHostPathVolume(t *testing.T) {
	for _, tt := range []struct {
		name   string
		volume string
		want   string
	}{
		{"an absolute host path", "/var/run/docker.sock:/var/run/docker.sock", "names a host path"},
		{"the CVM's own root", "/:/host", "names a host path"},
		{"a relative path", "./secrets:/secrets", "names a host path"},
		{"a parent-relative path", "../etc:/etc", "names a host path"},
		{"no target at all", "hfcache", "is not source:target"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			c, got := engineDaemon(t)
			spec := engineSpec()
			spec.Volumes = []string{tt.volume}

			err := c.CreateEngine(context.Background(), spec)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("CreateEngine() = %v, want a refusal mentioning %q", err, tt.want)
			}
			// Refused BEFORE the daemon is asked, so there is no container to clean up.
			if got.cfg.Image != "" {
				t.Errorf("a container was created despite the refusal: %+v", got.cfg)
			}
		})
	}
}

func TestCreateEngineAcceptsANamedVolume(t *testing.T) {
	c, got := engineDaemon(t)
	spec := engineSpec()
	spec.Volumes = []string{"hfcache:/root/.cache/huggingface"}

	if err := c.CreateEngine(context.Background(), spec); err != nil {
		t.Fatalf("CreateEngine() = %v, want nil", err)
	}
	if len(got.host.Binds) != 1 || got.host.Binds[0] != "hfcache:/root/.cache/huggingface" {
		t.Errorf("binds = %v, want the named volume", got.host.Binds)
	}
}

func TestCreateEngineBuildsAContainedContainer(t *testing.T) {
	c, got := engineDaemon(t)
	spec := engineSpec()
	spec.IPCHost = true
	spec.ShmSize = "32gb"
	spec.Network = "zg"
	spec.Env = map[string]string{"HF_HOME": "/root/.cache/huggingface"}

	if err := c.CreateEngine(context.Background(), spec); err != nil {
		t.Fatalf("CreateEngine() = %v, want nil", err)
	}

	if got.cfg.Labels[EngineLabel] == "" {
		t.Errorf("labels = %v, want %s: the label is how the engine set is derived at all", got.cfg.Labels, EngineLabel)
	}
	// No ports published to the host. Publishing would put an unauthenticated inference
	// server on the machine's interfaces; the broker reaches it over the compose network.
	if len(got.host.PortBindings) != 0 {
		t.Errorf("port bindings = %v, want none", got.host.PortBindings)
	}
	if got.host.Privileged || len(got.host.CapAdd) != 0 {
		t.Errorf("hostConfig grants privileges: privileged=%v capAdd=%v", got.host.Privileged, got.host.CapAdd)
	}
	if got.host.Runtime != nvidiaRuntime {
		t.Errorf("runtime = %q, want %q", got.host.Runtime, nvidiaRuntime)
	}
	// The runtime alone gives nothing: without a device request the container starts with
	// no cards at all. Count -1 is "every card the container can see", which the GPU
	// environment variable is what narrows.
	if len(got.host.DeviceRequests) != 1 || got.host.DeviceRequests[0].Count != -1 {
		t.Fatalf("device requests = %+v, want one asking for every visible card", got.host.DeviceRequests)
	}
	if caps := got.host.DeviceRequests[0].Capabilities; len(caps) != 1 || len(caps[0]) != 1 || caps[0][0] != "gpu" {
		t.Errorf("capabilities = %v, want [[gpu]]", caps)
	}
	// Unless-stopped rather than always, matching what the compose gives its own engines:
	// a container an operator stopped stays stopped across a daemon restart.
	if got.host.RestartPolicy.Name != container.RestartPolicyUnlessStopped {
		t.Errorf("restart policy = %q, want %q", got.host.RestartPolicy.Name, container.RestartPolicyUnlessStopped)
	}
	if got.host.IpcMode != container.IPCModeHost {
		t.Errorf("ipc = %q, want host for multi-process tensor parallelism", got.host.IpcMode)
	}
	if got.host.ShmSize != 32<<30 {
		t.Errorf("shm = %d, want %d", got.host.ShmSize, int64(32)<<30)
	}
	// The GPU narrowing, which is the only thing that keeps a second engine off the
	// first one's cards.
	if !containsEnv(got.cfg.Env, GPUEnvVar+"=6,7") {
		t.Errorf("env = %v, want %s=6,7", got.cfg.Env, GPUEnvVar)
	}
	if !containsEnv(got.cfg.Env, "HF_HOME=/root/.cache/huggingface") {
		t.Errorf("env = %v, want the controller's own variables", got.cfg.Env)
	}
}

// A spec's Env may not override the GPU narrowing: the spec's GPUs field is what the
// caller's request was checked against, so a second spelling winning would let a
// container hold cards the check never looked at.
func TestCreateEngineLetsTheGPUFieldDecideOverTheEnvironment(t *testing.T) {
	c, got := engineDaemon(t)
	spec := engineSpec()
	spec.Env = map[string]string{GPUEnvVar: "all"}

	if err := c.CreateEngine(context.Background(), spec); err != nil {
		t.Fatalf("CreateEngine() = %v, want nil", err)
	}
	if containsEnv(got.cfg.Env, GPUEnvVar+"=all") {
		t.Errorf("env = %v, want the spec's GPUs to decide, not an environment entry", got.cfg.Env)
	}
	if !containsEnv(got.cfg.Env, GPUEnvVar+"=6,7") {
		t.Errorf("env = %v, want %s=6,7", got.cfg.Env, GPUEnvVar)
	}
}

func TestParseShmSize(t *testing.T) {
	for _, tt := range []struct {
		in      string
		want    int64
		wantErr bool
	}{
		{"32gb", 32 << 30, false},
		{"32g", 32 << 30, false},
		{"1024m", 1024 << 20, false},
		{"1024mb", 1024 << 20, false},
		{"64", 64, false},
		{"", 0, true},
		{"0gb", 0, true},
		{"-1gb", 0, true},
		{"big", 0, true},
	} {
		t.Run(tt.in, func(t *testing.T) {
			got, err := parseShmSize(tt.in)
			if (err != nil) != tt.wantErr {
				t.Fatalf("parseShmSize(%q) = %d, %v; wantErr %v", tt.in, got, err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("parseShmSize(%q) = %d, want %d", tt.in, got, tt.want)
			}
		})
	}
}

func TestHeldGPUs(t *testing.T) {
	for _, tt := range []struct {
		name string
		c    Container
		want []string
	}{{
		// The regression that matters most: a CVM runs a dozen containers with no GPU
		// access, and none of them sets the variable. Reading that as "every card" would
		// report the machine occupied forever.
		name: "no GPU access at all",
		c:    Container{},
		want: nil,
	}, {
		name: "a GPU container that does not narrow",
		c:    Container{HasGPU: true},
		want: []string{GPUAll},
	}, {
		name: "all, spelled out",
		c:    Container{HasGPU: true, GPUs: "all"},
		want: []string{GPUAll},
	}, {
		name: "ALL, since docker does not care about case",
		c:    Container{HasGPU: true, GPUs: "ALL"},
		want: []string{GPUAll},
	}, {
		name: "specific cards, out of order",
		c:    Container{HasGPU: true, GPUs: "7,6"},
		want: []string{"6", "7"},
	}, {
		name: "specific cards with spaces",
		c:    Container{HasGPU: true, GPUs: " 6 , 7 "},
		want: []string{"6", "7"},
	}, {
		// A spelling this does not understand widens the claim rather than narrowing it:
		// the unknown spellings include ones that mean more cards, and only the widening
		// reading is safe to be wrong about.
		name: "a device UUID",
		c:    Container{HasGPU: true, GPUs: "GPU-8f2c1d3e"},
		want: []string{GPUAll},
	}, {
		name: "void, which means no cards but is not a list",
		c:    Container{HasGPU: true, GPUs: "void"},
		want: []string{GPUAll},
	}, {
		name: "a trailing comma",
		c:    Container{HasGPU: true, GPUs: "6,"},
		want: []string{"6"},
	}, {
		name: "nothing but commas",
		c:    Container{HasGPU: true, GPUs: ",,"},
		want: []string{GPUAll},
	}} {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.c.HeldGPUs()
			if strings.Join(got, ",") != strings.Join(tt.want, ",") {
				t.Errorf("HeldGPUs() = %v, want %v", got, tt.want)
			}
		})
	}
}

func containsEnv(env []string, want string) bool {
	for _, e := range env {
		if e == want {
			return true
		}
	}
	return false
}

// Without the network the whole feature does not work: the broker reaches an engine by
// container name over the compose network, and a container on the default bridge is one
// no upstream URL can resolve.
func TestCreateEngineJoinsTheComposeNetworkUnderItsOwnName(t *testing.T) {
	c, got := engineDaemon(t)
	spec := engineSpec()
	spec.Network = "zg"

	if err := c.CreateEngine(context.Background(), spec); err != nil {
		t.Fatalf("CreateEngine() = %v, want nil", err)
	}
	if got.name != "dsv4flash" {
		t.Errorf("created under %q, want the spec's name: it is the host an upstream URL spells", got.name)
	}
	endpoint, ok := got.net.EndpointsConfig["zg"]
	if !ok {
		t.Fatalf("networking = %+v, want an endpoint on the compose network", got.net)
	}
	if len(endpoint.Aliases) != 1 || endpoint.Aliases[0] != "dsv4flash" {
		t.Errorf("aliases = %v, want the container's own name", endpoint.Aliases)
	}
}

func TestCreateEngineNeedsANameAndAnImage(t *testing.T) {
	// Refused here as well as at the API boundary: this function is the last thing
	// between a spec and a running container, and a second caller would inherit none of
	// the API's checks.
	for _, tt := range []struct {
		name string
		spec func(*CreateEngineSpec)
	}{
		{"no name", func(s *CreateEngineSpec) { s.Name = "" }},
		{"no image", func(s *CreateEngineSpec) { s.Image = "" }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			c, got := engineDaemon(t)
			spec := engineSpec()
			tt.spec(&spec)
			if err := c.CreateEngine(context.Background(), spec); err == nil {
				t.Fatal("CreateEngine() = nil, want a refusal")
			}
			if got.cfg.Image != "" {
				t.Errorf("a container was created despite the refusal: %+v", got.cfg)
			}
		})
	}
}

// Two identical specs must build identical containers, which they do not if the
// environment comes out in map iteration order.
func TestCreateEngineBuildsTheEnvironmentDeterministically(t *testing.T) {
	env := map[string]string{"A": "1", "B": "2", "C": "3", "D": "4", "E": "5", "HF_HOME": "/c"}

	var first []string
	for i := 0; i < 8; i++ {
		c, got := engineDaemon(t)
		spec := engineSpec()
		spec.Env = env
		if err := c.CreateEngine(context.Background(), spec); err != nil {
			t.Fatalf("CreateEngine() = %v, want nil", err)
		}
		if first == nil {
			first = got.cfg.Env
			continue
		}
		if strings.Join(got.cfg.Env, "\x00") != strings.Join(first, "\x00") {
			t.Fatalf("environment came out as %v and then %v: two identical specs must build identical containers", first, got.cfg.Env)
		}
	}
}

// The label guard, tested at this layer because the ctrl layer no longer reaches it: it
// resolves an engine by exact labelled name and refuses before calling here. This guard
// stays because this function is the last thing between a name and a removal, and a
// second caller would inherit none of the API's checks — it is what keeps this from
// being a general "delete any container" endpoint.
func TestRemoveEngineRefusesAnUnlabelledContainer(t *testing.T) {
	const id = "eeee" + "111111111111111111111111111111111111111111111111111111111111"

	var removed bool
	labels := map[string]string{}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/_ping"):
			w.Header().Set("Api-Version", "1.47")
		case strings.HasSuffix(r.URL.Path, "/containers/json"):
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"Id": id, "Names": []string{"/prometheus"}},
				// The controller's own container, which every write path resolves first.
				{"Id": strings.Repeat("c", 64), "Names": []string{"/0g-controller"}},
			})
		case r.Method == http.MethodDelete:
			removed = true
			w.WriteHeader(http.StatusNoContent)
		case strings.HasSuffix(r.URL.Path, "/json"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"Id":     id,
				"Name":   "/prometheus",
				"Config": map[string]any{"Labels": labels},
				"State":  map[string]any{"Status": "running"},
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(SetHostnameForTests(strings.Repeat("c", 12)))

	c, err := NewClient(config.ControllerConfig{
		Docker: config.DockerConfig{Host: srv.URL, APIVersion: "1.47"},
	})
	if err != nil {
		t.Fatalf("building docker client: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })

	err = c.RemoveEngine(context.Background(), "prometheus")
	if err == nil || !strings.Contains(err.Error(), "not an engine this controller created") {
		t.Fatalf("RemoveEngine() = %v, want a refusal", err)
	}
	if removed {
		t.Error("the container was removed despite the refusal")
	}

	// And with the label it goes through, so the refusal is about the label and not about
	// the fixture.
	labels[EngineLabel] = "true"
	if err := c.RemoveEngine(context.Background(), "prometheus"); err != nil {
		t.Fatalf("RemoveEngine() = %v, want nil once it carries the label", err)
	}
	if !removed {
		t.Error("the container was not removed")
	}
}
