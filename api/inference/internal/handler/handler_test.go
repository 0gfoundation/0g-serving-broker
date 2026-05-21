package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/0glabs/0g-serving-broker/common/errors"
	"github.com/0glabs/0g-serving-broker/inference/internal/ctrl"
)

// TestHandleBrokerError_PricingUnavailableMapsTo503 locks in the SDK contract
// for the USD-pricing-feed outage: handleBrokerError must rewrite any error
// whose chain contains ctrl.ErrPricingUnavailable into a 503 Service
// Unavailable so SDKs treat it as retryable rather than as a 400/500 terminal
// failure. The check survives errors.Wrap because Wrap uses %w; this test
// verifies both the direct case and the wrapped case.
func TestHandleBrokerError_PricingUnavailableMapsTo503(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name             string
		err              error
		context          string
		wantStatus       int
		wantBodyContains string
	}{
		{
			name:             "bare ErrPricingUnavailable -> 503",
			err:              ctrl.ErrPricingUnavailable,
			wantStatus:       http.StatusServiceUnavailable,
			wantBodyContains: "PRICING_UNAVAILABLE",
		},
		{
			// Realistic call site: ctrl returns the sentinel wrapped via
			// fmt.Errorf("%w"), then handleBrokerError adds a route context.
			// errors.Is must still traverse to the sentinel through both
			// layers, otherwise the SDK sees a 400 and gives up.
			name:             "ErrPricingUnavailable wrapped by ctrl + context -> 503",
			err:              errors.Wrap(ctrl.ErrPricingUnavailable, "fetch USD rate"),
			context:          "service handler",
			wantStatus:       http.StatusServiceUnavailable,
			wantBodyContains: "PRICING_UNAVAILABLE",
		},
		{
			// Negative control: an unrelated error must NOT be promoted to
			// 503. This guards against an over-broad errors.Is match.
			name:             "unrelated error stays at default 400",
			err:              errors.New("invalid request body"),
			context:          "decode",
			wantStatus:       http.StatusBadRequest,
			wantBodyContains: "invalid request body",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodGet, "/v1/service", nil)

			handleBrokerError(c, tt.err, tt.context)

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d (body=%s)", rec.Code, tt.wantStatus, rec.Body.String())
			}
			var body map[string]string
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("unmarshal body: %v (raw=%q)", err, rec.Body.String())
			}
			if !strings.Contains(body["error"], tt.wantBodyContains) {
				t.Fatalf("body %q does not contain %q", body["error"], tt.wantBodyContains)
			}
			if strings.HasPrefix(body["error"], "Provider") {
				t.Fatalf("body %q contains legacy Provider prefix", body["error"])
			}
		})
	}
}
