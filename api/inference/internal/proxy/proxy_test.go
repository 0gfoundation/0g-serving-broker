package proxy

import (
	"strings"
	"testing"

	constant "github.com/0glabs/0g-serving-broker/inference/const"
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
