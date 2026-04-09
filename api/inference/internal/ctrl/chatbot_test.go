package ctrl

import (
	"crypto/ecdsa"
	"crypto/sha256"
	"encoding/hex"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/patrickmn/go-cache"

	teeutil "github.com/0glabs/0g-serving-broker/common/tee"
	"github.com/0glabs/0g-serving-broker/inference/config"
)

// newChatbotTestCtrl creates a minimal Ctrl with a real signing key for chatbot signing tests.
func newChatbotTestCtrl(t *testing.T, svc config.Service) *Ctrl {
	t.Helper()

	privateKey, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("failed to generate key: %v", err)
	}
	addr := crypto.PubkeyToAddress(privateKey.PublicKey)

	return &Ctrl{
		Service: svc,
		teeService: &teeutil.TeeService{
			ProviderSigner: privateKey,
			Address:        addr,
		},
		svcCache:            cache.New(5*time.Minute, 10*time.Minute),
		chatCacheExpiration: 5 * time.Minute,
		logger:              &testAsyncLoggerImpl{},
	}
}

// recoverSignerAddress recovers the signer address from a ChatSignature.
func recoverSignerAddress(t *testing.T, cs ChatSignature) common.Address {
	t.Helper()

	sigBytes, err := hexutil.Decode(cs.SignatureEcdsa)
	if err != nil {
		t.Fatalf("failed to decode signature: %v", err)
	}

	if sigBytes[64] == 27 || sigBytes[64] == 28 {
		sigBytes[64] -= 27
	}

	hash := accounts.TextHash([]byte(cs.Text))
	pubKey, err := crypto.SigToPub(hash, sigBytes)
	if err != nil {
		t.Fatalf("failed to recover public key: %v", err)
	}
	return crypto.PubkeyToAddress(*pubKey)
}

// ==========================================================================
// signCentralizedRoutingProof
// ==========================================================================

func TestSignCentralizedRoutingProof(t *testing.T) {
	reqBody := []byte(`{"model":"gpt-4","messages":[{"role":"user","content":"hello"}]}`)
	respData := []byte(`{"id":"chatcmpl-123","choices":[{"message":{"content":"hi"}}]}`)
	chatKey := "test-chat-key"

	svc := config.Service{
		ProviderType:     "centralized",
		ProviderIdentity: "openai",
	}
	ctrl := newChatbotTestCtrl(t, svc)

	err := ctrl.signCentralizedRoutingProof(reqBody, respData, chatKey, nil)
	if err != nil {
		t.Fatalf("signCentralizedRoutingProof returned error: %v", err)
	}

	// Retrieve from cache
	val, found := ctrl.svcCache.Get(ctrl.chatCacheKey(chatKey))
	if !found {
		t.Fatal("chat signature not found in cache")
	}
	cs := val.(ChatSignature)

	// Verify fields
	if cs.ProviderType != "centralized" {
		t.Errorf("ProviderType = %q, want %q", cs.ProviderType, "centralized")
	}
	if cs.ProviderIdentity != "openai" {
		t.Errorf("ProviderIdentity = %q, want %q", cs.ProviderIdentity, "openai")
	}
	if cs.SigningAlgo != "ecdsa" {
		t.Errorf("SigningAlgo = %q, want %q", cs.SigningAlgo, "ecdsa")
	}
	if cs.TLSCertFingerprint != "" {
		t.Errorf("TLSCertFingerprint = %q, want empty (nil TLS state)", cs.TLSCertFingerprint)
	}

	// Verify the proof text format
	hashAndEncode := func(b []byte) string {
		h := sha256.Sum256(b)
		return hex.EncodeToString(h[:])
	}
	expectedText := teeutil.FormatRoutingProofText(
		hashAndEncode(reqBody), hashAndEncode(respData),
		"centralized", "openai", "",
	)
	if cs.Text != expectedText {
		t.Errorf("Text = %q, want %q", cs.Text, expectedText)
	}

	// Verify signature recovers to the correct address
	recovered := recoverSignerAddress(t, cs)
	if recovered != ctrl.teeService.Address {
		t.Errorf("recovered address %s != signer address %s", recovered.Hex(), ctrl.teeService.Address.Hex())
	}
}

func TestSignCentralizedRoutingProof_DifferentProviders(t *testing.T) {
	reqBody := []byte(`{"prompt":"test"}`)
	respData := []byte(`{"result":"ok"}`)

	tests := []struct {
		name             string
		providerIdentity string
	}{
		{"openai", "openai"},
		{"anthropic", "anthropic"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := config.Service{
				ProviderType:     "centralized",
				ProviderIdentity: tt.providerIdentity,
			}
			ctrl := newChatbotTestCtrl(t, svc)

			err := ctrl.signCentralizedRoutingProof(reqBody, respData, "key-"+tt.name, nil)
			if err != nil {
				t.Fatalf("error: %v", err)
			}

			val, found := ctrl.svcCache.Get(ctrl.chatCacheKey("key-" + tt.name))
			if !found {
				t.Fatal("not found in cache")
			}
			cs := val.(ChatSignature)

			if cs.ProviderIdentity != tt.providerIdentity {
				t.Errorf("ProviderIdentity = %q, want %q", cs.ProviderIdentity, tt.providerIdentity)
			}

			recovered := recoverSignerAddress(t, cs)
			if recovered != ctrl.teeService.Address {
				t.Errorf("recovered address mismatch")
			}
		})
	}
}

// ==========================================================================
// signChatWithKey (decentralized path — ensure no regression)
// ==========================================================================

