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

// AesDecryptLargeFile decrypts a file produced by AesEncryptLargeFile.
// File format: [65-byte tagSig][12-byte nonce][chunk1 ciphertext+tag][chunk2 ciphertext+tag]...
// Each chunk's ciphertext size = defaultBufferSize + gcm.Overhead() (16 bytes GCM tag).
func AesDecryptLargeFile(key []byte, inputFile, outputFile string) error {
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

	// Skip 65-byte tagSig
	if _, err := io.ReadFull(inFile, make([]byte, tagSigSize)); err != nil {
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

	for {
		n, readErr := io.ReadFull(inFile, buf)
		if n == 0 {
			break
		}

		plaintext, decErr := gcm.Open(nil, nonce, buf[:n], nil)
		if decErr != nil {
			return fmt.Errorf("failed to decrypt chunk: %w", decErr)
		}

		if _, err := outFile.Write(plaintext); err != nil {
			return fmt.Errorf("failed to write plaintext: %w", err)
		}

		incrementNonce(nonce)

		if readErr != nil {
			break
		}
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
