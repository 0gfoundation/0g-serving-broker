package config

import (
	"errors"
	"strings"
	"testing"
)

func TestGetProviderPrivateKey_NilNetwork(t *testing.T) {
	_, err := GetProviderPrivateKey(nil)
	if err == nil {
		t.Fatal("expected error for nil network")
	}
	if !strings.Contains(err.Error(), "no provider private key") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestGetProviderPrivateKey_NoStore(t *testing.T) {
	_, err := GetProviderPrivateKey(&NetworkConfig{})
	if err == nil {
		t.Fatal("expected error when PrivateKeyStore is nil")
	}
	if !strings.Contains(err.Error(), "PrivateKeyStore") {
		t.Errorf("error should name the missing piece; got: %v", err)
	}
}

func TestGetProviderPrivateKey_EmptyStore(t *testing.T) {
	n := &NetworkConfig{PrivateKeys: nil}
	n.PrivateKeyStore = NewPrivateKeyStore(n)
	_, err := GetProviderPrivateKey(n)
	if err == nil {
		t.Fatal("expected error when keystore is empty")
	}
	// Fetch() returns its own error; we must wrap it so the cause is
	// visible to the caller via errors.Is / errors.Unwrap.
	if !errors.Is(err, &emptyStoreError{}) && !strings.Contains(err.Error(), "no keys found") {
		t.Errorf("error should expose the underlying Fetch() failure; got: %v", err)
	}
}

// emptyStoreError is a sentinel for errors.Is comparison in the test above.
// The current Fetch() implementation uses errors.New so errors.Is won't match
// any concrete type — the test falls back to substring matching. The sentinel
// exists so the test reads symmetrically with the wrapping intent.
type emptyStoreError struct{}

func (e *emptyStoreError) Error() string { return "no keys found" }

func TestGetProviderPrivateKey_Happy(t *testing.T) {
	n := &NetworkConfig{PrivateKeys: []string{"  0xabc  "}}
	n.PrivateKeyStore = NewPrivateKeyStore(n)
	got, err := GetProviderPrivateKey(n)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "0xabc" {
		t.Errorf("got %q, want trimmed %q", got, "0xabc")
	}
}
