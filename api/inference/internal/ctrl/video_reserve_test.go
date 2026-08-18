package ctrl

import (
	"bytes"
	"errors"
	"math/big"
	"mime/multipart"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/0glabs/0g-serving-broker/common/videospec"
	"github.com/0glabs/0g-serving-broker/inference/config"
)

// newReserveTestCtrl builds a multi-model video Ctrl whose single model names the
// given vendor, priced natively so no rate feed is involved.
func newReserveTestCtrl(t *testing.T, vendor videospec.Vendor) (*Ctrl, *gin.Context) {
	t.Helper()
	c := &Ctrl{logger: testLogger()}
	c.Service.Type = "video-generation"
	c.Service.ModelType = "vid-1"
	c.Service.ModelPricing = []config.ModelPricingEntry{{
		Model:       "vid-1",
		OutputPrice: "1000",
		Billing: &config.BillingConfig{
			Mode:   config.BillingModePerVideoSecond,
			Vendor: string(vendor),
		},
	}}
	if err := c.Service.BuildModelPricingMap(); err != nil {
		t.Fatalf("BuildModelPricingMap: %v", err)
	}
	ctx := &gin.Context{}
	ctx.Set(CtxKeyResolvedModel, "vid-1")
	ctx.Request = httptest.NewRequest("POST", "/videos", strings.NewReader(""))
	ctx.Request.Header.Set("Content-Type", "application/json")
	return c, ctx
}

