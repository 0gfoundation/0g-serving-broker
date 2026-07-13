package handler

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"

	commonconfig "github.com/0glabs/0g-serving-broker/common/config"
	"github.com/0glabs/0g-serving-broker/common/log"
	"github.com/0glabs/0g-serving-broker/videotranslator/internal/dashscope"
)

func newTestLogger(t *testing.T) log.Logger {
	t.Helper()
	logger, err := log.GetLogger(&commonconfig.LoggerConfig{Level: logrus.ErrorLevel.String(), Format: log.TextLogFormat})
	if err != nil {
		t.Fatalf("failed to build logger: %v", err)
	}
	return logger
}

func newMultipartBody(t *testing.T, fields map[string]string) (*bytes.Buffer, string) {
	t.Helper()
	body := &bytes.Buffer{}
	w := multipart.NewWriter(body)
	for k, v := range fields {
		if err := w.WriteField(k, v); err != nil {
			t.Fatalf("write field %s: %v", k, err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	return body, w.FormDataContentType()
}

func TestCreateVideo_TranslatesAndForwardsAuth(t *testing.T) {
	var gotAuth, gotAsyncHeader string
	var gotReq dashscope.CreateRequest

	mockDashScope := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotAsyncHeader = r.Header.Get("X-DashScope-Async")
		if err := json.NewDecoder(r.Body).Decode(&gotReq); err != nil {
			t.Fatalf("decode dashscope request: %v", err)
		}
		json.NewEncoder(w).Encode(dashscope.CreateResponse{
			Output: dashscope.CreateOutput{TaskID: "task-abc", TaskStatus: dashscope.TaskStatusPending},
		})
	}))
	defer mockDashScope.Close()

	client := dashscope.NewClient(mockDashScope.URL, mockDashScope.Client())
	h := NewVideoHandler(client, newTestLogger(t))

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.POST("/videos", h.CreateVideo)

	body, contentType := newMultipartBody(t, map[string]string{
		"model":   "happyhorse",
		"prompt":  "a cat playing piano",
		"seconds": "5",
		"size":    "720p",
	})
	req := httptest.NewRequest(http.MethodPost, "/videos", body)
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Authorization", "Bearer dashscope-secret-key")

	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if gotAuth != "Bearer dashscope-secret-key" {
		t.Errorf("dashscope saw Authorization = %q, want passthrough of broker's AdditionalSecret header", gotAuth)
	}
	if gotAsyncHeader != "enable" {
		t.Errorf("X-DashScope-Async = %q, want enable", gotAsyncHeader)
	}
	if gotReq.Model != "happyhorse" || gotReq.Input.Prompt != "a cat playing piano" || gotReq.Parameters.Duration != 5 {
		t.Errorf("dashscope create request = %+v", gotReq)
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp["id"] != "task-abc" || resp["status"] != "queued" || resp["object"] != "video" {
		t.Errorf("response = %+v, want id=task-abc status=queued object=video", resp)
	}
}

func TestCreateVideo_JSONBody(t *testing.T) {
	t.Run("valid JSON body is translated", func(t *testing.T) {
		var gotReq dashscope.CreateRequest
		mockDashScope := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if err := json.NewDecoder(r.Body).Decode(&gotReq); err != nil {
				t.Fatalf("decode dashscope request: %v", err)
			}
			json.NewEncoder(w).Encode(dashscope.CreateResponse{
				Output: dashscope.CreateOutput{TaskID: "task-abc", TaskStatus: dashscope.TaskStatusPending},
			})
		}))
		defer mockDashScope.Close()

		client := dashscope.NewClient(mockDashScope.URL, mockDashScope.Client())
		h := NewVideoHandler(client, newTestLogger(t))

		gin.SetMode(gin.TestMode)
		engine := gin.New()
		engine.POST("/videos", h.CreateVideo)

		// Deliberately mixes a numeric JSON literal (seconds) with the seed
		// undocumented-param convention (also a numeric literal) to exercise
		// json.Number handling on both fields via the JSON (not multipart)
		// request path.
		bodyJSON := `{"model":"happyhorse","prompt":"a cat","seconds":5,"size":"1280x720","seed":42}`
		req := httptest.NewRequest(http.MethodPost, "/videos", strings.NewReader(bodyJSON))
		req.Header.Set("Content-Type", "application/json")

		rec := httptest.NewRecorder()
		engine.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
		}
		if gotReq.Model != "happyhorse" || gotReq.Input.Prompt != "a cat" {
			t.Errorf("dashscope create request = %+v", gotReq)
		}
		if gotReq.Parameters.Duration != 5 {
			t.Errorf("dashscope create request duration = %d, want 5 (from JSON seconds:5)", gotReq.Parameters.Duration)
		}
		if gotReq.Parameters.Resolution != "720P" || gotReq.Parameters.Ratio != "16:9" {
			t.Errorf("dashscope create request resolution/ratio = %q/%q, want 720P/16:9", gotReq.Parameters.Resolution, gotReq.Parameters.Ratio)
		}
		if gotReq.Parameters.Seed == nil || *gotReq.Parameters.Seed != 42 {
			t.Errorf("dashscope create request seed = %v, want 42", gotReq.Parameters.Seed)
		}
	})

	t.Run("malformed JSON body is a 400, not forwarded to dashscope", func(t *testing.T) {
		called := false
		mockDashScope := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			called = true
		}))
		defer mockDashScope.Close()

		client := dashscope.NewClient(mockDashScope.URL, mockDashScope.Client())
		h := NewVideoHandler(client, newTestLogger(t))

		gin.SetMode(gin.TestMode)
		engine := gin.New()
		engine.POST("/videos", h.CreateVideo)

		req := httptest.NewRequest(http.MethodPost, "/videos", strings.NewReader(`{"model":`))
		req.Header.Set("Content-Type", "application/json")

		rec := httptest.NewRecorder()
		engine.ServeHTTP(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want %d", rec.Code, http.StatusBadRequest)
		}
		if called {
			t.Error("dashscope was called with a malformed request body")
		}
	})
}