func TestSignChatWithKey(t *testing.T) {
	reqBody := []byte(`{"model":"llama","messages":[]}`)
	respData := []byte(`{"choices":[{"message":{"content":"hi"}}]}`)
	chatKey := "decentralized-key"

	svc := config.Service{
		ProviderType: "decentralized",
	}
	ctrl := newChatbotTestCtrl(t, svc)

	err := ctrl.signChatWithKey(reqBody, respData, chatKey)
	if err != nil {
		t.Fatalf("signChatWithKey returned error: %v", err)
	}

	val, found := ctrl.svcCache.Get(ctrl.chatCacheKey(chatKey))
	if !found {
		t.Fatal("not found in cache")
	}
	cs := val.(ChatSignature)

	// Decentralized signatures should NOT have centralized fields
	if cs.ProviderType != "" {
		t.Errorf("ProviderType = %q, want empty for decentralized", cs.ProviderType)
	}
	if cs.ProviderIdentity != "" {
		t.Errorf("ProviderIdentity = %q, want empty for decentralized", cs.ProviderIdentity)
	}
	if cs.TLSCertFingerprint != "" {
		t.Errorf("TLSCertFingerprint = %q, want empty for decentralized", cs.TLSCertFingerprint)
	}

	// Verify text format: requestSha256:responseSha256
	hashAndEncode := func(b []byte) string {
		h := sha256.Sum256(b)
		return hex.EncodeToString(h[:])
	}
	expectedText := hashAndEncode(reqBody) + ":" + hashAndEncode(respData)
	if cs.Text != expectedText {
		t.Errorf("Text = %q, want %q", cs.Text, expectedText)
	}

	recovered := recoverSignerAddress(t, cs)
	if recovered != ctrl.teeService.Address {
		t.Errorf("recovered address %s != signer address %s", recovered.Hex(), ctrl.teeService.Address.Hex())
	}
}

// ==========================================================================
// Signature V-value adjustment (27/28 normalization)
// ==========================================================================

func TestSignatureVValueAdjustment(t *testing.T) {
	svc := config.Service{
		ProviderType:     "centralized",
		ProviderIdentity: "openai",
	}
	ctrl := newChatbotTestCtrl(t, svc)

	for i := 0; i < 10; i++ {
		chatKey := "v-test-" + hex.EncodeToString([]byte{byte(i)})
		reqBody := []byte(`{"i":` + hex.EncodeToString([]byte{byte(i)}) + `}`)
		respData := []byte(`{"r":` + hex.EncodeToString([]byte{byte(i)}) + `}`)

		err := ctrl.signCentralizedRoutingProof(reqBody, respData, chatKey, nil)
		if err != nil {
			t.Fatalf("iteration %d: %v", i, err)
		}

		val, _ := ctrl.svcCache.Get(ctrl.chatCacheKey(chatKey))
		cs := val.(ChatSignature)

		sigBytes, err := hexutil.Decode(cs.SignatureEcdsa)
		if err != nil {
			t.Fatalf("iteration %d: decode sig: %v", i, err)
		}

		v := sigBytes[64]
		if v != 27 && v != 28 {
			t.Errorf("iteration %d: v = %d, want 27 or 28", i, v)
		}
	}
}

// ==========================================================================
// ZG-Res-Key header condition logic
// ==========================================================================

func TestZGResKeyCondition(t *testing.T) {
	tests := []struct {
		name            string
		providerType    string
		targetSeparated bool
		wantResKey      bool
	}{
		{
			name:            "decentralized same network",
			providerType:    "decentralized",
			targetSeparated: false,
			wantResKey:      true,
		},
		{
			name:            "decentralized separate target",
			providerType:    "decentralized",
			targetSeparated: true,
			wantResKey:      false,
		},
		{
			name:            "centralized always sets key",
			providerType:    "centralized",
			targetSeparated: true,
			wantResKey:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := config.Service{
				ProviderType:    tt.providerType,
				TargetSeparated: tt.targetSeparated,
			}

			gotResKey := !svc.TargetSeparated || svc.IsCentralized()
			if gotResKey != tt.wantResKey {
				t.Errorf("ZG-Res-Key should be set = %v, got %v", tt.wantResKey, gotResKey)
			}
		})
	}
}

// ==========================================================================
// chatCacheKey format
// ==========================================================================

func TestChatCacheKey(t *testing.T) {
	ctrl := &Ctrl{}
	key := ctrl.chatCacheKey("abc-123")
	want := "chat:abc-123"
	if key != want {
		t.Errorf("chatCacheKey = %q, want %q", key, want)
	}
}

// ==========================================================================
// TeeService signing round-trip
// ==========================================================================

func TestTeeServiceSigningRoundTrip(t *testing.T) {
	privateKey, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	addr := crypto.PubkeyToAddress(privateKey.PublicKey)

	ts := &teeutil.TeeService{
		ProviderSigner: privateKey,
		Address:        addr,
	}

	msg := []byte("test message")
	hash := accounts.TextHash(msg)
	sig, err := crypto.Sign(hash, ts.ProviderSigner)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}

	pubKey, err := crypto.SigToPub(hash, sig)
	if err != nil {
		t.Fatalf("recover: %v", err)
	}

	if crypto.PubkeyToAddress(*pubKey) != ts.Address {
		t.Error("recovered address doesn't match TeeService address")
	}

	if _, ok := interface{}(ts.ProviderSigner).(*ecdsa.PrivateKey); !ok {
		t.Error("ProviderSigner is not *ecdsa.PrivateKey")
	}
}
