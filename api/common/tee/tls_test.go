package tee

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"testing"
	"time"
)

// generateSelfSignedCert creates a self-signed certificate for testing.
func generateSelfSignedCert(t *testing.T, cn string) *x509.Certificate {
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

func TestCertFingerprintFromX509(t *testing.T) {
	cert := generateSelfSignedCert(t, "test.example.com")
	fp := CertFingerprintFromX509(cert)

	if len(fp) != 64 {
		t.Errorf("expected 64-char hex fingerprint, got %d chars: %s", len(fp), fp)
	}

	// Same cert should produce same fingerprint
	fp2 := CertFingerprintFromX509(cert)
	if fp != fp2 {
		t.Errorf("fingerprint not deterministic: %s != %s", fp, fp2)
	}
}

func TestCertFingerprintFromDER(t *testing.T) {
	cert := generateSelfSignedCert(t, "test.example.com")
	fp1 := CertFingerprintFromDER(cert.Raw)
	fp2 := CertFingerprintFromX509(cert)

	if fp1 != fp2 {
		t.Errorf("DER and X509 fingerprints should match: %s != %s", fp1, fp2)
	}
}

func TestExtractTLSInfo_NilState(t *testing.T) {
	result := ExtractTLSInfo(nil)
	if result != nil {
		t.Error("expected nil for nil TLS state")
	}
}

func TestExtractTLSInfo_NoPeerCerts(t *testing.T) {
	state := &tls.ConnectionState{}
	result := ExtractTLSInfo(state)
	if result != nil {
		t.Error("expected nil for empty peer certificates")
	}
}

func TestExtractTLSInfo_WithCert(t *testing.T) {
	cert := generateSelfSignedCert(t, "api.openai.com")

	state := &tls.ConnectionState{
		PeerCertificates: []*x509.Certificate{cert},
		ServerName:       "api.openai.com",
	}

	result := ExtractTLSInfo(state)
	if result == nil {
		t.Fatal("expected non-nil result")
	}

	if result.PeerCertFingerprint == "" {
		t.Error("expected non-empty fingerprint")
	}
	if result.ServerName != "api.openai.com" {
		t.Errorf("expected server name api.openai.com, got %s", result.ServerName)
	}
	if len(result.CertChainFingerprints) != 1 {
		t.Errorf("expected 1 chain fingerprint, got %d", len(result.CertChainFingerprints))
	}
	if result.CertChainFingerprints[0] != result.PeerCertFingerprint {
		t.Error("chain fingerprint should match peer fingerprint for single cert")
	}
}

func TestExtractTLSInfo_MultipleCerts(t *testing.T) {
	leaf := generateSelfSignedCert(t, "api.openai.com")
	intermediate := generateSelfSignedCert(t, "Intermediate CA")

	state := &tls.ConnectionState{
		PeerCertificates: []*x509.Certificate{leaf, intermediate},
		ServerName:       "api.openai.com",
	}

	result := ExtractTLSInfo(state)
	if result == nil {
		t.Fatal("expected non-nil result")
	}

	if len(result.CertChainFingerprints) != 2 {
		t.Errorf("expected 2 chain fingerprints, got %d", len(result.CertChainFingerprints))
	}
	// Leaf fingerprint should be the peer fingerprint
	if result.PeerCertFingerprint != result.CertChainFingerprints[0] {
		t.Error("peer fingerprint should match first chain entry")
	}
	// Chain entries should be different certs
	if result.CertChainFingerprints[0] == result.CertChainFingerprints[1] {
		t.Error("different certs should have different fingerprints")
	}
}

func TestFormatRoutingProofText(t *testing.T) {
	tests := []struct {
		name               string
		requestSha256      string
		responseSha256     string
		providerType       string
		providerIdentity   string
		tlsCertFingerprint string
		expected           string
	}{
		{
			name:               "openai provider",
			requestSha256:      "abc123",
			responseSha256:     "def456",
			providerType:       "centralized",
			providerIdentity:   "openai",
			tlsCertFingerprint: "fingerprint789",
			expected:           "abc123:def456:centralized:openai:fingerprint789",
		},
		{
			name:               "empty fingerprint",
			requestSha256:      "req",
			responseSha256:     "resp",
			providerType:       "centralized",
			providerIdentity:   "anthropic",
			tlsCertFingerprint: "",
			expected:           "req:resp:centralized:anthropic:",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := FormatRoutingProofText(tt.requestSha256, tt.responseSha256, tt.providerType, tt.providerIdentity, tt.tlsCertFingerprint)
			if result != tt.expected {
				t.Errorf("expected %q, got %q", tt.expected, result)
			}
		})
	}
}
