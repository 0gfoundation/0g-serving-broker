package ctrl

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/gowebpki/jcs"

	"github.com/0gfoundation/0g-pc-e2ee/protocol/proof"

	teeutil "github.com/0glabs/0g-serving-broker/common/tee"
	"github.com/0glabs/0g-serving-broker/inference/monitor"
)

const ChatPrefix = "chat"

type SigningAlgo int

const (
	ECDSA SigningAlgo = iota
)

func (r SigningAlgo) String() string {
	return [...]string{"ecdsa"}[r]
}

// ChatSignature is the TEE-signed proof cached under a chatKey and served by
// /v1/proxy/signature/{chatKey}. It binds request/response hashes (and, for
// centralized providers, the TLS fingerprint and provider identity) to the
// broker's TEE signing key.
type ChatSignature struct {
	Text                string         `json:"text"`
	SignatureEcdsa      string         `json:"signature"`
	SigningAddressEcdsa common.Address `json:"signing_address"`
	SigningAlgo         string         `json:"signing_algo"`
	// Centralized provider routing proof fields (omitted for decentralized providers)
	ProviderType       string `json:"provider_type,omitempty"`
	ProviderIdentity   string `json:"provider_identity,omitempty"`
	TLSCertFingerprint string `json:"tls_cert_fingerprint,omitempty"`
	// RoutingProof carries the centralized routing proof ALONGSIDE a §8
	// ciphertext binding, for a sealed request to a centralized provider. Absent
	// on every other shape: an unsealed centralized response carries its routing
	// proof in the fields above (this struct IS the proof there), and no other
	// provider type has one to serve.
	//
	// A VERIFIER MUST CROSS-CHECK THE TWO, and MUST REQUIRE THIS FIELD on a route
	// it knows to be centralized. See RoutingProof's doc: they are two independent
	// signatures, JSON adjacency is not attested, and no signature covers this
	// field's presence — so absent-because-stripped and absent-because-unavailable
	// look identical on the wire.
	RoutingProof *RoutingProof `json:"routing_proof,omitempty"`
}

