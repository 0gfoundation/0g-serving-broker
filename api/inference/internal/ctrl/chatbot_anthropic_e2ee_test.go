package ctrl

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/0gfoundation/0g-pc-e2ee/protocol/wire"
	"github.com/gin-gonic/gin"
)

// newAnthropicGinCtx is newGinCtx on the /v1/messages path, which is what makes
// a request resolve to the Anthropic profile: the surface is half the key, and
// the SAME chatbot service answers on both chat paths.
func newAnthropicGinCtx() *gin.Context {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest("POST", "/v1/messages", nil)
	return ctx
}

// sealAnthropicRequest seals a /v1/messages request, sealing the fields named in
// sealedFields so a test can build both the conforming envelope and the one that
// leaves the system prompt in the clear.
func (f *e2eeTestFixture) sealAnthropicRequest(t *testing.T, sealedFields []string) []byte {
	t.Helper()
	req := wire.Request{
		"model":      mustRaw(t, "claude-x"),
		"max_tokens": mustRaw(t, 1024),
		"stream":     mustRaw(t, false),
		"system":     mustRaw(t, "top secret system prompt"),
		"messages":   mustRaw(t, []map[string]string{{"role": "user", "content": "top secret"}}),
	}
	// Through the CHAT profile deliberately: it has no opinion about `system`, so
	// it will build the leaky envelope this fixture needs for the receiver-half
	// test. The Anthropic profile's own seal-time check is covered in the protocol
	// package; what is under test here is the broker refusing what it receives.
	sealed, err := wire.SealRequestFor(wire.ProfileChat, f.encPub, req, sealedFields, f.signerAddr, f.clientEphPub)
	if err != nil {
		t.Fatalf("SealRequestFor: %v", err)
	}
	b, err := json.Marshal(sealed)
	if err != nil {
		t.Fatalf("marshal sealed request: %v", err)
	}
	return b
}