// buildVideoMultipart writes a real multipart create body with a fixed boundary.
func buildVideoMultipart(t *testing.T, fields [][2]string) ([]byte, string) {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	if err := w.SetBoundary("testboundary"); err != nil {
		t.Fatalf("SetBoundary: %v", err)
	}
	for _, f := range fields {
		if err := w.WriteField(f[0], f[1]); err != nil {
			t.Fatalf("WriteField %s: %v", f[0], err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	return buf.Bytes(), w.FormDataContentType()
}

// TestRawVideoRequestFields_Multipart pins that the two billing fields come out
// of the wire VERBATIM.
//
// This reader must form no opinion about what a value means — that opinion is
// what a second, divergent reading is made of. Its only job is to hand the bytes
// to common/videospec, which knows how each vendor resolves them.
func TestRawVideoRequestFields_Multipart(t *testing.T) {
	tests := []struct {
		name        string
		fields      [][2]string
		wantSeconds string
		wantSize    string
	}{
		{
			name:        "plain values",
			fields:      [][2]string{{"model", "m"}, {"seconds", "8"}, {"size", "1280x720"}},
			wantSeconds: "8",
			wantSize:    "1280x720",
		},
		{
			name:        "absent fields read as empty, which the spec knows how to interpret",
			fields:      [][2]string{{"model", "m"}, {"prompt", "a cat"}},
			wantSeconds: "",
			wantSize:    "",
		},
		{
			// Padding is NOT stripped: the upstream's ParseFloat does not trim, so a
			// padded value is unreadable to it. Trimming here would resolve a
			// duration the vendor will not.
			name:        "padding survives, because the vendor's own reader does not trim",
			fields:      [][2]string{{"seconds", " 5 "}},
			wantSeconds: " 5 ",
		},
		{
			// The value the vendor reads is the FIRST one, so that is the one whose
			// rendered output must be priced.
			name:        "first value wins, matching the upstream's FormValue",
			fields:      [][2]string{{"seconds", "5"}, {"seconds", "15"}},
			wantSeconds: "5",
		},
		{
			name:        "garbage is passed through for the spec to reject",
			fields:      [][2]string{{"seconds", "abc"}},
			wantSeconds: "abc",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body, ct := buildVideoMultipart(t, tt.fields)
			seconds, size := rawVideoRequestFields(body, ct)
			if seconds != tt.wantSeconds {
				t.Errorf("seconds = %q, want %q", seconds, tt.wantSeconds)
			}
			if size != tt.wantSize {
				t.Errorf("size = %q, want %q", size, tt.wantSize)
			}
		})
	}
}

// TestRawVideoRequestFields_OversizedIsAbsentNotTruncated is the case that
// mattered: a value longer than the cap must read as ABSENT, never as its
// prefix.
//
// A truncated prefix that happens to parse is how a reader resolves a number the
// vendor never saw — and it resolves to a SMALLER one, which is the direction
// that loses money. "Absent" is a case every vendor's rules already define.
func TestRawVideoRequestFields_OversizedIsAbsentNotTruncated(t *testing.T) {
	// "5" followed by 400 zeros: any prefix of it parses as a number.
	oversized := "5" + string(bytes.Repeat([]byte("0"), 400))
	body, ct := buildVideoMultipart(t, [][2]string{{"seconds", oversized}, {"size", oversized}})

	seconds, size := rawVideoRequestFields(body, ct)
	if seconds != "" {
		t.Errorf("seconds = %q (len %d), want empty — a prefix of an oversized value must never be read as the duration", seconds, len(seconds))
	}
	if size != "" {
		t.Errorf("size = %q (len %d), want empty", size, len(size))
	}
}

// TestRawVideoRequestFields_FilePartIsNotAValue: the upstream's form reader does
// not see a file part as one of these fields either, so neither may this.
func TestRawVideoRequestFields_FilePartIsNotAValue(t *testing.T) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	_ = w.SetBoundary("testboundary")
	fw, err := w.CreateFormFile("seconds", "seconds.txt")
	if err != nil {
		t.Fatalf("CreateFormFile: %v", err)
	}
	if _, err := fw.Write([]byte("999")); err != nil {
		t.Fatalf("write file part: %v", err)
	}
	_ = w.WriteField("size", "720P")
	_ = w.Close()

	seconds, size := rawVideoRequestFields(buf.Bytes(), w.FormDataContentType())
	if seconds != "" {
		t.Errorf("seconds = %q, want empty — a file part is not a form value", seconds)
	}
	if size != "720P" {
		t.Errorf("size = %q, want 720P (the real value part after it must still be found)", size)
	}
}

func TestRawVideoRequestFields_JSON(t *testing.T) {
	tests := []struct {
		name        string
		body        string
		wantSeconds string
		wantSize    string
	}{
		{name: "number and string", body: `{"seconds":8,"size":"720P"}`, wantSeconds: "8", wantSize: "720P"},
		{name: "float survives unrounded", body: `{"seconds":7.2}`, wantSeconds: "7.2"},
		{name: "absent reads as empty", body: `{"model":"m"}`},
		{name: "null reads as empty", body: `{"seconds":null}`},
		// The upstream decodes this field into a json.Number, so a string fails its
		// decode outright and the request is rejected before anything is rendered.
		// Resolving a duration from it here would be reading a request the upstream
		// refuses — and such a request costs nothing, so it needs no reserve.
		{name: "a string seconds is not a number to the upstream either", body: `{"seconds":"8"}`},
		{name: "a non-object body yields nothing", body: `[1,2,3]`},
		{name: "malformed JSON yields nothing", body: `{"seconds":`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			seconds, size := rawVideoRequestFields([]byte(tt.body), "application/json")
			if seconds != tt.wantSeconds {
				t.Errorf("seconds = %q, want %q", seconds, tt.wantSeconds)
			}
			if size != tt.wantSize {
				t.Errorf("size = %q, want %q", size, tt.wantSize)
			}
		})
	}
}

// TestRawVideoRequestFields_LargeJSONSeedSurvives: reading these two fields must
// not disturb anything else, including a seed past 2^53 that a float64 decode
// would mangle. UseNumber + RawMessage keeps the rest of the body untouched.
func TestRawVideoRequestFields_LargeJSONSeedSurvives(t *testing.T) {
	seconds, _ := rawVideoRequestFields([]byte(`{"seed":9007199254740993,"seconds":5}`), "application/json")
	if seconds != "5" {
		t.Errorf("seconds = %q, want 5", seconds)
	}
}

// TestVideoCreateReserve_OutOfRangeSecondsIsRefused: a duration no vendor can
// resolve is refused at the gate, before the request is forwarded.
//
// The alternative is not "reserve nothing" — it is that every vendor's own
// fallback renders something and bills for it (MiniMax its longest clip,
// DashScope its default). Refusing costs nobody anything, and doing it here
// rather than at the translator saves the round trip.
func TestVideoCreateReserve_OutOfRangeSecondsIsRefused(t *testing.T) {
	for _, vendor := range []videospec.Vendor{videospec.VendorMiniMax, videospec.VendorDashScope} {
		c, ctx := newReserveTestCtrl(t, vendor)
		_, err := c.VideoCreateReserve(ctx, []byte(`{"seconds":1e30}`))
		if !errors.Is(err, ErrVideoSecondsOutOfRange) {
			t.Errorf("%s: err = %v, want ErrVideoSecondsOutOfRange", vendor, err)
		}
	}
}

