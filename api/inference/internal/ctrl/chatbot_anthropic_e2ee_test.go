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

	// A conforming client opens it and gets the answer back, with the cleartext
	// the router billed on still in place.
	opened, err := wire.OpenResponseFor(wire.ProfileAnthropic, f.clientEphSk, frame)
	if err != nil {
		t.Fatalf("OpenResponseFor: a conforming client must accept this response: %v", err)
	}
	if !strings.Contains(string(opened["content"]), "the secret answer") {
		t.Errorf("opened content = %s, want the sealed answer merged back", opened["content"])
	}
	if !strings.Contains(string(opened["usage"]), `"input_tokens":11`) {
		t.Errorf("opened usage = %s, want the cleartext token counts", opened["usage"])
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
	// The stream stays a well-formed SSE stream of this API: an `event:` line
	// beside every sealed data line. The upstream's own line was dropped — these
	// are rebuilt from each frame's bound `type` (see
	// TestAnthropicStreamRebuildsTheEventLine).
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

	// And the half that actually matters: a CONFORMING CLIENT accepts every frame
	// and recovers the answer. OpenFrame is where the receiver-side rules run —
	// the per-shape sealed set, protected `message`, the sealed/cleartext
	// collision check, the unbound-field rules — so without this the suite would
	// pass on a stream the broker seals plausibly and a third-party client
	// rejects.
	assembled := openAnthropicStream(t, f, frames)
	if !strings.Contains(assembled, "the secret answer") {
		t.Errorf("the client must recover the delta text, assembled: %s", assembled)
	}
}

