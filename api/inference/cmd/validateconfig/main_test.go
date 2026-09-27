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

// "-" reads the candidate from stdin — how the controller passes it, so nothing is
// staged on disk and no in-container path has to be known.
func TestRunFromStdin(t *testing.T) {
	for name, tc := range map[string]struct {
		content string
		wantErr bool
	}{
		"valid":   {validConfig + "futureFeature: 1\n", false},
		"invalid": {validConfig + "nvGPU: notabool\n", true},
	} {
		t.Run(name, func(t *testing.T) {
			r, w, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			orig := os.Stdin
			os.Stdin = r
			t.Cleanup(func() { os.Stdin = orig })
			go func() { _, _ = w.WriteString(tc.content); _ = w.Close() }()
			if err := run("-"); (err != nil) != tc.wantErr {
				t.Fatalf("run(-) = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}
