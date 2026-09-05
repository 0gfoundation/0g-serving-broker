package handler

import (
	"bytes"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/0glabs/0g-serving-broker/inference/internal/ctrl"
	"github.com/0glabs/0g-serving-broker/inference/model"
)

// --- Mock asyncCtrl ---

type mockAsyncCtrl struct {
	// strictMultipart mirrors cfg.e2eeStrictMultipart. The structural refusals
	// are governed by it, so a handler test that does not set it is asserting the
	// DEFAULT behaviour — forward and count — which is the point of having it
	// here rather than a hardcoded true.
	strictMultipart bool
	asyncEnabled    bool
	whitelistUser   bool

	// ValidateSession
	sessionUser string
	sessionErr  error

	// SubmitAsyncJob
	submitJobID string
	submitErr   error
	// Captures for verification
	capturedSvcType    string
	capturedReqHeaders []byte
	capturedReqBody    []byte
	capturedWhitelist  bool

	// GetAsyncJob
	getJobResult model.AsyncJob
	getJobErr    error
}

func (m *mockAsyncCtrl) IsAsyncEnabled() bool {
	return m.asyncEnabled
}

// IsSealedRequest DELEGATES to the real implementation rather than mirroring it.
// The previous version reimplemented "a JSON object with a top-level _e2ee key"
// here, with the stated intent that a test posting a real envelope exercise the
// gate rather than the mock's opinion of it — but a reimplementation IS the
// mock's opinion, and it stops tracking the real rule the moment that rule
// grows. It just did: the gate now also has to see an envelope smuggled into a
// multipart part, which these async routes accept, and a mirrored JSON-only
// mock would have kept this test green while the route leaked.
//
// It calls the receiver-free ctrl.IsSealedRequest rather than constructing a
// zero Ctrl. The zero value worked — the predicate reads no enclave state — but
// it encoded that assumption HERE, so the day the predicate starts reading a
// field this fails as a nil-map panic inside a handler test instead of as a
// compile error where the change was made.
func (m *mockAsyncCtrl) RefuseAsync(contentType string, reqBody []byte) (ctrl.SealedVerdict, string) {
	return ctrl.RefuseAsync(m.strictMultipart, contentType, reqBody)
}

func (m *mockAsyncCtrl) ValidateSession(ctx *gin.Context) (string, error) {
	return m.sessionUser, m.sessionErr
}

func (m *mockAsyncCtrl) WhitelistMetricLabels(ctx *gin.Context, reqBody []byte, contentType string) (string, string) {
	return "mock-model", "mock-upstream"
}

func (m *mockAsyncCtrl) IsWhitelistedUser(userAddress string) bool {
	return m.whitelistUser
}

func (m *mockAsyncCtrl) SubmitAsyncJob(ctx *gin.Context, userAddress, svcType string, reqHeaders, reqBody []byte, isWhitelisted bool) (string, error) {
	m.capturedSvcType = svcType
	m.capturedReqHeaders = reqHeaders
	m.capturedReqBody = reqBody
	m.capturedWhitelist = isWhitelisted
	return m.submitJobID, m.submitErr
}

func (m *mockAsyncCtrl) GetAsyncJob(jobID string) (model.AsyncJob, error) {
	return m.getJobResult, m.getJobErr
}

// --- Test helpers ---

func newTestHandler(mock *mockAsyncCtrl) *Handler {
	return &Handler{
		asyncCtrl: mock,
	}
}

func performRequest(handler gin.HandlerFunc, method, path string, body string, headers map[string]string) *httptest.ResponseRecorder {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	ctx, engine := gin.CreateTestContext(w)

	var bodyReader *strings.Reader
	if body != "" {
		bodyReader = strings.NewReader(body)
	} else {
		bodyReader = strings.NewReader("")
	}
	ctx.Request = httptest.NewRequest(method, path, bodyReader)
	for k, v := range headers {
		ctx.Request.Header.Set(k, v)
	}

	engine.Handle(method, path, handler)
	engine.ServeHTTP(w, ctx.Request)
	return w
}

func parseJSON(t *testing.T, body []byte) map[string]interface{} {
	t.Helper()
	var result map[string]interface{}
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatalf("failed to parse JSON response: %v\nbody: %s", err, string(body))
	}
	return result
}

// ==========================================================================
// SubmitAsyncImageGeneration (POST /v1/async/images/generations)
// ==========================================================================

