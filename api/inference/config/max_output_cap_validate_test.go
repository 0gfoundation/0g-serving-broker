package config

import (
	"strings"
	"testing"

	constant "github.com/0glabs/0g-serving-broker/inference/const"
)

func chatbotWithCap() Service {
	return Service{
		Type: constant.ServiceTypeChatbot,
		ModelInfo: &ModelInfo{
			ContextLength:       262144,
			MaxCompletionTokens: 32768,
			SupportedParameters: []string{maxTokensParam},
		},
		EnforceMaxCompletionTokens: true,
	}
}

// strip runs after the clamp and would delete the cap it just set, leaving the
// flag silently inert.
func TestCheckNoCapOverrides_RejectsStripAtBothLevels(t *testing.T) {
	svc := chatbotWithCap()
	svc.StripBodyFields = []string{"logprobs", maxTokensParam}
	if err := svc.checkNoCapOverrides(); err == nil {
		t.Fatal("service-level strip of the cap key must be rejected")
	}

	svc = chatbotWithCap()
	svc.ModelPricing = []ModelPricingEntry{{Model: "m", StripBodyFields: []string{maxCompletionTokensParam}}}
	err := svc.checkNoCapOverrides()
	if err == nil {
		t.Fatal("per-entry strip of the cap key must be rejected")
	}
	if !strings.Contains(err.Error(), "modelPricing[0]") {
		t.Fatalf("error should name the offending entry, got %v", err)
	}
}

// inject is server-config-wins, so it overwrites the clamped value and can
// raise the cap above the advertised maximum.
func TestCheckNoCapOverrides_RejectsInjectAtBothLevels(t *testing.T) {
	svc := chatbotWithCap()
	svc.InjectBodyFields = map[string]interface{}{maxTokensParam: 200000}
	if err := svc.checkNoCapOverrides(); err == nil {
		t.Fatal("service-level inject of the cap key must be rejected")
	}

	svc = chatbotWithCap()
	svc.ModelPricing = []ModelPricingEntry{{Model: "m", InjectBodyFields: map[string]interface{}{maxCompletionTokensParam: 200000}}}
	if err := svc.checkNoCapOverrides(); err == nil {
		t.Fatal("per-entry inject of the cap key must be rejected")
	}
}

// Unrelated strip/inject keys are none of this check's business.
func TestCheckNoCapOverrides_AllowsUnrelatedKeys(t *testing.T) {
	svc := chatbotWithCap()
	svc.StripBodyFields = []string{"logprobs", "top_logprobs"}
	svc.InjectBodyFields = map[string]interface{}{"reasoning": map[string]interface{}{"enabled": false}}
	svc.ModelPricing = []ModelPricingEntry{{
		Model:            "m",
		StripBodyFields:  []string{"temperature"},
		InjectBodyFields: map[string]interface{}{"top_p": 0.9},
	}}

	if err := svc.checkNoCapOverrides(); err != nil {
		t.Fatalf("unrelated keys must be allowed, got %v", err)
	}
}

// The clamp injects a cap under whichever spelling the model advertises, so
// without that declaration the spelling would be a guess — and a wrong one
// fails every capless request on the service.
func TestAllModelInfos_ResolvesLikeEffectiveModelInfoFor(t *testing.T) {
	svc := chatbotWithCap()
	if got := svc.allModelInfos(); len(got) != 1 || got[0] != svc.ModelInfo {
		t.Fatalf("single-model service must report its own block, got %v", got)
	}

	entry := &ModelInfo{ContextLength: 1000, MaxCompletionTokens: 100}
	svc.ModelPricing = []ModelPricingEntry{
		{Model: "own", ModelInfo: entry},
		{Model: "inherits"}, // falls back to the service-level block
	}
	got := svc.allModelInfos()
	if len(got) != 2 || got[0] != entry || got[1] != svc.ModelInfo {
		t.Fatalf("per-entry block must win and a missing one must inherit, got %v", got)
	}

	svc.ModelInfo = nil
	svc.ModelPricing = []ModelPricingEntry{{Model: "none"}}
	if got := svc.allModelInfos(); len(got) != 0 {
		t.Fatalf("an entry with no metadata anywhere contributes nothing, got %v", got)
	}
}
