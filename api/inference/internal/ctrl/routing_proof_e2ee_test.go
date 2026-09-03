package ctrl

import (
	"strings"
	"testing"

	"github.com/0gfoundation/0g-pc-e2ee/protocol/proof"

	constant "github.com/0glabs/0g-serving-broker/inference/const"
	"github.com/gin-gonic/gin"
)

// testFingerprint is a well-formed SHA-256 certificate fingerprint. It has to
// pass teeutil.NormalizeCertFingerprint (32 hex bytes) or the proof is refused
// before any of these assertions could run.
const testFingerprint = "a1b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8f90"

// The two §8 on-wire binding hashes a sealed exchange produces (32-byte hex, as
// proof.formatText emits them). Distinct from each other and from
// testFingerprint so no assertion below can pass on a substring coincidence.
const (
	onWireReqHash  = "1111111111111111111111111111111111111111111111111111111111111111"
	onWireRespHash = "2222222222222222222222222222222222222222222222222222222222222222"
)

// centralizedFixture is newE2EEFixture with the provider shape that has a
// routing proof to offer at all. It does NOT make a request sealed — sealedCtx
// does that, separately, so the unsealed test below can share this fixture.
func centralizedFixture(t *testing.T) *e2eeTestFixture {
	t.Helper()
	f := newE2EEFixture(t)
	f.c.Service.ProviderType = constant.ProviderTypeCentralized
	f.c.Service.ProviderIdentity = "minimax"
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
	f := centralizedFixture(t)
	ctx := sealedCtx(t, f, testFingerprint)

	reqBody := []byte(`{"model":"m","_e2ee":{"v":1}}`)
	respData := []byte(`{"_e2ee":{"v":1,"ciphertext":"c2VhbGVk"}}`)
	const e2eeText = "zg-sig-v1/e2ee-ct:" + onWireReqHash + ":" + onWireRespHash

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
	if rp.ProviderIdentity != "minimax" {
		t.Errorf("provider identity = %q, want the service-level fallback (a lowercase machine key, not a host)", rp.ProviderIdentity)
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

// TestSealedRoutingProofBindsOnWireHashesNotPlaintext is the regression guard
// for the mistake this design actively invites: hashing the bytes
// signChatResponse is handed. On both chatbot paths those bytes are `clientBody`,
// which the handlers deliberately keep PLAINTEXT for billing while the sealed
// frames go to the wire — so hashing them produces two hashes that
//
//   - no sealed client can verify (it holds ciphertext, and nothing
//     canonicalizes the plaintext for it to reproduce), and
//   - leak: /v1/proxy/signature/{chatID} is unauthenticated and the router holds
//     the chatID from ZG-Res-Key, so plaintext digests published there hand a
//     confirmation oracle to the one party E2EE exists to exclude.
//
// Asserting the hashes are PRESENT would not catch either: the fix and the bug
// both produce a well-formed 5-field proof. So this asserts both directions —
// the §8 on-wire halves are in, and the plaintext digests are out.
func TestSealedRoutingProofBindsOnWireHashesNotPlaintext(t *testing.T) {
	f := centralizedFixture(t)
	ctx := sealedCtx(t, f, testFingerprint)

	// Distinguishable stand-ins for the plaintext the broker holds. Their sha256
	// is what a naive implementation would sign.
	reqPlaintext := []byte(`{"model":"m","messages":[{"role":"user","content":"SECRET PROMPT"}]}`)
	respPlaintext := []byte(`{"choices":[{"message":{"content":"SECRET ANSWER"}}]}`)
	const e2eeText = "zg-sig-v1/e2ee-ct:" + onWireReqHash + ":" + onWireRespHash

	if err := f.c.signChatResponse(ctx, reqPlaintext, respPlaintext, "ck-onwire", e2eeText, ""); err != nil {
		t.Fatalf("signChatResponse: %v", err)
	}
	sig, err := f.c.GetChatSignature("ck-onwire")
	if err != nil {
		t.Fatalf("GetChatSignature: %v", err)
	}
	if sig.RoutingProof == nil {
		t.Fatal("no routing proof to inspect")
	}
	got := sig.RoutingProof.Text

	// In: the same hashes §8 bound. This is what a verifier's mandatory
	// cross-check compares — the two signatures are independent statements by one
	// key, so a client that checks both and stops can be served §8 from one chat
	// beside a routing proof from another. Asserting it the way a verifier runs
	// it (parse both, compare the halves) is what keeps that obligation
	// satisfiable; see RoutingProof's godoc and the design doc.
	e2eeParts := strings.Split(sig.Text, ":")
	rpParts := strings.Split(got, ":")
	if len(e2eeParts) != 3 || len(rpParts) != 5 {
		t.Fatalf("unexpected text shapes: §8 %q, routing proof %q", sig.Text, got)
	}
	if rpParts[0] != e2eeParts[1] || rpParts[1] != e2eeParts[2] {
		t.Errorf("the two statements are not cross-checkable: §8 binds (%s, %s), routing proof binds (%s, %s)",
			e2eeParts[1], e2eeParts[2], rpParts[0], rpParts[1])
	}
	// Out: no digest of anything the client cannot reproduce.
	for _, leak := range []struct {
		what string
		hash string
	}{
		{"the plaintext request", sha256Hex(reqPlaintext)},
		{"the plaintext response", sha256Hex(respPlaintext)},
	} {
		if strings.Contains(got, leak.hash) {
			t.Errorf("routing proof publishes sha256 of %s (%s) on an unauthenticated endpoint: %q",
				leak.what, leak.hash, got)
		}
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
	f := centralizedFixture(t)
	ctx := sealedCtx(t, f, "") // proxy captured nothing: no TLS, or a 4xx from a sidecar

	const e2eeText = "zg-sig-v1/e2ee-ct:" + onWireReqHash + ":" + onWireRespHash
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

// TestSealedRoutingProofRefusesAMalformedE2EEText pins the fail-closed half of
// reading the hashes out of the §8 text: if that text is not
// "<scheme>:<reqHhex>:<respHhex>" with 32 hex bytes in each half, there are no
// on-wire hashes to bind and the proof must not be produced at all.
//
// Signing a truncated or non-hex half instead would put an attested value in a
// ':'-delimited text that escapes nothing — the same class of mistake the
// fingerprint validation exists to prevent. §8 itself must survive, for the same
// reason as the no-TLS case: it is the load-bearing signature.
func TestSealedRoutingProofRefusesAMalformedE2EEText(t *testing.T) {
	for _, tc := range []struct {
		name string
		text string
	}{
		{"truncated hashes", "zg-sig-v1/e2ee-ct:aa:bb"},
		{"non-hex half", "zg-sig-v1/e2ee-ct:" + onWireReqHash + ":" + strings.Repeat("z", 64)},
		{"too few fields", "zg-sig-v1/e2ee-ct:" + onWireReqHash},
		{"extra delimiter", "zg-sig-v1/e2ee-ct:" + onWireReqHash + ":" + onWireRespHash + ":extra"},
		// A DIFFERENT scheme of the same arity, whose hashes mean something else
		// entirely (plaintext, not aad‖ciphertext). Arity alone cannot tell them
		// apart, so accepting this would attest on-wire bytes over plaintext
		// hashes — the exact confusion this whole change exists to end.
		{"foreign scheme, same arity", proof.SchemePlaintext + ":" + onWireReqHash + ":" + onWireRespHash},
		{"empty scheme", ":" + onWireReqHash + ":" + onWireRespHash},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := centralizedFixture(t)
			ctx := sealedCtx(t, f, testFingerprint) // evidence IS present; only the text is bad

			if err := f.c.signChatResponse(ctx, []byte(`{}`), []byte(`{}`), "ck-bad", tc.text, ""); err != nil {
				t.Fatalf("signChatResponse must not fail over an unusable routing proof: %v", err)
			}
			sig, err := f.c.GetChatSignature("ck-bad")
			if err != nil {
				t.Fatalf("the §8 signature was not cached: %v", err)
			}
			if sig.Text != tc.text {
				t.Errorf("top-level text = %q, want the §8 text as given", sig.Text)
			}
			if sig.RoutingProof != nil {
				t.Errorf("a proof was signed over hashes taken from an unusable §8 text: %+v", sig.RoutingProof)
			}
		})
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

	const e2eeText = "zg-sig-v1/e2ee-ct:" + onWireReqHash + ":" + onWireRespHash
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
	f := centralizedFixture(t)
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
