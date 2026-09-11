package ctrl

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/0glabs/0g-serving-broker/common/attest"
	commonconfig "github.com/0glabs/0g-serving-broker/common/config"
	"github.com/0glabs/0g-serving-broker/common/log"
	"github.com/0glabs/0g-serving-broker/controller/internal/docker"
	"github.com/0glabs/0g-serving-broker/inference/config"
)

// engineRepo and engineRef are the image the fixtures run: a repo the fixture config
// permits, pinned by digest as every permitted image must be.
const (
	engineRepo = "lmsysorg/sglang"
	engineRef  = engineRepo + "@" + testDigest
	// A revision is 40 hex characters because it is a git commit sha, and every request
	// must carry one — see EngineModel.Revision for why that is the load-bearing rule.
	testRevision = "03179e95ab12cd34ef56789012345678901234ab"
)

// fakeContainer is one container on the fake machine.
//
// The GPU fields are separate on purpose: hasGPU says whether the container was given
// cards at all, gpus says which ones, and the two are genuinely independent — a CVM runs
// a dozen containers with no GPU access, and an engine with NVIDIA_VISIBLE_DEVICES=all
// has every card. Conflating them is the bug these fixtures exist to catch.
type fakeContainer struct {
	id      string
	name    string
	image   string
	args    []string
	gpus    string // the NVIDIA_VISIBLE_DEVICES value; "" means the variable is unset
	hasGPU  bool   // gets a device request, the modern compose spelling
	runtime string // "nvidia", the older compose spelling; an alternative to hasGPU
	engine  bool   // carries docker.EngineLabel
	// stopped models a container the daemon lists only when asked for all of them,
	// which is what makes the All:true in the listing observable.
	stopped bool
}

// fakeMachine is a docker daemon with a container table, serving the endpoints the two
// engine paths touch and logging every write into an opLog.
//
// A table rather than the fixed three-container fixture the upgrade tests use, because
// every question here is about what the machine already holds: which cards are taken,
// which engines exist, and what their argument lists are.
type fakeMachine struct {
	mu         sync.Mutex
	containers []fakeContainer
	log        *opLog
	// createErr fails /containers/create, which is the branch that has to correct a
	// record already written.
	createErr bool
	// failListN fails the nth container listing and no other, so a test can put the
	// failure exactly where it wants it — inside the snapshot the record is built from,
	// rather than at the start of the call where it would just abort. 0 never fails.
	failListN int
	lists     int
	// failInspect fails the inspect of this container id. inspectGone picks which
	// failure: a 404, which is the benign race, or a 500, which is not.
	failInspect string
	inspectGone bool
	// createdEnv is the environment the last created container was given, which is the
	// one thing about a created engine that must NOT also be in the record.
	createdEnv []string
}

func newFakeMachine(t *testing.T, l *opLog, containers ...fakeContainer) (*docker.Client, *fakeMachine) {
	t.Helper()

	m := &fakeMachine{containers: containers, log: l}

	// The controller's own container, which every write path resolves before it acts.
	m.containers = append(m.containers, fakeContainer{id: selfID, name: "0g-controller"})
	t.Cleanup(docker.SetHostnameForTests(selfHost))

	srv := httptest.NewServer(http.HandlerFunc(m.serve))
	t.Cleanup(srv.Close)

	c, err := docker.NewClient(config.ControllerConfig{
		Docker: config.DockerConfig{Host: srv.URL, APIVersion: "1.47"},
	})
	if err != nil {
		t.Fatalf("building docker client: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c, m
}

func (m *fakeMachine) find(id string) (fakeContainer, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, c := range m.containers {
		if c.id == id {
			return c, true
		}
	}
	return fakeContainer{}, false
}

// names reports the container names the machine holds, so a test can assert that a
// refused create left it alone.
func (m *fakeMachine) names() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []string
	for _, c := range m.containers {
		out = append(out, c.name)
	}
	return out
}

func (m *fakeMachine) serve(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	switch {
	case strings.HasSuffix(path, "/_ping"):
		w.Header().Set("Api-Version", "1.47")

	case strings.HasSuffix(path, "/containers/json"):
		// The daemon hides stopped containers unless asked for all of them, which is the
		// difference the listing's All:true exists for.
		all := r.URL.Query().Get("all") == "1" || r.URL.Query().Get("all") == "true"
		m.mu.Lock()
		m.lists++
		if m.failListN != 0 && m.lists == m.failListN {
			m.mu.Unlock()
			m.log.add("list refused")
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]any{"message": "daemon busy"})
			return
		}
		list := make([]map[string]any, 0, len(m.containers))
		for _, c := range m.containers {
			if c.stopped && !all {
				continue
			}
			list = append(list, map[string]any{"Id": c.id, "Names": []string{"/" + c.name}})
		}
		m.mu.Unlock()
		_ = json.NewEncoder(w).Encode(list)

	case strings.HasSuffix(path, "/containers/create"):
		if m.createErr {
			m.log.add("create refused")
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]any{"message": "no space left on device"})
			return
		}
		var body struct {
			Image  string
			Cmd    []string
			Env    []string
			Labels map[string]string
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		name := r.URL.Query().Get("name")
		m.log.add("create " + name + " cmd=" + strings.Join(body.Cmd, " "))
		id := fmt.Sprintf("%s%058d", "dddd", len(m.containers))
		gpus := ""
		for _, kv := range body.Env {
			if k, v, ok := strings.Cut(kv, "="); ok && k == docker.GPUEnvVar {
				gpus = v
			}
		}
		_, isEngine := body.Labels[docker.EngineLabel]
		m.mu.Lock()
		m.createdEnv = body.Env
		m.containers = append(m.containers, fakeContainer{
			id: id, name: name, image: body.Image, args: body.Cmd,
			gpus: gpus, hasGPU: true, engine: isEngine,
		})
		m.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"Id": id})

	case strings.HasSuffix(path, "/start"):
		m.log.add("start")
		w.WriteHeader(http.StatusNoContent)

	case r.Method == http.MethodDelete && strings.Contains(path, "/containers/"):
		id := strings.TrimPrefix(path[strings.LastIndex(path, "/containers/"):], "/containers/")
		m.mu.Lock()
		kept := m.containers[:0]
		removed := ""
		for _, c := range m.containers {
			if c.id == id {
				removed = c.name
				continue
			}
			kept = append(kept, c)
		}
		m.containers = kept
		m.mu.Unlock()
		m.log.add("remove " + removed)
		w.WriteHeader(http.StatusNoContent)

	case strings.Contains(path, "/images/create"):
		m.log.add("pull")
		_, _ = w.Write([]byte(`{"status":"Downloaded"}`))

	case strings.HasSuffix(path, "/json"):
		// Serves both the container inspect and the image inspect after a pull; the
		// two responses do not overlap, exactly as in the upgrade fixtures.
		if id := pathID(path); id != "" && id == m.failInspect {
			if m.inspectGone {
				w.WriteHeader(http.StatusNotFound)
				_ = json.NewEncoder(w).Encode(map[string]any{"message": "No such container"})
				return
			}
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]any{"message": "daemon busy"})
			return
		}
		c, ok := m.find(pathID(path))
		if !ok {
			// An image inspect. Only the digest is read back and only after a pull.
			_ = json.NewEncoder(w).Encode(map[string]any{
				"Id":          testDigest,
				"RepoDigests": []string{engineRef},
				"Created":     "2026-01-01T00:00:00Z",
			})
			return
		}
		env := []string{"PATH=/usr/bin"}
		if c.gpus != "" {
			env = append(env, docker.GPUEnvVar+"="+c.gpus)
		}
		labels := map[string]string{}
		if c.engine {
			labels[docker.EngineLabel] = "true"
		}
		var devices []map[string]any
		if c.hasGPU {
			devices = []map[string]any{{"Driver": "nvidia", "Count": -1, "Capabilities": [][]string{{"gpu"}}}}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"Id":   c.id,
			"Name": "/" + c.name,
			"Config": map[string]any{
				"Image":  c.image,
				"Cmd":    c.args,
				"Env":    env,
				"Labels": labels,
			},
			"HostConfig": map[string]any{
				"DeviceRequests": devices,
				"Runtime":        c.runtime,
			},
			"State": map[string]any{"Status": "running"},
			"NetworkSettings": map[string]any{
				"Networks": map[string]any{"default": map[string]any{}},
			},
		})

	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

