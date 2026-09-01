package ctrl

import (
	"context"
	"testing"

	"github.com/0glabs/0g-serving-broker/inference/config"
	"github.com/0glabs/0g-serving-broker/inference/monitor"
	"github.com/gin-gonic/gin"
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
	if got, _ := wildOnly.WhitelistMetricLabels(&gin.Context{}, []byte(`{"model":"qwen-default"}`), "application/json"); got != "qwen-default" {
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

// TestMetricUpstream drives the label that splits ONE canonical model across the
// several upstreams that may serve it — the dimension a model-only dashboard
// cannot see. aliyun is listed FIRST so an identity-blind lookup would answer
// aliyun for every glm-5.2 request, making the zhipu assertion proof of real
// identity binding rather than list order.
func TestMetricUpstream(t *testing.T) {
	c := &Ctrl{
		logger: testLogger(),
		Service: newMultiModelService(t, "NATIVE", []config.ModelPricingEntry{
			{Model: "glm-5.2", ProviderIdentity: "aliyun", InputPrice: "1", OutputPrice: "2"},
			{Model: "glm-5.2", ProviderIdentity: "zhipu", InputPrice: "3", OutputPrice: "4"},
		}, "glm-5.2"),
	}

	withIdentity := func(model, identity string) *gin.Context {
		ctx := ginCtxWithResolvedModel(model)
		ctx.Set(CtxKeyResolvedIdentity, identity)
		return ctx
	}

	cases := []struct {
		name string
		ctx  *gin.Context
		want string
	}{
		{"same model, zhipu upstream", withIdentity("glm-5.2", "zhipu"), "zhipu"},
		{"same model, aliyun upstream", withIdentity("glm-5.2", "aliyun"), "aliyun"},
		// A forged or stale X-0G-Upstream must never mint a series: the value is
		// always read back out of the pricing config, never echoed from the header.
		{"forged identity folds to a configured entry", withIdentity("glm-5.2", "attacker-minted"), "aliyun"},
		{"no identity falls back to the first entry", ginCtxWithResolvedModel("glm-5.2"), "aliyun"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := c.metricUpstream(tc.ctx); got != tc.want {
				t.Errorf("metricUpstream = %q, want %q", got, tc.want)
			}
		})
	}

	// Memoized exactly like metricModel: once PrepareHTTPRequest stamps the pair,
	// TrackMetrics and every Record* site read the SAME upstream, so the request
	// counter and the token counters cannot attribute one request to two upstreams.
	stamped := withIdentity("glm-5.2", "zhipu")
	stamped.Set(monitor.CtxKeyMetricUpstream, "stamped-upstream")
	if got := c.metricUpstream(stamped); got != "stamped-upstream" {
		t.Errorf("metricUpstream = %q, want the stamped value", got)
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

// TestWhitelistMetricLabels locks the bounded label set for the
// pre-validation whitelist request counters: enumerated id, "*", or the
// configured model — never a raw user string.
func TestWhitelistMetricLabels(t *testing.T) {
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
	// One model, one upstream, but a per-model providerIdentity that differs from
	// the service-level one: the only shape that catches an upstream resolved
	// from the RAW extracted model instead of the folded label.
	identified := &Ctrl{
		logger: testLogger(),
		Service: newMultiModelService(t, "NATIVE", []config.ModelPricingEntry{
			{Model: "glm-5.2", ProviderIdentity: "zhipu", InputPrice: "1", OutputPrice: "2"},
		}, "glm-5.2"),
	}

	cases := []struct {
		name         string
		c            *Ctrl
		body         string
		want         string
		wantUpstream string
	}{
		{"enumerated id verbatim", multi, `{"model":"qwen-max"}`, "qwen-max", "self"},
		{"unknown id on non-wildcard folds to default", multi, `{"model":"attacker-string"}`, "qwen-max", "self"},
		{"unknown id on wildcard folds to *", wild, `{"model":"attacker-string"}`, "*", "self"},
		{"empty body folds to default", multi, ``, "qwen-max", "self"},
		{"single-model always configured model", single, `{"model":"whatever"}`, "glm-5", "self"},
		{"wildcard literal in body never passes verbatim... it is the sentinel anyway", wild, `{"model":"*"}`, "*", "self"},
		{"per-model identity reaches the label", identified, `{"model":"glm-5.2"}`, "glm-5.2", "zhipu"},
		// The split this pairing exists to prevent: a body naming no model folds
		// to ModelType, so the upstream must be ModelType's — resolving the raw ""
		// instead short-circuits to the service-level identity and the whitelist
		// counter lands on a different series than the token counters for the
		// SAME request.
		{"no model in body resolves the folded model's upstream", identified, `{}`, "glm-5.2", "zhipu"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, gotUpstream := tc.c.WhitelistMetricLabels(&gin.Context{}, []byte(tc.body), "application/json")
			if got != tc.want {
				t.Errorf("WhitelistMetricLabels model = %q, want %q", got, tc.want)
			}
			if gotUpstream != tc.wantUpstream {
				t.Errorf("WhitelistMetricLabels upstream = %q, want %q", gotUpstream, tc.wantUpstream)
			}
		})
	}
}
