package tee

import (
	"crypto/sha256"
	"io"

	pccrypto "github.com/0gfoundation/0g-pc-e2ee/protocol/crypto"
	"github.com/cloudflare/circl/hpke"
	"golang.org/x/crypto/hkdf"

	"github.com/0glabs/0g-serving-broker/common/errors"
)

// E2EE (0g-pc) enclave encryption key, per SPEC.md §4.
//
// The enclave publishes an X25519 HPKE recipient key so a client can seal the
// sensitive request fields to this enclave (SPEC §5–§6). It is a SEPARATE key
// from the secp256k1 signer: the signer is the stable on-chain identity, while
// the enc key is derived from a distinct path and can be rotated independently
// for prompt forward-secrecy (§4.1). Both keys are bound into the same quote via
// report_data (§4.2), so a verifier extracts enc_pub directly from a verified
// quote — no side channel.

const (
	// encKeyDerivePath is the TEE key-derivation path for the enc key. It MUST be
	// distinct from the signer key's path ("/") so the two keys are independent
	// (§4.1).
	encKeyDerivePath = "/e2ee-enc"

	// encKeyHKDFInfo domain-separates the HKDF expansion that turns the raw
	// TEE-derived material into the KEM seed, so this key stream can never collide
	// with another use of the same material.
	encKeyHKDFInfo = "0g-pc/v1/enc-key"

	// keyIDLen is the length of key_id = SHA-256(enc_pub)[0:8] (§4.3).
	keyIDLen = 8
)

// TODO(#601 §4.2): bind enc_pub into the quote's report_data
// (enc_pub(32) ‖ signer_addr(20) ‖ version(4) ‖ reserved(8)) so a client can
// extract and verify the enc key directly from a verified attestation. Deferred
// to get the E2EE flow working first; enc_pub is currently published only via
// GET /v1/e2ee/pubkey, so it is not yet attestation-bound.

// encKemScheme is the HPKE KEM used for the enc key: DHKEM(X25519, HKDF-SHA256),
// matching the 0g-pc protocol suite (SPEC §3). Keys marshaled by this scheme are
// byte-compatible with the protocol/crypto package the request/response envelopes
// use, so a key derived here can be handed straight to wire.OpenRequest.
var encKemScheme = hpke.KEM_X25519_HKDF_SHA256.Scheme()

// deriveEncKey turns raw TEE-derived key material into a deterministic X25519
// HPKE keypair (§4.1). The material comes from the enclave's key-derivation
// service on a path distinct from the signer key, so the keypair is measurement-
// tied and rotates with the measurement; HKDF conditions it to the KEM seed size
// with domain separation. The returned keys are the scheme's marshaled bytes,
// interchangeable with protocol/crypto's PrivateKey/PublicKey.
func deriveEncKey(material []byte) (pccrypto.PrivateKey, pccrypto.PublicKey, error) {
	if len(material) == 0 {
		return nil, nil, errors.New("empty enc key material")
	}
	seed := make([]byte, encKemScheme.SeedSize())
	r := hkdf.New(sha256.New, material, nil, []byte(encKeyHKDFInfo))
	if _, err := io.ReadFull(r, seed); err != nil {
		return nil, nil, errors.Wrap(err, "derive enc key seed")
	}
	pub, priv := encKemScheme.DeriveKeyPair(seed)
	pubBytes, err := pub.MarshalBinary()
	if err != nil {
		return nil, nil, errors.Wrap(err, "marshal enc public key")
	}
	privBytes, err := priv.MarshalBinary()
	if err != nil {
		return nil, nil, errors.Wrap(err, "marshal enc private key")
	}
	return pccrypto.PrivateKey(privBytes), pccrypto.PublicKey(pubBytes), nil
}

// keyID returns key_id = SHA-256(enc_pub)[0:8] (§4.3), used by a client to select
// the right enc key across rotations.
func keyID(encPub pccrypto.PublicKey) []byte {
	h := sha256.Sum256(encPub)
	return h[:keyIDLen]
}
