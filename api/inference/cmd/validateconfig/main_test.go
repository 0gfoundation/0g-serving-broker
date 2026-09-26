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

	bad := filepath.Join(dir, "bad.yaml")
	if err := os.WriteFile(bad, []byte(validConfig+"nvGPU: notabool\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := run(bad); err == nil || !strings.Contains(err.Error(), "cannot unmarshal") {
		t.Fatalf("run(invalid) = %v, want the load error", err)
	}
	// It writes nothing next to the file: the config volume is read-only where it runs.
	if left, _ := filepath.Glob(filepath.Join(dir, "*.err")); len(left) != 0 {
		t.Fatalf("left %v behind", left)
	}

	if err := run(filepath.Join(dir, "missing.yaml")); err == nil {
		t.Fatal("run(missing file) = nil, want an error")
	}
}
