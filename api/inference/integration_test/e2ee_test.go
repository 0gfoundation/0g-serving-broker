//go:build integration

package integration_test

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	pccrypto "github.com/0gfoundation/0g-pc-e2ee/protocol/crypto"
	"github.com/0gfoundation/0g-pc-e2ee/protocol/wire"

	"github.com/0glabs/0g-serving-broker/inference/config"
	constant "github.com/0glabs/0g-serving-broker/inference/const"
	"github.com/0glabs/0g-serving-broker/inference/internal/ctrl"
	"github.com/0glabs/0g-serving-broker/inference/model"
)

// The E2EE tests that need a real broker: a real route table, a real MySQL, and a
// real settlement path.
//
// The ctrl-level tests (chatbot_e2ee_test.go and friends) build a synthetic gin
// context and call the seal/unseal functions directly, which is the right layer
// for the frame taxonomy and the fail-closed rules. Three things they structurally
// cannot reach, and all three are load-bearing claims of the design:
//
//   - the ROUTE resolves the profile. A ctrl test states the path in a fixture; if
//     proxy.Start() mounted /v1/proxy/messages on the wrong handler, or the
//     surface key stopped reaching profileForRequest, every ctrl test still passes.
//   - the enclave is the DECRYPTION POINT. The broker opens the envelope and
//     forwards plaintext upstream; only a test with a real upstream can assert
//     what the model server actually received.
//   - a sealed request is STILL BILLED. "The broker bills without reading the
//     payload" is the whole premise, and it is a row in MySQL — nothing below this
//     layer can check it.
//
// A caveat worth stating: the sealed request is built with the real client-side
// sealer (protocol/wire), so these also pin the broker against the shipped
// protocol rather than against a hand-rolled envelope this repo controls.

// sealedClient is a test client's half of one sealed exchange: the envelope to
// POST and the ephemeral private key needed to open the response.
type sealedClient struct {
	envelope []byte
	ephPriv  pccrypto.PrivateKey
}

// sealFor builds a sealed request for a surface, using the REAL sealer against
// the enclave key this env installed.
//
// key_id is not passed: wire derives it as SHA-256(enc_pub)[0:8], the same
// derivation setupSealedTestEnv uses, so an envelope sealed to env.encPub always
// selects the key the broker holds. That is what makes the mismatch test below
// have to reach for a foreign key rather than a bad string.
func sealFor(t *testing.T, env *testEnv, profile wire.Profile, req wire.Request, sealedFields []string) sealedClient {
	t.Helper()
	return sealToKey(t, env.encPub, env.teeSigner, profile, req, sealedFields)
}

// sealToKey is sealFor with the recipient named explicitly, for the one test
// that must seal to a key this enclave does NOT hold.
func sealToKey(t *testing.T, encPub pccrypto.PublicKey, signerAddr string, profile wire.Profile, req wire.Request, sealedFields []string) sealedClient {
	t.Helper()
	ephPriv, ephPub, err := pccrypto.GenerateRecipientKey()
	if err != nil {
		t.Fatalf("generate client ephemeral key: %v", err)
	}
	envelope, err := wire.SealRequestFor(profile, encPub, req, sealedFields, signerAddr, ephPub)
	if err != nil {
		t.Fatalf("seal request: %v", err)
	}
	raw, err := json.Marshal(envelope)
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	return sealedClient{envelope: raw, ephPriv: ephPriv}
}