func TestCreateVideo_SeedPassthrough(t *testing.T) {
	tests := []struct {
		name     string
		seed     string
		wantSeed *int64
	}{
		{"valid seed forwarded", "42", ptrInt64(42)},
		{"invalid seed omitted, not rejected", "not-a-number", nil},
		{"absent seed omitted", "", nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotReq dashscope.CreateRequest
			mockDashScope := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if err := json.NewDecoder(r.Body).Decode(&gotReq); err != nil {
					t.Fatalf("decode dashscope request: %v", err)
				}
				json.NewEncoder(w).Encode(dashscope.CreateResponse{
					Output: dashscope.CreateOutput{TaskID: "task-abc", TaskStatus: dashscope.TaskStatusPending},
				})
			}))
			defer mockDashScope.Close()

			client := dashscope.NewClient(mockDashScope.URL, mockDashScope.Client())
			h := NewVideoHandler(client, newTestLogger(t))

			gin.SetMode(gin.TestMode)
			engine := gin.New()
			engine.POST("/videos", h.CreateVideo)

			fields := map[string]string{"model": "happyhorse", "prompt": "x"}
			if tt.seed != "" {
				fields["seed"] = tt.seed
			}
			body, contentType := newMultipartBody(t, fields)
			req := httptest.NewRequest(http.MethodPost, "/videos", body)
			req.Header.Set("Content-Type", contentType)

			rec := httptest.NewRecorder()
			engine.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
			}
			if (gotReq.Parameters.Seed == nil) != (tt.wantSeed == nil) {
				t.Fatalf("Seed = %v, want %v", gotReq.Parameters.Seed, tt.wantSeed)
			} else if gotReq.Parameters.Seed != nil && *gotReq.Parameters.Seed != *tt.wantSeed {
				t.Errorf("Seed = %d, want %d", *gotReq.Parameters.Seed, *tt.wantSeed)
			}
		})
	}
}

