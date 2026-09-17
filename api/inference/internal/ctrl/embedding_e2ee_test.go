package ctrl

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	pccrypto "github.com/0gfoundation/0g-pc-e2ee/protocol/crypto"
	"github.com/0gfoundation/0g-pc-e2ee/protocol/wire"
	"github.com/0glabs/0g-serving-broker/inference/config"
	constant "github.com/0glabs/0g-serving-broker/inference/const"
	"github.com/0glabs/0g-serving-broker/inference/model"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/gin-gonic/gin"

	teeutil "github.com/0glabs/0g-serving-broker/common/tee"
)

// The corpus the tests check never leaks. Sealed as a two-element ARRAY rather
// than one string, because the batch form is what a retrieval pipeline sends and
// it is the form where `input` is a corpus rather than a query.
const (
	embSecretA = "patient chart 4471: presenting complaint"
	embSecretB = "internal roadmap Q3: acquisition targets"
)

// sealEmbeddingRequest seals with the profile's DEFAULT sealed set (nil), which
// is the only set these tests need: the non-default cases are argued at build
// time in TestMaybeUnsealEmbeddingRequestRejectsCleartextInput, which calls
// wire.SealRequestFor directly. No `sealedFields` parameter, so there is no
// unused knob to mislead the next caller.
func (f *e2eeTestFixture) sealEmbeddingRequest(t *testing.T) []byte {
	t.Helper()
	req := wire.Request{
		"model":           mustRaw(t, "qwen3.7-text-embedding"),
		"encoding_format": mustRaw(t, "base64"),
		"dimensions":      mustRaw(t, 256),
		"input":           mustRaw(t, []string{embSecretA, embSecretB}),
	}
	sealed, err := wire.SealRequestFor(wire.ProfileEmbedding, f.encPub, req, nil, f.signerAddr, f.clientEphPub)
	if err != nil {
		t.Fatalf("SealRequestFor(embedding): %v", err)
	}
	b, err := json.Marshal(sealed)
	if err != nil {
		t.Fatalf("marshal sealed embedding request: %v", err)
	}
	return b
}

// newSealedEmbeddingCtx unseals a sealed embedding request on a fresh context, so
// the response-side tests below run with the same context state the real handler
// sees (profile, ephemeral key, request binding hash).
func (f *e2eeTestFixture) newSealedEmbeddingCtx(t *testing.T) (*gin.Context, []byte) {
	t.Helper()
	f.c.Service = config.Service{Type: constant.ServiceTypeEmbedding}
	ctx := newGinCtx()
	out, err := unsealOn(f.c, ctx, f.sealEmbeddingRequest(t))
	if err != nil {
		t.Fatalf("unseal embedding request: %v", err)
	}
	return ctx, out
}

// The request side: the enclave recovers `input`, the router never saw it, and
// the two fields this profile deliberately leaves cleartext survive so the
// upstream still gets them.
func TestMaybeUnsealEmbeddingRequestReconstructsInput(t *testing.T) {
	f := newE2EEFixture(t)
	_, out := f.newSealedEmbeddingCtx(t)

	var req map[string]json.RawMessage
	if err := json.Unmarshal(out, &req); err != nil {
		t.Fatalf("reconstructed request is not JSON: %v", err)
	}
	var input []string
	if err := json.Unmarshal(req["input"], &input); err != nil {
		t.Fatalf("decode reconstructed input: %v", err)
	}
	if len(input) != 2 || input[0] != embSecretA || input[1] != embSecretB {
		t.Fatalf("enclave did not recover the batch: %v", input)
	}
	// §6 reconstructs `cleartext ∪ decrypted`, and the upstream needs both of
	// these — they are the request's two knobs, not payload.
	for f, want := range map[string]string{
		"encoding_format": `"base64"`,
		"dimensions":      `256`,
	} {
		if got := string(req[f]); got != want {
			t.Errorf("reconstructed request lost %s: got %s, want %s", f, got, want)
		}
	}
	if _, ok := req["_e2ee"]; ok {
		t.Error("the envelope must not survive into the reconstructed request")
	}
}

