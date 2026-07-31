package tee

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"strings"
	"testing"
)

func TestNormalizeCertFingerprint(t *testing.T) {
	valid := strings.Repeat("ab", 32)
	tests := []struct {
		name   string
		in     string
		want   string
		wantOK bool
	}{
		{name: "lowercase hex", in: valid, want: valid, wantOK: true},
		{name: "uppercase normalized", in: strings.ToUpper(valid), want: valid, wantOK: true},
		{name: "surrounding space trimmed", in: "  " + valid + "\n", want: valid, wantOK: true},
		{name: "empty", in: "", wantOK: false},
		{name: "too short", in: strings.Repeat("ab", 31), wantOK: false},
		{name: "too long", in: strings.Repeat("ab", 33), wantOK: false},
		{name: "non-hex", in: strings.Repeat("zz", 32), wantOK: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := NormalizeCertFingerprint(tt.in)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOK)
			}
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestCertCaptureFirstObservationWins(t *testing.T) {
	ctx, capture := WithCertCapture(context.Background())

	if got := CertCaptureFromContext(ctx).Fingerprint(); got != "" {
		t.Fatalf("fresh capture reported %q", got)
	}

	api := &tls.ConnectionState{PeerCertificates: []*x509.Certificate{{Raw: []byte("vendor-api-cert")}}, ServerName: "api.vendor.test"}
	cdn := &tls.ConnectionState{PeerCertificates: []*x509.Certificate{{Raw: []byte("cdn-cert")}}, ServerName: "cdn.vendor.test"}

	CertCaptureFromContext(ctx).Observe(api)
	// A later asset download to the vendor's CDN must not displace the API
	// certificate: the routing proof attests to the endpoint that served the
	// response being signed.
	CertCaptureFromContext(ctx).Observe(cdn)

	want := CertFingerprintFromDER([]byte("vendor-api-cert"))
	if got := capture.Fingerprint(); got != want {
		t.Errorf("got %q, want the first observed cert %q", got, want)
	}
	// The SNI travels with the fingerprint — it is what lets the broker catch its
	// own upstreamDomain having drifted from the shim's configured base URL — and
	// must come from the SAME observation, not the last one seen.
	if got := capture.ServerName(); got != "api.vendor.test" {
		t.Errorf("server name = %q, want the API endpoint's, not the CDN's", got)
	}
}

func TestCertCaptureIgnoresPlaintextAndIsNilSafe(t *testing.T) {
	_, capture := WithCertCapture(context.Background())
	capture.Observe(nil)
	if got := capture.Fingerprint(); got != "" {
		t.Errorf("plaintext connection recorded %q", got)
	}

	// No capture installed: Observe/Fingerprint must be no-ops rather than panic,
	// so a client can report unconditionally.
	absent := CertCaptureFromContext(context.Background())
	absent.Observe(&tls.ConnectionState{PeerCertificates: []*x509.Certificate{{Raw: []byte("x")}}})
	if got := absent.Fingerprint(); got != "" {
		t.Errorf("nil capture returned %q", got)
	}
}
