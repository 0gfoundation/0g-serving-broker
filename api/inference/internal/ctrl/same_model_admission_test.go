package ctrl

import (
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/0glabs/0g-serving-broker/inference/config"
)

// TestValidateModelAllowlist_BindsUpstreamIdentity drives the ACTUAL admission
// path (not the accessors directly): a request for the two-upstream model
// "glm-5.2" carrying X-0G-Upstream: zhipu runs through ValidateModelAllowlist,
// then every downstream per-model lookup is re-read via resolvedIdentity(ctx) —
// exactly the (resolvedModel, identity) shape the forward/secret sites use. It
// must resolve to the ZHIPU entry throughout.
//
// This is the regression guard for the load-bearing seam: delete the
// ctx.Set(CtxKeyResolvedIdentity, …) line in ValidateModelAllowlist and this
// test FAILS — resolvedIdentity(ctx) goes empty, and the identity assertion
// (plus EffectiveTargetURLFor, which then answers for the cheapest entry) no longer sees
// zhipu. aliyun is also the CHEAPEST entry (in 1 / out 2 vs zhipu's 3 / 4), so the
// identity-agnostic lookup would return aliyun for every glm-5.2 request — a
// passing test is therefore proof of real identity binding. Keep aliyun cheaper if
// the fixture prices are ever edited, or this stops proving anything.
func TestValidateModelAllowlist_BindsUpstreamIdentity(t *testing.T) {
	svc := newMultiModelService(t, "NATIVE", []config.ModelPricingEntry{
		{Model: "glm-5.2", ProviderIdentity: "aliyun", InputPrice: "1", OutputPrice: "2",
			TargetURL: "https://aliyun.example.com/v1", AdditionalSecret: map[string]string{"Authorization": "Bearer aliyun-key"}},
		{Model: "glm-5.2", ProviderIdentity: "zhipu", InputPrice: "3", OutputPrice: "4",
			TargetURL: "https://zhipu.example.com/v1", AdditionalSecret: map[string]string{"Authorization": "Bearer zhipu-key"}},
	}, "glm-5.2")

	c := &Ctrl{logger: testLogger(), Service: svc}
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest("POST", "/v1/chat/completions", nil)
	ctx.Request.Header.Set(config.UpstreamIdentityHeader, "zhipu")

	if _, err := c.ValidateModelAllowlist(ctx, []byte(`{"model":"glm-5.2","messages":[]}`), "0xuser"); err != nil {
		t.Fatalf("ValidateModelAllowlist: %v", err)
	}

	resolvedVal, _ := ctx.Get(CtxKeyResolvedModel)
	resolvedModel, _ := resolvedVal.(string)
	identity := resolvedIdentity(ctx)
	if identity != "zhipu" {
		t.Fatalf("resolvedIdentity(ctx) = %q; want zhipu (was the CtxKeyResolvedIdentity bind dropped?)", identity)
	}

	if got := c.Service.EffectiveTargetURLFor(resolvedModel, identity); got != "https://zhipu.example.com/v1" {
		t.Errorf("EffectiveTargetURLFor = %q; want zhipu upstream", got)
	}
	if got := c.Service.EffectiveAdditionalSecretFor(resolvedModel, identity)["Authorization"]; got != "Bearer zhipu-key" {
		t.Errorf("EffectiveAdditionalSecretFor = %q; want zhipu key", got)
	}
}
