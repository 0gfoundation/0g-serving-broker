package ctrl

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/0glabs/0g-serving-broker/common/attest"
	"github.com/0glabs/0g-serving-broker/controller/internal/attestproxy"
	"github.com/0glabs/0g-serving-broker/controller/internal/docker"
	"github.com/0glabs/0g-serving-broker/inference/config"
)

const layaInstances = `[{"name":"laya","broker":"laya-broker","event":"laya-event","ingress":"laya-ingress","configFile":"/etc/config-laya/config.yaml"}]`

func TestParseInstances(t *testing.T) {
	got, err := parseInstances(layaInstances, "/etc/config/config.yaml", false)
	if err != nil {
		t.Fatalf("parseInstances() = %v", err)
	}
	want := Instance{Name: "laya", Broker: "laya-broker", Event: "laya-event", Ingress: "laya-ingress", ConfigFile: "/etc/config-laya/config.yaml"}
	if len(got) != 1 || got[0] != want {
		t.Fatalf("parseInstances() = %+v, want [%+v]", got, want)
	}

	if got, err := parseInstances("  ", "/etc/config/config.yaml", true); err != nil || got != nil {
		t.Errorf("parseInstances(blank) = %v, %v; want none and no error", got, err)
	}

	one := func(fields string) string { return "[{" + fields + "}]" }
	ok := `"broker":"b1","event":"e1","configFile":"/c1/config.yaml"`
	refused := map[string]struct {
		raw               string
		recordUpstreamSet bool
	}{
		"not json":                             {raw: "laya"},
		"unknown field":                        {raw: one(`"name":"x",` + ok + `,"image":"evil"`)},
		"upper-case name":                      {raw: one(`"name":"Laya",` + ok)},
		"name with a space":                    {raw: one(`"name":"la ya",` + ok)},
		"no broker":                            {raw: one(`"name":"x","event":"e1","configFile":"/c1/config.yaml"`)},
		"broker is the primary's":              {raw: one(`"name":"x","broker":"0g-serving-provider-broker","event":"e1","configFile":"/c1/config.yaml"`)},
		"ingress is the primary's":             {raw: one(`"name":"x",` + ok + `,"ingress":"broker-ingress"`)},
		"broker and event are one":             {raw: one(`"name":"x","broker":"b1","event":"b1","configFile":"/c1/config.yaml"`)},
		"relative config":                      {raw: one(`"name":"x","broker":"b1","event":"e1","configFile":"c1/config.yaml"`)},
		"unclean config":                       {raw: one(`"name":"x","broker":"b1","event":"e1","configFile":"/c1/../config/config.yaml"`)},
		"config is the primary's":              {raw: one(`"name":"x","broker":"b1","event":"e1","configFile":"/etc/config/config.yaml"`)},
		"name declared twice":                  {raw: "[{" + `"name":"x",` + ok + "},{" + `"name":"x","broker":"b2","event":"e2","configFile":"/c2/config.yaml"` + "}]"},
		"container shared by two":              {raw: "[{" + `"name":"x",` + ok + "},{" + `"name":"y","broker":"b1","event":"e2","configFile":"/c2/config.yaml"` + "}]"},
		"config shared by two":                 {raw: "[{" + `"name":"x",` + ok + "},{" + `"name":"y","broker":"b2","event":"e2","configFile":"/c1/config.yaml"` + "}]"},
		"alongside a recorded upstream set":    {raw: one(`"name":"x",` + ok), recordUpstreamSet: true},
		"contains the primary broker's name":   {raw: one(`"name":"x","broker":"0g-serving-provider-broker-x","event":"e1","configFile":"/c1/config.yaml"`)},
		"one instance's name inside another's": {raw: "[{" + `"name":"x",` + ok + "},{" + `"name":"y","broker":"b1x","event":"e2","configFile":"/c2/config.yaml"` + "}]"},
	}
	for name, tc := range refused {
		t.Run(name, func(t *testing.T) {
			if got, err := parseInstances(tc.raw, "/etc/config/config.yaml", tc.recordUpstreamSet); err == nil {
				t.Errorf("parseInstances(%s) = %+v, want an error", tc.raw, got)
			}
		})
	}
}