// A sealed request that leaves `input` readable defeats the profile, and BOTH
// halves must refuse it — §12's rule, since a third-party client is under no
// obligation to run the sender's check.
//
// The two halves are reached differently, which is why they are separate
// assertions rather than one round trip:
//
//   - the SENDER is refused at build time for a sealed set that omits `input`;
//   - the ENCLAVE cannot be handed that same envelope — SealRequestFor will not
//     build it and there is no non-conforming request sealer — so what is
//     exercised here is the reachable half: `input` re-added to the CLEARTEXT
//     side on the wire is refused.
//
// Note WHICH mechanism refuses that second case, because it is not the payload
// rule and the difference is the point. `input` is still named in
// `sealed_fields`, so re-adding it in cleartext is a sealed/cleartext COLLISION
// (§5.1), and that check sits after the decrypt — while the cleartext half is
// inside the AAD, so the AEAD fails first. A tampered envelope can therefore
// only ever produce an authentication failure, which is the stronger answer: an
// intermediary cannot smuggle a payload field back into the readable half at
// all. The rule-based half of the enclave check (a sealed set that never covered
// the payload, caught by validatePayloadSealedFor BEFORE any decrypt) is
// profile-independent and covered upstream by the protocol's own
// TestOpenRequestForRunsEveryReceiverSideCheck.
func TestMaybeUnsealEmbeddingRequestRejectsCleartextInput(t *testing.T) {
	f := newE2EEFixture(t)
	f.c.Service = config.Service{Type: constant.ServiceTypeEmbedding}

	// Sender side.
	req := wire.Request{
		"model":      mustRaw(t, "m"),
		"dimensions": mustRaw(t, 256),
		"input":      mustRaw(t, embSecretA),
	}
	if _, err := wire.SealRequestFor(wire.ProfileEmbedding, f.encPub, req,
		[]string{"dimensions"}, f.signerAddr, f.clientEphPub); err == nil {
		t.Fatal("a sealed set omitting `input` must be refused at build time")
	} else if !strings.Contains(err.Error(), "input") {
		t.Errorf("error should name the payload field, got %v", err)
	}

	// Enclave side: a conforming envelope, then `input` re-added in the clear.
	var env map[string]json.RawMessage
	if err := json.Unmarshal(f.sealEmbeddingRequest(t), &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	env["input"] = mustRaw(t, embSecretA)
	tampered, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal tampered envelope: %v", err)
	}
	out, err := unsealOn(f.c, newGinCtx(), tampered)
	if err == nil {
		t.Fatalf("the enclave must refuse a sealed request carrying `input` in cleartext, got %s", out)
	}
	// The cleartext half is bound, so this is an authentication failure rather
	// than a rule violation — see the note above. Asserted so a future change that
	// moved `input` out of the AAD (which would make this pass for the wrong
	// reason, via the collision rule) is visible here.
	if !strings.Contains(err.Error(), "authentication failed") {
		t.Errorf("want the AEAD to refuse the tampered cleartext half, got %v", err)
	}
}

// The response side, and the core of SPEC §7.4: the vectors are sealed and the
// billable INPUT token count rides alongside as cleartext, so the router bills
// without holding the vectors.
func TestSealedEmbeddingResponseHidesVectorsAndPublishesBillableCount(t *testing.T) {
	f := newE2EEFixture(t)
	ctx, _ := f.newSealedEmbeddingCtx(t)

	// An upstream that reported no usage at all — the case that makes the §7.4
	// requirement load-bearing rather than decorative.
	provider := `{"object":"list","model":"m","data":[` +
		`{"object":"embedding","index":0,"embedding":[0.0231,-0.0917]},` +
		`{"object":"embedding","index":1,"embedding":[0.1104,0.2280]}]}`
	withUsage, err := withEmbeddingUsage([]byte(provider), 1470)
	if err != nil {
		t.Fatalf("withEmbeddingUsage: %v", err)
	}
	out, isSealed, _, err := f.c.maybeSealNonStreamResponse(ctx, withUsage)
	if err != nil || !isSealed {
		t.Fatalf("seal embedding response: sealed=%v err=%v", isSealed, err)
	}

	if strings.Contains(string(out), "0.0231") {
		t.Fatal("a vector coordinate must not appear in the sealed frame")
	}

	var frame wire.Response
	if err := json.Unmarshal(out, &frame); err != nil {
		t.Fatalf("unmarshal sealed frame: %v", err)
	}
	if _, ok := frame["data"]; ok {
		t.Fatal("data must be sealed, not cleartext")
	}
	// The router reads this without any key.
	var usage struct {
		PromptTokens int `json:"prompt_tokens"`
	}
	if err := json.Unmarshal(frame["usage"], &usage); err != nil {
		t.Fatalf("usage must be readable cleartext: %v", err)
	}
	if usage.PromptTokens != 1470 {
		t.Fatalf("usage.prompt_tokens = %d, want the billed count 1470", usage.PromptTokens)
	}

	// The client recovers the vectors.
	opened, err := wire.OpenResponseFor(wire.ProfileEmbedding, f.clientEphSk, frame)
	if err != nil {
		t.Fatalf("client open: %v", err)
	}
	var data []struct {
		Index     int       `json:"index"`
		Embedding []float64 `json:"embedding"`
	}
	if err := json.Unmarshal(opened["data"], &data); err != nil {
		t.Fatalf("decode opened data: %v", err)
	}
	if len(data) != 2 || data[1].Index != 1 || len(data[1].Embedding) != 2 {
		t.Fatalf("opened data = %+v", data)
	}
}

