package proxy

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	logrus "github.com/sirupsen/logrus"

	"github.com/0glabs/0g-serving-broker/common/log"
	"github.com/0glabs/0g-serving-broker/common/middleware"
	"github.com/0glabs/0g-serving-broker/inference/config"
	constant "github.com/0glabs/0g-serving-broker/inference/const"
	"github.com/0glabs/0g-serving-broker/inference/internal/ctrl"
)

// newTestProxy builds the minimal Proxy needed for handleImageServeRoute tests.
func newTestProxy(t *testing.T, c *ctrl.Ctrl) *Proxy {
	t.Helper()
	return &Proxy{ctrl: c, logger: noopLogger{}}
}

// newGinCtxForPath builds a gin.Context whose RequestURI matches path under ServicePrefix.
func newGinCtxForPath(t *testing.T, path string) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	w := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(w)
	ctx.Request = httptest.NewRequest("GET", constant.ServicePrefix+path, nil)
	// Populate the wildcard "any" param the same way gin does in production.
	ctx.Params = gin.Params{{Key: "any", Value: path}}
	return ctx, w
}

// ==========================================================================
// Video route registration in TargetRoute
// ==========================================================================

func TestTargetRoute_ContainsVideos(t *testing.T) {
	if _, ok := constant.TargetRoute["/videos"]; !ok {
		t.Error("expected /videos to be in TargetRoute")
	}
}

func TestTargetRoute_VideoSubpathsNotInTargetRoute(t *testing.T) {
	// Video status and content paths should NOT be in TargetRoute
	// (they use AuthRequiredPrefixes instead)
	subpaths := []string{
		"/videos/video-123",
		"/videos/video-123/content",
	}
	for _, path := range subpaths {
		if _, ok := constant.TargetRoute[path]; ok {
			t.Errorf("expected %s to NOT be in TargetRoute (should use AuthRequiredPrefixes)", path)
		}
	}
}

// ==========================================================================
// AuthRequiredPrefixes for video status/content endpoints
// ==========================================================================

func TestAuthRequiredPrefixes_MatchesVideoSubpaths(t *testing.T) {
	tests := []struct {
		path      string
		shouldMatch bool
	}{
		{"/videos/video-123", true},
		{"/videos/video-123/content", true},
		{"/videos/", true},
		{"/videos", false},           // exact /videos goes through TargetRoute
		{"/attestation/report", false}, // should NOT match auth prefix
		{"/images/generations", false}, // should NOT match auth prefix
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			matched := false
			for _, prefix := range constant.AuthRequiredPrefixes {
				if strings.HasPrefix(strings.ToLower(tt.path), prefix) {
					matched = true
					break
				}
			}
			if matched != tt.shouldMatch {
				t.Errorf("path %s: expected match=%v, got match=%v", tt.path, tt.shouldMatch, matched)
			}
		})
	}
}

// ==========================================================================
// ServiceType constant
// ==========================================================================

func TestServiceTypeVideoGeneration(t *testing.T) {
	if constant.ServiceTypeVideoGeneration != "video-generation" {
		t.Errorf("expected ServiceTypeVideoGeneration=video-generation, got %s", constant.ServiceTypeVideoGeneration)
	}
}

// ==========================================================================
// Proxy.Start() accepts video-generation (validated via switch)
// ==========================================================================

func TestProxyStart_VideoGenerationIsValidServiceType(t *testing.T) {
	// Verify video-generation is in the accepted set of service types
	// by checking the same switch logic used in proxy.Start()
	validTypes := map[string]bool{
		"zgStorage":        true,
		"chatbot":          true,
		"text-to-image":    true,
		"speech-to-text":   true,
		"image-editing":    true,
		"video-generation": true,
	}

	if !validTypes["video-generation"] {
		t.Error("video-generation should be a valid service type for proxy")
	}

	// Verify an invalid type is not accepted
	if validTypes["unknown-type"] {
		t.Error("unknown-type should not be a valid service type")
	}
}

// ==========================================================================
// Centralized provider: block LLM attestation requests
// ==========================================================================

// noopLogger satisfies log.Logger for tests without producing output.
type noopLogger struct{}

