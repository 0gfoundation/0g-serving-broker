package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/0glabs/0g-serving-broker/inference/model"
)

// --- Mock asyncCtrl ---

type mockAsyncCtrl struct {
	asyncEnabled  bool
	whitelistUser bool

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

func (m *mockAsyncCtrl) ValidateSession(ctx *gin.Context) (string, error) {
	return m.sessionUser, m.sessionErr
}

func (m *mockAsyncCtrl) WhitelistMetricModel(reqBody []byte, contentType string) string {
	return "mock-model"
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
