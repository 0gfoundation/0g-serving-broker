package util

import (
	"bytes"
	"crypto/rand"
	"os"
	"path/filepath"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	ethcrypto "github.com/ethereum/go-ethereum/crypto"
)

// signTagStream mirrors what Finalizer.finalizeModel does: sign
// Keccak256(tag stream) with the enclave key and write the 65-byte signature
// over the reserved head of the encrypted file. TeeService.SignHash calls
// go-ethereum's crypto.Sign directly, so v arrives as a raw 0/1.
func signTagStream(t *testing.T, encPath string, tag []byte, priv string) common.Address {
	t.Helper()
	key, err := ethcrypto.HexToECDSA(priv)
	if err != nil {
		t.Fatalf("HexToECDSA: %v", err)
	}
	sig, err := ethcrypto.Sign(ethcrypto.Keccak256(tag), key)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if err := WriteToFileHead(encPath, sig); err != nil {
		t.Fatalf("WriteToFileHead: %v", err)
	}
	return ethcrypto.PubkeyToAddress(key.PublicKey)
}

func encryptFixture(t *testing.T, plaintext []byte) (aesKey []byte, plainPath, encPath string, tag []byte) {
	t.Helper()
	dir := t.TempDir()
	plainPath = filepath.Join(dir, "plain.bin")
	encPath = filepath.Join(dir, "enc.data")

	if err := os.WriteFile(plainPath, plaintext, 0600); err != nil {
		t.Fatalf("write plaintext: %v", err)
	}
	aesKey = make([]byte, 32)
	if _, err := rand.Read(aesKey); err != nil {
		t.Fatalf("rand: %v", err)
	}
	tag, err := AesEncryptLargeFile(aesKey, plainPath, encPath)
	if err != nil {
		t.Fatalf("AesEncryptLargeFile: %v", err)
	}
	return aesKey, plainPath, encPath, tag
}

const testEnclaveKey = "b71c71a67e1177ad4e901695e1b4b9ee17ae16c6668d313eac2f96dbcda3f291"

func TestAesDecryptLargeFile_VerifiesTagSignature(t *testing.T) {
	want := []byte("a LoRA adapter archive that only the enclave should be able to vouch for")
	aesKey, _, encPath, tag := encryptFixture(t, want)
	signer := signTagStream(t, encPath, tag, testEnclaveKey)

	out := filepath.Join(t.TempDir(), "decrypted.bin")
	if err := AesDecryptLargeFile(aesKey, encPath, out, signer); err != nil {
		t.Fatalf("AesDecryptLargeFile with the correct signer: %v", err)
	}
	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("round trip mismatch: got %q", got)
	}
}

// The reported defect: mutate ONLY the 65-byte signature prefix, leaving the
// ciphertext and every AES-GCM tag intact. Decryption still succeeds, so the old
// code returned plaintext and unzipped the adapter. Verification must reject it.
func TestAesDecryptLargeFile_ForgedSignaturePrefixRejected(t *testing.T) {
	aesKey, _, encPath, tag := encryptFixture(t, []byte("adapter bytes"))
	signer := signTagStream(t, encPath, tag, testEnclaveKey)

	// Re-sign the same tag stream with a key the enclave does not hold.
	attacker := "4c0883a69102937d6231471b5dbb6204fe512961708279b7e1a8d7d7a3c2b9e3"
	attackerAddr := signTagStream(t, encPath, tag, attacker)
	if attackerAddr == signer {
		t.Fatal("test setup: attacker key must differ from the enclave key")
	}

	out := filepath.Join(t.TempDir(), "decrypted.bin")
	err := AesDecryptLargeFile(aesKey, encPath, out, signer)
	if err == nil {
		t.Fatal("want a signer-mismatch error, got nil (a forged prefix was accepted)")
	}
	if _, statErr := os.Stat(out); !os.IsNotExist(statErr) {
		t.Error("plaintext must not be left on disk when verification fails")
	}
}

func TestAesDecryptLargeFile_CorruptedSignatureRejected(t *testing.T) {
	aesKey, _, encPath, tag := encryptFixture(t, []byte("adapter bytes"))
	signer := signTagStream(t, encPath, tag, testEnclaveKey)

	// Flip a bit inside r, leaving the recovery id valid.
	raw, err := os.ReadFile(encPath)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	raw[3] ^= 0xff
	if err := os.WriteFile(encPath, raw, 0600); err != nil {
		t.Fatalf("write: %v", err)
	}

	out := filepath.Join(t.TempDir(), "decrypted.bin")
	if err := AesDecryptLargeFile(aesKey, encPath, out, signer); err == nil {
		t.Fatal("want an error for a corrupted signature, got nil")
	}
	if _, statErr := os.Stat(out); !os.IsNotExist(statErr) {
		t.Error("plaintext must not be left on disk when verification fails")
	}
}

// The zero address must not be usable as a "skip verification" sentinel.
func TestAesDecryptLargeFile_ZeroSignerRejected(t *testing.T) {
	aesKey, _, encPath, tag := encryptFixture(t, []byte("adapter bytes"))
	signTagStream(t, encPath, tag, testEnclaveKey)

	out := filepath.Join(t.TempDir(), "decrypted.bin")
	if err := AesDecryptLargeFile(aesKey, encPath, out, common.Address{}); err == nil {
		t.Fatal("want an error for the zero expected signer, got nil")
	}
}