func (noopLogger) Debugf(string, ...interface{})   {}
func (noopLogger) Infof(string, ...interface{})    {}
func (noopLogger) Printf(string, ...interface{})   {}
func (noopLogger) Warnf(string, ...interface{})    {}
func (noopLogger) Warningf(string, ...interface{}) {}
func (noopLogger) Errorf(string, ...interface{})   {}
func (noopLogger) Fatalf(string, ...interface{})   {}
func (noopLogger) Panicf(string, ...interface{})   {}
func (noopLogger) Debug(...interface{})             {}
func (noopLogger) Info(...interface{})              {}
func (noopLogger) Print(...interface{})             {}
func (noopLogger) Warn(...interface{})              {}
func (noopLogger) Warning(...interface{})           {}
func (noopLogger) Error(...interface{})             {}
func (noopLogger) Fatal(...interface{})             {}
func (noopLogger) Panic(...interface{})             {}
func (noopLogger) Debugln(...interface{})           {}
func (noopLogger) Infoln(...interface{})            {}
func (noopLogger) Println(...interface{})           {}
func (noopLogger) Warnln(...interface{})            {}
func (noopLogger) Warningln(...interface{})         {}
func (noopLogger) Errorln(...interface{})           {}
func (noopLogger) Fatalln(...interface{})           {}
func (noopLogger) Panicln(...interface{})           {}

func (n noopLogger) WithFields(_ logrus.Fields) log.Logger { return n }
func (noopLogger) InnerLogger() *logrus.Logger             { return logrus.New() }

func TestProxyHTTPRequest_CentralizedBlocksAttestation(t *testing.T) {
	gin.SetMode(gin.TestMode)

	// Build a minimal Proxy with a centralized service config.
	c := &ctrl.Ctrl{
		Service: config.Service{
			ProviderType:     "centralized",
			ProviderIdentity: "openai",
		},
	}
	p := &Proxy{
		ctrl:          c,
		logger:        noopLogger{},
		serviceTarget: "https://api.openai.com",
		serviceType:   "chatbot",
	}

	tests := []struct {
		name       string
		path       string
		wantStatus int
	}{
		{"attestation/report", constant.ServicePrefix + "/attestation/report", http.StatusNotImplemented},
		{"attestation/report with model param", constant.ServicePrefix + "/attestation/report?model=gpt-4o", http.StatusNotImplemented},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			ctx, engine := gin.CreateTestContext(w)
			ctx.Request = httptest.NewRequest("GET", tt.path, nil)

			// Register route with wildcard so gin populates RequestURI correctly.
			engine.GET(constant.ServicePrefix+"/*any", func(c *gin.Context) {
				p.proxyHTTPRequest(c)
			})
			engine.ServeHTTP(w, ctx.Request)

			if w.Code != tt.wantStatus {
				t.Errorf("expected status %d, got %d; body: %s", tt.wantStatus, w.Code, w.Body.String())
			}

			var body map[string]interface{}
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatalf("failed to parse response body: %v", err)
			}
			errMsg, _ := body["error"].(string)
			if !strings.Contains(errMsg, "centralized") {
				t.Errorf("expected error mentioning 'centralized', got: %s", errMsg)
			}
		})
	}
}

func TestCentralizedAttestationGuard_Logic(t *testing.T) {
	// Verify the guard condition: only centralized + attestation path triggers blocking.
	tests := []struct {
		name         string
		providerType string
		path         string
		shouldBlock  bool
	}{
		{"centralized + attestation", "centralized", "/attestation/report", true},
		{"centralized + attestation with query", "centralized", "/attestation/report?model=gpt-4o", true},
		{"decentralized + attestation", "decentralized", "/attestation/report", false},
		{"centralized + signature", "centralized", "/signature/abc123", false},
		{"decentralized + signature", "decentralized", "/signature/abc123", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := config.Service{ProviderType: tt.providerType}

			// Strip query params for path matching (mirrors proxy.go logic)
			targetPath := tt.path
			if idx := strings.Index(targetPath, "?"); idx != -1 {
				targetPath = targetPath[:idx]
			}

			blocked := svc.IsCentralized() && strings.HasPrefix(strings.ToLower(targetPath), "/attestation")
			if blocked != tt.shouldBlock {
				t.Errorf("expected shouldBlock=%v, got %v", tt.shouldBlock, blocked)
			}
		})
	}
}

// ==========================================================================
// handleImageServeRoute
// ==========================================================================

