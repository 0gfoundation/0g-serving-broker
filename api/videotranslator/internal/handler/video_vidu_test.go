package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/0glabs/0g-serving-broker/videotranslator/internal/vidu"
)

func TestViduCreateVideo_RejectsMissingLastFrame(t *testing.T) {
	called := false
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	defer mock.Close()

	client := vidu.NewClient(mock.URL, mock.Client())
	h := NewViduVideoHandler(client, newTestLogger(t))

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.POST("/videos", h.CreateVideo)

	body, contentType := newMultipartBody(t, map[string]string{
		"model":           "vidu/viduq3-turbo_start-end2video",
		"prompt":          "a cat",
		"input_reference": "https://example.com/first.png",
		// last_frame_reference deliberately omitted
	})
	req := httptest.NewRequest(http.MethodPost, "/videos", body)
	req.Header.Set("Content-Type", contentType)

	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d (ValidateViduCreateRequest error must surface as 400, not a generic 502 — the request can never succeed by retrying)", rec.Code, http.StatusBadRequest)
	}
	if called {
		t.Error("vidu was called despite a missing last_frame_reference")
	}
}

func TestViduCreateVideo_BothFramesSucceeds(t *testing.T) {
	var gotReq vidu.CreateRequest
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-DashScope-Async") != "enable" {
			t.Errorf("X-DashScope-Async header missing")
		}
		if err := json.NewDecoder(r.Body).Decode(&gotReq); err != nil {
			t.Fatalf("decode vidu create request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"request_id":"r1","output":{"task_id":"t1","task_status":"PENDING"}}`))
	}))
	defer mock.Close()

	client := vidu.NewClient(mock.URL, mock.Client())
	h := NewViduVideoHandler(client, newTestLogger(t))

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.POST("/videos", h.CreateVideo)

	body, contentType := newMultipartBody(t, map[string]string{
		"model":                "vidu/viduq3-turbo_start-end2video",
		"prompt":               "a cat",
		"input_reference":      "https://example.com/first.png",
		"last_frame_reference": "https://example.com/last.png",
	})
	req := httptest.NewRequest(http.MethodPost, "/videos", body)
	req.Header.Set("Content-Type", contentType)

	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if len(gotReq.Input.Media) != 2 {
		t.Fatalf("media length = %d, want 2", len(gotReq.Input.Media))
	}
	if gotReq.Input.Media[0].URL != "https://example.com/first.png" || gotReq.Input.Media[1].URL != "https://example.com/last.png" {
		t.Errorf("media = %+v, want first then last frame", gotReq.Input.Media)
	}

	// The response id must be the ENCODED form (translate.EncodeJobID), not
	// the vendor's raw task_id verbatim — Vidu shares DashScope's task-id
	// contract (see FromViduCreateResponse), and this is the client-visible
	// proof that the create path actually goes through it.
	var resp map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp["id"] != "v0_t1" {
		t.Errorf("id = %v, want v0_t1 (encoded)", resp["id"])
	}
}
