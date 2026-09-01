package ctrl

// This file implements the provider (enclave) side of the 0g-pc end-to-end
// encryption protocol (SPEC.md §5–§7): unsealing the sensitive request fields a
// client sealed to this enclave's HPKE key, and sealing the sensitive response
// fields back to the client's ephemeral key. The wire format and crypto are
// provided by github.com/0gfoundation/0g-pc-e2ee/protocol (imported byte-for-byte);
// this layer wires them into the broker's proxy/billing/signing path and adds
// the broker-specific policy checks the protocol package deliberately leaves to
// the caller.

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"

	pccrypto "github.com/0gfoundation/0g-pc-e2ee/protocol/crypto"
	"github.com/0gfoundation/0g-pc-e2ee/protocol/proof"
	"github.com/0gfoundation/0g-pc-e2ee/protocol/wire"
	"github.com/gin-gonic/gin"

	"github.com/0glabs/0g-serving-broker/common/errors"
	"github.com/0glabs/0g-serving-broker/common/log"
)

// ErrE2EEKeyMismatch marks a sealed request whose key_id is not the enclave's
// current enc key — e.g. after a provider upgrade rotated the measurement-tied
// key while the router/client still hold the old one. It is a RETRIABLE,
// self-healing condition (the client should re-fetch + re-verify the enc key and
// re-seal), distinct from a hard fail-closed unseal failure (tampered AAD, bad
// envelope, unusable ephemeral key). The proxy maps it to HTTP 409 with a
// machine-recognizable "e2ee_key_mismatch" message prefix so the router
// (0g-router#618) re-syncs this provider rather than bouncing a 4xx to the user;
// all other unseal failures stay 400.
var ErrE2EEKeyMismatch = errors.New("e2ee_key_mismatch")

const (
	// e2eeBodyMarker is the reserved top-level field carrying the sealing metadata
	// (SPEC §5). A request is treated as sealed iff it has this as a top-level JSON
	// key — matching the router, which routes on the body field, not a header.
	// (A header signal may be added later; the field is the source of truth today.)
	e2eeBodyMarker = "_e2ee"

	// clientEphPubLen is the byte length of the client's response ephemeral X25519
	// public key (SPEC §3 suite).
	clientEphPubLen = 32

	// CtxKeyE2EESealed marks (bool) that the current request arrived sealed, so the
	// response path knows to seal its reply.
	CtxKeyE2EESealed = "e2eeSealed"
	// CtxKeyE2EEClientEphPub holds the client's response ephemeral X25519 public
	// key (pccrypto.PublicKey) extracted from the request envelope (SPEC §7).
	CtxKeyE2EEClientEphPub = "e2eeClientEphPub"
	// CtxKeyE2EEPlaintextReq holds the reconstructed plaintext request bytes
	// ([]byte) captured immediately after unsealing, BEFORE the proxy's upstream
	// rewrites (model enforcement, stream_options injection, …). Retained for
	// observability/audit; the §8 signature no longer binds plaintext (see
	// CtxKeyE2EEReqBindHash).
	CtxKeyE2EEPlaintextReq = "e2eePlaintextReq"
	// CtxKeyE2EEReqBindHash holds the §8 request binding hash ([32]byte =
	// proof.FrameBindingHash of the sealed request: sha256(sha256(aad)‖sha256(ct)))
	// captured at unseal time. The response signature binds the on-wire
	// ciphertext, but the proxy replaces the sealed request with its plaintext
	// before forwarding, so the response path can no longer recompute it — the
	// binding is stashed here and combined with the response hash at sign time.
	CtxKeyE2EEReqBindHash = "e2eeReqBindHash"
)