// RoutingProof is a TEE-signed centralized routing proof — request/response
// hashes bound to the upstream's TLS certificate fingerprint and the identity
// that actually served the request.
//
// It is a type of its own, rather than more fields on ChatSignature, because a
// sealed request needs the routing proof IN ADDITION TO its §8 binding and the
// two cannot share one envelope. ChatSignature's Text/Signature carry exactly
// one signed statement, and on a sealed response that has to stay the §8 pair
// the E2EE client verifies — so the routing proof travels nested, carrying its
// OWN text and signature. That is the whole point: a verifier checks it on its
// own terms, and nothing inside it is a claim the enclosing signature fails to
// cover. No field is omitempty: a proof that exists at all has every one of them
// populated (buildCentralizedRoutingProof refuses to sign without a well-formed
// fingerprint, and falls back to the service-level identity), so an absent field
// here would mean a malformed proof rather than an inapplicable one.
//
// The signed text is the SAME format an unsealed centralized response uses
// (teeutil.FormatRoutingProofText), deliberately: a verifier needs no new code
// for the nested case. What the two hashes commit to differs by shape, and the
// difference is the whole correctness question — unsealed hashes the plaintext
// the client holds; SEALED reuses §8's own on-wire binding hashes, lifted from
// the signed text rather than recomputed (buildSealedRoutingProof). Hashing the
// plaintext on a sealed request would be both unverifiable by the client and a
// plaintext-digest leak on an unauthenticated endpoint; see
// buildSealedRoutingProof.
//
// # A verifier MUST cross-check the nested proof against the §8 text
//
// This and the enclosing ChatSignature are TWO INDEPENDENT STATEMENTS by the
// same key. Neither signature covers the other, and nothing signed says they
// describe the same exchange — the chaining is true at the SIGNER, by
// construction, but adjacency in one JSON object is not attested. So a verifier
// that checks both signatures and stops has verified less than it thinks:
//
//	an intermediary holding cached signature responses for chats A and B — the
//	router does hold them; the chatID travels in ZG-Res-Key and the endpoint is
//	unauthenticated — can serve §8 from A with routing_proof from B. Both
//	signatures verify, signing_address is the right enclave, the fingerprint is a
//	real vendor fingerprint, and the client concludes "the ciphertext I decrypted
//	arrived over TLS to that vendor", which no signature ever said.
//
// The check that closes it is one comparison, and it works only because the
// sealed proof reuses §8's hashes rather than computing its own:
//
//	Text's two hash halves MUST equal routing_proof.Text's first two
//	colon-separated fields. Reject the pair otherwise — do not fall back to
//	trusting §8 alone plus an unbound fingerprint.
//
// Note what this buys and what it does not: it binds the two statements by
// EQUALITY OF VALUES a verifier checks, not by one signature covering the other.
//
// # …and MUST require it to be present on a centralized route
//
// Comparing the halves only catches a MISMATCHED proof. The cheaper move for the
// same intermediary is to delete this object outright, leaving a §8 signature
// that verifies perfectly, from the right enclave, over the right ciphertext. No
// signature covers the field's presence, and the broker legitimately omits it on
// several shapes (decentralized provider, no TLS evidence, an unreadable §8
// text), so stripped and unavailable are byte-identical:
//
//	A verifier that knows the route is centralized MUST require routing_proof to
//	be present. Its absence is a failed verification, not "no vendor evidence
//	available".
//
// The route type has to come from provider metadata, not from the signature
// response under attack: GET /v1/models publishes provider_type per model
// (omitted for decentralized), the same value signed into ProviderType below.
//
// This asymmetry is introduced by the NESTING. On the unsealed shape the evidence
// is inside the one signed text and cannot be stripped — the router can only
// withhold the whole signature, which is detectable as no signature at all. Here
// it is an unsigned optional sibling of a signature that stays valid without it.
//
// Folding the routing evidence into the §8 signed text is the strictly stronger
// end state — it makes both rules unnecessary, since absence and mismatch alike
// become a §8 verification failure — and it is deferred to the routing proof's
// next version because it is a protocol change across every verifier. Until then
// the two obligations above are the contract, and they are stated in
// docs/design/sidecar-routing-proof.md too, since that doc — not the 0g-pc
// protocol package, which has no routing-proof concept — is where verifier
// implementors read them.
type RoutingProof struct {
	Text                string         `json:"text"`
	SignatureEcdsa      string         `json:"signature"`
	SigningAddressEcdsa common.Address `json:"signing_address"`
	SigningAlgo         string         `json:"signing_algo"`
	ProviderType        string         `json:"provider_type"`
	ProviderIdentity    string         `json:"provider_identity"`
	TLSCertFingerprint  string         `json:"tls_cert_fingerprint"`
}

func (*Ctrl) chatCacheKey(chatID string) string {
	return fmt.Sprintf("%s:%s", ChatPrefix, chatID)
}

func sha256Hex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// jcsSha256Hex returns sha256(JCS(b)) as hex. Used by the E2EE content binding
// (§8), where the client verifies the request/response hashes over JCS-canonical
// JSON so Go/TS/Rust agree byte-for-byte.
func jcsSha256Hex(b []byte) (string, error) {
	canon, err := jcs.Transform(b)
	if err != nil {
		return "", fmt.Errorf("jcs canonicalize: %w", err)
	}
	return sha256Hex(canon), nil
}