// pathID pulls the container or image id out of an inspect path.
func pathID(path string) string {
	trimmed := strings.TrimSuffix(path, "/json")
	return trimmed[strings.LastIndex(trimmed, "/")+1:]
}

// engineCtrl builds a Ctrl wired to a fake machine, with one permitted image.
func engineCtrl(t *testing.T, containers ...fakeContainer) (*Ctrl, *fakeMachine, *opLog) {
	t.Helper()
	l := &opLog{}
	dc, m := newFakeMachine(t, l, containers...)
	logger, err := log.GetLogger(&commonconfig.LoggerConfig{Format: "text", Level: "error"})
	if err != nil {
		t.Fatalf("building logger: %v", err)
	}
	return &Ctrl{
		config: config.ControllerConfig{
			RecordUpstreamSet: true,
			EngineNetwork:     "zg",
			Engines: []config.EngineImage{{
				ImageRepo:    engineRepo,
				ModelFlag:    "--model-path",
				RevisionFlag: "--revision",
				PortFlag:     "--port",
				HostFlag:     "--host",
				IPCHost:      true,
				ShmSize:      "32gb",
			}},
		},
		dockerClient: dc,
		emitter:      &fakeEmitter{log: l},
		logger:       logger,
	}, m, l
}

// okSpec is a spec that passes every check, so a test changing one field is testing
// exactly that field.
func okSpec() EngineSpec {
	return EngineSpec{
		Name:  "dsv4flash",
		Image: engineRef,
		GPUs:  "6,7",
		Port:  8000,
		Model: EngineModel{Repo: "deepseek-ai/DeepSeek-V4-Flash", Revision: testRevision},
		Args:  []string{"--tp", "2", "--mem-fraction-static", "0.85"},
	}
}

func TestCreateEngineRecordsBeforeItCreates(t *testing.T) {
	c, _, l := engineCtrl(t)

	if err := c.CreateEngine(context.Background(), okSpec()); err != nil {
		t.Fatalf("CreateEngine() = %v, want nil", err)
	}

	emit := l.indexOf("emit " + attest.EventEngineSet)
	create := l.indexOf("create dsv4flash")
	if emit == -1 || create == -1 {
		t.Fatalf("ops = %v, want both an emit and a create", l.all())
	}
	// The whole design in one assertion: RTMR3 is append-only, so a record written
	// before the container cannot be missing for a container that exists. The other
	// order can leave one serving plaintext that the ledger does not name.
	if emit > create {
		t.Errorf("ops = %v, want the record BEFORE the container", l.all())
	}
	if start := l.indexOf("start"); start < create {
		t.Errorf("ops = %v, want the container started after it is created", l.all())
	}
}

func TestCreateEngineRecordsTheWholeSetIncludingExistingEngines(t *testing.T) {
	// An engine already running with its own argument list. The record is a SNAPSHOT,
	// so this one has to reappear in it verbatim — a record that dropped its arguments
	// would describe an engine nobody configured that way.
	c, _, l := engineCtrl(t, fakeContainer{
		id: "eeee" + strings.Repeat("1", 60), name: "glm53", image: engineRef,
		args: []string{"--model-path", "zai-org/GLM-5.3", "--tp", "8"},
		gpus: "0,1,2,3,4,5", hasGPU: true, engine: true,
	})

	spec := okSpec()
	if err := c.CreateEngine(context.Background(), spec); err != nil {
		t.Fatalf("CreateEngine() = %v, want nil", err)
	}

	payload := emittedEngineSet(t, l)
	if !strings.Contains(payload, "count=2") {
		t.Errorf("payload = %q, want a set of 2", payload)
	}
	if !strings.Contains(payload, "glm53\t"+engineRef+"\t0,1,2,3,4,5\t--model-path zai-org/GLM-5.3 --tp 8") {
		t.Errorf("payload = %q, want the existing engine's own arguments preserved", payload)
	}
	if !strings.Contains(payload, "dsv4flash\t"+engineRef+"\t6,7\t") {
		t.Errorf("payload = %q, want the new engine", payload)
	}
}

