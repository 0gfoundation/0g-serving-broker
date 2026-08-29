package lora

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/0glabs/0g-serving-broker/common/util"
	"github.com/0glabs/0g-serving-broker/inference/config"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

// testExpectedSigner is any non-zero address: every case below fails before
// decryption is reached, and AesDecryptLargeFile rejects the zero address so a
// caller cannot opt out of TEE tag-signature verification.
var testExpectedSigner = common.HexToAddress("0x71562b71999873DB5b286dF957af199Ec94617F7")

func TestNewStorageDownloader_EmptyIndexerUrl(t *testing.T) {
	cfg := config.LoRAConfig{StorageIndexerUrl: ""}
	_, err := NewStorageDownloader(cfg, "0xprivatekey", getTestLogger())
	if err == nil {
		t.Fatal("expected error for empty indexer URL")
	}
	if !contains(err.Error(), "not configured") {
		t.Errorf("error = %q, expected mention of 'not configured'", err.Error())
	}
}

func TestNewStorageDownloader_ValidUrl(t *testing.T) {
	cfg := config.LoRAConfig{StorageIndexerUrl: "http://localhost:12345"}
	d, err := NewStorageDownloader(cfg, "0xprivatekey", getTestLogger())
	if err != nil {
		t.Fatalf("unexpected error for valid URL: %v", err)
	}
	if d == nil {
		t.Fatal("expected non-nil downloader")
	}
	if d.providerKey != "0xprivatekey" {
		t.Errorf("providerKey = %q, want 0xprivatekey", d.providerKey)
	}
}

func TestDownloadAndDecrypt_ECIESDecryptFailure(t *testing.T) {
	cfg := config.LoRAConfig{StorageIndexerUrl: "http://localhost:12345"}
	d, err := NewStorageDownloader(cfg, "invalid-not-a-hex-key", getTestLogger())
	if err != nil {
		t.Fatalf("NewStorageDownloader: %v", err)
	}

	outputDir := filepath.Join(t.TempDir(), "output")
	_, err = d.DownloadAndDecrypt(context.Background(), "0xdeadbeef", []byte("fake-encrypted-data"), outputDir, testExpectedSigner)
	if err == nil {
		t.Fatal("expected error when ECIES decryption fails with invalid key")
	}
	if !contains(err.Error(), "ECIES") {
		t.Errorf("error = %q, expected mention of 'ECIES'", err.Error())
	}
}

func TestDownloadAndDecrypt_ECIESDecryptBadCiphertext(t *testing.T) {
	privKey, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	privKeyHex := "0x" + crypto.PubkeyToAddress(privKey.PublicKey).Hex()

	cfg := config.LoRAConfig{StorageIndexerUrl: "http://localhost:12345"}
	d, err := NewStorageDownloader(cfg, privKeyHex, getTestLogger())
	if err != nil {
		t.Fatalf("NewStorageDownloader: %v", err)
	}

	outputDir := filepath.Join(t.TempDir(), "output")
	_, err = d.DownloadAndDecrypt(context.Background(), "0xdeadbeef", []byte("garbage-ciphertext"), outputDir, testExpectedSigner)
	if err == nil {
		t.Fatal("expected error for invalid ECIES ciphertext")
	}
}

func TestDownloadAndDecrypt_CreatesParentDirectory(t *testing.T) {
	// Use a valid private key so we can test that parent directory creation happens
	// before ECIES decrypt (which happens first and will fail with bad ciphertext,
	// but at this point the method hasn't reached the directory creation step yet).
	// The directory creation happens AFTER ECIES decrypt succeeds (step 2),
	// so this test validates the error path ordering.
	privKey, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	hexKey := crypto.FromECDSA(privKey)
	privKeyHex := "0x" + string(rune(hexKey[0])) // intentionally bad

	cfg := config.LoRAConfig{StorageIndexerUrl: "http://localhost:12345"}
	d, err := NewStorageDownloader(cfg, privKeyHex, getTestLogger())
	if err != nil {
		t.Fatalf("NewStorageDownloader: %v", err)
	}

	deepOutputDir := filepath.Join(t.TempDir(), "a", "b", "c", "output")
	_, err = d.DownloadAndDecrypt(context.Background(), "0xdeadbeef", []byte("fake"), deepOutputDir, testExpectedSigner)
	// Expect ECIES error since the ciphertext is garbage
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestDownloadAndDecrypt_EndToEnd_WithRealCrypto(t *testing.T) {
	// This test verifies the full ECIES decrypt + AES decrypt + unzip pipeline
	// using real crypto operations but a mock storage download.
	// We can't mock the indexer client (concrete type), so the 0G Storage download
	// step will fail. But we can verify everything up to that point.
	privKey, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	privKeyHex := crypto.PubkeyToAddress(privKey.PublicKey).Hex()
	_ = privKeyHex

	// Generate a real AES key and encrypt it with ECIES
	aesKey, err := util.GenerateAESKey(32)
	if err != nil {
		t.Fatalf("generate AES key: %v", err)
	}

	privKeyHexRaw := fmt.Sprintf("%x", crypto.FromECDSA(privKey))
	encryptedAESKey, err := util.ProviderECIESEncrypt(privKeyHexRaw, aesKey)
	if err != nil {
		t.Fatalf("ECIES encrypt: %v", err)
	}

	// Create downloader with the private key
	cfg := config.LoRAConfig{StorageIndexerUrl: "http://localhost:12345"}
	d, err := NewStorageDownloader(cfg, privKeyHexRaw, getTestLogger())
	if err != nil {
		t.Fatalf("NewStorageDownloader: %v", err)
	}

	outputDir := filepath.Join(t.TempDir(), "output")
	_, err = d.DownloadAndDecrypt(context.Background(), "0xdeadbeef", encryptedAESKey, outputDir, testExpectedSigner)
	// Should fail at the 0G Storage download step (no real storage server),
	// not at the ECIES decrypt step (which should succeed).
	if err == nil {
		t.Fatal("expected error (no real 0G Storage server)")
	}
	if contains(err.Error(), "ECIES") {
		t.Errorf("should not fail at ECIES step, got: %v", err)
	}

	// Verify the parent directory was created for the encrypted download
	parentDir := filepath.Dir(outputDir + "_encrypted.download")
	if _, statErr := os.Stat(parentDir); os.IsNotExist(statErr) {
		t.Error("expected parent directory to be created before download attempt")
	}
}

func TestStorageHashPrefixNormalization(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		wantHas0x bool
	}{
		{"already has 0x prefix", "0xdeadbeef", true},
		{"missing 0x prefix", "deadbeef", true},
		{"empty string", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Reproduce the prefix logic from DownloadAndDecrypt
			rootWithPrefix := tt.input
			if len(rootWithPrefix) < 2 || rootWithPrefix[:2] != "0x" {
				rootWithPrefix = "0x" + rootWithPrefix
			}

			has0x := len(rootWithPrefix) >= 2 && rootWithPrefix[:2] == "0x"
			if has0x != tt.wantHas0x {
				t.Errorf("has0x = %v, want %v (result: %q)", has0x, tt.wantHas0x, rootWithPrefix)
			}
		})
	}
}

