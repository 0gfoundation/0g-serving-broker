//go:build openaicontract

package integration_test

// This file implements the OpenAI SDK contract test proposed in issue #577:
// unlike the rest of this package (build tag `integration`), which drives the
// broker with Go structs via httptest.NewRequest/ServeHTTP (in-process, no
// real socket), the tests here put a REAL TCP listener in front of the same
// in-process engine and drive it with the actual `openai` npm package running
// as a Node subprocess. That is the only way to catch SDK-level wire
// incompatibilities (SSE framing the SDK's stream parser is strict about,
// response/error JSON shape, header casing) that survive when Go-only tests
// stay green — see the issue for the full motivation.
//
// The upstream is a mocked OpenAI-shaped server (deterministic), never a live
// model — this is a contract test, not an e2e test. It is opt-in: build tag
// `openaicontract`, kept out of the default `integration` tag so it never
// runs in the default PR CI job (see .github/workflows/openai-sdk-contract.yml).
//
// Running locally:
//
//	cd api/inference/integration_test/openai_sdk_client && npm ci
//	cd api && go test -tags openaicontract ./inference/integration_test/... -run TestOpenAISDK -v

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/crypto"

	"github.com/0glabs/0g-serving-broker/inference/config"
	constant "github.com/0glabs/0g-serving-broker/inference/const"
	"github.com/0glabs/0g-serving-broker/inference/contract"
	"github.com/0glabs/0g-serving-broker/inference/model"
)

// ==========================================================================
// Mocked OpenAI-shaped upstream
// ==========================================================================

// newContractMockUpstream starts a deterministic OpenAI-shaped mock upstream.
// Behavior is derived entirely from the request the broker forwards (stream /
// tools / anything else), never from an out-of-band scenario flag, so it
// naturally exercises whatever the request-body translation pipeline
// (TranslateMaxTokens, TranslateReasoning, ...) actually produced.
//
// The default (non-stream, no tools) response echoes the exact body it
// received back to the client under "_debug_received_body". The broker's
// response sanitizer (#184) is a fixed denylist (see sanitize.go), so this
// extra field survives untouched — giving the contract test visibility into
// what reached the upstream AFTER translation, without instrumenting
// production code.
func newContractMockUpstream(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(contractMockUpstreamHandler())
}

// newContractMockUpstreamTLS is identical to newContractMockUpstream but
// serves over HTTPS so resp.TLS is populated, as required for centralized
// provider routing-proof signing (mirrors newMockChatbotProviderTLS in
// chatbot_test.go, kept independent here since that helper lives behind the
// `integration` build tag and this suite must build standalone under
// `openaicontract`).
func newContractMockUpstreamTLS(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewTLSServer(contractMockUpstreamHandler())
}

func contractMockUpstreamHandler() http.HandlerFunc {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/chat/completions" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		bodyBytes, _ := io.ReadAll(r.Body)
		var reqBody map[string]interface{}
		_ = json.Unmarshal(bodyBytes, &reqBody)

		isStream, _ := reqBody["stream"].(bool)
		includeUsage := false
		if so, ok := reqBody["stream_options"].(map[string]interface{}); ok {
			includeUsage, _ = so["include_usage"].(bool)
		}
		tools, hasTools := reqBody["tools"].([]interface{})

		switch {
		case isStream:
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			flusher, _ := w.(http.Flusher)
			write := func(s string) {
				_, _ = w.Write([]byte(s))
				if flusher != nil {
					flusher.Flush()
				}
			}
			write("data: {\"id\":\"chatcmpl-001\",\"object\":\"chat.completion.chunk\",\"model\":\"gpt-4o\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"Hello\"},\"finish_reason\":null}]}\n\n")
			write("data: {\"id\":\"chatcmpl-001\",\"object\":\"chat.completion.chunk\",\"model\":\"gpt-4o\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\" world\"},\"finish_reason\":null}]}\n\n")
			write("data: {\"id\":\"chatcmpl-001\",\"object\":\"chat.completion.chunk\",\"model\":\"gpt-4o\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
			if includeUsage {
				write("data: {\"id\":\"chatcmpl-001\",\"object\":\"chat.completion.chunk\",\"model\":\"gpt-4o\",\"choices\":[],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":2,\"total_tokens\":12}}\n\n")
			}
			write("data: [DONE]\n\n")

		case hasTools && len(tools) > 0:
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"id": "chatcmpl-001", "object": "chat.completion", "model": "gpt-4o",
				"choices": []map[string]interface{}{{
					"index": 0,
					"message": map[string]interface{}{
						"role":    "assistant",
						"content": nil,
						"tool_calls": []map[string]interface{}{{
							"id":   "call_1",
							"type": "function",
							"function": map[string]interface{}{
								"name":      "get_weather",
								"arguments": `{"location":"Paris"}`,
							},
						}},
					},
					"finish_reason": "tool_calls",
				}},
				"usage": map[string]interface{}{"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15},
			})

		default:
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"id": "chatcmpl-001", "object": "chat.completion", "model": "gpt-4o",
				"choices": []map[string]interface{}{{
					"index":         0,
					"message":       map[string]interface{}{"role": "assistant", "content": "Hello world"},
					"finish_reason": "stop",
				}},
				"usage":                map[string]interface{}{"prompt_tokens": 10, "completion_tokens": 2, "total_tokens": 12},
				"_debug_received_body": json.RawMessage(bodyBytes),
			})
		}
	})
}