// TestVideoBillingTier_SettlementAgreesWithTheGate is the case that made this
// function necessary.
//
// DashScope's poll response carries no resolution, so settlement falls back to
// the size the CLIENT sent. A client that sent pixel dimensions used to have
// "1792x1024" looked up in a price table keyed by 720P/1080P — a miss, billed at
// the baseline multiplier — while the gate had already held the 1080P amount for
// the very same request. The vendor renders 1080P either way, so the provider
// simply lost the difference.
func TestVideoBillingTier_SettlementAgreesWithTheGate(t *testing.T) {
	tests := []struct {
		name        string
		vendor      videospec.Vendor
		settledSize string
		want        string
	}{
		{
			name:   "dashscope: pixel dimensions become the tier the vendor rendered",
			vendor: videospec.VendorDashScope, settledSize: "1792x1024", want: "1080P",
		},
		{
			name:   "dashscope: a reported token passes through",
			vendor: videospec.VendorDashScope, settledSize: "720P", want: "720P",
		},
		{
			// MiniMax reports its real tier back, so this is already right — but it
			// must go through the same resolution, not a second code path.
			name:   "minimax: the reported tier is honoured",
			vendor: videospec.VendorMiniMax, settledSize: "1080P", want: "1080P",
		},
		{
			name:   "minimax: pixel dimensions fall to its single tier, as the vendor does",
			vendor: videospec.VendorMiniMax, settledSize: "1280x720", want: "2K",
		},
		{
			// Nothing determined a tier. "" is billed at the baseline either way, but
			// it records "unknown" rather than filing a pixel dimension as a price class.
			name:   "dashscope: an unreadable size determines no tier",
			vendor: videospec.VendorDashScope, settledSize: "garbage", want: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, ctx := newReserveTestCtrl(t, tt.vendor)
			if got := c.VideoBillingTier(ctx, tt.settledSize); got != tt.want {
				t.Errorf("VideoBillingTier(%q) = %q, want %q", tt.settledSize, got, tt.want)
			}
		})
	}
}

// TestVideoBillingTier_UnrecordedVendorKeepsTodaysBehaviour: a deployment whose
// vendor has no rules recorded must settle exactly as it does now. Changing what
// it bills would make this a pricing change for vendors nobody has looked at yet.
func TestVideoBillingTier_UnrecordedVendorKeepsTodaysBehaviour(t *testing.T) {
	c, ctx := newReserveTestCtrl(t, "no-such-vendor")
	if got := c.VideoBillingTier(ctx, "1792x1024"); got != "1792x1024" {
		t.Errorf("VideoBillingTier = %q, want the size unchanged", got)
	}

	// Single-model services have no per-model rules at all.
	single := &Ctrl{logger: testLogger()}
	single.Service.Type = "video-generation"
	if got := single.VideoBillingTier(&gin.Context{}, "1792x1024"); got != "1792x1024" {
		t.Errorf("single-model VideoBillingTier = %q, want the size unchanged", got)
	}
}