// usage.prompt_tokens is BOUND (not in unbound_fields), so a router that deflates
// the count to under-charge — or inflates it to over-charge — breaks the client's
// Open instead of going unnoticed.
func TestSealedEmbeddingResponseDetectsTamperedTokenCount(t *testing.T) {
	f := newE2EEFixture(t)
	ctx, _ := f.newSealedEmbeddingCtx(t)

	withUsage, err := withEmbeddingUsage([]byte(`{"data":[{"index":0,"embedding":[0.5]}]}`), 1470)
	if err != nil {
		t.Fatalf("withEmbeddingUsage: %v", err)
	}
	out, _, _, err := f.c.maybeSealNonStreamResponse(ctx, withUsage)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	var frame wire.Response
	if err := json.Unmarshal(out, &frame); err != nil {
		t.Fatalf("unmarshal frame: %v", err)
	}
	frame["usage"] = json.RawMessage(`{"prompt_tokens":1}`)
	if _, err := wire.OpenResponseFor(wire.ProfileEmbedding, f.clientEphSk, frame); err == nil {
		t.Fatal("a rewritten usage.prompt_tokens must fail the client's Open")
	}
}

// Without the count the frame does not seal AT ALL — this is the §7.4 rule
// reached through this broker's own path, not just asserted in the wire package.
//
// It is what makes withEmbeddingUsage mandatory rather than a nicety: the router
// cannot recover the number by itself, because its fallback estimator measures
// the request's `input`, which this profile seals.
func TestSealedEmbeddingResponseWithoutBillableCountIsRefused(t *testing.T) {
	f := newE2EEFixture(t)
	ctx, _ := f.newSealedEmbeddingCtx(t)

	noUsage := `{"object":"list","model":"m","data":[{"index":0,"embedding":[0.5]}]}`
	if _, _, _, err := f.c.maybeSealNonStreamResponse(ctx, []byte(noUsage)); err == nil {
		t.Fatal("an embedding response stating no billable count must be refused")
	} else if !strings.Contains(err.Error(), "prompt_tokens") {
		t.Fatalf("error should name the missing count, got: %v", err)
	}

	// A usage block that exists but omits the count is the same violation — the
	// floor on `usage` keeps the field readable, it does not require the number.
	emptyUsage := `{"object":"list","model":"m","usage":{"total_tokens":14},` +
		`"data":[{"index":0,"embedding":[0.5]}]}`
	if _, _, _, err := f.c.maybeSealNonStreamResponse(ctx, []byte(emptyUsage)); err == nil {
		t.Fatal("a usage block without prompt_tokens must be refused too")
	}
}

// Embedding has NO placeholder entry, unlike image whose `data` may be absent on
// a legitimate frame. A response with no vectors is a broken upstream, and a
// placeholder would seal, sign and mark final an empty answer while the router
// bills the prompt_tokens the same frame carries.
func TestSealedEmbeddingResponseWithoutDataIsRefused(t *testing.T) {
	f := newE2EEFixture(t)
	ctx, _ := f.newSealedEmbeddingCtx(t)

	noData, err := withEmbeddingUsage([]byte(`{"object":"list","model":"m"}`), 1470)
	if err != nil {
		t.Fatalf("withEmbeddingUsage: %v", err)
	}
	if _, _, _, err := f.c.maybeSealNonStreamResponse(ctx, noData); err == nil {
		t.Fatal("an embedding response carrying no `data` must be refused, not sealed with a placeholder")
	}
}

