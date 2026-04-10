package tee

import (
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"fmt"
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

// FormatRoutingProofText creates the text payload for TEE signing of a centralized
// provider routing proof. Format:
//
//	requestSha256:responseSha256:providerType:providerIdentity:tlsCertFingerprint
func FormatRoutingProofText(requestSha256, responseSha256, providerType, providerIdentity, tlsCertFingerprint string) string {
	return fmt.Sprintf("%s:%s:%s:%s:%s",
		requestSha256, responseSha256, providerType, providerIdentity, tlsCertFingerprint)
}