// TestWarnVideoDurationDrift covers the one thing that can still go wrong once
// the tier stopped being configurable: the recorded rules falling behind the
// vendor. MiniMax H3's floor moved from 5s to 4s once already, and nothing
// announces such a change.
//
// The negative cases matter more than the positive one. This fires on a gap
// nobody can close from configuration, so an alert that also fires when
// everything is fine would be worse than none at all.
func TestWarnVideoDurationDrift(t *testing.T) {
	count := func(t *testing.T, c *Ctrl, ctx *gin.Context, body string, billedSeconds int64) int {
		t.Helper()
		rec := &countingLogger{Logger: c.logger}
		c.logger = rec
		c.WarnVideoDurationDrift(ctx, []byte(body), "application/json", billedSeconds)
		return rec.errors
	}

	t.Run("the vendor rendered a length the rules did not predict", func(t *testing.T) {
		c, ctx := newReserveTestCtrl(t, videospec.VendorMiniMax)
		// Rules say 5; the vendor reports it rendered 8.
		if got := count(t, c, ctx, `{"seconds":5}`, 8); got != 1 {
			t.Errorf("logged %d lines for a duration mismatch, want 1", got)
		}
	})

	t.Run("agreement is silent", func(t *testing.T) {
		c, ctx := newReserveTestCtrl(t, videospec.VendorMiniMax)
		if got := count(t, c, ctx, `{"seconds":5}`, 5); got != 0 {
			t.Errorf("logged %d lines for a matching request, want 0", got)
		}
	})

	t.Run("a clamp the rules themselves applied is not drift", func(t *testing.T) {
		// Asking for 1 and being billed 4 is MiniMax's floor working exactly as
		// recorded — the gate reserved 4 too.
		c, ctx := newReserveTestCtrl(t, videospec.VendorMiniMax)
		if got := count(t, c, ctx, `{"seconds":1}`, 4); got != 0 {
			t.Errorf("logged %d lines for a correctly-predicted floor, want 0", got)
		}
	})

	t.Run("nothing was predicted, so nothing can have drifted", func(t *testing.T) {
		// DashScope omits an unreadable duration and picks the length itself, so
		// the gate never predicted one. Reporting here would put a permanent
		// baseline under an alert that means "the recorded rules are stale".
		c, ctx := newReserveTestCtrl(t, videospec.VendorDashScope)
		if got := count(t, c, ctx, `{"size":"720P"}`, 9); got != 0 {
			t.Errorf("logged %d lines when the vendor chose the length, want 0", got)
		}
	})

	t.Run("an unrecorded vendor has no rules to fall behind", func(t *testing.T) {
		c, ctx := newReserveTestCtrl(t, "no-such-vendor")
		if got := count(t, c, ctx, `{"seconds":5}`, 99); got != 0 {
			t.Errorf("logged %d lines for an unrecorded vendor, want 0", got)
		}
	})

	t.Run("a single-model service has no per-model rules at all", func(t *testing.T) {
		c := &Ctrl{logger: testLogger()}
		c.Service.Type = "video-generation"
		if got := count(t, c, &gin.Context{}, `{"seconds":5}`, 99); got != 0 {
			t.Errorf("logged %d lines for a single-model service, want 0", got)
		}
	})
}

// newTokenBilledReserveTestCtrl builds a Ctrl whose model bills per_video_token —
// the mode whose fee is a vendor-computed token count, so no reading of the
// request determines it.
//
// The vendor is one WITH rules recorded, deliberately: that is what makes this a
// test of the MODE rather than of a missing vendor. A vendor videospec knows
// nothing about would take VideoCreateReserve's unknown_vendor early return and
// never reach the token-billing branch at all, so it would pass whether or not
// that branch exists.
//
// This helper builds the lookup map directly (the real validator is
// package-private to config); that this config also LOADS is pinned separately by
// TestValidateModelPricing_TokenBilledModelLoads over in that package.
func newTokenBilledReserveTestCtrl(t *testing.T) (*Ctrl, *gin.Context) {
	t.Helper()
	c := &Ctrl{logger: testLogger()}
	c.Service.Type = "video-generation"
	c.Service.ModelType = "vid-1"
	c.Service.ModelPricing = []config.ModelPricingEntry{{
		Model:       "vid-1",
		OutputPrice: "1000",
		Billing: &config.BillingConfig{
			Mode:   config.BillingModePerVideoToken,
			Vendor: string(videospec.VendorMiniMax),
		},
	}}
	if err := c.Service.BuildModelPricingMap(); err != nil {
		t.Fatalf("BuildModelPricingMap: %v", err)
	}
	ctx := &gin.Context{}
	ctx.Set(CtxKeyResolvedModel, "vid-1")
	ctx.Request = httptest.NewRequest("POST", "/videos", strings.NewReader(""))
	ctx.Request.Header.Set("Content-Type", "application/json")
	return c, ctx
}

