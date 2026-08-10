package ctrl

import (
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/0glabs/0g-serving-broker/common/videospec"
	"github.com/0glabs/0g-serving-broker/inference/config"
)

// newDriftTestCtrl builds a multi-model video Ctrl whose single model names the
// given vendor and deployment tier.
func newDriftTestCtrl(t *testing.T, vendor videospec.Vendor, deploymentTier string) (*Ctrl, *gin.Context) {
	t.Helper()
	c := &Ctrl{logger: testLogger()}
	c.Service.Type = "video-generation"
	c.Service.ModelType = "vid-1"
	c.Service.ModelPricing = []config.ModelPricingEntry{{
		Model:       "vid-1",
		OutputPrice: "1000",
		Billing: &config.BillingConfig{
			Mode:              config.BillingModePerVideoSecond,
			Vendor:            string(vendor),
			DefaultResolution: deploymentTier,
		},
	}}
	if err := c.Service.BuildModelPricingMap(); err != nil {
		t.Fatalf("BuildModelPricingMap: %v", err)
	}
	ctx := &gin.Context{}
	ctx.Set(CtxKeyResolvedModel, "vid-1")
	return c, ctx
}

// countDrift runs one reconciliation and reports how many drift lines it logged.
// The throttle keys on the drifting values, so distinct subtests do not mask
// each other.
func countDrift(t *testing.T, c *Ctrl, ctx *gin.Context, body string, billedSeconds int64, billedSize string) int {
	t.Helper()
	rec := &countingLogger{Logger: c.logger}
	c.logger = rec
	c.reconcileVideoSpec(ctx, []byte(body), "application/json", billedSeconds, billedSize)
	return rec.errors
}

// TestReconcileVideoSpec_TierDrift is the case this whole pass exists for.
//
// MiniMax takes its rendered tier from the translator's own configuration, not
// from the request — so a broker configured with a different defaultResolution
// prices every request at a tier the vendor never renders. Nothing else in the
// system would ever notice: the reserve is the only consumer of that value, and
// nothing checks the reserve.
func TestReconcileVideoSpec_TierDrift(t *testing.T) {
	c, ctx := newDriftTestCtrl(t, videospec.VendorMiniMax, "720P")

	// Broker was told 720P; the vendor reports it rendered 2K.
	if got := countDrift(t, c, ctx, `{"seconds":5}`, 5, "2K"); got != 1 {
		t.Errorf("logged %d drift lines for a tier mismatch, want 1", got)
	}
}

func TestReconcileVideoSpec_NoDriftWhenTheyAgree(t *testing.T) {
	c, ctx := newDriftTestCtrl(t, videospec.VendorMiniMax, "2K")

	if got := countDrift(t, c, ctx, `{"seconds":5}`, 5, "2K"); got != 0 {
		t.Errorf("logged %d drift lines for a matching request, want 0", got)
	}
}

// TestReconcileVideoSpec_TierCaseIsNotDrift: billing already treats "1080P" and
// "1080p" as one tier (its multiplier lookup normalizes), so reporting drift for
// a vendor that simply lower-cases its tokens would fire on every request.
func TestReconcileVideoSpec_TierCaseIsNotDrift(t *testing.T) {
	c, ctx := newDriftTestCtrl(t, videospec.VendorMiniMax, "1080P")

	if got := countDrift(t, c, ctx, `{"seconds":5}`, 5, " 1080p "); got != 0 {
		t.Errorf("logged %d drift lines for a case/space-only difference, want 0", got)
	}
}

// TestReconcileVideoSpec_SecondsDrift: the recorded rules said one length, the
// vendor rendered another — the reserve described a different clip than the bill.
func TestReconcileVideoSpec_SecondsDrift(t *testing.T) {
	c, ctx := newDriftTestCtrl(t, videospec.VendorMiniMax, "2K")

	// Request asks for 5; the vendor reports it rendered 8.
	if got := countDrift(t, c, ctx, `{"seconds":5}`, 8, "2K"); got != 1 {
		t.Errorf("logged %d drift lines for a duration mismatch, want 1", got)
	}
}

