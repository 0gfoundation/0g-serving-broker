package ctrl

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
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
func (c *Ctrl) signChatE2EE(text, chatKey string) error {
	sig, err := crypto.Sign(accounts.TextHash([]byte(text)), c.teeService.ProviderSigner)
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
	sig, err := crypto.Sign(accounts.TextHash([]byte(text)), c.teeService.ProviderSigner)
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

	sig, err := crypto.Sign(accounts.TextHash([]byte(text)), c.teeService.ProviderSigner)
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
func (c *Ctrl) signCentralizedRoutingProof(reqBody, respData []byte, chatKey, tlsFingerprint string) error {
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
		return fmt.Errorf("no usable upstream TLS certificate fingerprint for centralized provider routing proof (response and billing are unaffected)")
	}
	tlsFingerprint = normalized

	text := teeutil.FormatRoutingProofText(
		requestSha256, responseSha256,
		c.Service.ProviderType, c.Service.ProviderIdentity,
		tlsFingerprint,
	)

	c.logger.Debugf("Routing proof text: %s, signer address: %s", text, c.teeService.Address.Hex())

	sig, err := crypto.Sign(accounts.TextHash([]byte(text)), c.teeService.ProviderSigner)
	if err != nil {
		monitor.RecordRoutingProofSkipped(monitor.RoutingProofSkipSignError)
		return fmt.Errorf("failed to sign routing proof: %w", err)
	}

	if sig[64] == 0 || sig[64] == 1 {
		sig[64] += 27
	}

	chatSignature := ChatSignature{
		Text:                text,
		SignatureEcdsa:      hexutil.Encode(sig),
		SigningAddressEcdsa: c.teeService.Address,
		SigningAlgo:         ECDSA.String(),
		ProviderType:        c.Service.ProviderType,
		ProviderIdentity:    c.Service.ProviderIdentity,
		TLSCertFingerprint:  tlsFingerprint,
	}

	key := c.chatCacheKey(chatKey)
	c.logger.Debugf("key: %v, centralized chat signature: %v", key, chatSignature)
	c.svcCache.Set(key, chatSignature, c.chatCacheExpiration)
	return nil
}
