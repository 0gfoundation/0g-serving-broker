package ctrl

// This file implements the provider (enclave) side of the 0g-pc end-to-end
// encryption protocol (SPEC.md §5–§7): unsealing the sensitive request fields a
// client sealed to this enclave's HPKE key, and sealing the sensitive response
// fields back to the client's ephemeral key. The wire format and crypto are
// provided by github.com/0gfoundation/0g-pc/protocol (imported byte-for-byte);
// this layer wires them into the broker's proxy/billing/signing path and adds
// the broker-specific policy checks the protocol package deliberately leaves to
// the caller.

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	pccrypto "github.com/0gfoundation/0g-pc/protocol/crypto"
	"github.com/0gfoundation/0g-pc/protocol/wire"
	"github.com/gin-gonic/gin"
)

const (
	// e2eeHeader is the request header a client/router sets to flag a sealed
	// request. Coordinated with the router (0g-router#618). Detection also falls
	// back to the presence of the "_e2ee" body field, so a client that omits the
	// header is still handled.
	e2eeHeader = "X-0G-E2EE"

	// e2eeBodyMarker is the reserved top-level field carrying the sealing metadata
	// (SPEC §5). Used for a cheap pre-check before a full JSON parse.
	e2eeBodyMarker = "_e2ee"

	// CtxKeyE2EESealed marks (bool) that the current request arrived sealed, so the
	// response path knows to seal its reply.
	CtxKeyE2EESealed = "e2eeSealed"
	// CtxKeyE2EEClientEphPub holds the client's response ephemeral X25519 public
	// key (pccrypto.PublicKey) extracted from the request envelope (SPEC §7).
	CtxKeyE2EEClientEphPub = "e2eeClientEphPub"
	// CtxKeyE2EEPlaintextReq holds the reconstructed plaintext request bytes
	// ([]byte) captured immediately after unsealing, BEFORE the proxy's upstream
	// rewrites (model enforcement, stream_options injection, …). The response
	// signature (§8) must bind the request the CLIENT reconstructs, not the
	// modified upstream body.
	CtxKeyE2EEPlaintextReq = "e2eePlaintextReq"
)

// isSealedRequest reports whether reqBody / the request headers indicate a sealed
// E2EE request. Cheap: a substring pre-check on the body plus the header, before
// any JSON parse. The definitive check is in UnsealRequest, which fails closed.
func isSealedRequest(ctx *gin.Context, reqBody []byte) bool {
	if ctx != nil && ctx.Request != nil && ctx.Request.Header.Get(e2eeHeader) != "" {
		return true
	}
	return bytes.Contains(reqBody, []byte(e2eeBodyMarker))
}