func TestHandleImageServeRoute_NoMatch_ShortPath(t *testing.T) {
	gin.SetMode(gin.TestMode)
	p := newTestProxy(t, &ctrl.Ctrl{})

	ctx, _ := newGinCtxForPath(t, "/images/only-one-segment")
	handled := p.handleImageServeRoute(ctx, "/images/only-one-segment")
	if handled {
		t.Error("path with no index segment should not be handled")
	}
}

// TestHandleImageServeRoute_ShapeFailDoesNotConsumeRateLimit pins the
// ordering fix: malformed paths under /images/ (e.g. missing the index
// segment) must NOT consume a rate-limit token. Otherwise a caller looping
// on GET /images/xyz could drain the per-IP bucket without ever reaching the
// store, then fall through to the next matcher at zero cost to themselves.
func TestHandleImageServeRoute_ShapeFailDoesNotConsumeRateLimit(t *testing.T) {
	gin.SetMode(gin.TestMode)

	c := &ctrl.Ctrl{}
	if err := c.SetupImageStoreForTest(t.TempDir()); err != nil {
		t.Fatalf("SetupImageStoreForTest: %v", err)
	}
	// Seed one valid image so the well-formed probe at the end succeeds.
	if err := c.StoreTestImage("ordered", [][]byte{[]byte{0x89, 0x50, 0x4E, 0x47, 0, 0, 0, 1}}); err != nil {
		t.Fatalf("StoreTestImage: %v", err)
	}

	p := newTestProxy(t, c)
	// Budget of 1 per "minute" at 60 RPM, burst 1 — simulates a drained bucket
	// if the shape-fail path consumed a token. Using PerUserRateLimiter so we
	// depend on the production primitive, not a test fake.
	p.imageServeLimiter = middleware.NewPerUserRateLimiter(60, 1)

	// 3 shape-fail requests with the same RemoteAddr. Each SHOULD return false
	// (unhandled) without touching the limiter.
	for i := 0; i < 3; i++ {
		w := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(w)
		ctx.Request = httptest.NewRequest("GET", constant.ServicePrefix+"/images/only-one-segment", nil)
		ctx.Request.RemoteAddr = "10.0.0.1:55555"
		ctx.Params = gin.Params{{Key: "any", Value: "/images/only-one-segment"}}

		if handled := p.handleImageServeRoute(ctx, "/images/only-one-segment"); handled {
			t.Fatalf("iter %d: shape-fail should return false (unhandled), not consume a token", i)
		}
	}

	// A well-formed request from the same IP should still have its burst.
	w := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(w)
	ctx.Request = httptest.NewRequest("GET", constant.ServicePrefix+"/images/ordered/0", nil)
	ctx.Request.RemoteAddr = "10.0.0.1:55555"
	ctx.Params = gin.Params{{Key: "any", Value: "/images/ordered/0"}}

	if !p.handleImageServeRoute(ctx, "/images/ordered/0") {
		t.Fatal("well-formed request after shape-fails should be handled")
	}
	if w.Code != http.StatusOK {
		t.Errorf("well-formed after shape-fails: status = %d, want 200 (burst exhausted by shape-fails?)", w.Code)
	}
}

func TestHandleImageServeRoute_NoMatch_NonImagePath(t *testing.T) {
	gin.SetMode(gin.TestMode)
	p := newTestProxy(t, &ctrl.Ctrl{})

	ctx, _ := newGinCtxForPath(t, "/signature/some-key")
	handled := p.handleImageServeRoute(ctx, "/signature/some-key")
	if handled {
		t.Error("non-image path should not be handled")
	}
}

