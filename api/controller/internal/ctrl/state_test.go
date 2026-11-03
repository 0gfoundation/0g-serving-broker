package ctrl

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/0glabs/0g-serving-broker/common/attest"
	"github.com/0glabs/0g-serving-broker/inference/config"
)

func unb64(t *testing.T, s string) string {
	t.Helper()
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		t.Fatalf("decoding %q: %v", s, err)
	}
	return string(b)
}

// stateCtrl is noLedgerCtrl with a config file on disk.
func stateCtrl(t *testing.T, content string) (*Ctrl, string) {
	t.Helper()
	c, _ := noLedgerCtrl(t, config.Service{TargetURL: "http://vllm:8000/v1"})
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	c.config.ConfigFile = path
	return c, path
}

// The document states, in a fixed order and in the record grammars a reader already parses,
// what runs now: the image and both keys, the config on disk, the bound set, the engines, and
// that there is no ledger. Its identity is the one the keys were derived for.
func TestStateDocumentStatesWhatRunsNow(t *testing.T) {
	c, path := stateCtrl(t, beforeConfig)
	if err := c.RecordUpstreamSet(context.Background()); err != nil {
		t.Fatal(err)
	}
	digest, err := c.RunningBrokerDigest(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	doc, id, err := c.StateDocument(context.Background())
	if err != nil {
		t.Fatalf("StateDocument() = %v", err)
	}
	if id.Digest != digest || id.UpstreamSetHash != c.boundUpstreamSetHash() {
		t.Errorf("identity %+v, want the running digest and the bound set", id)
	}

	lines := strings.Split(string(doc), "\n")
	want := []string{"zg-state-v2", "image", "config", "upstreams", "engines", "ledger"}
	if len(lines) != len(want) {
		t.Fatalf("document %q, want %d lines", doc, len(want))
	}
	for i, prefix := range want {
		if !strings.HasPrefix(lines[i], prefix) {
			t.Fatalf("line %d = %q, want %s", i, lines[i], prefix)
		}
	}
	if got := unb64(t, strings.TrimPrefix(lines[1], "image ")); got != imageRecord(imageRepo+"@"+digest) {
		t.Errorf("image line %q", got)
	}
	content, _ := os.ReadFile(path)
	sum := sha256.Sum256(content)
	if got := unb64(t, strings.TrimPrefix(lines[2], "config ")); got != hex.EncodeToString(sum[:]) {
		t.Errorf("config line %q, want the file's sha256", got)
	}
	setPayload, _ := attest.RenderUpstreamSet([]attest.Upstream{{Name: "vllm", URL: "http://vllm:8000/v1"}})
	if got := unb64(t, strings.TrimPrefix(lines[3], "upstreams ")); got != setPayload {
		t.Errorf("upstreams line %q, want the bound set %q", got, setPayload)
	}
	noEngines, _ := attest.RenderEngineSet(nil)
	if got := unb64(t, strings.TrimPrefix(lines[4], "engines ")); got != noEngines {
		t.Errorf("engines line %q, want %q", got, noEngines)
	}
	if lines[5] != "ledger unavailable" {
		t.Errorf("ledger line %q", lines[5])
	}
	if strings.HasSuffix(string(doc), "\n") {
		t.Error("the document ends in LF; the hash is over lines joined with no final LF")
	}
}

// With a ledger and the switch off, nothing bounds where plaintext goes, and the document
// says so rather than naming a set.
func TestStateDocumentSaysUnrecordedWhenNothingIsRecorded(t *testing.T) {
	c, _ := stateCtrl(t, beforeConfig)
	c.ledger = ledgerRTMR3
	c.config.RecordUpstreamSet = false

	doc, _, err := c.StateDocument(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(doc), "\nupstreams unrecorded\n") || !strings.HasSuffix(string(doc), "\nledger rtmr3") {
		t.Errorf("document %q", doc)
	}
}

// A set recorded but not bound (a binding that failed) is not a bound the keys depend on, so
// the document states it as invalidated.
func TestStateDocumentDoesNotClaimAnUnboundSet(t *testing.T) {
	c, _ := stateCtrl(t, beforeConfig)
	c.ledger = ledgerRTMR3
	c.config.RecordUpstreamSet = true // recorded, never bound

	doc, _, err := c.StateDocument(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(doc), "\nupstreams "+b64(upstreamSetInvalidated)+"\n") {
		t.Errorf("document %q, want the set stated as invalidated", doc)
	}
}

// No document for a set nobody can state — the same refusal CurrentKeyIdentity makes.
func TestStateDocumentRefusesAnUnreadableSet(t *testing.T) {
	c, _ := stateCtrl(t, beforeConfig)
	c.markUpstreamSetUnreadable()
	if _, _, err := c.StateDocument(context.Background()); err == nil {
		t.Error("StateDocument() built a document for an unreadable set")
	}
}

// Instances are listed by name, each with its config file's hash.
func TestStateDocumentListsInstancesSorted(t *testing.T) {
	c, _ := stateCtrl(t, beforeConfig)
	if err := c.RecordUpstreamSet(context.Background()); err != nil {
		t.Fatal(err)
	}
	c.configDir = t.TempDir()
	for _, name := range []string{"zeta", "alpha"} {
		if err := os.WriteFile(filepath.Join(c.configDir, name+".yaml"), []byte(name), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	doc, _, err := c.StateDocument(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	a := strings.Index(string(doc), "\ninstance alpha ")
	z := strings.Index(string(doc), "\ninstance zeta ")
	if a < 0 || z < 0 || a > z {
		t.Fatalf("document %q, want alpha then zeta", doc)
	}
	sum := sha256.Sum256([]byte("alpha"))
	line := strings.SplitN(string(doc)[a+1:], "\n", 2)[0]
	if got := unb64(t, strings.TrimPrefix(line, "instance alpha ")); got != "alpha "+hex.EncodeToString(sum[:]) {
		t.Errorf("instance line payload %q", got)
	}
}