// e2eeResponseUnboundFields are declared in every sealed response's
// `unbound_fields` (SPEC §5.2). They are cleartext fields EXCLUDED from the seal
// AAD, so the router may inject or rewrite them on the way back to the client
// (broker → router → client) without breaking the client's Open — they are not
// covered by the §8 signature. Per the §8 corollary a router-injected value is
// not cryptographically trusted (trust comes from on-chain settlement), so these
// MUST stay unbound rather than being bound/signed fields:
//   - "model": the router substitutes the served model back to the alias the
//     client requested, so it must be rewritable without invalidating the seal.
//   - "x_0g_trace": observability metadata the router injects downstream.
var e2eeResponseUnboundFields = []string{"model", "x_0g_trace"}

// e2eeSensitiveResponseFields lists every top-level response field that can carry
// model output on a surface this broker serves. The wire v1 default sealed set is
// ["choices"], which covers only the OpenAI shape; the broker also serves the
// Anthropic Messages surface as a first-class path (service.supportedFormats
// accepts "anthropic", IsAnthropicEndpoint matches /messages and /v1/messages),
// and an Anthropic response carries its output in "content" — or, per streaming
// event, in "message" / "content_block" / "delta" — and has no "choices" at all.
// Sealing the v1 default against such a frame therefore sealed nothing and
// forwarded the completion in the clear inside an envelope that reported itself
// sealed.
//
// The set is deliberately a union across surfaces rather than a per-format table:
// the sealed set is computed per frame from the fields actually present
// (e2eeSealedFieldsFor), so one list stays correct for a response that mixes
// shapes and needs no format detection on the sealing path.
//
// Anything added here MUST be a field whose value is model output or output
// metadata, never a routing/billing field — a sealed field is removed from the
// cleartext frame, so sealing "model" or "usage" would hide what the router and
// the billing path read.
var e2eeSensitiveResponseFields = []string{
	"choices",       // OpenAI /v1/chat/completions
	"content",       // Anthropic /v1/messages, non-streaming
	"message",       // Anthropic streaming: message_start
	"content_block", // Anthropic streaming: content_block_start
	"delta",         // Anthropic streaming: content_block_delta, message_delta
}

// streamDoneSentinel is the payload of the OpenAI SSE terminator, compared after
// the "data:" prefix is stripped and the payload trimmed so every spacing variant
// an upstream might emit is recognised.
const streamDoneSentinel = "[DONE]"

// e2eeResponseControlFields are the keys a frame may carry while carrying no
// model output. Everything here is transport, identity, routing or billing
// metadata, and none of it is the completion.
//
// This list is what keeps a field in CLEARTEXT. Everything in a frame that is
// neither here nor in e2eeSensitiveResponseFields is sealed (see
// e2eeSealedFieldsFor), so the default for an unknown key is confidentiality.
//
// Adding a key here therefore asserts it never carries model output, nor anything
// derived from the request, on any surface we forward. Getting that wrong forwards
// it in cleartext beside a sealed frame, which is the vulnerability rather than a
// cosmetic issue: Azure OpenAI's first streaming chunk is choices:[] beside
// prompt_filter_results, and those results are derived from the user's prompt. They
// are sealed precisely because they are not on this list.
//
// Adding a key here asserts it never carries model output on any surface we
// forward. Getting that wrong forwards a completion in cleartext, so add only
// what has been checked against the surface that emits it.
var e2eeResponseControlFields = map[string]bool{
	// OpenAI /v1/chat/completions, streaming and not
	"id":                 true,
	"object":             true,
	"created":            true,
	"system_fingerprint": true,
	"service_tier":       true,
	// Anthropic /v1/messages streaming
	"type":  true,
	"index": true,
	// Anthropic /v1/messages non-streaming, alongside the sealed "content"
	"role":          true,
	"stop_reason":   true,
	"stop_sequence": true,
	// Both surfaces
	"model": true,
	"usage": true,
	// Declared in e2eeResponseUnboundFields, i.e. a frame is expected to be able to
	// carry it and the router may rewrite it. The two lists have to agree.
	"x_0g_trace": true,
	// Deliberately NOT here: "error". Gateway error text is not guaranteed to be
	// prompt-free — LiteLLM and OpenRouter embed the upstream's raw error body,
	// which can quote the request ("prompt too long: '…'") — so it is sealed like any
	// other unrecognised field rather than forwarded in the clear. The client still
	// gets the diagnostics; it opens the frame either way.
}