func TestWithEmbeddingUsage(t *testing.T) {
	tests := []struct {
		name         string
		body         string
		promptTokens int
		wantUsage    string
		wantErr      bool
	}{
		{
			name:         "no usage at all — the case §7.4 exists for",
			body:         `{"object":"list","data":[]}`,
			promptTokens: 14,
			wantUsage:    `{"prompt_tokens":14}`,
		},
		{
			name:         "upstream usage preserved, count overridden",
			body:         `{"usage":{"prompt_tokens":9,"total_tokens":9},"data":[]}`,
			promptTokens: 14,
			wantUsage:    `{"prompt_tokens":14,"total_tokens":9}`,
		},
		{
			// A provider that reported only a total: the broker's usage has already
			// normalized prompt := total upstream of here, so the two agree.
			name:         "total-only upstream keeps its total",
			body:         `{"usage":{"total_tokens":50},"data":[]}`,
			promptTokens: 50,
			wantUsage:    `{"prompt_tokens":50,"total_tokens":50}`,
		},
		{
			// `null` unmarshals into a map as the ZERO value with no error, so
			// decoding in place would leave a nil map and the write would panic.
			// The inference engine runs without gin.Recovery(), so that panic kills
			// the connection: truncated response, no billing, no attribution.
			name:         "usage null is replaced, not panicked on",
			body:         `{"usage":null,"data":[]}`,
			promptTokens: 14,
			wantUsage:    `{"prompt_tokens":14}`,
		},
		{
			name:         "usage of the wrong type is replaced",
			body:         `{"usage":"lots","data":[]}`,
			promptTokens: 14,
			wantUsage:    `{"prompt_tokens":14}`,
		},
		{
			name:         "zero is a real count, not a missing one",
			body:         `{"data":[]}`,
			promptTokens: 0,
			wantUsage:    `{"prompt_tokens":0}`,
		},
		{
			name:         "not a JSON object",
			body:         `[1,2,3]`,
			promptTokens: 14,
			wantErr:      true,
		},
		{
			// The case a `[1,2,3]` fixture walks straight past: a JSON `null` body
			// unmarshals into a nil map with NO error, so an implementation that
			// wraps the (nil) error returns (nil, nil) and the caller reads a
			// success with no body. Wants a non-nil error, like every other
			// malformed body.
			name:         "JSON null body",
			body:         `null`,
			promptTokens: 14,
			wantErr:      true,
		},
		{
			// A compressed body that could not be decoded reaches here as bytes,
			// and must fail rather than be sealed as opaque content.
			name:         "not JSON at all",
			body:         "\x1f\x8b\x08\x00",
			promptTokens: 14,
			wantErr:      true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := withEmbeddingUsage([]byte(tt.body), tt.promptTokens)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("want an error, got body %s", out)
				}
				return
			}
			if err != nil {
				t.Fatalf("withEmbeddingUsage: %v", err)
			}
			var got map[string]json.RawMessage
			if err := json.Unmarshal(out, &got); err != nil {
				t.Fatalf("result is not JSON: %v", err)
			}
			var wantMap, gotMap map[string]any
			if err := json.Unmarshal([]byte(tt.wantUsage), &wantMap); err != nil {
				t.Fatalf("bad fixture: %v", err)
			}
			if err := json.Unmarshal(got["usage"], &gotMap); err != nil {
				t.Fatalf("usage is not an object: %v", err)
			}
			if len(wantMap) != len(gotMap) {
				t.Fatalf("usage = %s, want %s", got["usage"], tt.wantUsage)
			}
			for k, want := range wantMap {
				if gotMap[k] != want {
					t.Errorf("usage[%q] = %v, want %v", k, gotMap[k], want)
				}
			}
			// Everything else the upstream sent passes through untouched.
			if _, ok := got["data"]; !ok {
				t.Error("`data` must survive: this helper only touches usage")
			}
		})
	}
}

