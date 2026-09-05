package ctrl

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

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

// promptJSON builds a chat body whose PROMPT is n bytes, so fit-test cases
// track the prompt rather than the envelope.
func promptJSON(n int) string {
	return `{"messages":[{"role":"user","content":"` + strings.Repeat("x", n) + `"}]}`
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

// Injecting a cap derived from the estimate would compound its deliberate
// over-reading: at a long prompt it reports a fraction of the room that
// actually remains, and on a reasoning model a cap that small is swallowed by
// thinking tokens — empty content, billed. So the decision is all or nothing:
// when the advertised cap no longer fits, nothing is injected and the engine
// decides from the real token count.
func TestCapMaxOutputTokens_NoRoomForTheFullCapInjectsNothing(t *testing.T) {
	const contextLength, maxCompletion = 200000, 131072
	c := newTestCtrlForOutputCapCtx(t, true, maxCompletion, contextLength)

	// ~150k estimated prompt tokens: the advertised 131072 cannot also fit.
	body := []byte(promptJSON(600000))
	out, err := c.CapMaxOutputTokens(body, "glm-5.3", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(out) != string(body) {
		m := decodeBodyMap(t, out)
		t.Fatalf("no room for the full cap must inject nothing, got max_tokens=%v", m["max_tokens"])
	}
}

// Whenever it does inject, the value is the operator's advertised number —
// never a smaller one derived from the byte estimate.
func TestCapMaxOutputTokens_InjectsTheAdvertisedCapOrNothing(t *testing.T) {
	const contextLength, maxCompletion = 262144, 32768
	c := newTestCtrlForOutputCapCtx(t, true, maxCompletion, contextLength)

	// Walk the body size across the point where the cap stops fitting; every
	// injected value must be exactly the advertised cap.
	for _, bodyLen := range []int{1000, 100000, 400000, 600000, 688000, 690000, 700000} {

		body := []byte(promptJSON(bodyLen))
		out, err := c.CapMaxOutputTokens(body, "glm-5.3", "")
		if err != nil {
			t.Fatalf("bodyLen %d: unexpected error: %v", bodyLen, err)
		}
		m := decodeBodyMap(t, out)
		v, injected := m["max_tokens"]
		if !injected {
			continue
		}
		if v.(float64) != maxCompletion {
			t.Fatalf("bodyLen %d: injected %v, want the advertised %d or nothing", bodyLen, v, maxCompletion)
		}
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

// The byte ratio decides whether an injected cap overflows the context window,
// and the two errors are not symmetric: reading too high forwards without a cap
// (bounded by the window itself), reading too low injects a cap the upstream
// rejects with a 400. Nothing pinned the constant, so 3, 4 and 8 were
// interchangeable — this fixes the boundary in place.
func TestCapMaxOutputTokens_FitBoundaryIsPinnedToTheRatio(t *testing.T) {
	// 29-qwavity-35b-sia's real shape: a 32768 window advertising 8192.
	const contextLength, maxCompletion = 32768, 8192
	c := newTestCtrlForOutputCapCtx(t, true, maxCompletion, contextLength)

	inject := func(promptLen int) (float64, bool) {
		t.Helper()
		body := []byte(promptJSON(promptLen))
		out, err := c.CapMaxOutputTokens(body, "glm-5.3", "")
		if err != nil {
			t.Fatalf("promptLen %d: %v", promptLen, err)
		}
		m := decodeBodyMap(t, out)
		v, ok := m["max_tokens"]
		if !ok {
			return 0, false
		}
		return v.(float64), true
	}

	// The threshold must sit at exactly 3 bytes per token: reading lower widens
	// the fail-open band for nothing, reading higher injects caps that overflow
	// the window and come back as 400s. Both sides are pinned, so 1, 2, 4 and 8
	// all fail.
	const gap = contextLength - maxCompletion // room the prompt may occupy

	if v, injected := inject(3*gap - 1024); !injected || v != maxCompletion {
		t.Fatalf("just under the threshold: injected %v (present=%v), want the advertised %d — the ratio is reading too low", v, injected, maxCompletion)
	}
	if v, injected := inject(3*gap + 1024); injected {
		t.Fatalf("just over the threshold: injected %v, want none — the ratio is reading too high and this cap would overflow the window", v)
	}

	// A short prompt leaves plenty; the advertised cap goes in whole.
	if v, injected := inject(1000); !injected || v != maxCompletion {
		t.Fatalf("short prompt: injected %v (present=%v), want the advertised %d", v, injected, maxCompletion)
	}
}

// The clamp is only worth anything if it is actually wired into the forward
// path. Nothing asserted that: deleting the CapMaxOutputTokens call from
// PrepareHTTPRequest left every test in this package and the config package
// green, so the whole feature could be dropped in a bad rebase and CI would
// have nothing to say. The load-time checks would still pass, the flag would
// still be accepted, and no cap would ever be injected.
func TestPrepareHTTPRequest_AppliesTheOutputCap(t *testing.T) {
	svc := config.Service{
		Type:                       "chatbot",
		ModelType:                  "glm-5.3",
		ProviderType:               "centralized",
		ProviderIdentity:           "zhipu",
		EnforceMaxCompletionTokens: true,
		ModelInfo: &config.ModelInfo{
			ContextLength:       262144,
			MaxCompletionTokens: 32768,
			SupportedParameters: []string{maxTokensKey},
		},
	}
	c := newChatbotTestCtrl(t, svc)
	c.Service.Type = "chatbot"

	body := []byte(`{"model":"glm-5.3","messages":[{"role":"user","content":"hi"}]}`)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))

	req, err := c.PrepareHTTPRequest(ctx, "http://upstream.invalid/v1/chat/completions", body, "chatbot")
	if err != nil {
		t.Fatalf("PrepareHTTPRequest: %v", err)
	}
	forwarded, err := io.ReadAll(req.Body)
	if err != nil {
		t.Fatalf("read forwarded body: %v", err)
	}
	if got := capOf(t, forwarded, maxTokensKey); got != 32768 {
		t.Fatalf("forwarded max_tokens = %v, want the advertised 32768 — the clamp is not wired into the forward path", got)
	}
}

// An image is half a megabyte of base64 standing for on the order of a thousand
// tokens, because vision models charge by patch. Measured by length it reads as
// a nearly full context window, and the cap is skipped on precisely the request
// with the most room left to generate into.
func TestCapMaxOutputTokens_ImagePartDoesNotConsumeTheWindow(t *testing.T) {
	// 20-qwavity-35b's shape, which advertises image input.
	c := newTestCtrlForOutputCapCtx(t, true, 32768, 262144)

	image := `{"type":"image_url","image_url":{"url":"data:image/jpeg;base64,` + strings.Repeat("A", 700000) + `"}}`
	body := []byte(`{"messages":[{"role":"user","content":[` + image + `,{"type":"text","text":"what is this"}]}]}`)

	out, err := c.CapMaxOutputTokens(body, "glm-5.3", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := capOf(t, out, "max_tokens"); got != 32768 {
		t.Fatalf("max_tokens = %v, want the advertised 32768 — a 700 KB image must not read as a full context window", got)
	}
}

// The flat allowance still has to be charged, or a message full of images would
// weigh nothing at all.
func TestPromptTextBytes_ChargesNonTextPartsAnAllowance(t *testing.T) {
	one := promptTextBytes([]byte(`{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:x"}}]}]}`))
	many := promptTextBytes([]byte(`{"messages":[{"role":"user","content":[` +
		strings.TrimSuffix(strings.Repeat(`{"type":"image_url","image_url":{"url":"data:x"}},`, 20), ",") + `]}]}`))

	if one < nonTextPartBytes {
		t.Fatalf("one image charged %d, want at least the %d allowance", one, nonTextPartBytes)
	}
	if many < one*15 {
		t.Fatalf("20 images charged %d against %d for one — each must be charged", many, one)
	}
}