func TestAnthropicSurfaceUnsealsUnderTheAnthropicProfile(t *testing.T) {
	f := newE2EEFixture(t)
	ctx := newAnthropicGinCtx()

	plaintext, err := f.c.MaybeUnsealRequest(ctx, f.sealAnthropicRequest(t, []string{"messages", "system"}))
	if err != nil {
		t.Fatalf("unseal: %v", err)
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal(plaintext, &got); err != nil {
		t.Fatalf("reconstructed body: %v", err)
	}
	for _, f := range []string{"messages", "system", "model", "max_tokens"} {
		if _, ok := got[f]; !ok {
			t.Errorf("reconstructed request is missing %q", f)
		}
	}
	// The profile is stashed for the response path, so the reply cannot be sealed
	// under rules that were never applied to this request.
	if p, ok := e2eeProfile(ctx); !ok || p != wire.ProfileAnthropic {
		t.Errorf("stashed profile = %q (ok=%v), want %q", p, ok, wire.ProfileAnthropic)
	}
}

// The receiver half of the conditional payload rule (SPEC §5.1/§12): a
// third-party client that seals only `messages` produces a perfectly well-formed
// envelope with the system prompt in its cleartext half. Only the enclave can
// refuse it, and it must refuse BEFORE the request reaches an upstream.
func TestAnthropicSurfaceRefusesACleartextSystemPrompt(t *testing.T) {
	f := newE2EEFixture(t)
	leaky := f.sealAnthropicRequest(t, []string{"messages"})

	// Same bytes on the OpenAI surface are a legitimate chat request: the point is
	// that the surface, not the body, decides which rule applies.
	if _, err := f.c.MaybeUnsealRequest(newGinCtx(), leaky); err != nil {
		t.Fatalf("precondition: the envelope is valid as a chat request: %v", err)
	}

	_, err := f.c.MaybeUnsealRequest(newAnthropicGinCtx(), leaky)
	if err == nil {
		t.Fatal("expected the enclave to refuse a /v1/messages request whose system prompt arrived in the clear")
	}
	if !strings.Contains(err.Error(), "system") {
		t.Errorf("error should name the field that arrived in the clear, got: %v", err)
	}
}

// The regression that the (service type, surface) key exists for. Keyed on the
// service type alone, a sealed Anthropic response sealed an INJECTED empty
// `choices` — satisfying the chat profile — while the real `content` rode in the
// frame's cleartext half. Nothing failed: identical wire format, plausible frame,
// AEAD happy, client happy.
func TestAnthropicNonStreamResponseSealsContentNotChoices(t *testing.T) {
	f := newE2EEFixture(t)
	ctx := newAnthropicGinCtx()
	if _, err := f.c.MaybeUnsealRequest(ctx, f.sealAnthropicRequest(t, []string{"messages", "system"})); err != nil {
		t.Fatalf("unseal: %v", err)
	}

	const provider = `{"id":"msg_1","type":"message","role":"assistant","model":"claude-x",` +
		`"stop_reason":"end_turn","usage":{"input_tokens":11,"output_tokens":20},` +
		`"content":[{"type":"text","text":"the secret answer"}]}`

	out, isSealed, _, err := f.c.maybeSealNonStreamResponse(ctx, []byte(provider))
	if err != nil || !isSealed {
		t.Fatalf("seal response: sealed=%v err=%v", isSealed, err)
	}
	if strings.Contains(string(out), "the secret answer") {
		t.Fatalf("the answer must not appear in the sealed frame: %s", out)
	}

	var frame wire.Response
	if err := json.Unmarshal(out, &frame); err != nil {
		t.Fatalf("sealed frame: %v", err)
	}
	if _, ok := frame["choices"]; ok {
		t.Error("an injected empty `choices` means the frame was sealed under the chat profile")
	}
	if _, ok := frame["content"]; ok {
		t.Error("`content` must be sealed away, not left cleartext")
	}
	for _, k := range []string{"usage", "model", "id", "type", "stop_reason"} {
		if _, ok := frame[k]; !ok {
			t.Errorf("%q must stay cleartext for the router", k)
		}
	}
	e2ee, err := frame.E2EE()
	if err != nil {
		t.Fatalf("read _e2ee: %v", err)
	}
	if len(e2ee.SealedFields) != 1 || e2ee.SealedFields[0] != "content" {
		t.Errorf("sealed_fields = %v, want [content]", e2ee.SealedFields)
	}
	if !e2ee.Final {
		t.Error("a single-frame response must be marked final")
	}
}

// anthropicSSE is a real /v1/messages stream: `event:` line plus `data:` line per
// event, no [DONE] sentinel, terminated by message_stop.
var anthropicSSE = []string{
	"event: message_start\n",
	`data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-x","content":[],"usage":{"input_tokens":11,"output_tokens":1}}}` + "\n",
	"\n",
	"event: content_block_start\n",
	`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}` + "\n",
	"\n",
	"event: content_block_delta\n",
	`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"the secret answer"}}` + "\n",
	"\n",
	"event: content_block_stop\n",
	`data: {"type":"content_block_stop","index":0}` + "\n",
	"\n",
	"event: message_delta\n",
	`data: {"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":20}}` + "\n",
	"\n",
	"event: message_stop\n",
	`data: {"type":"message_stop"}` + "\n",
	"\n",
}

// sealAnthropicStream drives the fixture's sealer over anthropicSSE and returns
// the emitted lines plus the sealed frames in send order.
func (f *e2eeTestFixture) sealAnthropicStream(t *testing.T, ctx *gin.Context) (out []string, frames []wire.Response) {
	t.Helper()
	sealer, err := f.c.newResponseFrameSealer(ctx)
	if err != nil {
		t.Fatalf("newResponseFrameSealer: %v", err)
	}
	if sealer == nil {
		t.Fatal("expected a frame sealer for a sealed request")
	}
	for _, line := range anthropicSSE {
		sealedLine, err := sealer.sealSSELine(line)
		if err != nil {
			t.Fatalf("sealSSELine(%q): %v", line, err)
		}
		out = append(out, sealedLine)
		for _, l := range strings.Split(sealedLine, "\n") {
			payload, ok := strings.CutPrefix(strings.TrimSpace(l), "data:")
			if !ok || !strings.HasPrefix(strings.TrimSpace(payload), "{") {
				continue
			}
			var frame wire.Response
			if err := json.Unmarshal([]byte(payload), &frame); err != nil {
				t.Fatalf("sealed frame is not JSON: %v", err)
			}
			frames = append(frames, frame)
		}
	}
	// A stream that already sent message_stop needs no synthetic terminal frame.
	tail, err := sealer.finalFrameLine()
	if err != nil {
		t.Fatalf("finalFrameLine: %v", err)
	}
	if tail != "" {
		t.Errorf("message_stop IS the final frame; nothing may follow it, got %q", tail)
	}
	return out, frames
}

func TestAnthropicStreamSealsPerFrameShape(t *testing.T) {
	f := newE2EEFixture(t)
	ctx := newAnthropicGinCtx()
	if _, err := f.c.MaybeUnsealRequest(ctx, f.sealAnthropicRequest(t, []string{"messages", "system"})); err != nil {
		t.Fatalf("unseal: %v", err)
	}

	lines, frames := f.sealAnthropicStream(t, ctx)
	joined := strings.Join(lines, "")

	if strings.Contains(joined, "the secret answer") {
		t.Fatalf("a delta rode in the clear:\n%s", joined)
	}
	// The `event:` lines pass through beside their sealed data lines, so the
	// stream stays a well-formed SSE stream of this API.
	for _, want := range []string{"event: message_start", "event: content_block_delta", "event: message_stop"} {
		if !strings.Contains(joined, want) {
			t.Errorf("%q must survive sealing", want)
		}
	}

	wantSealed := [][]string{{}, {"content_block"}, {"delta"}, {}, {"delta"}, {}}
	if len(frames) != len(wantSealed) {
		t.Fatalf("sealed %d frames, want %d", len(frames), len(wantSealed))
	}
	for i, frame := range frames {
		e2ee, err := frame.E2EE()
		if err != nil {
			t.Fatalf("frame %d has no _e2ee: %v", i, err)
		}
		if got := e2ee.SealedFields; !sameStrings(got, wantSealed[i]) {
			t.Errorf("frame %d sealed_fields = %v, want %v", i, got, wantSealed[i])
		}
		// Every frame is sealed, sequencing ones included: the router requires an
		// `_e2ee` on each, and §8 binds them all.
		if e2ee.Ciphertext == "" {
			t.Errorf("frame %d carries no ciphertext", i)
		}
		// The shape stays readable without a key, since that is what the receiver
		// keys its checks off (never the `event:` line, which is outside the AAD).
		if _, ok := frame["type"]; !ok {
			t.Errorf("frame %d must keep `type` cleartext", i)
		}
		if wantFinal := i == len(frames)-1; e2ee.Final != wantFinal {
			t.Errorf("frame %d final = %v, want %v (message_stop is guaranteed last)", i, e2ee.Final, wantFinal)
		}
	}

	// The router's billing inputs survive: input tokens inside message_start's
	// cleartext `message`, output tokens in message_delta's top-level `usage`.
	if !strings.Contains(string(frames[0]["message"]), `"input_tokens":11`) {
		t.Errorf("message_start must keep the input token count readable: %s", frames[0]["message"])
	}
	if !strings.Contains(string(frames[4]["usage"]), `"output_tokens":20`) {
		t.Errorf("message_delta must keep the output token count readable: %s", frames[4]["usage"])
	}
}

// An upstream that drops off without sending message_stop still has to leave the
// client a completion marker, and for this profile that marker is a message_stop
// event — a legal frame of the API, event line included, not a bare placeholder.
func TestAnthropicStreamSynthesizesMessageStopOnEOF(t *testing.T) {
	f := newE2EEFixture(t)
	ctx := newAnthropicGinCtx()
	if _, err := f.c.MaybeUnsealRequest(ctx, f.sealAnthropicRequest(t, []string{"messages", "system"})); err != nil {
		t.Fatalf("unseal: %v", err)
	}
	sealer, err := f.c.newResponseFrameSealer(ctx)
	if err != nil {
		t.Fatalf("newResponseFrameSealer: %v", err)
	}
	// Truncated: the stream stops after the delta.
	for _, line := range anthropicSSE[:9] {
		if _, err := sealer.sealSSELine(line); err != nil {
			t.Fatalf("sealSSELine(%q): %v", line, err)
		}
	}

	tail, err := sealer.finalFrameLine()
	if err != nil {
		t.Fatalf("finalFrameLine: %v", err)
	}
	if !strings.HasPrefix(tail, "event: message_stop\n") {
		t.Errorf("the synthetic terminal frame must be a well-formed message_stop event, got %q", tail)
	}
	payload, ok := strings.CutPrefix(strings.TrimSpace(strings.TrimPrefix(tail, "event: message_stop\n")), "data:")
	if !ok {
		t.Fatalf("no data line in %q", tail)
	}
	var frame wire.Response
	if err := json.Unmarshal([]byte(strings.TrimSpace(payload)), &frame); err != nil {
		t.Fatalf("synthetic frame is not JSON: %v", err)
	}
	e2ee, err := frame.E2EE()
	if err != nil {
		t.Fatalf("read _e2ee: %v", err)
	}
	if !e2ee.Final {
		t.Error("the synthetic terminal frame must be marked final")
	}
	if len(e2ee.SealedFields) != 0 {
		t.Errorf("message_stop seals nothing, got %v", e2ee.SealedFields)
	}
	var kind string
	if err := json.Unmarshal(frame["type"], &kind); err != nil || kind != "message_stop" {
		t.Errorf("synthetic frame type = %q (%v), want message_stop", kind, err)
	}
	// Calling again is a no-op, so the [DONE]-and-EOF double call cannot emit two.
	if again, err := sealer.finalFrameLine(); err != nil || again != "" {
		t.Errorf("a second final frame must not be emitted, got %q (%v)", again, err)
	}
}

// A turn that fails partway ends with `error` and sends no message_stop at all.
// That frame is terminal too, so it must carry `final` itself — and the EOF path
// must then add nothing. Marking it non-final instead would append a
// `message_stop` AFTER an `error`: a sequence no Anthropic stream produces, which
// reads to a client as a turn that completed normally.
func TestAnthropicStreamMarksAnErrorFrameFinal(t *testing.T) {
	f := newE2EEFixture(t)
	ctx := newAnthropicGinCtx()
	if _, err := f.c.MaybeUnsealRequest(ctx, f.sealAnthropicRequest(t, []string{"messages", "system"})); err != nil {
		t.Fatalf("unseal: %v", err)
	}
	sealer, err := f.c.newResponseFrameSealer(ctx)
	if err != nil {
		t.Fatalf("newResponseFrameSealer: %v", err)
	}
	// A turn that produced some output and then failed.
	for _, line := range anthropicSSE[:9] {
		if _, err := sealer.sealSSELine(line); err != nil {
			t.Fatalf("sealSSELine(%q): %v", line, err)
		}
	}
	const errLine = `data: {"type":"error","error":{"type":"overloaded_error","message":"upstream overloaded"}}` + "\n"
	sealedErr, err := sealer.sealSSELine(errLine)
	if err != nil {
		t.Fatalf("sealSSELine(error): %v", err)
	}
	if strings.Contains(sealedErr, "overloaded") {
		t.Errorf("the error payload must be sealed, not readable: %s", sealedErr)
	}

	payload, ok := strings.CutPrefix(strings.TrimSpace(sealedErr), "data:")
	if !ok {
		t.Fatalf("no data line in %q", sealedErr)
	}
	var frame wire.Response
	if err := json.Unmarshal([]byte(strings.TrimSpace(payload)), &frame); err != nil {
		t.Fatalf("sealed error frame: %v", err)
	}
	e2ee, err := frame.E2EE()
	if err != nil {
		t.Fatalf("read _e2ee: %v", err)
	}
	if !e2ee.Final {
		t.Error("`error` ends the stream, so it must be the frame carrying final")
	}
	if len(e2ee.SealedFields) != 1 || e2ee.SealedFields[0] != "error" {
		t.Errorf("sealed_fields = %v, want [error]", e2ee.SealedFields)
	}
	if tail, err := sealer.finalFrameLine(); err != nil || tail != "" {
		t.Errorf("nothing may follow a terminal frame, got %q (%v)", tail, err)
	}
}

// A frame whose shape declares a content field it does not carry is a malformed
// upstream frame. It must fail closed rather than be papered over with an empty
// placeholder: `delta` is an OBJECT, so `[]` there would be a type error shipped
// to the client, and an empty delta would silently swallow content.
func TestAnthropicStreamFailsClosedOnAContentFrameMissingItsPayload(t *testing.T) {
	f := newE2EEFixture(t)
	ctx := newAnthropicGinCtx()
	if _, err := f.c.MaybeUnsealRequest(ctx, f.sealAnthropicRequest(t, []string{"messages", "system"})); err != nil {
		t.Fatalf("unseal: %v", err)
	}
	sealer, err := f.c.newResponseFrameSealer(ctx)
	if err != nil {
		t.Fatalf("newResponseFrameSealer: %v", err)
	}

	for _, line := range []string{
		`data: {"type":"content_block_delta","index":0}` + "\n",  // declares delta, carries none
		`data: {"type":"thinking_block_delta","index":0}` + "\n", // a shape the taxonomy predates
	} {
		if _, err := sealer.sealSSELine(line); err == nil {
			t.Errorf("expected %q to fail closed", strings.TrimSpace(line))
		}
	}
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
