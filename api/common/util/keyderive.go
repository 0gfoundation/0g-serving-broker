package util

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"golang.org/x/crypto/hkdf"
)

// Fallback key derivation for TEE backends that expose no key-derivation service
// of their own (AliCloud, GCP). It exists so those backends can satisfy the one
// invariant the E2EE design depends on — that the provider signing key and the
// enclave encryption key are INDEPENDENT secrets (SPEC §4.1) — without inventing
// an attestation service they do not have.
//
// LIMITATION, stated plainly: the root secret is a file on the container
// filesystem, not material sealed to the enclave measurement. It therefore does
// NOT rotate with the measurement, and an operator who can read the file can
// reproduce every derived key on an unattested machine. Backends with a real
// derivation service (Phala/dstack, via PhalaTappdClient.DeriveKey) are required
// for anything that relies on measurement binding. tee.NewTeeService logs this
// limitation when it selects one of these backends.

const (
	// TEEKeyMaterialRootPath is where the root secret is persisted. Kept at the
	// historical location so an existing deployment reuses the secret it already
	// has rather than minting a second one beside it.
	TEEKeyMaterialRootPath = "/data/tee_key"

	// keyMaterialHKDFInfoPrefix domain-separates this derivation from every other
	// use of the same material. The derivation path is appended to it, which is
	// what makes DeriveKey("/") and DeriveKey("/e2ee-enc") independent.
	keyMaterialHKDFInfoPrefix = "0g-broker/v1/tee-key-material:"

	// rootSecretSize is the size of both the persisted root secret and each
	// derived value, in bytes.
	rootSecretSize = 32
)

// DeriveKeyMaterialForPath returns key material for path, derived from a
// persistent root secret with HKDF-SHA256 and the path as domain-separating info.
//
// The returned value is the hex encoding of 32 derived bytes, matching what
// PhalaTappdClient.DeriveKey returns (dstack's GetKey emits a hex string) and what
// the two consumers expect: tee.getEncKey conditions the STRING's bytes, while
// tee.getSigningKey parses it as hex. Callers must not decode it first.
//
// Distinct paths yield unrelated material. That is the property the previous
// AliCloud and GCP implementations broke by ignoring path entirely: AliCloud
// returned one cached secret for every path, so the secp256k1 signer and the
// X25519 HPKE recipient key were the same secret and either one disclosed the
// other; GCP returned a fresh random key per call, so nothing was reproducible
// across a restart.
//
// IDENTITY ROTATION: on AliCloud the signer key changes with this switch, because
// the old code returned the root file verbatim for path "/" and this returns
// HKDF(root, info=".../"). An existing AliCloud provider therefore comes up with a
// new signer address and must re-register / be re-acknowledged on chain. Deriving
// "/" from the root unchanged would have avoided that and defeated the fix: the
// root would still be recoverable from the signing key, and the enc key with it.
// On GCP nothing is lost — every restart already produced a new address.
func DeriveKeyMaterialForPath(path string) (string, error) {
	return deriveKeyMaterial(TEEKeyMaterialRootPath, path)
}

// deriveKeyMaterial is DeriveKeyMaterialForPath with an injectable root location,
// so tests do not have to write to /data.
func deriveKeyMaterial(rootPath, path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("key derivation path must not be empty")
	}

	root, err := loadOrCreateRootSecret(rootPath)
	if err != nil {
		return "", err
	}

	derived := make([]byte, rootSecretSize)
	r := hkdf.New(sha256.New, root, nil, []byte(keyMaterialHKDFInfoPrefix+path))
	if _, err := io.ReadFull(r, derived); err != nil {
		return "", fmt.Errorf("derive key material for path %q: %w", path, err)
	}
	return hex.EncodeToString(derived), nil
}

// loadOrCreateRootSecret reads the root secret, generating and persisting one on
// first use.
//
// A write failure is returned rather than ignored. The previous code discarded it
// with `_ = os.WriteFile(...)`, so on a read-only or full filesystem every call
// generated a FRESH random secret: the signer address and enc_pub would differ
// between two calls in the same process, and the address published on chain, the
// key clients sealed to, and the key the enclave could actually unseal with would
// all disagree — with no error anywhere.
func loadOrCreateRootSecret(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	switch {
	case err == nil && len(data) > 0:
		// The historical format is the hex-encoded D scalar of a P-256 key, written
		// by the old DeriveKey. Whatever the file holds is treated as opaque root
		// material — HKDF accepts arbitrary bytes — so an existing deployment keeps
		// using the secret it already has.
		//
		// It does NOT keep its signer address: the old code returned this file
		// verbatim for path "/", and HKDF(file, info=".../") is a different key.
		// That is the point. Deriving the signer from the root unchanged would leave
		// the root recoverable from the signer key, and therefore the enc key
		// recoverable from it too — precisely the collapse this package exists to
		// undo. The cost is a one-time identity rotation on these two backends; see
		// the note in DeriveKeyMaterialForPath.
		return data, nil
	case err != nil && !os.IsNotExist(err):
		return nil, fmt.Errorf("read TEE key material at %s: %w", path, err)
	}

	root := make([]byte, rootSecretSize)
	if _, err := io.ReadFull(rand.Reader, root); err != nil {
		return nil, fmt.Errorf("generate TEE key material: %w", err)
	}
	encoded := []byte(hex.EncodeToString(root))

	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, fmt.Errorf("create directory for TEE key material at %s: %w", path, err)
	}
	if err := os.WriteFile(path, encoded, 0600); err != nil {
		return nil, fmt.Errorf(
			"persist TEE key material at %s (without it a fresh secret would be generated on every call, changing the provider identity between calls): %w",
			path, err)
	}
	return encoded, nil
}
