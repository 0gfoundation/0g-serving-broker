package ctrl

import (
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/gowebpki/jcs"

	teeutil "github.com/0glabs/0g-serving-broker/common/tee"
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

// signChatE2EE is the E2EE (0g-pc SPEC §8) variant of signChatWithKey. The
// client verifies the signature over the DECRYPTED content, so the signed text
// binds the JCS-canonical reconstructed request and the JCS-canonical decrypted
// response — not the sealed bytes the client received over the wire.
//
// reqPlaintext is the reconstructed request captured at unseal time (before the
// proxy's upstream rewrites), matching what the client reconstructs.
// respPlaintext is the sanitized cleartext response the broker sealed.
//
// Streaming note (TODO, tracked in 0g-serving-broker#552 and 0g-pc#7): a
// streaming response has no single canonical JSON object, so the exact
// canonicalization the client will recompute is not yet finalized. As a
// provisional binding we hash the ordered concatenation of the delivered
// plaintext frames as-is (whole-frame plaintext concatenation). This MUST be
// reconciled with the client verify implementation before streaming E2EE
// signatures are relied upon.
func (c *Ctrl) signChatE2EE(reqPlaintext, respPlaintext []byte, chatKey string, isStream bool) error {
	requestSha256, err := jcsSha256Hex(reqPlaintext)
	if err != nil {
		return fmt.Errorf("e2ee request hash: %w", err)
	}

	var responseSha256 string
	if isStream {
		// Provisional: ordered plaintext-frame concatenation (see doc comment).
		responseSha256 = sha256Hex(respPlaintext)
	} else {
		responseSha256, err = jcsSha256Hex(respPlaintext)
		if err != nil {
			return fmt.Errorf("e2ee response hash: %w", err)
		}
	}

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
// must be added that also takes *tls.ConnectionState and emits the TLS
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
func (c *Ctrl) signCentralizedRoutingProof(reqBody, respData []byte, chatKey string, tlsState *tls.ConnectionState) error {
	requestSha256 := sha256Hex(reqBody)
	responseSha256 := sha256Hex(respData)

	// Extract TLS certificate fingerprint — refuse to sign without it.
	// A routing proof with an empty fingerprint carries a TEE signature but
	// provides no TLS evidence, giving verifiers a false sense of security.
	certInfo := teeutil.ExtractTLSInfo(tlsState)
	if certInfo == nil || certInfo.PeerCertFingerprint == "" {
		return fmt.Errorf("TLS certificate not available for centralized provider routing proof (response and billing are unaffected)")
	}
	tlsFingerprint := certInfo.PeerCertFingerprint
	c.logger.Debugf("TLS cert fingerprint captured: %s (server: %s)", tlsFingerprint, certInfo.ServerName)

	text := teeutil.FormatRoutingProofText(
		requestSha256, responseSha256,
		c.Service.ProviderType, c.Service.ProviderIdentity,
		tlsFingerprint,
	)

	c.logger.Debugf("Routing proof text: %s, signer address: %s", text, c.teeService.Address.Hex())

	sig, err := crypto.Sign(accounts.TextHash([]byte(text)), c.teeService.ProviderSigner)
	if err != nil {
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