// TestVideoCreateReserve_PerVideoTokenWithoutEstimatorIsUnreservable: a
// token-billed model whose vendor publishes no rule for how the token count
// follows from the request cannot be bounded, so it keeps the metered skip.
//
// The helper's vendor is MiniMax deliberately: its rules are recorded (so the
// unknown_vendor path is not what fires) but it satisfies no TokenEstimator, which
// is exactly this case. The point of the test is the LOUDNESS, not the zero — a
// "0" fee is indistinguishable from a genuinely free request, so returning one
// silently would stop counting the exposure.
func TestVideoCreateReserve_PerVideoTokenWithoutEstimatorIsUnreservable(t *testing.T) {
	c, ctx := newTokenBilledReserveTestCtrl(t)
	rec := &countingLogger{Logger: c.logger}
	c.logger = rec

	fee, err := c.VideoCreateReserve(ctx, []byte(`{"seconds":8,"size":"1280x720"}`))
	if err != nil {
		t.Fatalf("VideoCreateReserve: %v", err)
	}
	if fee != "0" {
		t.Errorf("fee = %q, want %q — this vendor publishes no rule to bound its token count", fee, "0")
	}
	if rec.errors != 1 {
		t.Errorf("logged %d lines for an unreservable create, want 1", rec.errors)
	}
}

// TestVideoCreateReserve_PerVideoTokenStillRefusesUnpriceableSeconds: the early
// return for token billing must not swallow the one duration that is refused
// outright. A magnitude no clamp can honestly represent is still a client error —
// the vendor is never called and nobody pays for a clip nothing asked for.
func TestVideoCreateReserve_PerVideoTokenStillRefusesUnpriceableSeconds(t *testing.T) {
	c, ctx := newTokenBilledReserveTestCtrl(t)
	if _, err := c.VideoCreateReserve(ctx, []byte(`{"seconds":1e30}`)); !errors.Is(err, ErrVideoSecondsOutOfRange) {
		t.Errorf("err = %v, want ErrVideoSecondsOutOfRange", err)
	}
}

// TestVideoOutputUnits_ReservePathReportsNothing: the pre-flight gate calls the
// same unit math as settlement, with no vendor observation at all. It must produce
// no free-serve line — zero tokens is the normal state of the world there, not a
// response that failed to report them.
//
// The warning lives in warnIfTokenBillingObservedNothing, which only the paying
// settlement paths call. This test is what keeps it from drifting back in here.
func TestVideoOutputUnits_ReservePathReportsNothing(t *testing.T) {
	c, ctx := newTokenBilledReserveTestCtrl(t)
	rec := &countingLogger{Logger: c.logger}
	c.logger = rec

	if units := c.videoOutputUnits(ctx, 8, "720p"); units != 0 {
		t.Errorf("units = %d, want 0", units)
	}
	if units := c.videoOutputUnits(ctx, 8, "720p", 0); units != 0 {
		t.Errorf("units = %d, want 0", units)
	}
	if rec.errors != 0 {
		t.Errorf("logged %d lines; the unit math must not report on its own", rec.errors)
	}
}

// newEstimatedTokenReserveTestCtrl builds a token-billed model whose vendor DOES
// publish how its token count follows from the request (Seedance satisfies
// videospec.TokenEstimator), priced at 1 wei per token so a fee reads as a token
// count.
func newEstimatedTokenReserveTestCtrl(t *testing.T) (*Ctrl, *gin.Context) {
	t.Helper()
	c := &Ctrl{logger: testLogger()}
	c.Service.Type = "video-generation"
	c.Service.ModelType = "vid-1"
	c.Service.ModelPricing = []config.ModelPricingEntry{{
		Model:       "vid-1",
		OutputPrice: "1",
		Billing: &config.BillingConfig{
			Mode:   config.BillingModePerVideoToken,
			Vendor: string(videospec.VendorSeedance),
			// Both tiers at the full price, so a fee still reads as a token count
			// — this fixture is about the ESTIMATE, not about tier scaling.
			TokenPriceTiers: []config.VideoTokenPriceTier{
				{Resolution: "480p", Multiplier: 1},
				{Resolution: "720p", Multiplier: 1},
			},
		},
	}}
	if err := c.Service.BuildModelPricingMap(); err != nil {
		t.Fatalf("BuildModelPricingMap: %v", err)
	}
	ctx := &gin.Context{}
	ctx.Set(CtxKeyResolvedModel, "vid-1")
	ctx.Request = httptest.NewRequest("POST", "/videos", strings.NewReader(""))
	ctx.Request.Header.Set("Content-Type", "application/json")
	return c, ctx
}

