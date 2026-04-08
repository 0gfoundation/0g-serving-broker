package ctrl

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/0glabs/0g-serving-broker/fine-tuning/internal/utils"
)

func TestIsValidDatasetHash(t *testing.T) {
	tests := []struct {
		name  string
		hash  string
		valid bool
	}{
		{"valid lowercase", "0xabcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890", true},
		{"valid uppercase", "0xABCDEF1234567890ABCDEF1234567890ABCDEF1234567890ABCDEF1234567890", true},
		{"valid mixed case", "0xAbCdEf1234567890aBcDeF1234567890AbCdEf1234567890aBcDeF1234567890", true},
		{"missing 0x prefix", "abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890", false},
		{"too short", "0xabcdef", false},
		{"too long", "0xabcdef1234567890abcdef1234567890abcdef1234567890abcdef12345678901", false},
		{"invalid hex chars", "0xgggggg1234567890abcdef1234567890abcdef1234567890abcdef1234567890", false},
		{"empty string", "", false},
		{"just 0x", "0x", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isValidDatasetHash(tt.hash)
			if got != tt.valid {
				t.Errorf("isValidDatasetHash(%q) = %v, want %v", tt.hash, got, tt.valid)
			}
		})
	}
}

func TestGetUserDatasetStorageSize(t *testing.T) {
	tmpDir := t.TempDir()
	utils.SetDataDir(tmpDir)
	defer utils.SetDataDir("")

	userAddr := "0x1234567890abcdef1234567890abcdef12345678"
	userDir := filepath.Join(tmpDir, "datasets", userAddr)
	if err := os.MkdirAll(userDir, 0755); err != nil {
		t.Fatal(err)
	}

	ctrl := &Ctrl{}

	t.Run("empty directory", func(t *testing.T) {
		size, err := ctrl.GetUserDatasetStorageSize(userAddr)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if size != 0 {
			t.Errorf("expected 0, got %d", size)
		}
	})

	t.Run("with dataset files", func(t *testing.T) {
		f1 := filepath.Join(userDir, "0xabc123")
		f2 := filepath.Join(userDir, "0xdef456")
		if err := os.WriteFile(f1, make([]byte, 1000), 0644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(f2, make([]byte, 2000), 0644); err != nil {
			t.Fatal(err)
		}

		size, err := ctrl.GetUserDatasetStorageSize(userAddr)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if size != 3000 {
			t.Errorf("expected 3000, got %d", size)
		}
	})

	t.Run("excludes temp files", func(t *testing.T) {
		tempFile := filepath.Join(userDir, "temp_abc123")
		if err := os.WriteFile(tempFile, make([]byte, 5000), 0644); err != nil {
			t.Fatal(err)
		}

		size, err := ctrl.GetUserDatasetStorageSize(userAddr)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if size != 3000 {
			t.Errorf("expected 3000 (temp excluded), got %d", size)
		}
	})

	t.Run("includes _hf directory contents", func(t *testing.T) {
		hfDir := filepath.Join(userDir, "0xabc123_hf")
		if err := os.MkdirAll(hfDir, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(hfDir, "data.arrow"), make([]byte, 500), 0644); err != nil {
			t.Fatal(err)
		}

		size, err := ctrl.GetUserDatasetStorageSize(userAddr)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if size != 3500 {
			t.Errorf("expected 3500 (files + hf), got %d", size)
		}
	})

	t.Run("nonexistent user returns 0", func(t *testing.T) {
		size, err := ctrl.GetUserDatasetStorageSize("0xnonexistent")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if size != 0 {
			t.Errorf("expected 0, got %d", size)
		}
	})
}

func TestDeleteDatasetValidation(t *testing.T) {
	ctrl := &Ctrl{}

	t.Run("invalid user address", func(t *testing.T) {
		err := ctrl.DeleteDataset("not-an-address", "0xabcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890")
		if err == nil {
			t.Error("expected error for invalid address")
		}
	})

	t.Run("invalid hash format", func(t *testing.T) {
		err := ctrl.DeleteDataset("0x1234567890abcdef1234567890abcdef12345678", "bad-hash")
		if err == nil {
			t.Error("expected error for invalid hash")
		}
	})
}
