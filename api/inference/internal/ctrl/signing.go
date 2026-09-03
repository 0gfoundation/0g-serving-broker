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
// for the nested case, and on a sealed request the two hashes are over the
// on-wire ciphertext, so this proof says "this TLS connection to that vendor
// produced exactly the bytes §8 binds to your plaintext". The two chain.
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
// routing is the centralized routing proof to carry alongside, or nil when there
// is none (any non-centralized provider, or a centralized one whose TLS evidence
// could not be assembled). It is nested rather than merged: §8 stays this
// response's top-level signed statement, so an E2EE client verifies exactly what
// it verified before this field existed.
func (c *Ctrl) signChatE2EE(text, chatKey string, routing *RoutingProof) error {
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
		RoutingProof:        routing,
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
func (c *Ctrl) buildCentralizedRoutingProof(reqBody, respData []byte, tlsFingerprint string, providerIdentity string) (*RoutingProof, error) {
	if providerIdentity == "" {
		providerIdentity = c.Service.ProviderIdentity
	}
	requestSha256 := sha256Hex(reqBody)
	responseSha256 := sha256Hex(respData)

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