func TestCreateEngineRecordsTheForcedFlags(t *testing.T) {
	c, m, l := engineCtrl(t)

	if err := c.CreateEngine(context.Background(), okSpec()); err != nil {
		t.Fatalf("CreateEngine() = %v, want nil", err)
	}

	payload := emittedEngineSet(t, l)
	for _, want := range []string{
		"--model-path deepseek-ai/DeepSeek-V4-Flash",
		"--revision " + testRevision,
		"--port 8000",
		// Forced, not defaulted: the broker reaches the engine over the compose network
		// and no port is published, so a narrower bind address would make it unreachable.
		"--host 0.0.0.0",
		// The caller's own flags, passed through — the allowlist decides what a request
		// may not say, not what it must.
		"--tp 2",
		"--mem-fraction-static 0.85",
	} {
		if !strings.Contains(payload, want) {
			t.Errorf("payload = %q, want it to carry %q", payload, want)
		}
	}

	// And the container got the same list the record describes, which is the only thing
	// that makes the record worth reading.
	cmd := l.indexOf("create dsv4flash cmd=--model-path deepseek-ai/DeepSeek-V4-Flash --revision " + testRevision + " --port 8000 --host 0.0.0.0 --tp 2 --mem-fraction-static 0.85")
	if cmd == -1 {
		t.Errorf("ops = %v, want the container created with exactly the recorded arguments", l.all())
	}
	if names := m.names(); !contains(names, "dsv4flash") {
		t.Errorf("machine = %v, want the engine on it", names)
	}
}

func TestCreateEngineRefusals(t *testing.T) {
	tests := []struct {
		name string
		spec func(*EngineSpec)
		want string
	}{{
		name: "an unpinned image",
		spec: func(s *EngineSpec) { s.Image = engineRepo + ":latest" },
		want: "must pin a digest",
	}, {
		name: "an image with no digest at all",
		spec: func(s *EngineSpec) { s.Image = engineRepo },
		want: "must pin a digest",
	}, {
		name: "an uppercase digest, which no daemon resolves",
		spec: func(s *EngineSpec) { s.Image = engineRepo + "@sha256:" + strings.Repeat("A", 64) },
		want: "must pin a digest",
	}, {
		name: "a repository no rule covers",
		spec: func(s *EngineSpec) { s.Image = "vllm/vllm-openai@" + testDigest },
		want: "is not configured under controller.engines",
	}, {
		name: "a name the record could not carry",
		spec: func(s *EngineSpec) { s.Name = "DSV4Flash" },
		want: "not a lowercase alphanumeric container name",
	}, {
		name: "no name",
		spec: func(s *EngineSpec) { s.Name = "" },
		want: "not a lowercase alphanumeric container name",
	}, {
		name: "no revision",
		spec: func(s *EngineSpec) { s.Model.Revision = "" },
		want: "must be a 40-character lowercase hex commit sha",
	}, {
		name: "a branch name instead of a revision",
		spec: func(s *EngineSpec) { s.Model.Revision = "main" },
		want: "must be a 40-character lowercase hex commit sha",
	}, {
		name: "a revision of the right length that is not hex",
		spec: func(s *EngineSpec) { s.Model.Revision = strings.Repeat("z", 40) },
		want: "must be a 40-character lowercase hex commit sha",
	}, {
		name: "no model repository",
		spec: func(s *EngineSpec) { s.Model.Repo = "" },
		want: "must name a model repository",
	}, {
		name: "no port",
		spec: func(s *EngineSpec) { s.Port = 0 },
		want: "is not a port",
	}, {
		name: "a port past the range",
		spec: func(s *EngineSpec) { s.Port = 70000 },
		want: "is not a port",
	}, {
		name: "no GPU list",
		spec: func(s *EngineSpec) { s.GPUs = "" },
		want: "must name the GPUs it may see",
	}, {
		// Every flag the controller sets is one a request may not pass, because a caller
		// that could set it could make the container disagree with the record.
		name: "the model flag passed by hand",
		spec: func(s *EngineSpec) { s.Args = []string{"--model-path", "somewhere/else"} },
		want: "--model-path is set by the controller",
	}, {
		name: "the revision flag passed by hand",
		spec: func(s *EngineSpec) { s.Args = []string{"--revision", "main"} },
		want: "--revision is set by the controller",
	}, {
		name: "the port flag passed by hand",
		spec: func(s *EngineSpec) { s.Args = []string{"--port", "9000"} },
		want: "--port is set by the controller",
	}, {
		// The one with teeth: a caller that could rebind the engine could put an
		// unauthenticated inference server somewhere the broker is not the only client.
		name: "the host flag passed by hand",
		spec: func(s *EngineSpec) { s.Args = []string{"--host", "1.2.3.4"} },
		want: "--host is set by the controller",
	}, {
		name: "a reserved flag in its equals spelling",
		spec: func(s *EngineSpec) { s.Args = []string{"--host=1.2.3.4"} },
		want: "--host is set by the controller",
	}, {
		name: "a tab in an argument, which would move the record's field boundaries",
		spec: func(s *EngineSpec) { s.Args = []string{"--chat-template\tx"} },
		want: "contains a tab or a newline",
	}, {
		name: "a newline in the model repository, which would split the record's line",
		spec: func(s *EngineSpec) { s.Model.Repo = "a\nb" },
		want: "contains a tab or a newline",
	}, {
		name: "a tab in the GPU list",
		spec: func(s *EngineSpec) { s.GPUs = "6\t7" },
		want: "contains a tab or a newline",
	}}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, m, l := engineCtrl(t)
			spec := okSpec()
			tt.spec(&spec)

			err := c.CreateEngine(context.Background(), spec)
			if err == nil {
				t.Fatalf("CreateEngine() = nil, want a refusal")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("CreateEngine() = %v, want it to mention %q", err, tt.want)
			}
			// A refusal is a 400, and the handler decides that by asking this.
			if !errors.Is(err, ErrEngineRefused) {
				t.Errorf("CreateEngine() = %v, want it to be an ErrEngineRefused", err)
			}
			// Nothing may be recorded and nothing created: a refused spec is one the
			// ledger must not carry, and a record of a container that never existed is
			// only acceptable when a create was actually attempted.
			if ops := l.all(); len(ops) != 0 {
				t.Errorf("ops = %v, want a refusal to touch nothing", ops)
			}
			if names := m.names(); contains(names, spec.Name) {
				t.Errorf("machine = %v, want the engine absent", names)
			}
		})
	}
}

func TestCreateEngineRefusesAnOccupiedCard(t *testing.T) {
	c, _, l := engineCtrl(t, fakeContainer{
		id: "eeee" + strings.Repeat("1", 60), name: "glm53", image: engineRef,
		gpus: "6,7", hasGPU: true, engine: true,
	})

	err := c.CreateEngine(context.Background(), okSpec())
	if err == nil || !strings.Contains(err.Error(), "already occupied") {
		t.Fatalf("CreateEngine() = %v, want a refusal naming the held card", err)
	}
	if ops := l.all(); len(ops) != 0 {
		t.Errorf("ops = %v, want the card checked before anything is pulled or recorded", ops)
	}
}

