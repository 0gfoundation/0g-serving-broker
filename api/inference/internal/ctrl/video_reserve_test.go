package ctrl

import (
	"bytes"
	"math"
	"mime/multipart"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
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

// gateCtx is the context the reserve actually runs with: the gate is upstream of PrepareHTTPRequest,
// which is the only thing that stamps CtxKeyResolvedModel. Passing a pre-stamped context is how an
// earlier revision of these tests asserted a per-model branch that was dead in production.
func gateCtx() *gin.Context {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	return c
}

// multipartSeconds builds a multipart body carrying only `seconds`, for the cases where the transport
// decides which source the upstream reads.
func multipartSeconds(t *testing.T, seconds string) (string, string) {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	f, err := w.CreateFormField("seconds")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte(seconds)); err != nil {
		t.Fatal(err)
	}
	w.Close()
	return buf.String(), w.FormDataContentType()
}

func videoReserve(t *testing.T, c *Ctrl, body, contentType, rawQuery string) int64 {
	t.Helper()
	fee, err := c.VideoCreateReserveFee(gateCtx(), []byte(body), contentType, rawQuery)
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
	t.Run("an unreadable query is ignored on JSON, and shadows on multipart", func(t *testing.T) {
		// On a JSON create the query reaches no reader — the translator decodes the body — so the
		// body's duration stands (raised to the vendor floor). On multipart the query IS the reader's
		// source: FormValue returns its unusable value and never consults the body, so the vendor
		// applies its own default and the reserve has to fall back.
		if got := videoReserve(t, c, `{"seconds":1}`, "application/json", "seconds=abc"); got != videoReserveFloorSeconds {
			t.Errorf("json: reserve = %d, want %d (the body's value, floored)", got, videoReserveFloorSeconds)
		}
		mp, ct := multipartSeconds(t, "1")
		if got := videoReserve(t, c, mp, ct, "seconds=abc"); got != videoReserveFallbackSeconds {
			t.Errorf("multipart: reserve = %d, want %d (the query shadows the body upstream)", got, videoReserveFallbackSeconds)
		}
	})
}

// TestVideoReserveSingleModelUsesDearestServiceRatio covers the path with no ModelPricingEntry, where
// settlement reads the service-level videoSizeRatios map with the resolution the RESPONSE names.
func TestVideoReserveSingleModelUsesDearestServiceRatio(t *testing.T) {
	// Shipped defaults: dearest is 1024x1792 / 1792x1024 at 2.0.
	bare := singleModelVideoCtrl(t, nil)
	fee, err := bare.VideoCreateReserveFee(gateCtx(), []byte(`{"seconds":6,"size":"1280x720"}`), "application/json", "")
	if err != nil {
		t.Fatalf("VideoCreateReserveFee: %v", err)
	}
	if fee != "12" {
		t.Errorf("reserve = %s, want 12 (6s x the dearest shipped ratio 2.0); a cheap requested size must not lower it", fee)
	}

	custom := singleModelVideoCtrl(t, map[string]float64{"720x1280": 1.0, "2048x2048": 4.0})
	fee, err = custom.VideoCreateReserveFee(gateCtx(), []byte(`{"seconds":6}`), "application/json", "")
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

	fee, err := c.VideoCreateReserveFee(gateCtx(), []byte(`{"seconds":5,"size":"2K"}`), "application/json", "")
	if err != nil {
		t.Fatalf("VideoCreateReserveFee: %v", err)
	}
	// 5 units x 1.3396 0G = 6.698 0G, i.e. the bill — not the "0" the gate used to pass.
	if fee != "6698000000000000000" {
		t.Errorf("reserve = %s wei, want 6698000000000000000 (the fee this request actually incurred)", fee)
	}
}

// TestVideoReserveResolvesTheModelItself is the regression test for the defect these tests hid: the
// reserve used to read CtxKeyResolvedModel, which only PrepareHTTPRequest stamps — and the gate runs
// BEFORE that. So the per-model billing branch was dead on every real create, and every case here
// passed a pre-stamped context that the production path never has.
func TestVideoReserveResolvesTheModelItself(t *testing.T) {
	c := videoReserveCtrl(t, &config.BillingConfig{
		Mode:  config.BillingModePerUnitTable,
		Table: []config.BillingUnitTier{{Resolution: "2K", Duration: 15, Units: 120}},
	})

	// gateCtx() stamps nothing, exactly like the real gate.
	if got := videoReserve(t, c, `{"model":"vid","seconds":15,"size":"2K"}`, "application/json", ""); got != 120 {
		t.Errorf("reserve = %d, want 120 (the model's own table); the per-model branch is not running", got)
	}
	// And the key is stamped on the way out, so GetBillingPrices in the same call prices the requested
	// model instead of logging "resolvedModel missing from context" and falling back.
	ctx := gateCtx()
	if _, err := c.VideoCreateReserveFee(ctx, []byte(`{"model":"vid","seconds":15}`), "application/json", ""); err != nil {
		t.Fatalf("VideoCreateReserveFee: %v", err)
	}
	if got, _ := ctx.Get(CtxKeyResolvedModel); got != "vid" {
		t.Errorf("resolved model stamped = %v, want \"vid\"", got)
	}
}

