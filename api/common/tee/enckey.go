package tee

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"io"

	pccrypto "github.com/0gfoundation/0g-pc/protocol/crypto"
	"github.com/cloudflare/circl/hpke"
	"github.com/ethereum/go-ethereum/common"
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

	// reportDataVersion is the report_data layout version written at bytes [52:56]
	// (§4.2). Bumped only on a breaking change to the layout.
	reportDataVersion uint32 = 1

	// reportDataSize is the fixed TDX report_data length (§4.2).
	reportDataSize = 64
	// encPubLen is the X25519 public key length.
	encPubLen = 32
	// keyIDLen is the length of key_id = SHA-256(enc_pub)[0:8] (§4.3).
	keyIDLen = 8
)

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

// buildReportData assembles the 64-byte report_data payload (§4.2):
//
//	offset  size  field
//	0       32    enc_pub      X25519 public key
//	32      20    signer_addr  secp256k1 Ethereum address
//	52      4     version      uint32 big-endian (= reportDataVersion)
//	56      8     reserved     zero
//
// This binds both keys into one attestation. It is a breaking change from the
// legacy layout (signer address hex), gated by the version field.
func buildReportData(encPub pccrypto.PublicKey, signer common.Address) ([]byte, error) {
	if len(encPub) != encPubLen {
		return nil, fmt.Errorf("enc_pub must be %d bytes, got %d", encPubLen, len(encPub))
	}
	rd := make([]byte, reportDataSize)
	copy(rd[0:32], encPub)
	copy(rd[32:52], signer.Bytes()) // common.Address is exactly 20 bytes
	binary.BigEndian.PutUint32(rd[52:56], reportDataVersion)
	// rd[56:64] left zero (reserved).
	return rd, nil
}
