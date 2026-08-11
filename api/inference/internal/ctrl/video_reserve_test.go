package ctrl

import (
	"bytes"
	"errors"
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
			Vendor: string(videospec.VendorSeedance),
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

// TestVideoCreateReserve_PerVideoTokenIsUnpredictable: recording a vendor's rules
// does not make a token-billed model reservable. The duration and tier both
// resolve here, and the fee still does not follow from them.
//
// The point of the test is the LOUDNESS, not the zero. A "0" fee is
// indistinguishable from a genuinely free request, so returning one silently
// would be strictly worse than the unknown-vendor state recording the rules
// removed: the exposure would stop being counted.
func TestVideoCreateReserve_PerVideoTokenIsUnpredictable(t *testing.T) {
	c, ctx := newTokenBilledReserveTestCtrl(t)
	rec := &countingLogger{Logger: c.logger}
	c.logger = rec

	fee, err := c.VideoCreateReserve(ctx, []byte(`{"seconds":8,"size":"1280x720"}`))
	if err != nil {
		t.Fatalf("VideoCreateReserve: %v", err)
	}
	if fee != "0" {
		t.Errorf("fee = %q, want %q — a token count cannot be predicted from the request", fee, "0")
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

// TestVideoOutputUnits_ReservePathDoesNotReportServedFree: videoOutputUnits logs a
// free-serve error when a per_video_token request observed no tokens. The
// pre-flight gate calls the same function with no observation at all, where zero
// tokens is the normal state of the world — it must not be reported as a response
// that failed to report them, or every priced create emits a false alarm and
// meters a billing skip.
func TestVideoOutputUnits_ReservePathDoesNotReportServedFree(t *testing.T) {
	c, ctx := newTokenBilledReserveTestCtrl(t)
	rec := &countingLogger{Logger: c.logger}
	c.logger = rec

	// No variadic token count: the shape of every non-settlement caller.
	if units := c.videoOutputUnits(ctx, 8, "720p"); units != 0 {
		t.Errorf("units = %d, want 0", units)
	}
	if rec.errors != 0 {
		t.Errorf("logged %d lines with no observation to report on, want 0", rec.errors)
	}

	// A settlement caller that DID observe a response, and saw no tokens in it,
	// must still be reported — that is a real free serve.
	if units := c.videoOutputUnits(ctx, 8, "720p", 0); units != 0 {
		t.Errorf("units = %d, want 0", units)
	}
	if rec.errors != 1 {
		t.Errorf("logged %d lines for an observed zero-token response, want 1", rec.errors)
	}
}