// e2eeSealedFieldsFor returns every field of frame that must be sealed: the known
// output fields it carries, in the fixed order of e2eeSensitiveResponseFields so a
// client sees a deterministic sealed_fields list, then every remaining field that
// is not known cleartext metadata, sorted.
//
// An unrecognised field is SEALED, not refused. Refusing it was the obvious
// reading of "fail closed" and it is the wrong one: vLLM — this broker's primary
// upstream — serialises through model_dump(), so prompt_logprobs and
// kv_transfer_params are on every body, and refusing turned every sealed request
// into a 502. Sealing keeps the confidentiality that mattered (the client merges
// the field back when it opens the frame) without betting availability on a list
// of upstream quirks being complete. Refusal is reserved for what cannot be sealed
// at all: a body or line that is not a JSON object.
//
// An empty result means the frame carries nothing but cleartext metadata —
// legitimate for a streaming keepalive, a usage-only trailer or an Anthropic
// content_block_stop / message_stop, and a fail-closed condition on the
// non-streaming path where a completion always carries output.
func e2eeSealedFieldsFor(frame wire.Response) []string {
	fields := make([]string, 0, len(frame))
	for _, f := range e2eeSensitiveResponseFields {
		if _, ok := frame[f]; ok {
			fields = append(fields, f)
		}
	}
	var unknown []string
	for k := range frame {
		if e2eeResponseControlFields[k] || slices.Contains(e2eeSensitiveResponseFields, k) {
			continue
		}
		unknown = append(unknown, k)
	}
	slices.Sort(unknown)
	return append(fields, unknown...)
}

// hasE2EEMarker is a cheap substring pre-check to skip the JSON parse on the vast
// majority of (non-sealed) requests. A match is not proof of a sealed request —
// the substring could appear inside message content — so MaybeUnsealRequest
// confirms a genuine top-level "_e2ee" key before committing to fail-closed.
func hasE2EEMarker(reqBody []byte) bool {
	return bytes.Contains(reqBody, []byte(e2eeBodyMarker))
}

