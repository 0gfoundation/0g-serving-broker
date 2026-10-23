package ctrl

import (
	"encoding/json"
	"testing"

	"github.com/0glabs/0g-serving-broker/common/util"
	"github.com/0glabs/0g-serving-broker/inference/config"
	"github.com/0glabs/0g-serving-broker/inference/internal/modalprice"
)

var omniShape = &modalprice.Published{
	Input:  map[string]string{"image": "11/6", "video": "11/6", "audio": "79/9"},
	Output: map[string]string{"text_only_input": "69/127", "audio": "626/127"},
}

// The fee updateAccountWithUsage settles for staging-measured qwen3-omni-flash
// usages at 1.8e9 / 12.7e9 wei per token. Expected values are the router's
// blend-then-multiply: floor(price × factor) per token, × tokens — e.g. the
// image request's input is floor(1.8e9 × 119/74) × 37 = 107099999978, not
// floor(1.8e9 × 59.5) = 107100000000.
func TestModalityFee_MatchesRouter(t *testing.T) {
	p, err := omniShape.Parse()
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name              string
		usage             string
		inputFee, outFee  string
		wantIn, wantOutPr string
	}{
		{"image: 27 image + 10 text",
			`{"prompt_tokens":37,"completion_tokens":2,"prompt_tokens_details":{"text_tokens":10,"image_tokens":27}}`,
			"107099999978", "25400000000", "2894594594", "12700000000"},
		{"video: 2552 video + 65 audio + 7 text",
			`{"prompt_tokens":2624,"completion_tokens":2,"prompt_tokens_details":{"text_tokens":7,"video_tokens":2552,"audio_tokens":65}}`,
			"9461199997632", "25400000000", "3605640243", "12700000000"},
		{"text-only input: output discount",
			`{"prompt_tokens":195,"completion_tokens":35,"prompt_tokens_details":{"text_tokens":195}}`,
			"351000000000", "241500000000", "1800000000", "6900000000"},
		{"audio output: 27 audio of 35 completion",
			`{"prompt_tokens":195,"completion_tokens":35,"prompt_tokens_details":{"text_tokens":195},"completion_tokens_details":{"text_tokens":8,"audio_tokens":27}}`,
			"351000000000", "1690199999985", "1800000000", "48291428571"},
		{"no split: flat",
			`{"prompt_tokens":100,"completion_tokens":10}`,
			"180000000000", "127000000000", "1800000000", "12700000000"},
	}
	for _, tc := range cases {
		var u Usage
		if err := json.Unmarshal([]byte(tc.usage), &u); err != nil {
			t.Fatal(err)
		}
		// The same steps updateAccountWithUsage takes after tiers.
		in, out, err := modalityPrices(p, &u, "1800000000", "12700000000")
		if err != nil {
			t.Fatal(err)
		}
		fee, err := computeInputFee(in, &u, config.CacheTokenBillingConfig{})
		if err != nil {
			t.Fatal(err)
		}
		outFee, err := util.Multiply(out, int64(u.CompletionTokens))
		if err != nil {
			t.Fatal(err)
		}
		if in != tc.wantIn || out != tc.wantOutPr || fee.Total.String() != tc.inputFee || outFee.String() != tc.outFee {
			t.Errorf("%s: prices %s/%s fees %s/%s, want %s/%s fees %s/%s", tc.name,
				in, out, fee.Total, outFee, tc.wantIn, tc.wantOutPr, tc.inputFee, tc.outFee)
		}
	}
}

func TestModalityFee_FlatShapeIsUnchanged(t *testing.T) {
	u := Usage{PromptTokens: 37, CompletionTokens: 2, PromptTokensDetails: &PromptTokensDetails{ImageTokens: 27}}
	in, out, err := modalityPrices(modalprice.Pricing{}, &u, "1800000000", "12700000000")
	if err != nil || in != "1800000000" || out != "12700000000" {
		t.Fatalf("flat: %s/%s %v", in, out, err)
	}
}

// GetBillingPrices carries the per-model shape (else the service level) to
// billing; the multi-model fallback, which bills at the on-chain ceiling that
// already includes the shape's maximum, carries none.
func TestGetBillingPrices_ModalityShapeResolution(t *testing.T) {
	audioOnly := &modalprice.Published{Input: map[string]string{"audio": "53/7"}}
	c := &Ctrl{
		logger:          testLogger(),
		modalityPricing: audioOnly,
		Service: newMultiModelService(t, "NATIVE", []config.ModelPricingEntry{
			{Model: "omni", InputPrice: "1800000000", OutputPrice: "12700000000", ModalityPricing: omniShape},
			{Model: "plain", InputPrice: "40", OutputPrice: "120"},
			{Model: "flat", InputPrice: "40", OutputPrice: "120", ModalityPricing: &modalprice.Published{}},
		}, "plain"),
	}
	p, err := c.GetBillingPrices(ginCtxWithResolvedModel("omni"))
	if err != nil || p.ModalityPricing.AudioOutput == nil {
		t.Fatalf("omni: %+v %v", p.ModalityPricing, err)
	}
	p, err = c.GetBillingPrices(ginCtxWithResolvedModel("plain"))
	if err != nil || p.ModalityPricing.Input["audio"] == nil || p.ModalityPricing.Input["audio"].String() != "53/7" {
		t.Fatalf("plain inherits the service shape: %+v %v", p.ModalityPricing, err)
	}
	p, err = c.GetBillingPrices(ginCtxWithResolvedModel("flat"))
	if err != nil || !p.ModalityPricing.Empty() {
		t.Fatalf("flat override: %+v %v", p.ModalityPricing, err)
	}
}
