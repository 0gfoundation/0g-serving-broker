package tee

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/0glabs/0g-serving-broker/common/log"
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

// warnRecorder is a logger that records Warnf calls.
type warnRecorder struct {
	log.Logger
	warns []string
}

func (w *warnRecorder) Warnf(format string, args ...interface{}) {
	w.warns = append(w.warns, fmt.Sprintf(format, args...))
}

// A reply the features cannot be added to is served unchanged, with a warning:
// an advertisement must never be why SyncQuote fails and the server does not
// start.
func TestAdvertiseE2EEFeaturesDegradesInsteadOfFailing(t *testing.T) {
	rec := &warnRecorder{Logger: testLogger(t)}
	s := &TeeService{logger: rec, E2EEFeatures: []string{"resp-bind"}}

	for _, quote := range []string{`not json`, `{"e2ee_features":["other"]}`} {
		if got := s.advertiseE2EEFeatures(quote); got != quote {
			t.Errorf("%s: served %q, want the reply unchanged", quote, got)
		}
	}
	if len(rec.warns) != 2 {
		t.Fatalf("warnings = %v, want one per failed reply", rec.warns)
	}

	if got := s.advertiseE2EEFeatures(`{"quote":"00"}`); !strings.Contains(got, `"e2ee_features":["resp-bind"]`) {
		t.Fatalf("a well-formed reply was not advertised on: %s", got)
	}
}