// TestVideoCreateReserve_EstimatedTokenBilling: the whole point of the change. A
// token-billed create must now hold a NON-ZERO amount, quietly.
//
// Zero is the failure this closes, and it is not a rounding problem: the gate's
// other input (CalculateUnsettledFee) sums the fee stamped on in-flight rows, so a
// zero reserve means N simultaneous creates from one wallet each read 0 and every
// one of them passes on the same balance — the wallet ends up owing N clips it
// could never pay for. See 0gfoundation/0g-serving-broker#628.
func TestVideoCreateReserve_EstimatedTokenBilling(t *testing.T) {
	c, ctx := newEstimatedTokenReserveTestCtrl(t)
	rec := &countingLogger{Logger: c.logger}
	c.logger = rec

	// The published 720p rate (21,590 tokens/s) × 8s × 1 wei per token.
	fee, err := c.VideoCreateReserve(ctx, []byte(`{"seconds":8,"size":"1280x720"}`))
	if err != nil {
		t.Fatalf("VideoCreateReserve: %v", err)
	}
	if fee != "172720" {
		t.Errorf("fee = %q, want %q (8s × 21590 tokens/s × 1 wei)", fee, "172720")
	}
	// A bounded create is an ordinary priced create: no un-reserved-create line.
	if rec.errors != 0 {
		t.Errorf("logged %d lines for a create it could price, want 0", rec.errors)
	}
}

// TestVideoCreateReserve_EstimatedTokenBillingScalesWithTheRequest: the reserve must
// track what the request will actually cost, or a cheap 480p clip is gated as
// harshly as a 30-second 720p one and callers get refused for balance they do
// have.
func TestVideoCreateReserve_EstimatedTokenBillingScalesWithTheRequest(t *testing.T) {
	for _, tt := range []struct {
		name string
		body string
		want string
	}{
		{"480p is cheaper than 720p", `{"seconds":8,"size":"480p"}`, "77008"},             // 8 × 9626
		{"the ceiling clamps the estimate too", `{"seconds":99,"size":"720p"}`, "647700"}, // 30 × 21590
		{"below the floor estimates the floor", `{"seconds":1,"size":"720p"}`, "86360"},   // 4 × 21590
	} {
		t.Run(tt.name, func(t *testing.T) {
			c, ctx := newEstimatedTokenReserveTestCtrl(t)
			fee, err := c.VideoCreateReserve(ctx, []byte(tt.body))
			if err != nil {
				t.Fatalf("VideoCreateReserve: %v", err)
			}
			if fee != tt.want {
				t.Errorf("fee = %q, want %q", fee, tt.want)
			}
		})
	}
}

// TestVideoCreateReserve_EstimatedTokenBillingSkipsWhenUnestimable: a vendor WITH
// an estimator still has requests it cannot estimate (an unreadable duration is
// omitted and the vendor picks its own). That must fall back to the metered skip
// rather than to a zero fee that reads as free.
func TestVideoCreateReserve_EstimatedTokenBillingSkipsWhenUnestimable(t *testing.T) {
	c, ctx := newEstimatedTokenReserveTestCtrl(t)
	rec := &countingLogger{Logger: c.logger}
	c.logger = rec

	fee, err := c.VideoCreateReserve(ctx, []byte(`{"size":"720p"}`))
	if err != nil {
		t.Fatalf("VideoCreateReserve: %v", err)
	}
	if fee != "0" {
		t.Errorf("fee = %q, want %q", fee, "0")
	}
	if rec.errors != 1 {
		t.Errorf("logged %d lines for an unestimable create, want 1", rec.errors)
	}
}