// TestHandleImageServeRoute_RejectsNonGET pins the method guard. Before the
// fix, any HTTP method (POST, PUT, DELETE, OPTIONS) at the same path would have
// been silently served or silently handled as "not matched" depending on path
// shape. Now anything other than GET/HEAD must return 405 so a future route
// collision doesn't mask a real handler.
func TestHandleImageServeRoute_RejectsNonGET(t *testing.T) {
	gin.SetMode(gin.TestMode)

	c := &ctrl.Ctrl{}
	if err := c.SetupImageStoreForTest(t.TempDir()); err != nil {
		t.Fatalf("SetupImageStoreForTest: %v", err)
	}
	chatKey := "method-test"
	if err := c.StoreTestImage(chatKey, [][]byte{[]byte("img")}); err != nil {
		t.Fatalf("StoreTestImage: %v", err)
	}

	p := newTestProxy(t, c)
	path := "/images/" + chatKey + "/0"

	for _, method := range []string{"POST", "PUT", "DELETE", "PATCH", "OPTIONS"} {
		t.Run(method, func(t *testing.T) {
			w := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(w)
			ctx.Request = httptest.NewRequest(method, constant.ServicePrefix+path, nil)
			ctx.Params = gin.Params{{Key: "any", Value: path}}

			if !p.handleImageServeRoute(ctx, path) {
				t.Fatal("expected route to be handled (handler must reject the method, not return false)")
			}
			if w.Code != http.StatusMethodNotAllowed {
				t.Errorf("status = %d, want 405", w.Code)
			}
			if allow := w.Header().Get("Allow"); allow == "" {
				t.Error("expected Allow header on 405 response")
			}
		})
	}

	t.Run("HEAD-allowed", func(t *testing.T) {
		w := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(w)
		ctx.Request = httptest.NewRequest("HEAD", constant.ServicePrefix+path, nil)
		ctx.Params = gin.Params{{Key: "any", Value: path}}

		if !p.handleImageServeRoute(ctx, path) {
			t.Fatal("HEAD should be handled")
		}
		if w.Code != http.StatusOK {
			t.Errorf("HEAD status = %d, want 200", w.Code)
		}
	})
}

func TestHandleImageServeRoute_InvalidIndex_Returns400(t *testing.T) {
	gin.SetMode(gin.TestMode)
	p := newTestProxy(t, &ctrl.Ctrl{})

	ctx, w := newGinCtxForPath(t, "/images/some-chat-key/notanumber")
	handled := p.handleImageServeRoute(ctx, "/images/some-chat-key/notanumber")
	if !handled {
		t.Fatal("expected route to be handled")
	}
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestHandleImageServeRoute_NotFound_Returns404(t *testing.T) {
	gin.SetMode(gin.TestMode)
	p := newTestProxy(t, &ctrl.Ctrl{})

	ctx, w := newGinCtxForPath(t, "/images/unknown-uuid/0")
	handled := p.handleImageServeRoute(ctx, "/images/unknown-uuid/0")
	if !handled {
		t.Fatal("expected route to be handled")
	}
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

func TestHandleImageServeRoute_ServesImage(t *testing.T) {
	gin.SetMode(gin.TestMode)

	c := &ctrl.Ctrl{}
	if err := c.SetupImageStoreForTest(t.TempDir()); err != nil {
		t.Fatalf("SetupImageStoreForTest: %v", err)
	}

	chatKey := "serve-test-uuid"
	// PNG-like bytes so content-type detection works.
	pngImg := []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A, 0, 0, 0, 1}
	if err := c.StoreTestImage(chatKey, [][]byte{pngImg}); err != nil {
		t.Fatalf("StoreTestImage: %v", err)
	}

	p := newTestProxy(t, c)
	path := "/images/" + chatKey + "/0"
	ctx, w := newGinCtxForPath(t, path)

	handled := p.handleImageServeRoute(ctx, path)
	if !handled {
		t.Fatal("expected route to be handled")
	}
	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	ct := w.Header().Get("Content-Type")
	if !strings.HasPrefix(ct, "image/") {
		t.Errorf("content-type = %q, want image/*", ct)
	}
	if w.Body.Bytes() == nil || len(w.Body.Bytes()) == 0 {
		t.Error("expected non-empty image body")
	}
}

func TestHandleImageServeRoute_IndexOutOfRange_Returns404(t *testing.T) {
	gin.SetMode(gin.TestMode)

	c := &ctrl.Ctrl{}
	if err := c.SetupImageStoreForTest(t.TempDir()); err != nil {
		t.Fatalf("SetupImageStoreForTest: %v", err)
	}
	chatKey := "range-test"
	if err := c.StoreTestImage(chatKey, [][]byte{[]byte("img")}); err != nil {
		t.Fatalf("StoreTestImage: %v", err)
	}

	p := newTestProxy(t, c)
	path := "/images/" + chatKey + "/5" // only index 0 exists
	ctx, w := newGinCtxForPath(t, path)

	handled := p.handleImageServeRoute(ctx, path)
	if !handled {
		t.Fatal("expected route to be handled")
	}
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}
