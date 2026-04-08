package utils

import (
	"os"
	"path/filepath"
	"testing"
)

func TestGetDatasetBaseDir(t *testing.T) {
	tmpDir := t.TempDir()
	SetDataDir(tmpDir)
	defer SetDataDir("")

	got := GetDatasetBaseDir()
	want := filepath.Join(tmpDir, "datasets")
	if got != want {
		t.Errorf("GetDatasetBaseDir() = %q, want %q", got, want)
	}
}

func TestGetDatasetBaseDirDefault(t *testing.T) {
	saved := dataDir
	dataDir = ""
	defer func() { dataDir = saved }()

	got := GetDatasetBaseDir()
	want := filepath.Join(os.TempDir(), "datasets")
	if got != want {
		t.Errorf("GetDatasetBaseDir() = %q, want %q", got, want)
	}
}

func TestSetDataDir(t *testing.T) {
	original := GetDataDir()
	defer SetDataDir(original)

	SetDataDir("/custom/path")
	if got := GetDataDir(); got != "/custom/path" {
		t.Errorf("GetDataDir() = %q, want /custom/path", got)
	}

	SetDataDir("")
	if got := GetDataDir(); got != "/custom/path" {
		t.Errorf("SetDataDir(\"\") should not clear; got %q", got)
	}
}