// postSealed sends a sealed envelope through the REAL engine, at the real
// client-facing path.
func postSealed(t *testing.T, env *testEnv, path string, sc sealedClient) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("POST", path, strings.NewReader(string(sc.envelope)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", createAuthHeader(t, env.privateKey, env.providerAddr))
	w := httptest.NewRecorder()
	env.engine.ServeHTTP(w, req)
	return w
}

// billedFee returns the fee on this user's one request row. Reading it out of
// MySQL is the point: a sealed request the broker could not bill would still
// return a perfectly good response.
//
// It requires EXACTLY one row rather than taking the newest of several. Each
// setupTestEnv generates a fresh user key, so a test that made one billable
// request has one row — which makes "exactly one" a free assertion against
// double-billing, and avoids depending on list order (ListRequest defaults to
// created_at DESC, so the LAST element is the oldest, not the latest).
func billedFee(t *testing.T, env *testEnv) string {
	t.Helper()
	mine := filterUserRequests(t, env)
	if len(mine) != 1 {
		t.Fatalf("want exactly 1 billing record for one sealed request, got %d: %v", len(mine), mine)
	}
	return mine[0].Fee
}

// newMockAnthropicProvider is the upstream model server for the Anthropic
// surface, at the post-strip path (/messages, per constant.TargetRoute).
//
// It asserts on what it RECEIVED, which is the half no other layer can: the
// broker must forward the opened PLAINTEXT, so `messages` and the top-level
// `system` must be here in the clear and `_e2ee` must be gone. An upstream that
// received the envelope would mean the broker forwarded a request it never
// opened.
func newMockAnthropicProvider(t *testing.T, saw *map[string]any) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || !strings.HasSuffix(strings.TrimRight(r.URL.Path, "/"), "/messages") {
			t.Errorf("unexpected upstream request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("upstream could not decode the forwarded body: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		*saw = body
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		// A non-streaming Anthropic turn: the `message` shape, whose payload field
		// is `content` (SPEC §7.2).
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":      "msg_upstream_001",
			"type":    "message",
			"role":    "assistant",
			"model":   "claude-x",
			"content": []map[string]string{{"type": "text", "text": "Hello world"}},
			"usage":   map[string]any{"input_tokens": 10, "output_tokens": 2},
		})
	}))
}