// The end-to-end handler test, and the one that guards the failure this whole
// change exists for: on a sealed turn the count the frame publishes must be the
// count the broker BILLED, not a second computation of it.
//
// The provider here reports NO usage at all, so the broker falls back to its
// estimator — which, inside the enclave, measures the DECRYPTED input and is
// therefore accurate. Before §7.4 that number went only into the broker's own
// billing: the frame carried no usage, so the router fell through to its own
// estimator over a SEALED `input`, floored to 1 token, and the two sides priced
// one request differently. Asserting the frame's number equals the estimator's
// is what pins that shut.
// Run across both signing topologies, because they take different paths to the
// ZG-Res-Key handle and only one of them was covered before.
//
// `TargetSeparated && !IsCentralized` is the case that exposed a real hole: the
// header gate used to read `!TargetSeparated || IsCentralized()`, with no
// e2eeSealed arm, so on this topology the broker sealed the frame, signed §8 and
// cached the signature while sending NO handle — and an E2EE client refuses a
// response it cannot verify, so that is a response the caller must reject and
// has already paid for. The first arm made the decentralized row pass regardless,
// which is why one row was not enough.
func TestHandleEmbeddingResponseSealsAndPublishesTheBilledCount(t *testing.T) {
	decentralized := config.Service{
		Type:         constant.ServiceTypeEmbedding,
		ProviderType: constant.ProviderTypeDecentralized,
	}
	for _, tc := range []struct {
		name string
		svc  config.Service
		// usage is the upstream's own `usage` block (a JSON fragment ending in a
		// comma), or "" for an upstream that sends none.
		usage string
	}{
		{name: "in-network decentralized", svc: decentralized},
		{
			// Neither arm of the OLD gate fires here: TargetSeparated is true and
			// the provider is not centralized.
			name: "targetSeparated, not centralized",
			svc: config.Service{
				Type:            constant.ServiceTypeEmbedding,
				ProviderType:    constant.ProviderTypeStandard,
				TargetSeparated: true,
			},
		},
		{
			// prompt_tokens ≠ total_tokens, which is what makes "publishes the
			// BILLABLE field" falsifiable. With no upstream usage the broker's
			// estimator returns prompt == total, so a frame built from
			// `usage.TotalTokens` would be indistinguishable — this row is the one
			// that tells the two apart.
			name:  "upstream reports prompt_tokens != total_tokens",
			svc:   decentralized,
			usage: `"usage":{"prompt_tokens":9,"total_tokens":40},`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runSealedEmbeddingHandler(t, tc.svc, tc.usage)
		})
	}
}

