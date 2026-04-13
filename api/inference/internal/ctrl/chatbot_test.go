package ctrl

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"math/big"
	"strings"
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

// generateTestCert creates a self-signed certificate for testing TLS state.
func generateTestCert(t *testing.T, cn string) *x509.Certificate {
	t.Helper()

	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("failed to generate key: %v", err)
	}

	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    time.Now(),
		NotAfter:     time.Now().Add(time.Hour),
	}

	der, err := x509.CreateCertificate(rand.Reader, template, template, &priv.PublicKey, priv)
	if err != nil {
		t.Fatalf("failed to create certificate: %v", err)
	}

	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("failed to parse certificate: %v", err)
	}
	return cert
}

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

func TestSignCentralizedRoutingProof_NilTLSState(t *testing.T) {
	reqBody := []byte(`{"model":"gpt-4","messages":[{"role":"user","content":"hello"}]}`)
	respData := []byte(`{"id":"chatcmpl-123","choices":[{"message":{"content":"hi"}}]}`)
	chatKey := "test-chat-key"

	svc := config.Service{
		ProviderType:     "centralized",
		ProviderIdentity: "openai",
	}
	ctrl := newChatbotTestCtrl(t, svc)

	// Without TLS state, signing must refuse — a proof with an empty
	// fingerprint would give verifiers false security.
	err := ctrl.signCentralizedRoutingProof(reqBody, respData, chatKey, nil)
	if err == nil {
		t.Fatal("expected error when TLS state is nil")
	}
	if !strings.Contains(err.Error(), "TLS certificate not available") {
		t.Errorf("unexpected error message: %v", err)
	}

	// Nothing should be cached
	if _, found := ctrl.svcCache.Get(ctrl.chatCacheKey(chatKey)); found {
		t.Error("signature should not be cached when TLS state is missing")
	}
}

