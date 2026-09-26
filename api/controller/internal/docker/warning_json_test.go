package docker

import (
	"encoding/json"
	"strings"
	"testing"
)

// The warning only reaches an operator through the images/update response body.
func TestImageUpdateResultSerialisesWarning(t *testing.T) {
	b, err := json.Marshal(ImageUpdateResult{Warning: "check the config"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"warning":"check the config"`) {
		t.Fatalf("%s does not carry the warning", b)
	}
	if b, _ := json.Marshal(ImageUpdateResult{}); strings.Contains(string(b), "warning") {
		t.Fatalf("%s: an empty warning must be omitted", b)
	}
}