// TestWarnVideoTokenEstimateDrift: the estimate comes from the vendor's published
// per-second rate, and what invalidates it is the vendor changing something it does
// not announce. That has no visible symptom — the gate just starts holding too
// little on every request — so this is the only thing that says so.
//
// The tolerance is the point of most of these cases. Comparing exactly would report
// the vendor's own rounding on every request and tell an operator nothing.
func TestWarnVideoTokenEstimateDrift(t *testing.T) {
	count := func(t *testing.T, body string, billedTokens int64) int {
		t.Helper()
		c, ctx := newEstimatedTokenReserveTestCtrl(t)
		rec := &countingLogger{Logger: c.logger}
		c.logger = rec
		c.WarnVideoTokenEstimateDrift(ctx, []byte(body), "application/json", billedTokens)
		return rec.errors
	}

	// 8s at 720p estimates 8 × 21,590 = 172,720.
	const estimate = 8 * 21590
	const body = `{"seconds":8,"size":"720p"}`

	t.Run("within the tolerance is silent", func(t *testing.T) {
		for _, tt := range []struct {
			name   string
			billed int64
		}{
			{"exactly the estimate", estimate},
			{"under the estimate", estimate / 2},
			// The vendor's price table rounds to three decimals, so a real bill can sit
			// a fraction of a percent above the estimate. Reporting that would be
			// reporting arithmetic.
			{"a fraction of a percent over", estimate + estimate/500},
			{"just inside the 15% tolerance", estimate + estimate*14/100},
		} {
			if got := count(t, body, tt.billed); got != 0 {
				t.Errorf("%s (billed %d vs estimate %d): logged %d lines, want 0", tt.name, tt.billed, estimate, got)
			}
		}
	})

	t.Run("past the tolerance is reported", func(t *testing.T) {
		for _, tt := range []struct {
			name   string
			billed int64
		}{
			// The things the check exists for: a frame-rate change is +25%, a tier whose
			// pixel count varies by aspect ratio is +31% to +78%.
			{"a frame-rate change (+25%)", estimate * 125 / 100},
			{"an aspect-ratio dependent tier (+31%)", estimate * 131 / 100},
			{"the worst case (+78%)", estimate * 178 / 100},
		} {
			if got := count(t, body, tt.billed); got != 1 {
				t.Errorf("%s (billed %d vs estimate %d): logged %d lines, want 1", tt.name, tt.billed, estimate, got)
			}
		}
	})

	t.Run("no estimate means nothing to drift from", func(t *testing.T) {
		if got := count(t, `{"size":"720p"}`, 999999); got != 0 {
			t.Errorf("logged %d lines when nothing was estimated, want 0", got)
		}
	})

	t.Run("a seconds-billed model is not this check's business", func(t *testing.T) {
		c, ctx := newReserveTestCtrl(t, videospec.VendorMiniMax)
		rec := &countingLogger{Logger: c.logger}
		c.logger = rec
		c.WarnVideoTokenEstimateDrift(ctx, []byte(`{"seconds":8,"size":"720P"}`), "application/json", 999999)
		if rec.errors != 0 {
			t.Errorf("logged %d lines for a per_video_second model, want 0", rec.errors)
		}
	})
}

// newTieredTokenPriceCtrl builds a Seedance-shaped model whose per-token price
// differs by tier: 720p at half the entry's outputPrice, 480p unpriced.
func newTieredTokenPriceCtrl(t *testing.T) (*Ctrl, *gin.Context, *config.BillingConfig) {
	t.Helper()
	billing := &config.BillingConfig{
		Mode:   config.BillingModePerVideoToken,
		Vendor: string(videospec.VendorSeedance),
		TokenPriceTiers: []config.VideoTokenPriceTier{
			{Resolution: "720p", Multiplier: 0.5},
		},
	}
	c := &Ctrl{logger: testLogger()}
	c.Service.Type = "video-generation"
	c.Service.ModelType = "vid-1"
	c.Service.ModelPricing = []config.ModelPricingEntry{{
		Model:       "vid-1",
		OutputPrice: "1000",
		Billing:     billing,
	}}
	if err := c.Service.BuildModelPricingMap(); err != nil {
		t.Fatalf("BuildModelPricingMap: %v", err)
	}
	ctx := &gin.Context{}
	ctx.Set(CtxKeyResolvedModel, "vid-1")
	ctx.Request = httptest.NewRequest("POST", "/videos", strings.NewReader(""))
	ctx.Request.Header.Set("Content-Type", "application/json")
	return c, ctx, billing
}