// ==========================================================================
// Node SDK client invocation
// ==========================================================================

// nodeClientDir resolves the directory of the real openai-SDK contract client
// (openai_sdk_client/run.js), skipping the test with a clear message when its
// dependencies have not been installed (`npm ci`) rather than failing with a
// confusing exec error.
func nodeClientDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs("openai_sdk_client")
	if err != nil {
		t.Fatalf("resolve openai_sdk_client dir: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "node_modules", "openai")); err != nil {
		t.Skipf("openai_sdk_client/node_modules not installed; run `npm ci` in %s before this test suite: %v", dir, err)
	}
	if _, err := exec.LookPath("node"); err != nil {
		t.Skipf("node not found on PATH; required to run the OpenAI SDK contract client: %v", err)
	}
	return dir
}

// sdkResult is the JSON line run.js prints on completion.
type sdkResult struct {
	OK       bool                   `json:"ok"`
	Scenario string                 `json:"scenario"`
	Result   map[string]interface{} `json:"result"`
	Error    string                 `json:"error"`
	ErrType  string                 `json:"errorType"`
	Status   float64                `json:"status"`
}

// runNodeSDKScenario execs the real openai npm SDK (as a Node subprocess)
// against baseURL for the given scenario, asserting only that the client
// produced a well-formed result line — per-scenario assertions on the result
// live in the calling test.
func runNodeSDKScenario(t *testing.T, baseURL, authHeader, scenario string) sdkResult {
	t.Helper()
	dir := nodeClientDir(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "node", "run.js")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"BASE_URL="+baseURL,
		"AUTH_HEADER="+authHeader,
		"SCENARIO="+scenario,
	)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	runErr := cmd.Run()

	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	last := lines[len(lines)-1]
	var result sdkResult
	if err := json.Unmarshal([]byte(last), &result); err != nil {
		t.Fatalf("scenario %q: could not parse node client output (run err=%v)\nstdout:\n%s\nstderr:\n%s",
			scenario, runErr, stdout.String(), stderr.String())
	}
	return result
}

// ==========================================================================
// Test environment wiring: real TCP listener in front of the in-process engine
// ==========================================================================

// startRealListener wraps env.engine in a real TCP httptest.Server so the
// Node OpenAI SDK subprocess can hit it over an actual socket (the whole
// point of this suite — see the file banner).
func startRealListener(t *testing.T, env *testEnv) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(env.engine)
	t.Cleanup(srv.Close)
	return srv
}

func chatContractModelInfo(supportedParameters ...string) *config.ModelInfo {
	return &config.ModelInfo{
		Name:          "Contract Test Model",
		Description:   "Model used by the OpenAI SDK contract test suite",
		ContextLength: 8192,
		Architecture: &config.ModelArchitecture{
			Modality:         "text->text",
			InputModalities:  []string{"text"},
			OutputModalities: []string{"text"},
		},
		SupportedParameters: supportedParameters,
	}
}