func TestCreateEngineRefusesACardHeldByAWholeMachineEngine(t *testing.T) {
	// The case every deployment in this project is actually in: the engine runs with
	// NVIDIA_VISIBLE_DEVICES=all, so it can use every card and no card is free.
	c, _, _ := engineCtrl(t, fakeContainer{
		id: "eeee" + strings.Repeat("1", 60), name: "glm53", image: engineRef,
		gpus: "all", hasGPU: true, engine: true,
	})

	err := c.CreateEngine(context.Background(), okSpec())
	if err == nil || !strings.Contains(err.Error(), "already occupied") {
		t.Fatalf("CreateEngine() = %v, want a refusal: an engine that can use every card holds every card", err)
	}
}

func TestCreateEngineIgnoresContainersWithNoGPUAccess(t *testing.T) {
	// The regression this test exists for: a CVM runs a dozen containers with no GPU
	// access, and NONE of them sets NVIDIA_VISIBLE_DEVICES. Reading an absent variable
	// as "all cards" would report the whole machine occupied forever and no engine could
	// ever be created.
	c, _, _ := engineCtrl(t,
		fakeContainer{id: "eeee" + strings.Repeat("1", 60), name: "broker-ingress"},
		fakeContainer{id: "ffff" + strings.Repeat("2", 60), name: "prometheus"},
		fakeContainer{id: "aaab" + strings.Repeat("3", 60), name: "0g-serving-provider-broker"},
	)

	if err := c.CreateEngine(context.Background(), okSpec()); err != nil {
		t.Fatalf("CreateEngine() = %v, want nil: containers with no GPU access hold no cards", err)
	}
}

func TestCreateEngineCountsTheOlderNvidiaRuntimeSpelling(t *testing.T) {
	// This project's own compose declares GPUs both ways — one service with a device
	// reservation, another with `runtime: nvidia`. Recognising only the first would
	// report a card free while a model sat on it.
	c, _, _ := engineCtrl(t, fakeContainer{
		id: "eeee" + strings.Repeat("1", 60), name: "dcgm-exporter",
		gpus: "6", runtime: "nvidia",
	})

	err := c.CreateEngine(context.Background(), okSpec())
	if err == nil || !strings.Contains(err.Error(), "already occupied") {
		t.Fatalf("CreateEngine() = %v, want a refusal: `runtime: nvidia` gives a container cards too", err)
	}
}

func TestCreateEngineRefusedWhenRecordingIsOff(t *testing.T) {
	c, m, l := engineCtrl(t)
	c.config.RecordUpstreamSet = false

	err := c.CreateEngine(context.Background(), okSpec())
	if err == nil || !strings.Contains(err.Error(), "recordUpstreamSet is off") {
		t.Fatalf("CreateEngine() = %v, want a refusal naming the switch", err)
	}
	// The point of the refusal: with nowhere to write the set, a created container would
	// be a destination inside the CVM that no verifier could tell from an external one.
	if names := m.names(); contains(names, "dsv4flash") {
		t.Errorf("machine = %v, want nothing created", names)
	}
	if ops := l.all(); len(ops) != 0 {
		t.Errorf("ops = %v, want nothing done", ops)
	}
}

func TestCreateEngineCreatesNothingWhenTheRecordFails(t *testing.T) {
	c, m, l := engineCtrl(t)
	c.emitter = &fakeEmitter{log: l, err: errors.New("guest agent unreachable")}

	err := c.CreateEngine(context.Background(), okSpec())
	if err == nil || !strings.Contains(err.Error(), "recording the engine set") {
		t.Fatalf("CreateEngine() = %v, want the emit failure surfaced", err)
	}
	// Not a refusal — a failure. The handler reports it as a 500, because retrying may
	// well work, where retrying a refused spec never will.
	if errors.Is(err, ErrEngineRefused) {
		t.Errorf("CreateEngine() = %v, want a failure rather than a refusal", err)
	}
	if names := m.names(); contains(names, "dsv4flash") {
		t.Errorf("machine = %v, want no container when the ledger could not be written", names)
	}
	if i := l.indexOf("create dsv4flash"); i != -1 {
		t.Errorf("ops = %v, want no create", l.all())
	}
}

func TestCreateEngineCorrectsTheRecordWhenTheCreateFails(t *testing.T) {
	c, m, l := engineCtrl(t)
	m.createErr = true

	if err := c.CreateEngine(context.Background(), okSpec()); err == nil {
		t.Fatalf("CreateEngine() = nil, want the create failure surfaced")
	}

	// Two records: the one naming the engine, and the correction that does not. The
	// intervening state over-states the set, which is the harmless direction — a
	// destination named that does not exist, rather than one that exists and is not.
	var emits []string
	for _, op := range l.all() {
		if strings.HasPrefix(op, "emit "+attest.EventEngineSet) {
			emits = append(emits, op)
		}
	}
	if len(emits) != 2 {
		t.Fatalf("emits = %v, want the record and its correction", emits)
	}
	if !strings.Contains(emits[0], "dsv4flash") {
		t.Errorf("first emit = %q, want it to name the engine", emits[0])
	}
	if strings.Contains(emits[1], "dsv4flash") || !strings.Contains(emits[1], "count=0") {
		t.Errorf("correction = %q, want the set without the engine that was never created", emits[1])
	}
	if names := m.names(); contains(names, "dsv4flash") {
		t.Errorf("machine = %v, want nothing created", names)
	}
}

func TestCreateEngineRefusesWhileAnotherChangeRuns(t *testing.T) {
	c, _, _ := engineCtrl(t)
	c.changing.Lock()
	defer c.changing.Unlock()

	// 409, not 500: nothing was touched and retrying once the other change finishes is
	// the right move.
	if err := c.CreateEngine(context.Background(), okSpec()); !errors.Is(err, ErrChangeInProgress) {
		t.Errorf("CreateEngine() = %v, want ErrChangeInProgress", err)
	}
}

func TestRemoveEngineRemovesThenRecords(t *testing.T) {
	c, m, l := engineCtrl(t, fakeContainer{
		id: "eeee" + strings.Repeat("1", 60), name: "whisper", image: engineRef,
		gpus: "7", hasGPU: true, engine: true,
	})

	if err := c.RemoveEngine(context.Background(), "whisper"); err != nil {
		t.Fatalf("RemoveEngine() = %v, want nil", err)
	}

	remove := l.indexOf("remove whisper")
	emit := l.indexOf("emit " + attest.EventEngineSet)
	if remove == -1 || emit == -1 {
		t.Fatalf("ops = %v, want a remove and an emit", l.all())
	}
	// The reverse of the create order, for the same invariant: the ledger must never
	// UNDERSTATE where plaintext may go. Recording first would leave a running container
	// absent from the ledger; this way the intervening record merely names one that is
	// already gone.
	if emit < remove {
		t.Errorf("ops = %v, want the container removed BEFORE the record is corrected", l.all())
	}
	if payload := emittedEngineSet(t, l); !strings.Contains(payload, "count=0") {
		t.Errorf("payload = %q, want the empty set", payload)
	}
	if names := m.names(); contains(names, "whisper") {
		t.Errorf("machine = %v, want the engine gone", names)
	}
}