// openAnthropicStream opens a sealed Anthropic stream the way a client does:
// one opener seeded with the first frame, then every frame in order,
// fail-closed. It returns the concatenation of the decrypted payloads.
func openAnthropicStream(t *testing.T, f *e2eeTestFixture, frames []wire.Response) string {
	t.Helper()
	ro, err := wire.NewResponseOpenerFor(wire.ProfileAnthropic, f.clientEphSk, frames[0])
	if err != nil {
		t.Fatalf("NewResponseOpenerFor: %v", err)
	}
	var assembled strings.Builder
	for i, fr := range frames {
		opened, err := ro.OpenFrame(fr)
		if err != nil {
			t.Fatalf("OpenFrame[%d]: a conforming client must accept every frame this broker seals: %v", i, err)
		}
		// The cleartext half survives the merge, which is what the router read.
		if _, ok := opened[anthropicFrameType]; !ok {
			t.Errorf("OpenFrame[%d]: the opened frame lost its `type`", i)
		}
		for _, sealed := range []string{"content", "content_block", "delta", "error"} {
			if v, ok := opened[sealed]; ok {
				assembled.Write(v)
			}
		}
	}
	return assembled.String()
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

	// The event line this broker emits comes from the frame's own bound `type`.
	if !strings.HasPrefix(sealedErr, "event: error\n") {
		t.Errorf("the sealed error event must be announced as `event: error`, got %q", sealedErr)
	}
	frame, ok := sealedFrameFrom(t, sealedErr)
	if !ok {
		t.Fatalf("no data line in %q", sealedErr)
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

// The SSE `event:` line is REBUILT from each frame's bound `type`, never
// forwarded. It sits outside the frame JSON and so outside the AAD, which is why
// §7.2 has a receiver ignore the received line and rebuild it — and why an
// upstream must not be able to write into it: everything a sealed frame's
// cleartext half may hold is checked by the profile taxonomy, and this line is
// checked by nothing (sanitizeStreamLine's leak-field stripping also only
// inspects `data:` JSON). Forwarding it would hand the router text in the clear
// on an otherwise sealed turn, and buy nothing.
func TestAnthropicStreamRebuildsTheEventLine(t *testing.T) {
	f := newE2EEFixture(t)
	ctx := newAnthropicGinCtx()
	if _, err := f.c.MaybeUnsealRequest(ctx, f.sealAnthropicRequest(t, []string{"messages", "system"})); err != nil {
		t.Fatalf("unseal: %v", err)
	}
	sealer, err := f.c.newResponseFrameSealer(ctx)
	if err != nil {
		t.Fatalf("newResponseFrameSealer: %v", err)
	}

	// An upstream event line — whatever it says — is dropped, not forwarded. So is
	// every other non-data field line: none is inside the AAD, none is checked by
	// anything, and none carries content a sealed receiver may act on.
	for _, line := range []string{
		"event: SECRET-ON-THE-EVENT-LINE\n",
		"event: content_block_delta\n", // even the correct one: it is not the source of truth
		"id: SECRET-ON-THE-ID-LINE\n",
		"retry: 10000\n",
		"unknown-field: SECRET\n",
	} {
		out, err := sealer.sealSSELine(line)
		if err != nil {
			t.Errorf("sealSSELine(%q): %v", strings.TrimSpace(line), err)
		}
		if out != "" {
			t.Errorf("a non-data SSE line must not be forwarded, got %q", out)
		}
	}

	// The one the client receives is derived from the frame's own bound `type`,
	// so it is inside the AAD and cannot disagree with the frame.
	out, err := sealer.sealSSELine(`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi"}}` + "\n")
	if err != nil {
		t.Fatalf("sealSSELine: %v", err)
	}
	if !strings.HasPrefix(out, "event: content_block_delta\ndata: {") {
		t.Errorf("the emitted event must be announced from its bound type, got %q", out)
	}
	frame, ok := sealedFrameFrom(t, out)
	if !ok {
		t.Fatalf("no sealed frame in %q", out)
	}
	var kind string
	if err := json.Unmarshal(frame["type"], &kind); err != nil || "event: "+kind+"\n" != strings.SplitAfter(out, "\n")[0] {
		t.Errorf("the event line and the bound type must agree: %q vs %q (%v)", out, kind, err)
	}
}

// A chat stream carries no `event:` lines, because its API sends none — the line
// is derived from a discriminator the chat profile's frames do not have.
func TestStreamFrameSealerChatEmitsNoEventLine(t *testing.T) {
	f := newE2EEFixture(t)
	ctx := newGinCtx()
	ctx.Set(CtxKeyE2EESealed, true)
	ctx.Set(CtxKeyE2EEProfile, wire.ProfileChat)
	ctx.Set(CtxKeyE2EEClientEphPub, f.clientEphPub)
	ctx.Set(CtxKeyE2EEReqBindHash, f.reqBindHash(t))
	sealer, err := f.c.newResponseFrameSealer(ctx)
	if err != nil {
		t.Fatalf("newResponseFrameSealer: %v", err)
	}
	// Including — especially — a chunk carrying a top-level `type`. On this profile
	// `type` is an ordinary cleartext field the wire package has no rule about, so
	// an event line derived from it would put an UNVALIDATED upstream string on the
	// wire ahead of the sealed frame; with a newline in it, a whole
	// attacker-chosen, unsealed, unbound `data:` frame — reopening, one branch
	// away, the channel that dropping the upstream's own `event:` line closes.
	for _, line := range []string{
		`data: {"id":"a","choices":[{"delta":{"content":"hi"}}]}` + "\n",
		`data: {"id":"a","choices":[{"delta":{"content":"hi"}}],"type":"benign"}` + "\n",
		`data: {"id":"a","choices":[{"delta":{"content":"real"}}],` +
			`"type":"evil\ndata: {\"choices\":[{\"delta\":{\"content\":\"injected\"}}]}\n"}` + "\n",
	} {
		out, err := sealer.sealSSELine(line)
		if err != nil {
			t.Fatalf("sealSSELine: %v", err)
		}
		if !strings.HasPrefix(out, "data: {") {
			t.Errorf("a chat frame must be emitted without an `event:` line, got %q", out)
		}
		// The invariant is about SSE LINES, not about the bytes appearing anywhere:
		// the smuggled newline stays a JSON escape inside the sealed frame's
		// cleartext `type`, which is a bound field like any other and rides through
		// exactly as it does on main. What must not happen is a new LINE.
		dataLines, eventLines := 0, 0
		for _, l := range strings.Split(out, "\n") {
			switch {
			case strings.HasPrefix(l, "data: "):
				dataLines++
			case strings.HasPrefix(l, "event:"):
				eventLines++
			case strings.TrimSpace(l) == "":
			default:
				t.Errorf("unexpected SSE line %q in %q", l, out)
			}
		}
		if dataLines != 1 || eventLines != 0 {
			t.Errorf("emitted %d data and %d event lines, want exactly 1 and 0: %q", dataLines, eventLines, out)
		}
	}
}

// A tool-use turn is the shape combination the text-only stream never exercises:
// the tool NAME and the model's arguments arrive in `content_block` and `delta`
// of the same shapes, so both must be sealed and both must open — a tool call
// leaking the function name and its arguments in the clear would be as bad as
// leaking prose.
func TestAnthropicStreamSealsAToolUseTurn(t *testing.T) {
	f := newE2EEFixture(t)
	ctx := newAnthropicGinCtx()
	if _, err := f.c.MaybeUnsealRequest(ctx, f.sealAnthropicRequest(t, []string{"messages", "system"})); err != nil {
		t.Fatalf("unseal: %v", err)
	}
	sealer, err := f.c.newResponseFrameSealer(ctx)
	if err != nil {
		t.Fatalf("newResponseFrameSealer: %v", err)
	}

	toolUseSSE := []string{
		"event: message_start\n",
		`data: {"type":"message_start","message":{"id":"msg_2","type":"message","role":"assistant","model":"claude-x","content":[],"usage":{"input_tokens":9,"output_tokens":1}}}` + "\n",
		"event: content_block_start\n",
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_1","name":"transfer_funds","input":{}}}` + "\n",
		"event: content_block_delta\n",
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"to\":\"0xSECRET\""}}` + "\n",
		"event: content_block_stop\n",
		`data: {"type":"content_block_stop","index":0}` + "\n",
		"event: message_delta\n",
		`data: {"type":"message_delta","delta":{"stop_reason":"tool_use","stop_sequence":null},"usage":{"output_tokens":14}}` + "\n",
		"event: message_stop\n",
		`data: {"type":"message_stop"}` + "\n",
	}

	var frames []wire.Response
	var joined strings.Builder
	for _, line := range toolUseSSE {
		out, err := sealer.sealSSELine(line)
		if err != nil {
			t.Fatalf("sealSSELine(%q): %v", line, err)
		}
		joined.WriteString(out)
		if fr, ok := sealedFrameFrom(t, out); ok {
			frames = append(frames, fr)
		}
	}
	for _, secret := range []string{"transfer_funds", "0xSECRET", "input_json_delta", "tool_use\"", "toolu_1"} {
		if strings.Contains(joined.String(), secret) {
			t.Errorf("%q rode in the clear:\n%s", secret, joined.String())
		}
	}
	// `stop_reason: "tool_use"` is model-produced and deliberately NOT sealed
	// (§7.2 rule 6 covers `stop_sequence`, the caller's own input), but it lives
	// inside message_delta's sealed `delta`, so it is not readable here either.
	assembled := openAnthropicStream(t, f, frames)
	for _, want := range []string{"transfer_funds", "0xSECRET"} {
		if !strings.Contains(assembled, want) {
			t.Errorf("the client must recover %q, assembled: %s", want, assembled)
		}
	}
}

// The only path where the taxonomy's per-shape "seal if present" fires, and so
// the only place the sealed set varies WITHIN one shape: a `message_delta`
// echoing back a custom stop string the CALLER supplied in `stop_sequences`. It
// rides inside the sealed `delta` here, which is what keeps a request that
// deliberately sealed `stop_sequences` from getting the same value back in the
// clear.
func TestAnthropicStreamSealsANonNullStopSequence(t *testing.T) {
	f := newE2EEFixture(t)
	ctx := newAnthropicGinCtx()
	if _, err := f.c.MaybeUnsealRequest(ctx, f.sealAnthropicRequest(t, []string{"messages", "system"})); err != nil {
		t.Fatalf("unseal: %v", err)
	}
	sealer, err := f.c.newResponseFrameSealer(ctx)
	if err != nil {
		t.Fatalf("newResponseFrameSealer: %v", err)
	}

	const marker = "CLIENT-SECRET-MARKER"
	lines := []string{
		`data: {"type":"message_start","message":{"id":"msg_3","type":"message","role":"assistant","model":"claude-x","content":[],"usage":{"input_tokens":7,"output_tokens":1}}}` + "\n",
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi"}}` + "\n",
		`data: {"type":"message_delta","delta":{"stop_reason":"stop_sequence","stop_sequence":"` + marker + `"},"usage":{"output_tokens":3}}` + "\n",
		`data: {"type":"message_stop"}` + "\n",
	}
	var frames []wire.Response
	var joined strings.Builder
	for _, line := range lines {
		out, err := sealer.sealSSELine(line)
		if err != nil {
			t.Fatalf("sealSSELine(%q): %v", line, err)
		}
		joined.WriteString(out)
		if fr, ok := sealedFrameFrom(t, out); ok {
			frames = append(frames, fr)
		}
	}
	if strings.Contains(joined.String(), marker) {
		t.Errorf("the caller's own stop string rode back in the clear:\n%s", joined.String())
	}
	// The count the router bills on is still readable beside it.
	if !strings.Contains(string(frames[2]["usage"]), `"output_tokens":3`) {
		t.Errorf("message_delta must keep its cleartext usage: %s", frames[2]["usage"])
	}
	if got := openAnthropicStream(t, f, frames); !strings.Contains(got, marker) {
		t.Errorf("the client must recover the stop string, assembled: %s", got)
	}
}