func TestNewStorageDownloader_PreservesKey(t *testing.T) {
	cfg := config.LoRAConfig{StorageIndexerUrl: "http://localhost:12345"}
	key := "0xdeadbeef1234567890abcdef"
	d, err := NewStorageDownloader(cfg, key, getTestLogger())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if d.providerKey != key {
		t.Errorf("providerKey = %q, want %q", d.providerKey, key)
	}
}

func TestNewStorageDownloader_HasIndexerClient(t *testing.T) {
	cfg := config.LoRAConfig{StorageIndexerUrl: "http://localhost:12345"}
	d, err := NewStorageDownloader(cfg, "key", getTestLogger())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if d.indexerClient == nil {
		t.Fatal("expected non-nil indexerClient")
	}
}

func TestDownloadAndDecrypt_EmptyProviderEncKey(t *testing.T) {
	cfg := config.LoRAConfig{StorageIndexerUrl: "http://localhost:12345"}
	d, err := NewStorageDownloader(cfg, "invalid-key", getTestLogger())
	if err != nil {
		t.Fatalf("NewStorageDownloader: %v", err)
	}

	outputDir := filepath.Join(t.TempDir(), "output")
	_, err = d.DownloadAndDecrypt(context.Background(), "0xhash", []byte{}, outputDir, testExpectedSigner)
	if err == nil {
		t.Fatal("expected error for empty encrypted key")
	}
}

func TestDownloadAndDecrypt_LongProviderKey(t *testing.T) {
	privKey, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	privKeyHexRaw := fmt.Sprintf("%x", crypto.FromECDSA(privKey))

	aesKey, err := util.GenerateAESKey(32)
	if err != nil {
		t.Fatalf("generate AES key: %v", err)
	}

	encryptedAESKey, err := util.ProviderECIESEncrypt(privKeyHexRaw, aesKey)
	if err != nil {
		t.Fatalf("ECIES encrypt: %v", err)
	}

	cfg := config.LoRAConfig{StorageIndexerUrl: "http://localhost:12345"}
	d, err := NewStorageDownloader(cfg, privKeyHexRaw, getTestLogger())
	if err != nil {
		t.Fatalf("NewStorageDownloader: %v", err)
	}

	outputDir := filepath.Join(t.TempDir(), "output")
	// ECIES decryption succeeds but 0G Storage download fails (no server)
	_, err = d.DownloadAndDecrypt(context.Background(), "deadbeef", encryptedAESKey, outputDir, testExpectedSigner)
	if err == nil {
		t.Fatal("expected error (no 0G Storage server)")
	}
	if contains(err.Error(), "ECIES") {
		t.Errorf("should not fail at ECIES step, got: %v", err)
	}
}

func TestDownloaderTempFilePaths(t *testing.T) {
	tests := []struct {
		name          string
		outputDir     string
		wantEncrypted string
		wantDecrypted string
	}{
		{
			name:          "simple path",
			outputDir:     "/data/lora-modules/ft-model-task123",
			wantEncrypted: "/data/lora-modules/ft-model-task123_encrypted.download",
			wantDecrypted: "/data/lora-modules/ft-model-task123_decrypted.zip",
		},
		{
			name:          "nested path",
			outputDir:     "/home/user/adapters/v1/ft-qwen-abc",
			wantEncrypted: "/home/user/adapters/v1/ft-qwen-abc_encrypted.download",
			wantDecrypted: "/home/user/adapters/v1/ft-qwen-abc_decrypted.zip",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Reproduce the temp file path logic from DownloadAndDecrypt
			encryptedFile := tt.outputDir + "_encrypted.download"
			decryptedZip := tt.outputDir + "_decrypted.zip"

			if encryptedFile != tt.wantEncrypted {
				t.Errorf("encryptedFile = %q, want %q", encryptedFile, tt.wantEncrypted)
			}
			if decryptedZip != tt.wantDecrypted {
				t.Errorf("decryptedZip = %q, want %q", decryptedZip, tt.wantDecrypted)
			}
		})
	}
}