// MaybeUnsealRequest unseals a sealed E2EE request in-enclave and returns the
// reconstructed plaintext body to forward upstream. A request is sealed iff it
// carries a top-level "_e2ee" object (SPEC §5); any other request (including one
// that merely contains the substring "_e2ee" inside its content) is returned
// unchanged.
//
// On success for a sealed request it stashes, on the gin context, that the
// request was sealed and the client's response ephemeral key, so the response
// path seals its reply (SPEC §7). Once a request is confirmed sealed, any failure
// is returned as an error and MUST be treated as fail-closed by the caller (no
// plaintext fallback, SPEC §6) — a sealed request that cannot be opened, whose
// signer_addr is not this enclave, or whose key_id is unknown is rejected.
func (c *Ctrl) MaybeUnsealRequest(ctx *gin.Context, reqBody []byte) ([]byte, error) {
	if !hasE2EEMarker(reqBody) {
		return reqBody, nil
	}

	var env wire.Request
	if err := json.Unmarshal(reqBody, &env); err != nil {
		// Not a JSON object → cannot be a sealed envelope; forward unchanged.
		return reqBody, nil
	}
	if _, ok := env[e2eeBodyMarker]; !ok {
		// The substring matched inside content, not a real envelope; not sealed.
		return reqBody, nil
	}

	// Confirmed sealed from here on: fail-closed on any error.
	if len(c.teeService.EncPrivateKey) == 0 {
		return nil, fmt.Errorf("received a sealed request but the enclave enc key is not available")
	}
	e2ee, err := env.E2EE()
	if err != nil {
		return nil, fmt.Errorf("sealed request has a malformed %q envelope: %w", e2eeBodyMarker, err)
	}

	// Select the enc key by key_id (SPEC §6). The broker holds a single current
	// enc key; a mismatch means the client sealed to a rotated/foreign key we
	// cannot open, so reject with a clear error rather than a raw HPKE failure.
	if err := c.verifyEncKeyID(e2ee.KeyID); err != nil {
		return nil, err
	}

	// Enforce provider pinning (SPEC §5/§6): the enclave rejects a request pinned
	// to a different provider. The pin is the provider's TEE signer address
	// (renamed provider_id → signer_addr upstream in 0g-pc-e2ee #17; same value).
	// OpenRequest deliberately does not check this — the broker knows its own identity.
	if !strings.EqualFold(e2ee.SignerAddr, c.teeService.Address.Hex()) {
		return nil, fmt.Errorf("sealed request signer_addr %q does not match this enclave", e2ee.SignerAddr)
	}

	// Extract the client's response ephemeral key before opening, so the response
	// path can seal even though the field lives in the (now consumed) envelope.
	// Validate its length here, BEFORE the request is forwarded upstream: an
	// invalid key only breaks response sealing, which happens after inference has
	// already run — so a malformed key would otherwise buy free (unbilled) compute
	// and fail closed only at seal time. Reject it fail-closed pre-inference.
	clientEphPub, err := base64.RawURLEncoding.DecodeString(e2ee.ClientEphPub)
	if err != nil {
		return nil, fmt.Errorf("sealed request has invalid client_eph_pub: %w", err)
	}
	if len(clientEphPub) != clientEphPubLen {
		return nil, fmt.Errorf("sealed request client_eph_pub must be %d bytes (X25519), got %d", clientEphPubLen, len(clientEphPub))
	}
	// Length alone is not enough: a 32-byte value can still be a low-order/invalid
	// X25519 point that only fails at response-seal time (post-inference), which
	// would buy free unbilled compute. Probe HPKE setup now — fail closed here,
	// before forwarding upstream. The probe sealer is discarded; the response path
	// creates its own.
	if _, err := wire.NewResponseSealer(pccrypto.PublicKey(clientEphPub)); err != nil {
		return nil, fmt.Errorf("sealed request client_eph_pub is not a usable X25519 key: %w", err)
	}

	// §8 request binding: hash the on-wire aad‖ciphertext of the sealed request
	// NOW, while we still hold the envelope. The proxy replaces reqBody with the
	// reconstructed plaintext before forwarding upstream, so the response-signing
	// path (which runs post-inference) can no longer see these bytes. Stash the
	// 32-byte binding so signChatE2EE can combine it with the response hash.
	reqBindHash, err := proof.FrameBindingHash(env)
	if err != nil {
		return nil, fmt.Errorf("compute e2ee request binding: %w", err)
	}

	// Open (verifies v/kem_id, recomputes AAD, HPKE-Open fail-closed, checks
	// decrypted keys == sealed_fields with no cleartext collision, and reconstructs
	// the original request = cleartext ∪ decrypted). SPEC §6.
	reconstructed, err := wire.OpenRequest(c.teeService.EncPrivateKey, env)
	if err != nil {
		return nil, fmt.Errorf("unseal request: %w", err)
	}

	plaintext, err := json.Marshal(reconstructed)
	if err != nil {
		return nil, fmt.Errorf("re-encode unsealed request: %w", err)
	}

	ctx.Set(CtxKeyE2EESealed, true)
	ctx.Set(CtxKeyE2EEClientEphPub, pccrypto.PublicKey(clientEphPub))
	ctx.Set(CtxKeyE2EEPlaintextReq, plaintext)
	ctx.Set(CtxKeyE2EEReqBindHash, reqBindHash)
	c.logger.Debugf("E2EE: unsealed request (sealed_fields=%v, key_id=%s)", e2ee.SealedFields, e2ee.KeyID)
	return plaintext, nil
}

