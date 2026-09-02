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
	"strings"

	pccrypto "github.com/0gfoundation/0g-pc-e2ee/protocol/crypto"
	"github.com/0gfoundation/0g-pc-e2ee/protocol/proof"
	"github.com/0gfoundation/0g-pc-e2ee/protocol/wire"
	"github.com/gin-gonic/gin"

	"github.com/0glabs/0g-serving-broker/common/errors"
	"github.com/0glabs/0g-serving-broker/common/log"
	"github.com/0glabs/0g-serving-broker/inference/config"
	constant "github.com/0glabs/0g-serving-broker/inference/const"
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

	// anthropicFrameType is the cleartext field an Anthropic response frame names
	// its own shape in, and anthropicMessageStop the event a completed turn ends
	// with (SPEC §7.2). The wire package owns the full taxonomy — including which
	// shapes are terminal, which is asked of it rather than restated here; the
	// broker needs these two only to SYNTHESIZE the event a truncated stream never
	// got.
	anthropicFrameType   = "type"
	anthropicMessageStop = "message_stop"

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
	// CtxKeyE2EEProfile holds the wire.Profile (SPEC §5.1) the request was opened
	// under, resolved once at unseal time from the service type AND the API
	// surface the request arrived on.
	//
	// The response path reads it back rather than re-deriving it, so a response
	// cannot be sealed under a profile whose rules were never applied to its
	// request. Re-deriving looked equivalent while every service type had exactly
	// one surface; it stopped being equivalent with /v1/messages, which is the
	// SAME chatbot service type as /v1/chat/completions — so the chat literal at
	// the non-streaming call site was simply wrong for it, and being wrong here is
	// silent (identical wire format, plausible frames, content in the clear).
	CtxKeyE2EEProfile = "e2eeProfile"
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

// hasE2EEMarker is a cheap substring pre-check to skip the JSON parse on the vast
// majority of (non-sealed) requests. A match is not proof of a sealed request —
// the substring could appear inside message content — so MaybeUnsealRequest
// confirms a genuine top-level "_e2ee" key before committing to fail-closed.
func hasE2EEMarker(reqBody []byte) bool {
	return bytes.Contains(reqBody, []byte(e2eeBodyMarker))
}

