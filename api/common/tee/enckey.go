package tee

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"io"

	pccrypto "github.com/0gfoundation/0g-pc-e2ee/protocol/crypto"
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

	// keyIDLen is the length of key_id = SHA-256(enc_pub)[0:8] (§4.3).
	keyIDLen = 8

	// reportDataSize is the fixed size of a TDX quote's report_data field, in
	// bytes. This is a hardware limit, not a buffer that can be grown.
	reportDataSize = 64

	// reportDataVersion is the report_data layout version (§4.2), written as a
	// big-endian uint32 at reportDataVersionOffset. Bump it on any layout change
	// so a consumer can reject a layout it does not understand. This is a separate
	// version from the _e2ee envelope version (§5, wire.Version) advertised by
	// GET /v1/e2ee/pubkey: they version independent SPEC layers and need not agree.
	reportDataVersion = 1
)

// report_data field offsets within the 64-byte §4.2 layout:
//
//	offset  size  field
//	0       32    enc_pub      X25519 recipient key (RFC 7748 u-coord, little-endian)
//	32      20    signer_addr  secp256k1 Ethereum address, raw bytes
//	52      4     version      uint32, big-endian
//	56      8     reserved     MUST be zero
const (
	reportDataEncPubOffset   = 0
	reportDataSignerOffset   = 32
	reportDataVersionOffset  = 52
	reportDataReservedOffset = 56
)

// buildReportData packs the 64-byte report_data bound into the TDX quote per
// SPEC §4.2, so a client can extract and verify enc_pub and signer_addr straight
// out of a verified attestation instead of trusting a side channel:
//
//	enc_pub(32) ‖ signer_addr(20) ‖ version(4, big-endian) ‖ reserved(8, zero)
//
// report_data is exactly 64 bytes — a hard TDX limit, not a buffer we can grow.
// It is the only hardware-signed side channel in a quote, so it is reserved for
// the essentials that must be self-verifying straight out of a verified quote
// (today: enc_pub, which bootstraps request sealing before any other channel can
// be trusted). To bind additional data in the future, do not cram more fields in
// here until 64 bytes run out, nor mint a separate quote per feature — a quote is
// a full attestation of this enclave, and generating/verifying one per feature
// pays that (expensive) cost repeatedly to carry cheap payloads about the same
// hardware. Instead store a commitment here (a hash, or a Merkle root over
// per-feature leaves), serve the preimages/proofs over plain endpoints, and have
// the client recompute against report_data. Bump reportDataVersion when migrating
// the layout.
func buildReportData(encPub pccrypto.PublicKey, signer common.Address) ([]byte, error) {
	if len(encPub) != 32 {
		return nil, fmt.Errorf("enc_pub must be 32 bytes, got %d", len(encPub))
	}
	rd := make([]byte, reportDataSize)
	copy(rd[reportDataEncPubOffset:reportDataSignerOffset], encPub)
	copy(rd[reportDataSignerOffset:reportDataVersionOffset], signer.Bytes())
	binary.BigEndian.PutUint32(rd[reportDataVersionOffset:reportDataReservedOffset], reportDataVersion)
	// reserved [reportDataReservedOffset:reportDataSize] stays zero.
	return rd, nil
}

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