// verifyEncKeyID checks that a request's key_id (base64url) selects this
// enclave's current enc key (SPEC §4.3/§6). A mismatch returns an error wrapping
// ErrE2EEKeyMismatch (→ retriable 409). The current key_id is included as a
// NON-authoritative hint (a public hash, not the key material and not a trust
// source): the client must re-fetch and verify the enc key, not trust this value.
func (c *Ctrl) verifyEncKeyID(b64KeyID string) error {
	want := base64.RawURLEncoding.EncodeToString(c.teeService.KeyID)
	if b64KeyID != want {
		return fmt.Errorf("%w: sealed request key_id %q is not the enclave's current enc key (current %q); re-fetch the enc key and re-seal", ErrE2EEKeyMismatch, b64KeyID, want)
	}
	return nil
}

// e2eeSealedRequest reports whether the current request was unsealed (so the
// response must be sealed), returning the client's response ephemeral key.
func e2eeSealedRequest(ctx *gin.Context) (pccrypto.PublicKey, bool) {
	sealed, _ := ctx.Get(CtxKeyE2EESealed)
	if b, ok := sealed.(bool); !ok || !b {
		return nil, false
	}
	v, ok := ctx.Get(CtxKeyE2EEClientEphPub)
	if !ok {
		return nil, false
	}
	pub, ok := v.(pccrypto.PublicKey)
	if !ok || len(pub) == 0 {
		return nil, false
	}
	return pub, true
}

// e2eePlaintextRequest returns the reconstructed plaintext request captured at
// unseal time, used as the request side of the §8 content binding.
func e2eePlaintextRequest(ctx *gin.Context) ([]byte, bool) {
	v, ok := ctx.Get(CtxKeyE2EEPlaintextReq)
	if !ok {
		return nil, false
	}
	b, ok := v.([]byte)
	return b, ok && len(b) > 0
}

// e2eeReqBindHash returns the §8 request binding hash (sha256 of the sealed
// request's aad‖ciphertext) captured at unseal time, used as the request half of
// the E2EE response signature.
func e2eeReqBindHash(ctx *gin.Context) ([32]byte, bool) {
	v, ok := ctx.Get(CtxKeyE2EEReqBindHash)
	if !ok {
		return [32]byte{}, false
	}
	h, ok := v.([32]byte)
	return h, ok
}