func TestInstanceAliasesResolve(t *testing.T) {
	c := &Ctrl{}
	var err error
	if c.instances, err = parseInstances(layaInstances, "/etc/config/config.yaml", false); err != nil {
		t.Fatal(err)
	}
	for alias, want := range map[string]string{
		"laya-broker":  "laya-broker",
		"laya-event":   "laya-event",
		"laya-ingress": "laya-ingress",
		"broker":       containerBroker,
		"laya":         "",
		"other-broker": "",
	} {
		if got := c.getContainerName(alias); got != want {
			t.Errorf("getContainerName(%q) = %q, want %q", alias, got, want)
		}
	}
	for _, alias := range c.GetAllManagedContainerAliases() {
		if c.getContainerName(alias) == "" {
			t.Errorf("alias %q is listed but resolves to nothing", alias)
		}
	}
}

// daemonFaults shapes fakeFaultyDaemon, a daemon holding the primary pair, the
// controller itself and one extra instance, logging every write by container NAME:
// creates or stops that fail by name, containers absent from the list, neighbours
// added to it (whose names contain an instance's), and per-container images (default
// prevRef), states (default running) and health (default none).
//
// The instance tests fail the primary event's create, the same stopping point the
// single-instance tests use: past everything asserted and short of the contract sync,
// which would need a chain.
type daemonFaults struct {
	failCreate map[string]bool
	failStop   map[string]bool
	missing    map[string]bool
	extra      []string
	image      map[string]string
	state      map[string]string
	health     map[string]string
}

func fakeInstanceDaemon(t *testing.T, l *opLog, failCreate map[string]bool) *docker.Client {
	t.Helper()
	return fakeFaultyDaemon(t, l, daemonFaults{failCreate: failCreate})
}