func TestSubmitAsyncImageGeneration_Success(t *testing.T) {
	mock := &mockAsyncCtrl{
		asyncEnabled: true,
		sessionUser:  "0xUser1",
		submitJobID:  "job-123",
	}
	h := newTestHandler(mock)

	w := performRequest(h.SubmitAsyncImageGeneration, "POST", "/v1/async/images/generations",
		`{"prompt":"a cat","n":1}`,
		map[string]string{"Content-Type": "application/json"},
	)

	if w.Code != http.StatusAccepted {
		t.Errorf("expected 202, got %d: %s", w.Code, w.Body.String())
	}

	resp := parseJSON(t, w.Body.Bytes())
	if resp["jobId"] != "job-123" {
		t.Errorf("expected jobId=job-123, got %v", resp["jobId"])
	}
	if resp["status"] != "pending" {
		t.Errorf("expected status=pending, got %v", resp["status"])
	}

	// Verify the handler passed the correct service type
	if mock.capturedSvcType != "text-to-image" {
		t.Errorf("expected svcType=text-to-image, got %s", mock.capturedSvcType)
	}
}

func TestSubmitAsyncImageEdit_Success(t *testing.T) {
	mock := &mockAsyncCtrl{
		asyncEnabled: true,
		sessionUser:  "0xUser1",
		submitJobID:  "job-456",
	}
	h := newTestHandler(mock)

	w := performRequest(h.SubmitAsyncImageEdit, "POST", "/v1/async/images/edits",
		`{"prompt":"make blue","n":1}`,
		map[string]string{"Content-Type": "application/json"},
	)

	if w.Code != http.StatusAccepted {
		t.Errorf("expected 202, got %d: %s", w.Code, w.Body.String())
	}

	if mock.capturedSvcType != "image-editing" {
		t.Errorf("expected svcType=image-editing, got %s", mock.capturedSvcType)
	}
}

func TestSubmitAsync_AsyncDisabled(t *testing.T) {
	mock := &mockAsyncCtrl{
		asyncEnabled: false,
	}
	h := newTestHandler(mock)

	w := performRequest(h.SubmitAsyncImageGeneration, "POST", "/v1/async/images/generations",
		`{"prompt":"test"}`,
		nil,
	)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", w.Code)
	}
	resp := parseJSON(t, w.Body.Bytes())
	if !strings.Contains(resp["error"].(string), "not enabled") {
		t.Errorf("expected 'not enabled' error, got: %v", resp["error"])
	}
}

func TestSubmitAsync_SessionValidationFails(t *testing.T) {
	mock := &mockAsyncCtrl{
		asyncEnabled: true,
		sessionErr:   fmt.Errorf("invalid authorization header"),
	}
	h := newTestHandler(mock)

	w := performRequest(h.SubmitAsyncImageGeneration, "POST", "/v1/async/images/generations",
		`{"prompt":"test"}`,
		nil,
	)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
	resp := parseJSON(t, w.Body.Bytes())
	errMsg, _ := resp["error"].(string)
	if !strings.Contains(errMsg, "validate session") {
		t.Errorf("expected error to contain 'validate session', got: %s", errMsg)
	}
}

func TestSubmitAsync_EmptyBody(t *testing.T) {
	mock := &mockAsyncCtrl{
		asyncEnabled: true,
		sessionUser:  "0xUser1",
	}
	h := newTestHandler(mock)

	w := performRequest(h.SubmitAsyncImageGeneration, "POST", "/v1/async/images/generations",
		"", // empty body
		nil,
	)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
	resp := parseJSON(t, w.Body.Bytes())
	if !strings.Contains(resp["error"].(string), "request body is required") {
		t.Errorf("expected 'request body is required' error, got: %v", resp["error"])
	}
}

func TestSubmitAsync_SubmitJobFails(t *testing.T) {
	mock := &mockAsyncCtrl{
		asyncEnabled: true,
		sessionUser:  "0xUser1",
		submitErr:    fmt.Errorf("queue is full"),
	}
	h := newTestHandler(mock)

	w := performRequest(h.SubmitAsyncImageGeneration, "POST", "/v1/async/images/generations",
		`{"prompt":"test"}`,
		nil,
	)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
	resp := parseJSON(t, w.Body.Bytes())
	errMsg, _ := resp["error"].(string)
	if !strings.Contains(errMsg, "submit async job") {
		t.Errorf("expected error to contain 'submit async job', got: %s", errMsg)
	}
}

