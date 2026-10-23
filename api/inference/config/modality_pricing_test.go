package config

import (
	"strings"
	"testing"
)

const modalitySingleModel = `
service:
  servingUrl: "http://example.com"
  targetUrl: "https://backend:8000"
  inputPrice: "1800000000"
  outputPrice: "12700000000"
  type: "chatbot"
  model: "qwen3-omni-flash"
  verifiability: "TeeML"
`

const modalityMultiModel = `
service:
  servingUrl: "http://example.com"
  targetUrl: "https://backend:8000"
  type: "chatbot"
  model: "qwen3-omni-flash"
  providerType: "centralized"
  providerIdentity: "alibaba"
  verifiability: "TeeML"
  modelPricing:
    - model: "qwen3-omni-flash"
      inputPrice: "1800000000"
      outputPrice: "12700000000"
%s    - model: "qwen-plus"
      inputPrice: "2000000000"
      outputPrice: "3000000000"
%s`

func multiModel(omniExtra, plusExtra string) string {
	return strings.Replace(strings.Replace(modalityMultiModel, "%s", omniExtra, 1), "%s", plusExtra, 1)
}

const omniShapeYAML = `
modalityPricing:
  input:  {image: "11/6", video: "11/6", audio: "79/9"}
  output: {text_only_input: "69/127", audio: "626/127"}
`

const omniEntryShapeYAML = `      modalityPricing:
        input:  {image: "11/6", video: "11/6", audio: "79/9"}
        output: {text_only_input: "69/127", audio: "626/127"}
`

func TestModalityPricing_AcceptsRealQwenShapes(t *testing.T) {
	cfg, err := loadYAML(t, modalitySingleModel+omniShapeYAML)
	if err != nil {
		t.Fatalf("service-level omni shape: %v", err)
	}
	if got := cfg.ModalityPricing.Output["audio"]; got != "626/127" {
		t.Fatalf("output.audio = %q", got)
	}
	// Other Qwen omni / audio shapes the router accepts.
	for _, y := range []string{
		"modalityPricing:\n  input: {audio: \"90/11\"}\n  output: {audio: \"720/133\"}\n",
		"modalityPricing:\n  input: {audio: \"53/7\"}\n  output: {audio: \"213/40\"}\n",
	} {
		if _, err := loadYAML(t, modalitySingleModel+y); err != nil {
			t.Errorf("%q: %v", y, err)
		}
	}
	cfg, err = loadYAML(t, multiModel(omniEntryShapeYAML, ""))
	if err != nil {
		t.Fatalf("per-model omni shape: %v", err)
	}
	if e := cfg.Service.GetModelPricing("qwen3-omni-flash"); e == nil || e.ModalityPricing == nil || e.ModalityPricing.Input["audio"] != "79/9" {
		t.Fatalf("per-model shape not loaded: %+v", e)
	}
}

// The on-chain ceiling is the max over models of price × the shape's largest
// multiplier: input 1.8e9 × 79/9, output 12.7e9 × 626/127.
func TestModalityPricing_CeilingCoversTheDearestModality(t *testing.T) {
	cfg, err := loadYAML(t, multiModel(omniEntryShapeYAML, ""))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Service.InputPrice != "15800000000" || cfg.Service.OutputPrice != "62600000000" {
		t.Fatalf("ceiling = %s / %s, want 15800000000 / 62600000000", cfg.Service.InputPrice, cfg.Service.OutputPrice)
	}
	// Without a shape the ceiling is the plain max, as before.
	cfg, err = loadYAML(t, multiModel("", ""))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Service.InputPrice != "2000000000" || cfg.Service.OutputPrice != "12700000000" {
		t.Fatalf("flat ceiling = %s / %s", cfg.Service.InputPrice, cfg.Service.OutputPrice)
	}
}

func TestModalityPricing_Rejects(t *testing.T) {
	shape := func(in, out string) string {
		return "modalityPricing:\n  input: {" + in + "}\n  output: {" + out + "}\n"
	}
	entryShape := func(in, out string) string {
		return "      modalityPricing:\n        input: {" + in + "}\n        output: {" + out + "}\n"
	}
	const tiers = "tieredPricing:\n  enabled: true\n  tiers:\n    - {maxInputTokens: 0, inputMultiplier: 1, outputMultiplier: 1}\n"
	const cache = "cacheTokenBilling: {enabled: true, divisor: 5}\n"
	const entryTiers = "      tiers:\n        - {maxInputTokens: 0, inputMultiplier: 1, outputMultiplier: 1}\n"
	const entryCache = "      cacheTokenBilling: {enabled: true, divisor: 5}\n"
	cases := map[string]string{
		"unknown input key":         modalitySingleModel + shape(`text: "2"`, ""),
		"unknown output key":        modalitySingleModel + shape("", `video: "2"`),
		"spaces":                    modalitySingleModel + shape(`image: "11 / 6"`, ""),
		"decimal":                   modalitySingleModel + shape(`image: "1.5"`, ""),
		"input below 1x":            modalitySingleModel + shape(`image: "1/2"`, ""),
		"input above 100x":          modalitySingleModel + shape(`audio: "101"`, ""),
		"text-only above 1x":        modalitySingleModel + shape("", `text_only_input: "3/2"`),
		"output audio below 1x":     modalitySingleModel + shape("", `audio: "1/2"`),
		"service + tieredPricing":   modalitySingleModel + omniShapeYAML + tiers,
		"service + cache divisor 5": modalitySingleModel + omniShapeYAML + cache,
		"per-model bad value":       multiModel(entryShape(`audio: "0/1"`, ""), ""),
		"per-model + own tiers":     multiModel(omniEntryShapeYAML+entryTiers, ""),
		"per-model + own cache":     multiModel(omniEntryShapeYAML+entryCache, ""),
		"per-model + service cache": multiModel(omniEntryShapeYAML, "") + cache,
		"inherited + entry tiers":   multiModel("", entryTiers) + omniShapeYAML,
	}
	for name, y := range cases {
		if _, err := loadYAML(t, y); err == nil || !strings.Contains(err.Error(), "modalityPricing") {
			t.Errorf("%s: want a modalityPricing error, got %v", name, err)
		}
	}
	// A cache block with divisor 1 discounts nothing, so it may coexist.
	if _, err := loadYAML(t, modalitySingleModel+omniShapeYAML+"cacheTokenBilling: {enabled: true, divisor: 1}\n"); err != nil {
		t.Errorf("divisor 1: %v", err)
	}
}
