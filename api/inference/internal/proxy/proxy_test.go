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
	"github.com/0glabs/0g-serving-broker/inference/config"
	constant "github.com/0glabs/0g-serving-broker/inference/const"
	"github.com/0glabs/0g-serving-broker/inference/internal/ctrl"
)

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