func TestSubmitAsync_WhitelistedUser(t *testing.T) {
	mock := &mockAsyncCtrl{
		asyncEnabled:  true,
		sessionUser:   "0xWhitelist",
		whitelistUser: true,
		submitJobID:   "job-wl",
	}
	h := newTestHandler(mock)

	w := performRequest(h.SubmitAsyncImageGeneration, "POST", "/v1/async/images/generations",
		`{"prompt":"test"}`,
		nil,
	)

	if w.Code != http.StatusAccepted {
		t.Errorf("expected 202, got %d: %s", w.Code, w.Body.String())
	}
	if !mock.capturedWhitelist {
		t.Error("expected isWhitelisted=true to be passed to SubmitAsyncJob")
	}
}

func TestSubmitAsync_NonWhitelistedUser(t *testing.T) {
	mock := &mockAsyncCtrl{
		asyncEnabled:  true,
		sessionUser:   "0xNormal",
		whitelistUser: false,
		submitJobID:   "job-normal",
	}
	h := newTestHandler(mock)

	w := performRequest(h.SubmitAsyncImageGeneration, "POST", "/v1/async/images/generations",
		`{"prompt":"test"}`,
		nil,
	)

	if w.Code != http.StatusAccepted {
		t.Errorf("expected 202, got %d: %s", w.Code, w.Body.String())
	}
	if mock.capturedWhitelist {
		t.Error("expected isWhitelisted=false to be passed to SubmitAsyncJob")
	}
}

func TestSubmitAsync_ContentTypeHeaderCaptured(t *testing.T) {
	mock := &mockAsyncCtrl{
		asyncEnabled: true,
		sessionUser:  "0xUser1",
		submitJobID:  "job-ct",
	}
	h := newTestHandler(mock)

	w := performRequest(h.SubmitAsyncImageGeneration, "POST", "/v1/async/images/generations",
		`{"prompt":"test"}`,
		map[string]string{"Content-Type": "multipart/form-data; boundary=abc123"},
	)

	if w.Code != http.StatusAccepted {
		t.Errorf("expected 202, got %d: %s", w.Code, w.Body.String())
	}

	// Verify Content-Type was captured in the headers passed to SubmitAsyncJob
	var headers map[string][]string
	if err := json.Unmarshal(mock.capturedReqHeaders, &headers); err != nil {
		t.Fatalf("failed to parse captured headers: %v", err)
	}
	if headers["Content-Type"] == nil || headers["Content-Type"][0] != "multipart/form-data; boundary=abc123" {
		t.Errorf("expected Content-Type to be captured, got: %v", headers)
	}
}

func TestSubmitAsync_NoContentTypeHeader(t *testing.T) {
	mock := &mockAsyncCtrl{
		asyncEnabled: true,
		sessionUser:  "0xUser1",
		submitJobID:  "job-noct",
	}
	h := newTestHandler(mock)

	// Don't set Content-Type header
	w := performRequest(h.SubmitAsyncImageGeneration, "POST", "/v1/async/images/generations",
		`{"prompt":"test"}`,
		nil,
	)

	if w.Code != http.StatusAccepted {
		t.Errorf("expected 202, got %d: %s", w.Code, w.Body.String())
	}

	// Verify empty headers map (no Content-Type)
	var headers map[string][]string
	if err := json.Unmarshal(mock.capturedReqHeaders, &headers); err != nil {
		t.Fatalf("failed to parse captured headers: %v", err)
	}
	if len(headers) != 0 {
		t.Errorf("expected empty headers map, got: %v", headers)
	}
}

func TestSubmitAsync_RequestBodyPassedThrough(t *testing.T) {
	mock := &mockAsyncCtrl{
		asyncEnabled: true,
		sessionUser:  "0xUser1",
		submitJobID:  "job-body",
	}
	h := newTestHandler(mock)

	body := `{"prompt":"detailed prompt","n":3,"size":"1024x1024"}`
	w := performRequest(h.SubmitAsyncImageGeneration, "POST", "/v1/async/images/generations",
		body,
		map[string]string{"Content-Type": "application/json"},
	)

	if w.Code != http.StatusAccepted {
		t.Errorf("expected 202, got %d: %s", w.Code, w.Body.String())
	}
	if string(mock.capturedReqBody) != body {
		t.Errorf("expected body to be passed through, got: %s", string(mock.capturedReqBody))
	}
}

// ==========================================================================
// GetAsyncJob (GET /v1/async/jobs/:jobID)
// ==========================================================================

