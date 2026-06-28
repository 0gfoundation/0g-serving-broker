package ctrl

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/0glabs/0g-serving-broker/inference/config"
)

// TestValidateModelAllowlist_PerEntryUpstreamRewrite covers the issue-558 path:
// a multi-model centralized provider rewrites the request body's `model` to the
// matched entry's upstream id before forwarding, while billing/metrics stay
// keyed on the public id (resolvedModel). Aliases resolve to their canonical
// entry; an entry without upstreamModel forwards its own id; unknown models are
// rejected.
func TestValidateModelAllowlist_PerEntryUpstreamRewrite(t *testing.T) {
	svc := newMultiModelService(t, "NATIVE", []config.ModelPricingEntry{
		{
			Model:         "zai-org/GLM-5-FP8",
			InputPrice:    "100",
			OutputPrice:   "300",
			UpstreamModel: "z-ai/glm-5",
			ModelAliases:  []string{"glm-5-legacy"},
		},
		{
			Model:         "deepseek-ai/DeepSeek-V4-Flash",
			InputPrice:    "10",
			OutputPrice:   "30",
			UpstreamModel: "deepseek/deepseek-v4-flash",
		},
		{
			Model:       "plain-model",
			InputPrice:  "10",
			OutputPrice: "30",
		},
	}, "zai-org/GLM-5-FP8")

	cases := []struct {
		name          string
		requestModel  string // "" means omit the model field entirely
		wantForwarded string
		wantResolved  string
		wantErr       bool
	}{
		{"public id rewritten to upstream", "zai-org/GLM-5-FP8", "z-ai/glm-5", "zai-org/GLM-5-FP8", false},
		{"alias resolves to canonical entry", "glm-5-legacy", "z-ai/glm-5", "zai-org/GLM-5-FP8", false},
		{"second model rewritten", "deepseek-ai/DeepSeek-V4-Flash", "deepseek/deepseek-v4-flash", "deepseek-ai/DeepSeek-V4-Flash", false},
		{"no upstream forwards own id", "plain-model", "plain-model", "plain-model", false},
		{"omitted model uses default and rewrites", "", "z-ai/glm-5", "zai-org/GLM-5-FP8", false},
		{"unknown model rejected", "nope/not-served", "", "", true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := &Ctrl{logger: testLogger(), Service: svc}
			ctx, _ := gin.CreateTestContext(httptest.NewRecorder())

			var body []byte
			if tc.requestModel == "" {
				body = []byte(`{"messages":[]}`)
			} else {
				body = []byte(`{"model":"` + tc.requestModel + `","messages":[]}`)
			}

			out, err := c.ValidateModelAllowlist(ctx, body, "0xuser")
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected rejection for %q, got nil", tc.requestModel)
				}
				return
			}
			if err != nil {
				t.Fatalf("ValidateModelAllowlist(%q): %v", tc.requestModel, err)
			}

			var m map[string]interface{}
			if err := json.Unmarshal(out, &m); err != nil {
				t.Fatalf("unmarshal forwarded body: %v", err)
			}
			if got, _ := m["model"].(string); got != tc.wantForwarded {
				t.Errorf("forwarded model = %q, want %q", got, tc.wantForwarded)
			}
			resolved, _ := ctx.Get(CtxKeyResolvedModel)
			if resolved != tc.wantResolved {
				t.Errorf("resolvedModel = %v, want %q", resolved, tc.wantResolved)
			}
		})
	}
}

// TestValidateModelAllowlist_WildcardForwardsVerbatim verifies that a wildcard
// catch-all entry (no concrete upstream id) forwards the requested model
// unchanged and records it as the resolved model for wildcard attribution.
func TestValidateModelAllowlist_WildcardForwardsVerbatim(t *testing.T) {
	svc := newMultiModelService(t, "NATIVE", []config.ModelPricingEntry{
		{Model: config.ModelWildcard, InputPrice: "200", OutputPrice: "600"},
	}, "default-model")

	c := &Ctrl{logger: testLogger(), Service: svc}
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())

	out, err := c.ValidateModelAllowlist(ctx, []byte(`{"model":"some/unlisted-model","messages":[]}`), "0xuser")
	if err != nil {
		t.Fatalf("ValidateModelAllowlist: %v", err)
	}
	var m map[string]interface{}
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got, _ := m["model"].(string); got != "some/unlisted-model" {
		t.Errorf("wildcard forwarded model = %q, want verbatim some/unlisted-model", got)
	}
	if resolved, _ := ctx.Get(CtxKeyResolvedModel); resolved != "some/unlisted-model" {
		t.Errorf("resolvedModel = %v, want some/unlisted-model", resolved)
	}
}
