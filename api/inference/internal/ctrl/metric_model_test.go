package ctrl

import (
	"context"
	"testing"

	"github.com/0glabs/0g-serving-broker/inference/config"
	"github.com/0glabs/0g-serving-broker/inference/monitor"
)

// TestMetricModel locks the metric-label resolution chain and its cardinality
// bound: enumerated ids pass through, wildcard-admitted user strings collapse
// to "*", missing resolvedModel falls back to the configured model.
func TestMetricModel(t *testing.T) {
	multi := &Ctrl{
		logger: testLogger(),
		Service: newMultiModelService(t, "NATIVE", []config.ModelPricingEntry{
			{Model: "qwen-max", InputPrice: "160", OutputPrice: "640"},
		}, "qwen-max"),
	}
	wild := &Ctrl{
		logger: testLogger(),
		Service: newMultiModelService(t, "NATIVE", []config.ModelPricingEntry{
			{Model: "qwen-max", InputPrice: "160", OutputPrice: "640"},
			{Model: "*", InputPrice: "200", OutputPrice: "800"},
		}, "qwen-max"),
	}
	single := &Ctrl{logger: testLogger(), Service: config.Service{ModelType: "glm-5"}}

	cases := []struct {
		name     string
		c        *Ctrl
		resolved string // "" = key absent
		want     string
	}{
		{"multi-model enumerated id passes through", multi, "qwen-max", "qwen-max"},
		{"multi-model missing key falls back to default (logged)", multi, "", "qwen-max"},
		{"wildcard-admitted user string collapses to *", wild, "totally/made-up-model", "*"},
		{"wildcard deployment, enumerated id still verbatim", wild, "qwen-max", "qwen-max"},
		{"wildcard deployment, configured default model never collapses to *", wild, "qwen-max", "qwen-max"},
		{"single-model resolved value passes through", single, "glm-5", "glm-5"},
		{"single-model missing key falls back to configured", single, "", "glm-5"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.c.metricModel(ginCtxWithResolvedModel(tc.resolved))
			if got != tc.want {
				t.Errorf("metricModel = %q, want %q", got, tc.want)
			}
		})
	}

	// Non-gin context: fallback, never panic.
	if got := single.metricModel(context.Background()); got != "glm-5" {
		t.Errorf("non-gin ctx: got %q, want glm-5", got)
	}

	// Wildcard-ONLY deployment (config permits the configured model to be
	// admitted solely via "*"): the default model must keep its own label,
	// not collapse to the sentinel.
	wildOnly := &Ctrl{
		logger: testLogger(),
		Service: newMultiModelService(t, "NATIVE", []config.ModelPricingEntry{
			{Model: "*", InputPrice: "200", OutputPrice: "800"},
		}, "qwen-default"),
	}
	if got := wildOnly.metricModel(ginCtxWithResolvedModel("qwen-default")); got != "qwen-default" {
		t.Errorf("wildcard-only: configured model = %q, want qwen-default", got)
	}
	if got := wildOnly.metricModel(ginCtxWithResolvedModel("random-user-string")); got != "*" {
		t.Errorf("wildcard-only: user string = %q, want *", got)
	}
	if got := wildOnly.WhitelistMetricModel([]byte(`{"model":"qwen-default"}`), "application/json"); got != "qwen-default" {
		t.Errorf("wildcard-only whitelist: configured model = %q, want qwen-default", got)
	}
}

// TestMetricModel_MemoizesStampedKey: after PrepareHTTPRequest stamps
// monitor.CtxKeyMetricModel, that value is authoritative — a later mutation
// of CtxKeyResolvedModel must not desync the label.
func TestMetricModel_MemoizesStampedKey(t *testing.T) {
	c := &Ctrl{
		logger: testLogger(),
		Service: newMultiModelService(t, "NATIVE", []config.ModelPricingEntry{
			{Model: "qwen-max", InputPrice: "160", OutputPrice: "640"},
		}, "qwen-max"),
	}
	ctx := ginCtxWithResolvedModel("qwen-max")
	ctx.Set(monitor.CtxKeyMetricModel, "stamped-value")
	if got := c.metricModel(ctx); got != "stamped-value" {
		t.Errorf("metricModel = %q, want the stamped value", got)
	}
}

// TestBoundedModelLabel locks the single fold definition both helpers share.
func TestBoundedModelLabel(t *testing.T) {
	multi := &Ctrl{
		logger: testLogger(),
		Service: newMultiModelService(t, "NATIVE", []config.ModelPricingEntry{
			{Model: "qwen-max", InputPrice: "160", OutputPrice: "640"},
			{Model: "*", InputPrice: "200", OutputPrice: "800"},
		}, "qwen-default"),
	}
	single := &Ctrl{logger: testLogger(), Service: config.Service{ModelType: "glm-5"}}

	cases := []struct {
		name      string
		c         *Ctrl
		m         string
		validated bool
		want      string
	}{
		{"single-model always folds to configured (even junk)", single, "junk-value", true, "glm-5"},
		{"enumerated id verbatim", multi, "qwen-max", true, "qwen-max"},
		{"configured model exempt from collapse", multi, "qwen-default", true, "qwen-default"},
		{"validated wildcard-admitted folds to *", multi, "user/string", true, "*"},
		{"unvalidated wildcard-admitted folds to *", multi, "user/string", false, "*"},
		{"empty folds to configured", multi, "", false, "qwen-default"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.c.boundedModelLabel(tc.m, tc.validated); got != tc.want {
				t.Errorf("boundedModelLabel(%q, %v) = %q, want %q", tc.m, tc.validated, got, tc.want)
			}
		})
	}
}

// TestWhitelistMetricModel locks the bounded label set for the
// pre-validation whitelist request counters: enumerated id, "*", or the
// configured model — never a raw user string.
func TestWhitelistMetricModel(t *testing.T) {
	multi := &Ctrl{
		logger: testLogger(),
		Service: newMultiModelService(t, "NATIVE", []config.ModelPricingEntry{
			{Model: "qwen-max", InputPrice: "160", OutputPrice: "640"},
		}, "qwen-max"),
	}
	wild := &Ctrl{
		logger: testLogger(),
		Service: newMultiModelService(t, "NATIVE", []config.ModelPricingEntry{
			{Model: "qwen-max", InputPrice: "160", OutputPrice: "640"},
			{Model: "*", InputPrice: "200", OutputPrice: "800"},
		}, "qwen-max"),
	}
	single := &Ctrl{logger: testLogger(), Service: config.Service{ModelType: "glm-5"}}

	cases := []struct {
		name string
		c    *Ctrl
		body string
		want string
	}{
		{"enumerated id verbatim", multi, `{"model":"qwen-max"}`, "qwen-max"},
		{"unknown id on non-wildcard folds to default", multi, `{"model":"attacker-string"}`, "qwen-max"},
		{"unknown id on wildcard folds to *", wild, `{"model":"attacker-string"}`, "*"},
		{"empty body folds to default", multi, ``, "qwen-max"},
		{"single-model always configured model", single, `{"model":"whatever"}`, "glm-5"},
		{"wildcard literal in body never passes verbatim... it is the sentinel anyway", wild, `{"model":"*"}`, "*"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.c.WhitelistMetricModel([]byte(tc.body), "application/json")
			if got != tc.want {
				t.Errorf("WhitelistMetricModel = %q, want %q", got, tc.want)
			}
		})
	}
}