func getAsyncJobRequest(h *Handler, jobID string) *httptest.ResponseRecorder {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	_, engine := gin.CreateTestContext(w)
	engine.GET("/v1/async/jobs/:jobID", h.GetAsyncJob)
	req := httptest.NewRequest("GET", "/v1/async/jobs/"+jobID, nil)
	engine.ServeHTTP(w, req)
	return w
}

func TestGetAsyncJob_Pending(t *testing.T) {
	now := time.Now()
	mock := &mockAsyncCtrl{
		asyncEnabled: true,
		sessionUser:  "0xUser1",
		getJobResult: model.AsyncJob{
			Model:       model.Model{CreatedAt: &now, UpdatedAt: &now},
			JobID:       "job-pending",
			Status:      model.AsyncJobStatusPending,
			UserAddress: "0xUser1",
		},
	}
	h := newTestHandler(mock)
	w := getAsyncJobRequest(h, "job-pending")

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	resp := parseJSON(t, w.Body.Bytes())
	if resp["jobId"] != "job-pending" {
		t.Errorf("expected jobId=job-pending, got %v", resp["jobId"])
	}
	if resp["status"] != "pending" {
		t.Errorf("expected status=pending, got %v", resp["status"])
	}

	// Should have Retry-After header
	if w.Header().Get("Retry-After") != "5" {
		t.Errorf("expected Retry-After=5 for pending job, got %q", w.Header().Get("Retry-After"))
	}

	// Should not have data field
	if resp["data"] != nil {
		t.Errorf("expected no data for pending job, got %v", resp["data"])
	}
}

func TestGetAsyncJob_Processing(t *testing.T) {
	now := time.Now()
	mock := &mockAsyncCtrl{
		asyncEnabled: true,
		sessionUser:  "0xUser1",
		getJobResult: model.AsyncJob{
			Model:       model.Model{CreatedAt: &now, UpdatedAt: &now},
			JobID:       "job-proc",
			Status:      model.AsyncJobStatusProcessing,
			UserAddress: "0xUser1",
		},
	}
	h := newTestHandler(mock)
	w := getAsyncJobRequest(h, "job-proc")

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	resp := parseJSON(t, w.Body.Bytes())
	if resp["status"] != "processing" {
		t.Errorf("expected status=processing, got %v", resp["status"])
	}
	if w.Header().Get("Retry-After") != "5" {
		t.Errorf("expected Retry-After=5 for processing job, got %q", w.Header().Get("Retry-After"))
	}
}

func TestGetAsyncJob_Completed_WithValidJSON(t *testing.T) {
	now := time.Now()
	providerResp := `{"created":1234567890,"data":[{"b64_json":"iVBOR=="}]}`
	mock := &mockAsyncCtrl{
		asyncEnabled: true,
		sessionUser:  "0xUser1",
		getJobResult: model.AsyncJob{
			Model:        model.Model{CreatedAt: &now, UpdatedAt: &now},
			JobID:        "job-done",
			Status:       model.AsyncJobStatusCompleted,
			UserAddress:  "0xUser1",
			ResponseBody: []byte(providerResp),
		},
	}
	h := newTestHandler(mock)
	w := getAsyncJobRequest(h, "job-done")

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	resp := parseJSON(t, w.Body.Bytes())
	if resp["status"] != "completed" {
		t.Errorf("expected status=completed, got %v", resp["status"])
	}

	// Should NOT have Retry-After header for completed jobs
	if w.Header().Get("Retry-After") != "" {
		t.Errorf("expected no Retry-After for completed job, got %q", w.Header().Get("Retry-After"))
	}

	// Data field should contain the provider's response as embedded JSON
	if resp["data"] == nil {
		t.Fatal("expected data field for completed job")
	}
	// Verify data is the parsed provider response (not a string wrapper)
	dataMap, ok := resp["data"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected data to be a JSON object, got %T", resp["data"])
	}
	if dataMap["created"] != float64(1234567890) {
		t.Errorf("expected created=1234567890 in data, got %v", dataMap["created"])
	}
}

