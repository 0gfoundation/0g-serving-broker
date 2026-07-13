//go:build openaicontract

package integration_test

// This file contract-tests the DashScope video translator against the REAL
// `openai` npm SDK (0gfoundation/0g-serving-broker#582), mirroring the
// pattern api/inference/integration_test/openai_sdk_contract_test.go
// established for issue #577: a real TCP listener in front of the
// translator's in-process gin engine, backed by a mocked DashScope-shaped
// upstream, driven by the actual SDK running as a Node subprocess (not Go
// structs) — the only way to catch wire-level surprises a Go-only decoder
// wouldn't (field naming the SDK's own types are strict about, JSON shape).
//
// This uses a SEPARATE npm client (openai_sdk_client/ in this directory)
// from the chat contract test's client: the official SDK only gained a
// `videos` resource in its 6.x line, while the chat client pins the older
// 5.8.2 deliberately. See openai_sdk_client/README.md for the full reasoning
// (including why this can't be a Go SDK contract test: github.com/openai/openai-go
// has no video support as of v1.12.0).
//
// Running locally:
//
//	cd api/videotranslator/integration_test/openai_sdk_client && npm ci
//	cd api && go test -tags openaicontract ./videotranslator/integration_test/... -v

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	commonconfig "github.com/0glabs/0g-serving-broker/common/config"
	"github.com/0glabs/0g-serving-broker/common/log"
	"github.com/0glabs/0g-serving-broker/videotranslator/internal/dashscope"
	"github.com/0glabs/0g-serving-broker/videotranslator/internal/handler"
	"github.com/gin-gonic/gin"
)

// ==========================================================================
// Mocked DashScope upstream
// ==========================================================================

// mockDashScopeCapture records what the translator actually sent upstream,
// so scenarios can assert on auth passthrough and request translation.
type mockDashScopeCapture struct {
	gotAuthHeader string
	gotCreateBody dashscope.CreateRequest
}

// newMockDashScopeCreate returns a mock that answers the create-task call
// with a PENDING task. capture (may be nil) records the Authorization header
// and decoded request body it received.
func newMockDashScopeCreate(t *testing.T, taskID string, capture *mockDashScopeCapture) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if capture != nil {
			capture.gotAuthHeader = r.Header.Get("Authorization")
			_ = json.NewDecoder(r.Body).Decode(&capture.gotCreateBody)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(dashscope.CreateResponse{
			Output: dashscope.CreateOutput{TaskID: taskID, TaskStatus: dashscope.TaskStatusPending},
		})
	}))
}

// newMockDashScopeGetTask returns a mock that answers every get-task call
// with the given fixed response, regardless of task ID.
func newMockDashScopeGetTask(t *testing.T, resp dashscope.GetTaskResponse) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
}

// newMockDashScopeCompletedWithAsset returns a mock that answers a get-task
// call with a SUCCEEDED status whose video_url points back at this same
// server's own /asset.mp4 path (serving assetBytes) — exercising the full
// GetVideoContent flow (get-task to discover the URL, then fetch it) end to
// end through a real TCP round trip.
func newMockDashScopeCompletedWithAsset(t *testing.T, taskID string, assetBytes []byte) *httptest.Server {
	t.Helper()
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/api/v1/tasks/"):
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(dashscope.GetTaskResponse{
				Output: dashscope.TaskOutput{TaskID: taskID, TaskStatus: dashscope.TaskStatusSucceeded, VideoURL: srv.URL + "/asset.mp4"},
			})
		case r.URL.Path == "/asset.mp4":
			w.Header().Set("Content-Type", "video/mp4")
			_, _ = w.Write(assetBytes)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	return srv
}

// ==========================================================================
// Translator engine + real TCP listener
// ==========================================================================

