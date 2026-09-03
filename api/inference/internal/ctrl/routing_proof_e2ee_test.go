package ctrl

import (
	"strings"
	"testing"

	constant "github.com/0glabs/0g-serving-broker/inference/const"
	"github.com/gin-gonic/gin"
)

// testFingerprint is a well-formed SHA-256 certificate fingerprint. It has to
// pass teeutil.NormalizeCertFingerprint (32 hex bytes) or the proof is refused
// before any of these assertions could run.
const testFingerprint = "a1b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8f90"

// sealedCentralizedFixture is newE2EEFixture with the provider shape that owns
// BOTH proofs: centralized (so a routing proof exists at all) and serving a
// sealed request (so the §8 binding is the top-level signature).
func sealedCentralizedFixture(t *testing.T) *e2eeTestFixture {
	t.Helper()
	f := newE2EEFixture(t)
	f.c.Service.ProviderType = constant.ProviderTypeCentralized
	f.c.Service.ProviderIdentity = "api.vendor.example"
	return f
}

// sealedCtx marks a gin context the way MaybeUnsealRequest does, so
// signChatResponse takes its E2EE branch, and optionally plants the upstream TLS
// fingerprint the proxy captures before the flush.
func sealedCtx(t *testing.T, f *e2eeTestFixture, fingerprint string) *gin.Context {
	t.Helper()
	ctx := newGinCtx()
	ctx.Set(CtxKeyE2EESealed, true)
	ctx.Set(CtxKeyE2EEClientEphPub, f.clientEphPub)
	if fingerprint != "" {
		ctx.Set(CtxKeyUpstreamCertFingerprint, fingerprint)
	}
	return ctx
}

// TestSealedResponseCarriesRoutingProofOnCentralizedProvider is the point of
// this change: on a centralized provider a sealed request needs BOTH proofs, and
// before this it got only §8 — signChatResponse returns from the E2EE branch
// before the IsCentralized() one, so the vendor attestation that is the primary
// trust artifact of a centralized route was silently dropped on sealed traffic.
//
// The two answer different questions and neither implies the other: §8 says this
// enclave produced the ciphertext the client decrypted; the routing proof says
// which vendor the upstream hop actually terminated TLS at.
func TestSealedResponseCarriesRoutingProofOnCentralizedProvider(t *testing.T) {
	f := sealedCentralizedFixture(t)
	ctx := sealedCtx(t, f, testFingerprint)

	reqBody := []byte(`{"model":"m","_e2ee":{"v":1}}`)
	respData := []byte(`{"_e2ee":{"v":1,"ciphertext":"c2VhbGVk"}}`)
	// Placeholder binding hashes, deliberately NOT the fingerprint: the
	// assertions below distinguish the two signed texts by content, and reusing
	// one value across both would let a substring coincidence stand in for a
	// real match.
	const e2eeText = "zg-sig-v1/e2ee-ct:reqhash:resphash"

	if err := f.c.signChatResponse(ctx, reqBody, respData, "ck-both", e2eeText, ""); err != nil {
		t.Fatalf("signChatResponse: %v", err)
	}
	sig, err := f.c.GetChatSignature("ck-both")
	if err != nil {
		t.Fatalf("GetChatSignature: %v", err)
	}

	// The §8 binding is UNCHANGED at the top level. This is the compatibility
	// assertion the whole nesting decision exists to make true: an E2EE client
	// verifies exactly what it verified before this field existed.
	if sig.Text != e2eeText {
		t.Errorf("top-level text = %q, want the §8 binding %q", sig.Text, e2eeText)
	}
	if sig.RoutingProof == nil {
		t.Fatal("a sealed response from a centralized provider carries no routing proof: the vendor attestation is lost")
	}

	// The nested proof carries its OWN signature over its OWN text — nothing in
	// it is a claim the top-level signature fails to cover, which is what makes
	// reading tls_cert_fingerprint from here sound rather than false confidence.
	rp := sig.RoutingProof
	if rp.Text == sig.Text {
		t.Error("the nested proof signed the §8 text; it must sign the routing-proof text")
	}
	if rp.SignatureEcdsa == sig.SignatureEcdsa {
		t.Error("the nested proof reuses the §8 signature; it must carry its own")
	}
	if got := len(strings.Split(rp.Text, ":")); got != 5 {
		t.Errorf("routing proof text has %d colon-separated fields, want the 5-field TeeTLS shape: %q", got, rp.Text)
	}
	if rp.TLSCertFingerprint != testFingerprint {
		t.Errorf("fingerprint = %q, want %q", rp.TLSCertFingerprint, testFingerprint)
	}
	if rp.ProviderIdentity != "api.vendor.example" {
		t.Errorf("provider identity = %q, want the service-level fallback", rp.ProviderIdentity)
	}
	if rp.ProviderType != constant.ProviderTypeCentralized {
		t.Errorf("provider type = %q, want %q", rp.ProviderType, constant.ProviderTypeCentralized)
	}

	// Recovering the signer is what makes this a proof rather than a struct: the
	// nested signature must verify against THIS enclave over the nested text.
	if recovered := recoverEIP191(t, rp.Text, rp.SignatureEcdsa); !strings.EqualFold(recovered.Hex(), f.signerAddr) {
		t.Errorf("nested proof signed by %q, want this enclave %q", recovered.Hex(), f.signerAddr)
	}
}