// maybeSealNonStreamResponse seals the output fields a non-streaming response
// actually carries, plus anything it carries that is not known cleartext metadata
// (e2eeSealedFieldsFor), of
// a non-streaming response (SPEC §7) when the request was sealed; otherwise it
// returns body unchanged with sealed=false. Fail-closed: when the request was
// sealed but sealing fails, it returns an error and the caller MUST NOT forward
// the plaintext body.
func (c *Ctrl) maybeSealNonStreamResponse(ctx *gin.Context, body []byte) (out []byte, sealed bool, respBindHash [32]byte, err error) {
	ephPub, isSealed := e2eeSealedRequest(ctx)
	if !isSealed {
		return body, false, respBindHash, nil
	}
	var resp wire.Response
	// A literal JSON `null` unmarshals into a nil map WITHOUT error, and a nil map
	// has no fields to seal, so without this it would reach the fail-closed branch
	// below and be reported as an unrecognised shape rather than as what it is.
	if uerr := json.Unmarshal(body, &resp); uerr != nil || resp == nil {
		// 502 like the other unsealable-shape refusals below: an upstream that
		// answered 200 with a body that is not a JSON object is not the caller's
		// fault, and a 400 would file it under client error in the health accounting.
		return nil, true, respBindHash, errors.NewHTTPError(http.StatusBadGateway,
			fmt.Errorf("seal response: body is not a JSON object"))
	}
	// Seal whatever output fields this response actually carries rather than the
	// wire v1 default ["choices"], which is empty on the Anthropic surface. A
	// non-streaming completion always carries output, so an empty set means we do
	// not recognise the shape — fail closed instead of forwarding a body whose
	// payload we could not identify (and which the old code shipped in cleartext
	// beside an injected "choices":[]).
	sealedFields := e2eeSealedFieldsFor(resp)
	if len(sealedFields) == 0 {
		// Reachable only when EVERY key is on the cleartext allowlist, e.g.
		// {"id","model","usage"} — an unknown key is sealed now, not refused, so an
		// unrecognised body shape no longer arrives here. What is left is a
		// non-streaming completion that carries no output at all, which no upstream
		// should ever produce.
		//
		// 502, not the default 400: the fault is ours and the upstream's, never the
		// caller's, and a 400 would file it under client error in the rejection and
		// health accounting, which is exactly where nobody would look for it.
		// errors.Response also logs 5xx at Error level, so it is loud rather than
		// silent.
		//
		// One consequence worth being deliberate about: a gateway that returns
		// {"error":…} under a 200 is now sealed and delivered as a 200, because
		// "error" is not on the cleartext allowlist (its text can quote the prompt).
		// A client that only calls OpenResponse on completion-shaped bodies sees an
		// opaque _e2ee blob where it used to read the message. Sealing it is still the
		// right call — the alternative leaks — but the client has to open it.
		return nil, true, respBindHash, errors.NewHTTPError(http.StatusBadGateway, fmt.Errorf(
			"seal response: no sealable output field in response (looked for %v)", e2eeSensitiveResponseFields))
	}
	// model + x_0g_trace stay unbound so the router may rewrite/inject them
	// downstream (SPEC §5.2).
	frame, err := wire.SealResponse(ephPub, resp, sealedFields, e2eeResponseUnboundFields...)
	if err != nil {
		return nil, true, respBindHash, fmt.Errorf("seal response: %w", err)
	}
	// §8 response binding over the exact sealed frame the client receives.
	respBindHash, err = proof.FrameBindingHash(frame)
	if err != nil {
		return nil, true, respBindHash, fmt.Errorf("seal response binding: %w", err)
	}
	out, err = json.Marshal(frame)
	if err != nil {
		return nil, true, respBindHash, fmt.Errorf("encode sealed response: %w", err)
	}
	return out, true, respBindHash, nil
}

// responseFrameSealer seals a sequence of streaming SSE frames under one HPKE
// response context (SPEC §7). Frames are sealed in order; the client opens them
// in the same order.
type responseFrameSealer struct {
	sealer       *wire.ResponseSealer
	binder       *proof.StreamBinder
	logger       log.Logger
	emittedFinal bool
	frameCount   int
}

// newResponseFrameSealer returns a per-stream frame sealer when the request was
// sealed, or (nil, nil) when it was not (the caller then forwards plaintext).
func (c *Ctrl) newResponseFrameSealer(ctx *gin.Context) (*responseFrameSealer, error) {
	ephPub, sealed := e2eeSealedRequest(ctx)
	if !sealed {
		return nil, nil
	}
	// Declare model + x_0g_trace unbound on every frame so the router may
	// rewrite/inject them into the sealed stream downstream (SPEC §5.2). The whole
	// stream shares one context, so the unbound set is fixed once here.
	s, err := wire.NewResponseSealer(ephPub, e2eeResponseUnboundFields...)
	if err != nil {
		return nil, fmt.Errorf("set up response sealer: %w", err)
	}
	// Seed the §8 streaming binding with the request hash captured at unseal time.
	// Its absence means the response path ran without a prior unseal — fail closed
	// rather than sign a stream bound to a zero request hash.
	reqBindHash, ok := e2eeReqBindHash(ctx)
	if !ok {
		return nil, fmt.Errorf("e2ee stream: request binding hash missing from context")
	}
	return &responseFrameSealer{
		sealer: s,
		binder: proof.NewStreamBinderFromReqHash(reqBindHash),
		logger: c.logger,
	}, nil
}

