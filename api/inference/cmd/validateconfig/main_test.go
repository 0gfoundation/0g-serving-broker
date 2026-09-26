package validateconfig

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const validConfig = `
service:
  servingUrl: "http://example.com"
  targetUrl: "http://upstream:8000/v1"
  inputPrice: "1000"
  outputPrice: "2000"
  type: "chatbot"
  model: "test"
  verifiability: "TeeML"
`

func TestRun(t *testing.T) {
	dir := t.TempDir()

	ok := filepath.Join(dir, "ok.yaml")
	if err := os.WriteFile(ok, []byte(validConfig+"futureFeature: 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := run(ok); err != nil {
		t.Fatalf("run(valid, with an unknown key) = %v, want nil", err)
	}
	if _, err := os.Stat(ok + ".err"); !os.IsNotExist(err) {
		t.Fatal("a passing validation must not leave an .err file")
	}

	bad := filepath.Join(dir, "bad.yaml")
	if err := os.WriteFile(bad, []byte(validConfig+"nvGPU: notabool\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := run(bad); err == nil {
		t.Fatal("run(invalid) = nil, want the load error")
	}
	msg, err := os.ReadFile(bad + ".err")
	if err != nil || !strings.Contains(string(msg), "cannot unmarshal") {
		t.Fatalf(".err = %q (%v), want the reason for the caller to read", msg, err)
	}

	if err := run(filepath.Join(dir, "missing.yaml")); err == nil {
		t.Fatal("run(missing file) = nil, want an error")
	}
}