func TestSignCentralizedRoutingProof_WithTLSState(t *testing.T) {
	reqBody := []byte(`{"model":"gpt-4","messages":[{"role":"user","content":"hello"}]}`)
	respData := []byte(`{"id":"chatcmpl-123","choices":[{"message":{"content":"hi"}}]}`)
	chatKey := "tls-test-key"

	svc := config.Service{
		ProviderType:     "centralized",
		ProviderIdentity: "openai",
	}
	ctrl := newChatbotTestCtrl(t, svc)

	// Create a TLS connection state with a real certificate
	cert := generateTestCert(t, "api.openai.com")
	tlsState := &tls.ConnectionState{
		PeerCertificates: []*x509.Certificate{cert},
		ServerName:       "api.openai.com",
	}

	err := ctrl.signCentralizedRoutingProof(reqBody, respData, chatKey, tlsState)
	if err != nil {
		t.Fatalf("signCentralizedRoutingProof returned error: %v", err)
	}

	val, found := ctrl.svcCache.Get(ctrl.chatCacheKey(chatKey))
	if !found {
		t.Fatal("chat signature not found in cache")
	}
	cs := val.(ChatSignature)

	// TLS fingerprint should be non-empty and match the cert
	expectedFingerprint := teeutil.CertFingerprintFromX509(cert)
	if cs.TLSCertFingerprint != expectedFingerprint {
		t.Errorf("TLSCertFingerprint = %q, want %q", cs.TLSCertFingerprint, expectedFingerprint)
	}
	if len(cs.TLSCertFingerprint) != 64 {
		t.Errorf("TLSCertFingerprint length = %d, want 64 hex chars", len(cs.TLSCertFingerprint))
	}

	// Verify the fingerprint is included in the routing proof text
	hashAndEncode := func(b []byte) string {
		h := sha256.Sum256(b)
		return hex.EncodeToString(h[:])
	}
	expectedText := teeutil.FormatRoutingProofText(
		hashAndEncode(reqBody), hashAndEncode(respData),
		"centralized", "openai", expectedFingerprint,
	)
	if cs.Text != expectedText {
		t.Errorf("Text = %q, want %q", cs.Text, expectedText)
	}

	// Verify the proof text contains the fingerprint (not empty trailing colon)
	parts := strings.Split(cs.Text, ":")
	if len(parts) != 5 {
		t.Fatalf("expected 5 colon-separated parts, got %d", len(parts))
	}
	if parts[4] != expectedFingerprint {
		t.Errorf("proof text fingerprint part = %q, want %q", parts[4], expectedFingerprint)
	}

	// Verify signature is valid
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
		certCN           string
	}{
		{"openai", "openai", "api.openai.com"},
		{"anthropic", "anthropic", "api.anthropic.com"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := config.Service{
				ProviderType:     "centralized",
				ProviderIdentity: tt.providerIdentity,
			}
			ctrl := newChatbotTestCtrl(t, svc)

			cert := generateTestCert(t, tt.certCN)
			tlsState := &tls.ConnectionState{
				PeerCertificates: []*x509.Certificate{cert},
				ServerName:       tt.certCN,
			}

			err := ctrl.signCentralizedRoutingProof(reqBody, respData, "key-"+tt.name, tlsState)
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

	cert := generateTestCert(t, "api.openai.com")
	tlsState := &tls.ConnectionState{
		PeerCertificates: []*x509.Certificate{cert},
		ServerName:       "api.openai.com",
	}

	for i := 0; i < 10; i++ {
		chatKey := "v-test-" + hex.EncodeToString([]byte{byte(i)})
		reqBody := []byte(`{"i":` + hex.EncodeToString([]byte{byte(i)}) + `}`)
		respData := []byte(`{"r":` + hex.EncodeToString([]byte{byte(i)}) + `}`)

		err := ctrl.signCentralizedRoutingProof(reqBody, respData, chatKey, tlsState)
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

// ==========================================================================
// MessageContent JSON marshaling/unmarshaling
// ==========================================================================

func TestMessageContent_UnmarshalString(t *testing.T) {
	input := `"Hello, world!"`
	var mc MessageContent
	if err := json.Unmarshal([]byte(input), &mc); err != nil {
		t.Fatalf("UnmarshalJSON error: %v", err)
	}
	if mc.Text != "Hello, world!" {
		t.Errorf("Text = %q, want %q", mc.Text, "Hello, world!")
	}
	if mc.Parts != nil {
		t.Error("Parts should be nil for string content")
	}
}

func TestMessageContent_UnmarshalMultimodal(t *testing.T) {
	input := `[{"type":"text","text":"Describe this image"},{"type":"image_url","image_url":{"url":"data:image/png;base64,iVBOR","detail":"high"}}]`
	var mc MessageContent
	if err := json.Unmarshal([]byte(input), &mc); err != nil {
		t.Fatalf("UnmarshalJSON error: %v", err)
	}
	if len(mc.Parts) != 2 {
		t.Fatalf("Parts length = %d, want 2", len(mc.Parts))
	}
	if mc.Parts[0].Type != "text" || mc.Parts[0].Text != "Describe this image" {
		t.Errorf("Parts[0] = %+v, want text part", mc.Parts[0])
	}
	if mc.Parts[1].Type != "image_url" {
		t.Errorf("Parts[1].Type = %q, want %q", mc.Parts[1].Type, "image_url")
	}
	if mc.Parts[1].ImageURL == nil {
		t.Fatal("Parts[1].ImageURL is nil")
	}
	if mc.Parts[1].ImageURL.URL != "data:image/png;base64,iVBOR" {
		t.Errorf("ImageURL.URL = %q, want data URI", mc.Parts[1].ImageURL.URL)
	}
	if mc.Parts[1].ImageURL.Detail != "high" {
		t.Errorf("ImageURL.Detail = %q, want %q", mc.Parts[1].ImageURL.Detail, "high")
	}
	// Text should be populated from text parts
	if mc.Text != "Describe this image" {
		t.Errorf("Text = %q, want %q", mc.Text, "Describe this image")
	}
}

func TestMessageContent_UnmarshalNull(t *testing.T) {
	input := `null`
	var mc MessageContent
	if err := json.Unmarshal([]byte(input), &mc); err != nil {
		t.Fatalf("UnmarshalJSON error: %v", err)
	}
	if mc.Text != "" {
		t.Errorf("Text = %q, want empty", mc.Text)
	}
	if mc.Parts != nil {
		t.Error("Parts should be nil")
	}
}

func TestMessageContent_MarshalString(t *testing.T) {
	mc := MessageContent{Text: "Hello"}
	data, err := mc.MarshalJSON()
	if err != nil {
		t.Fatalf("MarshalJSON error: %v", err)
	}
	if string(data) != `"Hello"` {
		t.Errorf("MarshalJSON = %s, want %q", data, `"Hello"`)
	}
}

func TestMessageContent_MarshalMultimodal(t *testing.T) {
	mc := MessageContent{
		Parts: []ContentPart{
			{Type: "text", Text: "Describe"},
			{Type: "image_url", ImageURL: &ImageURL{URL: "data:image/png;base64,abc"}},
		},
	}
	data, err := mc.MarshalJSON()
	if err != nil {
		t.Fatalf("MarshalJSON error: %v", err)
	}
	// Should marshal as array, not string
	if data[0] != '[' {
		t.Errorf("expected array JSON, got: %s", data)
	}

	// Round-trip: unmarshal back
	var mc2 MessageContent
	if err := json.Unmarshal(data, &mc2); err != nil {
		t.Fatalf("round-trip unmarshal error: %v", err)
	}
	if len(mc2.Parts) != 2 {
		t.Errorf("round-trip Parts length = %d, want 2", len(mc2.Parts))
	}
}

func TestRequestBody_MultimodalRoundTrip(t *testing.T) {
	// Simulate a full OpenAI vision request body
	input := `{
		"messages": [
			{"role": "system", "content": "You are a helpful assistant."},
			{"role": "user", "content": [
				{"type": "text", "text": "What is in this image?"},
				{"type": "image_url", "image_url": {"url": "data:image/jpeg;base64,/9j/4AAQ"}}
			]}
		]
	}`

	var body RequestBody
	if err := json.Unmarshal([]byte(input), &body); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	if len(body.Messages) != 2 {
		t.Fatalf("Messages length = %d, want 2", len(body.Messages))
	}

	// First message: plain string content
	if body.Messages[0].Role != "system" {
		t.Errorf("Messages[0].Role = %q, want %q", body.Messages[0].Role, "system")
	}
	if body.Messages[0].Content.Text != "You are a helpful assistant." {
		t.Errorf("Messages[0].Content.Text = %q", body.Messages[0].Content.Text)
	}
	if body.Messages[0].Content.Parts != nil {
		t.Error("Messages[0].Content.Parts should be nil for string content")
	}

	// Second message: multimodal content
	if body.Messages[1].Role != "user" {
		t.Errorf("Messages[1].Role = %q, want %q", body.Messages[1].Role, "user")
	}
	if len(body.Messages[1].Content.Parts) != 2 {
		t.Fatalf("Messages[1].Content.Parts length = %d, want 2", len(body.Messages[1].Content.Parts))
	}
	if body.Messages[1].Content.Parts[1].ImageURL == nil {
		t.Fatal("image_url part should have ImageURL")
	}
	if body.Messages[1].Content.Parts[1].ImageURL.URL != "data:image/jpeg;base64,/9j/4AAQ" {
		t.Errorf("ImageURL.URL = %q", body.Messages[1].Content.Parts[1].ImageURL.URL)
	}
	// Text should be populated from text parts only
	if body.Messages[1].Content.Text != "What is in this image?" {
		t.Errorf("Messages[1].Content.Text = %q, want %q", body.Messages[1].Content.Text, "What is in this image?")
	}

	// Round-trip: marshal back and ensure it can be re-parsed
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}
	var body2 RequestBody
	if err := json.Unmarshal(data, &body2); err != nil {
		t.Fatalf("round-trip unmarshal error: %v", err)
	}
	if len(body2.Messages) != 2 {
		t.Errorf("round-trip Messages length = %d, want 2", len(body2.Messages))
	}
	if len(body2.Messages[1].Content.Parts) != 2 {
		t.Errorf("round-trip Parts length = %d, want 2", len(body2.Messages[1].Content.Parts))
	}
}

func TestResponseMessage_UnmarshalStringContent(t *testing.T) {
	// LLM responses always have string content — ensure ResponseMessage still works
	input := `{"id":"chatcmpl-123","choices":[{"message":{"role":"assistant","content":"The image shows a cat."},"delta":{"content":""},"finish_reason":"stop"}]}`
	var chunk CompletionChunk
	if err := json.Unmarshal([]byte(input), &chunk); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	if len(chunk.Choices) != 1 {
		t.Fatalf("Choices length = %d, want 1", len(chunk.Choices))
	}
	if chunk.Choices[0].Message.Content != "The image shows a cat." {
		t.Errorf("Message.Content = %q", chunk.Choices[0].Message.Content)
	}
}
