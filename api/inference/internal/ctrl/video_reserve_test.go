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
// given vendor and deployment tier, priced natively so no rate feed is involved.
func newReserveTestCtrl(t *testing.T, vendor videospec.Vendor, deploymentTier string) (*Ctrl, *gin.Context) {
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
		c, ctx := newReserveTestCtrl(t, vendor, "2K")
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
		name           string
		vendor         videospec.Vendor
		deploymentTier string
		settledSize    string
		want           string
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
			vendor: videospec.VendorMiniMax, deploymentTier: "2K", settledSize: "1080P", want: "1080P",
		},
		{
			name:   "minimax: pixel dimensions fall to the deployment tier, as the vendor does",
			vendor: videospec.VendorMiniMax, deploymentTier: "2K", settledSize: "1280x720", want: "2K",
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
			c, ctx := newReserveTestCtrl(t, tt.vendor, tt.deploymentTier)
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
	c, ctx := newReserveTestCtrl(t, "seedance", "720P")
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