func TestRemoveEngineRefusesAContainerItDidNotCreate(t *testing.T) {
	// The compose's own services are not the controller's to remove: a reboot brings
	// them back anyway, and an endpoint that could delete them would be a general
	// "delete any container" API wearing an engine's name.
	//
	// The refusal comes from the resolution now, not from the removal — the label is what
	// makes a container resolvable as an engine at all, so a compose service is refused
	// before anything touches it. The docker layer keeps its own label guard for a second
	// caller that would inherit none of this; TestRemoveEngineRefusesAnUnlabelledContainer
	// in the docker package covers that one.
	c, m, l := engineCtrl(t, fakeContainer{
		id: "eeee" + strings.Repeat("1", 60), name: "glm53-engine", image: engineRef,
		gpus: "all", hasGPU: true, // no label
	})

	err := c.RemoveEngine(context.Background(), "glm53-engine")
	if err == nil || !strings.Contains(err.Error(), "no engine named") {
		t.Fatalf("RemoveEngine() = %v, want a refusal", err)
	}
	if names := m.names(); !contains(names, "glm53-engine") {
		t.Errorf("machine = %v, want the container still there", names)
	}
	if i := l.indexOf("emit"); i != -1 {
		t.Errorf("ops = %v, want no record for a removal that did not happen", l.all())
	}
}

func TestRemoveEngineRefusedWhenRecordingIsOff(t *testing.T) {
	c, m, _ := engineCtrl(t, fakeContainer{
		id: "eeee" + strings.Repeat("1", 60), name: "whisper", image: engineRef,
		gpus: "7", hasGPU: true, engine: true,
	})
	c.config.RecordUpstreamSet = false

	err := c.RemoveEngine(context.Background(), "whisper")
	if err == nil || !strings.Contains(err.Error(), "recordUpstreamSet is off") {
		t.Fatalf("RemoveEngine() = %v, want a refusal naming the switch", err)
	}
	// Refused rather than allowed: a removal whose correction cannot be recorded leaves
	// the ledger naming a container that is gone, with no way to say so.
	if names := m.names(); !contains(names, "whisper") {
		t.Errorf("machine = %v, want the engine untouched", names)
	}
}

func TestListEnginesReportsOnlyWhatThisControllerCreated(t *testing.T) {
	c, _, _ := engineCtrl(t,
		fakeContainer{id: "eeee" + strings.Repeat("1", 60), name: "whisper", image: engineRef, gpus: "7", hasGPU: true, engine: true},
		fakeContainer{id: "ffff" + strings.Repeat("2", 60), name: "glm53-engine", image: engineRef, gpus: "all", hasGPU: true},
		fakeContainer{id: "aaab" + strings.Repeat("3", 60), name: "prometheus"},
	)

	got, err := c.ListEngines(context.Background())
	if err != nil {
		t.Fatalf("ListEngines() = %v", err)
	}
	if len(got) != 1 || got[0].Name != "whisper" {
		t.Fatalf("ListEngines() = %+v, want only the labelled engine", got)
	}
	if got[0].Image != engineRef || got[0].GPUs != "7" {
		t.Errorf("ListEngines()[0] = %+v, want the image and cards it runs on", got[0])
	}
}

func TestGPUAllocationSeparatesNoCardsFromEveryCard(t *testing.T) {
	c, _, _ := engineCtrl(t,
		fakeContainer{id: "eeee" + strings.Repeat("1", 60), name: "whisper", gpus: "7", hasGPU: true, engine: true},
		fakeContainer{id: "ffff" + strings.Repeat("2", 60), name: "glm53-engine", gpus: "all", hasGPU: true},
		fakeContainer{id: "aaab" + strings.Repeat("3", 60), name: "prometheus"},
	)

	alloc, err := c.GPUAllocation(context.Background())
	if err != nil {
		t.Fatalf("GPUAllocation() = %v", err)
	}
	if len(alloc["7"]) != 1 || alloc["7"][0].HeldBy != "whisper" || !alloc["7"][0].ByEngine {
		t.Errorf("alloc[7] = %+v, want the engine holding it", alloc["7"])
	}
	if len(alloc[docker.GPUAll]) != 1 || alloc[docker.GPUAll][0].HeldBy != "glm53-engine" {
		t.Errorf("alloc[all] = %+v, want the whole-machine engine", alloc[docker.GPUAll])
	}
	// The distinction the HasGPU field exists for: no GPU access is not the same claim
	// as an unnarrowed one.
	for gpu, claims := range alloc {
		for _, cl := range claims {
			if cl.HeldBy == "prometheus" {
				t.Errorf("alloc[%s] = %+v, want a container with no GPU access to hold nothing", gpu, claims)
			}
		}
	}
	// And the controller's own container, which has no cards either.
	if len(alloc) != 2 {
		t.Errorf("alloc = %+v, want exactly the two cards that are claimed", alloc)
	}
}

func TestEngineImageRuleRefusesAnUnusableConfigEntry(t *testing.T) {
	// Checked when the rule is USED rather than at config load, because this struct is
	// shared with the broker and event binaries: a malformed entry must not be able to
	// keep a deployment that creates no engines from booting.
	tests := []struct {
		name string
		img  config.EngineImage
		want string
	}{
		{"no model flag", config.EngineImage{ImageRepo: engineRepo, RevisionFlag: "--revision", PortFlag: "--port"}, "sets no modelFlag"},
		{"no revision flag", config.EngineImage{ImageRepo: engineRepo, ModelFlag: "--model-path", PortFlag: "--port"}, "sets no revisionFlag"},
		{"no port flag", config.EngineImage{ImageRepo: engineRepo, ModelFlag: "--model-path", RevisionFlag: "--revision"}, "sets no portFlag"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, _, _ := engineCtrl(t)
			c.config.Engines = []config.EngineImage{tt.img}

			err := c.CreateEngine(context.Background(), okSpec())
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("CreateEngine() = %v, want a refusal naming the missing key", err)
			}
		})
	}
}

