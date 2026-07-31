package ctrl

import (
	"crypto/tls"
	"crypto/x509"
	"net/http"
	"strings"
	"testing"

	"github.com/0glabs/0g-serving-broker/common/log"
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

	// The host always matches the configured upstreamDomain here: this table is
	// about which EVIDENCE SOURCE is admissible, and the drift check has its own
	// test (TestUpstreamCertFingerprintRefusesDomainDrift).
	withHeader := func(resp *http.Response, v string) *http.Response {
		resp.Header = http.Header{}
		resp.Header.Set(teeutil.HeaderUpstreamCertFingerprint, v)
		resp.Header.Set(teeutil.HeaderUpstreamCertHost, "api.vendor.test")
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
				UpstreamDomain:   "api.vendor.test",
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

// TestUpstreamCertFingerprintRefusesDomainDrift is the check that closes the one
// failure the routing-proof metrics could not see. service.upstreamDomain (broker
// config) and the shim's own *_BASE_URL (a different file, a different container)
// are the same fact stored twice with nothing coupling them — and the shipped
// compose example hardcodes the former while telling a domestic-site operator to
// change the latter. Drift signs host A's certificate while serving_domain points
// verifiers at host B: every verification fails, and nothing is malformed, so
// without this the broker never learns.
func TestUpstreamCertFingerprintRefusesDomainDrift(t *testing.T) {
	fp := strings.Repeat("ab", 32)
	newResp := func(host string) *http.Response {
		resp := &http.Response{Header: http.Header{}}
		resp.Header.Set(teeutil.HeaderUpstreamCertFingerprint, fp)
		if host != "" {
			resp.Header.Set(teeutil.HeaderUpstreamCertHost, host)
		}
		return resp
	}

	tests := []struct {
		name     string
		reported string
		want     string
	}{
		{name: "shim dialed the declared host", reported: "api.minimax.io", want: fp},
		{name: "case and trailing dot are not drift", reported: "API.MiniMax.IO.", want: fp},
		{name: "shim drifted to the domestic site", reported: "api.minimaxi.com", want: ""},
		{name: "shim reported no host at all", reported: "", want: ""},
		{name: "shim reported a subdomain of the declared host", reported: "eu.api.minimax.io", want: ""},
		{name: "shim reported the declared host with a port-looking suffix", reported: "api.minimax.io:443", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := newChatbotTestCtrl(t, config.Service{
				ProviderType:     "centralized",
				ProviderIdentity: "minimax",
				TargetTLSProxy:   true,
				UpstreamDomain:   "api.minimax.io",
			})
			resp := newResp(tt.reported)
			if got := ctrl.upstreamCertFingerprint(resp.Header, resp.TLS); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

// TestUpstreamCertHostIsStrippedFromClientResponse: the host header is the same
// class of evidence as the fingerprint — internal, and on a standard provider it
// names the upstream that deployment exists to hide.
func TestUpstreamCertHostIsStrippedFromClientResponse(t *testing.T) {
	if !isUpstreamLeakHeader(teeutil.HeaderUpstreamCertHost) {
		t.Errorf("%s must be stripped from forwarded responses", teeutil.HeaderUpstreamCertHost)
	}
}

// TestDomainMismatchLogsOncePerDistinctHost pins the rate-limit's actual contract.
// The naive shape (Swap, then re-read what you just stored) compares a value with
// itself and therefore fires only on the FIRST mismatch ever — going silent exactly
// when an operator drifts to a second wrong host, which is when they most need to
// hear about it.
func TestDomainMismatchLogsOncePerDistinctHost(t *testing.T) {
	ctrl := newChatbotTestCtrl(t, config.Service{
		ProviderType:     "centralized",
		ProviderIdentity: "minimax",
		TargetTLSProxy:   true,
		UpstreamDomain:   "api.minimax.io",
	})
	rec := &countingLogger{Logger: ctrl.logger}
	ctrl.logger = rec

	resp := func(host string) (http.Header, *tls.ConnectionState) {
		h := http.Header{}
		h.Set(teeutil.HeaderUpstreamCertFingerprint, strings.Repeat("ab", 32))
		h.Set(teeutil.HeaderUpstreamCertHost, host)
		return h, nil
	}

	for i := 0; i < 5; i++ {
		hdr, st := resp("api.minimaxi.com")
		ctrl.upstreamCertFingerprint(hdr, st)
	}
	if rec.errors != 1 {
		t.Errorf("same host repeated: logged %d times, want 1", rec.errors)
	}

	hdr, st := resp("api.example.com")
	ctrl.upstreamCertFingerprint(hdr, st)
	if rec.errors != 2 {
		t.Errorf("a DIFFERENT wrong host must log: total %d, want 2", rec.errors)
	}
}

type countingLogger struct {
	log.Logger
	errors int
}

func (l *countingLogger) Errorf(format string, args ...interface{}) { l.errors++ }
