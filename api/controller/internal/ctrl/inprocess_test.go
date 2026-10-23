package ctrl

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/crypto"

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
		ok        bool
	}{
		{"unset", "", laya, true},
		{"alone", "/etc/config/instances", nil, true},
		{"with container instances", "/etc/config/instances", laya, false},
	} {
		if err := checkConfigDir(tc.dir, tc.instances); (err == nil) != tc.ok {
			t.Errorf("%s: checkConfigDir() = %v, want ok=%v", tc.name, err, tc.ok)
		}
	}
}

func inProcAddress(t *testing.T, key string) string {
	t.Helper()
	k, err := crypto.HexToECDSA(strings.TrimPrefix(key, "0x"))
	if err != nil {
		t.Fatal(err)
	}
	return strings.ToLower(crypto.PubkeyToAddress(k.PublicKey).Hex())
}

// With in-process instances the recorded set is every provider's: the instances run
// under the same signer, so where any of them may send plaintext is the answer a
// reader needs. Each member says whose config permits it, so one engine both use
// is two members.
func TestInProcessUpstreamSetCoversEveryProvider(t *testing.T) {
	l := &opLog{}
	c, _, _ := newInProcessCtrl(t, l)
	c.config.RecordUpstreamSet = true

	if err := c.RecordUpstreamSet(context.Background()); err != nil {
		t.Fatalf("RecordUpstreamSet() = %v", err)
	}
	a, b := inProcAddress(t, inProcKeyA), inProcAddress(t, inProcKeyB)
	first, second := a, b
	if b < a {
		first, second = b, a
	}
	want := "count=2\n" +
		"engine http://engine:8000/v1 provider=" + first + "\n" +
		"engine http://engine:8000/v1 provider=" + second
	if got := emitted(l); len(got) != 1 || got[0] != want {
		t.Fatalf("recorded %q, want [%q]", got, want)
	}
}

// A change to one instance moves the whole set, so it is recorded again — after the
// instance's config record and before the file is written, like the primary's.
func TestInProcessInstanceChangeRecordsTheNewSetBeforeTheWrite(t *testing.T) {
	l := &opLog{}
	c, _, img := newInProcessCtrl(t, l)
	c.config.RecordUpstreamSet = true
	watcher := &fileWatchingEmitter{log: l, path: img, at: map[string]string{}}
	c.emitter = watcher
	before, _ := os.ReadFile(img)

	content := strings.Replace(inProcConfig("https://b.example", inProcKeyB, "after"), "http://engine:8000/v1", "http://img-engine:8000/v1", 1)
	if err := c.ApplyInstanceConfig(context.Background(), "img", content); err != nil {
		t.Fatalf("ApplyInstanceConfig() = %v", err)
	}
	got := emitted(l)
	b := inProcAddress(t, inProcKeyB)
	if len(got) != 1 || !strings.Contains(got[0], "img-engine http://img-engine:8000/v1 provider="+b) || !strings.Contains(got[0], "engine http://engine:8000/v1 provider="+inProcAddress(t, inProcKeyA)) {
		t.Fatalf("recorded %q, want the primary's engine and the instance's new one", got)
	}
	if onDisk := watcher.at[attest.EventUpstreamSet]; onDisk != string(before) {
		t.Errorf("the set was recorded with %q on disk; it must be recorded before the write", onDisk)
	}
	if i, j := l.indexOf("emit "+attest.EventInstanceConfigUpdate), l.indexOf("emit "+attest.EventUpstreamSet); i < 0 || j < i {
		t.Errorf("ops = %v, want the instance's config record before the set", l.all())
	}
}