// TestVideoReserveClampsToTheVendorFloor covers durations below what any vendor will render. Vendors
// clamp UP as well as down (MiniMax to [4,15]) and bill what they rendered, so pricing the requested
// duration reserved a quarter of the bill.
func TestVideoReserveClampsToTheVendorFloor(t *testing.T) {
	c := videoReserveCtrl(t, &config.BillingConfig{
		Mode:                  config.BillingModePerVideoSecond,
		ResolutionMultipliers: map[string]float64{"2K": 1.0},
	})
	for _, body := range []string{`{"seconds":1}`, `{"seconds":2}`, `{"seconds":3}`, `{"seconds":0.5}`} {
		if got := videoReserve(t, c, body, "application/json", ""); got < videoReserveFloorSeconds {
			t.Errorf("%s: reserve = %d, want >= %d (the vendor renders and bills its own minimum)",
				body, got, videoReserveFloorSeconds)
		}
	}
	// At or above the floor the request is taken at its word.
	if got := videoReserve(t, c, `{"seconds":9}`, "application/json", ""); got != 9 {
		t.Errorf("reserve = %d, want 9", got)
	}
}

// TestVideoReserveQueryEdgeCases pins the two one-character ways the query used to defeat the
// query-first read. Both are measured against real net/http behaviour, which is what the upstream uses.
func TestVideoReserveQueryEdgeCases(t *testing.T) {
	c := videoReserveCtrl(t, &config.BillingConfig{
		Mode:                  config.BillingModePerVideoSecond,
		ResolutionMultipliers: map[string]float64{"2K": 1.0},
	})

	t.Run("present but empty shadows the body only where the query is read", func(t *testing.T) {
		// multipart: `?seconds=` puts "" in r.Form, FormValue returns it, the body is never consulted,
		// and the vendor applies its own default — so the fallback, not the body's value.
		for _, q := range []string{"seconds=", "seconds=%20"} {
			mp, ct := multipartSeconds(t, "1")
			if got := videoReserve(t, c, mp, ct, q); got != videoReserveFallbackSeconds {
				t.Errorf("multipart %q: reserve = %d, want %d", q, got, videoReserveFallbackSeconds)
			}
			// JSON: the query reaches nobody, so it must not quadruple the reserve for a caller who
			// named a duration in the body.
			if got := videoReserve(t, c, `{"seconds":9}`, "application/json", q); got != 9 {
				t.Errorf("json %q: reserve = %d, want 9 (the body's value)", q, got)
			}
		}
	})

	t.Run("a malformed pair elsewhere does not discard a readable seconds", func(t *testing.T) {
		// url.ParseQuery errors on %zz but still returns the pairs it parsed, and r.FormValue ignores
		// that error too — so the upstream reads 15. Guarding on err == nil priced the body's 1.
		if got := videoReserve(t, c, `{"seconds":1}`, "application/json", "seconds=15&junk=%zz"); got != 15 {
			t.Errorf("reserve = %d, want 15", got)
		}
	})

	t.Run("repeated seconds takes the first, like FormValue", func(t *testing.T) {
		if got := videoReserve(t, c, `{"seconds":1}`, "application/json", "seconds=9&seconds=2"); got != 9 {
			t.Errorf("reserve = %d, want 9", got)
		}
	})
}

