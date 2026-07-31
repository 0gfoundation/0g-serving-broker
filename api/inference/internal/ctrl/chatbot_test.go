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

// fingerprintOf mirrors what Ctrl.upstreamCertFingerprint hands the signer for a
// direct (non-sidecar) centralized connection, so these tests keep exercising the
// real cert -> fingerprint path rather than a hand-written hex string.
func fingerprintOf(state *tls.ConnectionState) string {
	info := teeutil.ExtractTLSInfo(state)
	if info == nil {
		return ""
	}
	return info.PeerCertFingerprint
}

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
	err := ctrl.signCentralizedRoutingProof(reqBody, respData, chatKey, "")
	if err == nil {
		t.Fatal("expected error when TLS state is nil")
	}
	if !strings.Contains(err.Error(), "no usable upstream TLS certificate fingerprint") {
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

	err := ctrl.signCentralizedRoutingProof(reqBody, respData, chatKey, fingerprintOf(tlsState))
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

			err := ctrl.signCentralizedRoutingProof(reqBody, respData, "key-"+tt.name, fingerprintOf(tlsState))
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

		err := ctrl.signCentralizedRoutingProof(reqBody, respData, chatKey, fingerprintOf(tlsState))
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
		{
			// A standard provider never signs, so it must never advertise ZG-Res-Key
			// (it is TargetSeparated + !centralized). This is the gate used by the
			// chatbot, speech-to-text, text-to-image, image-editing, and video paths.
			name:            "standard never sets key",
			providerType:    "standard",
			targetSeparated: true,
			wantResKey:      false,
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
// extractUsageFromLine — guards against empty usage from attestation chunks
// ==========================================================================

func TestExtractUsageFromLine(t *testing.T) {
	ctrl := &Ctrl{}

	tests := []struct {
		name           string
		line           string
		wantNil        bool
		wantPrompt     int
		wantCompletion int
	}{
		{
			name:           "valid usage",
			line:           `data: {"id":"test","choices":[],"usage":{"prompt_tokens":18,"completion_tokens":12,"total_tokens":30}}`,
			wantNil:        false,
			wantPrompt:     18,
			wantCompletion: 12,
		},
		{
			name:    "empty usage object from attestation chunk",
			line:    `data: {"id":"test","choices":[],"usage":{},"attestation":{"foo":"bar"}}`,
			wantNil: true,
		},
		{
			name:    "no usage field",
			line:    `data: {"id":"test","choices":[{"index":0,"delta":{"content":"hello"}}]}`,
			wantNil: true,
		},
		{
			name:    "invalid JSON",
			line:    `data: {invalid`,
			wantNil: true,
		},
		{
			name:    "done marker",
			line:    `data: [DONE]`,
			wantNil: true,
		},
		{
			name:    "all zero usage",
			line:    `data: {"id":"test","choices":[],"usage":{"prompt_tokens":0,"completion_tokens":0,"total_tokens":0}}`,
			wantNil: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ctrl.extractUsageFromLine([]byte(tt.line))
			if tt.wantNil {
				if got != nil {
					t.Errorf("expected nil, got %+v", got)
				}
				return
			}
			if got == nil {
				t.Fatal("expected non-nil usage")
			}
			if got.PromptTokens != tt.wantPrompt {
				t.Errorf("PromptTokens = %d, want %d", got.PromptTokens, tt.wantPrompt)
			}
			if got.CompletionTokens != tt.wantCompletion {
				t.Errorf("CompletionTokens = %d, want %d", got.CompletionTokens, tt.wantCompletion)
			}
		})
	}
}

// TestExtractUsageOverwritePrevention verifies that a stream with an
// attestation chunk carrying "usage":{} does not overwrite real usage data.
func TestExtractUsageOverwritePrevention(t *testing.T) {
	ctrl := &Ctrl{}

	// Simulate the actual stream chunk order from the provider
	lines := []string{
		`data: {"id":"test","choices":[],"usage":{"prompt_tokens":18,"completion_tokens":12,"total_tokens":30}}`,
		`data: {"id":"test","choices":[],"usage":{},"attestation":{"recipient":"0x0"}}`,
	}

	var usage *Usage
	for _, line := range lines {
		if extracted := ctrl.extractUsageFromLine([]byte(line)); extracted != nil {
			usage = extracted
		}
	}

	if usage == nil {
		t.Fatal("usage should not be nil")
	}
	if usage.PromptTokens != 18 || usage.CompletionTokens != 12 {
		t.Errorf("usage was overwritten: got prompt=%d completion=%d, want 18 and 12",
			usage.PromptTokens, usage.CompletionTokens)
	}
}

// ==========================================================================
// isSSEComment — SSE keepalive/comment lines must be ignored
// ==========================================================================

// TestIsSSEComment locks in the regression fix for OpenRouter's keepalive
// frames (": OPENROUTER PROCESSING") that previously caused
// `Error unmarshaling JSON: invalid character ':'` in processOpenAIStream.
func TestIsSSEComment(t *testing.T) {
	tests := []struct {
		name string
		line string
		want bool
	}{
		{"openrouter keepalive", ": OPENROUTER PROCESSING", true},
		{"bare colon", ":", true},
		{"colon with space", ": ping", true},
		{"data line is not a comment", `data: {"id":"x"}`, false},
		{"empty line is not a comment", "", false},
		{"event line is not a comment", "event: message_delta", false},
		{"colon mid-line is not a comment", `data: {"a":"b"}`, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isSSEComment([]byte(tt.line)); got != tt.want {
				t.Errorf("isSSEComment(%q) = %v, want %v", tt.line, got, tt.want)
			}
		})
	}
}

