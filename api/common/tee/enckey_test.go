package tee

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"testing"

	pccrypto "github.com/0gfoundation/0g-pc-e2ee/protocol/crypto"
	"github.com/0gfoundation/0g-pc-e2ee/protocol/wire"
)

// TestGetEncKeyFromClient covers the TeeService.getEncKey path (material from a
// TappdClient → deriveEncKey), which SyncQuote uses. The mock client returns
// fixed key material, so the derived key is deterministic and usable.
func TestGetEncKeyFromClient(t *testing.T) {
	s := &TeeService{}
	priv, pub, err := s.getEncKey(context.Background(), &MockTappdClient{})
	if err != nil {
		t.Fatalf("getEncKey: %v", err)
	}
	if len(pub) != 32 {
		t.Fatalf("enc_pub len = %d, want 32", len(pub))
	}
	if len(priv) == 0 {
		t.Fatal("enc_priv is empty")
	}
	// Deterministic in the client material.
	priv2, pub2, err := s.getEncKey(context.Background(), &MockTappdClient{})
	if err != nil {
		t.Fatalf("getEncKey (2): %v", err)
	}
	if !bytes.Equal(pub, pub2) || !bytes.Equal(priv, priv2) {
		t.Error("getEncKey not deterministic for identical material")
	}
}

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

func TestDeriveEncKeyDeterministic(t *testing.T) {
	priv1, pub1, err := deriveEncKey([]byte("some-tee-derived-material"))
	if err != nil {
		t.Fatalf("deriveEncKey: %v", err)
	}
	priv2, pub2, err := deriveEncKey([]byte("some-tee-derived-material"))
	if err != nil {
		t.Fatalf("deriveEncKey (2): %v", err)
	}
	if !bytes.Equal(pub1, pub2) || !bytes.Equal(priv1, priv2) {
		t.Fatal("derivation is not deterministic for identical material")
	}
	if len(pub1) != 32 {
		t.Fatalf("enc_pub len = %d, want 32", len(pub1))
	}
	// Different material must yield a different key.
	_, pub3, err := deriveEncKey([]byte("other-material"))
	if err != nil {
		t.Fatalf("deriveEncKey (3): %v", err)
	}
	if bytes.Equal(pub1, pub3) {
		t.Fatal("different material produced the same enc_pub")
	}
}

func TestKeyIDMatchesSpec(t *testing.T) {
	_, pub, err := deriveEncKey([]byte("material"))
	if err != nil {
		t.Fatalf("deriveEncKey: %v", err)
	}
	got := keyID(pub)
	want := sha256.Sum256(pub)
	if !bytes.Equal(got, want[:keyIDLen]) {
		t.Errorf("key_id = %x, want %x", got, want[:keyIDLen])
	}
}

// TestEncKeyRoundTripWithProtocol verifies the derived key is usable by the
// 0g-pc protocol wire package: a request sealed to enc_pub opens with the
// derived enc private key. This is the byte-for-byte interop guarantee.
func TestEncKeyRoundTripWithProtocol(t *testing.T) {
	priv, pub, err := deriveEncKey([]byte("material"))
	if err != nil {
		t.Fatalf("deriveEncKey: %v", err)
	}
	signer := "0x2A94D671f1A5e080f75A8164087Cdd35c8442e69"

	// A client ephemeral key (response recipient) — any valid X25519 key.
	_, ephPub, err := pccrypto.GenerateRecipientKey()
	if err != nil {
		t.Fatalf("GenerateRecipientKey: %v", err)
	}

	req := wire.Request{
		"model":    mustJSON(t, "gpt-4o"),
		"messages": mustJSON(t, []map[string]string{{"role": "user", "content": "secret prompt"}}),
	}
	sealed, err := wire.SealRequest(pub, req, []string{"messages"}, signer, ephPub)
	if err != nil {
		t.Fatalf("SealRequest: %v", err)
	}
	opened, err := wire.OpenRequest(priv, sealed)
	if err != nil {
		t.Fatalf("OpenRequest with derived key: %v", err)
	}
	if !bytes.Equal(opened["messages"], req["messages"]) {
		t.Error("opened messages differ from original")
	}
}