// sealSSELine transforms one already-sanitized SSE line into its sealed form
// (SPEC §7). Every "data: {json}" chunk is sealed as a NON-final frame; exactly
// one final frame is emitted synthetically at stream end — before a "data: [DONE]"
// sentinel here, or on EOF by the caller via finalFrameLine. Deriving `final` from
// per-frame usage is deliberately avoided: some upstreams emit empty "usage":{}
// mid-stream, and vLLM continuous_usage_stats puts usage on every chunk, either of
// which would mark a non-terminal frame final and truncate the client's stream.
// Blank lines and the payload-free SSE fields pass through unchanged; anything
// else that cannot be turned into a frame is refused rather than forwarded.
func (rs *responseFrameSealer) sealSSELine(line string) (string, error) {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return line, nil // preserve SSE event separators
	}
	// SSE lines that carry no payload: the event/id/retry fields, and a comment
	// (":"). Forwarded as-is because there is nothing in them to hide. The comment
	// case is belt-and-braces — sanitizeStreamLine already drops comments with
	// forward=false, so this function never sees one on the live path — but a
	// payload-free line must not be refused if that ever changes.
	if strings.HasPrefix(trimmed, ":") ||
		strings.HasPrefix(trimmed, "event:") ||
		strings.HasPrefix(trimmed, "id:") ||
		strings.HasPrefix(trimmed, "retry:") {
		return line, nil
	}
	// Everything past here is content, and on a sealed request content that cannot
	// be turned into a frame must be refused rather than passed through. Returning
	// such a line unchanged is how the previous version leaked: an Ollama-style
	// upstream streams bare ND-JSON with no "data:" prefix at all, so
	// {"model":"…","response":"…"} was forwarded byte for byte in the clear, and so
	// was any "data:" payload that was not an object (a JSON array, say).
	after, ok := strings.CutPrefix(trimmed, "data:")
	if !ok {
		return "", rs.refuseUnsealableLine("line is not an SSE field", trimmed)
	}
	payload := strings.TrimSpace(after)
	// The done sentinel, decided from the payload rather than from a byte-exact
	// compare of the whole line. isStreamDone only matches "data: [DONE]", and
	// sanitizeStreamLine normalises the "data:" spacing only for JSON payloads — so
	// an upstream emitting "data:[DONE]" or "data:  [DONE]" used to fall through to
	// the JSON parse below and be refused, aborting the stream on its very last
	// line: the stream loop returns before the EOF branch, so no final frame is ever
	// emitted and the client reads a truncation. And by then the headers are flushed,
	// so the 502 cannot even be delivered as a status.
	if payload == streamDoneSentinel {
		final, err := rs.finalFrameLine()
		if err != nil {
			return "", err
		}
		return final + line, nil // synthetic final frame (if any) precedes [DONE]
	}
	var frame wire.Response
	if err := json.Unmarshal([]byte(payload), &frame); err != nil || frame == nil {
		return "", rs.refuseUnsealableLine("data payload is neither a JSON object nor the done sentinel", payload)
	}
	return rs.sealFrame(frame, false)
}

// refuseUnsealableLine logs why a line could not be sealed and returns the client a
// 502 that does not describe it.
//
// The client-facing message is deliberately shapeless. A 502 body reaches the
// caller verbatim (errors.Response substitutes the message only for 500), and the
// upstream's own vocabulary is a fingerprint: prompt_filter_results is Azure,
// kv_transfer_params is vLLM, x_groq is Groq, done_reason is Ollama. #184 strips
// provider identity out of responses; describing the upstream here would put it
// back.
//
// The log gets the shape and not the content: detail may BE the model output, or
// quote the prompt, so only its first byte and length are recorded — enough to
// tell an ND-JSON upstream from a JSON array, which is what an operator needs to
// act on.
func (rs *responseFrameSealer) refuseUnsealableLine(why, detail string) error {
	firstByte := "(empty)"
	if len(detail) > 0 {
		firstByte = string(detail[0])
	}
	if rs.logger != nil {
		rs.logger.Errorf(
			"e2ee: refusing to forward a stream line on a sealed request: %s (starts with %q, %d bytes) — this upstream's streaming format cannot be sealed",
			why, firstByte, len(detail))
	}
	return errors.NewHTTPError(http.StatusBadGateway,
		fmt.Errorf("upstream returned a streaming response this broker cannot seal"))
}