// TestReconcileVideoSpec_SilentWhenNothingWasPredicted covers every shape where
// there is no prediction to disagree with. Reporting drift for these would put a
// permanent baseline under an alert that is supposed to mean "someone must fix
// configuration".
func TestReconcileVideoSpec_SilentWhenNothingWasPredicted(t *testing.T) {
	t.Run("vendor has no rules recorded", func(t *testing.T) {
		c, ctx := newDriftTestCtrl(t, "seedance", "720P")
		if got := countDrift(t, c, ctx, `{"seconds":5}`, 99, "4K"); got != 0 {
			t.Errorf("logged %d drift lines for an unrecorded vendor, want 0 (nothing was predicted)", got)
		}
	})

	t.Run("vendor reported no tier", func(t *testing.T) {
		c, ctx := newDriftTestCtrl(t, videospec.VendorMiniMax, "2K")
		if got := countDrift(t, c, ctx, `{"seconds":5}`, 5, ""); got != 0 {
			t.Errorf("logged %d drift lines when the vendor reported no tier, want 0", got)
		}
	})

	t.Run("the request determined no tier", func(t *testing.T) {
		// DashScope with an unparsable size: the spec predicts nothing, so the
		// vendor's own default is not a disagreement.
		c, ctx := newDriftTestCtrl(t, videospec.VendorDashScope, "")
		if got := countDrift(t, c, ctx, `{"seconds":5,"size":"garbage"}`, 5, "1080P"); got != 0 {
			t.Errorf("logged %d drift lines when the request determined no tier, want 0", got)
		}
	})

	t.Run("the request determined no duration", func(t *testing.T) {
		// DashScope omits an unreadable duration and picks its own — never
		// predicted, so never drifted.
		c, ctx := newDriftTestCtrl(t, videospec.VendorDashScope, "")
		if got := countDrift(t, c, ctx, `{"size":"720P"}`, 9, "720P"); got != 0 {
			t.Errorf("logged %d drift lines when the request determined no duration, want 0", got)
		}
	})

	t.Run("single-model service has no per-model spec at all", func(t *testing.T) {
		c := &Ctrl{logger: testLogger()}
		c.Service.Type = "video-generation"
		ctx := &gin.Context{}
		rec := &countingLogger{Logger: c.logger}
		c.logger = rec
		c.reconcileVideoSpec(ctx, []byte(`{"seconds":5}`), "application/json", 99, "4K")
		if rec.errors != 0 {
			t.Errorf("logged %d drift lines for a single-model service, want 0", rec.errors)
		}
	})
}

// TestReconcileVideoSpec_SilentWhenTheVendorReportsNoTier is the honesty
// constraint on this whole check.
//
// A vendor that reports no resolution back (DashScope) leaves settlement
// resolving the tier from the same request the prediction came from — so the two
// agree by construction, and there is no independent observation to disagree
// with. Comparing against the raw "size" instead would report drift on EVERY
// request from such a vendor: predicted "1080P" against a literal "1792x1024"
// that was never a tier to begin with.
//
// An alert that fires constantly for a vendor behaving perfectly is worse than
// no alert.
func TestReconcileVideoSpec_SilentWhenTheVendorReportsNoTier(t *testing.T) {
	c, ctx := newDriftTestCtrl(t, videospec.VendorDashScope, "")
	// What settlement now passes: the tier resolved from the client's pixel
	// dimensions, because the vendor reported none of its own.
	settledTier := c.VideoBillingTier(ctx, "1792x1024")
	if settledTier != "1080P" {
		t.Fatalf("precondition: settled tier = %q, want 1080P", settledTier)
	}
	if got := countDrift(t, c, ctx, `{"seconds":5,"size":"1792x1024"}`, 5, settledTier); got != 0 {
		t.Errorf("logged %d drift lines for a vendor that reports no tier, want 0", got)
	}
}
