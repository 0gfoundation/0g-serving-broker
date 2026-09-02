package util

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdsa"
	"crypto/rand"
	"fmt"
	"io"
	"os"
	"strings"

	ecies "github.com/ecies/go/v2"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
)

const (
	defaultBufferSize = 64 * 1024 * 1024
)

func GenerateAESKey(keySize int) ([]byte, error) {
	if keySize != 16 && keySize != 24 && keySize != 32 {
		return nil, fmt.Errorf("invalid AES key size. Supported sizes are 16, 24, or 32 bytes")
	}

	key := make([]byte, keySize)
	_, err := io.ReadFull(rand.Reader, key)
	if err != nil {
		return nil, fmt.Errorf("error generating random key: %w", err)
	}

	return key, nil
}

func AesEncrypt(key []byte, plaintext []byte) ([]byte, []byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, nil, err
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, nil, err
	}

	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, nil, err
	}

	ciphertext := gcm.Seal(nonce, nonce, plaintext, nil)
	tag := ciphertext[len(ciphertext)-gcm.Overhead():]

	return ciphertext, tag, nil
}

func AesEncryptLargeFile(key []byte, inputFile, outputFile string) ([]byte, error) {
	inFile, err := os.Open(inputFile)
	if err != nil {
		return nil, fmt.Errorf("failed to open input file: %w", err)
	}
	defer inFile.Close()

	outFile, err := os.Create(outputFile)
	if err != nil {
		return nil, fmt.Errorf("failed to create output file: %w", err)
	}

	success := false
	defer func() {
		outFile.Close()
		if !success {
			os.Remove(outputFile)
		}
	}()

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("failed to create AES cipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("failed to create GCM cipher: %w", err)
	}

	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("failed to generate nonce: %w", err)
	}

	incrementNonce := func(counter []byte) {
		for i := len(counter) - 1; i >= 0; i-- {
			counter[i]++
			if counter[i] != 0 {
				break
			}
		}
	}

	signature := make([]byte, 65)
	if _, err := outFile.Write(append(signature, nonce...)); err != nil {
		return nil, fmt.Errorf("failed to write nonce to output file: %w", err)
	}

	buf := make([]byte, defaultBufferSize)
	tagBuf := new(bytes.Buffer)

	for {
		n, err := inFile.Read(buf)
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("failed to read input file: %w", err)
		}

		ciphertext := gcm.Seal(nil, nonce, buf[:n], nil)
		tagBuf.Write(ciphertext[len(ciphertext)-gcm.Overhead():])

		if _, err := outFile.Write(ciphertext); err != nil {
			return nil, fmt.Errorf("failed to write ciphertext to output file: %w", err)
		}

		incrementNonce(nonce)
	}

	success = true
	return tagBuf.Bytes(), nil
}

func AesDecrypt(key, ciphertext []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}

	nonceSize := gcm.NonceSize()
	if len(ciphertext) < nonceSize {
		return nil, fmt.Errorf("ciphertext too short: %d bytes, need at least %d", len(ciphertext), nonceSize)
	}
	nonce, ciphertext := ciphertext[:nonceSize], ciphertext[nonceSize:]

	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, err
	}

	return plaintext, nil
}

// tagSigSize is the 65-byte ECDSA signature (R=32 + S=32 + V=1) prepended to
// encrypted files by AesEncryptLargeFile for integrity verification.
const tagSigSize = 65

// RecoverSigner recovers the Ethereum address that produced sig over hash.
//
// It accepts both recovery-id conventions in sig[64]: go-ethereum's crypto.Sign
// emits a raw 0/1 (this is what tee.TeeService.SignHash returns, since it calls
// crypto.Sign directly), while a signature that has been through an
// Ethereum-facing path carries 27/28. Subtracting 27 unconditionally underflows a
// raw 0 to 229 and makes SigToPub reject a signature that is perfectly valid.
func RecoverSigner(hash, sig []byte) (common.Address, error) {
	pub, err := RecoverPubkey(hash, sig)
	if err != nil {
		return common.Address{}, err
	}
	return crypto.PubkeyToAddress(*pub), nil
}

// RecoverPubkey is RecoverSigner returning the public key itself, for the one
// caller that persists it (fine-tuning stores the user's pubkey to encrypt the
// model's AES key to). It owns the recovery-id normalisation both share.
func RecoverPubkey(hash, sig []byte) (*ecdsa.PublicKey, error) {
	if len(sig) != tagSigSize {
		return nil, fmt.Errorf("signature must be %d bytes, got %d", tagSigSize, len(sig))
	}
	// Copy: normalising in place would mutate a caller's buffer, and several
	// callers reuse theirs after the recovery.
	normalized := make([]byte, tagSigSize)
	copy(normalized, sig)
	switch normalized[64] {
	case 27, 28:
		normalized[64] -= 27
	case 0, 1:
		// already a raw recovery id
	default:
		return nil, fmt.Errorf("invalid signature recovery id %d", normalized[64])
	}
	pub, err := crypto.SigToPub(hash, normalized)
	if err != nil {
		return nil, fmt.Errorf("recover public key: %w", err)
	}
	return pub, nil
}

