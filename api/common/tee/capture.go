package tee

import (
	"context"
	"crypto/tls"
	"sync"
)

// CertCapture records the upstream TLS certificate observed while handling one
// inbound request. A protocol-translation sidecar installs it on the request
// context (WithCertCapture), its HTTP client reports every vendor response into it
// (Observe), and the response path reads it back out (Fingerprint) to set
// HeaderUpstreamCertFingerprint.
//
// It is per-request rather than per-client because concurrent requests to the same
// vendor client would otherwise race over a single shared field, and a routing
// proof must bind the certificate THIS request saw.
type CertCapture struct {
	mu          sync.Mutex
	fingerprint string
	serverName  string
}

type certCaptureKey struct{}

// WithCertCapture returns a context carrying a fresh CertCapture, plus the capture
// itself so the caller can read it after the work completes.
func WithCertCapture(ctx context.Context) (context.Context, *CertCapture) {
	c := &CertCapture{}
	return context.WithValue(ctx, certCaptureKey{}, c), c
}

// CertCaptureFromContext returns the capture installed by WithCertCapture, or nil.
// Every CertCapture method is nil-safe, so a caller can Observe unconditionally
// without knowing whether one was installed (e.g. in tests, or on a code path that
// no sidecar middleware wraps).
func CertCaptureFromContext(ctx context.Context) *CertCapture {
	if ctx == nil {
		return nil
	}
	c, _ := ctx.Value(certCaptureKey{}).(*CertCapture)
	return c
}

// Observe records the leaf certificate of a TLS connection state. The FIRST
// observation wins: a single inbound request can make several outbound calls (e.g.
// query the vendor API, then download the finished asset from its CDN), and the
// proof must bind the vendor API endpoint — the one that authenticated our key and
// produced the response being signed — not whichever host happened to answer last.
// A nil state (plaintext connection) is ignored.
func (c *CertCapture) Observe(state *tls.ConnectionState) {
	if c == nil {
		return
	}
	info := ExtractTLSInfo(state)
	if info == nil || info.PeerCertFingerprint == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.fingerprint == "" {
		c.fingerprint = info.PeerCertFingerprint
		c.serverName = info.ServerName
	}
}

// ServerName returns the SNI of the observed connection, or "" if none was
// observed. Reported alongside the fingerprint so the broker can check the host the
// shim actually dialed against the one it publishes to verifiers — see
// HeaderUpstreamCertHost.
func (c *CertCapture) ServerName() string {
	if c == nil {
		return ""
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.serverName
}

// Fingerprint returns the observed leaf-certificate fingerprint, or "" if no TLS
// connection was observed.
func (c *CertCapture) Fingerprint() string {
	if c == nil {
		return ""
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.fingerprint
}
