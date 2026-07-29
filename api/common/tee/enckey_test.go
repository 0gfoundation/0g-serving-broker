package tee

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"testing"

	pccrypto "github.com/0gfoundation/0g-pc-e2ee/protocol/crypto"
	"github.com/0gfoundation/0g-pc-e2ee/protocol/wire"
	"github.com/ethereum/go-ethereum/common"
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

// TestBuildReportDataLayout checks the 64-byte §4.2 layout byte-for-byte:
// enc_pub(32) ‖ signer_addr(20) ‖ version(4, big-endian) ‖ reserved(8, zero).
func TestBuildReportDataLayout(t *testing.T) {
	_, pub, err := deriveEncKey([]byte("material"))
	if err != nil {
		t.Fatalf("deriveEncKey: %v", err)
	}
	signer := common.HexToAddress("0x2A94D671f1A5e080f75A8164087Cdd35c8442e69")

	rd, err := buildReportData(pub, signer)
	if err != nil {
		t.Fatalf("buildReportData: %v", err)
	}
	if len(rd) != reportDataSize {
		t.Fatalf("report_data len = %d, want %d", len(rd), reportDataSize)
	}
	if got := rd[reportDataEncPubOffset:reportDataSignerOffset]; !bytes.Equal(got, pub) {
		t.Errorf("enc_pub = %x, want %x", got, pub)
	}
	if got := rd[reportDataSignerOffset:reportDataVersionOffset]; !bytes.Equal(got, signer.Bytes()) {
		t.Errorf("signer_addr = %x, want %x", got, signer.Bytes())
	}
	if got := binary.BigEndian.Uint32(rd[reportDataVersionOffset:reportDataReservedOffset]); got != reportDataVersion {
		t.Errorf("version = %d, want %d", got, reportDataVersion)
	}
	for i := reportDataReservedOffset; i < reportDataSize; i++ {
		if rd[i] != 0 {
			t.Errorf("reserved byte %d = %d, want 0", i, rd[i])
		}
	}
}

// TestBuildReportDataRejectsBadEncPub ensures a non-32-byte enc_pub is rejected
// rather than silently truncated or misaligned in the layout.
func TestBuildReportDataRejectsBadEncPub(t *testing.T) {
	signer := common.HexToAddress("0x2A94D671f1A5e080f75A8164087Cdd35c8442e69")
	if _, err := buildReportData(pccrypto.PublicKey(make([]byte, 31)), signer); err == nil {
		t.Error("expected error for 31-byte enc_pub, got nil")
	}
}

// TestBindEncPubEnabledDefault checks the TEE_REPORT_DATA_BIND_ENC_PUB gate:
// unset defaults to on; only an explicit falsey value turns it off.
func TestBindEncPubEnabledDefault(t *testing.T) {
	cases := map[string]bool{
		"":      true,
		"true":  true,
		"1":     true,
		"on":    true,
		"false": false,
		"0":     false,
		"off":   false,
		"no":    false,
		"FALSE": false,
	}
	for v, want := range cases {
		t.Setenv(bindEncPubEnvVar, v)
		if got := bindEncPubEnabled(); got != want {
			t.Errorf("bindEncPubEnabled(%q) = %v, want %v", v, got, want)
		}
	}
}

// TestLegacyReportData checks the legacy payload is the ASCII signer address,
// which is exactly what a client decoding report_data as the signer reads.
func TestLegacyReportData(t *testing.T) {
	signer := common.HexToAddress("0x2A94D671f1A5e080f75A8164087Cdd35c8442e69")
	if got := string(legacyReportData(signer)); got != signer.Hex() {
		t.Errorf("legacyReportData = %q, want %q", got, signer.Hex())
	}
}

// TestGetQuoteSelectsLayout checks GetQuote returns the §4.2 quote by default,
// the legacy quote when asked, and falls back to legacy when no §4.2 quote was
// generated.
func TestGetQuoteSelectsLayout(t *testing.T) {
	s := &TeeService{Quote: "legacy-quote", QuoteV2: "v2-quote"}

	if got := s.GetQuote(false); got != "v2-quote" {
		t.Errorf("GetQuote(false) = %q, want §4.2 quote", got)
	}
	if got := s.GetQuote(true); got != "legacy-quote" {
		t.Errorf("GetQuote(true) = %q, want legacy quote", got)
	}

	// Binding disabled → no §4.2 quote → default (legacy=false) falls back.
	s.QuoteV2 = ""
	if got := s.GetQuote(false); got != "legacy-quote" {
		t.Errorf("GetQuote(false) fallback = %q, want legacy quote", got)
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
