package util

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	pccrypto "github.com/0gfoundation/0g-pc-e2ee/protocol/crypto"
	"github.com/cloudflare/circl/hpke"
	"github.com/ethereum/go-ethereum/crypto"
	"golang.org/x/crypto/hkdf"
)

func rootIn(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "data", "tee_key")
}

// The reported critical: DeriveKey ignored its path argument, so the signer path
// "/" and the enc path "/e2ee-enc" returned IDENTICAL material and the two keys
// the design requires to be independent were one secret.
func TestDeriveKeyMaterialForPath_PathsAreIndependent(t *testing.T) {
	root := rootIn(t)

	signer, err := deriveKeyMaterial(root, "/")
	if err != nil {
		t.Fatalf(`deriveKeyMaterial("/"): %v`, err)
	}
	enc, err := deriveKeyMaterial(root, "/e2ee-enc")
	if err != nil {
		t.Fatalf(`deriveKeyMaterial("/e2ee-enc"): %v`, err)
	}

	if signer == enc {
		t.Fatal(`"/" and "/e2ee-enc" returned identical material: the signer key and the enc key are the same secret`)
	}
	if len(signer) != 2*rootSecretSize || len(enc) != 2*rootSecretSize {
		t.Errorf("expected %d hex chars, got %d and %d", 2*rootSecretSize, len(signer), len(enc))
	}
}

// Independence has to hold at the level that matters: holding the secp256k1
// signing key must not let anyone reconstruct the X25519 enc private key. This
// walks the real derivation both consumers use (getSigningKey parses the hex;
// getEncKey HKDFs the STRING's bytes into a KEM seed) and checks the attack in
// the report — "decrypted with the signer key alone" — no longer works.
func TestDeriveKeyMaterialForPath_SignerKeyCannotReconstructEncKey(t *testing.T) {
	root := rootIn(t)

	signerMaterial, err := deriveKeyMaterial(root, "/")
	if err != nil {
		t.Fatalf("derive signer: %v", err)
	}
	encMaterial, err := deriveKeyMaterial(root, "/e2ee-enc")
	if err != nil {
		t.Fatalf("derive enc: %v", err)
	}

	if _, err := crypto.HexToECDSA(signerMaterial); err != nil {
		t.Fatalf("signer material is not a usable secp256k1 key: %v", err)
	}

	// enckey.deriveEncKey, reproduced: HKDF over the material STRING's bytes with
	// the SPEC §4.1 info, then DeriveKeyPair.
	encPrivFrom := func(material string) pccrypto.PrivateKey {
		t.Helper()
		scheme := hpke.KEM_X25519_HKDF_SHA256.Scheme()
		seed := make([]byte, scheme.SeedSize())
		r := hkdf.New(sha256.New, []byte(material), nil, []byte("0g-pc/v1/enc-key"))
		if _, err := r.Read(seed); err != nil {
			t.Fatalf("hkdf: %v", err)
		}
		_, priv := scheme.DeriveKeyPair(seed)
		b, err := priv.MarshalBinary()
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		return pccrypto.PrivateKey(b)
	}

	real := encPrivFrom(encMaterial)
	// What the attacker in the report had: the signing key, nothing else.
	fromSigner := encPrivFrom(signerMaterial)

	if string(real) == string(fromSigner) {
		t.Fatal("the enc private key is reconstructible from the signing key alone")
	}
}

func TestDeriveKeyMaterialForPath_DeterministicAcrossCalls(t *testing.T) {
	root := rootIn(t)

	first, err := deriveKeyMaterial(root, "/e2ee-enc")
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	// A second call must reuse the persisted root, not mint a new one — the GCP
	// bug was a fresh random key per call, which invalidated the published enc_pub
	// and the on-chain signer address on every restart.
	second, err := deriveKeyMaterial(root, "/e2ee-enc")
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if first != second {
		t.Errorf("not deterministic across calls: %s != %s", first, second)
	}
}

func TestDeriveKeyMaterialForPath_ReusesExistingLegacyFile(t *testing.T) {
	root := rootIn(t)
	if err := os.MkdirAll(filepath.Dir(root), 0700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// Historical format: hex of a P-256 D scalar, written by the old DeriveKey.
	legacy := "33c0ec54281f5219670407215fb82975fcb354b432849be1529ae07a3753ce36"
	if err := os.WriteFile(root, []byte(legacy), 0600); err != nil {
		t.Fatalf("seed legacy file: %v", err)
	}

	got, err := deriveKeyMaterial(root, "/")
	if err != nil {
		t.Fatalf("derive: %v", err)
	}
	if got == legacy {
		t.Error("returned the root verbatim; the signing key must be derived from it, or the root stays recoverable from the signer")
	}

	// The file must be left untouched, so a deployment keeps one root secret.
	after, err := os.ReadFile(root)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(after) != legacy {
		t.Errorf("root file was rewritten: %s", after)
	}

	// And the derivation must be reproducible from that same root.
	again, err := deriveKeyMaterial(root, "/")
	if err != nil {
		t.Fatalf("re-derive: %v", err)
	}
	if again != got {
		t.Error("derivation from an existing root is not reproducible")
	}
}

func TestLoadOrCreateRootSecret_PersistsWith0600(t *testing.T) {
	root := rootIn(t)
	if _, err := deriveKeyMaterial(root, "/"); err != nil {
		t.Fatalf("derive: %v", err)
	}
	fi, err := os.Stat(root)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0600 {
		t.Errorf("root secret mode = %04o, want 0600", perm)
	}
	if _, err := hex.DecodeString(string(mustRead(t, root))); err != nil {
		t.Errorf("root secret is not hex: %v", err)
	}
}

// A write failure must surface. The old code discarded it, so a read-only /data
// meant a fresh random secret on every call and an identity that changed
// underneath the caller with no error.
func TestLoadOrCreateRootSecret_WriteFailureIsReported(t *testing.T) {
	dir := t.TempDir()
	// Make the parent a file, so both MkdirAll and WriteFile beneath it fail.
	blocker := filepath.Join(dir, "blocked")
	if err := os.WriteFile(blocker, []byte("x"), 0600); err != nil {
		t.Fatalf("setup: %v", err)
	}

	if _, err := deriveKeyMaterial(filepath.Join(blocker, "tee_key"), "/"); err == nil {
		t.Fatal("want an error when the root secret cannot be persisted, got nil")
	}
}

func TestDeriveKeyMaterialForPath_EmptyPathRejected(t *testing.T) {
	if _, err := deriveKeyMaterial(rootIn(t), ""); err == nil {
		t.Error("want an error for an empty derivation path")
	}
}

func mustRead(t *testing.T, p string) []byte {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read %s: %v", p, err)
	}
	return b
}