func TestGetAsyncJob_Completed_WithInvalidJSON(t *testing.T) {
	now := time.Now()
	mock := &mockAsyncCtrl{
		asyncEnabled: true,
		sessionUser:  "0xUser1",
		getJobResult: model.AsyncJob{
			Model:        model.Model{CreatedAt: &now, UpdatedAt: &now},
			JobID:        "job-binary",
			Status:       model.AsyncJobStatusCompleted,
			UserAddress:  "0xUser1",
			ResponseBody: []byte("not valid json data"),
		},
	}
	h := newTestHandler(mock)
	w := getAsyncJobRequest(h, "job-binary")

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	resp := parseJSON(t, w.Body.Bytes())
	// Data should be a JSON string wrapping the binary content
	if resp["data"] == nil {
		t.Fatal("expected data field for completed job with non-JSON response")
	}
	dataStr, ok := resp["data"].(string)
	if !ok {
		t.Fatalf("expected data to be a string for non-JSON response, got %T", resp["data"])
	}
	if dataStr != "not valid json data" {
		t.Errorf("expected data to contain original response, got: %s", dataStr)
	}
}

func TestGetAsyncJob_Completed_EmptyResponseBody(t *testing.T) {
	now := time.Now()
	mock := &mockAsyncCtrl{
		asyncEnabled: true,
		sessionUser:  "0xUser1",
		getJobResult: model.AsyncJob{
			Model:        model.Model{CreatedAt: &now, UpdatedAt: &now},
			JobID:        "job-empty",
			Status:       model.AsyncJobStatusCompleted,
			UserAddress:  "0xUser1",
			ResponseBody: nil,
		},
	}
	h := newTestHandler(mock)
	w := getAsyncJobRequest(h, "job-empty")

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	resp := parseJSON(t, w.Body.Bytes())
	if resp["data"] != nil {
		t.Errorf("expected no data for completed job with empty response, got %v", resp["data"])
	}
}

func TestGetAsyncJob_Failed(t *testing.T) {
	now := time.Now()
	mock := &mockAsyncCtrl{
		asyncEnabled: true,
		sessionUser:  "0xUser1",
		getJobResult: model.AsyncJob{
			Model:        model.Model{CreatedAt: &now, UpdatedAt: &now},
			JobID:        "job-fail",
			Status:       model.AsyncJobStatusFailed,
			UserAddress:  "0xUser1",
			ErrorMessage: "provider returned 500",
		},
	}
	h := newTestHandler(mock)
	w := getAsyncJobRequest(h, "job-fail")

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	resp := parseJSON(t, w.Body.Bytes())
	if resp["status"] != "failed" {
		t.Errorf("expected status=failed, got %v", resp["status"])
	}
	if resp["errorMessage"] != "provider returned 500" {
		t.Errorf("expected errorMessage, got %v", resp["errorMessage"])
	}
	// No Retry-After for failed jobs
	if w.Header().Get("Retry-After") != "" {
		t.Errorf("expected no Retry-After for failed job")
	}
}

func TestGetAsyncJob_AsyncDisabled(t *testing.T) {
	mock := &mockAsyncCtrl{
		asyncEnabled: false,
	}
	h := newTestHandler(mock)
	w := getAsyncJobRequest(h, "some-id")

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", w.Code)
	}
}

func TestGetAsyncJob_SessionFails(t *testing.T) {
	mock := &mockAsyncCtrl{
		asyncEnabled: true,
		sessionErr:   fmt.Errorf("bad token"),
	}
	h := newTestHandler(mock)
	w := getAsyncJobRequest(h, "some-id")

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestGetAsyncJob_NotFound(t *testing.T) {
	mock := &mockAsyncCtrl{
		asyncEnabled: true,
		sessionUser:  "0xUser1",
		getJobErr:    fmt.Errorf("not found"),
	}
	h := newTestHandler(mock)
	w := getAsyncJobRequest(h, "nonexistent")

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", w.Code)
	}
	resp := parseJSON(t, w.Body.Bytes())
	if !strings.Contains(resp["error"].(string), "not found") {
		t.Errorf("expected 'not found' error, got: %v", resp["error"])
	}
}

func TestGetAsyncJob_ForbiddenDifferentUser(t *testing.T) {
	now := time.Now()
	mock := &mockAsyncCtrl{
		asyncEnabled: true,
		sessionUser:  "0xAttacker",
		getJobResult: model.AsyncJob{
			Model:       model.Model{CreatedAt: &now, UpdatedAt: &now},
			JobID:       "job-owned",
			Status:      model.AsyncJobStatusCompleted,
			UserAddress: "0xOwner",
		},
	}
	h := newTestHandler(mock)
	w := getAsyncJobRequest(h, "job-owned")

	if w.Code != http.StatusForbidden {
		t.Errorf("expected 403, got %d: %s", w.Code, w.Body.String())
	}
	resp := parseJSON(t, w.Body.Bytes())
	if !strings.Contains(resp["error"].(string), "permission") {
		t.Errorf("expected 'permission' error, got: %v", resp["error"])
	}
}

