package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/0glabs/0g-serving-broker/videotranslator/internal/kling"
)

func TestKlingCreateVideo_TranslatesAndForwardsAuth(t *testing.T) {
	var gotAuth, gotAsyncHeader string
	var gotReq kling.CreateRequest

	mockKling := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotAsyncHeader = r.Header.Get("X-DashScope-Async")
		if err := json.NewDecoder(r.Body).Decode(&gotReq); err != nil {
			t.Fatalf("decode kling request: %v", err)
		}
		json.NewEncoder(w).Encode(kling.CreateResponse{Output: kling.CreateOutput{TaskID: "abc123", TaskStatus: kling.TaskStatusPending}})
	}))
	defer mockKling.Close()

	client := kling.NewClient(mockKling.URL, mockKling.Client())
	h := NewKlingVideoHandler(client, newTestLogger(t))

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.POST("/videos", h.CreateVideo)

	body, contentType := newMultipartBody(t, map[string]string{
		"model":   "kling/kling-v3-video-generation",
		"prompt":  "a cat playing piano",
		"seconds": "5",
		"size":    "1920x1080",
	})
	req := httptest.NewRequest(http.MethodPost, "/videos", body)
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Authorization", "Bearer kling-secret-key")

	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if gotAuth != "Bearer kling-secret-key" {
		t.Errorf("kling saw Authorization = %q, want passthrough", gotAuth)
	}
	if gotAsyncHeader != "enable" {
		t.Errorf("kling saw X-DashScope-Async = %q, want enable (kling has no sync mode)", gotAsyncHeader)
	}
	if gotReq.Model != "kling/kling-v3-video-generation" || gotReq.Input.Prompt != "a cat playing piano" {
		t.Errorf("kling create request = %+v", gotReq)
	}
	if gotReq.Parameters.Mode != "pro" {
		t.Errorf("mode = %q, want pro (1920x1080 snaps to pro)", gotReq.Parameters.Mode)
	}
	if gotReq.Parameters.Watermark == nil || *gotReq.Parameters.Watermark != false {
		t.Errorf("watermark = %+v, want forced false", gotReq.Parameters.Watermark)
	}
	if gotReq.Parameters.Audio == nil || *gotReq.Parameters.Audio != false {
		t.Errorf("audio = %+v, want forced false", gotReq.Parameters.Audio)
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp["status"] != "queued" || resp["object"] != "video" {
		t.Errorf("response = %+v, want status=queued object=video", resp)
	}
	if id, _ := resp["id"].(string); !strings.HasPrefix(id, "v0_") {
		t.Errorf("id = %v, want v0_ tagged", resp["id"])
	}
}

func TestKlingCreateVideo_FirstFrameImageURL_TranslatesCorrectly(t *testing.T) {
	var gotReq kling.CreateRequest
	mockKling := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&gotReq); err != nil {
			t.Fatalf("decode kling request: %v", err)
		}
		json.NewEncoder(w).Encode(kling.CreateResponse{Output: kling.CreateOutput{TaskID: "abc123", TaskStatus: kling.TaskStatusPending}})
	}))
	defer mockKling.Close()

	client := kling.NewClient(mockKling.URL, mockKling.Client())
	h := NewKlingVideoHandler(client, newTestLogger(t))

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.POST("/videos", h.CreateVideo)

	const body = `{"prompt":"animate this","input_reference":{"image_url":"https://cdn.example.com/a.png"}}`
	req := httptest.NewRequest(http.MethodPost, "/videos", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if len(gotReq.Input.Media) != 1 {
		t.Fatalf("want [first_frame], got %+v", gotReq.Input.Media)
	}
	if gotReq.Input.Media[0].Type != "first_frame" || gotReq.Input.Media[0].URL != "https://cdn.example.com/a.png" {
		t.Errorf("media[0] wrong: %+v", gotReq.Input.Media[0])
	}
}

func TestKlingCreateVideo_InputReferenceFileIDRejected(t *testing.T) {
	// The upstream must never be called for a request the pre-flight validator
	// rejects — asserted implicitly by not standing up a mock server at all.
	client := kling.NewClient("http://unused.invalid", http.DefaultClient)
	h := NewKlingVideoHandler(client, newTestLogger(t))

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.POST("/videos", h.CreateVideo)

	const body = `{"prompt":"p","input_reference":{"file_id":"file-abc123"}}`
	req := httptest.NewRequest(http.MethodPost, "/videos", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (file_id has no Kling mapping, must not silently degrade to a billed text-to-video request): %s", rec.Code, rec.Body.String())
	}
}

func TestKlingGetVideo_SucceededBillsOnUsageDuration(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"output":{"task_id":"t1","task_status":"SUCCEEDED","video_url":"https://cdn/x.mp4"},"usage":{"duration":5,"size":"1280*720"}}`))
	}))
	defer upstream.Close()

	client := kling.NewClient(upstream.URL, upstream.Client())
	h := NewKlingVideoHandler(client, newTestLogger(t))

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.GET("/videos/:id", h.GetVideo)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/videos/v0_t1", nil)
	engine.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	respBody := rec.Body.String()
	if !strings.Contains(respBody, `"status":"completed"`) || !strings.Contains(respBody, `"output_video_duration":5`) {
		t.Fatalf("unexpected body: %s", respBody)
	}
}

func TestKlingGetVideoContent_NotReadyReturns404(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"output":{"task_id":"t1","task_status":"PENDING"}}`))
	}))
	defer upstream.Close()

	client := kling.NewClient(upstream.URL, upstream.Client())
	h := NewKlingVideoHandler(client, newTestLogger(t))

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.GET("/videos/:id/content", h.GetVideoContent)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/videos/v0_t1/content", nil)
	engine.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("code = %d, want 404: %s", rec.Code, rec.Body.String())
	}
}

func TestKlingCreateVideo_4xxPropagatesVendorError(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"code":"ContentModeration","message":"reference image contains a real human face"}`))
	}))
	defer upstream.Close()

	client := kling.NewClient(upstream.URL, upstream.Client())
	h := NewKlingVideoHandler(client, newTestLogger(t))

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.POST("/videos", h.CreateVideo)

	body, contentType := newMultipartBody(t, map[string]string{"prompt": "p", "seconds": "5"})
	req := httptest.NewRequest(http.MethodPost, "/videos", body)
	req.Header.Set("Content-Type", contentType)

	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("code = %d, want 403 (vendor 4xx passthrough): %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "real human face") {
		t.Fatalf("body = %s, want the vendor's message surfaced", rec.Body.String())
	}
}

func TestKlingCreateVideo_5xxReturnsBadGateway(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer upstream.Close()

	client := kling.NewClient(upstream.URL, upstream.Client())
	h := NewKlingVideoHandler(client, newTestLogger(t))

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.POST("/videos", h.CreateVideo)

	body, contentType := newMultipartBody(t, map[string]string{"prompt": "p", "seconds": "5"})
	req := httptest.NewRequest(http.MethodPost, "/videos", body)
	req.Header.Set("Content-Type", contentType)

	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("code = %d, want 502: %s", rec.Code, rec.Body.String())
	}
}
