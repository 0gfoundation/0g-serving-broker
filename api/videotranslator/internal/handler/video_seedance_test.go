package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/0glabs/0g-serving-broker/videotranslator/internal/seedance"
)

func TestSeedanceCreateVideo_TranslatesAndForwardsAuth(t *testing.T) {
	var gotAuth string
	var gotReq seedance.CreateRequest

	mockSeedance := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		if err := json.NewDecoder(r.Body).Decode(&gotReq); err != nil {
			t.Fatalf("decode seedance request: %v", err)
		}
		json.NewEncoder(w).Encode(seedance.CreateResponse{ID: "cgt-20260606160057-6bbjd"})
	}))
	defer mockSeedance.Close()

	client := seedance.NewClient(mockSeedance.URL, mockSeedance.Client())
	h := NewSeedanceVideoHandler(client, newTestLogger(t))

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.POST("/videos", h.CreateVideo)

	body, contentType := newMultipartBody(t, map[string]string{
		"model":   "dreamina-seedance-2-5-260628",
		"prompt":  "a cat playing piano",
		"seconds": "5",
		// 2.5 only supports 480p/720p (1080p/4k are rejected by the vendor);
		// a "1080p" client request now silently snaps DOWN to 720p — the
		// nearest supported tier — rather than passing through unchanged.
		"size": "1080p",
	})
	req := httptest.NewRequest(http.MethodPost, "/videos", body)
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Authorization", "Bearer seedance-secret-key")

	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if gotAuth != "Bearer seedance-secret-key" {
		t.Errorf("seedance saw Authorization = %q, want passthrough", gotAuth)
	}
	if gotReq.Model != "dreamina-seedance-2-5-260628" || len(gotReq.Content) != 1 || gotReq.Content[0].Text != "a cat playing piano" {
		t.Errorf("seedance create request = %+v", gotReq)
	}
	if gotReq.Resolution != "720p" {
		t.Errorf("resolution = %q, want 720p (2.5 snaps a 1080p-equivalent request down to the nearest supported tier)", gotReq.Resolution)
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

func TestSeedanceCreateVideo_LastFrameReferenceIgnored_NotRejected(t *testing.T) {
	// last_frame_reference is not an OpenAI Video API field (see
	// translate.ToSeedanceCreateRequest's doc), so this integration has no
	// field to parse it into — a client sending it gets a plain first_frame
	// image-to-video request, not a 400. This replaces the old
	// TestSeedanceCreateVideo_ValidationRejectsLastFrameWithoutFirstFrame,
	// which asserted the opposite behavior for a field that no longer exists
	// on this integration's client-facing surface.
	var gotReq seedance.CreateRequest
	mockSeedance := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&gotReq); err != nil {
			t.Fatalf("decode seedance request: %v", err)
		}
		json.NewEncoder(w).Encode(seedance.CreateResponse{ID: "cgt-20260606160057-6bbjd"})
	}))
	defer mockSeedance.Close()

	client := seedance.NewClient(mockSeedance.URL, mockSeedance.Client())
	h := NewSeedanceVideoHandler(client, newTestLogger(t))

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.POST("/videos", h.CreateVideo)

	body, contentType := newMultipartBody(t, map[string]string{
		"prompt":               "p",
		"last_frame_reference": "https://cdn/b.png",
	})
	req := httptest.NewRequest(http.MethodPost, "/videos", body)
	req.Header.Set("Content-Type", contentType)

	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (last_frame_reference is silently ignored, not rejected): %s", rec.Code, rec.Body.String())
	}
	if len(gotReq.Content) != 1 || gotReq.Content[0].Type != "text" {
		t.Errorf("seedance create request should be text-only (last_frame_reference has no field to land in), got %+v", gotReq.Content)
	}
}

// input_reference.file_id IS a real OpenAI Video API field (the JSON path
// only — multipart has no file_id form field), but Seedance has no
// client-usable file-handle namespace to resolve it against. Without this
// rejection, a client using file_id would silently get a still-billed
// text-to-video request instead of the image-to-video they asked for — an
// independent review caught exactly this gap; this test proves the real
// HTTP handler actually rejects it end to end, not just the unit-level
// ValidateSeedanceCreateRequest call.
func TestSeedanceCreateVideo_InputReferenceFileIDRejected(t *testing.T) {
	// The upstream must never be called for a request the pre-flight validator
	// rejects — asserted implicitly by not standing up a mock server at all.
	client := seedance.NewClient("http://unused.invalid", http.DefaultClient)
	h := NewSeedanceVideoHandler(client, newTestLogger(t))

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.POST("/videos", h.CreateVideo)

	const body = `{"prompt":"p","input_reference":{"file_id":"file-abc123"}}`
	req := httptest.NewRequest(http.MethodPost, "/videos", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (file_id has no Seedance mapping, must not silently degrade to a billed text-to-video request): %s", rec.Code, rec.Body.String())
	}
}

func TestSeedanceGetVideo_SucceededBillsOnCompletionTokens(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"t1","status":"succeeded","resolution":"720p","duration":5,"usage":{"completion_tokens":246840,"total_tokens":246840}}`))
	}))
	defer upstream.Close()

	client := seedance.NewClient(upstream.URL, upstream.Client())
	h := NewSeedanceVideoHandler(client, newTestLogger(t))

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
	if !strings.Contains(respBody, `"status":"completed"`) || !strings.Contains(respBody, `"completion_tokens":246840`) {
		t.Fatalf("unexpected body: %s", respBody)
	}
	if strings.Contains(respBody, "output_video_duration") {
		t.Fatalf("Seedance must bill on completion_tokens, not output_video_duration: %s", respBody)
	}
}

func TestSeedanceGetVideoContent_NotReadyReturns404(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"t1","status":"queued"}`))
	}))
	defer upstream.Close()

	client := seedance.NewClient(upstream.URL, upstream.Client())
	h := NewSeedanceVideoHandler(client, newTestLogger(t))

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

func TestSeedanceCreateVideo_4xxPropagatesVendorError(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"error":{"code":"ContentModeration","message":"reference image contains a real human face"}}`))
	}))
	defer upstream.Close()

	client := seedance.NewClient(upstream.URL, upstream.Client())
	h := NewSeedanceVideoHandler(client, newTestLogger(t))

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

func TestSeedanceCreateVideo_5xxReturnsBadGateway(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer upstream.Close()

	client := seedance.NewClient(upstream.URL, upstream.Client())
	h := NewSeedanceVideoHandler(client, newTestLogger(t))

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