// A sealed Anthropic request, end to end through the real broker.
//
// The profile assertion is the subtle one, and it is why this test asserts the
// RESPONSE's sealed set rather than just a 200. An envelope whose sealed_fields
// are ["messages","system"] OPENS fine under the chat profile too — wire requires
// only that the set covers the profile's payload, and a superset is legal. The
// profiles diverge on the way back: chat seals `choices`, and the Anthropic
// `message` shape seals `content`. So "the response sealed content" is the
// evidence that the real route resolved ProfileAnthropic.
func TestE2EE_SealedAnthropicRequest_RealRouteRealDB(t *testing.T) {
	var upstreamSaw map[string]any
	provider := newMockAnthropicProvider(t, &upstreamSaw)
	t.Cleanup(provider.Close)

	env := setupSealedTestEnv(t, func(cfg *config.Config) {
		cfg.Service.TargetURL = provider.URL
		cfg.Service.Type = "chatbot"
		cfg.Service.ModelType = "claude-x"
		cfg.Service.TargetSeparated = true
	})

	const (
		secretPrompt = "the secret user prompt"
		secretSystem = "the secret system prompt"
	)
	sc := sealFor(t, env, wire.ProfileAnthropic, wire.Request{
		"model":      json.RawMessage(`"claude-x"`),
		"max_tokens": json.RawMessage(`1024`),
		"stream":     json.RawMessage(`false`),
		"system":     json.RawMessage(`"` + secretSystem + `"`),
		"messages":   json.RawMessage(`[{"role":"user","content":"` + secretPrompt + `"}]`),
	}, []string{"messages", "system"})

	w := postSealed(t, env, "/v1/proxy/messages", sc)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	// (1) The enclave is the decryption point: the model server saw plaintext.
	if upstreamSaw == nil {
		t.Fatal("the upstream was never called")
	}
	if _, forwarded := upstreamSaw["_e2ee"]; forwarded {
		t.Error("the broker forwarded the sealed envelope upstream instead of the opened request")
	}
	if got, _ := upstreamSaw["system"].(string); got != secretSystem {
		t.Errorf("upstream system = %q, want the opened %q", got, secretSystem)
	}
	if !strings.Contains(mustJSONString(t, upstreamSaw["messages"]), secretPrompt) {
		t.Errorf("upstream messages did not carry the opened prompt: %v", upstreamSaw["messages"])
	}

	// (2) The client's response is sealed, and only the client can read it.
	body := w.Body.Bytes()
	for _, secret := range []string{secretPrompt, secretSystem} {
		if strings.Contains(string(body), secret) {
			t.Errorf("%q came back in the clear", secret)
		}
	}
	var sealedResp wire.Response
	if err := json.Unmarshal(body, &sealedResp); err != nil {
		t.Fatalf("response is not a JSON object: %v", err)
	}
	meta, err := sealedResp.E2EE()
	if err != nil {
		t.Fatalf("response carries no readable _e2ee: %v (%s)", err, body)
	}
	// (3) The sealed set is what proves the PROFILE, and therefore the route.
	if !containsString(meta.SealedFields, "content") {
		t.Errorf("response sealed_fields = %v, want it to seal \"content\": the Anthropic message shape "+
			"seals content while chat seals choices, so this is what says the route resolved the Anthropic profile",
			meta.SealedFields)
	}
	if containsString(meta.SealedFields, "choices") {
		t.Errorf("response sealed \"choices\": the route resolved the CHAT profile for %s", "/v1/proxy/messages")
	}
	if _, cleartext := sealedResp["content"]; cleartext {
		t.Error("`content` is present in the response cleartext as well as sealed")
	}
	// Open it as the client would, which is the only proof the seal is usable
	// rather than merely present.
	opened, err := wire.OpenResponseFor(wire.ProfileAnthropic, sc.ephPriv, sealedResp)
	if err != nil {
		t.Fatalf("the client could not open the sealed response: %v", err)
	}
	if !strings.Contains(mustJSONString(t, opened["content"]), "Hello world") {
		t.Errorf("opened content = %v, want the upstream's text", opened["content"])
	}

	// (4) It was billed. The premise of the whole design is that the broker meters
	// a request whose payload it cannot read.
	if fee := billedFee(t, env); fee == "" || fee == "0" {
		t.Errorf("sealed request billed fee %q: the response was served but not metered", fee)
	}
	// `usage` must stay cleartext for exactly that reason — the router bills on it
	// without a key (SPEC §7).
	if _, ok := sealedResp["usage"]; !ok {
		t.Error("the sealed response withheld `usage`, which the router must read without a key")
	}

	// (5) The client gets a handle to fetch the §8 signature with. Whether that
	// fetch is SERVED here depends on the provider shape, so the fetch itself is
	// asserted in TestE2EE_SealedResponseSignatureIsFetchable rather than here —
	// see that test for why, and for the gap it documents.
	if w.Header().Get("ZG-Res-Key") == "" {
		t.Error("no ZG-Res-Key on a sealed response: the client has no handle to fetch the §8 signature with")
	}
}

// The same chain on the OpenAI chat surface, which is what makes the profile
// assertion above mean something: if the route ignored the surface and always
// resolved one profile, one of these two tests fails.
func TestE2EE_SealedChatRequest_RealRouteRealDB(t *testing.T) {
	provider := newMockChatbotProvider(t)
	t.Cleanup(provider.Close)

	env := setupSealedTestEnv(t, func(cfg *config.Config) {
		cfg.Service.TargetURL = provider.URL
		cfg.Service.Type = "chatbot"
		cfg.Service.ModelType = "gpt-4o"
		cfg.Service.TargetSeparated = true
	})

	const secretPrompt = "the secret user prompt"
	sc := sealFor(t, env, wire.ProfileChat, wire.Request{
		"model":    json.RawMessage(`"gpt-4o"`),
		"stream":   json.RawMessage(`false`),
		"messages": json.RawMessage(`[{"role":"user","content":"` + secretPrompt + `"}]`),
	}, []string{"messages"})

	w := postSealed(t, env, "/v1/proxy/chat/completions", sc)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	body := w.Body.Bytes()
	if strings.Contains(string(body), secretPrompt) {
		t.Error("the prompt came back in the clear")
	}
	var sealedResp wire.Response
	if err := json.Unmarshal(body, &sealedResp); err != nil {
		t.Fatalf("response is not a JSON object: %v", err)
	}
	meta, err := sealedResp.E2EE()
	if err != nil {
		t.Fatalf("response carries no readable _e2ee: %v (%s)", err, body)
	}
	if !containsString(meta.SealedFields, "choices") {
		t.Errorf("response sealed_fields = %v, want it to seal \"choices\" on the chat surface", meta.SealedFields)
	}
	if _, err := wire.OpenResponseFor(wire.ProfileChat, sc.ephPriv, sealedResp); err != nil {
		t.Fatalf("the client could not open the sealed chat response: %v", err)
	}
	if fee := billedFee(t, env); fee == "" || fee == "0" {
		t.Errorf("sealed chat request billed fee %q", fee)
	}
}

