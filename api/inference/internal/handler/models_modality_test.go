package handler

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/0glabs/0g-serving-broker/inference/config"
	"github.com/0glabs/0g-serving-broker/inference/internal/modalprice"
	"github.com/0glabs/0g-serving-broker/inference/model"
)

var omniModality = &modalprice.Published{
	Input:  map[string]string{"image": "11/6", "video": "11/6", "audio": "79/9"},
	Output: map[string]string{"text_only_input": "69/127", "audio": "626/127"},
}

const omniModalityJSON = `"modality_pricing":{"input":{"audio":"79/9","image":"11/6","video":"11/6"},"output":{"audio":"626/127","text_only_input":"69/127"}}`

func modalityModelsBody(t *testing.T, mock *mockModelsCtrl) string {
	t.Helper()
	w := performRequest(newModelsTestHandler(mock).GetModels, "GET", "/v1/models", "", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	return w.Body.String()
}

func TestGetModels_ModalityPricing_SingleModel(t *testing.T) {
	created := time.Unix(1700000000, 0)
	mock := &mockModelsCtrl{
		service: model.Service{Model: model.Model{CreatedAt: &created}, ModelType: "qwen3-omni-flash", Type: "chatbot",
			InputPrice: "1800000000", OutputPrice: "12700000000"},
	}
	unset := modalityModelsBody(t, mock)
	if strings.Contains(unset, "modality_pricing") {
		t.Fatalf("unset shape must not appear: %s", unset)
	}

	mock.modalityPricingConfig = omniModality
	body := modalityModelsBody(t, mock)
	if !strings.Contains(body, omniModalityJSON) {
		t.Fatalf("want %s in %s", omniModalityJSON, body)
	}
	// Setting the shape adds exactly that key and nothing else.
	if strings.Replace(body, ","+omniModalityJSON, "", 1) != unset {
		t.Fatalf("body differs beyond modality_pricing:\n%s\n%s", unset, body)
	}
}

func TestGetModels_ModalityPricing_PerModel(t *testing.T) {
	created := time.Unix(1700000000, 0)
	svcCfg := config.Service{
		ProviderType: "centralized", ProviderIdentity: "alibaba", TargetURL: "https://dashscope.aliyuncs.com/v1",
		ModelType: "qwen3-omni-flash", Type: "chatbot",
		ModelPricing: []config.ModelPricingEntry{
			{Model: "qwen3-omni-flash", InputPrice: "1800000000", OutputPrice: "12700000000", ModalityPricing: omniModality},
			{Model: "qwen-plus", InputPrice: "40", OutputPrice: "120"},
			{Model: "qwen-flat", InputPrice: "40", OutputPrice: "120", ModalityPricing: &modalprice.Published{}},
		},
	}
	if err := svcCfg.BuildModelPricingMap(); err != nil {
		t.Fatal(err)
	}
	mock := &mockModelsCtrl{
		service:       model.Service{Model: model.Model{CreatedAt: &created}, ModelType: "qwen3-omni-flash", Type: "chatbot"},
		serviceConfig: svcCfg,
	}
	var resp struct {
		Data []struct {
			ID      string          `json:"id"`
			Pricing json.RawMessage `json:"pricing"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(modalityModelsBody(t, mock)), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Data) != 3 {
		t.Fatalf("got %d models", len(resp.Data))
	}
	for _, m := range resp.Data {
		has := strings.Contains(string(m.Pricing), omniModalityJSON)
		if has != (m.ID == "qwen3-omni-flash") || (!has && strings.Contains(string(m.Pricing), "modality_pricing")) {
			t.Errorf("%s pricing: %s", m.ID, m.Pricing)
		}
	}

	// A service-level shape reaches entries without their own; "{}" stays flat.
	mock.modalityPricingConfig = &modalprice.Published{Input: map[string]string{"audio": "53/7"}}
	if err := json.Unmarshal([]byte(modalityModelsBody(t, mock)), &resp); err != nil {
		t.Fatal(err)
	}
	for _, m := range resp.Data {
		inherited := strings.Contains(string(m.Pricing), `"modality_pricing":{"input":{"audio":"53/7"}}`)
		if inherited != (m.ID == "qwen-plus") {
			t.Errorf("%s pricing with service shape: %s", m.ID, m.Pricing)
		}
	}
}