// TestVideoReserveNeverBelowSettlementBaseline covers the two config shapes where the dearest
// CONFIGURED price is below what settlement charges for a resolution the config does not mention.
func TestVideoReserveNeverBelowSettlementBaseline(t *testing.T) {
	t.Run("per_video_second with every multiplier below 1.0", func(t *testing.T) {
		// resolutionMultiplier returns 1.0 for an unlisted resolution, so the map's max is below
		// settlement's own floor. validateBillingConfig only requires mult > 0, so this loads.
		c := videoReserveCtrl(t, &config.BillingConfig{
			Mode:                  config.BillingModePerVideoSecond,
			ResolutionMultipliers: map[string]float64{"480p": 0.5},
		})
		reserve := videoReserve(t, c, `{"model":"vid","seconds":10,"size":"480p"}`, "application/json", "")
		settled := c.videoOutputUnits(ginCtxWithResolvedModel("vid"), 10, "an-unlisted-tier")
		if reserve < settled {
			t.Errorf("reserve %d < settled %d — the map's max is below settlement's 1.0 baseline", reserve, settled)
		}
	})

	t.Run("per_unit_table whose dearest row is at a shorter duration", func(t *testing.T) {
		// Settlement reaches the table MAXIMUM whenever the resolution the response names has no
		// covering row, independent of duration — so filtering rows by duration under-reserved.
		c := videoReserveCtrl(t, &config.BillingConfig{
			Mode: config.BillingModePerUnitTable,
			Table: []config.BillingUnitTier{
				{Resolution: "4K", Duration: 4, Units: 900},
				{Resolution: "720p", Duration: 10, Units: 100},
			},
		})
		reserve := videoReserve(t, c, `{"model":"vid","seconds":10,"size":"720p"}`, "application/json", "")
		settled := c.videoOutputUnits(ginCtxWithResolvedModel("vid"), 10, "4K")
		if reserve < settled {
			t.Errorf("reserve %d < settled %d — the vendor rendered a tier with no covering row", reserve, settled)
		}
	})
}

// TestVideoReserveTakesTheMaxOfQueryAndBody pins the fix for a hole an earlier revision of THIS reserve
// opened. `seconds` has three consumers and they disagree on where to look: the multipart create reads
// r.FormValue (query wins), the JSON create decodes the body (query never read), and settlement's
// degraded path re-parses the body only (query never read). Reading query-first on the strength of the
// multipart rule alone handed a caller a discount on the other two.
func TestVideoReserveTakesTheMaxOfQueryAndBody(t *testing.T) {
	c := videoReserveCtrl(t, &config.BillingConfig{
		Mode:                  config.BillingModePerVideoSecond,
		ResolutionMultipliers: map[string]float64{"2K": 1.0},
	})

	t.Run("a cheaper query does not undercut a dearer body", func(t *testing.T) {
		// `?seconds=4` with a JSON body of 15: the translator decodes the body and renders 15, and even
		// on multipart, settlement bills the body's value whenever the response omits its duration.
		if got := videoReserve(t, c, `{"seconds":15}`, "application/json", "seconds=4"); got != 15 {
			t.Errorf("reserve = %d, want 15 (the body's value; the query is cheaper and must not win)", got)
		}
	})
	t.Run("a dearer query still wins over the body", func(t *testing.T) {
		// The multipart transport's own rule: r.FormValue resolves the query before the body.
		if got := videoReserve(t, c, `{"seconds":1}`, "application/json", "seconds=15"); got != 15 {
			t.Errorf("reserve = %d, want 15 (the query's value)", got)
		}
	})
	t.Run("a readable query is used when the body names nothing", func(t *testing.T) {
		// The caller DID name a duration. Treating the fallback as a candidate value rather than an
		// unknown sentinel reserved 15 here and ignored them.
		if got := videoReserve(t, c, `{"prompt":"a cat"}`, "application/json", "seconds=6"); got != 6 {
			t.Errorf("reserve = %d, want 6 (the query's value)", got)
		}
	})
	t.Run("an unreadable query does not undercut a dearer body", func(t *testing.T) {
		if got := videoReserve(t, c, `{"seconds":30}`, "application/json", "seconds="); got != 30 {
			t.Errorf("reserve = %d, want 30", got)
		}
	})
}

// TestVideoReserveModeMustBeAVideoMode covers a billing block whose mode is per_token or omitted.
// validBillingModeForType accepts both for ANY service type, so a video model carrying one loads — and
// settlement cannot price video from it either (OutputUnits errors, videoOutputUnits falls back to the
// service size-ratio). Treating them as per_video_second reserved the ratio-less duration against a
// ratio-scaled bill.
func TestVideoReserveModeMustBeAVideoMode(t *testing.T) {
	for _, mode := range []config.BillingMode{"", config.BillingModePerToken} {
		t.Run(string("mode="+mode), func(t *testing.T) {
			billing := &config.BillingConfig{Mode: mode, ResolutionMultipliers: map[string]float64{"2K": 1.2}}
			if _, ok, mayFallBack := billing.MaxVideoOutputUnitsFor(10); ok || !mayFallBack {
				t.Errorf("MaxVideoOutputUnitsFor = (ok %v, mayFallBack %v), want (false, true)", ok, mayFallBack)
			}
			c := videoReserveCtrl(t, billing)
			reserve := videoReserve(t, c, `{"model":"vid","seconds":10,"size":"2K"}`, "application/json", "")
			settled := c.videoOutputUnits(ginCtxWithResolvedModel("vid"), 10, "1024x1792")
			if reserve < settled {
				t.Errorf("reserve %d < settled %d — a non-video mode must not be priced off its multipliers", reserve, settled)
			}
		})
	}
}

