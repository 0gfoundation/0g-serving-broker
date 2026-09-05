package ctrl

import (
	"encoding/json"
	"testing"

	"github.com/0glabs/0g-serving-broker/inference/config"
)

// newTestCtrlForOutputCap builds a single-model chatbot Ctrl advertising the
// given maxCompletionTokens, with the clamp switched on or off.
func newTestCtrlForOutputCap(t *testing.T, enforce bool, maxCompletion int) *Ctrl {
	t.Helper()
	return &Ctrl{
		Service: config.Service{
			Type:                       "chatbot",
			ModelType:                  "glm-5.3",
			EnforceMaxCompletionTokens: enforce,
			ModelInfo:                  &config.ModelInfo{MaxCompletionTokens: maxCompletion},
		},
		logger:         testLogger(),
		whitelistUsers: make(map[string]struct{}),
	}
}

// capOf reads a numeric cap field, failing if it is missing or not a number.
func capOf(t *testing.T, body []byte, key string) float64 {
	t.Helper()
	m := decodeBodyMap(t, body)
	v, ok := m[key]
	if !ok {
		t.Fatalf("body has no %q: %s", key, body)
	}
	n, ok := v.(float64)
	if !ok {
		t.Fatalf("%q is not a number: %#v", key, v)
	}
	return n
}

// The case the clamp exists for: no cap at all means the engine generates until
// the context window stops it, holding its KV slot the whole time.
func TestCapMaxOutputTokens_AbsentGetsTheCap(t *testing.T) {
	c := newTestCtrlForOutputCap(t, true, 32768)

	out, err := c.CapMaxOutputTokens([]byte(`{"model":"glm-5.3","messages":[]}`), "glm-5.3", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := capOf(t, out, "max_tokens"); got != 32768 {
		t.Fatalf("max_tokens = %v, want 32768", got)
	}
}

// An explicit null is a client saying "no limit" — same case as absent.
func TestCapMaxOutputTokens_NullGetsTheCap(t *testing.T) {
	c := newTestCtrlForOutputCap(t, true, 32768)

	out, err := c.CapMaxOutputTokens([]byte(`{"max_tokens":null}`), "glm-5.3", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := capOf(t, out, "max_tokens"); got != 32768 {
		t.Fatalf("max_tokens = %v, want 32768", got)
	}
}

func TestCapMaxOutputTokens_HigherIsLowered(t *testing.T) {
	c := newTestCtrlForOutputCap(t, true, 32768)

	out, err := c.CapMaxOutputTokens([]byte(`{"max_tokens":200000}`), "glm-5.3", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := capOf(t, out, "max_tokens"); got != 32768 {
		t.Fatalf("max_tokens = %v, want 32768", got)
	}
}

// The whole reason this is not injectBodyFields: a client asking for less must
// keep its own value, or it gets billed for output it never wanted.
func TestCapMaxOutputTokens_LowerIsUntouched(t *testing.T) {
	c := newTestCtrlForOutputCap(t, true, 32768)
	body := []byte(`{"max_tokens":512}`)

	out, err := c.CapMaxOutputTokens(body, "glm-5.3", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := capOf(t, out, "max_tokens"); got != 512 {
		t.Fatalf("max_tokens = %v, want 512 (client's own value)", got)
	}
	if string(out) != string(body) {
		t.Fatalf("body was re-marshalled with nothing to change: %s", out)
	}
}

// Newer clients send max_completion_tokens; both spellings must be clamped,
// since either one alone would otherwise leave the request unbounded.
func TestCapMaxOutputTokens_ClampsBothSpellings(t *testing.T) {
	c := newTestCtrlForOutputCap(t, true, 1000)

	out, err := c.CapMaxOutputTokens([]byte(`{"max_tokens":5000,"max_completion_tokens":6000}`), "glm-5.3", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := capOf(t, out, "max_tokens"); got != 1000 {
		t.Fatalf("max_tokens = %v, want 1000", got)
	}
	if got := capOf(t, out, "max_completion_tokens"); got != 1000 {
		t.Fatalf("max_completion_tokens = %v, want 1000", got)
	}
}

// A present-but-larger sibling must not be mistaken for "no cap": only one
// spelling is clamped, the other is left absent rather than injected.
func TestCapMaxOutputTokens_DoesNotInjectSiblingSpelling(t *testing.T) {
	c := newTestCtrlForOutputCap(t, true, 1000)

	out, err := c.CapMaxOutputTokens([]byte(`{"max_completion_tokens":6000}`), "glm-5.3", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	m := decodeBodyMap(t, out)
	if _, exists := m["max_tokens"]; exists {
		t.Fatalf("max_tokens must not be injected alongside an existing cap: %s", out)
	}
	if got := capOf(t, out, "max_completion_tokens"); got != 1000 {
		t.Fatalf("max_completion_tokens = %v, want 1000", got)
	}
}

func TestCapMaxOutputTokens_OffByDefault(t *testing.T) {
	c := newTestCtrlForOutputCap(t, false, 32768)
	body := []byte(`{"model":"glm-5.3"}`)

	out, err := c.CapMaxOutputTokens(body, "glm-5.3", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(out) != string(body) {
		t.Fatalf("clamp must be inert when not enabled, got %s", out)
	}
}

// An unset or zero maxCompletionTokens means the model advertises no limit;
// inventing one here would truncate every request on that provider.
func TestCapMaxOutputTokens_NoAdvertisedLimitIsNoOp(t *testing.T) {
	c := newTestCtrlForOutputCap(t, true, 0)
	body := []byte(`{"model":"glm-5.3"}`)

	out, err := c.CapMaxOutputTokens(body, "glm-5.3", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(out) != string(body) {
		t.Fatalf("no advertised limit must be a no-op, got %s", out)
	}
}

// Malformed values belong to the upstream's validator, which has a better error
// message than anything invented here.
func TestCapMaxOutputTokens_NonNumericLeftAlone(t *testing.T) {
	c := newTestCtrlForOutputCap(t, true, 1000)
	body := []byte(`{"max_tokens":"lots"}`)

	out, err := c.CapMaxOutputTokens(body, "glm-5.3", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(out) != string(body) {
		t.Fatalf("non-numeric cap must be forwarded untouched, got %s", out)
	}
}

// Unrelated large integers must survive the round-trip as integers, not as
// float64 in scientific notation.
func TestCapMaxOutputTokens_PreservesOtherLargeNumbers(t *testing.T) {
	c := newTestCtrlForOutputCap(t, true, 1000)

	out, err := c.CapMaxOutputTokens([]byte(`{"seed":12345678901234567,"max_tokens":9999}`), "glm-5.3", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("body is not valid JSON: %v", err)
	}
	if string(m["seed"]) != "12345678901234567" {
		t.Fatalf("seed was mangled: %s", m["seed"])
	}
}

func TestCapMaxOutputTokens_NonObjectBodyIsNoOp(t *testing.T) {
	c := newTestCtrlForOutputCap(t, true, 1000)
	for _, body := range []string{``, `null`, `[1,2]`, `not json`} {
		out, err := c.CapMaxOutputTokens([]byte(body), "glm-5.3", "")
		if err != nil {
			t.Fatalf("body %q: unexpected error: %v", body, err)
		}
		if string(out) != body {
			t.Fatalf("body %q must be forwarded unchanged, got %s", body, out)
		}
	}
}
