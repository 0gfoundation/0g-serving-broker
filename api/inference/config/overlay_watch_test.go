package config

import (
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// A changed overlay ends in SIGTERM to this process, which is how the broker and the event
// service pick it up. The test catches the signal instead of dying of it.
func TestWatchOverlay_SignalsOnChange(t *testing.T) {
	path := filepath.Join(t.TempDir(), "overlay.yaml")
	if err := os.WriteFile(path, []byte("whitelist:\n  enabled: false\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(OverlayFileEnvVar, path)

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM)
	defer signal.Stop(sig)

	WatchOverlay(20*time.Millisecond, t.Logf)

	select {
	case <-sig:
		t.Fatal("SIGTERM before the overlay changed")
	case <-time.After(100 * time.Millisecond):
	}
	if err := os.WriteFile(path, []byte("whitelist:\n  enabled: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	select {
	case <-sig:
	case <-time.After(2 * time.Second):
		t.Fatal("no SIGTERM after the overlay changed")
	}
}
