package handler

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/0glabs/0g-serving-broker/inference/config"
	"github.com/0glabs/0g-serving-broker/inference/model"
)

func getSingleModel(t *testing.T, svc model.Service, isUSD bool) ModelObject {
	t.Helper()
	h := newModelsTestHandler(&mockModelsCtrl{
		service:        svc,
		serviceConfig:  config.Service{},
		priceFeedIsUSD: isUSD,
	})
	w := performRequest(h.GetModels, "GET", "/v1/models", "", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", w.Code)
	}
	var resp ModelListResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}
	if len(resp.Data) != 1 {
		t.Fatalf("models = %d, want 1", len(resp.Data))
	}
	return resp.Data[0]
}

// A single-model audio provider publishes its per-second rate under `audio`,
// the field the multi-model path already uses. It used to fall through to the
// token default and publish the per-SECOND rate as `completion`, a per-TOKEN
// field, with `audio` empty — so a consumer either misread it or, like the 0G
// router, found no audio price at all.
func TestGetModels_SingleModelAudioPricing(t *testing.T) {
	m := getSingleModel(t, model.Service{
		ModelType:   "seed-audio-1.0",
		Type:        "audio-generation",
		InputPrice:  "0",
		OutputPrice: "2500000000000",
	}, false)

	if m.Pricing.Audio != "2500000000000" {
		t.Errorf("pricing.audio = %q, want the per-second OutputPrice", m.Pricing.Audio)
	}
	if m.Pricing.Prompt != "0" || m.Pricing.Completion != "0" {
		t.Errorf("pricing prompt/completion = (%q, %q), want (0, 0) — audio has no per-token charge", m.Pricing.Prompt, m.Pricing.Completion)
	}
}

// USD mirror: service.outputPriceUSDPerSecond is normalized ×1e6 into the
// per-million field at config load, so ÷1e6 recovers the per-second figure.
func TestGetModels_SingleModelAudioPricingUSD(t *testing.T) {
	m := getSingleModel(t, model.Service{
		ModelType:                      "seed-audio-1.0",
		Type:                           "audio-generation",
		InputPrice:                     "0",
		OutputPrice:                    "15870395000000",
		InputPriceUSDPerMillionTokens:  "0",
		OutputPriceUSDPerMillionTokens: "2500",
	}, true)

	if m.PricingUSD == nil {
		t.Fatal("expected pricing_usd for a USD audio-generation service")
	}
	if m.PricingUSD.Audio != "0.0025" {
		t.Errorf("pricing_usd.audio = %q, want 0.0025", m.PricingUSD.Audio)
	}
	if m.PricingUSD.Prompt != "0" || m.PricingUSD.Completion != "0" {
		t.Errorf("pricing_usd prompt/completion = (%q, %q), want (0, 0)", m.PricingUSD.Prompt, m.PricingUSD.Completion)
	}
}