// TestSSECommentDoesNotPoisonUsage simulates the OpenRouter stream order
// (keepalive comment, then data chunks, then [DONE]) and verifies the loop
// pattern used by processOpenAIStream skips comments before they reach the
// JSON-parsing helpers.
func TestSSECommentDoesNotPoisonUsage(t *testing.T) {
	ctrl := &Ctrl{}

	lines := []string{
		": OPENROUTER PROCESSING",
		": OPENROUTER PROCESSING",
		`data: {"id":"x","choices":[{"index":0,"delta":{"content":"Hi"}}]}`,
		`data: {"id":"x","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":14,"completion_tokens":73,"total_tokens":87}}`,
		"data: [DONE]",
	}

	var usage *Usage
	for _, line := range lines {
		b := []byte(line)
		if isLineEmpty(b) || isSSEComment(b) || isStreamDone(b) {
			continue
		}
		if extracted := ctrl.extractUsageFromLine(b); extracted != nil {
			usage = extracted
			continue
		}
		if _, err := ctrl.processLine(b); err != nil {
			t.Fatalf("processLine should not fail after comment skip: %v", err)
		}
	}

	if usage == nil {
		t.Fatal("usage should be captured from the final data chunk")
	}
	if usage.PromptTokens != 14 || usage.CompletionTokens != 73 {
		t.Errorf("usage = prompt=%d completion=%d, want 14 and 73",
			usage.PromptTokens, usage.CompletionTokens)
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
// signImageResponse
// ==========================================================================

func TestSignImageResponse_TextFormat(t *testing.T) {
	ctrl := newChatbotTestCtrl(t, config.Service{ProviderType: "decentralized"})

	reqBody := []byte(`{"prompt":"a cat","n":2}`)
	images := [][]byte{[]byte("img0bytes"), []byte("img1bytes")}
	chatKey := "image-sign-test-key"

	if err := ctrl.signImageResponse(reqBody, images, chatKey); err != nil {
		t.Fatalf("signImageResponse: %v", err)
	}

	val, ok := ctrl.svcCache.Get(ctrl.chatCacheKey(chatKey))
	if !ok {
		t.Fatal("signature not found in cache")
	}
	cs := val.(ChatSignature)

	hashAndEncode := func(b []byte) string {
		h := sha256.Sum256(b)
		return hex.EncodeToString(h[:])
	}
	want := hashAndEncode(reqBody) + ":" + hashAndEncode(images[0]) + "," + hashAndEncode(images[1])
	if cs.Text != want {
		t.Errorf("text = %q\nwant = %q", cs.Text, want)
	}
}

func TestSignImageResponse_SignatureRecoverable(t *testing.T) {
	ctrl := newChatbotTestCtrl(t, config.Service{ProviderType: "decentralized"})

	if err := ctrl.signImageResponse(
		[]byte(`{"prompt":"dog"}`),
		[][]byte{[]byte("img-data")},
		"recoverable-key",
	); err != nil {
		t.Fatalf("signImageResponse: %v", err)
	}

	val, _ := ctrl.svcCache.Get(ctrl.chatCacheKey("recoverable-key"))
	cs := val.(ChatSignature)

	recovered := recoverSignerAddress(t, cs)
	if recovered != ctrl.teeService.Address {
		t.Errorf("recovered %v, want %v", recovered, ctrl.teeService.Address)
	}
	if cs.SigningAlgo != ECDSA.String() {
		t.Errorf("signing_algo = %q, want %q", cs.SigningAlgo, ECDSA.String())
	}
}

func TestSignImageResponse_SingleImage_NoCommaInText(t *testing.T) {
	ctrl := newChatbotTestCtrl(t, config.Service{ProviderType: "decentralized"})

	reqBody := []byte(`{"prompt":"solo"}`)
	img := []byte("one-image")
	chatKey := "single-img-key"

	if err := ctrl.signImageResponse(reqBody, [][]byte{img}, chatKey); err != nil {
		t.Fatalf("signImageResponse: %v", err)
	}

	val, _ := ctrl.svcCache.Get(ctrl.chatCacheKey(chatKey))
	cs := val.(ChatSignature)

	// With a single image there should be no comma separating hashes.
	parts := strings.SplitN(cs.Text, ":", 2)
	if len(parts) != 2 {
		t.Fatalf("text has unexpected format: %q", cs.Text)
	}
	if strings.Contains(parts[1], ",") {
		t.Errorf("single image text should not contain comma, got %q", cs.Text)
	}
}

func TestSignImageResponse_DifferentImagesProduceDifferentText(t *testing.T) {
	ctrl := newChatbotTestCtrl(t, config.Service{ProviderType: "decentralized"})

	req := []byte(`{"prompt":"same"}`)
	imagesA := [][]byte{[]byte("image-A")}
	imagesB := [][]byte{[]byte("image-B")}

	_ = ctrl.signImageResponse(req, imagesA, "key-a")
	_ = ctrl.signImageResponse(req, imagesB, "key-b")

	valA, _ := ctrl.svcCache.Get(ctrl.chatCacheKey("key-a"))
	valB, _ := ctrl.svcCache.Get(ctrl.chatCacheKey("key-b"))
	csA := valA.(ChatSignature)
	csB := valB.(ChatSignature)

	if csA.Text == csB.Text {
		t.Error("different images should produce different signed text")
	}
}

// TestSignImageResponse_RefusesCentralized pins the parity guard: the
// decentralized signer must never run for a centralized provider, because its
// signature envelope omits the TLS fingerprint / ProviderType / ProviderIdentity
// fields that signCentralizedRoutingProof carries. Without this guard, a dev
// who flips providerType=centralized while targetSeparated=false would get a
// silently weaker TEE proof — a routing-proof envelope without the TLS
// evidence, giving verifiers a false sense of security.
func TestSignImageResponse_RefusesCentralized(t *testing.T) {
	ctrl := newChatbotTestCtrl(t, config.Service{
		ProviderType:     "centralized",
		ProviderIdentity: "openai",
	})

	err := ctrl.signImageResponse([]byte(`{"prompt":"x"}`), [][]byte{[]byte("img")}, "centralized-key")
	if err == nil {
		t.Fatal("signImageResponse must refuse when the service is centralized")
	}
	if !strings.Contains(err.Error(), "routing-proof") {
		t.Errorf("error should point to the missing routing-proof variant; got: %v", err)
	}
	// Nothing should be cached.
	if _, found := ctrl.svcCache.Get(ctrl.chatCacheKey("centralized-key")); found {
		t.Error("no signature must be cached when the centralized guard trips")
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

// TestGetTierMultipliers_NeverExceedsCeiling pins the overcharge-safe invariant
// the multi-model design relies on: per-request billing selects a tier via
// getTierMultipliers, while the on-chain advertised ceiling is sized by
// config (*ModelPricingEntry).maxTierMultipliers = max over the tier set
// (floored at 1). Billing only multiplies when the selected multiplier > 1
// (see updateAccountWithUsage), so the EFFECTIVE billed multiplier must never
// exceed the ceiling multiplier for any prompt-token count. If the two
// tier-reduction copies ever drift, this fails.
func TestGetTierMultipliers_NeverExceedsCeiling(t *testing.T) {
	tierSets := [][]config.PricingTier{
		{{MaxInputTokens: 1000, InputMultiplier: 1, OutputMultiplier: 1}, {MaxInputTokens: 0, InputMultiplier: 4, OutputMultiplier: 3}},
		{{MaxInputTokens: 256000, InputMultiplier: 1, OutputMultiplier: 1}, {MaxInputTokens: 1000000, InputMultiplier: 2, OutputMultiplier: 2}, {MaxInputTokens: 0, InputMultiplier: 8, OutputMultiplier: 6}},
		{{MaxInputTokens: 0, InputMultiplier: 1, OutputMultiplier: 1}}, // flat
		// Fractional tiers: 3/2 = 1.5x, 5/2 = 2.5x — the case int64 multipliers couldn't express.
		{
			{MaxInputTokens: 32000, InputMultiplier: 1, OutputMultiplier: 1},
			{MaxInputTokens: 0, InputMultiplier: 3, InputMultiplierDenominator: 2, OutputMultiplier: 5, OutputMultiplierDenominator: 2},
		},
	}
	// ceilFrac mirrors (*ModelPricingEntry).maxTierMultipliers: the max effective
	// fraction over the set, floored at 1/1. Compared by cross-multiplication.
	ceilFrac := func(get func(config.PricingTier) (int64, int64), tiers []config.PricingTier) (num, den int64) {
		num, den = 1, 1
		for _, tr := range tiers {
			if n, d := get(tr); n*den > num*d {
				num, den = n, d
			}
		}
		return
	}
	for ti, tiers := range tierSets {
		ceilInN, ceilInD := ceilFrac(config.PricingTier.EffectiveInputMultiplier, tiers)
		ceilOutN, ceilOutD := ceilFrac(config.PricingTier.EffectiveOutputMultiplier, tiers)
		// Sweep prompt-token counts across every boundary and beyond the last tier.
		for tok := 0; tok <= 1_200_000; tok += 50_000 {
			tier := matchedTier(tiers, tok)
			inN, inD := tier.EffectiveInputMultiplier()
			outN, outD := tier.EffectiveOutputMultiplier()
			if inN*ceilInD > ceilInN*inD { // inN/inD > ceilInN/ceilInD
				t.Errorf("set %d tok=%d: billed input %d/%d exceeds ceiling %d/%d", ti, tok, inN, inD, ceilInN, ceilInD)
			}
			if outN*ceilOutD > ceilOutN*outD {
				t.Errorf("set %d tok=%d: billed output %d/%d exceeds ceiling %d/%d", ti, tok, outN, outD, ceilOutN, ceilOutD)
			}
		}
	}
}

func TestApplyTierMultiplier(t *testing.T) {
	cases := []struct {
		price    string
		num, den int64
		want     string
	}{
		{"100", 1, 1, "100"}, // 1x unchanged
		{"100", 3, 2, "150"}, // 1.5x
		{"100", 5, 2, "250"}, // 2.5x
		{"100", 2, 1, "200"}, // legacy integer 2x (den defaults handled by caller)
		{"7", 3, 2, "10"},    // 7*3/2 = 10 (floor of 10.5), truncation like addTierFee
		{"1000000000000000000", 3, 2, "1500000000000000000"}, // wei-scale, exact
	}
	for _, c := range cases {
		got, err := applyTierMultiplier(c.price, c.num, c.den)
		if err != nil {
			t.Fatalf("applyTierMultiplier(%s, %d, %d): %v", c.price, c.num, c.den, err)
		}
		if got != c.want {
			t.Errorf("applyTierMultiplier(%s, %d/%d) = %s; want %s", c.price, c.num, c.den, got, c.want)
		}
	}
	if _, err := applyTierMultiplier("not-a-number", 3, 2); err == nil {
		t.Error("expected error for unparseable price")
	}
}
