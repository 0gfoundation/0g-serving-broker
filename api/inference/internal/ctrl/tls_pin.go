package ctrl

import (
	"bytes"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
)

// pinnedTLSConfig returns a tls.Config that authenticates the peer by
// comparing sha256(leaf SubjectPublicKeyInfo) against pin, REPLACING CA and
// hostname validation — the assay verifier's TLS key is attested through the
// tapp KMS derivation (docs/spml-broker-assay-tls.md §2), so no CA will ever
// vouch for it. The pin applies to every TLS connection the transport makes;
// in this deployment the shared provider client only ever dials the verifier.
// A nil getter returns nil: default CA validation, plain http unaffected.
// The pin is re-read from getPin on EVERY handshake, so a Phase-2 attestor
// can rotate it without rebuilding the client; getPin returning nil/empty
// fails the handshake (no pin available = fail closed, never unverified).
func pinnedTLSConfig(getPin func() []byte) *tls.Config {
	if getPin == nil {
		return nil
	}
	return &tls.Config{
		// Not actually insecure: this only switches off CA+hostname checks,
		// which VerifyPeerCertificate below replaces with a stronger one.
		// The callback still runs with this flag set (crypto/tls docs).
		InsecureSkipVerify: true,
		VerifyPeerCertificate: func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
			want := getPin()
			if len(want) == 0 {
				return errors.New("assay TLS: no pin available (attestation not yet verified and no static fallback) — failing closed")
			}
			if len(rawCerts) == 0 {
				return errors.New("assay TLS: peer presented no certificate")
			}
			leaf, err := x509.ParseCertificate(rawCerts[0])
			if err != nil {
				return fmt.Errorf("assay TLS: cannot parse peer certificate: %w", err)
			}
			sum := sha256.Sum256(leaf.RawSubjectPublicKeyInfo)
			if !bytes.Equal(sum[:], want) {
				return fmt.Errorf("assay TLS key pin mismatch: peer SPKI sha256 %x, pinned %x", sum, want)
			}
			return nil
		},
	}
}