func ptrInt64(v int64) *int64 { return &v }

func TestGetVideo_TranslatesCompletedStatusAndUsage(t *testing.T) {
	mockDashScope := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(dashscope.GetTaskResponse{
			Output: dashscope.TaskOutput{TaskID: "task-abc", TaskStatus: dashscope.TaskStatusSucceeded, VideoURL: "https://x/y.mp4"},
			Usage:  &dashscope.TaskUsage{OutputVideoDuration: "5"},
		})
	}))
	defer mockDashScope.Close()

	client := dashscope.NewClient(mockDashScope.URL, mockDashScope.Client())
	h := NewVideoHandler(client, newTestLogger(t))

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.GET("/videos/:id", h.GetVideo)

	req := httptest.NewRequest(http.MethodGet, "/videos/task-abc", nil)
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp["id"] != "task-abc" || resp["status"] != "completed" {
		t.Errorf("response = %+v, want id=task-abc status=completed", resp)
	}
	usage, ok := resp["usage"].(map[string]interface{})
	if !ok {
		t.Fatalf("usage missing from response: %+v", resp)
	}
	if usage["output_video_duration"] != float64(5) {
		t.Errorf("usage.output_video_duration = %v, want 5 (renamed from dashscope's usage.video_duration)", usage["output_video_duration"])
	}
}

func TestGetVideo_TranslatesFailedStatus(t *testing.T) {
	mockDashScope := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(dashscope.GetTaskResponse{
			Output: dashscope.TaskOutput{
				TaskID:     "task-abc",
				TaskStatus: dashscope.TaskStatusFailed,
				Code:       "InvalidParameter",
				Message:    "prompt violates content policy",
			},
		})
	}))
	defer mockDashScope.Close()

	client := dashscope.NewClient(mockDashScope.URL, mockDashScope.Client())
	h := NewVideoHandler(client, newTestLogger(t))

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.GET("/videos/:id", h.GetVideo)

	req := httptest.NewRequest(http.MethodGet, "/videos/task-abc", nil)
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)

	var resp map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp["status"] != "failed" {
		t.Errorf("status = %v, want failed", resp["status"])
	}
	errObj, ok := resp["error"].(map[string]interface{})
	if !ok {
		t.Fatalf("error missing from response: %+v", resp)
	}
	if errObj["code"] != "InvalidParameter" {
		t.Errorf("error.code = %v, want InvalidParameter", errObj["code"])
	}
}

func TestGetVideoContent_StreamsBytes(t *testing.T) {
	mockDashScope := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/api/v1/tasks/"):
			videoURL := "http://" + r.Host + "/asset.mp4"
			json.NewEncoder(w).Encode(dashscope.GetTaskResponse{
				Output: dashscope.TaskOutput{TaskID: "task-abc", TaskStatus: dashscope.TaskStatusSucceeded, VideoURL: videoURL},
			})
		case r.URL.Path == "/asset.mp4":
			w.Header().Set("Content-Type", "video/mp4")
			_, _ = w.Write([]byte("fake video bytes"))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer mockDashScope.Close()

	client := dashscope.NewClient(mockDashScope.URL, mockDashScope.Client())
	h := NewVideoHandler(client, newTestLogger(t))

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.GET("/videos/:id/content", h.GetVideoContent)

	req := httptest.NewRequest(http.MethodGet, "/videos/task-abc/content", nil)
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "video/mp4" {
		t.Errorf("Content-Type = %q, want video/mp4", got)
	}
	if rec.Body.String() != "fake video bytes" {
		t.Errorf("body = %q, want %q", rec.Body.String(), "fake video bytes")
	}
}

func TestGetVideoContent_NotReadyReturns404(t *testing.T) {
	mockDashScope := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(dashscope.GetTaskResponse{
			Output: dashscope.TaskOutput{TaskID: "task-abc", TaskStatus: dashscope.TaskStatusRunning},
		})
	}))
	defer mockDashScope.Close()

	client := dashscope.NewClient(mockDashScope.URL, mockDashScope.Client())
	h := NewVideoHandler(client, newTestLogger(t))

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.GET("/videos/:id/content", h.GetVideoContent)

	req := httptest.NewRequest(http.MethodGet, "/videos/task-abc/content", nil)
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d (no video_url yet)", rec.Code, http.StatusNotFound)
	}
}

