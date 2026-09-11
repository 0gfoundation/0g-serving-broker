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

// TestSeedance20CreateVideo_UsesItsOwnRules is the end-to-end proof that
// NewSeedance20VideoHandler actually wires the 2.0-specific translate
// functions through the real HTTP handler, not just at the unit level:
// duration clamps to 2.0's own 15s ceiling (not 2.5's 30s), 4k is forwarded
// rather than downgraded, and output_format is never sent even though the
// client asked for one.
func TestSeedance20CreateVideo_UsesItsOwnRules(t *testing.T) {
	var gotReq seedance.CreateRequest
	mockSeedance := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&gotReq); err != nil {
			t.Fatalf("decode seedance request: %v", err)
		}
		json.NewEncoder(w).Encode(seedance.CreateResponse{ID: "cgt-20260606160057-6bbjd"})
	}))
	defer mockSeedance.Close()

	client := seedance.NewClient(mockSeedance.URL, mockSeedance.Client())
	h := NewSeedance20VideoHandler(client, newTestLogger(t))

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.POST("/videos", h.CreateVideo)

	const body = `{"prompt":"a cat","seconds":"20","size":"4k","output_format":"webm"}`
	req := httptest.NewRequest(http.MethodPost, "/videos", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if gotReq.Duration != 15 {
		t.Errorf("duration = %d, want clamped to 2.0's own ceiling 15 (not 2.5's 30)", gotReq.Duration)
	}
	if gotReq.Resolution != "4k" {
		t.Errorf("resolution = %q, want 4k forwarded (2.0 serves it)", gotReq.Resolution)
	}
	if gotReq.OutputFormat != nil {
		t.Errorf("output_format = %+v, want nil (2.0's wire shape never sends it, even if the client asked for one)", gotReq.OutputFormat)
	}
}

// TestSeedance20CreateVideo_DurationOver15Clamped is a second, narrower e2e
// case pinning the exact boundary: 16 must clamp to 15 through the real
// handler.
func TestSeedance20CreateVideo_DurationOver15Clamped(t *testing.T) {
	var gotReq seedance.CreateRequest
	mockSeedance := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&gotReq); err != nil {
			t.Fatalf("decode seedance request: %v", err)
		}
		json.NewEncoder(w).Encode(seedance.CreateResponse{ID: "cgt-x"})
	}))
	defer mockSeedance.Close()

	client := seedance.NewClient(mockSeedance.URL, mockSeedance.Client())
	h := NewSeedance20VideoHandler(client, newTestLogger(t))

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.POST("/videos", h.CreateVideo)

	const body = `{"prompt":"p","seconds":"16"}`
	req := httptest.NewRequest(http.MethodPost, "/videos", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if gotReq.Duration != 15 {
		t.Errorf("duration = %d, want 15 (16 clamped down to 2.0's ceiling)", gotReq.Duration)
	}
}

// TestSeedance20CreateVideo_FileIDStillRejected: the version-independent
// validation rules (asset:// / file_id rejection) must still apply through
// the 2.0 constructor -- it is a different handler instance, wired to a
// different validateFn, and this proves that different function still
// enforces the same rule, not that the rule was accidentally dropped.
func TestSeedance20CreateVideo_FileIDStillRejected(t *testing.T) {
	client := seedance.NewClient("http://unused.invalid", http.DefaultClient)
	h := NewSeedance20VideoHandler(client, newTestLogger(t))

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.POST("/videos", h.CreateVideo)

	const body = `{"prompt":"p","input_reference":{"file_id":"file-abc123"}}`
	req := httptest.NewRequest(http.MethodPost, "/videos", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (file_id rejection must still apply for 2.0): %s", rec.Code, rec.Body.String())
	}
}
