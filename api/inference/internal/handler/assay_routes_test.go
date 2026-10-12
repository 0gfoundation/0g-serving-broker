package handler

import (
	"testing"

	"github.com/gin-gonic/gin"
	"golang.org/x/time/rate"

	"github.com/0glabs/0g-serving-broker/common/middleware"
	"github.com/0glabs/0g-serving-broker/inference/config"
	constant "github.com/0glabs/0g-serving-broker/inference/const"
	"github.com/0glabs/0g-serving-broker/inference/internal/ctrl"
)

// The assay routes exist only on a spml broker: a teeml broker must serve exactly the
// routes it served before spml was merged.
func TestAssayRoutesOnlyForSPML(t *testing.T) {
	gin.SetMode(gin.TestMode)
	assay := []string{"/v1/attestation/assay", "/v1/attestation/assay/scan", "/v1/payout/voucher"}
	for _, tc := range []struct {
		providerType string
		want         bool
	}{
		{constant.ProviderTypeDecentralized, false},
		{constant.ProviderTypeSPML, true},
	} {
		h := &Handler{
			ctrl:        &ctrl.Ctrl{Service: config.Service{ProviderType: tc.providerType}},
			rateLimiter: middleware.NewRateLimiter(rate.Limit(10), 20),
		}
		r := gin.New()
		h.Register(r)
		have := map[string]bool{}
		for _, rt := range r.Routes() {
			have[rt.Path] = true
		}
		for _, p := range assay {
			if have[p] != tc.want {
				t.Errorf("providerType %q: route %s registered = %v, want %v", tc.providerType, p, have[p], tc.want)
			}
		}
	}
}