// ==========================================================================
// Non-streaming / streaming chat completion
// ==========================================================================

func TestOpenAISDK_NonStreamChatCompletion(t *testing.T) {
	mockUpstream := newContractMockUpstream(t)
	t.Cleanup(mockUpstream.Close)

	env := setupTestEnv(t, func(cfg *config.Config) {
		cfg.Service.TargetURL = mockUpstream.URL
		cfg.Service.Type = "chatbot"
		cfg.Service.ModelType = "gpt-4o"
		cfg.Service.TargetSeparated = true
	})
	srv := startRealListener(t, env)
	authHeader := createAuthHeader(t, env.privateKey, env.providerAddr)

	res := runNodeSDKScenario(t, srv.URL+"/v1/proxy", authHeader, "nonstream")
	if !res.OK {
		t.Fatalf("nonstream scenario failed: %s (%s)", res.Error, res.ErrType)
	}
	if got := res.Result["content"]; got != "Hello world" {
		t.Errorf("content = %v, want %q", got, "Hello world")
	}
}

func TestOpenAISDK_StreamChatCompletion(t *testing.T) {
	mockUpstream := newContractMockUpstream(t)
	t.Cleanup(mockUpstream.Close)

	env := setupTestEnv(t, func(cfg *config.Config) {
		cfg.Service.TargetURL = mockUpstream.URL
		cfg.Service.Type = "chatbot"
		cfg.Service.ModelType = "gpt-4o"
		cfg.Service.TargetSeparated = true
	})
	srv := startRealListener(t, env)
	authHeader := createAuthHeader(t, env.privateKey, env.providerAddr)

	res := runNodeSDKScenario(t, srv.URL+"/v1/proxy", authHeader, "stream")
	if !res.OK {
		t.Fatalf("stream scenario failed: %s (%s)", res.Error, res.ErrType)
	}
	if got := res.Result["content"]; got != "Hello world" {
		t.Errorf("streamed content = %v, want %q", got, "Hello world")
	}
	usage, _ := res.Result["usage"].(map[string]interface{})
	if usage == nil {
		t.Fatal("expected a stream_options.include_usage usage chunk parsed by the SDK, got none")
	}
}

// ==========================================================================
// Tool / function calling
// ==========================================================================

func TestOpenAISDK_ToolCalling(t *testing.T) {
	mockUpstream := newContractMockUpstream(t)
	t.Cleanup(mockUpstream.Close)

	env := setupTestEnv(t, func(cfg *config.Config) {
		cfg.Service.TargetURL = mockUpstream.URL
		cfg.Service.Type = "chatbot"
		cfg.Service.ModelType = "gpt-4o"
		cfg.Service.TargetSeparated = true
	})
	srv := startRealListener(t, env)
	authHeader := createAuthHeader(t, env.privateKey, env.providerAddr)

	res := runNodeSDKScenario(t, srv.URL+"/v1/proxy", authHeader, "toolcall")
	if !res.OK {
		t.Fatalf("toolcall scenario failed: %s (%s)", res.Error, res.ErrType)
	}
}

// ==========================================================================
// GET /v1/models via client.models.list()
// ==========================================================================

func TestOpenAISDK_ModelsList(t *testing.T) {
	mockUpstream := newContractMockUpstream(t)
	t.Cleanup(mockUpstream.Close)

	env := setupTestEnv(t, func(cfg *config.Config) {
		cfg.Service.TargetURL = mockUpstream.URL
		cfg.Service.Type = "chatbot"
		cfg.Service.ModelType = "gpt-4o"
		cfg.Service.TargetSeparated = true
		// A native reasoning toggle causes the router to advertise the
		// portable reasoning_effort too (see AdvertisedSupportedParameters) —
		// checked below as a models-endpoint proof that the design doc's
		// documented behavior is what a real client sees.
		cfg.Service.ModelInfo = chatContractModelInfo("chat_template_kwargs", "max_tokens")
	})
	srv := startRealListener(t, env)
	authHeader := createAuthHeader(t, env.privateKey, env.providerAddr)

	res := runNodeSDKScenario(t, srv.URL+"/v1/proxy", authHeader, "models")
	if !res.OK {
		t.Fatalf("models scenario failed: %s (%s)", res.Error, res.ErrType)
	}
	ids, _ := res.Result["ids"].([]interface{})
	found := false
	for _, id := range ids {
		if id == "gpt-4o" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected gpt-4o in client.models.list() output, got %v", ids)
	}
	supported, _ := res.Result["supported_parameters"].([]interface{})
	hasReasoningEffort := false
	for _, p := range supported {
		if p == "reasoning_effort" {
			hasReasoningEffort = true
		}
	}
	if !hasReasoningEffort {
		t.Errorf("expected supported_parameters to advertise reasoning_effort alongside chat_template_kwargs, got %v", supported)
	}
}

