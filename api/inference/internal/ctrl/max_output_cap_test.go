package ctrl

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/0glabs/0g-serving-broker/inference/config"
)

// newTestCtrlForOutputCap builds a single-model chatbot Ctrl advertising the
// given maxCompletionTokens, with the clamp switched on or off.
func newTestCtrlForOutputCap(t *testing.T, enforce bool, maxCompletion int) *Ctrl {
	t.Helper()
	return newTestCtrlForOutputCapCtx(t, enforce, maxCompletion, 0)
}

// newTestCtrlForOutputCapCtx also sets the advertised contextLength, which
// bounds how much output can be injected into a request that carried no cap.
func newTestCtrlForOutputCapCtx(t *testing.T, enforce bool, maxCompletion, contextLength int) *Ctrl {
	t.Helper()
	return &Ctrl{
		Service: config.Service{
			Type:                       "chatbot",
			ModelType:                  "glm-5.3",
			EnforceMaxCompletionTokens: enforce,
			ModelInfo: &config.ModelInfo{
				MaxCompletionTokens: maxCompletion,
				ContextLength:       contextLength,
				SupportedParameters: []string{"max_tokens"},
			},
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

// json.Number.Int64 is strconv.ParseInt, so it rejects every non-integer
// literal — and 1000.0 is what a client computing its cap in floating point
// sends. Treating that failure as "too big" would RAISE the client's value to
// the advertised maximum, inverting the whole contract.
func TestCapMaxOutputTokens_FloatFormattedValuesAreNotRaised(t *testing.T) {
	c := newTestCtrlForOutputCap(t, true, 32768)

	for _, literal := range []string{"1000.0", "1e3", "100.5", "1.0e2"} {
		body := []byte(`{"max_tokens":` + literal + `}`)
		out, err := c.CapMaxOutputTokens(body, "glm-5.3", "")
		if err != nil {
			t.Fatalf("%s: unexpected error: %v", literal, err)
		}
		if string(out) != string(body) {
			t.Fatalf("%s: a value below the cap must be forwarded untouched, got %s", literal, out)
		}
	}
}

// The same float path must still clamp when the value really is too large.
func TestCapMaxOutputTokens_FloatFormattedOverLimitIsLowered(t *testing.T) {
	c := newTestCtrlForOutputCap(t, true, 32768)

	for _, literal := range []string{"1e20", "200000.0", "5e5"} {
		out, err := c.CapMaxOutputTokens([]byte(`{"max_tokens":`+literal+`}`), "glm-5.3", "")
		if err != nil {
			t.Fatalf("%s: unexpected error: %v", literal, err)
		}
		if got := capOf(t, out, "max_tokens"); got != 32768 {
			t.Fatalf("%s: max_tokens = %v, want 32768", literal, got)
		}
	}
}

// A null next to a real sibling value must not become the cap: the sibling is
// the client's actual request, and TranslateMaxTokensFor may later keep the
// null's spelling and drop the sibling's.
func TestCapMaxOutputTokens_NullBesideRealSiblingDoesNotRaise(t *testing.T) {
	c := newTestCtrlForOutputCap(t, true, 32768)

	out, err := c.CapMaxOutputTokens([]byte(`{"max_tokens":null,"max_completion_tokens":500}`), "glm-5.3", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	m := decodeBodyMap(t, out)
	if _, exists := m["max_tokens"]; exists {
		t.Fatalf("a null cap must be dropped, not filled with the limit: %s", out)
	}
	if got := capOf(t, out, "max_completion_tokens"); got != 500 {
		t.Fatalf("max_completion_tokens = %v, want the client's 500", got)
	}
}

// Injecting the advertised maximum can push input + max_tokens past the context
// window, turning a request that used to work into an upstream 400. The
// injected value must be what still fits.
func TestCapMaxOutputTokens_InjectionRespectsContextWindow(t *testing.T) {
	// A model whose advertised output cap is a large fraction of its context.
	const contextLength, maxCompletion = 200000, 131072
	c := newTestCtrlForOutputCapCtx(t, true, maxCompletion, contextLength)

	// ~150k tokens of prompt at the conservative 3 bytes/token estimate.
	body := []byte(`{"p":"` + strings.Repeat("x", 450000) + `"}`)
	out, err := c.CapMaxOutputTokens(body, "glm-5.3", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	injected := capOf(t, out, "max_tokens")
	if injected >= maxCompletion {
		t.Fatalf("injected %v, want less than the advertised %d for a long prompt", injected, maxCompletion)
	}
	// The estimate deliberately runs high, so prompt + injected output must sit
	// inside the window even before the real tokenizer shortens the prompt.
	if promptEstimate := float64(len(body) / conservativeBytesPerToken); promptEstimate+injected > contextLength {
		t.Fatalf("injected %v on top of an estimated %v prompt exceeds the %d context window", injected, promptEstimate, contextLength)
	}
}

// When the prompt already fills the context, injecting anything is guesswork —
// the engine computes the real remaining room from the tokenized prompt.
func TestCapMaxOutputTokens_NoRoomInjectsNothing(t *testing.T) {
	c := newTestCtrlForOutputCapCtx(t, true, 32768, 10000)
	body := []byte(`{"p":"` + strings.Repeat("x", 60000) + `"}`)

	out, err := c.CapMaxOutputTokens(body, "glm-5.3", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(out) != string(body) {
		t.Fatalf("with no context room left, the body must be forwarded unchanged")
	}
}

// A short prompt leaves plenty of room, so the advertised cap is used as-is.
func TestCapMaxOutputTokens_ShortPromptGetsTheFullCap(t *testing.T) {
	c := newTestCtrlForOutputCapCtx(t, true, 32768, 200000)

	out, err := c.CapMaxOutputTokens([]byte(`{"model":"glm-5.3"}`), "glm-5.3", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := capOf(t, out, "max_tokens"); got != 32768 {
		t.Fatalf("max_tokens = %v, want the full advertised 32768", got)
	}
}

// The engines behind this broker validate with pydantic in lax mode, which
// coerces "1000000" to an int — so a quoted cap really is honoured upstream.
// Leaving it unread would make the clamp bypassable by one character.
func TestCapMaxOutputTokens_StringValueIsClamped(t *testing.T) {
	c := newTestCtrlForOutputCap(t, true, 32768)

	out, err := c.CapMaxOutputTokens([]byte(`{"max_tokens":"1000000"}`), "glm-5.3", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := capOf(t, out, "max_tokens"); got != 32768 {
		t.Fatalf("max_tokens = %v, want 32768", got)
	}
}

// A quoted value below the cap is still the client's own request.
func TestCapMaxOutputTokens_StringValueBelowCapIsKept(t *testing.T) {
	c := newTestCtrlForOutputCap(t, true, 32768)
	body := []byte(`{"max_tokens":"512"}`)

	out, err := c.CapMaxOutputTokens(body, "glm-5.3", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(out) != string(body) {
		t.Fatalf("a quoted value below the cap must be forwarded untouched, got %s", out)
	}
}

// Values that cannot be read as a number cannot be honoured, and must not be a
// way around the clamp either.
func TestCapMaxOutputTokens_UnreadableValuesGetTheCap(t *testing.T) {
	c := newTestCtrlForOutputCap(t, true, 32768)

	for _, literal := range []string{`"lots"`, `1e400`, `{"a":1}`, `true`, `"NaN"`, `"nan"`, `"Inf"`, `"-Inf"`} {
		out, err := c.CapMaxOutputTokens([]byte(`{"max_tokens":`+literal+`}`), "glm-5.3", "")
		if err != nil {
			t.Fatalf("%s: unexpected error: %v", literal, err)
		}
		if got := capOf(t, out, "max_tokens"); got != 32768 {
			t.Fatalf("%s: max_tokens = %v, want the cap", literal, got)
		}
	}
}

// "NaN" parses as a number and then loses every comparison, so before this it
// was neither clamped nor dropped while still suppressing the injection — a
// request forwarded with no enforceable bound at all.
func TestCapMaxOutputTokens_NaNDoesNotEscapeTheClamp(t *testing.T) {
	c := newTestCtrlForOutputCap(t, true, 32768)

	out, err := c.CapMaxOutputTokens([]byte(`{"max_tokens":"NaN"}`), "glm-5.3", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := capOf(t, out, "max_tokens"); got != 32768 {
		t.Fatalf("max_tokens = %v, want the cap", got)
	}
}

// An unreadable value must be dropped, not overwritten with the limit: the
// rename pass keeps the destination spelling and discards the source, so
// overwriting one spelling can replace a perfectly good smaller value in the
// other — raising the cap, the one thing this pass promises never to do.
func TestCapMaxOutputTokens_UnreadableDoesNotOverrideSibling(t *testing.T) {
	c := newTestCtrlForOutputCap(t, true, 32768)

	out, err := c.CapMaxOutputTokens([]byte(`{"max_tokens":"lots","max_completion_tokens":512}`), "glm-5.3", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	m := decodeBodyMap(t, out)
	if _, exists := m["max_tokens"]; exists {
		t.Fatalf("the unreadable spelling must be dropped, not set to the limit: %s", out)
	}
	if got := capOf(t, out, "max_completion_tokens"); got != 512 {
		t.Fatalf("max_completion_tokens = %v, want the client's 512", got)
	}
}

// Zero is what an unset int field serializes to in Go and Java, and it means
// "generate nothing" everywhere else — not "unlimited". Replacing it with the
// cap would be this pass's largest possible raise.
func TestCapMaxOutputTokens_ZeroIsForwardedNotRaised(t *testing.T) {
	c := newTestCtrlForOutputCap(t, true, 32768)
	body := []byte(`{"max_tokens":0}`)

	out, err := c.CapMaxOutputTokens(body, "glm-5.3", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(out) != string(body) {
		t.Fatalf("zero must be forwarded for the upstream to reject, got %s", out)
	}
}

// An upstream that accepts only max_completion_tokens must not be handed the
// older spelling: OpenAI's reasoning models answer that with a 400.
func TestCapMaxOutputTokens_InjectsTheSpellingTheModelAdvertises(t *testing.T) {
	c := newTestCtrlForOutputCap(t, true, 32768)
	c.Service.ModelInfo.SupportedParameters = []string{"max_completion_tokens"}

	out, err := c.CapMaxOutputTokens([]byte(`{"model":"glm-5.3"}`), "glm-5.3", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	m := decodeBodyMap(t, out)
	if _, exists := m["max_tokens"]; exists {
		t.Fatalf("must not inject the spelling this model does not accept: %s", out)
	}
	if got := capOf(t, out, "max_completion_tokens"); got != 32768 {
		t.Fatalf("max_completion_tokens = %v, want 32768", got)
	}
}

// Several clients spell "unlimited" as -1. That is not a bound, so it falls
// through to injection; going from unlimited to the cap is a reduction.
func TestCapMaxOutputTokens_NegativeIsTreatedAsNoCap(t *testing.T) {
	c := newTestCtrlForOutputCap(t, true, 32768)

	for _, literal := range []string{"-1"} {
		out, err := c.CapMaxOutputTokens([]byte(`{"max_tokens":`+literal+`}`), "glm-5.3", "")
		if err != nil {
			t.Fatalf("%s: unexpected error: %v", literal, err)
		}
		if got := capOf(t, out, "max_tokens"); got != 32768 {
			t.Fatalf("%s: max_tokens = %v, want a real cap injected", literal, got)
		}
	}
}

// The byte estimate runs high by a third, and that error compounds as the
// prompt approaches the window: a cap computed from it can be a twentieth of
// the room that actually remains. On a reasoning model a cap that small is
// consumed entirely by thinking tokens, so the client gets an empty answer with
// finish_reason "length" and pays for it — strictly worse than injecting
// nothing, which is what the no-room case already does.
func TestCapMaxOutputTokens_TinyRemainingRoomInjectsNothing(t *testing.T) {
	const contextLength, maxCompletion = 262144, 32768
	c := newTestCtrlForOutputCapCtx(t, true, maxCompletion, contextLength)

	// Sized so the estimate leaves a positive but useless remainder.
	bodyLen := (contextLength - 500) * conservativeBytesPerToken
	body := []byte(`{"p":"` + strings.Repeat("x", bodyLen) + `"}`)

	out, err := c.CapMaxOutputTokens(body, "glm-5.3", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(out) != string(body) {
		m := decodeBodyMap(t, out)
		t.Fatalf("a sub-floor remainder must inject nothing, got max_tokens=%v", m["max_tokens"])
	}
}

// Just above the floor it still injects, so the floor is a floor and not a
// silent disable of the whole feature.
func TestCapMaxOutputTokens_RoomAtTheFloorStillInjects(t *testing.T) {
	const contextLength, maxCompletion = 262144, 32768
	c := newTestCtrlForOutputCapCtx(t, true, maxCompletion, contextLength)

	bodyLen := (contextLength - 4096) * conservativeBytesPerToken
	out, err := c.CapMaxOutputTokens([]byte(`{"p":"`+strings.Repeat("x", bodyLen)+`"}`), "glm-5.3", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	injected := capOf(t, out, "max_tokens")
	if injected < minInjectableOutputTokens {
		t.Fatalf("injected %v, want at least the floor %d", injected, minInjectableOutputTokens)
	}
	if injected >= maxCompletion {
		t.Fatalf("injected %v, want the context-reduced value", injected)
	}
}