// TestSealedResponseWithoutTLSEvidenceStillCarriesTheE2EESignature pins the
// fail-closed posture in BOTH directions at once. No fingerprint means no proof
// — never a proof with an empty fingerprint, which would give a verifier false
// confidence — but the §8 binding must still be cached, because it is the
// load-bearing signature on a sealed request and the client refuses the response
// without it. Losing §8 because the vendor evidence was unavailable would turn a
// missing nicety into a failed request.
func TestSealedResponseWithoutTLSEvidenceStillCarriesTheE2EESignature(t *testing.T) {
	f := sealedCentralizedFixture(t)
	ctx := sealedCtx(t, f, "") // proxy captured nothing: no TLS, or a 4xx from a sidecar

	const e2eeText = "zg-sig-v1/e2ee-ct:aa:bb"
	if err := f.c.signChatResponse(ctx, []byte(`{}`), []byte(`{}`), "ck-notls", e2eeText, ""); err != nil {
		t.Fatalf("signChatResponse must not fail when only the routing proof is unavailable: %v", err)
	}
	sig, err := f.c.GetChatSignature("ck-notls")
	if err != nil {
		t.Fatalf("the §8 signature was not cached at all: %v", err)
	}
	if sig.Text != e2eeText {
		t.Errorf("top-level text = %q, want the §8 binding", sig.Text)
	}
	if sig.RoutingProof != nil {
		t.Errorf("a proof was emitted with no TLS evidence behind it: %+v", sig.RoutingProof)
	}
}

// TestSealedResponseOnDecentralizedProviderCarriesNoRoutingProof keeps the
// nesting scoped to the shape that has vendor evidence to offer. A decentralized
// provider has no external vendor hop to attest, so a routing proof there would
// be a field with nothing behind it — and the fingerprint is planted here on
// purpose, so the assertion turns on the provider type rather than on the
// evidence happening to be absent.
func TestSealedResponseOnDecentralizedProviderCarriesNoRoutingProof(t *testing.T) {
	f := newE2EEFixture(t) // ProviderType left unset: not centralized
	ctx := sealedCtx(t, f, testFingerprint)

	const e2eeText = "zg-sig-v1/e2ee-ct:aa:bb"
	if err := f.c.signChatResponse(ctx, []byte(`{}`), []byte(`{}`), "ck-dec", e2eeText, ""); err != nil {
		t.Fatalf("signChatResponse: %v", err)
	}
	sig, err := f.c.GetChatSignature("ck-dec")
	if err != nil {
		t.Fatalf("GetChatSignature: %v", err)
	}
	if sig.RoutingProof != nil {
		t.Errorf("a decentralized provider emitted a routing proof: %+v", sig.RoutingProof)
	}
}

// TestUnsealedCentralizedSignatureShapeUnchanged is the regression guard for the
// half of this change that must NOT be observable. An unsealed centralized
// response still carries its routing proof as the top-level signature with the
// three flat fields, and nests nothing — the build/publish split was a refactor
// there, not a behaviour change, and a client reading provider_type off the top
// level keeps working.
func TestUnsealedCentralizedSignatureShapeUnchanged(t *testing.T) {
	f := sealedCentralizedFixture(t)
	ctx := newGinCtx() // NOT marked sealed
	ctx.Set(CtxKeyUpstreamCertFingerprint, testFingerprint)

	if err := f.c.signChatResponse(ctx, []byte(`{"model":"m"}`), []byte(`{"id":"x"}`), "ck-plain", "", ""); err != nil {
		t.Fatalf("signChatResponse: %v", err)
	}
	sig, err := f.c.GetChatSignature("ck-plain")
	if err != nil {
		t.Fatalf("GetChatSignature: %v", err)
	}
	if got := len(strings.Split(sig.Text, ":")); got != 5 {
		t.Errorf("top-level text has %d fields, want the 5-field routing proof: %q", got, sig.Text)
	}
	if sig.TLSCertFingerprint != testFingerprint {
		t.Errorf("top-level fingerprint = %q, want %q", sig.TLSCertFingerprint, testFingerprint)
	}
	if sig.ProviderType != constant.ProviderTypeCentralized {
		t.Errorf("top-level provider type = %q, want %q", sig.ProviderType, constant.ProviderTypeCentralized)
	}
	if sig.RoutingProof != nil {
		t.Errorf("an unsealed centralized response nested a duplicate proof: %+v", sig.RoutingProof)
	}
	// And the flat fields still agree with the signed text they describe, which
	// is the invariant the refactor could most plausibly have broken by mapping
	// one field to the wrong source.
	if !strings.Contains(sig.Text, sig.TLSCertFingerprint) {
		t.Errorf("signed text %q does not carry the reported fingerprint %q", sig.Text, sig.TLSCertFingerprint)
	}
}
