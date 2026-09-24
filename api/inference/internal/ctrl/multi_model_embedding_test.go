package ctrl

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/0glabs/0g-serving-broker/inference/config"
)

// One decentralized provider, two embedding engines in its CVM: the request's model
// must pick both the engine it is forwarded to and the price it is billed at.
func TestPrepareHTTPRequest_MultiModelEmbeddingRoutesAndBillsPerModel(t *testing.T) {
	svc := config.Service{
		Type:              "embedding",
		ProviderType:      "decentralized",
		PriceDenomination: "NATIVE",
		TargetURL:         "http://embed-a:8000/v1",
		ModelType:         "a",
		ModelPricing: []config.ModelPricingEntry{
			{Model: "a", InputPrice: "300", OutputPrice: "0"},
			{Model: "b", InputPrice: "100", OutputPrice: "0", TargetURL: "http://embed-b:8000/v1"},
		},
	}
	if err := svc.BuildModelPricingMap(); err != nil {
		t.Fatalf("BuildModelPricingMap: %v", err)
	}
	c := &Ctrl{Service: svc, logger: testLogger(), whitelistUsers: make(map[string]struct{})}

	for _, tc := range []struct {
		body, wantURL, wantPrice string
	}{
		{`{"model":"b","input":"hi"}`, "http://embed-b:8000/v1/embeddings", "100"},
		{`{"model":"a","input":"hi"}`, "http://embed-a:8000/v1/embeddings", "300"},
		// No model: the default, at its own engine and price.
		{`{"input":"hi"}`, "http://embed-a:8000/v1/embeddings", "300"},
	} {
		ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
		ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/embeddings", bytes.NewReader([]byte(tc.body)))
		ctx.Request.Header.Set("Content-Type", "application/json")

		req, err := c.PrepareHTTPRequest(ctx, "http://embed-a:8000/v1/embeddings", []byte(tc.body), "embedding")
		if err != nil {
			t.Fatalf("%s: PrepareHTTPRequest: %v", tc.body, err)
		}
		if got := req.URL.String(); got != tc.wantURL {
			t.Errorf("%s: forwarded to %q, want %q", tc.body, got, tc.wantURL)
		}
		prices, err := c.GetBillingPrices(ctx)
		if err != nil {
			t.Fatalf("%s: GetBillingPrices: %v", tc.body, err)
		}
		if prices.InputPrice != tc.wantPrice {
			t.Errorf("%s: billed input %q, want %q", tc.body, prices.InputPrice, tc.wantPrice)
		}
	}

	// A model outside the allowlist is refused, not forwarded to the default engine.
	body := []byte(`{"model":"c","input":"hi"}`)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/embeddings", bytes.NewReader(body))
	ctx.Request.Header.Set("Content-Type", "application/json")
	if _, err := c.PrepareHTTPRequest(ctx, "http://embed-a:8000/v1/embeddings", body, "embedding"); err == nil {
		t.Fatal("unlisted model: PrepareHTTPRequest = nil, want a refusal")
	}
}
