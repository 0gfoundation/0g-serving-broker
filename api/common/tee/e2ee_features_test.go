package tee

import (
	"encoding/json"
	"strings"
	"testing"
)

// The features ride in the quote reply as a top-level member, and nothing the
// backend produced changes: a client hashes the app compose out of this body, so
// a re-marshal that reordered or re-escaped it would break that comparison.
func TestWithE2EEFeatures(t *testing.T) {
	quote := `{"quote":"00","event_log":"[{\"x\":\"<y>\"}]","tcb_info":"{\"app_compose\":\"a&b\"}"}`
	got, err := withE2EEFeatures(quote, []string{"resp-bind"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got, strings.TrimSuffix(quote, "}")) {
		t.Fatalf("the backend's bytes changed:\n%s", got)
	}
	var obj struct {
		Quote    string   `json:"quote"`
		Features []string `json:"e2ee_features"`
	}
	if err := json.Unmarshal([]byte(got), &obj); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, got)
	}
	if obj.Quote != "00" || len(obj.Features) != 1 || obj.Features[0] != "resp-bind" {
		t.Fatalf("decoded %+v", obj)
	}
}

func TestWithE2EEFeaturesEdges(t *testing.T) {
	if got, err := withE2EEFeatures(`{"quote":"00"}`, nil); err != nil || got != `{"quote":"00"}` {
		t.Fatalf("no features changed the reply: %q, %v", got, err)
	}
	if got, err := withE2EEFeatures("{}\n", []string{"resp-bind"}); err != nil || got != `{"e2ee_features":["resp-bind"]}` {
		t.Fatalf("empty object: %q, %v", got, err)
	}
	for _, bad := range []string{`not json`, `[1]`, `null`, `{"e2ee_features":[]}`} {
		if _, err := withE2EEFeatures(bad, []string{"resp-bind"}); err == nil {
			t.Errorf("%s: accepted", bad)
		}
	}
}
