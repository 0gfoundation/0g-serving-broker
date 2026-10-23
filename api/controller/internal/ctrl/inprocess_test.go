package ctrl

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/0glabs/0g-serving-broker/common/attest"
	"github.com/0glabs/0g-serving-broker/controller/internal/attestproxy"
	"github.com/0glabs/0g-serving-broker/controller/internal/docker"
	"github.com/0glabs/0g-serving-broker/inference/config"
)

// Two distinct dev keys; only their derived addresses matter here.
const (
	inProcKeyA = "0x59c6995e998f97a5a0044966f0945389dc9e86dae88c7a8412f4603b6b78690d"
	inProcKeyB = "0x5de4111afa1a4b94908f83103eb1f1706367c2e68ca870fc3fb9a804cdab365a"
)

func inProcConfig(servingURL, key, model string) string {
	return "service:\n" +
		"  servingUrl: " + servingURL + "\n" +
		"  targetUrl: http://engine:8000/v1\n" +
		"  type: chatbot\n" +
		"  model: " + model + "\n" +
		"  inputPrice: \"1\"\n" +
		"  outputPrice: \"1\"\n" +
		"network:\n" +
		"  url: http://rpc\n" +
		"  chainID: 16661\n" +
		"  privateKeys: [\"" + key + "\"]\n"
}

// newInProcessCtrl is a controller over a primary config and one in-process
// instance "img" in its CONFIG_DIR, with the fake daemon the instance tests use.
func newInProcessCtrl(t *testing.T, l *opLog) (c *Ctrl, primary, img string) {
	t.Helper()
	dir := t.TempDir()
	primary = filepath.Join(dir, "config.yaml")
	instDir := filepath.Join(dir, "instances")
	if err := os.Mkdir(instDir, 0o755); err != nil {
		t.Fatal(err)
	}
	img = filepath.Join(instDir, "img.yaml")
	for path, content := range map[string]string{
		primary: inProcConfig("https://a.example", inProcKeyA, "primary"),
		img:     inProcConfig("https://b.example", inProcKeyB, "before"),
	} {
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("DATABASE_DSN", "")
	t.Cleanup(docker.SetHostnameForTests(selfHost))
	t.Setenv(attestproxy.SocketEnvVar, "/var/run/zg-tee/tee.sock")
	return &Ctrl{
		config:       config.ControllerConfig{ImageRepo: imageRepo, ConfigFile: primary},
		dockerClient: fakeInstanceDaemon(t, l, nil),
		emitter:      &fakeEmitter{log: l},
		deriver:      &fakeDeriver{log: l},
		logger:       testLogger(t),
		configDir:    instDir,
	}, primary, img
}

// The instance runs in the primary processes, so its change restarts them —
// and is still recorded as that instance's, never as the primary's.
func TestInProcessInstanceConfigChangeIsRecordedAndRestartsThePrimaryPair(t *testing.T) {
	l := &opLog{}
	c, primary, img := newInProcessCtrl(t, l)
	primaryBefore, _ := os.ReadFile(primary)

	content := inProcConfig("https://b.example", inProcKeyB, "after")
	if err := c.ApplyInstanceConfig(context.Background(), "img", content); err != nil {
		t.Fatalf("ApplyInstanceConfig() = %v", err)
	}

	sum := sha256.Sum256([]byte(content))
	ops := l.all()
	if want := "emit " + attest.EventInstanceConfigUpdate + " img " + hex.EncodeToString(sum[:]); len(ops) == 0 || ops[0] != want {
		t.Fatalf("ops = %v, want %q first", ops, want)
	}
	for _, want := range []string{"restart " + containerBroker, "restart " + containerEvent} {
		if l.indexOf(want) < 0 {
			t.Errorf("ops = %v, want %q", ops, want)
		}
	}
	if l.indexOf("emit "+attest.EventConfigUpdate) >= 0 {
		t.Errorf("ops = %v, want no primary config record", ops)
	}
	if got, _ := os.ReadFile(img); string(got) != content {
		t.Errorf("instance config = %q, want %q", got, content)
	}
	if got, _ := os.ReadFile(primary); string(got) != string(primaryBefore) {
		t.Errorf("primary config changed: %q", got)
	}
	if got, err := c.GetInstanceConfig("img"); err != nil || got != content {
		t.Errorf("GetInstanceConfig() = %q, %v; want the new content", got, err)
	}
}

// A change the instance's own loader accepts can still stop the process that
// serves every instance; it is refused before anything is recorded.
func TestInProcessChangesAreValidatedAsASet(t *testing.T) {
	l := &opLog{}
	c, _, _ := newInProcessCtrl(t, l)
	var invalidConfig *InvalidConfigError
	var invalidInstance *InvalidInstanceError

	// The instance takes the primary's host, then the primary takes the instance's wallet.
	if err := c.ApplyInstanceConfig(context.Background(), "img", inProcConfig("https://a.example", inProcKeyB, "x")); !errors.As(err, &invalidConfig) {
		t.Errorf("instance onto the primary's host = %v, want InvalidConfigError", err)
	}
	if err := c.ApplyCoreConfig(context.Background(), inProcConfig("https://a.example", inProcKeyB, "x")); !errors.As(err, &invalidConfig) {
		t.Errorf("primary onto the instance's wallet = %v, want InvalidConfigError", err)
	}
	// Instances are created by redeploying, never through the API.
	for _, name := range []string{"new", "../img", "", "primary"} {
		if err := c.ApplyInstanceConfig(context.Background(), name, inProcConfig("https://c.example", inProcKeyB, "x")); !errors.As(err, &invalidInstance) {
			t.Errorf("ApplyInstanceConfig(%q) = %v, want InvalidInstanceError", name, err)
		}
	}
	if ops := l.all(); len(ops) != 0 {
		t.Errorf("ops = %v, want nothing recorded or touched by a refused change", ops)
	}
}

func TestCheckConfigDir(t *testing.T) {
	laya := []Instance{{Name: "laya"}}
	for _, tc := range []struct {
		name      string
		dir       string
		instances []Instance
		record    bool
		ok        bool
	}{
		{"unset", "", laya, true, true},
		{"alone", "/etc/config/instances", nil, false, true},
		{"with container instances", "/etc/config/instances", laya, false, false},
		{"with the upstream-set record", "/etc/config/instances", nil, true, false},
	} {
		if err := checkConfigDir(tc.dir, tc.instances, tc.record); (err == nil) != tc.ok {
			t.Errorf("%s: checkConfigDir() = %v, want ok=%v", tc.name, err, tc.ok)
		}
	}
}