// IsSealedRequest reports whether reqBody is a sealed envelope (SPEC §5): a JSON
// object with a top-level "_e2ee" key. It is the same test MaybeUnsealRequest
// makes before committing to fail-closed, exposed for entry points that cannot
// SERVE a sealed request and so must refuse it rather than forward it.
//
// The async submit routes are those entry points. They do not go through the
// proxy, so they never reach MaybeUnsealRequest; without this, a sealed envelope
// POSTed to /v1/async/images/generations was enqueued verbatim, had its
// cleartext rewritten by forceB64ResponseFormat (which also invalidates the
// AAD), was forwarded upstream still sealed, and had its result served in
// plaintext — while the user was billed for the garbage job. The prompt stayed
// sealed throughout, so little was disclosed; what broke is that "a sealed
// request is fail-closed" stopped being a property of the enclave and became a
// property of which route the client picked.
func (c *Ctrl) IsSealedRequest(reqBody []byte) bool {
	if !hasE2EEMarker(reqBody) {
		return false
	}
	var env wire.Request
	if err := json.Unmarshal(reqBody, &env); err != nil {
		return false // not a JSON object → cannot be an envelope
	}
	_, ok := env[e2eeBodyMarker]
	return ok
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

	// Everything the receiver is responsible for now runs inside
	// wire.OpenRequestFor below (SPEC §12): the sealed set covers this profile's
	// payload, and the pinned cleartext field is present, correctly valued, not
	// sealed away and not declared unbound. Resolve the profile here — the
	// protocol package cannot know which endpoint this broker serves.
	surface := apiFormatForPath(ctx.Request.URL.Path)
	profile, sealable := profileForRequest(c.Service.Type, surface)
	if !sealable {
		return nil, fmt.Errorf("sealed requests are not supported for service type %q on the %q API surface", c.Service.Type, surface)
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
	reconstructed, err := wire.OpenRequestFor(profile, c.teeService.EncPrivateKey, env)
	if err != nil {
		return nil, fmt.Errorf("unseal request: %w", err)
	}

	plaintext, err := json.Marshal(reconstructed)
	if err != nil {
		return nil, fmt.Errorf("re-encode unsealed request: %w", err)
	}

	ctx.Set(CtxKeyE2EESealed, true)
	ctx.Set(CtxKeyE2EEProfile, profile)
	ctx.Set(CtxKeyE2EEClientEphPub, pccrypto.PublicKey(clientEphPub))
	ctx.Set(CtxKeyE2EEPlaintextReq, plaintext)
	ctx.Set(CtxKeyE2EEReqBindHash, reqBindHash)
	c.logger.Debugf("E2EE: unsealed request (sealed_fields=%v, key_id=%s)", e2ee.SealedFields, e2ee.KeyID)
	return plaintext, nil
}

// profileForRequest maps the endpoint this broker serves — service type AND the
// API surface the request arrived on — to the wire profile whose rules apply to a
// sealed request on it. sealable=false means "no sealed request is acceptable
// here".
//
// The SURFACE is half the key, not decoration. One chatbot service answers on two
// of them: /v1/chat/completions (OpenAI) and /v1/messages (Anthropic), whose
// payload and response shapes differ (a top-level `system` prompt; response
// frames typed by `type`, SPEC §7.2). Keyed on the service type alone,
// an Anthropic sealed request resolved to ProfileChat, which sealed an injected
// empty `choices` while the real `content`/`delta` rode in the clear — no error
// anywhere, since the wire format is identical and the frames look plausible.
//
// The mapping is an ALLOWLIST, and deliberately so. Everything absent either has
// no envelope format yet (the multipart service types — their request is
// multipart/form-data, which cannot be an envelope, so a JSON envelope arriving
// on one would unseal into a body the upstream cannot consume) or has simply not
// been specified (video-generation, and whatever service type is added next). A
// default arm that guessed ProfileChat for those would apply the wrong rule to a
// request shape nobody has analyzed — and would do it silently for a service type
// that does not exist yet. Refusing is the honest answer, and it is what SPEC §1
// requires.
//
// That allowlist discipline extends to the SURFACE, so an UNRECOGNIZED one
// (apiFormatForPath's "") is refused on the chatbot arm rather than read as
// chat's own case. It is the likelier of the two mistakes: adding a chat route
// means adding an entry to constant.TargetRoute, and teaching apiFormatForPath
// about it is a SEPARATE edit that nothing forces — so a new surface that
// resolved "" to ProfileChat would apply chat's rules to an unanalyzed request
// shape, silently, which is the exact bug the surface key was added to fix.
// Refusing costs nothing today: every chatbot route in TargetRoute (/messages,
// /v1/messages, /chat/completions) is matched by apiFormatForPath, and an
// unsealed request never reaches here at all — the profile is resolved only
// after the envelope is confirmed.
//
// The image and multipart endpoints are not chat surfaces, so the surface is
// whatever their path happened to be and only the chatbot arm consults it.
func profileForRequest(svcType, surface string) (p wire.Profile, sealable bool) {
	switch svcType {
	case constant.ServiceTypeChatbot:
		switch surface {
		case config.APIFormatAnthropic:
			return wire.ProfileAnthropic, true
		case config.APIFormatOpenAI:
			return wire.ProfileChat, true
		default:
			return "", false
		}
	case constant.ServiceTypeTextToImage:
		return wire.ProfileImage, true
	default:
		return "", false
	}
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

// e2eeProfile returns the wire profile the request was opened under, stashed at
// unseal time. Its absence on a sealed request means the response path ran
// without a prior unseal, which the seal paths treat as fail-closed rather than
// picking a profile of their own.
func e2eeProfile(ctx *gin.Context) (wire.Profile, bool) {
	v, ok := ctx.Get(CtxKeyE2EEProfile)
	if !ok {
		return "", false
	}
	p, ok := v.(wire.Profile)
	return p, ok && p != ""
}

// maybeSealNonStreamResponse seals a non-streaming response (SPEC §7) under the
// profile the request was opened with, when the request was sealed; otherwise it
// returns body unchanged with sealed=false. Fail-closed: when the request was
// sealed but sealing fails, it returns an error and the caller MUST NOT forward
// the plaintext body.
//
// The profile comes from the context rather than the call site. It implies the
// sealed-field list AND the profile-specific checks the sealer runs on the frame
// — for image, that it carries the cleartext `usage.output_images` the router
// bills on (§7.1) — so passing the list alone let the image path seal through the
// chat profile, which knows of no such requirement. A profile CONSTANT at the
// call site fixed that but had the same shape of flaw one level up: the chatbot
// handler serves both /v1/chat/completions and /v1/messages, so its `ProfileChat`
// literal was wrong for every sealed Anthropic request. Reading what the request
// was actually opened under is the only version of this that cannot drift.
func (c *Ctrl) maybeSealNonStreamResponse(ctx *gin.Context, body []byte) (out []byte, sealed bool, respBindHash [32]byte, err error) {
	ephPub, isSealed := e2eeSealedRequest(ctx)
	if !isSealed {
		return body, false, respBindHash, nil
	}
	profile, ok := e2eeProfile(ctx)
	if !ok {
		return nil, true, respBindHash, fmt.Errorf("seal response: the request's e2ee profile is missing from the context")
	}
	var resp wire.Response
	// A literal JSON `null` unmarshals into a nil map WITHOUT error;
	// ensureSealedFieldsPresent would then panic writing to it. Reject any
	// non-object body fail-closed.
	if uerr := json.Unmarshal(body, &resp); uerr != nil || resp == nil {
		return nil, true, respBindHash, fmt.Errorf("seal response: body is not a JSON object")
	}
	// Resolved against the RESPONSE, not the profile alone: a frame-typed profile
	// (Anthropic) answers per frame shape (§7.2).
	sealedFields, err := wire.ResponseSealedFieldsForFrame(profile, resp)
	if err != nil {
		return nil, true, respBindHash, fmt.Errorf("seal response: %w", err)
	}
	ensureSealedFieldsPresent(profile, resp, sealedFields)
	// Declare model + x_0g_trace unbound so the router may rewrite/inject them
	// downstream (SPEC §5.2).
	frame, err := wire.SealResponseFor(profile, ephPub, resp, sealedFields, e2eeResponseUnboundFields...)
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
	sealer *wire.ResponseSealer
	binder *proof.StreamBinder
	// profile is the one the REQUEST was opened under (read off the context, not
	// re-derived), so the stream cannot be sealed under rules that were never
	// applied to the request. What each frame must seal is then resolved from the
	// frame: a frame-typed profile (Anthropic, §7.2) answers per event shape, and
	// holding one set for the whole stream is exactly the mistake — it would seal
	// nothing on every content frame.
	profile wire.Profile
	// synthFinal is what this profile's stream is CAPPED with when an upstream
	// drops off without sending a terminal event of its own. Zero for a
	// single-shape profile, whose synthetic final frame carries only empty
	// placeholders.
	//
	// It is deliberately NOT how a terminal frame is RECOGNIZED: which shapes end
	// a stream is the profile's business (Anthropic has two — a completed turn
	// ends with `message_stop`, a failed one with `error` and no `message_stop`),
	// so that question goes to wire.IsTerminalResponseFrame. Capping and
	// recognizing coincide for a normal turn and diverge for a failed one, which
	// is why they are separate.
	synthFinal   wire.Response
	emittedFinal bool
	frameCount   int
	// logger reports what was dropped: a frame this broker declines to seal but
	// does not fail the request over leaves no other trace, since the client sees
	// a complete stream either way.
	logger log.Logger
}

// newResponseFrameSealer returns a per-stream frame sealer when the request was
// sealed, or (nil, nil) when it was not (the caller then forwards plaintext).
func (c *Ctrl) newResponseFrameSealer(ctx *gin.Context) (*responseFrameSealer, error) {
	ephPub, sealed := e2eeSealedRequest(ctx)
	if !sealed {
		return nil, nil
	}
	// The profile the REQUEST was opened under, so the stream cannot be sealed
	// under rules that were never applied to the request.
	profile, ok := e2eeProfile(ctx)
	if !ok {
		return nil, fmt.Errorf("e2ee stream: the request's e2ee profile is missing from the context")
	}
	// Declare model + x_0g_trace unbound on every frame so the router may
	// rewrite/inject them into the sealed stream downstream (SPEC §5.2). The whole
	// stream shares one context, so the unbound set is fixed once here.
	s, err := wire.NewResponseSealerFor(profile, ephPub, e2eeResponseUnboundFields...)
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
	// Prove now that the frame this stream would be CAPPED with is one the profile
	// can actually seal. The check is here rather than at EOF because EOF is the
	// one moment a failure cannot be reported: the caller can no longer answer the
	// request, so a profile with no usable entry in synthFinalFrameFor would leave
	// the client a stream with no final frame — a truncation it rejects wholesale
	// (§7). Refusing at setup makes that a failed request instead, with the
	// profile named.
	//
	// It passes trivially for a single-shape profile (the zero frame resolves to
	// the profile default) and for Anthropic (`message_stop` seals nothing), and
	// fails for a frame-typed profile added without an entry, whose zero frame has
	// no discriminator to resolve.
	synthFinal := synthFinalFrameFor(profile)
	if _, err := wire.ResponseSealedFieldsForFrame(profile, synthFinal); err != nil {
		return nil, fmt.Errorf("e2ee stream: profile %q declares no synthetic terminal frame this broker can seal: %w", profile, err)
	}
	return &responseFrameSealer{
		sealer:     s,
		binder:     proof.NewStreamBinderFromReqHash(reqBindHash),
		profile:    profile,
		synthFinal: synthFinal,
		logger:     c.logger,
	}, nil
}

// profileHasFrameDiscriminator reports whether this profile's response frames
// name their own shape in a cleartext field the wire package validates — which
// is what makes an `event:` line derivable from a frame, and what makes the
// derived value trustworthy (ResponseSealedFieldsForFrame refuses a shape outside
// the taxonomy, so the value can only be one of a fixed set of identifiers).
//
// It sits beside synthFinalFrameFor because it is the same kind of per-profile
// fact — what this profile's SSE stream looks like — and, like it, is a serving
// question the wire package does not expose an answer to. Both are the reason a
// frame-typed profile added without touching this file is refused at stream
// setup rather than served wrongly.
func profileHasFrameDiscriminator(p wire.Profile) bool {
	return p == wire.ProfileAnthropic
}

// synthFinalFrameFor returns the plaintext frame to cap a truncated stream of
// this profile with, or nil for a profile whose streams have no such event.
//
// It answers with the FRAME only, no event name beside it: sealFrame derives the
// SSE `event:` line from whatever frame it is sealing, reading the frame's own
// bound discriminator, so a synthesized event is announced exactly like a
// forwarded one and the name is not a second per-profile literal anywhere.
//
// Anthropic's stream ends with a `message_stop` event rather than a `[DONE]`
// sentinel, and that event is a legal frame of the profile — it seals nothing
// (§7.2) — so it is what an upstream that dropped off should have sent, and what
// this broker sends in its place. A chat stream has no equivalent event: its
// final frame is a placeholder with empty content, so it gets the zero value.
//
// A stream that failed partway ends with `error`, which is terminal too — but a
// broker never SYNTHESIZES one: it has no error to report, only a truncation,
// and inventing an `error` frame would attribute a failure to the model that the
// model did not produce.
//
// The capped turn is INCOMPLETE, deliberately visibly so. Anthropic's grammar
// ends a turn `content_block_stop` → `message_delta` (which carries `stop_reason`
// and `usage.output_tokens`) → `message_stop`, and a stream truncated
// mid-`content_block_delta` skips the first two: an SDK accumulating it gets a
// message with `stop_reason: null`, and the router sees no output-token count.
// Filling that gap with a synthesized `message_delta` would mean inventing both
// values — and §8 signs whatever is sent, so the broker would be attesting
// numbers the model never produced. A null `stop_reason` is the honest signal
// that the turn did not complete.
//
// This is the one per-profile literal left in this file, and the profile that
// needs it is the only one that can supply it: the wire package owns which
// shapes END a stream, but "which event should a broker invent when the upstream
// sent none" is a serving decision, not a wire rule (an enclave could
// legitimately choose to fail the request instead). A frame-typed profile added
// without an entry here therefore does not degrade quietly:
// newResponseFrameSealer proves the entry can be sealed before the first frame
// goes out, so the stream is refused up front rather than truncated at EOF.
func synthFinalFrameFor(p wire.Profile) wire.Response {
	if p == wire.ProfileAnthropic {
		return wire.Response{anthropicFrameType: json.RawMessage(`"` + anthropicMessageStop + `"`)}
	}
	return nil
}

// sealSSELine transforms one already-sanitized SSE line into its sealed form
// (SPEC §7). A "data: {json}" chunk is sealed as a NON-final frame, except an
// event a frame-typed profile defines as TERMINAL (for Anthropic `message_stop`
// closing a completed turn, or `error` closing a failed one), which is sealed AS
// the final frame — it is last by definition, so nothing synthetic follows it.
//
// For a chat stream, which has no such event, exactly one final frame is emitted
// synthetically at stream end — before a "data: [DONE]" sentinel here, or on EOF
// by the caller via finalFrameLine. Deriving `final` from per-frame usage is
// deliberately avoided: some upstreams emit empty "usage":{} mid-stream, and vLLM
// continuous_usage_stats puts usage on every chunk, either of which would mark a
// non-terminal frame final and truncate the client's stream.
//
// What passes through is an ALLOWLIST, because a sealed stream's every byte
// should be either sealed or accounted for: the blank line that separates SSE
// events, the `[DONE]` sentinel, and `data:` frames (sealed). Every other line —
// `event:`, `id:`, `retry:`, an unknown field — is DROPPED, and a `data:` payload
// that is not a JSON object fails the stream closed.
//
// The reason is the same for all of them: they sit outside the frame JSON and so
// outside the AAD and the §8 binding, and while everything a sealed frame's
// cleartext half may contain is checked by the profile taxonomy, these lines are
// checked by nothing (sanitizeStreamLine's leak-field stripping, #184, only
// inspects `data:` JSON too). Forwarding one hands an upstream a channel for
// arbitrary text to the client and to every intermediary on an otherwise sealed
// turn. The `event:` line loses nothing by being dropped, since §7.2 already
// requires a receiver to ignore the received line and rebuild it from the bound
// discriminator — which is what sealFrame does.
func (rs *responseFrameSealer) sealSSELine(line string) (string, error) {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return line, nil // preserve SSE event separators
	}
	if isStreamDone([]byte(trimmed)) {
		final, err := rs.finalFrameLine()
		if err != nil {
			return "", err
		}
		return final + line, nil // synthetic final frame (if any) precedes [DONE]
	}
	after, ok := strings.CutPrefix(trimmed, "data:")
	if !ok {
		// Any other SSE field line: `event:` (rebuilt by sealFrame from the bound
		// discriminator), `id:`, `retry:`, or something unrecognized. None of them
		// is inside the AAD, none of them is checked by anything, and none carries
		// content a sealed receiver may act on — so none is forwarded. Debug rather
		// than Warn: an upstream that sends `id:` sends it on every frame, and this
		// is a normal thing to discard, not an incident.
		rs.logger.Debugf("e2ee stream: dropping a non-data SSE line, which no sealed receiver may trust: %q", trimmed)
		return "", nil
	}
	payload := strings.TrimSpace(after)
	if !strings.HasPrefix(payload, "{") {
		// Not [DONE] (handled above) and not a JSON object, so there is no frame to
		// seal and nothing that could check it — the same hole as the `event:`
		// line, one branch away, except that clients RENDER `data:` payloads. A
		// sealed stream fails closed rather than passing arbitrary text through to
		// the client and every intermediary in the clear.
		return "", fmt.Errorf("seal stream frame: upstream sent a `data:` payload that is neither [DONE] nor a JSON object, so it cannot be sealed")
	}
	var frame wire.Response
	if err := json.Unmarshal([]byte(payload), &frame); err != nil {
		return "", fmt.Errorf("seal stream frame: %w", err)
	}
	// §7 puts the final frame LAST, so a frame arriving behind it is never
	// SEALED. Two things would otherwise go wrong, and the second is the serious
	// one: it would be sealed final=false behind a frame that already said final
	// (no receiver-side check catches that — OpenFrame has no post-final rule),
	// and sealFrame would fold it into the §8 streaming binding. A client stops
	// consuming at the frame marked final, so it would recompute the binding over
	// N frames while this broker signed N+1, and the signature would fail to
	// verify on a turn that otherwise succeeded.
	//
	// Handling it HERE, before sealFrame, is what keeps the binding equal to what
	// the client received.
	//
	// This became reachable with frame-typed profiles: a chat stream can only
	// emit its final frame at [DONE] or EOF, i.e. where nothing follows, but an
	// Anthropic terminal event (`error`, or a `message_stop` an upstream appends
	// anything to) can land mid-stream.
	if rs.emittedFinal {
		return rs.handleFrameAfterFinal(frame)
	}
	return rs.sealFrame(frame, rs.isTerminal(frame))
}

// handleFrameAfterFinal decides what to do with a data frame that arrived behind
// the final one. Either way it is not sealed and not bound; the question is only
// whether the stream continues.
//
// It is DROPPED when the frame CARRIES no answer: a duplicate or trailing
// `message_stop`, a `ping`, or any frame holding none of the fields its shape
// would seal — EMPTY counting as absent, since `choices: []` is how this file
// itself writes "nothing here" (ensureSealedFieldsPresent manufactures exactly
// that as its placeholder). OpenAI's trailing usage-only chunk is the case that
// makes the distinction load-bearing: it carries `"choices": []`, so a
// presence-only test failed the very frame this branch was written for.
// That is the case actually seen in the wild — a proxy that appends
// `message_stop` after `error` or sends it twice, and a chat upstream's trailing
// usage-only chunk behind [DONE] — and the client is unharmed, having already
// received a complete final frame. Failing instead would be worse than the
// quirk: the stream is already committed and flushed, so the error path appends
// a JSON error body behind the sealed final frame and reports a turn that fully
// delivered as a broker error.
//
// It FAILS the stream when the frame does carry one of those fields, or when its
// shape is unknown and so might. That is the one case where dropping loses data:
// something the client will never see, silently. Stopping is also all this
// broker can do about it — the frame cannot be sealed without breaking the §8
// binding the client verifies. Being TERMINAL is not an exemption: Anthropic's
// `error` is terminal AND carries content, so a trailing one reports a real
// downstream failure and must not be swallowed.
//
// The decision is on what the frame HOLDS, not on what its shape may seal,
// because for a single-shape profile those differ: chat's sealed set is
// ["choices"] for every frame whatever it contains, and no chat frame is ever
// terminal, so a shape-based test failed the stream on every post-[DONE] chunk —
// including the usage-only one that legitimately carries no `choices` at all
// (the frame ensureSealedFieldsPresent exists to accommodate). For a frame-typed
// profile the two tests agree: a `content_block_delta` carries its `delta`, and
// `ping` / `message_stop` carry nothing to begin with.
func (rs *responseFrameSealer) handleFrameAfterFinal(frame wire.Response) (string, error) {
	const because = "§7 requires the final frame to be last, and sealing this one would break the §8 binding the client recomputes"
	sealed, err := wire.ResponseSealedFieldsForFrame(rs.profile, frame)
	if err != nil {
		return "", fmt.Errorf("seal stream frame: upstream sent a frame of unknown shape after the terminal frame, which may carry content: %s: %w", because, err)
	}
	for _, f := range sealed {
		v, ok := frame[f]
		if !ok || isEmptyJSONValue(v) {
			continue
		}
		// Carries an answer. Being TERMINAL does not exempt it: Anthropic's
		// `error` is both terminal and content-bearing, so a "terminal frames are
		// safe to drop" shortcut silently swallowed a downstream failure report
		// that arrived behind a `message_stop` — the exact case this branch exists
		// for. Whether a shape ends a stream says nothing about whether it carries
		// something the client needs.
		return "", fmt.Errorf("seal stream frame: upstream sent a frame (%s) carrying %q after the terminal frame: %s", frameDescriptionOf(frame, rs.profile), f, because)
	}
	rs.logger.Warnf("e2ee stream: dropping a frame (%s) that arrived after the terminal frame and carries no answer; %s", frameDescriptionOf(frame, rs.profile), because)
	return "", nil
}

// isEmptyJSONValue reports whether a raw JSON value carries nothing: `null`, an
// empty array, object or string. It is the counterpart of the empty-array
// placeholder ensureSealedFieldsPresent injects — a field holding `[]` and a
// field that is absent mean the same thing on this wire, so they must take the
// same branch wherever "does this frame carry an answer" is asked.
func isEmptyJSONValue(v json.RawMessage) bool {
	var decoded any
	if err := json.Unmarshal(v, &decoded); err != nil {
		return false // undecodable: treat as content rather than assume it is empty
	}
	switch t := decoded.(type) {
	case nil:
		return true
	case []any:
		return len(t) == 0
	case map[string]any:
		return len(t) == 0
	case string:
		return t == ""
	}
	return false
}

// frameKindOf returns a frame's bound discriminator value, or "" when it has
// none (a single-shape profile's frames, which their API sends without an
// `event:` line). It reads the same cleartext field the wire package keys its
// per-shape rules off, which is why the line built from it is trustworthy in a
// way the upstream's own line is not: this value is inside the AAD.
func frameKindOf(frame wire.Response) string {
	var kind string
	if err := json.Unmarshal(frame[anthropicFrameType], &kind); err != nil {
		return ""
	}
	return kind
}

// frameDescriptionOf names a frame for a log or an error: its shape for a
// frame-typed profile, and just the profile for a single-shape one, whose frames
// have no shape to name (calling a chat chunk "untyped" read as a defect rather
// than as normal). It is for humans only — every decision reads the shape
// through the wire package.
func frameDescriptionOf(frame wire.Response, profile wire.Profile) string {
	if kind := frameKindOf(frame); kind != "" {
		return fmt.Sprintf("%s %s", profile, kind)
	}
	return fmt.Sprintf("%s profile", profile)
}

// isTerminal reports whether this frame is an event that CLOSES this profile's
// stream, and so must be sealed with final=true.
//
// Which shapes those are is the profile's business, so it is asked, not
// hardcoded here: Anthropic has two — `message_stop` for a completed turn and
// `error` for one that failed partway, which sends no `message_stop` at all.
// Recognizing only the frame this broker would SYNTHESIZE (see synthFinalFrame)
// would mark an error-terminated stream non-final, and the EOF path would then
// append a `message_stop` after the `error` — a sequence no Anthropic stream
// produces, reading to a client as a turn that completed normally.
//
// A single-shape profile has no terminal event and answers false for every
// frame, which is what keeps the chat stream on its synthetic-final path. An
// unrecognized shape also answers false; sealFrame refuses it a moment later,
// which is where that belongs.
//
// It has no opinion about a SECOND terminal event, deliberately: nothing reaches
// it once one has been sealed, because sealSSELine refuses every data frame
// behind the final one. `final` appearing exactly once is that rule's job, not a
// duplicate check here.
func (rs *responseFrameSealer) isTerminal(frame wire.Response) bool {
	terminal, err := wire.IsTerminalResponseFrame(rs.profile, frame)
	return err == nil && terminal
}

// finalFrameLine returns a synthetic final SSE frame so the client always
// receives exactly one completion marker (SPEC §7). It returns "" if a final
// frame was already emitted, making it safe to call on both [DONE] and EOF — and
// on an Anthropic stream that already sent a terminal event, which IS the final
// frame, so the EOF path then adds nothing.
//
// What the frame contains comes from the profile, never a literal
// {"choices": []}: a single-shape profile gets empty placeholders for its sealed
// fields (every one of them is a JSON array, so an empty one merges to nothing on
// the client), and a frame-typed profile gets the event a healthy upstream would
// have closed with, which is a legal frame of that profile and seals nothing.
func (rs *responseFrameSealer) finalFrameLine() (string, error) {
	if rs.emittedFinal {
		return "", nil
	}
	frame := wire.Response{}
	for k, v := range rs.synthFinal {
		frame[k] = v
	}
	// sealFrame builds the `event:` line from this frame's own bound `type`, so a
	// synthesized event is announced exactly like a forwarded one.
	return rs.sealFrame(frame, true)
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
	// Resolved per frame, not once per stream: a frame-typed profile's answer is a
	// property of the frame (§7.2), and one set held for the whole stream would
	// seal nothing on every content frame.
	sealedFields, err := wire.ResponseSealedFieldsForFrame(rs.profile, frame)
	if err != nil {
		return "", fmt.Errorf("seal frame: %w", err)
	}
	ensureSealedFieldsPresent(rs.profile, frame, sealedFields)
	out, err := rs.sealer.SealFrame(frame, sealedFields, final)
	if err != nil {
		return "", fmt.Errorf("seal frame: %w", err)
	}
	// The SSE `event:` line, rebuilt from the frame's own BOUND discriminator —
	// the upstream's was dropped (see sealSSELine), and this is the same
	// derivation §7.2 requires of a receiver.
	//
	// Only for a profile that HAS a discriminator, which is the load-bearing half.
	// On such a profile the value is already validated: ResponseSealedFieldsForFrame
	// above refuses any shape outside the taxonomy, so `kind` is one of a fixed set
	// of identifiers. On a single-shape profile nothing validates it — `type` is an
	// ordinary cleartext field the wire package has no rule about — so an upstream
	// could put anything there, INCLUDING a newline, and a line built from it would
	// end and start a fresh SSE line: an attacker-chosen, unsealed, unbound `data:`
	// frame written into a sealed stream, ahead of the real one. That is exactly the
	// channel dropping the upstream's own `event:` line closes, so it must not be
	// reopened here.
	eventLine := ""
	if profileHasFrameDiscriminator(rs.profile) {
		kind := frameKindOf(frame)
		if strings.ContainsAny(kind, "\r\n") {
			// Unreachable through the taxonomy, and fail-closed rather than
			// silently dropped: a shape identifier with a line break means an
			// assumption above this line stopped holding.
			return "", fmt.Errorf("seal frame: frame discriminator %q contains a line break", kind)
		}
		if kind != "" {
			eventLine = "event: " + kind + "\n"
		}
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
	return eventLine + "data: " + string(b) + "\n\n", nil
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

// placeholderSealedFields are, PER PROFILE, the sealed fields a legitimate frame
// of that profile can OMIT and whose value is a JSON array, so that an empty one
// merges to nothing on the client. Both halves must hold to be listed, and the
// first is the operative one: the placeholder is for a frame that is well-formed
// WITHOUT the field, never for one that should have carried it.
//
// Keyed on the profile because that is what the invariant is a property of — the
// field NAME alone does not carry it. A bare name set is correct only by
// coincidence of vocabulary: a frame-typed profile with a shape whose content
// field happened to be called `data` would inherit the image profile's
// permission and get a placeholder on a frame obliged to carry content, which is
// exactly the failure the Anthropic reasoning below rejects.
//
// Only chat and image have an entry. A trailing usage-only chat chunk
// legitimately carries no `choices`, and that is what this exists for.
//
// Anthropic has NO entry, for two separate reasons:
//   - its per-shape stream fields (`delta`, `content_block`, `error`) are OBJECTS,
//     where `[]` would not be a placeholder but a type error shipped to a client;
//   - its non-streaming `content` IS an array, but the Messages API always returns
//     it on a `message` response (an empty array at worst), so a placeholder there
//     could only ever fire on a broken upstream — and it would then seal, sign and
//     mark final a frame containing an empty answer, while the router bills the
//     output tokens the same response reported. Nothing would report a problem:
//     exactly the silent failure this profile's rules exist to remove.
//
// So a frame whose shape declares a field it does not carry fails closed here,
// with the sealer's own "sealed field not present in frame".
var placeholderSealedFields = map[wire.Profile]map[string]struct{}{
	wire.ProfileChat:  {"choices": {}},
	wire.ProfileImage: {"data": {}},
}

// ensureSealedFieldsPresent guarantees every field of sealedFields that a frame
// of THIS profile may legitimately omit (placeholderSealedFields) exists on it,
// so SealFrame — which errors on a declared-but-absent sealed field — never
// fails on a frame that is well-formed without one, e.g. a trailing usage-only
// chat chunk with no "choices".
//
// The profile travels with the field list because the list alone cannot answer
// the question; both callers resolved the list FROM a profile, so neither has to
// go looking for it.
//
// This is a shape guard, not a content fallback: a path where a missing sealed
// field would mean lost content must reject BEFORE sealing rather than rely on
// this (the image path refuses an undecodable response for exactly that reason).
// Anything not listed for this profile is left alone, so the sealer's own
// "sealed field not present in frame" is the answer for it.
func ensureSealedFieldsPresent(profile wire.Profile, frame wire.Response, sealedFields []string) {
	mayOmit := placeholderSealedFields[profile]
	for _, f := range sealedFields {
		if _, ok := mayOmit[f]; !ok {
			continue
		}
		if _, ok := frame[f]; !ok {
			frame[f] = json.RawMessage("[]")
		}
	}
}