// ==========================================================================
// Error mapping: broker-level rejections surface as the correct SDK error type
// ==========================================================================

func TestOpenAISDK_ErrorMapping_Unauthorized(t *testing.T) {
	mockUpstream := newContractMockUpstream(t)
	t.Cleanup(mockUpstream.Close)

	env := setupTestEnv(t, func(cfg *config.Config) {
		cfg.Service.TargetURL = mockUpstream.URL
		cfg.Service.Type = "chatbot"
		cfg.Service.ModelType = "gpt-4o"
		cfg.Service.TargetSeparated = true
	})
	srv := startRealListener(t, env)

	res := runNodeSDKScenario(t, srv.URL+"/v1/proxy", "", "unauthorized")
	if !res.OK {
		t.Fatalf("unauthorized scenario failed unexpectedly: %s (%s)", res.Error, res.ErrType)
	}
	if isAuth, _ := res.Result["isAuthError"].(bool); !isAuth {
		t.Errorf("expected err instanceof OpenAI.AuthenticationError, got errorType=%v status=%v",
			res.Result["errorType"], res.Result["status"])
	}
	if status, _ := res.Result["status"].(float64); status != http.StatusUnauthorized {
		t.Errorf("status = %v, want %d", res.Result["status"], http.StatusUnauthorized)
	}
}

func TestOpenAISDK_ErrorMapping_BadRequest(t *testing.T) {
	mockUpstream := newContractMockUpstream(t)
	t.Cleanup(mockUpstream.Close)

	env := setupTestEnv(t, func(cfg *config.Config) {
		cfg.Service.TargetURL = mockUpstream.URL
		cfg.Service.Type = "chatbot"
		cfg.Service.ModelType = "gpt-4o"
		cfg.Service.TargetSeparated = true
	})
	srv := startRealListener(t, env)
	authHeader := createAuthHeader(t, env.privateKey, env.providerAddr)

	res := runNodeSDKScenario(t, srv.URL+"/v1/proxy", authHeader, "badrequest")
	if !res.OK {
		t.Fatalf("badrequest scenario failed unexpectedly: %s (%s)", res.Error, res.ErrType)
	}
	if isBad, _ := res.Result["isBadRequest"].(bool); !isBad {
		t.Errorf("expected err instanceof OpenAI.BadRequestError, got errorType=%v status=%v",
			res.Result["errorType"], res.Result["status"])
	}
}

func TestOpenAISDK_ErrorMapping_RateLimit(t *testing.T) {
	mockUpstream := newContractMockUpstream(t)
	t.Cleanup(mockUpstream.Close)

	env := setupTestEnv(t, func(cfg *config.Config) {
		cfg.Service.TargetURL = mockUpstream.URL
		cfg.Service.Type = "chatbot"
		cfg.Service.ModelType = "gpt-4o"
		cfg.Service.TargetSeparated = true
		// Burst=1 means the second rapid request always trips the limiter,
		// independent of scheduling jitter between the two SDK calls.
		cfg.ConcurrencyLimit = config.ConcurrencyLimitConfig{PerUserRPM: 1, PerUserBurst: 1}
	})
	srv := startRealListener(t, env)
	authHeader := createAuthHeader(t, env.privateKey, env.providerAddr)

	res := runNodeSDKScenario(t, srv.URL+"/v1/proxy", authHeader, "ratelimit")
	if !res.OK {
		t.Fatalf("ratelimit scenario failed unexpectedly: %s (%s)", res.Error, res.ErrType)
	}
	if isRL, _ := res.Result["isRateLimit"].(bool); !isRL {
		t.Errorf("expected err instanceof OpenAI.RateLimitError, got errorType=%v status=%v",
			res.Result["errorType"], res.Result["status"])
	}
	if status, _ := res.Result["status"].(float64); status != http.StatusTooManyRequests {
		t.Errorf("status = %v, want %d", res.Result["status"], http.StatusTooManyRequests)
	}
}