func TestGetAsyncJob_CaseInsensitiveOwnerMatch(t *testing.T) {
	now := time.Now()
	mock := &mockAsyncCtrl{
		asyncEnabled: true,
		sessionUser:  "0xABCDEF1234567890ABCDEF1234567890ABCDEF12",
		getJobResult: model.AsyncJob{
			Model:       model.Model{CreatedAt: &now, UpdatedAt: &now},
			JobID:       "job-case",
			Status:      model.AsyncJobStatusPending,
			UserAddress: "0xabcdef1234567890abcdef1234567890abcdef12",
		},
	}
	h := newTestHandler(mock)
	w := getAsyncJobRequest(h, "job-case")

	// Should succeed — case-insensitive comparison
	if w.Code != http.StatusOK {
		t.Errorf("expected 200 for case-insensitive match, got %d: %s", w.Code, w.Body.String())
	}
}

func TestGetAsyncJob_EmptyJobID(t *testing.T) {
	mock := &mockAsyncCtrl{
		asyncEnabled: true,
		sessionUser:  "0xUser1",
	}
	h := newTestHandler(mock)

	// Register without :jobID param — ctx.Param("jobID") will return ""
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	_, engine := gin.CreateTestContext(w)
	engine.GET("/v1/async/jobs/", h.GetAsyncJob)
	req := httptest.NewRequest("GET", "/v1/async/jobs/", nil)
	engine.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for empty jobID, got %d: %s", w.Code, w.Body.String())
	}
}

// The async submit routes never reach MaybeUnsealRequest — they are separate gin
// handlers, not proxy traffic — so before this gate a sealed envelope was
// enqueued verbatim, had its cleartext rewritten by forceB64ResponseFormat
// (invalidating the AAD), was forwarded upstream still sealed, had its result
// served in plaintext, and was billed. The prompt stayed sealed, so little was
// disclosed; what broke is that "a sealed request is fail-closed" became a
// property of which route the client picked rather than of the enclave.
func TestSubmitAsync_RejectsSealedRequest(t *testing.T) {
	sealed := `{"_e2ee":{"v":1,"kem_id":"0x0020","key_id":"k","signer_addr":"0xabc","client_eph_pub":"p","enc":"e","sealed_fields":["prompt"],"ciphertext":"c"},"model":"z-image","response_format":"b64_json"}`

	for _, tc := range []struct {
		name string
		path string
	}{
		{"images/generations", "/v1/async/images/generations"},
		{"images/edits", "/v1/async/images/edits"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mock := &mockAsyncCtrl{asyncEnabled: true, sessionUser: "0xUser1", submitJobID: "job-1"}
			h := newTestHandler(mock)
			fn := h.SubmitAsyncImageGeneration
			if tc.path == "/v1/async/images/edits" {
				fn = h.SubmitAsyncImageEdit
			}

			w := performRequest(fn, "POST", tc.path, sealed,
				map[string]string{"Content-Type": "application/json"})

			if w.Code != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
			}
			if !strings.Contains(w.Body.String(), "e2ee") {
				t.Errorf("error should name e2ee, got: %s", w.Body.String())
			}
			// The job must never have been enqueued — that is the whole point.
			if mock.capturedReqBody != nil {
				t.Errorf("a sealed request must not be submitted, got body: %s", mock.capturedReqBody)
			}
		})
	}
}