// runSealedEmbeddingHandler drives the real handler on a sealed turn.
// providerUsage is the upstream's `usage` block, as a JSON fragment ending in a
// comma, or "" for an upstream that reports none.
func runSealedEmbeddingHandler(t *testing.T, svc config.Service, providerUsage string) {
	t.Helper()
	encPriv, encPub, err := pccrypto.GenerateRecipientKey()
	if err != nil {
		t.Fatalf("GenerateRecipientKey (enc): %v", err)
	}
	signerKey, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("generate signer key: %v", err)
	}
	keyID := sha256.Sum256(encPub)

	c := newChatbotTestCtrl(t, svc)
	// Held, not discarded: this is what the drift assertion reads. The whitelisted
	// branch funnels through recordWhitelistedUsage → AccumulateHourlyUsage, so the
	// row's InputCount is the handler's OWN number — the one it would bill on —
	// rather than a recomputation of it.
	recon := &mockReconciliationDB{}
	c.reconciliationDB = recon
	c.teeService = &teeutil.TeeService{
		ProviderSigner: signerKey,
		Address:        crypto.PubkeyToAddress(signerKey.PublicKey),
		EncPrivateKey:  encPriv,
		EncPublicKey:   encPub,
		KeyID:          keyID[:8],
	}

	clientEphSk, clientEphPub, err := pccrypto.GenerateRecipientKey()
	if err != nil {
		t.Fatalf("GenerateRecipientKey (client eph): %v", err)
	}

	// Seal a batch whose plaintext the estimator can measure, then unseal it on
	// the context the handler will run against.
	req := wire.Request{
		"model":           mustRaw(t, "m"),
		"encoding_format": mustRaw(t, "base64"),
		"input":           mustRaw(t, []string{embSecretA, embSecretB}),
	}
	env, err := wire.SealRequestFor(wire.ProfileEmbedding, encPub, req, nil,
		c.teeService.Address.Hex(), clientEphPub)
	if err != nil {
		t.Fatalf("SealRequestFor: %v", err)
	}
	sealedReqBody, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal env: %v", err)
	}

	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest("POST", "/v1/proxy/embeddings", nil)
	reconstructed, err := unsealOn(c, ctx, sealedReqBody)
	if err != nil {
		t.Fatalf("unseal: %v", err)
	}
	if !strings.Contains(string(reconstructed), embSecretA) {
		t.Fatalf("enclave did not recover the input: %s", reconstructed)
	}

	// A floor check on the fixture only — NOT the value the drift assertion
	// compares against. The number that matters is read back off the handler
	// below; recomputing it here and comparing the two would assert that the
	// estimator is deterministic, which it trivially is, and would pass just as
	// well if the handler published `usage.TotalTokens` or any other field.
	if floor := estimateEmbeddingUsageFromRequest(reconstructed).PromptTokens; floor <= 1 {
		t.Fatalf("precondition: the fixture must estimate to more than the flat floor, got %d", floor)
	}

	provider := []byte(`{"object":"list","model":"m",` + providerUsage + `"data":[` +
		`{"object":"embedding","index":0,"embedding":[0.0231,-0.0917]}]}`)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{},
		Body:       io.NopCloser(bytes.NewReader(provider)),
	}
	reqModel := model.Request{IsWhitelisted: true, ServiceName: "embedding", RequestHash: "h"}
	if err := c.handleEmbeddingResponse(ctx, resp, model.User{}, "0", reconstructed, reqModel); err != nil {
		t.Fatalf("handleEmbeddingResponse: %v", err)
	}

	// The handle, and a signature that actually resolves through it. A sealed turn
	// that ships a frame with no ZG-Res-Key gives the client no way to fetch §8,
	// and an E2EE client refuses a response it cannot verify — so a cached
	// signature with no handle is strictly worse than no signature: the caller is
	// billed for a response it must throw away.
	handle := rec.Header().Get("ZG-Res-Key")
	if handle == "" {
		t.Fatal("a sealed turn must publish ZG-Res-Key, whatever the signing topology: " +
			"the client cannot fetch the §8 signature without it")
	}
	if _, err := c.GetChatSignature(handle); err != nil {
		t.Fatalf("ZG-Res-Key %q does not resolve to a cached signature: %v", handle, err)
	}

	out := rec.Body.Bytes()
	if strings.Contains(string(out), "0.0231") {
		t.Fatal("a vector coordinate reached the client in the clear")
	}
	var frame wire.Response
	if err := json.Unmarshal(out, &frame); err != nil {
		t.Fatalf("written body is not a sealed frame: %v (%s)", err, out)
	}
	if _, ok := frame["_e2ee"]; !ok {
		t.Fatalf("written body is not sealed: %s", out)
	}
	if _, ok := frame["data"]; ok {
		t.Fatal("data must be sealed, not cleartext")
	}

	var usage struct {
		PromptTokens int `json:"prompt_tokens"`
	}
	if err := json.Unmarshal(frame["usage"], &usage); err != nil {
		t.Fatalf("usage must be readable cleartext: %v", err)
	}
	// THE drift assertion: the number in the frame must be the number the handler
	// accounted for, read back off its own ledger write rather than recomputed.
	if len(recon.calls) != 1 {
		t.Fatalf("want exactly one usage row recorded, got %d", len(recon.calls))
	}
	if billed := recon.calls[0].InputCount; int64(usage.PromptTokens) != billed {
		t.Fatalf("the frame publishes %d tokens but the broker accounted for %d: the router "+
			"would transact this request at a different price than the provider",
			usage.PromptTokens, billed)
	}

	// And the client can still open it.
	opened, err := wire.OpenResponseFor(wire.ProfileEmbedding, clientEphSk, frame)
	if err != nil {
		t.Fatalf("client open: %v", err)
	}
	if !strings.Contains(string(opened["data"]), "0.0231") {
		t.Fatalf("client did not recover the vectors: %s", opened["data"])
	}
}