// signChatE2EE caches the E2EE (0g-pc SPEC §8) response signature under chatKey.
// Unlike signChatWithKey (which binds plaintext), the E2EE signature binds the
// on-wire aad‖ciphertext: the client that just decrypted the response already
// holds those exact bytes, so it can verify without reconstructing or
// re-canonicalizing any plaintext, and Go/TS/Rust hash identical bytes.
//
// text is the scheme-tagged signed text assembled by the shared proof package
// from the exact sealed bytes emitted to the client —
// proof.SignedTextE2EEFromHashes for a non-stream response,
// proof.StreamBinder for a stream. Assembling it there (not here) is what keeps
// the broker's signed bytes and the client's recomputed bytes byte-for-byte
// identical; this function is a thin signer over the finished text.
//
// buildRouting, when non-nil, produces the centralized routing proof to carry
// alongside (nil from it means there is none — a centralized provider whose TLS
// evidence could not be assembled). It is nested rather than merged: §8 stays
// this response's top-level signed statement, so an E2EE client verifies exactly
// what it verified before this field existed.
//
// It is a FUNCTION rather than a finished proof so that ORDER is enforced here:
// §8 is signed first, and buildRouting — which costs a second signer call —
// only runs once §8 is in hand. Both signatures happen before the body is
// flushed (issue #619), and with TEE_SOCKET each SignHash is a controller round
// trip bounded by remoteSignerTimeout, so signing the optional proof first would
// let a wedged controller burn that timeout ahead of the signature the client
// refuses the response without — and on the non-stream path, where a signing
// failure fails the request closed, would double the time to that failure.
// Passing a finished proof cannot express this: building it is the cost.
//
// The result is attached to the SAME cache entry, in one Set. Setting §8 first
// and re-Setting with the proof would publish, for that window, a §8 with no
// routing_proof — which RoutingProof's own MUST-present rule tells a verifier on
// a centralized route to reject.
func (c *Ctrl) signChatE2EE(text, chatKey string, buildRouting func() *RoutingProof) error {
	sig, err := c.teeService.SignHash(accounts.TextHash([]byte(text)))
	if err != nil {
		return err
	}
	if sig[64] == 0 || sig[64] == 1 {
		sig[64] += 27
	}

	chatSignature := ChatSignature{
		Text:                text,
		SignatureEcdsa:      hexutil.Encode(sig),
		SigningAddressEcdsa: c.teeService.Address,
		SigningAlgo:         ECDSA.String(),
	}
	if buildRouting != nil {
		chatSignature.RoutingProof = buildRouting()
	}
	key := c.chatCacheKey(chatKey)
	c.logger.Debugf("e2ee chat signature key: %v", key)
	c.svcCache.Set(key, chatSignature, c.chatCacheExpiration)
	return nil
}

// signChatWithKey signs sha256(reqBody):sha256(respData) and caches the result
// under chatKey. Used by chatbot, video, speech-to-text, and the fallback path
// of text-to-image / image-editing when b64 images cannot be extracted.
func (c *Ctrl) signChatWithKey(reqBody, respData []byte, chatKey string) error {
	requestSha256 := sha256Hex(reqBody)
	responseSha256 := sha256Hex(respData)

	c.logger.Debugf("requestSha256: %s, responseSha256: %s, signer address %s", requestSha256, responseSha256, c.teeService.Address.Hex())
	text := fmt.Sprintf("%s:%s", requestSha256, responseSha256)
	sig, err := c.teeService.SignHash(accounts.TextHash([]byte(text)))
	if err != nil {
		return err
	}

	if sig[64] == 0 || sig[64] == 1 {
		sig[64] += 27
	}

	chatSignature := ChatSignature{
		Text:                text,
		SignatureEcdsa:      hexutil.Encode(sig),
		SigningAddressEcdsa: c.teeService.Address,
		SigningAlgo:         ECDSA.String(),
	}

	key := c.chatCacheKey(chatKey)
	c.logger.Debugf("key: %v, chat signature: %v", key, chatSignature)
	c.svcCache.Set(key, chatSignature, c.chatCacheExpiration)
	return nil
}