// AesDecryptLargeFile decrypts a file produced by AesEncryptLargeFile and
// verifies the TEE signature over its chunk-tag stream.
//
// File format: [65-byte tagSig][12-byte nonce][chunk1 ciphertext+tag][chunk2 ciphertext+tag]...
// Each chunk's ciphertext size = defaultBufferSize + gcm.Overhead() (16 bytes GCM tag).
//
// expectedSigner is the address the artifact's producer signed with. It is
// REQUIRED: AesEncryptLargeFile reserves the leading 65 bytes and the finalizer
// signs Keccak256(tag stream) into them, but every consumer used to skip those
// bytes without looking at them, which made the signature dead metadata — a
// forged or corrupted prefix was indistinguishable from a genuine one as long as
// AES-GCM decryption succeeded. Passing the zero address is rejected rather than
// treated as "skip", so a caller cannot opt out of the check by accident.
//
// Verification happens after the whole tag stream is known, i.e. after the last
// chunk is decrypted, so outputFile is fully written before the signature is
// checked. It is removed on failure (the deferred cleanup below), so a caller
// never sees plaintext from an artifact that failed verification.
func AesDecryptLargeFile(key []byte, inputFile, outputFile string, expectedSigner common.Address) error {
	if expectedSigner == (common.Address{}) {
		return fmt.Errorf("expected TEE signer address is required to verify %s", inputFile)
	}
	inFile, err := os.Open(inputFile)
	if err != nil {
		return fmt.Errorf("failed to open encrypted file: %w", err)
	}
	defer inFile.Close()

	outFile, err := os.Create(outputFile)
	if err != nil {
		return fmt.Errorf("failed to create output file: %w", err)
	}

	success := false
	defer func() {
		outFile.Close()
		if !success {
			os.Remove(outputFile)
		}
	}()

	block, err := aes.NewCipher(key)
	if err != nil {
		return fmt.Errorf("failed to create AES cipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return fmt.Errorf("failed to create GCM cipher: %w", err)
	}

	// Read (not skip) the 65-byte tagSig; it is verified against the recomputed
	// tag stream once the last chunk has been decrypted.
	tagSig := make([]byte, tagSigSize)
	if _, err := io.ReadFull(inFile, tagSig); err != nil {
		return fmt.Errorf("failed to read tag signature: %w", err)
	}

	// Read nonce
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(inFile, nonce); err != nil {
		return fmt.Errorf("failed to read nonce: %w", err)
	}

	incrementNonce := func(counter []byte) {
		for i := len(counter) - 1; i >= 0; i-- {
			counter[i]++
			if counter[i] != 0 {
				break
			}
		}
	}

	chunkSize := defaultBufferSize + gcm.Overhead()
	buf := make([]byte, chunkSize)

	// Rebuild the same stream AesEncryptLargeFile signed: the trailing
	// gcm.Overhead() bytes of every chunk's ciphertext, concatenated in order.
	tagBuf := new(bytes.Buffer)

	for {
		n, readErr := io.ReadFull(inFile, buf)
		if n == 0 {
			break
		}

		plaintext, decErr := gcm.Open(nil, nonce, buf[:n], nil)
		if decErr != nil {
			return fmt.Errorf("failed to decrypt chunk: %w", decErr)
		}
		tagBuf.Write(buf[n-gcm.Overhead() : n])

		if _, err := outFile.Write(plaintext); err != nil {
			return fmt.Errorf("failed to write plaintext: %w", err)
		}

		incrementNonce(nonce)

		if readErr != nil {
			break
		}
	}

	// An empty tag stream means no chunk was decrypted, so there is nothing the
	// signature could attest to. Refuse rather than "verify" sha3 of nothing.
	if tagBuf.Len() == 0 {
		return fmt.Errorf("no ciphertext chunks in %s; nothing to verify", inputFile)
	}

	signer, err := RecoverSigner(crypto.Keccak256(tagBuf.Bytes()), tagSig)
	if err != nil {
		return fmt.Errorf("verify TEE tag signature for %s: %w", inputFile, err)
	}
	if signer != expectedSigner {
		return fmt.Errorf("TEE tag signature signer mismatch for %s: recovered %s, want %s",
			inputFile, signer.Hex(), expectedSigner.Hex())
	}

	success = true
	return nil
}

// ProviderECIESEncrypt encrypts data with the provider wallet's ECIES public key.
// The private key hex string (with or without 0x prefix) is used to derive the public key.
func ProviderECIESEncrypt(providerPrivKeyHex string, plaintext []byte) ([]byte, error) {
	privKey, err := crypto.HexToECDSA(strings.TrimPrefix(providerPrivKeyHex, "0x"))
	if err != nil {
		return nil, fmt.Errorf("parse provider private key: %w", err)
	}
	pubKeyBytes := crypto.FromECDSAPub(&privKey.PublicKey)
	eciesPubKey, err := ecies.NewPublicKeyFromBytes(pubKeyBytes)
	if err != nil {
		return nil, fmt.Errorf("create ECIES public key: %w", err)
	}
	return ecies.Encrypt(eciesPubKey, plaintext)
}

// ProviderECIESDecrypt decrypts data with the provider wallet's ECIES private key.
func ProviderECIESDecrypt(providerPrivKeyHex string, ciphertext []byte) ([]byte, error) {
	privKey, err := crypto.HexToECDSA(strings.TrimPrefix(providerPrivKeyHex, "0x"))
	if err != nil {
		return nil, fmt.Errorf("parse provider private key: %w", err)
	}
	eciesPrivKey := ecies.NewPrivateKeyFromBytes(crypto.FromECDSA(privKey))
	return ecies.Decrypt(eciesPrivKey, ciphertext)
}

func UnmarshalPubkey(pub string) (*ecdsa.PublicKey, error) {
	bytes, err := hexutil.Decode(pub)
	if err != nil {
		return nil, fmt.Errorf("failed to decode public key: %w", err)
	}

	ecdsaPub, err := crypto.UnmarshalPubkey(bytes)
	if err != nil {
		return nil, fmt.Errorf("failed to parse encoded public key: %w", err)
	}

	return ecdsaPub, nil
}

func MarshalPubkey(pub *ecdsa.PublicKey) string {
	pubKeyBytes := crypto.FromECDSAPub(pub)
	return hexutil.Encode(pubKeyBytes)
}
