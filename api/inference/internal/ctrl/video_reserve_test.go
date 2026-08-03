package ctrl

import (
	"bytes"
	"mime/multipart"
	"strconv"
	"testing"
	"time"

	"github.com/patrickmn/go-cache"

	"github.com/0glabs/0g-serving-broker/inference/config"
	"github.com/0glabs/0g-serving-broker/inference/model"
)

// videoReserveCtrl builds a multi-model video service whose single entry carries the given billing
// block, priced at 1 wei per unit so a reserve in wei reads as a unit count.
func videoReserveCtrl(t *testing.T, billing *config.BillingConfig) *Ctrl {
	t.Helper()
	return &Ctrl{logger: testLogger(), Service: newMultiModelService(t, "NATIVE", []config.ModelPricingEntry{{
		Model:       "vid",
		InputPrice:  "0",
		OutputPrice: "1",
		Billing:     billing,
	}}, "vid")}
}

// singleModelVideoCtrl builds a service with NO modelPricing, so the reserve takes the service-ratio
// path and the price comes from the on-chain service record (seeded into the cache at 1 wei per unit,
// so a reserve in wei reads as a unit count).
func singleModelVideoCtrl(t *testing.T, ratios map[string]float64) *Ctrl {
	t.Helper()
	svc := config.Service{PriceDenomination: "NATIVE", ModelType: "vid"}
	if ratios != nil {
		svc.ModelInfo = &config.ModelInfo{VideoSizeRatios: ratios}
	}
	c := &Ctrl{
		logger:       testLogger(),
		Service:      svc,
		serviceCache: cache.New(time.Minute, time.Minute),
	}
	c.serviceCache.Set("current_service", model.Service{InputPrice: "0", OutputPrice: "1"}, cache.DefaultExpiration)
	return c
}

func videoReserve(t *testing.T, c *Ctrl, body, contentType, rawQuery string) int64 {
	t.Helper()
	fee, err := c.VideoCreateReserveFee(ginCtxWithResolvedModel("vid"), []byte(body), contentType, rawQuery)
	if err != nil {
		t.Fatalf("VideoCreateReserveFee: %v", err)
	}
	n, err := strconv.ParseInt(fee, 10, 64)
	if err != nil {
		t.Fatalf("reserve %q is not an integer: %v", fee, err)
	}
	return n
}

// TestVideoReserveIsNeverBelowSettlement is the invariant the gate exists for: whatever the request
// says, the reserve must cover what settlement will bill for the clip that actually comes back.
//
// The reserve reads the requested duration but IGNORES the requested size, because the vendor picks the
// rendered tier — MiniMax renders MINIMAX_RESOLUTION from the translator's own environment and uses
// pixel dimensions only for the aspect ratio, DashScope derives the tier from max(width, height). So
// each case here asks for the cheapest tier and settles at the dearest, which is the shape that used to
// underfund the gate.
func TestVideoReserveIsNeverBelowSettlement(t *testing.T) {
	perSecond := &config.BillingConfig{
		Mode:                  config.BillingModePerVideoSecond,
		ResolutionMultipliers: map[string]float64{"720p": 1.0, "1080p": 1.5, "2K": 3.0},
	}
	table := &config.BillingConfig{
		Mode: config.BillingModePerUnitTable,
		Table: []config.BillingUnitTier{
			{Resolution: "768P", Duration: 6, Units: 6},
			{Resolution: "2K", Duration: 6, Units: 45},
			{Resolution: "2K", Duration: 15, Units: 120},
		},
	}

	for _, tc := range []struct {
		name        string
		billing     *config.BillingConfig
		body        string
		dearestSize string
	}{
		{name: "per_video_second, cheap tier requested", billing: perSecond, body: `{"seconds":5,"size":"720p"}`, dearestSize: "2K"},
		{name: "per_video_second, pixel size requested", billing: perSecond, body: `{"seconds":5,"size":"1280x720"}`, dearestSize: "2K"},
		{name: "per_video_second, no size requested", billing: perSecond, body: `{"seconds":5}`, dearestSize: "2K"},
		{name: "per_unit_table, cheap tier requested", billing: table, body: `{"seconds":6,"size":"768P"}`, dearestSize: "2K"},
		{name: "per_unit_table, no size requested", billing: table, body: `{"seconds":6}`, dearestSize: "2K"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := videoReserveCtrl(t, tc.billing)
			reserve := videoReserve(t, c, tc.body, "application/json", "")

			// What settlement bills once the vendor reports the dearest tier it can render.
			seconds, _ := videoSecondsSizeFromRequest([]byte(tc.body), "application/json")
			settled := c.videoOutputUnits(ginCtxWithResolvedModel("vid"), seconds, tc.dearestSize)

			if reserve < settled {
				t.Errorf("reserve %d < settled %d for %s at %q — the gate would admit a create it cannot cover",
					reserve, settled, tc.body, tc.dearestSize)
			}
		})
	}
}