// signImageResponse creates a TEE signature for image generation / editing
// responses from a DECENTRALIZED provider. The signed text is:
//
//	sha256(originalClientReqBody):sha256(img0),sha256(img1),...
//
// This binds the signature to actual image bytes rather than the provider
// response JSON (which may contain inaccessible LAN URLs).
//
// Parity note: this is the image-flow counterpart of signChatWithKey, NOT of
// signCentralizedRoutingProof. If image-editing is ever enabled for a
// centralized provider, a sibling function (signImageResponseRoutingProof)
// must be added that also takes the upstream cert fingerprint and emits the TLS
// fingerprint / ProviderType / ProviderIdentity fields — otherwise a verifier
// would see a TEE-signed envelope with no evidence of which upstream served
// the image. The guard below fails loud so the gap cannot be reached silently
// by toggling providerType=centralized with targetSeparated=false in config.
func (c *Ctrl) signImageResponse(reqBody []byte, images [][]byte, chatKey string) error {
	if c.Service.IsCentralized() {
		return fmt.Errorf("signImageResponse does not cover centralized providers; add a routing-proof variant that binds the TLS fingerprint before enabling this path")
	}
	imgHashes := make([]string, len(images))
	for i, img := range images {
		imgHashes[i] = sha256Hex(img)
	}
	text := sha256Hex(reqBody) + ":" + strings.Join(imgHashes, ",")

	sig, err := c.teeService.SignHash(accounts.TextHash([]byte(text)))
	if err != nil {
		return err
	}
	if sig[64] == 0 || sig[64] == 1 {
		sig[64] += 27
	}

	chatSignature := ChatSignature{
		Text:                text,
		SignatureEcdsa:      hexutil.Encode(sig),
		SigningAddressEcdsa: c.teeService.Address,
		SigningAlgo:         ECDSA.String(),
	}

	key := c.chatCacheKey(chatKey)
	c.logger.Debugf("image signature key: %v, sig: %v", key, chatSignature)
	c.svcCache.Set(key, chatSignature, c.chatCacheExpiration)
	return nil
}

// signCentralizedRoutingProof creates a TEE-signed routing proof for centralized
// provider requests. The proof includes request/response hashes, provider identity,
// and the TLS certificate fingerprint proving the connection target.
//
// tlsFingerprint comes from Ctrl.upstreamCertFingerprint (proxy.go), which resolves
// it from either the broker's own resp.TLS or an in-enclave shim's report — this
// function is deliberately indifferent to which, so the proof format and every
// verifier stay unchanged whether or not a protocol translator sits in the path.
//
// providerIdentity is the identity of the upstream that ACTUALLY served this
// request — the per-model identity for a multi-upstream provider (Bailian vs
// Minimax under one provider), so the proof names the real upstream alongside its
// TLS fingerprint instead of a single provider-level label. An empty string falls
// back to the service-level ProviderIdentity, so callers with no resolved model
// (and single-upstream providers) get the previous behaviour unchanged.
func (c *Ctrl) signCentralizedRoutingProof(reqBody, respData []byte, chatKey, tlsFingerprint string, providerIdentity string) error {
	rp, err := c.buildCentralizedRoutingProof(reqBody, respData, tlsFingerprint, providerIdentity)
	if err != nil {
		return err
	}

	// The unsealed shape: the routing proof IS this response's one signed
	// statement, so it goes at the top level. (A sealed response nests it under
	// RoutingProof instead, because there the top level is the §8 binding.)
	chatSignature := ChatSignature{
		Text:                rp.Text,
		SignatureEcdsa:      rp.SignatureEcdsa,
		SigningAddressEcdsa: rp.SigningAddressEcdsa,
		SigningAlgo:         rp.SigningAlgo,
		ProviderType:        rp.ProviderType,
		ProviderIdentity:    rp.ProviderIdentity,
		TLSCertFingerprint:  rp.TLSCertFingerprint,
	}

	key := c.chatCacheKey(chatKey)
	c.logger.Debugf("key: %v, centralized chat signature: %v", key, chatSignature)
	c.svcCache.Set(key, chatSignature, c.chatCacheExpiration)
	return nil
}

// buildCentralizedRoutingProof assembles and signs the routing proof without
// caching it, so the same evidence and the same signed-text format serve both
// callers: signCentralizedRoutingProof, which publishes it as an unsealed
// response's whole signature, and the sealed path, which nests it beside the §8
// binding. Splitting build from publish is what keeps those two from drifting
// into two proof formats.
// It hashes the bytes it is given, which on an UNSEALED response is exactly
// right: the client holds that plaintext and can recompute. A sealed response
// must NOT come through here — see buildSealedRoutingProof.
func (c *Ctrl) buildCentralizedRoutingProof(reqBody, respData []byte, tlsFingerprint string, providerIdentity string) (*RoutingProof, error) {
	return c.routingProofOverHashes(sha256Hex(reqBody), sha256Hex(respData), tlsFingerprint, providerIdentity)
}