// TestOpenAISDK_ErrorMapping_InsufficientBalance documents (rather than
// asserts a specific outcome for) what an SDK client actually observes when a
// user's locked balance cannot cover the request. The broker has no dedicated
// HTTP 402 mapping today — insufficientBalance in ctrl/request.go returns a
// plain error, which the generic broker-error handler maps to 400 (see
// common/errors.HTTPError's default) — so this intentionally records the
// real status/type instead of asserting 402, which would currently fail.
// Surfacing this here is itself a finding worth following up on separately.
func TestOpenAISDK_ErrorMapping_InsufficientBalance(t *testing.T) {
	mockUpstream := newContractMockUpstream(t)
	t.Cleanup(mockUpstream.Close)

	env := setupTestEnv(t, func(cfg *config.Config) {
		cfg.Service.TargetURL = mockUpstream.URL
		cfg.Service.Type = "chatbot"
		cfg.Service.ModelType = "gpt-4o"
		cfg.Service.TargetSeparated = true
	})
	srv := startRealListener(t, env)

	// A second user, seeded with zero balance, distinct from the default
	// env.userAddr (which setupTestEnv funds with 1000 0G).
	poorKey, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	poorAddr := crypto.PubkeyToAddress(poorKey.PublicKey)
	zero := "0"
	if err := env.db.CreateUserAccounts([]model.User{{
		User:                 poorAddr.Hex(),
		LockBalance:          &zero,
		LastBalanceCheckTime: model.PtrOf(time.Now().UTC()),
	}}); err != nil && !strings.Contains(err.Error(), "Duplicate") {
		t.Fatalf("create zero-balance user: %v", err)
	}
	env.ctrl.SeedContractAccountCache(poorAddr.Hex(), &contract.Account{
		User:          poorAddr,
		Balance:       big.NewInt(0),
		PendingRefund: big.NewInt(0),
		Generation:    big.NewInt(0),
		RevokedBitmap: big.NewInt(0),
		Acknowledged:  true,
	})

	authHeader := createAuthHeader(t, poorKey, env.providerAddr)
	res := runNodeSDKScenario(t, srv.URL+"/v1/proxy", authHeader, "insufficientbalance")
	if !res.OK {
		t.Fatalf("insufficientbalance scenario failed unexpectedly: %s (%s)", res.Error, res.ErrType)
	}
	t.Logf("insufficient-balance request surfaced to the SDK as errorType=%v status=%v (no dedicated 402 mapping exists today)",
		res.Result["errorType"], res.Result["status"])
}

// ==========================================================================
// Request-body translation, exercised end-to-end via the mock's echo field
// ==========================================================================

func TestOpenAISDK_MaxTokensTranslation(t *testing.T) {
	mockUpstream := newContractMockUpstream(t)
	t.Cleanup(mockUpstream.Close)

	env := setupTestEnv(t, func(cfg *config.Config) {
		cfg.Service.TargetURL = mockUpstream.URL
		cfg.Service.Type = "chatbot"
		cfg.Service.ModelType = "gpt-4o"
		cfg.Service.TargetSeparated = true
		// Advertises max_tokens but not max_completion_tokens: a client's
		// max_completion_tokens must be renamed to max_tokens before it
		// reaches the upstream (see ctrl.TranslateMaxTokens).
		cfg.Service.ModelInfo = chatContractModelInfo("max_tokens")
	})
	srv := startRealListener(t, env)
	authHeader := createAuthHeader(t, env.privateKey, env.providerAddr)

	res := runNodeSDKScenario(t, srv.URL+"/v1/proxy", authHeader, "maxtokens")
	if !res.OK {
		t.Fatalf("maxtokens scenario failed: %s (%s)", res.Error, res.ErrType)
	}
	debug, _ := res.Result["debugReceivedBody"].(map[string]interface{})
	if debug == nil {
		t.Fatal("expected _debug_received_body to be echoed back by the mock upstream")
	}
	if _, present := debug["max_completion_tokens"]; present {
		t.Errorf("upstream received max_completion_tokens=%v; expected it renamed to max_tokens", debug["max_completion_tokens"])
	}
	if got := debug["max_tokens"]; got != float64(123) {
		t.Errorf("upstream received max_tokens=%v, want 123", got)
	}
}