// TestVideoReserveSecondsSources pins where the duration comes from, in the order that matters.
func TestVideoReserveSecondsSources(t *testing.T) {
	// One multiplier, so reserve == seconds and the number reads as the duration the gate priced.
	c := videoReserveCtrl(t, &config.BillingConfig{
		Mode:                  config.BillingModePerVideoSecond,
		ResolutionMultipliers: map[string]float64{"720p": 1.0},
	})

	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	f, _ := w.CreateFormField("seconds")
	f.Write([]byte("7"))
	w.Close()

	t.Run("JSON body", func(t *testing.T) {
		if got := videoReserve(t, c, `{"seconds":5}`, "application/json", ""); got != 5 {
			t.Errorf("reserve = %d, want 5", got)
		}
	})
	t.Run("multipart body", func(t *testing.T) {
		if got := videoReserve(t, c, buf.String(), w.FormDataContentType(), ""); got != 7 {
			t.Errorf("reserve = %d, want 7", got)
		}
	})
	t.Run("query beats the body", func(t *testing.T) {
		// The upstream reads the create with r.FormValue, which resolves the query BEFORE the body —
		// so `?seconds=15` over a body of `seconds=1` renders 15. Pricing the body's 1 was a discount.
		if got := videoReserve(t, c, `{"seconds":1}`, "application/json", "seconds=15"); got != 15 {
			t.Errorf("reserve = %d, want 15 (the query's value)", got)
		}
	})
	t.Run("absent seconds prices the fallback", func(t *testing.T) {
		// The vendor applies its own default, which the broker cannot see.
		if got := videoReserve(t, c, `{"prompt":"a cat"}`, "application/json", ""); got != videoReserveFallbackSeconds {
			t.Errorf("reserve = %d, want %d", got, videoReserveFallbackSeconds)
		}
	})
	t.Run("unreadable seconds prices the fallback, never the floor", func(t *testing.T) {
		for _, body := range []string{`{"seconds":"abc"}`, `{"seconds":0}`, `{"seconds":-3}`, `{"seconds":true}`, `null`, ``} {
			if got := videoReserve(t, c, body, "application/json", ""); got != videoReserveFallbackSeconds {
				t.Errorf("body %q: reserve = %d, want %d", body, got, videoReserveFallbackSeconds)
			}
		}
	})
	t.Run("unreadable query value does not fall through to a cheaper body", func(t *testing.T) {
		if got := videoReserve(t, c, `{"seconds":1}`, "application/json", "seconds=abc"); got != videoReserveFallbackSeconds {
			t.Errorf("reserve = %d, want %d", got, videoReserveFallbackSeconds)
		}
	})
}

// TestVideoReserveSingleModelUsesDearestServiceRatio covers the path with no ModelPricingEntry, where
// settlement reads the service-level videoSizeRatios map with the resolution the RESPONSE names.
func TestVideoReserveSingleModelUsesDearestServiceRatio(t *testing.T) {
	// Shipped defaults: dearest is 1024x1792 / 1792x1024 at 2.0.
	bare := singleModelVideoCtrl(t, nil)
	fee, err := bare.VideoCreateReserveFee(ginCtxWithResolvedModel(""), []byte(`{"seconds":6,"size":"1280x720"}`), "application/json", "")
	if err != nil {
		t.Fatalf("VideoCreateReserveFee: %v", err)
	}
	if fee != "12" {
		t.Errorf("reserve = %s, want 12 (6s x the dearest shipped ratio 2.0); a cheap requested size must not lower it", fee)
	}

	custom := singleModelVideoCtrl(t, map[string]float64{"720x1280": 1.0, "2048x2048": 4.0})
	fee, err = custom.VideoCreateReserveFee(ginCtxWithResolvedModel(""), []byte(`{"seconds":6}`), "application/json", "")
	if err != nil {
		t.Fatalf("VideoCreateReserveFee: %v", err)
	}
	if fee != "24" {
		t.Errorf("reserve = %s, want 24 (6s x the operator's dearest ratio 4.0)", fee)
	}
}

// TestVideoReserveMeasuredMainnetCase is the shape that produced the incident: 1.0 0G locked, one 5s 2K
// clip billed 6.698 0G. With the on-chain price this provider advertised, the reserve must exceed the
// balance that used to pass the gate.
func TestVideoReserveMeasuredMainnetCase(t *testing.T) {
	// 2K at 5s billed 5 units; the fee was 6.698 0G, so ~1.3396 0G per unit.
	const perUnitWei = "1339600000000000000"
	c := &Ctrl{logger: testLogger(), Service: newMultiModelService(t, "NATIVE", []config.ModelPricingEntry{{
		Model:       "vid",
		InputPrice:  "0",
		OutputPrice: perUnitWei,
		Billing: &config.BillingConfig{
			Mode:                  config.BillingModePerVideoSecond,
			ResolutionMultipliers: map[string]float64{"2K": 1.0},
		},
	}}, "vid")}

	fee, err := c.VideoCreateReserveFee(ginCtxWithResolvedModel("vid"), []byte(`{"seconds":5,"size":"2K"}`), "application/json", "")
	if err != nil {
		t.Fatalf("VideoCreateReserveFee: %v", err)
	}
	// 5 units x 1.3396 0G = 6.698 0G, i.e. the bill — not the "0" the gate used to pass.
	if fee != "6698000000000000000" {
		t.Errorf("reserve = %s wei, want 6698000000000000000 (the fee this request actually incurred)", fee)
	}
}