// buildSealedRoutingProof is the sealed-path builder: it takes the two binding
// hashes out of the §8 signed text instead of hashing anything itself.
//
// Hashing the bytes signChatResponse is handed would be wrong here in two ways
// that compound. On both chatbot paths those bytes are `clientBody`, which the
// handlers deliberately keep PLAINTEXT for billing while the sealed frames go to
// the wire (see the comments at their declarations), so:
//
//  1. The hashes would be UNVERIFIABLE. A sealed client holds ciphertext and
//     cannot reproduce those plaintext bytes byte-for-byte — nothing
//     canonicalizes them, which is the very reason §8 binds on-wire bytes. Two
//     hashes nobody can check are the "value with nothing behind it" this whole
//     nesting design refuses to publish.
//  2. They would LEAK. /v1/proxy/signature/{chatID} is unauthenticated and the
//     chatID travels in ZG-Res-Key, so the router holds it — and the router is
//     precisely the party E2EE exists to keep plaintext from. Publishing
//     sha256(plaintext request) and sha256(plaintext response) there is a
//     confirmation oracle over low-entropy prompts.
//
// Reading the halves back out of the §8 text also makes the chaining property
// true BY CONSTRUCTION rather than by two call sites agreeing: whatever §8
// bound, this binds, because it is the same string. That is why this takes the
// assembled text rather than hashes plumbed in separately — the streaming binder
// finalizes its aggregate inside Text() and never exposes the halves, so
// plumbing would need a different mechanism per path, and per-path mechanisms
// are how the two sealed surfaces came to bind different things to begin with.
func (c *Ctrl) buildSealedRoutingProof(e2eeSignedText, tlsFingerprint, providerIdentity string) (*RoutingProof, error) {
	reqHash, respHash, ok := e2eeBindingHashes(e2eeSignedText)
	if !ok {
		monitor.RecordRoutingProofSkipped(monitor.RoutingProofSkipSignError)
		return nil, fmt.Errorf("sealed §8 text %q is not <scheme>:<reqHhex>:<respHhex>, so there are no on-wire hashes to bind", truncateForLog([]byte(e2eeSignedText), 80))
	}
	return c.routingProofOverHashes(reqHash, respHash, tlsFingerprint, providerIdentity)
}

// sealedProofSkipCause names why buildSealedRoutingProof refused, for
// logProofSkip's throttle key. Called only on the error path, so re-splitting
// the §8 text costs nothing on a healthy deployment.
//
// It classifies from the inputs rather than the error text because the two
// causes an operator actually hits — a §8 text this cannot read, and no TLS
// evidence captured upstream — need different fixes and must not share a memo
// key. The rest (a non-empty but malformed fingerprint, or an enclave signing
// failure) are caller/enclave bugs rather than deployment faults, and share one
// key deliberately: distinguishing them would mean matching on error strings.
func sealedProofSkipCause(e2eeSignedText, tlsFingerprint string) string {
	if _, _, ok := e2eeBindingHashes(e2eeSignedText); !ok {
		return "unusable_e2ee_text"
	}
	if tlsFingerprint == "" {
		return "no_fingerprint"
	}
	return "sign_failed"
}