// The same gate, on the request shape /v1/async/images/edits actually accepts.
// It takes multipart/form-data (it stores the boundary Content-Type for the
// upstream), and every JSON-shaped check reads a multipart body as "not sealed"
// — so before the gate learned about part names, an envelope smuggled into a
// part was enqueued exactly as the JSON one above used to be.
//
// This is at the ROUTE rather than only on the predicate because the route is
// what leaks: a mutation removing the multipart half of IsSealedRequest turned
// the ctrl tests red and left this package entirely green, which is precisely
// the coverage shape that lets a hole ship.
func TestSubmitAsync_RejectsSealedEnvelopeSmuggledIntoMultipart(t *testing.T) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	if err := w.WriteField("model", "qwen-image-edit"); err != nil {
		t.Fatalf("WriteField: %v", err)
	}
	if err := w.WriteField("_e2ee", `{"v":1,"kem_id":"0x0020","ciphertext":"c"}`); err != nil {
		t.Fatalf("WriteField: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	mock := &mockAsyncCtrl{asyncEnabled: true, sessionUser: "0xUser1", submitJobID: "job-1"}
	h := newTestHandler(mock)
	rec := performRequest(h.SubmitAsyncImageEdit, "POST", "/v1/async/images/edits", buf.String(),
		map[string]string{"Content-Type": w.FormDataContentType()})

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
	if mock.capturedReqBody != nil {
		t.Errorf("a smuggled envelope must not be submitted, got body: %s", mock.capturedReqBody)
	}
	// And the message says WHICH case it is. A genuinely named part is the one
	// case for which "use the synchronous endpoint" is the right advice, so the
	// reason has to reach the response rather than be discarded for a single
	// fixed sentence.
	if !strings.Contains(rec.Body.String(), `named \"_e2ee\"`) {
		t.Errorf("the refusal must name the reason it found, got: %s", rec.Body.String())
	}
}

// The same route, refusing for a DIFFERENT reason: a malformed multipart body
// that could be naming the marker. This is not a sealed request, so the old
// fixed message — "use the synchronous endpoint" — sent the caller to a route
// that refuses it too, with different wording. The reason must distinguish them.
func TestSubmitAsync_MalformedMultipartRefusalSaysWhy(t *testing.T) {
	// A body that mentions the marker, with a Content-Type carrying no boundary,
	// so the parts cannot be enumerated at all.
	body := "--x\r\nContent-Disposition: form-data; name=\"_e2ee\"\r\n\r\n{}\r\n--x--\r\n"

	// STRUCTURAL: the parts were never read, so this refusal is the one
	// cfg.e2eeStrictMultipart governs. TestSubmitAsync_StructuralRefusalIsGated
	// covers the default, where the same body is forwarded and counted.
	mock := &mockAsyncCtrl{asyncEnabled: true, sessionUser: "0xUser1", submitJobID: "job-1", strictMultipart: true}
	h := newTestHandler(mock)
	rec := performRequest(h.SubmitAsyncImageEdit, "POST", "/v1/async/images/edits", body,
		map[string]string{"Content-Type": "multipart/form-data"})

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
	if mock.capturedReqBody != nil {
		t.Errorf("must not be submitted, got body: %s", mock.capturedReqBody)
	}
	got := rec.Body.String()
	if !strings.Contains(got, "boundary is missing") {
		t.Errorf("the refusal must say the boundary was missing, got: %s", got)
	}
	// The two causes that share this branch need distinct wording: formatting a
	// nil error rendered a parseable-but-boundaryless Content-Type as "(<nil>)".
	if strings.Contains(got, "<nil>") {
		t.Errorf("a nil parse error must not be rendered, got: %s", got)
	}
}

// And its complement, so the route is not simply refusing all multipart: an
// ordinary edit whose `prompt` mentions the marker must still be accepted.
func TestSubmitAsync_MultipartMentioningTheMarkerIsAccepted(t *testing.T) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	if err := w.WriteField("model", "qwen-image-edit"); err != nil {
		t.Fatalf("WriteField: %v", err)
	}
	if err := w.WriteField("prompt", "annotate the diagram labelled _e2ee"); err != nil {
		t.Fatalf("WriteField: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	mock := &mockAsyncCtrl{asyncEnabled: true, sessionUser: "0xUser1", submitJobID: "job-1"}
	h := newTestHandler(mock)
	rec := performRequest(h.SubmitAsyncImageEdit, "POST", "/v1/async/images/edits", buf.String(),
		map[string]string{"Content-Type": w.FormDataContentType()})

	if rec.Code == http.StatusBadRequest && strings.Contains(rec.Body.String(), "e2ee") {
		t.Fatalf("an ordinary edit must not be refused over its prompt text: %s", rec.Body.String())
	}
}

// The gate keys on a genuine top-level "_e2ee", not on the substring: a prompt
// that merely mentions it is an ordinary request and must still be accepted.
func TestSubmitAsync_E2EESubstringInPromptIsNotSealed(t *testing.T) {
	mock := &mockAsyncCtrl{asyncEnabled: true, sessionUser: "0xUser1", submitJobID: "job-2"}
	h := newTestHandler(mock)

	w := performRequest(h.SubmitAsyncImageGeneration, "POST", "/v1/async/images/generations",
		`{"prompt":"a diagram explaining the _e2ee envelope","n":1}`,
		map[string]string{"Content-Type": "application/json"})

	if w.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", w.Code, w.Body.String())
	}
	if mock.capturedReqBody == nil {
		t.Error("an ordinary request must still be enqueued")
	}
}

// The default: a structural refusal forwards and is counted, so the operator can
// see whether the false-positive set is empty before the flag is flipped. These
// branches run before authentication on the sync path and have never seen
// production traffic, which is why they do not ship refusing.
func TestSubmitAsync_StructuralRefusalIsGated(t *testing.T) {
	body := "--x\r\nContent-Disposition: form-data; name=\"_e2ee\"\r\n\r\n{}\r\n--x--\r\n"

	mock := &mockAsyncCtrl{asyncEnabled: true, sessionUser: "0xUser1", submitJobID: "job-1"}
	if mock.strictMultipart {
		t.Fatal("this test is about the DEFAULT, which must be off")
	}
	h := newTestHandler(mock)
	rec := performRequest(h.SubmitAsyncImageEdit, "POST", "/v1/async/images/edits", body,
		map[string]string{"Content-Type": "multipart/form-data"})

	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202 with the flag off, got %d: %s", rec.Code, rec.Body.String())
	}
	if mock.capturedReqBody == nil {
		t.Error("the body must be submitted unchanged, not dropped")
	}
}