// RecoverSigner must accept both recovery-id conventions. TeeService.SignHash
// emits raw 0/1; an Ethereum-facing path emits 27/28. The blind "-27" used
// elsewhere in this repo underflows a raw 0 to 229 and rejects a valid signature.
func TestRecoverSigner_BothRecoveryIDConventions(t *testing.T) {
	key, err := ethcrypto.HexToECDSA(testEnclaveKey)
	if err != nil {
		t.Fatalf("HexToECDSA: %v", err)
	}
	want := ethcrypto.PubkeyToAddress(key.PublicKey)
	hash := ethcrypto.Keccak256([]byte("some tag stream"))

	raw, err := ethcrypto.Sign(hash, key)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if raw[64] != 0 && raw[64] != 1 {
		t.Fatalf("test assumption broken: crypto.Sign emitted v=%d", raw[64])
	}

	got, err := RecoverSigner(hash, raw)
	if err != nil {
		t.Fatalf("RecoverSigner(raw v=%d): %v", raw[64], err)
	}
	if got != want {
		t.Errorf("raw v: got %s, want %s", got, want)
	}

	eth := make([]byte, 65)
	copy(eth, raw)
	eth[64] += 27
	got, err = RecoverSigner(hash, eth)
	if err != nil {
		t.Fatalf("RecoverSigner(eth v=%d): %v", eth[64], err)
	}
	if got != want {
		t.Errorf("eth v: got %s, want %s", got, want)
	}
}

func TestRecoverSigner_RejectsBadInput(t *testing.T) {
	hash := ethcrypto.Keccak256([]byte("x"))
	if _, err := RecoverSigner(hash, make([]byte, 64)); err == nil {
		t.Error("want an error for a 64-byte signature")
	}
	bad := make([]byte, 65)
	bad[64] = 42
	if _, err := RecoverSigner(hash, bad); err == nil {
		t.Error("want an error for an out-of-range recovery id")
	}
}

// defaultBufferSize is 64 MiB, so a file larger than that is the only way to
// exercise the multi-chunk tag stream: the trailing gcm.Overhead() bytes of EACH
// chunk, concatenated in order, with the nonce incremented per chunk.
func TestAesDecryptLargeFile_MultiChunkTagStream(t *testing.T) {
	if testing.Short() {
		t.Skip("allocates >64 MiB")
	}
	dir := t.TempDir()
	plainPath := filepath.Join(dir, "plain.bin")
	encPath := filepath.Join(dir, "enc.data")

	// 64 MiB + 1 MiB → exactly two chunks, the second one short.
	size := defaultBufferSize + 1024*1024
	want := make([]byte, size)
	if _, err := rand.Read(want); err != nil {
		t.Fatalf("rand: %v", err)
	}
	if err := os.WriteFile(plainPath, want, 0600); err != nil {
		t.Fatalf("write: %v", err)
	}

	aesKey := make([]byte, 32)
	if _, err := rand.Read(aesKey); err != nil {
		t.Fatalf("rand: %v", err)
	}
	tag, err := AesEncryptLargeFile(aesKey, plainPath, encPath)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	// Two chunks → two 16-byte GCM tags.
	if len(tag) != 32 {
		t.Fatalf("tag stream is %d bytes, want 32 (two chunks); the fixture is not multi-chunk", len(tag))
	}

	key, err := ethcrypto.HexToECDSA(testEnclaveKey)
	if err != nil {
		t.Fatalf("key: %v", err)
	}
	sig, err := ethcrypto.Sign(ethcrypto.Keccak256(tag), key)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	if err := WriteToFileHead(encPath, sig); err != nil {
		t.Fatalf("write head: %v", err)
	}
	signer := ethcrypto.PubkeyToAddress(key.PublicKey)

	out := filepath.Join(dir, "out.bin")
	if err := AesDecryptLargeFile(aesKey, encPath, out, signer); err != nil {
		t.Fatalf("decrypt+verify across two chunks: %v", err)
	}
	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("multi-chunk round trip mismatch: got %d bytes, want %d", len(got), len(want))
	}
}

// The signature-verification sites across the repo used to hand-roll the
// recovery-id handling, most of them as a bare "sigBytes[64] - 27". These pin the
// two properties every caller now depends on: a raw 0/1 is accepted (so a client
// signing with go-ethereum's crypto.Sign is not refused), and the caller's buffer
// is not mutated (several callers reuse theirs after the recovery).

func TestRecoverPubkey_DoesNotMutateCallerSignature(t *testing.T) {
	key, err := ethcrypto.HexToECDSA(testEnclaveKey)
	if err != nil {
		t.Fatalf("HexToECDSA: %v", err)
	}
	hash := ethcrypto.Keccak256([]byte("payload"))
	sig, err := ethcrypto.Sign(hash, key)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	sig[64] += 27 // the Ethereum-facing form

	before := make([]byte, len(sig))
	copy(before, sig)

	if _, err := RecoverPubkey(hash, sig); err != nil {
		t.Fatalf("RecoverPubkey: %v", err)
	}
	if !bytes.Equal(sig, before) {
		t.Errorf("caller signature was mutated: v went %d → %d", before[64], sig[64])
	}
}

func TestRecoverPubkey_MatchesRecoverSigner(t *testing.T) {
	key, err := ethcrypto.HexToECDSA(testEnclaveKey)
	if err != nil {
		t.Fatalf("HexToECDSA: %v", err)
	}
	hash := ethcrypto.Keccak256([]byte("payload"))
	sig, err := ethcrypto.Sign(hash, key)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}

	pub, err := RecoverPubkey(hash, sig)
	if err != nil {
		t.Fatalf("RecoverPubkey: %v", err)
	}
	addr, err := RecoverSigner(hash, sig)
	if err != nil {
		t.Fatalf("RecoverSigner: %v", err)
	}
	if ethcrypto.PubkeyToAddress(*pub) != addr {
		t.Error("RecoverPubkey and RecoverSigner disagree")
	}
	if addr != ethcrypto.PubkeyToAddress(key.PublicKey) {
		t.Error("recovered the wrong address")
	}
}
