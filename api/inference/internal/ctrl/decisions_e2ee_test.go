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

// What the tests check never leaks: the state, and the caller's own criterion
// text inside the questions.
const (
	decSecretState     = "patient 4471 asks about their biopsy results"
	decSecretCriterion = "Answering a factual medical question"
)

func decisionsPlainRequest(t *testing.T) wire.Request {
	t.Helper()
	return wire.Request{
		"model": mustRaw(t, "vllm-sr/Vela-2.0-9B"),
		"state": mustRaw(t, decSecretState),
		"questions": json.RawMessage(`{"task":{"type":"choice","instructions":"Which?",` +
			`"criteria":{"qa_knowledge":"` + decSecretCriterion + `","math":"Solving math"}}}`),
	}
}

func (f *e2eeTestFixture) sealDecisionsRequest(t *testing.T) []byte {
	t.Helper()
	sealed, err := wire.SealRequestFor(wire.ProfileDecisions, f.encPub, decisionsPlainRequest(t), nil, f.signerAddr, f.clientEphPub)
	if err != nil {
		t.Fatalf("SealRequestFor(decisions): %v", err)
	}
	b, err := json.Marshal(sealed)
	if err != nil {
		t.Fatalf("marshal sealed decisions request: %v", err)
	}
	return b
}

// The request side: the enclave recovers both fields and the envelope is gone.
func TestMaybeUnsealDecisionsRequestReconstructsStateAndQuestions(t *testing.T) {
	f := newE2EEFixture(t)
	f.c.Service = config.Service{Type: constant.ServiceTypeDecisions}
	out, err := unsealOn(f.c, newEmbeddingGinCtx(constant.ServicePrefix+"/decisions"), f.sealDecisionsRequest(t))
	if err != nil {
		t.Fatalf("unseal decisions request: %v", err)
	}
	var req map[string]json.RawMessage
	if err := json.Unmarshal(out, &req); err != nil {
		t.Fatalf("reconstructed request is not JSON: %v", err)
	}
	var state string
	if err := json.Unmarshal(req["state"], &state); err != nil || state != decSecretState {
		t.Fatalf("enclave did not recover the state: %q (%v)", state, err)
	}
	if !strings.Contains(string(req["questions"]), decSecretCriterion) {
		t.Fatalf("enclave did not recover the questions: %s", req["questions"])
	}
	if _, ok := req["_e2ee"]; ok {
		t.Error("the envelope must not survive into the reconstructed request")
	}
}

// A sealed decisions envelope opens on /decisions and nowhere else.
func TestMaybeUnsealDecisionsRequestIsScopedToTheDecisionsRoute(t *testing.T) {
	for _, tc := range []struct {
		path     string
		offRoute bool
	}{
		{constant.ServicePrefix + "/decisions", false},
		{constant.ServicePrefix + "/v1/decisions", false},
		{constant.ServicePrefix + "/attestation/report", true},
		{constant.ServicePrefix + "/signature/decisions", true},
		{constant.ServicePrefix + "/embeddings", true},
	} {
		t.Run(tc.path, func(t *testing.T) {
			f := newE2EEFixture(t)
			f.c.Service = config.Service{Type: constant.ServiceTypeDecisions}
			out, err := unsealOn(f.c, newEmbeddingGinCtx(tc.path), f.sealDecisionsRequest(t))
			if tc.offRoute {
				if err == nil || !strings.Contains(err.Error(), "is only accepted on /decisions") {
					t.Fatalf("want the off-route refusal on %s, got %v (%s)", tc.path, err, out)
				}
				if strings.Contains(string(out), decSecretState) {
					t.Fatal("an off-route refusal must not return the decrypted state")
				}
				return
			}
			if err != nil || !strings.Contains(string(out), decSecretState) {
				t.Fatalf("a sealed decisions envelope must open on %s: %v", tc.path, err)
			}
		})
	}
}

func TestWithDecisionsUsage(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		wantErr    bool
		wantOut    int // output_tokens kept from upstream; -1 = absent
	}{
		{"no usage", `{"answers":{}}`, false, -1},
		{"usage null", `{"answers":{},"usage":null}`, false, -1},
		{"keeps output_tokens, overwrites input_tokens", `{"answers":{},"usage":{"input_tokens":1,"output_tokens":7}}`, false, 7},
		{"null body", `null`, true, 0},
		{"not an object", `[1,2]`, true, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := withDecisionsUsage([]byte(tc.body), 212)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("want an error, got %s", out)
				}
				return
			}
			if err != nil {
				t.Fatalf("withDecisionsUsage: %v", err)
			}
			var resp struct {
				Usage map[string]int `json:"usage"`
			}
			if err := json.Unmarshal(out, &resp); err != nil {
				t.Fatalf("output is not JSON: %v", err)
			}
			if resp.Usage["input_tokens"] != 212 {
				t.Errorf("usage.input_tokens = %d, want 212", resp.Usage["input_tokens"])
			}
			if got, ok := resp.Usage["output_tokens"]; tc.wantOut >= 0 && (!ok || got != tc.wantOut) {
				t.Errorf("output_tokens = %d (%v), want %d", got, ok, tc.wantOut)
			} else if tc.wantOut < 0 && ok {
				t.Errorf("output_tokens must not be invented, got %d", got)
			}
		})
	}
}