// startTranslator wires the translator's handler against dashscopeBaseURL
// and returns a real TCP listener's base URL for the Node SDK client to hit.
func startTranslator(t *testing.T, dashscopeBaseURL string) string {
	t.Helper()

	logger, err := log.GetLogger(&commonconfig.LoggerConfig{Level: "error", Format: log.TextLogFormat})
	if err != nil {
		t.Fatalf("build logger: %v", err)
	}
	client := dashscope.NewClient(dashscopeBaseURL, nil)
	h := handler.NewVideoHandler(client, logger)

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.POST("/videos", h.CreateVideo)
	engine.GET("/videos/:id", h.GetVideo)
	engine.GET("/videos/:id/content", h.GetVideoContent)

	srv := httptest.NewServer(engine)
	t.Cleanup(srv.Close)
	return srv.URL
}

// ==========================================================================
// Node SDK client invocation
// ==========================================================================

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

type sdkResult struct {
	OK       bool                   `json:"ok"`
	Scenario string                 `json:"scenario"`
	Result   map[string]interface{} `json:"result"`
	Error    string                 `json:"error"`
	ErrType  string                 `json:"errorType"`
}

const nodeScenarioTimeout = 30 * time.Second

func runNodeSDKScenario(t *testing.T, baseURL, authHeader, scenario, videoID string) sdkResult {
	t.Helper()
	dir := nodeClientDir(t)

	ctx, cancel := context.WithTimeout(context.Background(), nodeScenarioTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "node", "run.js")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"BASE_URL="+baseURL,
		"AUTH_HEADER="+authHeader,
		"SCENARIO="+scenario,
		"VIDEO_ID="+videoID,
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
// POST /videos via client.videos.create()
// ==========================================================================

func TestOpenAISDK_CreateVideo(t *testing.T) {
	capture := &mockDashScopeCapture{}
	mockDashScope := newMockDashScopeCreate(t, "task-abc123", capture)
	t.Cleanup(mockDashScope.Close)

	baseURL := startTranslator(t, mockDashScope.URL)

	res := runNodeSDKScenario(t, baseURL, "Bearer dashscope-secret-key", "create", "")
	if !res.OK {
		t.Fatalf("create scenario failed: %s (%s)", res.Error, res.ErrType)
	}
	if res.Result["id"] != "task-abc123" {
		t.Errorf("id = %v, want task-abc123", res.Result["id"])
	}
	if res.Result["status"] != "queued" {
		t.Errorf("status = %v, want queued (DashScope PENDING mapped)", res.Result["status"])
	}
	if res.Result["object"] != "video" {
		t.Errorf("object = %v, want video (the real SDK's Video.object literal)", res.Result["object"])
	}

	if capture.gotAuthHeader != "Bearer dashscope-secret-key" {
		t.Errorf("dashscope saw Authorization = %q, want the broker's AdditionalSecret header passed through unmodified", capture.gotAuthHeader)
	}
	if capture.gotCreateBody.Model != "happyhorse" || capture.gotCreateBody.Input.Prompt != "a cat playing piano on stage" {
		t.Errorf("dashscope create request = %+v", capture.gotCreateBody)
	}
	if capture.gotCreateBody.Parameters.Duration != 4 {
		t.Errorf("dashscope create request duration = %d, want 4 (from the SDK's seconds:\"4\")", capture.gotCreateBody.Parameters.Duration)
	}
	// run.js's create scenario requests size "1280x720" — HappyHorse has no
	// pixel-dimension concept, so this must arrive as its own coarse
	// resolution tier ("720P") plus a separately-derived aspect ratio
	// ("16:9"), not the raw "1280x720" string DashScope wouldn't recognize.
	if capture.gotCreateBody.Parameters.Resolution != "720P" {
		t.Errorf("dashscope create request resolution = %q, want 720P (derived from the SDK's size:\"1280x720\")", capture.gotCreateBody.Parameters.Resolution)
	}
	if capture.gotCreateBody.Parameters.Ratio != "16:9" {
		t.Errorf("dashscope create request ratio = %q, want 16:9 (derived from the SDK's size:\"1280x720\")", capture.gotCreateBody.Parameters.Ratio)
	}
	if capture.gotCreateBody.Parameters.Watermark != false {
		t.Errorf("dashscope create request watermark = %v, want false (always disabled, not something the SDK's typed params can even express)", capture.gotCreateBody.Parameters.Watermark)
	}
}

// ==========================================================================
// GET /videos/{id} via client.videos.retrieve()
// ==========================================================================

func TestOpenAISDK_RetrieveVideo_Completed(t *testing.T) {
	mockDashScope := newMockDashScopeGetTask(t, dashscope.GetTaskResponse{
		Output: dashscope.TaskOutput{TaskID: "task-abc123", TaskStatus: dashscope.TaskStatusSucceeded, VideoURL: "https://x/y.mp4"},
		Usage:  &dashscope.TaskUsage{OutputVideoDuration: "5"},
	})
	t.Cleanup(mockDashScope.Close)

	baseURL := startTranslator(t, mockDashScope.URL)

	res := runNodeSDKScenario(t, baseURL, "", "retrieve", "task-abc123")
	if !res.OK {
		t.Fatalf("retrieve scenario failed: %s (%s)", res.Error, res.ErrType)
	}
	if res.Result["status"] != "completed" {
		t.Errorf("status = %v, want completed (DashScope SUCCEEDED mapped)", res.Result["status"])
	}
	usage, ok := res.Result["usage"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected a usage object on the SDK's parsed response, got %v", res.Result["usage"])
	}
	if usage["output_video_duration"] != float64(5) {
		t.Errorf("usage.output_video_duration = %v, want 5 (renamed from dashscope's usage.video_duration — the field the broker's resolveVideoBilling recognizes)",
			usage["output_video_duration"])
	}
}

func TestOpenAISDK_RetrieveVideo_Failed(t *testing.T) {
	mockDashScope := newMockDashScopeGetTask(t, dashscope.GetTaskResponse{
		Output: dashscope.TaskOutput{
			TaskID:     "task-abc123",
			TaskStatus: dashscope.TaskStatusFailed,
			Code:       "InvalidParameter",
			Message:    "prompt violates content policy",
		},
	})
	t.Cleanup(mockDashScope.Close)

	baseURL := startTranslator(t, mockDashScope.URL)

	res := runNodeSDKScenario(t, baseURL, "", "retrieve", "task-abc123")
	if !res.OK {
		t.Fatalf("retrieve scenario failed: %s (%s)", res.Error, res.ErrType)
	}
	if res.Result["status"] != "failed" {
		t.Errorf("status = %v, want failed (DashScope FAILED mapped)", res.Result["status"])
	}
	errObj, ok := res.Result["error"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected the SDK's typed VideoCreateError on a failed video, got %v", res.Result["error"])
	}
	if errObj["code"] != "InvalidParameter" {
		t.Errorf("error.code = %v, want InvalidParameter", errObj["code"])
	}
	if errObj["message"] != "prompt violates content policy" {
		t.Errorf("error.message = %v, want %q", errObj["message"], "prompt violates content policy")
	}
}

// ==========================================================================
// GET /videos/{id}/content via client.videos.downloadContent()
// ==========================================================================

// TestOpenAISDK_DownloadContent exercises the fix for the gap the self-review
// found: earlier, this translator had no /content route at all, so a
// completed (and billed) video's bytes were unreachable through any code
// path a real client actually uses. This drives the exact call
// (videos.downloadContent()) the real SDK offers for that purpose, end to
// end through a real TCP round trip and a two-hop mock (get-task, then the
// asset URL it reports).
func TestOpenAISDK_DownloadContent(t *testing.T) {
	assetBytes := []byte("fake video bytes")
	mockDashScope := newMockDashScopeCompletedWithAsset(t, "task-abc123", assetBytes)
	t.Cleanup(mockDashScope.Close)

	baseURL := startTranslator(t, mockDashScope.URL)

	res := runNodeSDKScenario(t, baseURL, "", "downloadContent", "task-abc123")
	if !res.OK {
		t.Fatalf("downloadContent scenario failed: %s (%s)", res.Error, res.ErrType)
	}
	if got := res.Result["text"]; got != string(assetBytes) {
		t.Errorf("downloaded content = %q, want %q", got, string(assetBytes))
	}
	if got, _ := res.Result["byteLength"].(float64); int(got) != len(assetBytes) {
		t.Errorf("byteLength = %v, want %d", res.Result["byteLength"], len(assetBytes))
	}
}