func fakeFaultyDaemon(t *testing.T, l *opLog, f daemonFaults) *docker.Client {
	t.Helper()
	failCreate := f.failCreate

	names := map[string]string{
		brokerID:                         containerBroker,
		eventID:                          containerEvent,
		selfID:                           "0g-controller",
		"dddd" + strings.Repeat("4", 60): "laya-broker",
		"eeee" + strings.Repeat("5", 60): "laya-event",
		"ffff" + strings.Repeat("6", 60): "laya-ingress",
	}
	for i, n := range f.extra {
		names[fmt.Sprintf("%04d", i)+strings.Repeat("7", 60)] = n
	}
	nameOf := func(path, suffix string) string {
		parts := strings.Split(strings.TrimSuffix(path, suffix), "/")
		if n, ok := names[parts[len(parts)-1]]; ok {
			return n
		}
		return parts[len(parts)-1]
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/_ping"):
			w.Header().Set("Api-Version", "1.47")
		case strings.Contains(r.URL.Path, "/images/create"):
			l.add("pull")
			_, _ = w.Write([]byte(okPull))
		case strings.HasSuffix(r.URL.Path, "/containers/json"):
			var list []map[string]any
			for id, n := range names {
				if !f.missing[n] {
					list = append(list, map[string]any{"Id": id, "Names": []string{"/" + n}})
				}
			}
			_ = json.NewEncoder(w).Encode(list)
		case strings.HasSuffix(r.URL.Path, "/containers/create"):
			n := r.URL.Query().Get("name")
			if failCreate[n] {
				l.add("create " + n + " refused")
				w.WriteHeader(http.StatusInternalServerError)
				_ = json.NewEncoder(w).Encode(map[string]any{"message": "no space left on device"})
				return
			}
			l.add("create " + n)
			_ = json.NewEncoder(w).Encode(map[string]any{"Id": "abcd" + strings.Repeat("0", 60)})
		case strings.HasSuffix(r.URL.Path, "/stop"):
			n := nameOf(r.URL.Path, "/stop")
			if f.failStop[n] {
				l.add("stop " + n + " refused")
				w.WriteHeader(http.StatusInternalServerError)
				_ = json.NewEncoder(w).Encode(map[string]any{"message": "device busy"})
				return
			}
			l.add("stop " + n)
			w.WriteHeader(http.StatusNoContent)
		case strings.HasSuffix(r.URL.Path, "/restart"):
			l.add("restart " + nameOf(r.URL.Path, "/restart"))
			w.WriteHeader(http.StatusNoContent)
		case strings.HasSuffix(r.URL.Path, "/start"):
			l.add("start " + nameOf(r.URL.Path, "/start"))
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodDelete:
			l.add("remove " + nameOf(r.URL.Path, ""))
			w.WriteHeader(http.StatusNoContent)
		case strings.HasSuffix(r.URL.Path, "/json"):
			n := nameOf(r.URL.Path, "/json")
			img, state := prevRef, "running"
			if v, ok := f.image[n]; ok {
				img = v
			}
			if v, ok := f.state[n]; ok {
				state = v
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"Id":          brokerID,
				"Name":        "/" + n,
				"RepoDigests": []string{imageRepo + "@" + testDigest},
				"Created":     "2026-01-01T00:00:00Z",
				"Config":      map[string]any{"Image": img},
				"State":       stateOf(state, f.health[n]),
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

func newInstanceCtrl(t *testing.T, l *opLog, emitErr error, primaryConfig, layaConfig string, failCreate map[string]bool) *Ctrl {
	t.Helper()
	return newFaultyInstanceCtrl(t, l, emitErr, primaryConfig, layaConfig, daemonFaults{failCreate: failCreate})
}

func newFaultyInstanceCtrl(t *testing.T, l *opLog, emitErr error, primaryConfig, layaConfig string, f daemonFaults) *Ctrl {
	t.Helper()
	t.Cleanup(docker.SetHostnameForTests(selfHost))
	t.Setenv(attestproxy.SocketEnvVar, "/var/run/zg-tee/tee.sock")
	return &Ctrl{
		config:       config.ControllerConfig{ImageRepo: imageRepo, ConfigFile: primaryConfig},
		dockerClient: fakeFaultyDaemon(t, l, f),
		emitter:      &fakeEmitter{log: l, err: emitErr},
		deriver:      &fakeDeriver{log: l},
		logger:       testLogger(t),
		instances: []Instance{{
			Name: "laya", Broker: "laya-broker", Event: "laya-event", Ingress: "laya-ingress", ConfigFile: layaConfig,
		}},
	}
}

// The extra instance moves with the primary: stopped before the record, because it signs
// under the key the record rebinds, and carried onto the new image after it — broker,
// event and its own ingress.
func TestUpgradeCarriesTheExtraInstance(t *testing.T) {
	l := &opLog{}
	c := newInstanceCtrl(t, l, nil, "", "", map[string]bool{containerEvent: true})

	if _, err := c.UpdateImages(context.Background(), testDigest); err == nil {
		t.Fatal("UpdateImages() = nil, want the primary event recreate to fail")
	}

	ops := l.all()
	emit := l.indexOf("emit " + attest.EventImageUpdate + " " + imageRecord(imageRepo+"@"+testDigest))
	if emit < 0 {
		t.Fatalf("ops = %v, want the image change recorded", ops)
	}
	for _, before := range []string{"stop laya-event", "stop laya-broker", "stop " + containerEvent, "stop " + containerBroker} {
		if i := l.indexOf(before); i < 0 || i > emit {
			t.Errorf("ops = %v, want %q before the record at %d", ops, before, emit)
		}
	}
	for _, after := range []string{"create " + containerBroker, "create laya-broker", "create laya-event", "restart laya-ingress"} {
		if i := l.indexOf(after); i < 0 || i < emit {
			t.Errorf("ops = %v, want %q after the record at %d", ops, after, emit)
		}
	}
	// The primary broker goes first: it is the image the key is derived from.
	if l.indexOf("create laya-broker") < l.indexOf("create "+containerBroker) {
		t.Errorf("ops = %v, want the primary broker recreated before the instance's", ops)
	}
}

// A failure on the instance's side must not abort the primary's upgrade: the primary is
// already on the new image and has to finish, and the instance failure is still reported.
func TestUpgradeFinishesThePrimaryWhenAnInstanceFails(t *testing.T) {
	l := &opLog{}
	c := newInstanceCtrl(t, l, nil, "", "", map[string]bool{"laya-broker": true, containerEvent: true})

	result, err := c.UpdateImages(context.Background(), testDigest)
	if err == nil {
		t.Fatal("UpdateImages() = nil, want an error")
	}
	// Both halves reach the caller: the handler reports result.Error, not err.
	if !strings.Contains(err.Error(), "laya-broker") || result == nil || !strings.Contains(result.Error, "laya-broker") {
		t.Errorf("UpdateImages() = %+v, %v; want the instance failure in both the error and result.Error", result, err)
	}
	if l.indexOf("create "+containerEvent) < 0 {
		t.Errorf("ops = %v, want the primary to carry on to its event after the instance failed", l.all())
	}
	if l.indexOf("create laya-event") >= 0 {
		t.Errorf("ops = %v, want the failed instance's event left alone", l.all())
	}
}

// Nothing recorded means nothing changed, so every stopped container — the instance's
// included — is started again.
func TestFailedRecordRestartsTheExtraInstance(t *testing.T) {
	l := &opLog{}
	c := newInstanceCtrl(t, l, errors.New("dstack.sock: connection refused"), "", "", nil)

	if _, err := c.UpdateImages(context.Background(), testDigest); err == nil {
		t.Fatal("UpdateImages() = nil, want an error")
	}
	for _, want := range []string{"start laya-broker", "start laya-event", "start " + containerBroker, "start " + containerEvent} {
		if l.indexOf(want) < 0 {
			t.Errorf("ops = %v, want %q", l.all(), want)
		}
	}
	if l.indexOf("create") >= 0 {
		t.Errorf("ops = %v, want nothing recreated", l.all())
	}
}

func TestInstanceConfigChangeIsRecordedAndTouchesOnlyThatInstance(t *testing.T) {
	dir := t.TempDir()
	primary := filepath.Join(dir, "config.yaml")
	laya := filepath.Join(dir, "laya.yaml")
	const primaryBefore = "service:\n  model: primary\n"
	for path, content := range map[string]string{primary: primaryBefore, laya: "service:\n  model: before\n"} {
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	l := &opLog{}
	c := newInstanceCtrl(t, l, nil, primary, laya, nil)

	const content = "service:\n  model: after\n"
	if err := c.ApplyInstanceConfig(context.Background(), "laya", content); err != nil {
		t.Fatalf("ApplyInstanceConfig() = %v", err)
	}

	sum := sha256.Sum256([]byte(content))
	ops := l.all()
	if want := "emit " + attest.EventInstanceConfigUpdate + " laya " + hex.EncodeToString(sum[:]); len(ops) == 0 || ops[0] != want {
		t.Fatalf("ops = %v, want %q first", ops, want)
	}
	for _, want := range []string{"restart laya-broker", "restart laya-event", "restart laya-ingress"} {
		if l.indexOf(want) < 0 {
			t.Errorf("ops = %v, want %q", ops, want)
		}
	}
	for _, never := range []string{"restart " + containerBroker, "restart " + containerEvent, "emit " + attest.EventConfigUpdate} {
		if l.indexOf(never) >= 0 {
			t.Errorf("ops = %v, want no %q", ops, never)
		}
	}
	if got, _ := os.ReadFile(laya); string(got) != content {
		t.Errorf("instance config = %q, want %q", got, content)
	}
	if got, _ := os.ReadFile(primary); string(got) != primaryBefore {
		t.Errorf("primary config = %q, want it untouched", got)
	}
	if got, err := c.GetInstanceConfig("laya"); err != nil || got != content {
		t.Errorf("GetInstanceConfig() = %q, %v; want the new content", got, err)
	}
}

func TestInstanceConfigRefusals(t *testing.T) {
	dir := t.TempDir()
	laya := filepath.Join(dir, "laya.yaml")
	l := &opLog{}
	c := newInstanceCtrl(t, l, nil, filepath.Join(dir, "config.yaml"), laya, nil)

	var invalid *InvalidInstanceError
	for _, name := range []string{"nope", ""} {
		if err := c.ApplyInstanceConfig(context.Background(), name, "service:\n  model: x\n"); !errors.As(err, &invalid) {
			t.Errorf("ApplyInstanceConfig(%q) = %v, want InvalidInstanceError", name, err)
		}
	}
	if _, err := c.GetInstanceConfig("nope"); !errors.As(err, &invalid) {
		t.Errorf("GetInstanceConfig(unknown) = %v, want InvalidInstanceError", err)
	}
	if err := c.ApplyInstanceConfig(context.Background(), "laya", "service: [\n"); err == nil {
		t.Error("ApplyInstanceConfig(bad yaml) = nil, want an error")
	}
	if ops := l.all(); len(ops) != 0 {
		t.Errorf("ops = %v, want nothing recorded or touched by a refused change", ops)
	}
}

// A write that fails after the record is recorded again with what is actually on disk.
func TestFailedInstanceConfigWriteRestoresTheRecord(t *testing.T) {
	dir := t.TempDir()
	l := &opLog{}
	// A directory where the file should be: the write fails, the re-read fails too.
	laya := filepath.Join(dir, "laya.yaml")
	if err := os.Mkdir(laya, 0o755); err != nil {
		t.Fatal(err)
	}
	c := newInstanceCtrl(t, l, nil, filepath.Join(dir, "config.yaml"), laya, nil)

	if err := c.ApplyInstanceConfig(context.Background(), "laya", "service:\n  model: x\n"); err == nil {
		t.Fatal("ApplyInstanceConfig() = nil, want the write to fail")
	}
	ops := l.all()
	if want := "emit " + attest.EventInstanceConfigUpdate + " laya unknown"; len(ops) < 2 || ops[1] != want {
		t.Fatalf("ops = %v, want the record restored as %q", ops, want)
	}
	if l.indexOf("restart") >= 0 {
		t.Errorf("ops = %v, want nothing restarted", ops)
	}
}

// A stop that fails before the record brings back whatever this call already stopped,
// and records nothing.
func TestFailedInstanceStopRestartsWhatWasStopped(t *testing.T) {
	l := &opLog{}
	c := newFaultyInstanceCtrl(t, l, nil, "", "", daemonFaults{failStop: map[string]bool{"laya-broker": true}})

	if _, err := c.UpdateImages(context.Background(), testDigest); err == nil {
		t.Fatal("UpdateImages() = nil, want the failed stop reported")
	}
	if l.indexOf("start laya-event") < 0 {
		t.Errorf("ops = %v, want the already-stopped event started again", l.all())
	}
	for _, never := range []string{"emit", "create", "stop " + containerBroker} {
		if l.indexOf(never) >= 0 {
			t.Errorf("ops = %v, want no %q", l.all(), never)
		}
	}
}

// An instance container left on another image — what a recreate that failed before the
// removal leaves behind — cannot be started through the controller, by the container
// routes or by a config change's restart.
func TestInstanceOffTheRecordedImageIsNotStarted(t *testing.T) {
	dir := t.TempDir()
	laya := filepath.Join(dir, "laya.yaml")
	if err := os.WriteFile(laya, []byte("service:\n  model: before\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stale := imageRepo + "@sha256:" + strings.Repeat("7", 64)
	l := &opLog{}
	c := newFaultyInstanceCtrl(t, l, nil, filepath.Join(dir, "config.yaml"), laya, daemonFaults{image: map[string]string{"laya-broker": stale}})

	for _, call := range []func() error{
		func() error { return c.StartContainer(context.Background(), "laya-broker") },
		func() error { return c.RestartContainer(context.Background(), "laya-broker") },
		func() error { return c.ApplyInstanceConfig(context.Background(), "laya", "service:\n  model: after\n") },
	} {
		if err := call(); err == nil || !strings.Contains(err.Error(), "refusing to start") {
			t.Errorf("call = %v, want a refusal naming the stale image", err)
		}
	}
	if ops := l.all(); len(ops) != 0 {
		t.Errorf("ops = %v, want nothing recorded, started or restarted", ops)
	}
	// The event is on the recorded image, so its routes still work.
	if err := c.RestartContainer(context.Background(), "laya-event"); err != nil {
		t.Errorf("RestartContainer(laya-event) = %v, want it allowed", err)
	}
}

// With an instance container gone, docker's name lookup would fall back to the shortest
// name containing it. The instance routes refuse instead of acting on that neighbour.
func TestInstanceRoutesNeedTheExactContainer(t *testing.T) {
	l := &opLog{}
	// laya-event-db is the neighbour docker's lookup would settle on.
	c := newFaultyInstanceCtrl(t, l, nil, "", "", daemonFaults{missing: map[string]bool{"laya-event": true}, extra: []string{"laya-event-db"}})

	for alias, call := range map[string]func() error{
		"stop":    func() error { return c.StopContainer(context.Background(), "laya-event") },
		"start":   func() error { return c.StartContainer(context.Background(), "laya-event") },
		"restart": func() error { return c.RestartContainer(context.Background(), "laya-event") },
	} {
		var ambiguous *AmbiguousContainerError
		if err := call(); !errors.As(err, &ambiguous) {
			t.Errorf("%s = %v, want AmbiguousContainerError", alias, err)
		}
	}
	if ops := l.all(); len(ops) != 0 {
		t.Errorf("ops = %v, want nothing touched", ops)
	}
}

// A container left stopped on an old image by an earlier failed upgrade is not brought
// back by the recovery paths either, which start only what this call found running.
func TestRecoveryDoesNotStartAStaleInstance(t *testing.T) {
	for name, tc := range map[string]struct {
		emitErr  error
		failStop string
	}{
		"failed stop":   {failStop: containerEvent},
		"failed record": {emitErr: errors.New("dstack.sock: connection refused")},
	} {
		t.Run(name, func(t *testing.T) {
			l := &opLog{}
			f := daemonFaults{
				image: map[string]string{"laya-broker": imageRepo + "@sha256:" + strings.Repeat("7", 64)},
				state: map[string]string{"laya-broker": "exited"},
			}
			if tc.failStop != "" {
				f.failStop = map[string]bool{tc.failStop: true}
			}
			c := newFaultyInstanceCtrl(t, l, tc.emitErr, "", "", f)
			if _, err := c.UpdateImages(context.Background(), testDigest); err == nil {
				t.Fatal("UpdateImages() = nil, want an error")
			}
			if l.indexOf("start laya-broker") >= 0 {
				t.Errorf("ops = %v, want the stale broker left down", l.all())
			}
			if l.indexOf("start laya-event") < 0 {
				t.Errorf("ops = %v, want the running event started again", l.all())
			}
		})
	}
}

// A gone instance container cannot hold a key, so it does not block the upgrade: the
// primary goes ahead, the rest of the instance is stopped and left alone, and the
// report says what to do.
func TestUpgradeGoesAheadPastAGoneInstanceContainer(t *testing.T) {
	l := &opLog{}
	c := newFaultyInstanceCtrl(t, l, nil, "", "", daemonFaults{
		missing:    map[string]bool{"laya-event": true},
		failCreate: map[string]bool{containerEvent: true},
	})

	result, err := c.UpdateImages(context.Background(), testDigest)
	if err == nil || !strings.Contains(err.Error(), "laya-event is gone") || !strings.Contains(result.Error, "laya-event is gone") {
		t.Fatalf("UpdateImages() = %+v, %v; want the gone container reported", result, err)
	}
	emit := l.indexOf("emit " + attest.EventImageUpdate)
	if i := l.indexOf("stop laya-broker"); i < 0 || i > emit {
		t.Errorf("ops = %v, want the instance broker stopped before the record", l.all())
	}
	if l.indexOf("create "+containerBroker) < 0 {
		t.Errorf("ops = %v, want the primary upgraded", l.all())
	}
	if l.indexOf("create laya-broker") >= 0 {
		t.Errorf("ops = %v, want the incomplete instance left alone", l.all())
	}
}

// An instance container with no exact match is gone, even when docker's lookup would
// settle on a neighbour containing its name: the upgrade goes ahead, the neighbour is
// never touched, and the report says what is missing.
func TestNeighboursNeverStandInForAGoneInstanceContainer(t *testing.T) {
	for name, tc := range map[string]struct {
		gone, neighbour, report string
		upgraded                bool
	}{
		"broker, next to its config-init": {gone: "laya-broker", neighbour: "proj-laya-broker-config-init-1", report: "laya-broker is gone"},
		"ingress, under compose's name":   {gone: "laya-ingress", neighbour: "proj-laya-ingress-1", report: "ingress laya-ingress is gone", upgraded: true},
	} {
		t.Run(name, func(t *testing.T) {
			l := &opLog{}
			c := newFaultyInstanceCtrl(t, l, nil, "", "", daemonFaults{
				missing:    map[string]bool{tc.gone: true},
				extra:      []string{tc.neighbour},
				failCreate: map[string]bool{containerEvent: true},
			})
			result, err := c.UpdateImages(context.Background(), testDigest)
			if err == nil || !strings.Contains(result.Error, tc.report) {
				t.Fatalf("UpdateImages() = %+v, %v; want %q reported", result, err, tc.report)
			}
			if l.indexOf("create "+containerBroker) < 0 {
				t.Errorf("ops = %v, want the primary upgraded", l.all())
			}
			for _, op := range l.all() {
				if strings.Contains(op, tc.neighbour) {
					t.Errorf("ops = %v, want the neighbour %s never touched", l.all(), tc.neighbour)
				}
			}
			if got := l.indexOf("create laya-event") >= 0; got != tc.upgraded {
				t.Errorf("ops = %v, instance upgraded = %v, want %v", l.all(), got, tc.upgraded)
			}
		})
	}
}

// A broker that will not come up healthy on the new image still has its event moved
// onto it, so the config change that fixes the broker is not refused for a stale event.
func TestUnhealthyInstanceBrokerStillMovesItsEvent(t *testing.T) {
	l := &opLog{}
	c := newFaultyInstanceCtrl(t, l, nil, "", "", daemonFaults{
		state:      map[string]string{"laya-broker": "exited"},
		health:     map[string]string{"laya-broker": "unhealthy"},
		failCreate: map[string]bool{containerEvent: true},
	})
	result, err := c.UpdateImages(context.Background(), testDigest)
	if err == nil || !strings.Contains(result.Error, "laya-broker did not become healthy") {
		t.Fatalf("UpdateImages() = %+v, %v; want the health failure reported", result, err)
	}
	if l.indexOf("create laya-event") < 0 {
		t.Errorf("ops = %v, want the event recreated on the new image anyway", l.all())
	}
}

// An aborted primary recreate restores the record to the image the instances are on, so
// the ones that were running come back.
func TestAbortedPrimaryRecreateRestartsTheInstance(t *testing.T) {
	l := &opLog{}
	c := newFaultyInstanceCtrl(t, l, nil, "", "", daemonFaults{failCreate: map[string]bool{containerBroker: true}})
	if _, err := c.UpdateImages(context.Background(), testDigest); err == nil {
		t.Fatal("UpdateImages() = nil, want the primary recreate to fail")
	}
	for _, want := range []string{"start laya-broker", "start laya-event"} {
		if l.indexOf(want) < 0 {
			t.Errorf("ops = %v, want %q", l.all(), want)
		}
	}
	if l.indexOf("create laya-broker") >= 0 {
		t.Errorf("ops = %v, want the instance not recreated after an abort", l.all())
	}
}

func stateOf(status, health string) map[string]any {
	st := map[string]any{"Status": status}
	if health != "" {
		st["Health"] = map[string]any{"Status": health}
	}
	return st
}