// A request sealed to a FOREIGN enc key is refused with 409 and nothing is
// billed.
//
// Both halves live above ctrl: the 409 (rather than a generic 400) is
// proxy.handleBrokerError's mapping of ErrE2EEKeyMismatch, which exists so the
// router re-fetches the key and re-seals instead of bouncing the user; and "not
// billed" is a row that must NOT be in MySQL, which only a real DB can say.
//
// The key is foreign rather than a corrupted string because that is the real
// scenario — a provider upgrade rotated the enclave key under a client that had
// cached the old one — and because verifyEncKeyID runs BEFORE opening, so the
// client gets the self-healing signal rather than an HPKE failure.
func TestE2EE_SealedToAForeignKey_Returns409AndBillsNothing(t *testing.T) {
	var upstreamSaw map[string]any
	provider := newMockAnthropicProvider(t, &upstreamSaw)
	t.Cleanup(provider.Close)

	env := setupSealedTestEnv(t, func(cfg *config.Config) {
		cfg.Service.TargetURL = provider.URL
		cfg.Service.Type = "chatbot"
		cfg.Service.ModelType = "claude-x"
		cfg.Service.TargetSeparated = true
	})

	before := len(filterUserRequests(t, env))

	// Someone else's enclave key: the envelope is well formed, just not for us.
	_, foreignPub, err := pccrypto.GenerateRecipientKey()
	if err != nil {
		t.Fatalf("generate foreign key: %v", err)
	}
	sc := sealToKey(t, foreignPub, env.teeSigner, wire.ProfileAnthropic, wire.Request{
		"model":      json.RawMessage(`"claude-x"`),
		"max_tokens": json.RawMessage(`16`),
		"messages":   json.RawMessage(`[{"role":"user","content":"hi"}]`),
	}, []string{"messages"})

	w := postSealed(t, env, "/v1/proxy/messages", sc)
	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409 (the self-healing signal), got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "e2ee_key_mismatch") {
		t.Errorf("the 409 must carry the e2ee_key_mismatch token the router matches on: %s", w.Body.String())
	}
	// The message promises the current key_id as a hint; an empty or wrong one
	// would send the client back to re-seal against nothing.
	if want := base64.RawURLEncoding.EncodeToString(env.keyID); !strings.Contains(w.Body.String(), want) {
		t.Errorf("the 409 must name the enclave's current key_id %q as a hint: %s", want, w.Body.String())
	}

	// Detected pre-inference: the upstream was never reached and nothing was
	// metered. A mismatch that billed would charge for compute that never ran.
	if upstreamSaw != nil {
		t.Errorf("the upstream was called for a request that could not be opened: %v", upstreamSaw)
	}
	if after := len(filterUserRequests(t, env)); after != before {
		t.Errorf("billing rows went %d -> %d: a key mismatch is pre-inference and must bill nothing", before, after)
	}
}

// filterUserRequests is billedFee's counting half, for a test that asserts the
// ABSENCE of a row.
func filterUserRequests(t *testing.T, env *testEnv) []model.Request {
	t.Helper()
	requests, _, err := env.ctrl.ListRequest(model.RequestListOptions{})
	if err != nil {
		t.Fatalf("list requests: %v", err)
	}
	return filterRequestsByUser(requests, env.userAddr)
}