// TestVideoReserveSkipsUnusableMultipliers covers a block holding one entry settlement would refuse.
// Taking the raw maximum let that single entry make the whole block answer "cannot price", dropping the
// reserve to a service ratio that knows nothing about the usable multipliers next to it.
func TestVideoReserveSkipsUnusableMultipliers(t *testing.T) {
	for _, poison := range []float64{1e30, math.Inf(1), math.NaN()} {
		billing := &config.BillingConfig{
			Mode:                  config.BillingModePerVideoSecond,
			ResolutionMultipliers: map[string]float64{"cursed": poison, "1080p": 5.0},
		}
		c := videoReserveCtrl(t, billing)
		reserve := videoReserve(t, c, `{"model":"vid","seconds":4,"size":"1080p"}`, "application/json", "")
		settled := c.videoOutputUnits(ginCtxWithResolvedModel("vid"), 4, "1080p")
		if reserve < settled {
			t.Errorf("poison %v: reserve %d < settled %d — the usable 5.0 next to it must still be reserved",
				poison, reserve, settled)
		}
		// And the service-ratio floor must apply, because settlement CAN land there for this block.
		if _, _, mayFallBack := billing.MaxVideoOutputUnitsFor(4); !mayFallBack {
			t.Errorf("poison %v: mayFallBack = false; settlement can reach the service ratio here", poison)
		}
	}
}

// TestVideoReserveDoesNotStampRawClientInputOnSingleModel is the regression test for a free-clip path
// this reserve opened. On a single-model service ResolveRequestedModel returns ok=true with the RAW
// requested string, and the stamped value reaches VideoPollJob.ResolvedModel — a varchar(255). A
// 300-character `model` made that insert fail with MySQL 1406 after the create had already returned a
// job id and registered the caller as its owner: clip retrievable, never billed.
func TestVideoReserveDoesNotStampRawClientInputOnSingleModel(t *testing.T) {
	c := singleModelVideoCtrl(t, nil)
	ctx := gateCtx()
	body := `{"model":"` + strings.Repeat("A", 300) + `","seconds":6}`
	if _, err := c.VideoCreateReserveFee(ctx, []byte(body), "application/json", ""); err != nil {
		t.Fatalf("VideoCreateReserveFee: %v", err)
	}
	if v, exists := ctx.Get(CtxKeyResolvedModel); exists {
		t.Errorf("stamped %q (len %d) on a single-model service; PrepareHTTPRequest's ModelType default must stay in charge",
			v, len(v.(string)))
	}
}

// TestVideoReserveServiceRatioClampIsLoadBearing guards the `!(ratio >= 1)` clamp on the single-model
// path. Mutation testing found it was the one logic guard in the diff that no test covered, and
// videoSizeRatios is validated nowhere — so an operator listing only discount tiers halves every
// reserve without it, while settlement charges the 1.0 baseline for any resolution the map omits.
func TestVideoReserveServiceRatioClampIsLoadBearing(t *testing.T) {
	c := singleModelVideoCtrl(t, map[string]float64{"832x480": 0.5})
	fee, err := c.VideoCreateReserveFee(gateCtx(), []byte(`{"seconds":6,"size":"832x480"}`), "application/json", "")
	if err != nil {
		t.Fatalf("VideoCreateReserveFee: %v", err)
	}
	// Settlement's floor for a resolution the map does not list is 1.0, so 6 units. Without the clamp
	// the reserve would be 6 x 0.5 = 3.
	if fee != "6" {
		t.Errorf("reserve = %s, want 6 (settlement's 1.0 baseline); a below-1.0 map must not lower it", fee)
	}
}

// TestVideoReserveOnlyGatesCreates guards the POST-only check. `/videos` is an exact-match route with no
// method gate, so the OpenAI list endpoint reached the billing switch and demanded a create-sized lock
// to list videos — measured at 160.75 0G on a 4K-tiered config. Mutation testing found no test covered
// it.
func TestVideoReserveOnlyGatesCreates(t *testing.T) {
	// The reserve function itself is method-agnostic; the gate lives in the proxy arm. Assert the
	// property that makes the gate necessary: a bodyless request reserves a full fallback clip, so it
	// must not be reached by a GET.
	c := videoReserveCtrl(t, &config.BillingConfig{
		Mode:                  config.BillingModePerVideoSecond,
		ResolutionMultipliers: map[string]float64{"4K": 8.0},
	})
	if got := videoReserve(t, c, "", "", ""); got != videoReserveFallbackSeconds*8 {
		t.Errorf("bodyless reserve = %d, want %d — if this ever becomes cheap, the POST gate stops mattering and the assertion below is what should change",
			got, videoReserveFallbackSeconds*8)
	}
}