// e2eeBindingHashes splits a §8 signed text into its two hex binding hashes.
//
// The format is proof.formatText's "<scheme>:<reqHhex>:<respHhex>" and the
// scheme contains no ':' (only '/'), so a 3-way split is exact. The shape is
// re-validated here rather than trusted — 32 hex bytes each — for the same
// reason the fingerprint is: these values are joined into a ':'-delimited signed
// text that escapes nothing, so a value that could smuggle a delimiter, or a
// caller that passed some other string entirely, has to fail closed rather than
// end up inside an attested statement.
//
// The SCHEME is checked too, not just the arity, and that is the load-bearing
// part rather than belt-and-braces: proof.SchemePlaintext has the same 3-field
// shape with hashes that mean something else entirely (plaintext, not
// aad‖ciphertext). Accepting any 3-field text would let a future caller hand
// this a plaintext binding and get a routing proof attesting on-wire bytes over
// hashes that commit to plaintext — the same "per-path mechanisms bind different
// things" failure buildSealedRoutingProof exists to end, reintroduced one scheme
// later.
func e2eeBindingHashes(signedText string) (reqHash, respHash string, ok bool) {
	parts := strings.Split(signedText, ":")
	if len(parts) != 3 {
		return "", "", false
	}
	if parts[0] != proof.SchemeE2EECiphertext && parts[0] != proof.SchemeE2EECiphertextStream {
		return "", "", false
	}
	for _, h := range parts[1:] {
		if len(h) != 2*sha256.Size {
			return "", "", false
		}
		if _, err := hex.DecodeString(h); err != nil {
			return "", "", false
		}
	}
	return parts[1], parts[2], true
}

// routingProofOverHashes signs the routing proof for two already-hex request and
// response hashes, whatever bytes they commit to. Shared so the sealed and
// unsealed builders differ ONLY in that choice, and cannot diverge in the
// signed-text format, the fingerprint validation, or the metric.
func (c *Ctrl) routingProofOverHashes(requestSha256, responseSha256, tlsFingerprint string, providerIdentity string) (*RoutingProof, error) {
	if providerIdentity == "" {
		providerIdentity = c.Service.ProviderIdentity
	}

	// Refuse to sign without a well-formed fingerprint. An empty one carries a TEE
	// signature with no TLS evidence at all, giving verifiers false confidence.
	// Re-validating the format here (rather than trusting the resolver) is what
	// keeps this true for EVERY source, and it is the last gate before the value is
	// joined into a ':'-delimited signed text that escapes nothing — a value proven
	// to be 32 hex bytes cannot smuggle a delimiter, and a caller that passed the
	// wrong string (chatKey and this are adjacent same-typed parameters) fails
	// closed instead of signing a proof that attests to a UUID.
	normalized, ok := teeutil.NormalizeCertFingerprint(tlsFingerprint)
	if !ok {
		// No metric here when the value is empty: upstreamCertFingerprint already
		// counted that skip with the precise reason (no_tls / no_sidecar_report), and
		// counting again would make the reason label double-count the same lost proof.
		// A non-empty but malformed value never came from the resolver, so it is a
		// caller bug and does get counted.
		if tlsFingerprint != "" {
			monitor.RecordRoutingProofSkipped(monitor.RoutingProofSkipSignError)
		}
		return nil, fmt.Errorf("no usable upstream TLS certificate fingerprint for centralized provider routing proof (response and billing are unaffected)")
	}
	tlsFingerprint = normalized

	text := teeutil.FormatRoutingProofText(
		requestSha256, responseSha256,
		c.Service.ProviderType, providerIdentity,
		tlsFingerprint,
	)

	c.logger.Debugf("Routing proof text: %s, signer address: %s", text, c.teeService.Address.Hex())

	sig, err := c.teeService.SignHash(accounts.TextHash([]byte(text)))
	if err != nil {
		monitor.RecordRoutingProofSkipped(monitor.RoutingProofSkipSignError)
		return nil, fmt.Errorf("failed to sign routing proof: %w", err)
	}

	if sig[64] == 0 || sig[64] == 1 {
		sig[64] += 27
	}

	return &RoutingProof{
		Text:                text,
		SignatureEcdsa:      hexutil.Encode(sig),
		SigningAddressEcdsa: c.teeService.Address,
		SigningAlgo:         ECDSA.String(),
		ProviderType:        c.Service.ProviderType,
		ProviderIdentity:    providerIdentity,
		TLSCertFingerprint:  tlsFingerprint,
	}, nil
}
