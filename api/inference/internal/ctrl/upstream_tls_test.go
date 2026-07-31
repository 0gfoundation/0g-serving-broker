package ctrl

import (
	"crypto/tls"
	"crypto/x509"
	"net/http"
	"strings"
	"testing"

	teeutil "github.com/0glabs/0g-serving-broker/common/tee"
	"github.com/0glabs/0g-serving-broker/inference/config"
)

// TestUpstreamCertFingerprintSourceIsExclusive pins the security property that
// makes the sidecar-reported fingerprint safe: the two evidence sources never mix.
// A plain centralized deployment must ignore the header (or any upstream could
// dictate its own routing proof), and a targetTLSProxy deployment must ignore
// resp.TLS (which is the shim's own certificate, proving nothing about the vendor).
func TestUpstreamCertFingerprintSourceIsExclusive(t *testing.T) {
	shimReported := strings.Repeat("11", 32)
	directCert := &x509.Certificate{Raw: []byte("direct-upstream-cert")}
	directFingerprint := teeutil.CertFingerprintFromDER(directCert.Raw)

	withHeader := func(resp *http.Response, v string) *http.Response {
		resp.Header = http.Header{}
		resp.Header.Set(teeutil.HeaderUpstreamCertFingerprint, v)
		return resp
	}
	directTLS := &tls.ConnectionState{PeerCertificates: []*x509.Certificate{directCert}}

	tests := []struct {
		name           string
		targetTLSProxy bool
		resp           *http.Response
		want           string
	}{
		{
			name: "direct centralized uses resp.TLS",
			resp: withHeader(&http.Response{TLS: directTLS}, ""),
			want: directFingerprint,
		},
		{
			name: "direct centralized ignores a forged header",
			resp: withHeader(&http.Response{TLS: directTLS}, shimReported),
			want: directFingerprint,
		},
		{
			name: "direct centralized over plaintext has no evidence",
			resp: withHeader(&http.Response{}, shimReported),
			want: "",
		},
		{
			name:           "sidecar uses the reported header",
			targetTLSProxy: true,
			resp:           withHeader(&http.Response{}, strings.ToUpper(shimReported)),
			want:           shimReported,
		},
		{
			name:           "sidecar ignores resp.TLS (the shim's own cert)",
			targetTLSProxy: true,
			resp:           withHeader(&http.Response{TLS: directTLS}, shimReported),
			want:           shimReported,
		},
		{
			name:           "sidecar reporting nothing yields no evidence",
			targetTLSProxy: true,
			resp:           withHeader(&http.Response{TLS: directTLS}, ""),
			want:           "",
		},
		{
			name:           "sidecar reporting a malformed fingerprint yields no evidence",
			targetTLSProxy: true,
			resp:           withHeader(&http.Response{}, "not-a-fingerprint"),
			want:           "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := newChatbotTestCtrl(t, config.Service{
				ProviderType:     "centralized",
				ProviderIdentity: "vendor",
				TargetTLSProxy:   tt.targetTLSProxy,
			})
			if got := ctrl.upstreamCertFingerprint(tt.resp.Header, tt.resp.TLS); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

// TestUpstreamCertFingerprintHeaderIsStrippedFromClientResponse guards the other
// half: the header is broker-internal evidence, and on a standard provider it
// names the very upstream that deployment must hide.
func TestUpstreamCertFingerprintHeaderIsStrippedFromClientResponse(t *testing.T) {
	if !isUpstreamLeakHeader(teeutil.HeaderUpstreamCertFingerprint) {
		t.Errorf("%s must be stripped from forwarded responses", teeutil.HeaderUpstreamCertFingerprint)
	}
	if !isUpstreamLeakHeader(strings.ToLower(teeutil.HeaderUpstreamCertFingerprint)) {
		t.Error("header matching must be case-insensitive")
	}
}

// TestUpstreamCertFingerprintOnlyForCentralized: the video poll scheduler resolves
// this for every job regardless of provider type, so a decentralized in-network
// provider (plaintext target, nil resp.TLS) must not be reported as a lost routing
// proof — that would pin the very alert the counter exists to raise.
func TestUpstreamCertFingerprintOnlyForCentralized(t *testing.T) {
	for _, providerType := range []string{"decentralized", "standard"} {
		t.Run(providerType, func(t *testing.T) {
			ctrl := newChatbotTestCtrl(t, config.Service{ProviderType: providerType})
			resp := &http.Response{Header: http.Header{}}
			resp.Header.Set(teeutil.HeaderUpstreamCertFingerprint, strings.Repeat("11", 32))
			if got := ctrl.upstreamCertFingerprint(resp.Header, resp.TLS); got != "" {
				t.Errorf("resolved %q for a %s provider, which has no routing proof", got, providerType)
			}
		})
	}
}