func TestGetVideoContent_FetchErrorReturnsBadGateway(t *testing.T) {
	mockDashScope := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/api/v1/tasks/"):
			videoURL := "http://" + r.Host + "/asset.mp4"
			json.NewEncoder(w).Encode(dashscope.GetTaskResponse{
				Output: dashscope.TaskOutput{TaskID: "task-abc", TaskStatus: dashscope.TaskStatusSucceeded, VideoURL: videoURL},
			})
		default:
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer mockDashScope.Close()

	client := dashscope.NewClient(mockDashScope.URL, mockDashScope.Client())
	h := NewVideoHandler(client, newTestLogger(t))

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.GET("/videos/:id/content", h.GetVideoContent)

	req := httptest.NewRequest(http.MethodGet, "/videos/task-abc/content", nil)
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusBadGateway)
	}
}

func TestCreateVideo_DashScopeErrorReturnsBadGateway(t *testing.T) {
	mockDashScope := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer mockDashScope.Close()

	client := dashscope.NewClient(mockDashScope.URL, mockDashScope.Client())
	h := NewVideoHandler(client, newTestLogger(t))

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.POST("/videos", h.CreateVideo)

	body, contentType := newMultipartBody(t, map[string]string{"model": "happyhorse", "prompt": "x"})
	req := httptest.NewRequest(http.MethodPost, "/videos", body)
	req.Header.Set("Content-Type", contentType)

	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusBadGateway)
	}
}

func TestCreateVideo_DashScope4xxPropagatesVendorError(t *testing.T) {
	mockDashScope := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{
			"code":    "InvalidParameter",
			"message": "duration must be between 3 and 15 seconds",
		})
	}))
	defer mockDashScope.Close()

	client := dashscope.NewClient(mockDashScope.URL, mockDashScope.Client())
	h := NewVideoHandler(client, newTestLogger(t))

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.POST("/videos", h.CreateVideo)

	body, contentType := newMultipartBody(t, map[string]string{"model": "happyhorse", "prompt": "x", "seconds": "999"})
	req := httptest.NewRequest(http.MethodPost, "/videos", body)
	req.Header.Set("Content-Type", contentType)

	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d (DashScope's own 4xx, not a generic 502)", rec.Code, http.StatusBadRequest)
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	errObj, ok := resp["error"].(map[string]interface{})
	if !ok {
		t.Fatalf("error missing from response: %+v", resp)
	}
	if errObj["code"] != "InvalidParameter" {
		t.Errorf("error.code = %v, want InvalidParameter", errObj["code"])
	}
	if errObj["message"] != "duration must be between 3 and 15 seconds" {
		t.Errorf("error.message = %v, want the vendor's own message", errObj["message"])
	}
}

func TestGetVideo_DashScope4xxPropagatesVendorError(t *testing.T) {
	mockDashScope := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		json.NewEncoder(w).Encode(map[string]string{
			"code":    "Throttling",
			"message": "requests per second exceeds the limit",
		})
	}))
	defer mockDashScope.Close()

	client := dashscope.NewClient(mockDashScope.URL, mockDashScope.Client())
	h := NewVideoHandler(client, newTestLogger(t))

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.GET("/videos/:id", h.GetVideo)

	req := httptest.NewRequest(http.MethodGet, "/videos/task-abc", nil)
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)

	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want %d (DashScope's own 4xx, not a generic 502)", rec.Code, http.StatusTooManyRequests)
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	errObj, ok := resp["error"].(map[string]interface{})
	if !ok {
		t.Fatalf("error missing from response: %+v", resp)
	}
	if errObj["code"] != "Throttling" {
		t.Errorf("error.code = %v, want Throttling", errObj["code"])
	}
}