// finalFrameLine returns a synthetic final SSE frame (empty choices) so the client
// always receives exactly one completion marker (SPEC §7). It returns "" if a
// final frame was already emitted, making it safe to call on both [DONE] and EOF.
func (rs *responseFrameSealer) finalFrameLine() (string, error) {
	if rs.emittedFinal {
		return "", nil
	}
	return rs.sealFrame(wire.Response{"choices": json.RawMessage("[]")}, true)
}

// sealFrame seals one frame object and returns a self-contained SSE event:
// "data: {json}\n\n". The trailing blank line is the SSE event terminator and is
// REQUIRED — the client's SSE reader concatenates consecutive "data:" lines that
// are not separated by a blank line into a single event, so without it a sealed
// frame would merge with the following frame or the "data: [DONE]" sentinel,
// yielding "{json}\n[DONE]" and a JSON decode error on the client. We terminate
// every frame ourselves rather than relying on the upstream's blank line (which
// an abrupt EOF may omit); an extra blank line the upstream also sends is
// harmless (ignored by SSE parsers).
func (rs *responseFrameSealer) sealFrame(frame wire.Response, final bool) (string, error) {
	// Per-frame sealed set, because a streaming frame's payload field depends on
	// the event: "choices" on the OpenAI surface, "message" / "content_block" /
	// "delta" across the Anthropic event sequence. A frame that carries no output
	// at all (content_block_stop, message_stop, a usage-only trailer, the
	// synthetic final frame) has nothing to seal, and SealFrame rejects both an
	// empty set and a named field that is absent — so give it the placeholder
	// ensureChoices used to inject, which merges to nothing on the client.
	sealedFields := e2eeSealedFieldsFor(frame)
	if len(sealedFields) == 0 {
		// Nothing but cleartext metadata, so nothing to hide. Give it the placeholder
		// ensureChoices used to inject, which merges to nothing on the client, so
		// control frames still seal instead of breaking the stream.
		ensureChoices(frame)
		sealedFields = []string{"choices"}
	}
	out, err := rs.sealer.SealFrame(frame, sealedFields, final)
	if err != nil {
		return "", fmt.Errorf("seal frame: %w", err)
	}
	// Fold the exact on-wire frame into the §8 streaming binding, in send order
	// (the final frame last), so the signed aggregate matches what the client
	// recomputes over the frames it receives.
	if err := rs.binder.AddFrame(out); err != nil {
		return "", fmt.Errorf("bind sealed frame: %w", err)
	}
	rs.frameCount++
	b, err := json.Marshal(out)
	if err != nil {
		return "", fmt.Errorf("encode sealed frame: %w", err)
	}
	if final {
		rs.emittedFinal = true
	}
	return "data: " + string(b) + "\n\n", nil
}

// signedText finalizes the §8 streaming binding and returns the scheme-tagged
// signed text (proof.StreamBinder.Text). ok is false when no frame was sealed (a
// degenerate empty stream), so the caller skips caching a signature rather than
// binding sha256("").
func (rs *responseFrameSealer) signedText() (text string, ok bool, err error) {
	if rs.frameCount == 0 {
		return "", false, nil
	}
	text, err = rs.binder.Text()
	return text, err == nil, err
}

// ensureChoices injects an empty "choices" so a frame that carries no model
// output at all still has one field to seal (SealFrame rejects an empty sealed
// set, and a named field that is absent from the frame). Called only from
// sealFrame's no-output branch — a usage-only trailer, an Anthropic
// content_block_stop / message_stop, the synthetic final frame. An injected empty
// array merges to nothing on the client.
//
// It must NOT be called before computing the sealed set: doing so made every
// frame look sealable under the v1 default and was how an Anthropic response's
// cleartext "content" came to ride alongside a sealed-looking empty array.
func ensureChoices(frame wire.Response) {
	if _, ok := frame["choices"]; !ok {
		frame["choices"] = json.RawMessage("[]")
	}
}