// TestVideoTokenUnitPrice pins the number the broker charges per token against
// the number GET /v1/models publishes for the same tier. They are the same call
// (config.ScaledUnitPrice) precisely so this cannot drift: a consumer computes
// unit_price × completion_tokens, and if the broker charged anything else, one
// side of that trade would be systematically wrong on every Seedance request.
func TestVideoTokenUnitPrice(t *testing.T) {
	c, _, billing := newTieredTokenPriceCtrl(t)

	if got := c.videoTokenUnitPrice(billing, "1000", "720p", true); got != "500" {
		t.Errorf("720p price = %q, want 500 (0.5 x 1000)", got)
	}
	// The published row for the same tier must be that same number.
	mult, ok := billing.TokenPriceMultiplier("720p")
	if !ok {
		t.Fatal("720p must be tabled")
	}
	published, ok := config.ScaledUnitPrice(big.NewInt(1000), mult)
	if !ok || published.String() != "500" {
		t.Errorf("published unit_price = %v, must equal the billed price", published)
	}

	// Every other mode is untouched, so callers can apply this unconditionally.
	perSecond := &config.BillingConfig{Mode: config.BillingModePerVideoSecond}
	if got := c.videoTokenUnitPrice(perSecond, "1000", "720p", true); got != "1000" {
		t.Errorf("per_video_second price = %q, want it untouched", got)
	}
	if got := c.videoTokenUnitPrice(nil, "1000", "720p", true); got != "1000" {
		t.Errorf("no billing block = %q, want outputPrice untouched", got)
	}
}

// TestVideoTokenUnitPrice_UntabledTierIsLoudAndConservative: an untabled tier
// bills the entry's UNSCALED outputPrice — the advertised ceiling, so never an
// underbill — and must say so. Silence would be the worst outcome: the consumer
// paying this broker has no row for that request either, so both sides are
// falling back and neither knows.
func TestVideoTokenUnitPrice_UntabledTierIsLoudAndConservative(t *testing.T) {
	c, _, billing := newTieredTokenPriceCtrl(t)
	rec := &countingLogger{Logger: c.logger}
	c.logger = rec

	if got := c.videoTokenUnitPrice(billing, "1000", "480p", true); got != "1000" {
		t.Errorf("untabled tier price = %q, want the unscaled 1000 — never below the table", got)
	}
	if rec.errors != 1 {
		t.Errorf("logged %d lines; an untabled tier must be reported exactly once — a silent fallback is how a pricing gap survives", rec.errors)
	}

	// The gate prices the same create and must NOT meter it again: the settlement
	// call above is the one that knows the request was billed.
	rec.errors = 0
	if got := c.videoTokenUnitPrice(billing, "1000", "480p", false); got != "1000" {
		t.Errorf("untabled tier price = %q, want the unscaled 1000 on the gate path too", got)
	}
	if rec.errors != 0 {
		t.Errorf("logged %d lines from the gate; one create must not be counted twice", rec.errors)
	}
}

// TestVideoCreateReserve_HoldsTheTierPrice: the gate must hold what settlement
// will charge. Both go through videoTokenUnitPrice, so a tier at half price
// holds half as much — a gate that kept holding the ceiling would refuse
// callers for balance they do have.
func TestVideoCreateReserve_HoldsTheTierPrice(t *testing.T) {
	c, ctx := newEstimatedTokenReserveTestCtrl(t)
	// 100 wei per token, not the fixture's 1: a half-price tier of a 1-wei price
	// is sub-wei, and ScaledUnitPrice clamps that to 1 rather than to free.
	c.Service.ModelPricing[0].OutputPrice = "100"
	c.Service.ModelPricing[0].Billing.TokenPriceTiers = []config.VideoTokenPriceTier{
		{Resolution: "480p", Multiplier: 1},
		{Resolution: "720p", Multiplier: 0.5},
	}
	if err := c.Service.BuildModelPricingMap(); err != nil {
		t.Fatalf("BuildModelPricingMap: %v", err)
	}

	// Same request as TestVideoCreateReserve_EstimatedTokenBilling (8s x 21590
	// tokens/s at 1 wei per token = 172720), now at the 720p half-price tier.
	fee, err := c.VideoCreateReserve(ctx, []byte(`{"seconds":8,"size":"1280x720"}`))
	if err != nil {
		t.Fatalf("VideoCreateReserve: %v", err)
	}
	if fee != "8636000" {
		t.Errorf("fee = %q, want 8636000 (172720 tokens x floor(0.5 x 100 wei))", fee)
	}
}