// An EXACT refusal is not governed by the flag: a part whose name resolves to
// the marker is a fact about the request, and no operator setting should forward
// it. Same body shape as above but enumerable, so the name is actually read.
func TestSubmitAsync_ExactRefusalIgnoresTheFlag(t *testing.T) {
	body := "--x\r\nContent-Disposition: form-data; name=\"_e2ee\"\r\n\r\n{}\r\n--x--\r\n"

	for _, strict := range []bool{false, true} {
		mock := &mockAsyncCtrl{asyncEnabled: true, sessionUser: "0xUser1", submitJobID: "job-1", strictMultipart: strict}
		h := newTestHandler(mock)
		rec := performRequest(h.SubmitAsyncImageEdit, "POST", "/v1/async/images/edits", body,
			map[string]string{"Content-Type": "multipart/form-data; boundary=x"})

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("strict=%v: a part named the marker must be refused either way, got %d: %s", strict, rec.Code, rec.Body.String())
		}
		if got := rec.Body.String(); !strings.Contains(got, "declares a part named") {
			t.Errorf("strict=%v: the message should name the part, got: %s", strict, got)
		}
	}
}

// One bool could not tell the three refusals apart, so a single sentence had to
// serve all of them — and told a client with a merely-malformed body to retry on
// an endpoint that would refuse it too. Each verdict gets its own message now.
func TestSubmitAsync_MessagesDifferPerVerdict(t *testing.T) {
	cases := []struct {
		name        string
		contentType string
		body        string
		wants       string
		notWants    string
	}{
		{
			name:        "a real sealed envelope",
			contentType: "application/json",
			body:        `{"_e2ee":{"v":1}}`,
			wants:       "Send it to the synchronous endpoint",
		},
		{
			name:        "a part named the marker",
			contentType: "multipart/form-data; boundary=x",
			body:        "--x\r\nContent-Disposition: form-data; name=\"_e2ee\"\r\n\r\n{}\r\n--x--\r\n",
			wants:       "declares a part named",
		},
		{
			name:        "merely malformed",
			contentType: "multipart/form-data",
			body:        "--x\r\nContent-Disposition: form-data; name=\"_e2ee\"\r\n\r\n{}\r\n--x--\r\n",
			wants:       "Correct the body and retry",
			// The old single message sent this caller to an endpoint that would
			// also refuse them.
			notWants: "goes to the synchronous endpoint",
		},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			mock := &mockAsyncCtrl{asyncEnabled: true, sessionUser: "0xUser1", submitJobID: "job-1", strictMultipart: true}
			h := newTestHandler(mock)
			rec := performRequest(h.SubmitAsyncImageEdit, "POST", "/v1/async/images/edits", tt.body,
				map[string]string{"Content-Type": tt.contentType})
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
			}
			got := rec.Body.String()
			if !strings.Contains(got, tt.wants) {
				t.Errorf("message should contain %q, got: %s", tt.wants, got)
			}
			if tt.notWants != "" && strings.Contains(got, tt.notWants) {
				t.Errorf("message should NOT contain %q, got: %s", tt.notWants, got)
			}
		})
	}
}