// sealedFrameFrom parses the sealed frame out of one emitted SSE event, which is
// an `event:` line plus a `data:` line for a frame-typed profile — hence the line
// scan. ok=false for output that carries no frame at all (a dropped line).
func sealedFrameFrom(t *testing.T, out string) (wire.Response, bool) {
	t.Helper()
	for _, line := range strings.Split(out, "\n") {
		payload, ok := strings.CutPrefix(strings.TrimSpace(line), "data:")
		if !ok || !strings.HasPrefix(strings.TrimSpace(payload), "{") {
			continue
		}
		var fr wire.Response
		if err := json.Unmarshal([]byte(strings.TrimSpace(payload)), &fr); err != nil {
			t.Fatalf("sealed frame is not JSON: %v", err)
		}
		return fr, true
	}
	return nil, false
}

// §7 puts the final frame last, and with a frame-typed profile a terminal event
// can land mid-stream — so an upstream CAN send a data frame behind it (a proxy
// that appends `message_stop` after `error`, or duplicates it). Sealing that
// frame would fold it into the §8 streaming binding, and since a client stops
// consuming at the frame marked final, the client would recompute the binding
// over N frames while the broker signed N+1: signature failure on a turn that
// otherwise succeeded. So it is refused, and refused BEFORE sealing, which is
// what keeps the binding equal to what the client received.
func TestAnthropicStreamHandlesADataFrameAfterTheTerminalFrame(t *testing.T) {
	f := newE2EEFixture(t)
	ctx := newAnthropicGinCtx()
	if _, err := f.c.MaybeUnsealRequest(ctx, f.sealAnthropicRequest(t, []string{"messages", "system"})); err != nil {
		t.Fatalf("unseal: %v", err)
	}
	sealer, err := f.c.newResponseFrameSealer(ctx)
	if err != nil {
		t.Fatalf("newResponseFrameSealer: %v", err)
	}
	for _, line := range anthropicSSE { // ends with message_stop, the terminal event
		if _, err := sealer.sealSSELine(line); err != nil {
			t.Fatalf("sealSSELine(%q): %v", line, err)
		}
	}
	// What the client verifies §8 over: the frames up to and including `final`.
	atFinal, ok, err := sealer.signedText()
	if err != nil || !ok {
		t.Fatalf("signedText: ok=%v err=%v", ok, err)
	}
	boundAtFinal := sealer.frameCount

	// Lines that legitimately trail a terminal frame must not fail the stream: a
	// real Anthropic stream ends its last event with a blank line, and an
	// OpenAI-compatible shim may append [DONE]. (An `event:` line is dropped
	// rather than forwarded, here as everywhere.)
	for _, line := range []string{"\n", "event: message_stop\n", "data: [DONE]\n"} {
		if _, err := sealer.sealSSELine(line); err != nil {
			t.Errorf("sealSSELine(%q) must not fail the stream after the final frame: %v", strings.TrimSpace(line), err)
		}
	}

	// A data frame carrying no answer — a duplicate terminal event, or any shape
	// that seals nothing — is DROPPED, not sealed and not failed. That is the case
	// seen in the wild (a proxy that appends `message_stop` after `error`, or
	// sends it twice), and the client already has a complete final frame. Failing
	// would be worse than the quirk: the stream is committed and flushed, so the
	// error path appends a JSON error body behind the sealed final frame and
	// reports a fully delivered turn as a broker error.
	for _, line := range []string{
		`data: {"type":"message_stop"}` + "\n", // duplicate terminal event
		`data: {"type":"ping"}` + "\n",         // seals nothing
	} {
		out, err := sealer.sealSSELine(line)
		if err != nil {
			t.Errorf("sealSSELine(%q) should be dropped, not fail the stream: %v", strings.TrimSpace(line), err)
		}
		if out != "" {
			t.Errorf("sealSSELine(%q) must emit nothing, got %q", strings.TrimSpace(line), out)
		}
	}

	// A frame CARRYING CONTENT behind the final one is the case where dropping
	// loses data — something the client would never see — so the stream stops.
	// Being TERMINAL is not an exemption: `error` is both terminal and
	// content-bearing, and a trailing one reports a real downstream failure that
	// must not be swallowed (a "terminal frames are droppable" shortcut did
	// exactly that, and logged that it "carries no answer").
	for _, line := range []string{
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"late"}}` + "\n",
		`data: {"type":"error","error":{"type":"overloaded_error","message":"downstream died"}}` + "\n",
	} {
		if _, err := sealer.sealSSELine(line); err == nil {
			t.Errorf("a content-bearing frame after the terminal frame must fail the stream: %q", strings.TrimSpace(line))
		}
	}
	// So does one whose shape is unknown, since it might carry content.
	if _, err := sealer.sealSSELine(`data: {"type":"thinking_block_delta","index":0}` + "\n"); err == nil {
		t.Error("an unknown shape after the terminal frame must fail the stream")
	}

	// Whichever way it went, nothing after the final frame reached the §8
	// binding, so the signature the broker caches is the one the client — which
	// stopped at `final` — recomputes.
	after, _, err := sealer.signedText()
	if err != nil {
		t.Fatalf("signedText: %v", err)
	}
	if sealer.frameCount != boundAtFinal {
		t.Errorf("bound %d frames, want %d: no post-final frame may be folded into the binding", sealer.frameCount, boundAtFinal)
	}
	if after != atFinal {
		t.Errorf("the signed aggregate changed after the final frame:\n  at final: %s\n  after:    %s", atFinal, after)
	}
}

// The sealed set production actually produces on the non-streaming path. The
// Messages API always returns a top-level `stop_sequence` on a `message`
// response — null, or the custom string that stopped generation — so the
// taxonomy's per-shape "seal if present" fires on virtually every real response
// and the set is [content stop_sequence], not [content]. It is also the one
// place the sealed set varies WITHIN a shape on the path the router bills, and
// the value is the CALLER's own input echoed back, so it must not come back in
// the clear to a request that sealed it.
func TestAnthropicNonStreamResponseSealsStopSequenceWhenPresent(t *testing.T) {
	const marker = "CLIENT-SECRET-MARKER"
	tests := []struct {
		name       string
		stopFields string
		wantSealed []string
	}{
		{
			name:       "a matched custom stop string",
			stopFields: `"stop_reason":"stop_sequence","stop_sequence":"` + marker + `",`,
			wantSealed: []string{"content", "stop_sequence"},
		},
		{
			// Present-but-null is what an ordinary turn returns, and JSON null is
			// still PRESENT, so it is sealed too — which is why the production set
			// is two fields whether or not a stop string matched.
			name:       "present but null",
			stopFields: `"stop_reason":"end_turn","stop_sequence":null,`,
			wantSealed: []string{"content", "stop_sequence"},
		},
		{
			// Absent — the shape this suite's other cases use, kept to pin that the
			// rule is conditional rather than always-on.
			name:       "absent",
			stopFields: `"stop_reason":"end_turn",`,
			wantSealed: []string{"content"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newE2EEFixture(t)
			ctx := newAnthropicGinCtx()
			if _, err := f.c.MaybeUnsealRequest(ctx, f.sealAnthropicRequest(t, []string{"messages", "system"})); err != nil {
				t.Fatalf("unseal: %v", err)
			}
			provider := `{"id":"msg_1","type":"message","role":"assistant","model":"claude-x",` +
				tt.stopFields +
				`"usage":{"input_tokens":11,"output_tokens":20},` +
				`"content":[{"type":"text","text":"the secret answer"}]}`

			out, _, _, err := f.c.maybeSealNonStreamResponse(ctx, []byte(provider))
			if err != nil {
				t.Fatalf("seal response: %v", err)
			}
			if strings.Contains(string(out), marker) {
				t.Errorf("the caller's own stop string rode back in the clear: %s", out)
			}

			var frame wire.Response
			if err := json.Unmarshal(out, &frame); err != nil {
				t.Fatalf("sealed frame: %v", err)
			}
			e2ee, err := frame.E2EE()
			if err != nil {
				t.Fatalf("read _e2ee: %v", err)
			}
			if !sameStrings(e2ee.SealedFields, tt.wantSealed) {
				t.Errorf("sealed_fields = %v, want %v", e2ee.SealedFields, tt.wantSealed)
			}
			if _, ok := frame["stop_sequence"]; ok && len(tt.wantSealed) > 1 {
				t.Error("`stop_sequence` must be sealed away, not left cleartext")
			}
			// `stop_reason` is model-produced with no caller input in it, and the
			// router reads it, so it deliberately stays cleartext (§7.2 rule 6).
			if _, ok := frame["stop_reason"]; !ok {
				t.Error("`stop_reason` must stay cleartext for the router")
			}

			// And a conforming client gets both back.
			opened, err := wire.OpenResponseFor(wire.ProfileAnthropic, f.clientEphSk, frame)
			if err != nil {
				t.Fatalf("OpenResponseFor: %v", err)
			}
			if !strings.Contains(string(opened["content"]), "the secret answer") {
				t.Errorf("opened content = %s", opened["content"])
			}
			if len(tt.wantSealed) > 1 && !strings.Contains(string(opened["stop_sequence"]), "null") &&
				!strings.Contains(string(opened["stop_sequence"]), marker) {
				t.Errorf("opened stop_sequence = %s, want the sealed value merged back", opened["stop_sequence"])
			}
		})
	}
}

// The non-streaming shape's `content` IS an array, which makes it the one
// Anthropic field an empty-array placeholder would technically fit — and it must
// still fail closed. The Messages API always returns `content` on a `message`
// response (an empty array at worst), so a placeholder could only ever fire on a
// broken upstream, and it would then seal, sign and mark final a frame carrying
// an empty answer while the router bills the output tokens that same response
// reported. Nothing anywhere would report a problem.
func TestAnthropicNonStreamResponseFailsClosedWithoutContent(t *testing.T) {
	f := newE2EEFixture(t)
	ctx := newAnthropicGinCtx()
	if _, err := f.c.MaybeUnsealRequest(ctx, f.sealAnthropicRequest(t, []string{"messages", "system"})); err != nil {
		t.Fatalf("unseal: %v", err)
	}

	// A well-formed-looking response that reports 20 output tokens and carries no
	// content at all.
	const provider = `{"id":"msg_1","type":"message","role":"assistant","model":"claude-x",` +
		`"stop_reason":"end_turn","usage":{"input_tokens":11,"output_tokens":20}}`

	out, isSealed, _, err := f.c.maybeSealNonStreamResponse(ctx, []byte(provider))
	if !isSealed {
		t.Fatal("expected isSealed=true for a sealed request")
	}
	if err == nil {
		t.Fatalf("expected a malformed response to fail closed, got a sealed frame: %s", out)
	}
	if out != nil {
		t.Error("a failed seal must return no body: the caller must not forward plaintext")
	}
	if !strings.Contains(err.Error(), "content") {
		t.Errorf("error should name the field that was missing, got: %v", err)
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