// The real handler on a sealed turn, on both signing topologies: the answers
// reach the client sealed, the frame publishes the count the broker billed,
// and the handle resolves to a signature.
func TestHandleDecisionsResponseSealsAndPublishesTheBilledCount(t *testing.T) {
	for _, tc := range []struct {
		name  string
		svc   config.Service
		usage string
	}{
		{name: "in-network decentralized", svc: config.Service{Type: constant.ServiceTypeDecisions, ProviderType: constant.ProviderTypeDecentralized}},
		{name: "targetSeparated, not centralized", svc: config.Service{Type: constant.ServiceTypeDecisions, ProviderType: constant.ProviderTypeStandard, TargetSeparated: true}},
		{name: "upstream reports usage", svc: config.Service{Type: constant.ServiceTypeDecisions, ProviderType: constant.ProviderTypeDecentralized}, usage: `"usage":{"input_tokens":33,"output_tokens":0},`},
	} {
		t.Run(tc.name, func(t *testing.T) { runSealedDecisionsHandler(t, tc.svc, tc.usage) })
	}
}

func runSealedDecisionsHandler(t *testing.T, svc config.Service, providerUsage string) {
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
	env, err := wire.SealRequestFor(wire.ProfileDecisions, encPub, decisionsPlainRequest(t), nil, c.teeService.Address.Hex(), clientEphPub)
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
	ctx.Request = httptest.NewRequest("POST", "/v1/proxy/decisions", nil)
	reconstructed, err := unsealOn(c, ctx, sealedReqBody)
	if err != nil {
		t.Fatalf("unseal: %v", err)
	}

	provider := []byte(`{"model":"vllm-sr/Vela-2.0-9B",` + providerUsage +
		`"answers":{"task":{"type":"choice","choice":"qa_knowledge","confidence":0.97}}}`)
	resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(bytes.NewReader(provider))}
	reqModel := model.Request{IsWhitelisted: true, ServiceName: "decisions", RequestHash: "h"}
	if err := c.handleDecisionsResponse(ctx, resp, model.User{}, "0", reconstructed, reqModel); err != nil {
		t.Fatalf("handleDecisionsResponse: %v", err)
	}

	handle := rec.Header().Get("ZG-Res-Key")
	if handle == "" {
		t.Fatal("a sealed turn must publish ZG-Res-Key on every signing topology")
	}
	if _, err := c.GetChatSignature(handle); err != nil {
		t.Fatalf("ZG-Res-Key %q does not resolve to a cached signature: %v", handle, err)
	}

	out := rec.Body.Bytes()
	if strings.Contains(string(out), "qa_knowledge") {
		t.Fatal("the chosen criterion reached the client in the clear")
	}
	var frame wire.Response
	if err := json.Unmarshal(out, &frame); err != nil {
		t.Fatalf("written body is not a sealed frame: %v (%s)", err, out)
	}
	if _, ok := frame["_e2ee"]; !ok {
		t.Fatalf("written body is not sealed: %s", out)
	}
	if _, ok := frame["answers"]; ok {
		t.Fatal("answers must be sealed, not cleartext")
	}
	var usage struct {
		InputTokens int `json:"input_tokens"`
	}
	if err := json.Unmarshal(frame["usage"], &usage); err != nil || usage.InputTokens <= 0 {
		t.Fatalf("usage.input_tokens must be readable and positive: %+v (%v)", usage, err)
	}
	if len(recon.calls) != 1 {
		t.Fatalf("want exactly one usage row recorded, got %d", len(recon.calls))
	}
	if billed := recon.calls[0].InputCount; int64(usage.InputTokens) != billed {
		t.Fatalf("the frame publishes %d tokens but the broker accounted for %d", usage.InputTokens, billed)
	}
	opened, err := wire.OpenResponseFor(wire.ProfileDecisions, clientEphSk, frame)
	if err != nil {
		t.Fatalf("client open: %v", err)
	}
	if !strings.Contains(string(opened["answers"]), "qa_knowledge") {
		t.Fatalf("client did not recover the answers: %s", opened["answers"])
	}
}
