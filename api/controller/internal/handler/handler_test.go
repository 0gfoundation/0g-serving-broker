package handler

import (
	"strings"
	"testing"
)

func TestCoreConfigUpdatedBodyReportsIgnoredKeys(t *testing.T) {
	body := coreConfigUpdatedBody("service:\n  model: x\nfutureFeature: 1\n")
	ignored, ok := body["ignored_keys"].([]string)
	if !ok || len(ignored) != 1 || !strings.Contains(ignored[0], `"futureFeature"`) {
		t.Fatalf("ignored_keys = %v, want the one unknown key", body["ignored_keys"])
	}
	if body["warning"] == nil || body["message"] == nil {
		t.Fatalf("body = %v, want message and warning", body)
	}

	clean := coreConfigUpdatedBody("service:\n  model: x\n")
	if _, has := clean["ignored_keys"]; has {
		t.Fatalf("a config with only known keys must not report ignored keys: %v", clean)
	}
	if _, has := clean["warning"]; has {
		t.Fatalf("no warning expected: %v", clean)
	}
}