func TestOpenAISDK_ReasoningEffortTranslation(t *testing.T) {
	mockUpstream := newContractMockUpstream(t)
	t.Cleanup(mockUpstream.Close)

	env := setupTestEnv(t, func(cfg *config.Config) {
		cfg.Service.TargetURL = mockUpstream.URL
		cfg.Service.Type = "chatbot"
		cfg.Service.ModelType = "gpt-4o"
		cfg.Service.TargetSeparated = true
		// Qwen3/GLM-on-vLLM dialect: thinking toggle nested under
		// chat_template_kwargs.enable_thinking (see ctrl.applyNativeReasoning).
		cfg.Service.ModelInfo = chatContractModelInfo("chat_template_kwargs")
	})
	srv := startRealListener(t, env)
	authHeader := createAuthHeader(t, env.privateKey, env.providerAddr)

	res := runNodeSDKScenario(t, srv.URL+"/v1/proxy", authHeader, "reasoning")
	if !res.OK {
		t.Fatalf("reasoning scenario failed: %s (%s)", res.Error, res.ErrType)
	}
	debug, _ := res.Result["debugReceivedBody"].(map[string]interface{})
	if debug == nil {
		t.Fatal("expected _debug_received_body to be echoed back by the mock upstream")
	}
	if _, present := debug["reasoning_effort"]; present {
		t.Errorf("upstream received reasoning_effort=%v; expected it consumed and re-expressed natively", debug["reasoning_effort"])
	}
	kwargs, _ := debug["chat_template_kwargs"].(map[string]interface{})
	if kwargs == nil || kwargs["enable_thinking"] != true {
		t.Errorf("upstream received chat_template_kwargs=%v, want {enable_thinking: true}", debug["chat_template_kwargs"])
	}
}

// ==========================================================================
// Response headers the SDK relies on
// ==========================================================================

func TestOpenAISDK_ResponseHeaders_ZGResKey(t *testing.T) {
	// ZG-Res-Key is only set for centralized providers (or non-separated
	// targets) — see chatbot.go — so this mirrors
	// TestCentralizedProvider_NonStream's TLS setup rather than the plain
	// mock used elsewhere in this file.
	mockUpstream := newContractMockUpstreamTLS(t)
	t.Cleanup(mockUpstream.Close)

	env := setupTestEnv(t, func(cfg *config.Config) {
		cfg.Service.TargetURL = mockUpstream.URL
		cfg.Service.Type = "chatbot"
		cfg.Service.ModelType = "gpt-4o"
		cfg.Service.ProviderType = constant.ProviderTypeCentralized
		cfg.Service.ProviderIdentity = constant.CentralizedProviderOpenAI
		cfg.Service.TargetSeparated = true
	})
	env.ctrl.SetHTTPClient(mockUpstream.Client())
	srv := startRealListener(t, env)
	authHeader := createAuthHeader(t, env.privateKey, env.providerAddr)

	res := runNodeSDKScenario(t, srv.URL+"/v1/proxy", authHeader, "headers")
	if !res.OK {
		t.Fatalf("headers scenario failed: %s (%s)", res.Error, res.ErrType)
	}
	if zgResKey, _ := res.Result["zgResKey"].(string); zgResKey == "" {
		t.Error("expected the SDK's raw response (.withResponse()) to expose a non-empty ZG-Res-Key header")
	}
}