// MaybeUnsealRequest unseals a sealed E2EE request in-enclave and returns the
// reconstructed plaintext body to forward upstream. For a non-sealed request it
// returns reqBody unchanged.
//
// On success for a sealed request it stashes, on the gin context, that the
// request was sealed and the client's response ephemeral key, so the response
// path seals its reply (SPEC §7). Any failure on a request that claims to be
// sealed is returned as an error and MUST be treated as fail-closed by the caller
// (no plaintext fallback, SPEC §6) — a sealed request that cannot be opened,
// whose provider_id is not this enclave, or whose key_id is unknown is rejected.
func (c *Ctrl) MaybeUnsealRequest(ctx *gin.Context, reqBody []byte) ([]byte, error) {
	if !isSealedRequest(ctx, reqBody) {
		return reqBody, nil
	}
	if len(c.teeService.EncPrivateKey) == 0 {
		return nil, fmt.Errorf("received a sealed request but the enclave enc key is not available")
	}

	var env wire.Request
	if err := json.Unmarshal(reqBody, &env); err != nil {
		return nil, fmt.Errorf("sealed request body is not a JSON object: %w", err)
	}
	// A body carrying the header/marker but no real _e2ee object: treat as an error
	// rather than silently forwarding cleartext (fail-closed).
	e2ee, err := env.E2EE()
	if err != nil {
		return nil, fmt.Errorf("sealed request missing %q envelope: %w", e2eeBodyMarker, err)
	}

	// Select the enc key by key_id (SPEC §6). The broker holds a single current
	// enc key; a mismatch means the client sealed to a rotated/foreign key we
	// cannot open, so reject with a clear error rather than a raw HPKE failure.
	if err := c.verifyEncKeyID(e2ee.KeyID); err != nil {
		return nil, err
	}

	// Enforce provider pinning (SPEC §5/§6): the enclave rejects a request pinned
	// to a different provider. OpenRequest deliberately does not check this — the
	// broker knows its own identity.
	if !strings.EqualFold(e2ee.ProviderID, c.teeService.Address.Hex()) {
		return nil, fmt.Errorf("sealed request provider_id %q does not match this enclave", e2ee.ProviderID)
	}

	// Extract the client's response ephemeral key before opening, so the response
	// path can seal even though the field lives in the (now consumed) envelope.
	clientEphPub, err := base64.RawURLEncoding.DecodeString(e2ee.ClientEphPub)
	if err != nil {
		return nil, fmt.Errorf("sealed request has invalid client_eph_pub: %w", err)
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
	c.logger.Debugf("E2EE: unsealed request (sealed_fields=%v, key_id=%s)", e2ee.SealedFields, e2ee.KeyID)
	return plaintext, nil
}

// verifyEncKeyID checks that a request's key_id (base64url) selects this
// enclave's current enc key (SPEC §4.3/§6).
func (c *Ctrl) verifyEncKeyID(b64KeyID string) error {
	want := base64.RawURLEncoding.EncodeToString(c.teeService.KeyID)
	if b64KeyID != want {
		return fmt.Errorf("sealed request key_id %q does not match the enclave enc key", b64KeyID)
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

// maybeSealNonStreamResponse seals the sensitive fields (v1 default: choices) of
// a non-streaming response (SPEC §7) when the request was sealed; otherwise it
// returns body unchanged with sealed=false. Fail-closed: when the request was
// sealed but sealing fails, it returns an error and the caller MUST NOT forward
// the plaintext body.
func (c *Ctrl) maybeSealNonStreamResponse(ctx *gin.Context, body []byte) (out []byte, sealed bool, err error) {
	ephPub, isSealed := e2eeSealedRequest(ctx)
	if !isSealed {
		return body, false, nil
	}
	var resp wire.Response
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, true, fmt.Errorf("seal response: body is not a JSON object: %w", err)
	}
	ensureChoices(resp)
	out, err = sealResponseMarshal(ephPub, resp)
	if err != nil {
		return nil, true, err
	}
	return out, true, nil
}

func sealResponseMarshal(ephPub pccrypto.PublicKey, resp wire.Response) ([]byte, error) {
	out, err := wire.SealResponse(ephPub, resp, nil) // nil → v1 default ["choices"]
	if err != nil {
		return nil, fmt.Errorf("seal response: %w", err)
	}
	b, err := json.Marshal(out)
	if err != nil {
		return nil, fmt.Errorf("encode sealed response: %w", err)
	}
	return b, nil
}

// responseFrameSealer seals a sequence of streaming SSE frames under one HPKE
// response context (SPEC §7). Frames are sealed in order; the client opens them
// in the same order.
type responseFrameSealer struct {
	sealer       *wire.ResponseSealer
	emittedFinal bool
}

// newResponseFrameSealer returns a per-stream frame sealer when the request was
// sealed, or (nil, nil) when it was not (the caller then forwards plaintext).
func (c *Ctrl) newResponseFrameSealer(ctx *gin.Context) (*responseFrameSealer, error) {
	ephPub, sealed := e2eeSealedRequest(ctx)
	if !sealed {
		return nil, nil
	}
	s, err := wire.NewResponseSealer(ephPub)
	if err != nil {
		return nil, fmt.Errorf("set up response sealer: %w", err)
	}
	return &responseFrameSealer{sealer: s}, nil
}

// sealSSELine transforms one already-sanitized SSE line into its sealed form
// (SPEC §7). A "data: {json}" chunk has its choices sealed; the frame carrying
// usage is marked final (the broker forces stream_options.include_usage, so the
// last chunk before [DONE] carries usage). A "data: [DONE]" sentinel passes
// through, preceded by a synthetic final frame if none was emitted yet so the
// client can always detect completion. Blank/other lines pass through unchanged.
func (rs *responseFrameSealer) sealSSELine(line string) (string, error) {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return line, nil // preserve SSE event separators
	}
	if isStreamDone([]byte(trimmed)) {
		if rs.emittedFinal {
			return line, nil
		}
		final, err := rs.sealFrame(wire.Response{"choices": json.RawMessage("[]")}, true)
		if err != nil {
			return "", err
		}
		return final + line, nil
	}
	after, ok := strings.CutPrefix(trimmed, "data:")
	if !ok {
		return line, nil
	}
	payload := strings.TrimSpace(after)
	if !strings.HasPrefix(payload, "{") {
		return line, nil
	}
	var frame wire.Response
	if err := json.Unmarshal([]byte(payload), &frame); err != nil {
		return "", fmt.Errorf("seal stream frame: %w", err)
	}
	ensureChoices(frame)
	_, hasUsage := frame["usage"]
	final := hasUsage && !isJSONNull(frame["usage"])
	return rs.sealFrame(frame, final)
}

// sealFrame seals one frame object and returns the "data: {json}\n" line.
func (rs *responseFrameSealer) sealFrame(frame wire.Response, final bool) (string, error) {
	out, err := rs.sealer.SealFrame(frame, nil, final)
	if err != nil {
		return "", fmt.Errorf("seal frame: %w", err)
	}
	b, err := json.Marshal(out)
	if err != nil {
		return "", fmt.Errorf("encode sealed frame: %w", err)
	}
	if final {
		rs.emittedFinal = true
	}
	return "data: " + string(b) + "\n", nil
}

// ensureChoices guarantees a "choices" field is present so SealFrame (whose v1
// default seals "choices") never fails on a frame that legitimately omits it
// (e.g. a trailing usage-only chunk). An injected empty array merges to nothing
// on the client.
func ensureChoices(frame wire.Response) {
	if _, ok := frame["choices"]; !ok {
		frame["choices"] = json.RawMessage("[]")
	}
}

// isJSONNull reports whether raw is the JSON literal null.
func isJSONNull(raw json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}