func containsString(hay []string, needle string) bool {
	for _, s := range hay {
		if s == needle {
			return true
		}
	}
	return false
}

func mustJSONString(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal %v: %v", v, err)
	}
	return string(b)
}

// The §8 response signature is stored and fetchable: what makes a sealed
// response ATTESTABLE rather than merely confidential.
//
// It runs on a CENTRALIZED provider, and that is the finding rather than a
// convenience. proxy.handleSignatureRoute serves /signature/{key} from the
// broker's own cache only when `!TargetSeparated || IsCentralized() ||
// IsStandard()`; on a decentralized (TargetSeparated) provider it returns false
// and the path falls through to the FreePrefixes branch, which PROXIES it
// upstream — where no such signature exists.
//
// But signChatE2EE runs on EVERY sealed request, before the centralized branch.
// So a decentralized provider serving a sealed request computes and caches a §8
// signature its own client cannot fetch. That is a product gap, not a property
// of this test, so it is reported on the PR rather than papered over here: this
// test asserts the behaviour on the shape where the route is served, and the
// decentralized case is deliberately left unasserted rather than pinned to a
// value nobody wants.
func TestE2EE_SealedResponseSignatureIsFetchable(t *testing.T) {
	var upstreamSaw map[string]any
	provider := newMockAnthropicProvider(t, &upstreamSaw)
	t.Cleanup(provider.Close)

	env := setupSealedTestEnv(t, func(cfg *config.Config) {
		cfg.Service.TargetURL = provider.URL
		cfg.Service.Type = "chatbot"
		cfg.Service.ModelType = "claude-x"
		cfg.Service.TargetSeparated = true
		// The shape that owns its own signatures (see above).
		cfg.Service.ProviderType = constant.ProviderTypeCentralized
	})

	const answer = "Hello world"
	sc := sealFor(t, env, wire.ProfileAnthropic, wire.Request{
		"model":      json.RawMessage(`"claude-x"`),
		"max_tokens": json.RawMessage(`64`),
		"stream":     json.RawMessage(`false`),
		"messages":   json.RawMessage(`[{"role":"user","content":"hi"}]`),
	}, []string{"messages"})

	w := postSealed(t, env, "/v1/proxy/messages", sc)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	chatKey := w.Header().Get("ZG-Res-Key")
	if chatKey == "" {
		t.Fatal("no ZG-Res-Key on a sealed response")
	}

	sigW := httptest.NewRecorder()
	env.engine.ServeHTTP(sigW, httptest.NewRequest("GET", "/v1/proxy/signature/"+chatKey, nil))
	if sigW.Code != http.StatusOK {
		t.Fatalf("signature endpoint = %d, want 200: %s", sigW.Code, sigW.Body.String())
	}
	// Decoded into the REAL type, as chatbot_test.go does, rather than a local
	// struct with hand-written tags: the wire names are `signature` and
	// `signing_address`, not the Go field names, and a mistyped tag decodes to a
	// zero value that a presence assertion then reports as a product failure.
	var sig ctrl.ChatSignature
	if err := json.Unmarshal(sigW.Body.Bytes(), &sig); err != nil {
		t.Fatalf("parse signature: %v (%s)", err, sigW.Body.String())
	}
	if sig.SignatureEcdsa == "" {
		t.Error("the §8 signature is empty")
	}
	// It signs the on-wire CIPHERTEXT binding, so the answer must not be inside
	// the signed text either — the one place a sealed exchange could still spill
	// it, and the reason this asserts the text rather than just its presence.
	if strings.Contains(sig.Text, answer) {
		t.Errorf("the §8 signed text carries the plaintext answer: %s", sig.Text)
	}
	// The signer is this enclave, which is what ties the signature to the identity
	// the request was pinned to.
	if !strings.EqualFold(sig.SigningAddressEcdsa.Hex(), env.teeSigner) {
		t.Errorf("signed by %q, want this enclave %q", sig.SigningAddressEcdsa.Hex(), env.teeSigner)
	}
}
