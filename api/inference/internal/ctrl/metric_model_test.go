package ctrl

import (
	"context"
	"testing"

	"github.com/0glabs/0g-serving-broker/inference/config"
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
