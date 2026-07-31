package tee

import (
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"fmt"
	"strings"
)

// CapturedCert holds the captured TLS certificate data from a connection.
type CapturedCert struct {
	// PeerCertFingerprint is the SHA256 fingerprint of the leaf certificate.
	PeerCertFingerprint string
	// CertChainFingerprints contains SHA256 fingerprints of the full certificate chain.
	CertChainFingerprints []string
	// ServerName is the SNI server name from the TLS connection.
	ServerName string
}

// HeaderUpstreamCertFingerprint carries the SHA256 leaf-certificate fingerprint of
// the TLS connection a protocol-translation sidecar made to the real upstream, so
// the broker can bind it into a centralized routing proof.
//
// It exists because a sidecar shim moves the vendor TLS handshake out of the
// broker's own http.Client: the broker's hop is plaintext HTTP inside the CVM, so
// resp.TLS is nil and there is nothing to sign. The shim runs in the SAME TDX CVM
// (it is covered by the same quote), so its observation of the vendor certificate
// carries the same weight as the broker's would — but only under that assumption,
// which is why the broker trusts this header ONLY when the operator has declared
// service.targetTLSProxy (see inference/config).
const HeaderUpstreamCertFingerprint = "Zg-Upstream-Cert-Fingerprint"

// CertFingerprintFromDER computes a SHA256 fingerprint from a DER-encoded certificate.
func CertFingerprintFromDER(der []byte) string {
	hash := sha256.Sum256(der)
	return hex.EncodeToString(hash[:])
}

// CertFingerprintFromX509 computes a SHA256 fingerprint from a parsed X.509 certificate.
func CertFingerprintFromX509(cert *x509.Certificate) string {
	return CertFingerprintFromDER(cert.Raw)
}

// ExtractTLSInfo extracts certificate information from a TLS connection state.
// Returns nil if no peer certificates are present.
func ExtractTLSInfo(state *tls.ConnectionState) *CapturedCert {
	if state == nil || len(state.PeerCertificates) == 0 {
		return nil
	}

	captured := &CapturedCert{
		PeerCertFingerprint: CertFingerprintFromX509(state.PeerCertificates[0]),
		ServerName:          state.ServerName,
	}

	// Capture full chain fingerprints
	for _, cert := range state.PeerCertificates {
		captured.CertChainFingerprints = append(
			captured.CertChainFingerprints,
			CertFingerprintFromX509(cert),
		)
	}

	return captured
}

// NormalizeCertFingerprint validates a reported SHA256 certificate fingerprint and
// returns it lowercased. Anything that is not exactly 32 hex-encoded bytes is
// rejected: a fingerprint reaches the routing proof from a sidecar over a plain
// header, and a malformed value must fail closed (no proof) rather than be signed
// into one, where a verifier would compare it against a real certificate forever.
func NormalizeCertFingerprint(s string) (string, bool) {
	s = strings.ToLower(strings.TrimSpace(s))
	if len(s) != sha256.Size*2 {
		return "", false
	}
	if _, err := hex.DecodeString(s); err != nil {
		return "", false
	}
	return s, true
}

// FormatRoutingProofText creates the text payload for TEE signing of a centralized
// provider routing proof. Format:
//
//	requestSha256:responseSha256:providerType:providerIdentity:tlsCertFingerprint
func FormatRoutingProofText(requestSha256, responseSha256, providerType, providerIdentity, tlsCertFingerprint string) string {
	return fmt.Sprintf("%s:%s:%s:%s:%s",
		requestSha256, responseSha256, providerType, providerIdentity, tlsCertFingerprint)
}
