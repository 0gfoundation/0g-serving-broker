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

// TestSeedanceCreateVideo_RequestModelOverridesMisconfiguredDefault is the
// end-to-end proof for resolveFns: a handler built as 2.5 (as it would be if
// SEEDANCE_MODEL_VERSION were left unset on a deployment whose broker-side
// billing.vendor is actually "seedance-2.0" — the exact drift scenario
// nothing else in this codebase detects) still serves 2.0's own rules the
// moment the REQUEST's own "model" field names 2.0's wire id. Duration
// clamps to 15 (2.0's ceiling), not 30 (2.5's) — proving the request, not
// this process's static default, decided which rules applied.
func TestSeedanceCreateVideo_RequestModelOverridesMisconfiguredDefault(t *testing.T) {
	var gotReq seedance.CreateRequest
	mockSeedance := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&gotReq); err != nil {
			t.Fatalf("decode seedance request: %v", err)
		}
		json.NewEncoder(w).Encode(seedance.CreateResponse{ID: "cgt-mismatch"})
	}))
	defer mockSeedance.Close()

	client := seedance.NewClient(mockSeedance.URL, mockSeedance.Client())
	// Built as 2.5 -- e.g. SEEDANCE_MODEL_VERSION left unset by mistake on a
	// deployment that is actually supposed to run 2.0.
	h := NewSeedanceVideoHandler(client, newTestLogger(t))

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.POST("/videos", h.CreateVideo)

	const body = `{"model":"dreamina-seedance-2-0-260128","prompt":"a cat","seconds":"20"}`
	req := httptest.NewRequest(http.MethodPost, "/videos", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if gotReq.Duration != 15 {
		t.Errorf("duration = %d, want clamped to 2.0's ceiling 15 -- the request's own model field should have overridden this handler's 2.5 default", gotReq.Duration)
	}
	if gotReq.Model != "dreamina-seedance-2-0-260128" {
		t.Errorf("model = %q, want the 2.0 wire id echoed through unchanged", gotReq.Model)
	}
}

// TestSeedanceCreateVideo_UnrecognizedModelFallsBackToDefault confirms the
// other half of resolveFns' contract: a request whose "model" field doesn't
// identify either version by itself (here, the router catalog's canonical
// id with no on-chain rewrite applied -- a shape this sidecar has never
// been asked to recognize) falls back to the handler's own configured
// default rather than erroring or guessing. Duration clamps to 30 (2.5's
// ceiling), proving the 2.5-built handler's default still applied.
func TestSeedanceCreateVideo_UnrecognizedModelFallsBackToDefault(t *testing.T) {
	var gotReq seedance.CreateRequest
	mockSeedance := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&gotReq); err != nil {
			t.Fatalf("decode seedance request: %v", err)
		}
		json.NewEncoder(w).Encode(seedance.CreateResponse{ID: "cgt-fallback"})
	}))
	defer mockSeedance.Close()

	client := seedance.NewClient(mockSeedance.URL, mockSeedance.Client())
	h := NewSeedanceVideoHandler(client, newTestLogger(t))

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.POST("/videos", h.CreateVideo)

	const body = `{"model":"some-unrelated-model-id","prompt":"a cat","seconds":"40"}`
	req := httptest.NewRequest(http.MethodPost, "/videos", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if gotReq.Duration != 30 {
		t.Errorf("duration = %d, want clamped to 2.5's ceiling 30 (unrecognized model must fall back to this handler's own default)", gotReq.Duration)
	}
}