func TestEngineImageRuleWithoutAHostFlagForcesNothing(t *testing.T) {
	// An image that has no bind-address flag: the controller must not invent one, and a
	// request may pass anything that is not among the three it does set.
	c, _, l := engineCtrl(t)
	c.config.Engines[0].HostFlag = ""

	spec := okSpec()
	spec.Args = []string{"--host", "1.2.3.4"}
	if err := c.CreateEngine(context.Background(), spec); err != nil {
		t.Fatalf("CreateEngine() = %v, want nil", err)
	}
	payload := emittedEngineSet(t, l)
	if strings.Count(payload, "--host") != 1 {
		t.Errorf("payload = %q, want exactly the caller's --host and none forced", payload)
	}
}

// emittedEngineSet returns the payload of the last recorded engine set.
func emittedEngineSet(t *testing.T, l *opLog) string {
	t.Helper()
	prefix := "emit " + attest.EventEngineSet + " "
	for i := len(l.all()) - 1; i >= 0; i-- {
		if op := l.all()[i]; strings.HasPrefix(op, prefix) {
			return strings.TrimPrefix(op, prefix)
		}
	}
	t.Fatalf("ops = %v, want an engine-set record", l.all())
	return ""
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

func TestCreateEngineRefusesACardHeldByAStoppedEngine(t *testing.T) {
	// A stopped container still holds its name and its configuration, and the restart
	// policy means the next daemon restart brings it back onto the same cards. Placing a
	// second engine there would work until the reboot and then have two models fighting
	// over one card's memory.
	c, _, l := engineCtrl(t, fakeContainer{
		id: "eeee" + strings.Repeat("1", 60), name: "whisper", image: engineRef,
		gpus: "6,7", hasGPU: true, engine: true, stopped: true,
	})

	err := c.CreateEngine(context.Background(), okSpec())
	if err == nil || !strings.Contains(err.Error(), "already occupied") {
		t.Fatalf("CreateEngine() = %v, want a refusal: a stopped engine still holds its cards", err)
	}
	if ops := l.all(); len(ops) != 0 {
		t.Errorf("ops = %v, want nothing done", ops)
	}
}

func TestCreateEngineRecordsAStoppedEngineToo(t *testing.T) {
	// And the record is a snapshot of what the CVM CAN run, not of what is running this
	// second: a stopped engine left out of it would be a destination the ledger stopped
	// naming while the container was still there to be started again.
	c, _, l := engineCtrl(t, fakeContainer{
		id: "eeee" + strings.Repeat("1", 60), name: "whisper", image: engineRef,
		args: []string{"--model-path", "openai/whisper-large-v3"},
		gpus: "5", hasGPU: true, engine: true, stopped: true,
	})

	if err := c.CreateEngine(context.Background(), okSpec()); err != nil {
		t.Fatalf("CreateEngine() = %v, want nil", err)
	}
	if payload := emittedEngineSet(t, l); !strings.Contains(payload, "whisper\t") {
		t.Errorf("payload = %q, want the stopped engine in the set", payload)
	}
}

func TestCreateEngineStillRecordsWhenTheSnapshotCannotBeTaken(t *testing.T) {
	// A listing failure while the snapshot is being built produces a SHORTER set — the
	// wrong direction, since a set missing an engine understates where plaintext may go.
	// It is preferred to failing the whole change, because the caller is mid-change and
	// the alternative is a ledger left describing the state before it. Asserted so the
	// choice is a decision rather than an accident.
	c, m, l := engineCtrl(t, fakeContainer{
		id: "eeee" + strings.Repeat("1", 60), name: "whisper", image: engineRef,
		gpus: "5", hasGPU: true, engine: true,
	})
	// The second listing is the snapshot's: the first is the GPU check's, and the ones
	// after belong to starting the container. Failing only that one puts the failure
	// where the fail-open decision lives.
	m.failListN = 2

	if err := c.CreateEngine(context.Background(), okSpec()); err != nil {
		t.Fatalf("CreateEngine() = %v, want the change to go through", err)
	}
	payload := emittedEngineSet(t, l)
	if !strings.Contains(payload, "dsv4flash\t") {
		t.Errorf("payload = %q, want the engine being created", payload)
	}
	if strings.Contains(payload, "whisper\t") {
		t.Errorf("payload = %q, want the fixture to have actually failed the snapshot listing", payload)
	}
}

func TestRemoveEngineRefusesWhileAnotherChangeRuns(t *testing.T) {
	// The removal takes the same lock the create and the upgrade do, because RTMR3 is a
	// ledger: two changes interleaving would write records whose order does not describe
	// the order things happened in.
	c, _, _ := engineCtrl(t, fakeContainer{
		id: "eeee" + strings.Repeat("1", 60), name: "whisper", image: engineRef,
		gpus: "7", hasGPU: true, engine: true,
	})
	c.changing.Lock()
	defer c.changing.Unlock()

	if err := c.RemoveEngine(context.Background(), "whisper"); !errors.Is(err, ErrChangeInProgress) {
		t.Errorf("RemoveEngine() = %v, want ErrChangeInProgress", err)
	}
}

func TestCreateEnginePullsBeforeItRecords(t *testing.T) {
	// A record naming an image that cannot be fetched is a record of something that will
	// never exist, and the pull is the long step — so it goes first, where a failure
	// costs nothing but time.
	c, _, l := engineCtrl(t)

	if err := c.CreateEngine(context.Background(), okSpec()); err != nil {
		t.Fatalf("CreateEngine() = %v, want nil", err)
	}
	pull := l.indexOf("pull")
	emit := l.indexOf("emit " + attest.EventEngineSet)
	if pull == -1 {
		t.Fatalf("ops = %v, want the image pulled", l.all())
	}
	if pull > emit {
		t.Errorf("ops = %v, want the pull before the record", l.all())
	}
}

func TestCreateEngineRefusesWhenTheSetCannotBeRendered(t *testing.T) {
	// An engine already on the machine whose image is a TAG — created by an older
	// controller, or labelled by hand. The reader refuses an unpinned image, so the whole
	// set is unrenderable, and this create must fail rather than emit a payload
	// ResolveRunningState would reject.
	//
	// Fail-closed on purpose, and the cost is real: one unrenderable engine blocks every
	// later create until it is removed. The alternative is worse — an unreadable zg-
	// record makes the CVM unverifiable for EVERY question, not just the engine set.
	c, m, l := engineCtrl(t, fakeContainer{
		id: "eeee" + strings.Repeat("1", 60), name: "legacy", image: engineRepo + ":v0.5.18",
		gpus: "5", hasGPU: true, engine: true,
	})

	err := c.CreateEngine(context.Background(), okSpec())
	if err == nil || !strings.Contains(err.Error(), "cannot be recorded") {
		t.Fatalf("CreateEngine() = %v, want the render refusal surfaced", err)
	}
	if i := l.indexOf("emit"); i != -1 {
		t.Errorf("ops = %v, want nothing emitted when the set cannot be rendered", l.all())
	}
	if names := m.names(); contains(names, "dsv4flash") {
		t.Errorf("machine = %v, want nothing created", names)
	}
}

func TestListEnginesIsOrdered(t *testing.T) {
	// Sorted by name, so the answer does not depend on the order the daemon happened to
	// list them in.
	c, _, _ := engineCtrl(t,
		fakeContainer{id: "eeee" + strings.Repeat("1", 60), name: "zulu", image: engineRef, gpus: "7", hasGPU: true, engine: true},
		fakeContainer{id: "ffff" + strings.Repeat("2", 60), name: "alpha", image: engineRef, gpus: "6", hasGPU: true, engine: true},
	)

	got, err := c.ListEngines(context.Background())
	if err != nil {
		t.Fatalf("ListEngines() = %v", err)
	}
	if len(got) != 2 || got[0].Name != "alpha" || got[1].Name != "zulu" {
		t.Errorf("ListEngines() = %+v, want them ordered by name", got)
	}
}

func TestCreateEngineRefusesWhenAContainerCannotBeInspected(t *testing.T) {
	// An inspect that fails for any reason but "it is gone" is fatal, because a skipped
	// container is one whose claim goes unreported — and under-reporting is the unsafe
	// direction twice over: a card that looks free gets a second model on it, and an
	// engine left out of the snapshot is a destination the record stops naming. A caller
	// cannot tell a short answer from a small machine.
	c, m, l := engineCtrl(t, fakeContainer{
		id: "eeee" + strings.Repeat("1", 60), name: "glm53", image: engineRef,
		gpus: "all", hasGPU: true, engine: true,
	})
	m.failInspect = "eeee" + strings.Repeat("1", 60)

	err := c.CreateEngine(context.Background(), okSpec())
	if err == nil || !strings.Contains(err.Error(), "inspecting") {
		t.Fatalf("CreateEngine() = %v, want the inspect failure surfaced", err)
	}
	if ops := l.all(); len(ops) != 0 {
		t.Errorf("ops = %v, want nothing done", ops)
	}
	if names := m.names(); contains(names, "dsv4flash") {
		t.Errorf("machine = %v, want nothing created", names)
	}
}

func TestCreateEngineToleratesAContainerThatVanished(t *testing.T) {
	// The one failure that IS benign: a container removed between the listing and the
	// inspect. Refusing on it would make every engine operation lose to an operator
	// running docker rm at the wrong moment.
	c, m, _ := engineCtrl(t, fakeContainer{
		id: "eeee" + strings.Repeat("1", 60), name: "gone", image: engineRef,
		gpus: "5", hasGPU: true, engine: true,
	})
	m.failInspect, m.inspectGone = "eeee"+strings.Repeat("1", 60), true

	if err := c.CreateEngine(context.Background(), okSpec()); err != nil {
		t.Fatalf("CreateEngine() = %v, want the change to go through", err)
	}
}

func TestCreateEngineBoundsTheChangeAndSurvivesTheCallerHangingUp(t *testing.T) {
	// Two properties of one line. The deadline bounds a change that holds the lock every
	// other change needs, and an image pull has no deadline of its own. Detaching from
	// the caller's context means a client that hung up mid-change does not abort it
	// between the record and the container — which is the one window that leaves the
	// ledger describing something that was never created.
	c, _, l := engineCtrl(t)
	var deadline time.Time
	var hasDeadline bool
	c.emitter = &deadlineEmitter{log: l, deadline: &deadline, ok: &hasDeadline}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // the caller hangs up before anything happens

	if err := c.CreateEngine(ctx, okSpec()); err != nil {
		t.Fatalf("CreateEngine() = %v, want a cancelled caller not to abort the change", err)
	}
	if !hasDeadline {
		t.Fatal("the emit ran with no deadline; a change holding the lock must be bounded")
	}
	if d := time.Until(deadline); d <= 0 || d > engineChangeTimeout {
		t.Errorf("deadline is %v away, want within %v", d, engineChangeTimeout)
	}
}

// deadlineEmitter records the deadline the change ran under.
type deadlineEmitter struct {
	log      *opLog
	deadline *time.Time
	ok       *bool
}

func (e *deadlineEmitter) EmitEvent(ctx context.Context, event string, payload []byte) error {
	*e.deadline, *e.ok = func() (time.Time, bool) { return ctx.Deadline() }()
	e.log.add("emit " + event + " " + string(payload))
	return nil
}

func TestRemoveEngineSurvivesTheCallerHangingUp(t *testing.T) {
	// The same detachment the create needs, and here the window is worse: the container
	// is already gone when the correction is written, so a context cancelled in between
	// leaves the ledger naming an engine that no longer exists with nothing to say so.
	c, m, l := engineCtrl(t, fakeContainer{
		id: "eeee" + strings.Repeat("1", 60), name: "whisper", image: engineRef,
		gpus: "7", hasGPU: true, engine: true,
	})
	var deadline time.Time
	var hasDeadline bool
	c.emitter = &deadlineEmitter{log: l, deadline: &deadline, ok: &hasDeadline}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := c.RemoveEngine(ctx, "whisper"); err != nil {
		t.Fatalf("RemoveEngine() = %v, want a cancelled caller not to abort the change", err)
	}
	if !hasDeadline {
		t.Fatal("the correction ran with no deadline; a change holding the lock must be bounded")
	}
	if d := time.Until(deadline); d <= 0 || d > engineChangeTimeout {
		t.Errorf("deadline is %v away, want within %v", d, engineChangeTimeout)
	}
	if names := m.names(); contains(names, "whisper") {
		t.Errorf("machine = %v, want the engine gone", names)
	}
}

func TestCreateEnginePassesTheTokenToTheContainerAndNotToTheLedger(t *testing.T) {
	// HF_TOKEN comes from the controller's OWN environment, never from a request, and
	// this is the test that says why: the whole spec goes into a record RTMR3 serves to
	// anyone who asks. A request field that could carry the token would publish it.
	t.Setenv("HF_TOKEN", "hf_secretvalue")

	c, m, l := engineCtrl(t)
	c.config.EngineEnv = map[string]string{"HF_HOME": "/root/.cache/huggingface"}

	if err := c.CreateEngine(context.Background(), okSpec()); err != nil {
		t.Fatalf("CreateEngine() = %v, want nil", err)
	}

	if !contains(m.createdEnv, "HF_TOKEN=hf_secretvalue") {
		t.Errorf("env = %v, want the token the controller holds", m.createdEnv)
	}
	if !contains(m.createdEnv, "HF_HOME=/root/.cache/huggingface") {
		t.Errorf("env = %v, want the configured variables", m.createdEnv)
	}
	// The record describes the image, the cards and the arguments — never the
	// environment, because that is where the one secret is.
	if payload := emittedEngineSet(t, l); strings.Contains(payload, "hf_secretvalue") {
		t.Errorf("payload = %q, want no trace of the token in a record RTMR3 publishes", payload)
	}
}

func TestRemoveEngineNeedsTheExactName(t *testing.T) {
	// The docker layer falls back to substring matching when nothing matches exactly —
	// the upgrade path needs it, because compose prefixes its containers with the project
	// name. On a destructive endpoint that fallback turns "no engine called that" into
	// "removed a different engine whose name contains it", and the outcome is a model
	// going offline.
	c, m, l := engineCtrl(t,
		fakeContainer{id: "eeee" + strings.Repeat("1", 60), name: "whisper", image: engineRef, gpus: "7", hasGPU: true, engine: true},
		fakeContainer{id: "ffff" + strings.Repeat("2", 60), name: "glm53", image: engineRef, gpus: "0", hasGPU: true, engine: true},
	)

	for _, name := range []string{"isper", "whisp", "", "glm"} {
		t.Run("refuses "+name, func(t *testing.T) {
			err := c.RemoveEngine(context.Background(), name)
			if err == nil || !strings.Contains(err.Error(), "no engine named") {
				t.Fatalf("RemoveEngine(%q) = %v, want a refusal naming nothing removed", name, err)
			}
			if !errors.Is(err, ErrEngineRefused) {
				t.Errorf("RemoveEngine(%q) = %v, want a 404-shaped refusal rather than a server error", name, err)
			}
		})
	}

	if names := m.names(); !contains(names, "whisper") || !contains(names, "glm53") {
		t.Errorf("machine = %v, want both engines untouched", names)
	}
	if ops := l.all(); len(ops) != 0 {
		t.Errorf("ops = %v, want nothing recorded for a removal that did not happen", ops)
	}

	// And the exact name still works, so the refusal is about resolution and not about
	// having broken the endpoint.
	if err := c.RemoveEngine(context.Background(), "whisper"); err != nil {
		t.Fatalf("RemoveEngine(\"whisper\") = %v, want nil", err)
	}
}

// The regression that made this API refuse every request on every deployment it exists
// for. dcgm-exporter runs with `runtime: nvidia` and NVIDIA_VISIBLE_DEVICES=all on all of
// them: it sees every card and allocates on none, which is what a metrics exporter is.
// Read as occupancy it holds the whole machine forever.
func TestCreateEngineIgnoresAMonitoringContainersClaim(t *testing.T) {
	c, _, _ := engineCtrl(t, fakeContainer{
		id: "eeee" + strings.Repeat("1", 60), name: "dcgm-exporter",
		gpus: "all", runtime: "nvidia",
	})

	// Without the declaration the refusal stands, which is the fail-closed default: a
	// container that can use every card is treated as using them.
	err := c.CreateEngine(context.Background(), okSpec())
	if err == nil || !strings.Contains(err.Error(), "already occupied") {
		t.Fatalf("CreateEngine() = %v, want a refusal before the exporter is declared", err)
	}
	if !strings.Contains(err.Error(), "engineGPUIgnore") {
		t.Errorf("CreateEngine() = %v, want the refusal to name the way out", err)
	}

	c.config.EngineGPUIgnore = []string{"dcgm-exporter"}
	if err := c.CreateEngine(context.Background(), okSpec()); err != nil {
		t.Fatalf("CreateEngine() = %v, want nil once the exporter is declared non-occupying", err)
	}
}

func TestEngineGPUIgnoreNeedsTheExactContainerName(t *testing.T) {
	// A prefix that matched would silently exempt a container that does occupy, which is
	// the same reasoning the removal's exact resolution rests on.
	c, _, _ := engineCtrl(t, fakeContainer{
		id: "eeee" + strings.Repeat("1", 60), name: "dcgm-exporter",
		gpus: "all", runtime: "nvidia",
	})
	c.config.EngineGPUIgnore = []string{"dcgm"}

	if err := c.CreateEngine(context.Background(), okSpec()); err == nil {
		t.Fatal("CreateEngine() = nil, want a refusal: the declaration named no container")
	}
}

func TestEngineGPUIgnoreDoesNotExemptARealEngine(t *testing.T) {
	// The declaration is the operator's claim, and nothing here can check it — docker says
	// which cards a container may see, never whether it allocated on them. What this test
	// pins is the shape: a declared container stops blocking, an undeclared one does not,
	// so a claim covering one container cannot quietly cover another.
	c, _, _ := engineCtrl(t,
		fakeContainer{id: "eeee" + strings.Repeat("1", 60), name: "dcgm-exporter", gpus: "all", runtime: "nvidia"},
		fakeContainer{id: "ffff" + strings.Repeat("2", 60), name: "glm53", image: engineRef, gpus: "6,7", hasGPU: true, engine: true},
	)
	c.config.EngineGPUIgnore = []string{"dcgm-exporter"}

	err := c.CreateEngine(context.Background(), okSpec())
	if err == nil || !strings.Contains(err.Error(), "already occupied") {
		t.Fatalf("CreateEngine() = %v, want the real engine still to occupy cards 6 and 7", err)
	}
}

func TestGPUAllocationStillReportsAMonitoringClaim(t *testing.T) {
	// The report says what docker says; only the placement DECISION uses the operator's
	// declaration. Dropping the claim from the report would hide a container that really
	// can see the card, which is the fact an operator diagnosing the machine needs.
	c, _, _ := engineCtrl(t, fakeContainer{
		id: "eeee" + strings.Repeat("1", 60), name: "dcgm-exporter",
		gpus: "all", runtime: "nvidia",
	})
	c.config.EngineGPUIgnore = []string{"dcgm-exporter"}

	alloc, err := c.GPUAllocation(context.Background())
	if err != nil {
		t.Fatalf("GPUAllocation() = %v", err)
	}
	claims := alloc[docker.GPUAll]
	if len(claims) != 1 || claims[0].HeldBy != "dcgm-exporter" {
		t.Fatalf("alloc[all] = %+v, want the exporter still reported", claims)
	}
	if !claims[0].Monitoring {
		t.Error("the exporter's claim is not flagged as monitoring, so a reader cannot tell why it does not block")
	}
}
