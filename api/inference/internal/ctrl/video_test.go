package ctrl

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/patrickmn/go-cache"

	"github.com/0glabs/0g-serving-broker/common/errors"
	"github.com/0glabs/0g-serving-broker/inference/config"
	"github.com/0glabs/0g-serving-broker/inference/model"
)

// multipartBody is a helper to build multipart/form-data body for tests.
func multipartBody(fields map[string]string) []byte {
	var body string
	for name, value := range fields {
		body += "--boundary\r\nContent-Disposition: form-data; name=\"" + name + "\"\r\n\r\n" + value + "\r\n"
	}
	body += "--boundary--"
	return []byte(body)
}

// ==========================================================================
// videoResponseFields JSON parsing (used by handleVideoGenerationResponse)
// ==========================================================================

func TestVideoResponseFieldsParsing(t *testing.T) {
	tests := []struct {
		name        string
		respJSON    string
		wantSeconds int64
		wantSize    string
		wantErr     bool
	}{
		{
			name:        "full response with seconds and size",
			respJSON:    `{"id":"video-001","status":"queued","seconds":8,"size":"1024x1792"}`,
			wantSeconds: 8,
			wantSize:    "1024x1792",
		},
		{
			name:        "seconds only, no size",
			respJSON:    `{"id":"video-001","status":"queued","seconds":5}`,
			wantSeconds: 5,
			wantSize:    "",
		},
		{
			name:        "seconds as string number",
			respJSON:    `{"id":"video-001","seconds":"10","size":"720x1280"}`,
			wantSeconds: 10,
			wantSize:    "720x1280",
		},
		{
			name:     "missing seconds field",
			respJSON: `{"id":"video-001","status":"queued","size":"720x1280"}`,
			wantErr:  true, // seconds will be 0, treated as invalid
		},
		{
			name:     "zero seconds",
			respJSON: `{"id":"video-001","seconds":0}`,
			wantErr:  true, // seconds <= 0 is invalid
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var fields videoResponseFields
			if err := json.Unmarshal([]byte(tt.respJSON), &fields); err != nil {
				t.Fatalf("unexpected unmarshal error: %v", err)
			}

			seconds, err := fields.Seconds.Int64()
			if tt.wantErr {
				if err == nil && seconds > 0 {
					t.Errorf("expected invalid seconds, got %d", seconds)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error parsing seconds: %v", err)
			}
			if seconds != tt.wantSeconds {
				t.Errorf("seconds = %d, want %d", seconds, tt.wantSeconds)
			}
			if fields.Size != tt.wantSize {
				t.Errorf("size = %q, want %q", fields.Size, tt.wantSize)
			}
		})
	}
}

// ==========================================================================
// parseVideoGenerationModel
// ==========================================================================

func TestParseVideoGenerationModel(t *testing.T) {
	tests := []struct {
		name    string
		reqBody []byte
		want    string
	}{
		{
			name:    "multipart with model field",
			reqBody: multipartBody(map[string]string{"model": "sora-2-pro", "prompt": "A cat"}),
			want:    "sora-2-pro",
		},
		{
			name:    "multipart without model field",
			reqBody: multipartBody(map[string]string{"prompt": "A cat"}),
			want:    "",
		},
		{
			name:    "empty body",
			reqBody: []byte{},
			want:    "",
		},
		{
			name:    "nil body",
			reqBody: nil,
			want:    "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseVideoGenerationModel(tt.reqBody)
			if got != tt.want {
				t.Errorf("parseVideoGenerationModel() = %q, want %q", got, tt.want)
			}
		})
	}
}

// ==========================================================================
// ensureMultipartWaitField
// ==========================================================================

func TestEnsureMultipartWaitField(t *testing.T) {
	tests := []struct {
		name     string
		reqBody  []byte
		wantWait string
	}{
		{
			name:     "no wait field — inserts wait=false",
			reqBody:  multipartBody(map[string]string{"model": "sora-2", "prompt": "A cat"}),
			wantWait: "false",
		},
		{
			name:     "wait=true already present — unchanged",
			reqBody:  multipartBody(map[string]string{"model": "sora-2", "wait": "true"}),
			wantWait: "true",
		},
		{
			name:     "wait=false already present — unchanged",
			reqBody:  multipartBody(map[string]string{"model": "sora-2", "wait": "false"}),
			wantWait: "false",
		},
		{
			name:     "empty body — returned as-is",
			reqBody:  []byte{},
			wantWait: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ensureMultipartWaitField(tt.reqBody)
			got := parseMultipartField(string(result), "wait")
			if got != tt.wantWait {
				t.Errorf("wait field = %q, want %q\nbody:\n%s", got, tt.wantWait, string(result))
			}
		})
	}
}

// ==========================================================================
// config.Service.GetVideoSizeRatio (delegates to ModelInfo.VideoSizeRatios)
// ==========================================================================

func TestGetVideoSizeRatio(t *testing.T) {
	tests := []struct {
		name         string
		customRatios map[string]float64
		size         string
		want         float64
	}{
		{"default 832x480", nil, "832x480", 0.5},
		{"default 480x832", nil, "480x832", 0.5},
		{"default 720x1280", nil, "720x1280", 1.0},
		{"default 1280x720", nil, "1280x720", 1.0},
		{"default 1024x1792", nil, "1024x1792", 2.0},
		{"default 1792x1024", nil, "1792x1024", 2.0},
		{"default unknown size", nil, "4096x2160", 1.0},
		{"custom ratio", map[string]float64{"720x1280": 1.0, "4k": 4.0}, "4k", 4.0},
		{"custom missing size", map[string]float64{"720x1280": 1.0}, "unknown", 1.0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := &config.Service{}
			if tt.customRatios != nil {
				svc.ModelInfo = &config.ModelInfo{VideoSizeRatios: tt.customRatios}
			}
			got := svc.GetVideoSizeRatio(tt.size)
			if got != tt.want {
				t.Errorf("GetVideoSizeRatio(%q) = %v, want %v", tt.size, got, tt.want)
			}
		})
	}
}

// TestResolveVideoBilling covers the P0 fix for video underbilling: billing
// prefers the upstream response's seconds/size, falls back to the client
// request when the upstream doesn't echo them (e.g. Alibaba Wan2.7), and
// reports ok=false only when neither source has a positive duration (the
// caller then skips billing loudly + metered instead of serving free).
func TestResolveVideoBilling(t *testing.T) {
	const mpCT = "multipart/form-data; boundary=bnd"
	tests := []struct {
		name        string
		respBody    string
		reqBody     string
		contentType string
		wantSec     int64
		wantSize    string
		wantSource  string // "" means expect not-ok
	}{
		{
			name:     "response has seconds and size (preferred, actual output)",
			respBody: `{"seconds":8,"size":"1280x720"}`,
			reqBody:  `{"seconds":5,"size":"832x480"}`,
			wantSec:  8, wantSize: "1280x720", wantSource: videoSourceResponse,
		},
		{
			// Bailian Wan2.7 via an OpenAI-compatible shim: actual duration is in
			// usage.output_video_duration, not top-level seconds. We bill the ACTUAL
			// output (5 from usage), size borrowed from the request — source=response.
			name:     "response usage.output_video_duration is actual output",
			respBody: `{"output":{"video_url":"https://x/y.mp4"},"usage":{"output_video_duration":5}}`,
			reqBody:  `{"seconds":9,"size":"1024x1792"}`,
			wantSec:  5, wantSize: "1024x1792", wantSource: videoSourceResponse,
		},
		{
			// A shim may serialize the actual duration as a float ("7.5"). Int64()
			// errors on those; we must still bill the actual output (ceil) via the
			// response, not drop to the request.
			name:     "float actual duration billed via response (ceil)",
			respBody: `{"seconds":7.5,"size":"1280x720"}`,
			reqBody:  `{"seconds":3,"size":"832x480"}`,
			wantSec:  8, wantSize: "1280x720", wantSource: videoSourceResponse,
		},
		{
			name:     "float usage.output_video_duration billed via response",
			respBody: `{"usage":{"output_video_duration":5.0}}`,
			reqBody:  `{"seconds":9,"size":"1024x1792"}`,
			wantSec:  5, wantSize: "1024x1792", wantSource: videoSourceResponse,
		},
		{
			// Upstream reports NO duration at all → degraded fallback to requested.
			name:     "no response duration, fall back to requested (degraded)",
			respBody: `{"output":{"video_url":"https://x/y.mp4"}}`,
			reqBody:  `{"seconds":6,"size":"1280x720"}`,
			wantSec:  6, wantSize: "1280x720", wantSource: videoSourceRequest,
		},
		{
			name:     "response not JSON, fall back to requested (degraded)",
			respBody: `not-json`,
			reqBody:  `{"seconds":6,"size":"1280x720"}`,
			wantSec:  6, wantSize: "1280x720", wantSource: videoSourceRequest,
		},
		{
			// Production transport: /v1/videos is multipart/form-data, NOT JSON.
			// The request fallback must parse multipart, else Wan2.7-style upstreams
			// (200 without echoing seconds) bill nothing — the bug this guards.
			name:        "multipart request fallback (live transport)",
			respBody:    `{"output":{"video_url":"https://x/y.mp4"}}`,
			reqBody:     "--bnd\r\nContent-Disposition: form-data; name=\"seconds\"\r\n\r\n8\r\n--bnd\r\nContent-Disposition: form-data; name=\"size\"\r\n\r\n1280x720\r\n--bnd--\r\n",
			contentType: mpCT,
			wantSec:     8, wantSize: "1280x720", wantSource: videoSourceRequest,
		},
		{
			name:        "multipart request without seconds -> not ok (free-video guard)",
			respBody:    `{"output":{"video_url":"https://x/y.mp4"}}`,
			reqBody:     "--bnd\r\nContent-Disposition: form-data; name=\"model\"\r\n\r\nwan2.7\r\n--bnd--\r\n",
			contentType: mpCT,
			wantSource:  "",
		},
		{
			// Security: a prompt value embedding a fake name="seconds" must NOT be
			// mistaken for the real seconds field (substring-scan underbilling). The
			// strict MIME parser reads the genuine field (60), not the injected 1.
			name:        "multipart prompt-injection cannot spoof seconds",
			respBody:    `{"output":{"video_url":"https://x/y.mp4"}}`,
			reqBody:     "--bnd\r\nContent-Disposition: form-data; name=\"prompt\"\r\n\r\na cat name=\"seconds\"\r\n\r\n1\r\n--bnd\r\nContent-Disposition: form-data; name=\"seconds\"\r\n\r\n60\r\n--bnd\r\nContent-Disposition: form-data; name=\"size\"\r\n\r\n1280x720\r\n--bnd--\r\n",
			contentType: mpCT,
			wantSec:     60, wantSize: "1280x720", wantSource: videoSourceRequest,
		},
		{
			name:     "request omits size, borrow response size",
			respBody: `{"size":"1792x1024"}`,
			reqBody:  `{"seconds":4}`,
			wantSec:  4, wantSize: "1792x1024", wantSource: videoSourceRequest,
		},
		{
			name:       "neither has positive seconds -> not ok (free-video guard)",
			respBody:   `{"size":"1280x720"}`,
			reqBody:    `{"prompt":"a cat"}`,
			wantSource: "",
		},
		{
			name:       "zero seconds is not billable",
			respBody:   `{"seconds":0}`,
			reqBody:    `{"seconds":0}`,
			wantSource: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sec, size, source := resolveVideoBilling([]byte(tt.respBody), []byte(tt.reqBody), tt.contentType)
			if source != tt.wantSource {
				t.Fatalf("source = %q, want %q", source, tt.wantSource)
			}
			if tt.wantSource == "" {
				return
			}
			if sec != tt.wantSec {
				t.Errorf("seconds = %d, want %d", sec, tt.wantSec)
			}
			if size != tt.wantSize {
				t.Errorf("size = %q, want %q", size, tt.wantSize)
			}
		})
	}
}

// TestVideoOutputCount pins the effective-count rounding (ceil, floored at 1).
func TestVideoOutputCount(t *testing.T) {
	cases := []struct {
		seconds int64
		ratio   float64
		want    int64
	}{
		{5, 1.0, 5},
		{5, 2.25, 12}, // ceil(11.25)
		{8, 0.5, 4},
		{1, 0.0, 1}, // floored at 1
		{0, 2.0, 1}, // floored at 1
	}
	for _, c := range cases {
		if got := videoOutputCount(c.seconds, c.ratio); got != c.want {
			t.Errorf("videoOutputCount(%d, %v) = %d, want %d", c.seconds, c.ratio, got, c.want)
		}
	}
}

// TestVideoOutputUnits_PerModelAndFallback covers the multi-model video billing
// wiring: a resolved model with a per_video_second billing block uses its own
// resolution ratios, while a single-model service falls back to the legacy
// service-level size-ratio path unchanged.
func TestVideoOutputUnits_PerModelAndFallback(t *testing.T) {
	videoEntry := config.ModelPricingEntry{
		Model:       "wan2.7",
		OutputPrice: "1000",
		Billing: &config.BillingConfig{
			Mode:                  config.BillingModePerVideoSecond,
			ResolutionMultipliers: map[string]float64{"1280x720": 1.0, "1920x1080": 2.25},
		},
	}
	svc := newMultiModelService(t, "NATIVE", []config.ModelPricingEntry{videoEntry}, "wan2.7")
	c := &Ctrl{logger: testLogger(), Service: svc}

	// Per-model ratio applies: ceil(5 * 2.25) = 12.
	if got := c.videoOutputUnits(ginCtxWithResolvedModel("wan2.7"), 5, "1920x1080"); got != 12 {
		t.Errorf("per-model units (1080p) = %d, want 12", got)
	}
	// Unknown resolution → baseline 1.0 within the entry's billing.
	if got := c.videoOutputUnits(ginCtxWithResolvedModel("wan2.7"), 7, "unknown"); got != 7 {
		t.Errorf("per-model units (unknown res) = %d, want 7", got)
	}

	// Single-model service → legacy service-ratio path (DefaultVideoSizeRatios:
	// 1024x1792 = 2.0), byte-for-byte unchanged.
	cs := &Ctrl{logger: testLogger(), Service: config.Service{}}
	if got := cs.videoOutputUnits(ginCtxWithResolvedModel(""), 5, "1024x1792"); got != 10 {
		t.Errorf("single-model fallback units = %d, want 10", got)
	}
}

// TestVideoOutputUnits_PerUnitTableMiss verifies a bucketed-model request for an
// unlisted (resolution, duration) stays inside the table — rounding up to the
// bucket that covers it, or the table MAX when none does — never the seconds×ratio
// formula (which would underbill, and which a client could force by requesting an
// untabled combo).
func TestVideoOutputUnits_PerUnitTableMiss(t *testing.T) {
	entry := config.ModelPricingEntry{
		Model:       "minimax-hailuo",
		OutputPrice: "100",
		Billing: &config.BillingConfig{
			Mode: config.BillingModePerUnitTable,
			Table: []config.BillingUnitTier{
				{Resolution: "768P", Duration: 6, Units: 6},
				{Resolution: "1080P", Duration: 6, Units: 12}, // table max
			},
		},
	}
	c := &Ctrl{logger: testLogger(), Service: newMultiModelService(t, "NATIVE", []config.ModelPricingEntry{entry}, "minimax-hailuo")}

	// Exact bucket hit.
	if got := c.videoOutputUnits(ginCtxWithResolvedModel("minimax-hailuo"), 6, "768P"); got != 6 {
		t.Errorf("table hit (768P,6) = %d, want 6", got)
	}
	// Miss with NO bucket that covers it (duration 8 exceeds every 768P row): the
	// table max is the only conservative answer, and it must never fall to the
	// seconds-ratio underbill of ceil(8*1.0)=8.
	if got := c.videoOutputUnits(ginCtxWithResolvedModel("minimax-hailuo"), 8, "768P"); got != 12 {
		t.Errorf("uncovered miss = %d, want table-max 12 (never the seconds-ratio underbill)", got)
	}

	// Miss BELOW the smallest bucket: bill the cheapest bucket that covers it, not
	// the table max. This is the reachable one — a vendor whose minimum duration
	// shifts (MiniMax H3's floor moved 5 -> 4, which is also its default request
	// shape) drops the MOST COMMON request into a miss, and billing the table max
	// would charge a 4-second 768P clip at the 1080P rate. Rounding up to the next
	// bucket is what a bucketed price list means, and it is the price the client can
	// actually look up in /v1/models.
	if got := c.videoOutputUnits(ginCtxWithResolvedModel("minimax-hailuo"), 4, "768P"); got != 6 {
		t.Errorf("sub-bucket miss = %d, want the covering 768P bucket 6 — not the cross-resolution table max", got)
	}
	// The covering bucket is resolution-scoped: a 4s 1080P clip takes the 1080P row.
	if got := c.videoOutputUnits(ginCtxWithResolvedModel("minimax-hailuo"), 4, "1080P"); got != 12 {
		t.Errorf("sub-bucket miss at 1080P = %d, want 12", got)
	}
	// An untabulated RESOLUTION has no covering bucket either, however short the
	// clip — so it lands on the same table-max path as an over-long duration, not
	// on the seconds-ratio underbill. Worth pinning because the branch reads as if
	// it were about duration only.
	if got := c.videoOutputUnits(ginCtxWithResolvedModel("minimax-hailuo"), 4, "2160P"); got != 12 {
		t.Errorf("untabulated resolution = %d, want table-max 12", got)
	}
}

// TestVideoTableMissThrottleKeyIsNotClientMintable: every reason shares one 64-key
// memo, and overflow flushes it for ALL of them — so a table-miss key derived from
// the caller's own size/seconds would both un-throttle itself and take the
// routing-proof reasons down with it. The key must come from the configured table.
func TestVideoTableMissThrottleKeyIsNotClientMintable(t *testing.T) {
	entry := config.ModelPricingEntry{
		Model:       "minimax-hailuo",
		OutputPrice: "100",
		Billing: &config.BillingConfig{
			Mode:  config.BillingModePerUnitTable,
			Table: []config.BillingUnitTier{{Resolution: "768P", Duration: 6, Units: 6}},
		},
	}
	c := &Ctrl{logger: testLogger(), Service: newMultiModelService(t, "NATIVE", []config.ModelPricingEntry{entry}, "minimax-hailuo")}
	rec := &countingLogger{Logger: c.logger}
	c.logger = rec

	// next_bucket: every one of these rounds up to the same configured row, so the
	// caller's seconds must not turn one missing row into one line per request.
	for i := 0; i < maxProofSkipKeys*4; i++ {
		c.videoOutputUnits(ginCtxWithResolvedModel("minimax-hailuo"), int64(i%5)+1, "768P")
	}
	if rec.errors != 1 {
		t.Errorf("logged %d times for one missing row, want 1 — seconds is in the key", rec.errors)
	}

	// uncovered: size is free text echoed from the request, so a fresh one per
	// request must not mint a key either.
	for i := 0; i < maxProofSkipKeys*4; i++ {
		c.videoOutputUnits(ginCtxWithResolvedModel("minimax-hailuo"), 4, fmt.Sprintf("768P-%d", i))
	}
	if rec.errors != 2 {
		t.Errorf("logged %d times total, want 2 — size is in the key", rec.errors)
	}

	n := 0
	c.proofSkipLogged.Range(func(_, _ any) bool { n++; return true })
	if n != 2 {
		t.Errorf("memo holds %d entries for two missing rows, want 2", n)
	}
}

// TestEscapeVendorJobID pins what may reach the poll URL. The id is upstream-supplied
// and the URL carries the broker's injected vendor credentials, so a value that
// changes the URL's shape must not survive. Behind a translator EncodeJobID already
// rules those out; this covers the centralized vendor spoken to DIRECTLY, where
// isContractJobID only logs.
func TestEscapeVendorJobID(t *testing.T) {
	// Every id shape actually in use must be byte-identical — escaping must not
	// change the URL for any job that works today.
	for _, id := range []string{
		"v0_task-abc",                          // what our translator publishes
		"v1_0385dc795ff840739d5a1a7bc7f3e01d",  // ditto, UUID-compacted
		"0385dc79-5ff8-4073-9d5a-1a7bc7f3e01d", // DashScope, pre-tagging
		"425080991981768",                      // MiniMax
	} {
		if got := escapeVendorJobID(id); got != id {
			t.Errorf("escapeVendorJobID(%q) = %q — a working id's URL must not change", id, got)
		}
	}

	// PathEscape handles separators but leaves a bare dot segment live, and a live
	// ".." walks the vendor's URL instead of naming a task under it. The exact
	// output is asserted, not just "changed": double-encoding is the subtle part —
	// a single %2E is the classic bypass on a proxy that decodes before it
	// normalizes, and "" would be worse than "..", turning the vendor's item
	// endpoint into its collection endpoint.
	for _, tc := range []struct{ id, want string }{
		{"..", "%252E%252E"},
		{".", "%252E"},
	} {
		if got := escapeVendorJobID(tc.id); got != tc.want {
			t.Errorf("escapeVendorJobID(%q) = %q, want %q", tc.id, got, tc.want)
		}
	}
	for _, id := range []string{"a?b", "a#b", "a/b", "a b"} {
		got := escapeVendorJobID(id)
		if got == id {
			t.Errorf("escapeVendorJobID(%q) passed a URL metacharacter through unescaped", id)
		}
	}
}

// ==========================================================================
// Pre-flight reserve for a video create
// ==========================================================================

const reserveMultipartCT = "multipart/form-data; boundary=boundary"

// rawMultipartBody builds a body from ordered (name, value) pairs so a test can emit a
// REPEATED field — which map-keyed multipartBody cannot express, and which is the shape
// where this reader and a Starlette-style form parser disagree.
func rawMultipartBody(pairs ...[2]string) []byte {
	var body string
	for _, p := range pairs {
		body += "--boundary\r\nContent-Disposition: form-data; name=\"" + p[0] + "\"\r\n\r\n" + p[1] + "\r\n"
	}
	return []byte(body + "--boundary--")
}

func (s videoReserveDuration) String() string {
	switch s {
	case videoDurationPriced:
		return "priced"
	case videoDurationAbsent:
		return "absent"
	default:
		return "unpriceable"
	}
}

// TestVideoReserveSecondsSizeFromRequest asserts the CLASSIFICATION directly, not just
// "some error came back".
//
// That distinction is the whole point of the three states, and asserting only the outer
// error is how two bypasses hid through a full review round: on a service that publishes
// no default duration, `absent` and `unpriceable` both surface as an error, so a row that
// should have been `unpriceable` passed while actually being classified `absent` — a
// FUNDED state that prices the published default. Every `unpriceable` row below is a
// measured under-reserve, not a hypothetical.
func TestVideoReserveSecondsSizeFromRequest(t *testing.T) {
	tests := []struct {
		name        string
		reqBody     []byte
		contentType string
		wantSeconds int64
		wantSize    string
		wantState   videoReserveDuration
	}{
		{
			name:        "json duration and pixel size",
			reqBody:     []byte(`{"model":"MiniMax-H3","prompt":"a cat","seconds":6,"size":"1280x720"}`),
			wantSeconds: 6,
			wantSize:    "1280x720",
			wantState:   videoDurationPriced,
		},
		{
			// BYPASS: json.Unmarshal validates the whole input first, so one appended byte
			// populated NOTHING and the old guard fell through to the multipart reader,
			// which on a JSON content type reports the field absent. The translator decodes
			// with json.Decoder, which ignores trailing data and renders the full 15s.
			name:        "bypass: trailing byte must not hide the duration",
			reqBody:     []byte(`{"seconds":15,"size":"1280x720"}x`),
			wantSeconds: 15,
			wantSize:    "1280x720",
			wantState:   videoDurationPriced,
		},
		{
			name:        "trailing comma is tolerated like the upstream decoder",
			reqBody:     []byte(`{"seconds":15,"size":"1280x720"},`),
			wantSeconds: 15,
			wantSize:    "1280x720",
			wantState:   videoDurationPriced,
		},
		{
			name:        "trailing second object is tolerated",
			reqBody:     []byte("{\"seconds\":15,\"size\":\"1280x720\"}\n{\"seconds\":1}"),
			wantSeconds: 15,
			wantSize:    "1280x720",
			wantState:   videoDurationPriced,
		},
		{
			// Unknown siblings are ignored outright now that the reserve decodes key-wise
			// rather than through the response struct.
			name:        "response-shaped siblings do not disturb the duration",
			reqBody:     []byte(`{"seconds":15,"size":"1280x720","usage":0,"status":0,"id":123}`),
			wantSeconds: 15,
			wantSize:    "1280x720",
			wantState:   videoDurationPriced,
		},
		{
			name:        "string-encoded duration is priced",
			reqBody:     []byte(`{"seconds":"15"}`),
			wantSeconds: 15,
			wantState:   videoDurationPriced,
		},
		{
			name:        "fractional duration rounds up",
			reqBody:     []byte(`{"seconds":4.1}`),
			wantSeconds: 5,
			wantState:   videoDurationPriced,
		},
		{
			name:        "wrong-typed size degrades only the size",
			reqBody:     []byte(`{"seconds":15,"size":1080}`),
			wantSeconds: 15,
			wantState:   videoDurationPriced,
		},
		{
			name:      "omitted duration keeps the size",
			reqBody:   []byte(`{"prompt":"a cat","size":"1024x1792"}`),
			wantSize:  "1024x1792",
			wantState: videoDurationAbsent,
		},
		{
			// An explicit null is a client saying "unset" — the upstream treats it the same
			// as omitting the key, so it must not be refused.
			name:      "explicit null duration keeps the size",
			reqBody:   []byte(`{"seconds":null,"size":"1024x1792"}`),
			wantSize:  "1024x1792",
			wantState: videoDurationAbsent,
		},
		{
			// BYPASS: ceilSeconds refuses anything above maxVideoOutputUnits, and the
			// translator clamps the same value DOWN to the model maximum and bills it.
			name:      "bypass: out-of-range duration is unpriceable",
			reqBody:   []byte(`{"seconds":1e20}`),
			wantState: videoDurationUnpriceable,
		},
		{
			name:      "bypass: out-of-range duration plus trailing byte is still unpriceable",
			reqBody:   []byte(`{"seconds":1e20}x`),
			wantState: videoDurationUnpriceable,
		},
		{
			name:      "duration beyond float64 range is unpriceable",
			reqBody:   []byte(`{"seconds":1e400}`),
			wantState: videoDurationUnpriceable,
		},
		{
			// BYPASS: a wrong JSON TYPE is a hard decode failure for json.Number, so these
			// left the field empty and were classified absent — priced at the default while
			// a laxer upstream (pydantic, float("+6")) reads the real value.
			name:      "bypass: non-numeric string duration is unpriceable",
			reqBody:   []byte(`{"seconds":"abc"}`),
			wantState: videoDurationUnpriceable,
		},
		{
			name:      "bypass: space-padded string duration is unpriceable",
			reqBody:   []byte(`{"seconds":" 6 "}`),
			wantState: videoDurationUnpriceable,
		},
		{
			name:      "bypass: signed string duration is unpriceable",
			reqBody:   []byte(`{"seconds":"+6"}`),
			wantState: videoDurationUnpriceable,
		},
		{
			name:      "bypass: boolean duration is unpriceable",
			reqBody:   []byte(`{"seconds":true}`),
			wantState: videoDurationUnpriceable,
		},
		{
			name:      "bypass: array duration is unpriceable",
			reqBody:   []byte(`{"seconds":[15]}`),
			wantState: videoDurationUnpriceable,
		},
		{
			// And it must not take the size with it into a funded state.
			name:      "bypass: object duration with a dear size is unpriceable",
			reqBody:   []byte(`{"seconds":{"v":15},"size":"1024x1792"}`),
			wantState: videoDurationUnpriceable,
		},
		{
			name:      "zero duration is unpriceable",
			reqBody:   []byte(`{"seconds":0}`),
			wantState: videoDurationUnpriceable,
		},
		{
			name:      "negative duration is unpriceable",
			reqBody:   []byte(`{"seconds":-15}`),
			wantState: videoDurationUnpriceable,
		},
		{
			// BYPASS: encoding/json matches object keys onto struct fields case-INSENSITIVELY,
			// so the upstream reads this as a 15s duration while an exact-key lookup here read
			// it as absent — and absent is a funded state. Folding the lookup makes the two
			// agree, which is better than refusing: the request is priced at what will be
			// rendered.
			name:        "bypass: case-variant duration key is priced, not absent",
			reqBody:     []byte(`{"model":"m","Seconds":15}`),
			wantSeconds: 15,
			wantState:   videoDurationPriced,
		},
		{
			name:        "upper-case duration key is priced",
			reqBody:     []byte(`{"model":"m","SECONDS":15}`),
			wantSeconds: 15,
			wantState:   videoDurationPriced,
		},
		{
			// BYPASS: Go resolves competing variants by document order — the LAST wins, even
			// over an exact match — so there is no safe single reading from an unordered map.
			// Measured: this gate read 1, the upstream read 15.
			name:      "bypass: competing duration key variants are unpriceable",
			reqBody:   []byte(`{"seconds":1,"Seconds":15}`),
			wantState: videoDurationUnpriceable,
		},
		{
			name:        "case-variant size key is read, not dropped",
			reqBody:     []byte(`{"seconds":15,"Size":"1024x1792"}`),
			wantSeconds: 15,
			wantSize:    "1024x1792",
			wantState:   videoDurationPriced,
		},
		{
			name:      "bypass: competing size key variants are unpriceable",
			reqBody:   []byte(`{"seconds":15,"size":"832x480","SIZE":"1024x1792"}`),
			wantState: videoDurationUnpriceable,
		},
		{
			name:      "non-object json body is unpriceable",
			reqBody:   []byte(`[1,2]`),
			wantState: videoDurationUnpriceable,
		},
		{
			name:      "unparseable body is unpriceable",
			reqBody:   []byte(`not json at all`),
			wantState: videoDurationUnpriceable,
		},
		{
			name:      "empty body is absent",
			reqBody:   nil,
			wantState: videoDurationAbsent,
		},
		{
			name:        "multipart duration and size",
			reqBody:     multipartBody(map[string]string{"model": "MiniMax-H3", "seconds": "6", "size": "1024x1792"}),
			contentType: reserveMultipartCT,
			wantSeconds: 6,
			wantSize:    "1024x1792",
			wantState:   videoDurationPriced,
		},
		{
			name:        "multipart without a duration is absent, size preserved",
			reqBody:     rawMultipartBody([2]string{"model", "MiniMax-H3"}, [2]string{"size", "1024x1792"}),
			contentType: reserveMultipartCT,
			wantSize:    "1024x1792",
			wantState:   videoDurationAbsent,
		},
		{
			// BYPASS: the field reader caps a part at 1024 bytes, so a padded value read as
			// empty while the upstream's form parser (no cap, and it trims) read the real
			// one.
			name: "bypass: padded multipart duration is unpriceable",
			reqBody: rawMultipartBody(
				[2]string{"model", "MiniMax-H3"},
				[2]string{"seconds", strings.Repeat(" ", 2000) + "15"},
			),
			contentType: reserveMultipartCT,
			wantState:   videoDurationUnpriceable,
		},
		{
			// Same cap, same giveaway, one field over.
			name: "bypass: padded multipart size is unpriceable",
			reqBody: rawMultipartBody(
				[2]string{"seconds", "15"},
				[2]string{"size", strings.Repeat(" ", 2000) + "1024x1792"},
			),
			contentType: reserveMultipartCT,
			wantState:   videoDurationUnpriceable,
		},
		{
			// BYPASS: this reader takes the FIRST repeated part; Starlette/FastAPI take the
			// LAST. Pricing either one lets a caller reserve 1 and be rendered 15.
			name: "bypass: repeated multipart duration is unpriceable",
			reqBody: rawMultipartBody(
				[2]string{"seconds", "1"},
				[2]string{"seconds", "15"},
			),
			contentType: reserveMultipartCT,
			wantState:   videoDurationUnpriceable,
		},
		{
			name: "bypass: repeated multipart size is unpriceable",
			reqBody: rawMultipartBody(
				[2]string{"seconds", "15"},
				[2]string{"size", "832x480"},
				[2]string{"size", "1024x1792"},
			),
			contentType: reserveMultipartCT,
			wantState:   videoDurationUnpriceable,
		},
		{
			// BYPASS: a malformed part BEFORE the field aborted the walk, which used to read
			// as "field absent".
			name:        "bypass: unwalkable multipart body is unpriceable",
			reqBody:     []byte("--boundary\r\nbroken-header-line\r\n\r\nx\r\n--boundary\r\nContent-Disposition: form-data; name=\"seconds\"\r\n\r\n15\r\n--boundary--"),
			contentType: reserveMultipartCT,
			wantState:   videoDurationUnpriceable,
		},
		{
			// BYPASS: the JSON arm guarded `seconds` and `size` but not `model`, so
			// `{"model":"cheap","Model":"dear"}` priced (and settled) the cheap model while the
			// translator's folded decode rendered the dear one.
			name:      "bypass: competing json model spellings are unpriceable",
			reqBody:   []byte(`{"model":"cheap","Model":"dear","seconds":6,"size":"1080P"}`),
			wantState: videoDurationUnpriceable,
		},
		{
			// `model` selects the price, so it is in the same refuse-on-ambiguity set as the
			// duration and the size.
			name: "bypass: repeated multipart model is unpriceable",
			reqBody: rawMultipartBody(
				[2]string{"seconds", "6"},
				[2]string{"model", "cheap"},
				[2]string{"model", "expensive"},
			),
			contentType: reserveMultipartCT,
			wantState:   videoDurationUnpriceable,
		},
		{
			name: "bypass: padded multipart model is unpriceable",
			reqBody: rawMultipartBody(
				[2]string{"seconds", "6"},
				[2]string{"model", strings.Repeat(" ", 2000) + "expensive"},
			),
			contentType: reserveMultipartCT,
			wantState:   videoDurationUnpriceable,
		},
		{
			// A multipart content type with no boundary cannot be walked either.
			name:        "multipart content type without a boundary is unpriceable",
			reqBody:     multipartBody(map[string]string{"seconds": "15"}),
			contentType: "multipart/form-data",
			wantState:   videoDurationUnpriceable,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ct := tt.contentType
			if ct == "" {
				ct = "application/json"
			}
			seconds, size, state := videoReserveSecondsSizeFromRequest(tt.reqBody, ct)
			if state != tt.wantState {
				t.Fatalf("state = %s, want %s", state, tt.wantState)
			}
			if seconds != tt.wantSeconds {
				t.Errorf("seconds = %d, want %d", seconds, tt.wantSeconds)
			}
			if size != tt.wantSize {
				t.Errorf("size = %q, want %q", size, tt.wantSize)
			}
		})
	}
}

// TestVideoReserveUnitsFromRequest covers the unit arithmetic on top of the classifier,
// and the two failure classes the proxy attributes differently.
func TestVideoReserveUnitsFromRequest(t *testing.T) {
	// ModelInfo nil → config.DefaultVideoSizeRatios (1280x720 = 1.0, 1024x1792 = 2.0,
	// 832x480 = 0.5, anything unlisted = 1.0), and no published defaults.
	bare := &Ctrl{logger: testLogger(), Service: config.Service{}}
	// Publishes both defaults, the way a video service is expected to.
	published := &Ctrl{logger: testLogger(), Service: config.Service{
		ModelInfo: &config.ModelInfo{DefaultParameters: map[string]interface{}{"seconds": 4, "size": "1024x1792"}},
	}}

	t.Run("measured live case: 5s at an unlisted tier takes the baseline", func(t *testing.T) {
		// 5s at "2K" billed 5 units × outputPrice (6.698 0G against a 1.0 0G lock).
		assertUnits(t, bare, `{"seconds":5,"size":"2K"}`, 5)
	})
	t.Run("above-baseline ratio scales up", func(t *testing.T) {
		assertUnits(t, bare, `{"seconds":5,"size":"1024x1792"}`, 10)
	})
	t.Run("bypass: below-baseline ratio is clamped, not honoured", func(t *testing.T) {
		// H3 renders only 2K (ratio 1.0), so honouring 0.5 reserved 8 against a 15 bill.
		assertUnits(t, bare, `{"seconds":15,"size":"832x480"}`, 15)
	})
	t.Run("omitted duration is priced at the published default", func(t *testing.T) {
		// 4 × ratio("1024x1792") = 8: the published size applies too.
		assertUnits(t, published, `{"prompt":"a cat"}`, 8)
	})
	t.Run("bypass: omitted size is priced at the published tier, not the baseline", func(t *testing.T) {
		// The upstream renders its configured tier and settlement bills THAT, so an
		// omitted size must not reserve the 1.0 baseline: 6 × 2.0.
		assertUnits(t, published, `{"seconds":6}`, 12)
	})
	t.Run("an explicit size still wins over the published default", func(t *testing.T) {
		assertUnits(t, published, `{"seconds":6,"size":"1280x720"}`, 6)
	})
	t.Run("a published default cannot rescue an unpriceable duration", func(t *testing.T) {
		if _, err := published.videoReserveUnitsFromRequest([]byte(`{"seconds":1e20}`), "application/json"); !errors.Is(err, ErrVideoSecondsUnpriceable) {
			t.Errorf("err = %v, want ErrVideoSecondsUnpriceable", err)
		}
	})
	t.Run("no published default is a broker-attributed refusal", func(t *testing.T) {
		// Client-fault sentinels would blame the caller for an operator config gap.
		_, err := bare.videoReserveUnitsFromRequest([]byte(`{"prompt":"a cat"}`), "application/json")
		if !errors.Is(err, ErrVideoDefaultDurationUnpublished) {
			t.Fatalf("err = %v, want ErrVideoDefaultDurationUnpublished", err)
		}
		if errors.Is(err, ErrVideoSecondsUnpriceable) {
			t.Error("an unpublished default must not be reported as an invalid `seconds`")
		}
	})
	t.Run("NaN ratio cannot slip past the 1.0 clamp", func(t *testing.T) {
		nan := &Ctrl{logger: testLogger(), Service: config.Service{
			ModelInfo: &config.ModelInfo{VideoSizeRatios: map[string]float64{"weird": math.NaN()}},
		}}
		// videoOutputCount floors NaN at 1 unit, so an unclamped NaN would reserve 1 for a
		// 15s clip.
		assertUnits(t, nan, `{"seconds":15,"size":"weird"}`, 15)
	})
}

func assertUnits(t *testing.T, c *Ctrl, body string, want int64) {
	t.Helper()
	got, err := c.videoReserveUnitsFromRequest([]byte(body), "application/json")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != want {
		t.Errorf("reserve units = %d, want %d", got, want)
	}
}

// TestVideoReserveUnits_PerModelTier covers the per-model half: a tier the model prices by
// name is honoured (GetVideoSizeRatio knows only pixel keys, so without this a caller names
// "1080P" and reserves the 1.0 baseline against a 2x bucket), while a size the table cannot
// price EXACTLY falls through to the service-ratio basis rather than videoOutputUnits'
// next-bucket / table-maximum handling — which as a reserve would refuse solvent callers.
//
// Note the direction of the gap this pins: the untabled-size row reserves 6 where a
// response rendering 1080P bills 12. Deliberate, and the reason residual #2 exists.
func TestVideoReserveUnits_PerModelTier(t *testing.T) {
	entry := config.ModelPricingEntry{
		Model:       "minimax-hailuo",
		OutputPrice: "100",
		Billing: &config.BillingConfig{
			Mode: config.BillingModePerUnitTable,
			Table: []config.BillingUnitTier{
				{Resolution: "768P", Duration: 6, Units: 6},
				{Resolution: "768P", Duration: 10, Units: 20},
				{Resolution: "1080P", Duration: 6, Units: 12},
				{Resolution: "1080P", Duration: 10, Units: 40},
			},
		},
		ModelInfo: &config.ModelInfo{DefaultParameters: map[string]interface{}{"seconds": 6, "size": "1080P"}},
	}
	c := &Ctrl{
		logger:  testLogger(),
		Service: newMultiModelService(t, "NATIVE", []config.ModelPricingEntry{entry}, "minimax-hailuo"),
	}

	// An exact row: the tier the client named by name is honoured (the service ratio alone
	// would have produced 6).
	assertUnits(t, c, `{"model":"minimax-hailuo","seconds":6,"size":"1080P"}`, 12)

	// A DURATION the table does not carry, at a resolution it does. Settlement rounds UP to
	// the covering bucket (NextBucketUnits), so the reserve must too — falling through to the
	// seconds x service-ratio basis reserved 7 units against a 40-unit bill.
	assertUnits(t, c, `{"model":"minimax-hailuo","seconds":7,"size":"1080P"}`, 40)
	assertUnits(t, c, `{"model":"minimax-hailuo","seconds":9,"size":"768P"}`, 20)

	// An omitted size takes the published tier, so it lands on a table row rather than the
	// baseline: 9s at 1080P rounds up to the 10s bucket.
	assertUnits(t, c, `{"model":"minimax-hailuo","seconds":9}`, 40)

	// A duration ABOVE every bucket for its resolution. Settlement bills the table MAXIMUM for
	// that case (nothing covers the observation), so the reserve must too — falling through to
	// the seconds x ratio basis reserved 12 units against a 40-unit bill.
	assertUnits(t, c, `{"model":"minimax-hailuo","seconds":12,"size":"768P"}`, 40)
	assertUnits(t, c, `{"model":"minimax-hailuo","seconds":15,"size":"1080P"}`, 40)

	// A size this model prices NOWHERE — including "1280x720", the OpenAI Video API's documented
	// shape. The published tier is what the upstream will render and what settlement will bill
	// from, so it is the reserve's basis: refusing here 503'd a conforming, solvent client with a
	// message claiming it had sent no size.
	assertUnits(t, c, `{"model":"minimax-hailuo","seconds":6,"size":"1280x720"}`, 12)

	// Only a model that publishes no usable tier either is refused, and then broker-attributed —
	// table units are not seconds (a 6s clip at 2K can be 60), so there is no scale to fall back
	// to, and publishing a usable default resolution is the operator's job.
	noDefault := &Ctrl{
		logger: testLogger(),
		Service: newMultiModelService(t, "NATIVE", []config.ModelPricingEntry{{
			Model:       "minimax-hailuo",
			OutputPrice: "100",
			Billing:     entry.Billing,
			ModelInfo:   &config.ModelInfo{DefaultParameters: map[string]interface{}{"seconds": 6}},
		}}, "minimax-hailuo"),
	}
	if _, err := noDefault.videoReserveUnitsFromRequest([]byte(`{"model":"minimax-hailuo","seconds":6,"size":"1280x720"}`), "application/json"); !errors.Is(err, ErrVideoDefaultSizeUnpublished) {
		t.Errorf("err = %v, want ErrVideoDefaultSizeUnpublished when neither the request nor the model names a priced tier", err)
	}

	// A model this service does not serve must be reported as such, not as an invalid
	// `seconds`: the allowlist's own check runs after this gate, so folding the two lost the
	// model_mismatch accounting that throttles name enumeration.
	if _, err := c.videoReserveUnitsFromRequest([]byte(`{"model":"not-a-model","prompt":"x"}`), "application/json"); !errors.Is(err, ErrVideoModelNotServed) {
		t.Errorf("err = %v, want ErrVideoModelNotServed", err)
	}
}

// TestVideoReserveNeverBelowSettlement is the invariant anyone actually cares about:
// nothing measures reserve-vs-settled in production (see the design doc's residual 4), so
// this is the only signal that the two sides of the money path agree.
//
// It pins the KNOWN-GOOD pairs. The documented residuals are deliberately absent: they are
// the pairs where this invariant does not hold yet.
func TestVideoReserveNeverBelowSettlement(t *testing.T) {
	perSecond := config.ModelPricingEntry{
		Model:       "minimax-hailuo",
		OutputPrice: "100",
		Billing: &config.BillingConfig{
			Mode:                  config.BillingModePerVideoSecond,
			ResolutionMultipliers: map[string]float64{"768P": 1.0, "1080P": 2.0},
		},
		ModelInfo: &config.ModelInfo{DefaultParameters: map[string]interface{}{"seconds": 4, "size": "768P"}},
	}
	perTable := config.ModelPricingEntry{
		Model:       "bucketed",
		OutputPrice: "100",
		Billing: &config.BillingConfig{
			Mode: config.BillingModePerUnitTable,
			Table: []config.BillingUnitTier{
				{Resolution: "768P", Duration: 6, Units: 6},
				{Resolution: "768P", Duration: 10, Units: 20},
				{Resolution: "1080P", Duration: 6, Units: 12},
				{Resolution: "1080P", Duration: 10, Units: 40},
			},
		},
		ModelInfo: &config.ModelInfo{DefaultParameters: map[string]interface{}{"seconds": 6, "size": "1080P"}},
	}
	c := &Ctrl{
		logger:  testLogger(),
		Service: newMultiModelService(t, "NATIVE", []config.ModelPricingEntry{perSecond, perTable}, "minimax-hailuo"),
	}

	cases := []struct {
		name           string
		model          string
		body           string
		renderedSize   string
		renderedSecond int64
	}{
		{name: "per_video_second: rendered tier equals the requested tier", model: "minimax-hailuo", body: `{"model":"minimax-hailuo","seconds":6,"size":"1080P"}`, renderedSize: "1080P", renderedSecond: 6},
		{name: "per_video_second: rendered duration shorter than requested", model: "minimax-hailuo", body: `{"model":"minimax-hailuo","seconds":15,"size":"768P"}`, renderedSize: "768P", renderedSecond: 10},
		{name: "per_video_second: size omitted, published tier rendered", model: "minimax-hailuo", body: `{"model":"minimax-hailuo","seconds":15}`, renderedSize: "768P", renderedSecond: 15},
		{name: "per_video_second: duration omitted, published default rendered", model: "minimax-hailuo", body: `{"model":"minimax-hailuo","size":"1080P"}`, renderedSize: "1080P", renderedSecond: 4},
		// The bucketed rows are what a duration-miss regression shows up on: settlement rounds
		// up to the covering bucket, so a reserve on the seconds x ratio basis is 5.7x low.
		{name: "per_unit_table: exact row", model: "bucketed", body: `{"model":"bucketed","seconds":6,"size":"1080P"}`, renderedSize: "1080P", renderedSecond: 6},
		{name: "per_unit_table: duration between buckets", model: "bucketed", body: `{"model":"bucketed","seconds":7,"size":"1080P"}`, renderedSize: "1080P", renderedSecond: 7},
		{name: "per_unit_table: duration between buckets at the cheap tier", model: "bucketed", body: `{"model":"bucketed","seconds":9,"size":"768P"}`, renderedSize: "768P", renderedSecond: 9},
		{name: "per_unit_table: size omitted, published tier rendered", model: "bucketed", body: `{"model":"bucketed","seconds":9}`, renderedSize: "1080P", renderedSecond: 9},
		{name: "per_unit_table: duration above every bucket", model: "bucketed", body: `{"model":"bucketed","seconds":12,"size":"768P"}`, renderedSize: "768P", renderedSecond: 12},
		{name: "per_unit_table: duration above every bucket at the dear tier", model: "bucketed", body: `{"model":"bucketed","seconds":15,"size":"1080P"}`, renderedSize: "1080P", renderedSecond: 15},
		{name: "per_unit_table: pixel size the table prices nowhere", model: "bucketed", body: `{"model":"bucketed","seconds":6,"size":"1280x720"}`, renderedSize: "1080P", renderedSecond: 6},
		// The async poll response reports usage.output_video_duration and NO size (see
		// translate.FromGetTaskResponse), so resolveVideoBilling falls back to the client's
		// verbatim size — the only path videoBillingSize exists for, and the one a hand-written
		// renderedSize can never exercise. Driven end to end below.
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reserve, err := c.videoReserveUnitsFromRequest([]byte(tc.body), "application/json")
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			settled := c.videoOutputUnits(ginCtxWithResolvedModel(tc.model), tc.renderedSecond, tc.renderedSize)
			if reserve < settled {
				t.Errorf("reserve %d < settled %d — the gate admitted a request it cannot cover", reserve, settled)
			}
		})
	}
}

// TestVideoCreateReserveFee covers the money function itself: the fee handed to the balance
// gate is units × the service output price, and a client-caused refusal is returned before
// any price lookup.
func TestVideoCreateReserveFee(t *testing.T) {
	c := &Ctrl{
		logger:       testLogger(),
		Service:      config.Service{},
		serviceCache: cache.New(time.Minute, time.Minute),
	}
	c.serviceCache.Set("current_service", model.Service{OutputPrice: "1000"}, cache.DefaultExpiration)

	fee, err := c.VideoCreateReserveFee(context.Background(), []byte(`{"seconds":6,"size":"1280x720"}`), "application/json")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fee != "6000" {
		t.Errorf("fee = %q, want %q", fee, "6000")
	}

	// The ratio clamp survives end-to-end through the fee, not just the unit helper.
	fee, err = c.VideoCreateReserveFee(context.Background(), []byte(`{"seconds":15,"size":"832x480"}`), "application/json")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fee != "15000" {
		t.Errorf("clamped fee = %q, want %q", fee, "15000")
	}

	// A refusal must come back BEFORE the price is fetched. Asserted on a Ctrl with an
	// empty cache and no contract: if the order ever flips this panics or returns a
	// different class, which is what the proxy's 400-vs-503 split depends on.
	bare := &Ctrl{logger: testLogger(), Service: config.Service{}, serviceCache: cache.New(time.Minute, time.Minute)}
	if _, err := bare.VideoCreateReserveFee(context.Background(), []byte(`{"seconds":1e20}`), "application/json"); !errors.Is(err, ErrVideoSecondsUnpriceable) {
		t.Errorf("err = %v, want ErrVideoSecondsUnpriceable before any price lookup", err)
	}
}

// TestMultipartFormFields covers the one-walk reader the reserve depends on. Each row is a
// shape where "the value this reader returns" and "the value the upstream's form parser
// returns" can differ, which is why the reserve needs repetition, truncation and
// walk-completion rather than just a string.
func TestMultipartFormFields(t *testing.T) {
	t.Run("value, presence and clean walk", func(t *testing.T) {
		fields, walkedOK := multipartFormFields(multipartBody(map[string]string{"seconds": "15"}), reserveMultipartCT, "seconds", "size")
		if !walkedOK {
			t.Fatal("walkedOK = false for a well-formed body")
		}
		f := fields["seconds"]
		if f.Truncated || len(f.Values) != 1 || f.Values[0] != "15" {
			t.Errorf("got %+v, want one value \"15\", untruncated", f)
		}
	})
	t.Run("absent field on a clean walk", func(t *testing.T) {
		fields, walkedOK := multipartFormFields(multipartBody(map[string]string{"model": "m"}), reserveMultipartCT, "seconds")
		if !walkedOK || len(fields["seconds"].Values) != 0 {
			t.Errorf("got %+v walkedOK=%v, want no values on a completed walk", fields["seconds"], walkedOK)
		}
	})
	t.Run("repeated field keeps every value in order", func(t *testing.T) {
		// This reader takes the first; Starlette/FastAPI take the last. Callers must see both
		// to be able to refuse.
		fields, _ := multipartFormFields(rawMultipartBody([2]string{"seconds", "1"}, [2]string{"seconds", "15"}), reserveMultipartCT, "seconds")
		if v := fields["seconds"].Values; len(v) != 2 || v[0] != "1" || v[1] != "15" {
			t.Errorf("values = %q, want [1 15]", v)
		}
	})
	t.Run("value past the cap is reported truncated", func(t *testing.T) {
		fields, _ := multipartFormFields(rawMultipartBody([2]string{"seconds", strings.Repeat("0", maxMultipartFieldBytes+10) + "15"}), reserveMultipartCT, "seconds")
		if !fields["seconds"].Truncated {
			t.Errorf("got %+v, want Truncated for a value past the cap", fields["seconds"])
		}
	})
	t.Run("unwalkable body reports walkedOK false", func(t *testing.T) {
		body := []byte("--boundary\r\nbroken-header-line\r\n\r\nx\r\n--boundary--")
		if _, walkedOK := multipartFormFields(body, reserveMultipartCT, "seconds"); walkedOK {
			t.Error("walkedOK = true for a malformed body")
		}
	})
	t.Run("missing boundary reports walkedOK false", func(t *testing.T) {
		if _, walkedOK := multipartFormFields(multipartBody(map[string]string{"seconds": "15"}), "multipart/form-data", "seconds"); walkedOK {
			t.Error("walkedOK = true without a boundary")
		}
	})
	t.Run("file parts are not values", func(t *testing.T) {
		body := []byte("--boundary\r\nContent-Disposition: form-data; name=\"seconds\"; filename=\"s.txt\"\r\n\r\n15\r\n--boundary--")
		fields, _ := multipartFormFields(body, reserveMultipartCT, "seconds")
		if len(fields["seconds"].Values) != 0 {
			t.Errorf("values = %q, want none for a file part", fields["seconds"].Values)
		}
	})
	t.Run("an unrequested name cannot be mistaken for an unwalkable body", func(t *testing.T) {
		// walkedOK is a property of the BODY and returned separately for this reason: a zero
		// multipartField for a name nobody asked for used to carry WalkOK=false, so reading it
		// looked exactly like "this body could not be parsed".
		fields, walkedOK := multipartFormFields(multipartBody(map[string]string{"seconds": "15"}), reserveMultipartCT, "seconds")
		if !walkedOK {
			t.Fatal("walkedOK = false for a well-formed body")
		}
		if f, asked := fields["size"]; asked || len(f.Values) != 0 {
			t.Errorf("unrequested name present in the result: %+v", f)
		}
	})
	t.Run("multipartFormField still returns the first value and stops early", func(t *testing.T) {
		if got := multipartFormField(rawMultipartBody([2]string{"model", "a"}, [2]string{"model", "b"}), reserveMultipartCT, "model"); got != "a" {
			t.Errorf("multipartFormField = %q, want %q", got, "a")
		}
		// It must not need a walkable remainder: the shared modalities (speech-to-text,
		// image-editing) read `model` from the head of a body whose tail is megabytes of
		// upload, and delegating to the full-walk reader cost ~34ms of CPU per request.
		head := rawMultipartBody([2]string{"model", "whisper-1"})
		truncated := append([]byte(nil), head[:len(head)-len("--boundary--")]...)
		if got := multipartFormField(truncated, reserveMultipartCT, "model"); got != "whisper-1" {
			t.Errorf("multipartFormField on an unterminated body = %q, want %q", got, "whisper-1")
		}
	})
}

// TestDefaultVideoParametersFor covers the published-metadata lookups that price an omitted
// duration and size. These make a field that used to be pure GET /v1/models documentation
// load-bearing for billing, so the coercions and both bounds are pinned.
func TestDefaultVideoParametersFor(t *testing.T) {
	seconds := []struct {
		name  string
		value interface{}
		want  int64
		ok    bool
	}{
		{name: "int", value: 5, want: 5, ok: true},
		{name: "int64", value: int64(8), want: 8, ok: true},
		{name: "float rounds up", value: 4.2, want: 5, ok: true},
		{name: "quoted", value: "6", want: 6, ok: true},
		{name: "quoted with spaces", value: " 6 ", want: 6, ok: true},
		{name: "at the ceiling", value: 3600, want: 3600, ok: true},
		// Below one second would reserve a single unit while the upstream applied its real
		// default (H3's floor is 4s) and billed that — the giveaway the default closes.
		{name: "below one second", value: 0.4, ok: false},
		{name: "zero", value: 0, ok: false},
		{name: "negative", value: -4, ok: false},
		{name: "above the ceiling", value: 3601, ok: false},
		{name: "config typo", value: 3600000, ok: false},
		{name: "non-numeric", value: "auto", ok: false},
		{name: "yaml bool", value: true, ok: false},
	}
	for _, tt := range seconds {
		t.Run("seconds/"+tt.name, func(t *testing.T) {
			svc := config.Service{ModelInfo: &config.ModelInfo{
				DefaultParameters: map[string]interface{}{"seconds": tt.value},
			}}
			got, ok := svc.DefaultVideoSecondsFor("any")
			if ok != tt.ok {
				t.Fatalf("ok = %v, want %v", ok, tt.ok)
			}
			if ok && got != tt.want {
				t.Errorf("seconds = %d, want %d", got, tt.want)
			}
		})
	}

	for _, svc := range []config.Service{
		{},
		{ModelInfo: &config.ModelInfo{}},
		{ModelInfo: &config.ModelInfo{DefaultParameters: map[string]interface{}{"size": "2K"}}},
	} {
		if _, ok := svc.DefaultVideoSecondsFor("any"); ok {
			t.Errorf("DefaultVideoSecondsFor() = ok for a service publishing no default duration")
		}
	}

	sizes := []struct {
		name  string
		value interface{}
		want  string
		ok    bool
	}{
		{name: "tier token", value: "2K", want: "2K", ok: true},
		{name: "trimmed", value: " 1080P ", want: "1080P", ok: true},
		{name: "empty", value: "   ", ok: false},
		{name: "not a string", value: 1080, ok: false},
	}
	for _, tt := range sizes {
		t.Run("size/"+tt.name, func(t *testing.T) {
			svc := config.Service{ModelInfo: &config.ModelInfo{
				DefaultParameters: map[string]interface{}{"size": tt.value},
			}}
			got, ok := svc.DefaultVideoSizeFor("any")
			if ok != tt.ok {
				t.Fatalf("ok = %v, want %v", ok, tt.ok)
			}
			if ok && got != tt.want {
				t.Errorf("size = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestVideoDefaultParametersValidatedAtLoad pins that a published-but-unusable duration
// fails STARTUP rather than traffic. At runtime it is indistinguishable from "unpublished",
// which refuses every create that omits `seconds` — an operator typo presenting as a client
// error on conforming requests.
func TestVideoDefaultParametersValidatedAtLoad(t *testing.T) {
	newInfo := func(seconds interface{}) *config.ModelInfo {
		mi := &config.ModelInfo{
			Name:                "v",
			Description:         "v",
			Architecture:        &config.ModelArchitecture{Modality: "text->video", InputModalities: []string{"text"}, OutputModalities: []string{"video"}},
			SupportedParameters: []string{"seconds"},
		}
		if seconds != nil {
			mi.DefaultParameters = map[string]interface{}{"seconds": seconds}
		}
		return mi
	}
	// ABSENT is not a boot error — loadConfig warns and the reserve refuses the create at request
	// time. Erroring here would refuse to start every existing video deployment that has not
	// added the field yet, which is a bigger call than this gate should make.
	if err := newInfo(nil).Validate("video-generation"); err != nil {
		t.Errorf("an absent default duration must not fail the boot: %v", err)
	}
	// A blank `seconds:` in YAML decodes to nil, which is "published nothing" — the same state as
	// omitting the key, not a typo to fail the boot on.
	mi := newInfo(nil)
	mi.DefaultParameters = map[string]interface{}{"seconds": nil}
	if err := mi.Validate("video-generation"); err != nil {
		t.Errorf("a blank `seconds:` must be treated as unpublished, not a typo: %v", err)
	}
	if err := newInfo(4).Validate("video-generation"); err != nil {
		t.Errorf("a usable published default must load: %v", err)
	}
	for _, bad := range []interface{}{0, 3601, "auto", true, 0.4, math.NaN()} {
		if err := newInfo(bad).Validate("video-generation"); err == nil {
			t.Errorf("defaultParameters.seconds = %v must fail config load", bad)
		}
	}
	// A present-but-unusable `size` must fail too: it degrades SILENTLY at runtime (a YAML
	// `size: 1080` decodes as an int, reports unpublished, and the reserve drops to the
	// baseline ratio with no error, log or metric).
	for _, badSize := range []interface{}{1080, "", "   ", true} {
		mi := newInfo(4)
		mi.DefaultParameters["size"] = badSize
		if err := mi.Validate("video-generation"); err == nil {
			t.Errorf("defaultParameters.size = %v must fail config load", badSize)
		}
	}

	// Non-video services are unaffected: the field is pure /v1/models metadata there.
	chat := newInfo("auto")
	chat.ContextLength = 4096
	if err := chat.Validate("chatbot"); err != nil {
		t.Errorf("a chatbot model must not be gated on video defaults: %v", err)
	}
}

// TestVideoReserveRequestModel pins that the model the reserve prices against is the model the
// upstream will actually be asked for.
//
// The gate and the translator must read it with the same parser. ExtractModelName used
// json.Unmarshal (whole-input validation) while the translator uses a json.Decoder (which
// ignores trailing data), so one appended byte made the gate read no model at all and fall back
// to the configured default: the reserve priced the default model, ResolveModelForBilling's
// allowlist passed a model the caller never named, settlement billed the default's price, and
// the upstream rendered the expensive one.
func TestVideoReserveRequestModel(t *testing.T) {
	tests := []struct {
		name        string
		body        string
		contentType string
		want        string
	}{
		{name: "named in json", body: `{"model":"expensive","seconds":6}`, want: "expensive"},
		{name: "named in json with trailing data", body: `{"model":"expensive","seconds":6}x`, want: "expensive"},
		{name: "named in json with a trailing object", body: `{"model":"expensive"}` + "\n{}", want: "expensive"},
		{name: "absent falls back to the configured model", body: `{"seconds":6}`, want: "configured"},
		{name: "unparseable falls back to the configured model", body: `not json`, want: "configured"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ct := tt.contentType
			if ct == "" {
				ct = "application/json"
			}
			if got := videoReserveRequestModel([]byte(tt.body), ct, "configured"); got != tt.want {
				t.Errorf("videoReserveRequestModel() = %q, want %q", got, tt.want)
			}
		})
	}

	t.Run("multipart", func(t *testing.T) {
		body := multipartBody(map[string]string{"model": "expensive", "seconds": "6"})
		if got := videoReserveRequestModel(body, reserveMultipartCT, "configured"); got != "expensive" {
			t.Errorf("videoReserveRequestModel() = %q, want %q", got, "expensive")
		}
	})
}

// TestExtractModelNameMatchesUpstreamReading pins that the model the broker reads is the model
// the upstream reads. Everything downstream keys on this answer — the video reserve's price, the
// allowlist in ResolveModelForBilling, settlement's per-model price, the metric label — so a
// disagreement means the broker describes a different request than the one served.
//
// The upstream (api/videotranslator/internal/handler/video.go) decodes into a struct with a
// json.Decoder: trailing data is ignored, and encoding/json matches keys onto struct fields
// case-insensitively.
func TestExtractModelNameMatchesUpstreamReading(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{name: "exact key", body: `{"model":"expensive","seconds":6}`, want: "expensive"},
		// json.Unmarshal validates the whole input, so this returned "" and callers substituted
		// the configured default while the upstream rendered "expensive".
		{name: "trailing byte", body: `{"model":"expensive","seconds":6}x`, want: "expensive"},
		{name: "trailing object", body: `{"model":"expensive"}` + "\n{}", want: "expensive"},
		// Read by exact key this was invisible; the upstream reads it as the model.
		{name: "case-variant key", body: `{"Model":"expensive","seconds":6}`, want: "expensive"},
		{name: "upper-case key", body: `{"MODEL":"expensive"}`, want: "expensive"},
		// The EXACT spelling wins when present. Reporting "absent" here disabled the LoRA
		// ownership gate and the expiry gate, which short-circuit on an empty model name while
		// ValidateModelAllowlist read the exact key and admitted the adapter. The money path does
		// not rely on this tie-break: the video reserve refuses a body with competing spellings.
		{name: "competing variants, exact key wins", body: `{"model":"cheap","Model":"expensive"}`, want: "cheap"},
		// No exact key and no single folded match: nothing can be trusted, and no gate can be
		// fooled either — the allowlist also reads nothing, so the configured model is served.
		{name: "competing variants, no exact key", body: `{"Model":"a","MODEL":"b"}`, want: ""},
		{name: "absent", body: `{"seconds":6}`, want: ""},
		{name: "wrong-typed model degrades to absent", body: `{"model":123}`, want: ""},
		{name: "unparseable", body: `not json`, want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ExtractModelName([]byte(tt.body), "application/json"); got != tt.want {
				t.Errorf("ExtractModelName() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestReserveVideoSizeRatioNormalizes covers the reserve's ratio lookup. The normalization widening
// cannot move a bill — the reserve reads the REQUEST's size, settlement the RESPONSE's — and the
// per-model map is deliberately NOT folded in.
func TestReserveVideoSizeRatioNormalizes(t *testing.T) {
	// Single-model, default ratios (1024x1792 = 2.0) — the most common deployment.
	svc := config.Service{}
	for _, size := range []string{"1024x1792", "1024X1792", " 1024x1792", "1024x1792 "} {
		if got := svc.ReserveVideoSizeRatio("any", size); got != 2.0 {
			// One capital letter used to miss the 2.0 ratio and reserve half of what the vendor's
			// canonical echo settled at.
			t.Errorf("ReserveVideoSizeRatio(%q) = %v, want 2", size, got)
		}
	}
	// A modelInfo block with NO videoSizeRatios still falls back to the defaults — the shape this
	// repo's video example config ships. Branching on nil-ness left the fix inert for it.
	withInfo := config.Service{ModelInfo: &config.ModelInfo{}}
	if got := withInfo.ReserveVideoSizeRatio("any", "1024X1792"); got != 2.0 {
		t.Errorf("ratio with an empty videoSizeRatios map = %v, want the default 2", got)
	}
	// A per-model modelInfo.videoSizeRatios is NOT folded in: settlement prices a per-model entry
	// through entry.Billing and never reads that map, so honouring it here demanded 8x the real
	// fee and refused solvent callers. A ratio meant to price belongs in
	// entry.Billing.ResolutionMultipliers, which videoModelUnits already covers.
	perModel := newMultiModelService(t, "NATIVE", []config.ModelPricingEntry{{
		Model:       "dear",
		OutputPrice: "100",
		ModelInfo:   &config.ModelInfo{VideoSizeRatios: map[string]float64{"1080P": 8.0}},
	}}, "dear")
	if got := perModel.ReserveVideoSizeRatio("dear", "1080P"); got != 1.0 {
		t.Errorf("per-model display ratio = %v, want the baseline 1 (settlement never reads it)", got)
	}
	// Unknown sizes stay at the baseline.
	if got := svc.ReserveVideoSizeRatio("any", "2K"); got != 1.0 {
		t.Errorf("unlisted size ratio = %v, want 1", got)
	}
}

// TestVideoBillingSizeSubstitutesPublishedTier covers the settlement-side half of the size
// vocabulary problem: when the upstream omits `size`, resolveVideoBilling falls back to the
// REQUEST's, and a tier-keyed model prices a client shape like "1280x720" nowhere — so
// videoOutputUnits billed the table MAXIMUM, over-charging the caller and disagreeing with the
// reserve, which prices the published tier for exactly that case.
func TestVideoBillingSizeSubstitutesPublishedTier(t *testing.T) {
	entry := config.ModelPricingEntry{
		Model:       "bucketed",
		OutputPrice: "100",
		Billing: &config.BillingConfig{
			Mode: config.BillingModePerUnitTable,
			Table: []config.BillingUnitTier{
				{Resolution: "768P", Duration: 6, Units: 6},
				{Resolution: "1080P", Duration: 6, Units: 40},
			},
		},
		ModelInfo: &config.ModelInfo{DefaultParameters: map[string]interface{}{"seconds": 6, "size": "768P"}},
	}
	c := &Ctrl{logger: testLogger(), Service: newMultiModelService(t, "NATIVE", []config.ModelPricingEntry{entry}, "bucketed")}
	ctx := ginCtxWithResolvedModel("bucketed")

	// A tier the model prices nowhere becomes the published one.
	if got := c.videoBillingSize(ctx, "1280x720"); got != "768P" {
		t.Errorf("videoBillingSize(%q) = %q, want the published %q", "1280x720", got, "768P")
	}
	// A tier it does price is left alone, in any casing.
	for _, size := range []string{"1080P", "1080p"} {
		if got := c.videoBillingSize(ctx, size); got != size {
			t.Errorf("videoBillingSize(%q) = %q, want it unchanged", size, got)
		}
	}
	// And the reserve and the bill now agree on the substituted tier.
	reserve, err := c.videoReserveUnitsFromRequest([]byte(`{"model":"bucketed","seconds":6,"size":"1280x720"}`), "application/json")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	settled := c.videoOutputUnits(ctx, 6, c.videoBillingSize(ctx, "1280x720"))
	if reserve != settled {
		t.Errorf("reserve %d != settled %d for a client tier the model prices nowhere", reserve, settled)
	}
}

// TestVideoReserveNeverBelowSettlement_AsyncPollBasis drives the settlement basis the way the async
// poller does — through resolveVideoBilling on a response that reports a duration and NO size — and
// asserts the reserve still covers it.
//
// This is the assertion the invariant needed: every other case supplies renderedSize as a literal,
// so none of them exercises the request-size fallback, which is exactly where the reserve and the
// bill drifted apart (measured: reserve 6, async bill 60). translate.FromGetTaskResponse never sets
// Size, so on the async path this is not an edge case — it is every settlement.
func TestVideoReserveNeverBelowSettlement_AsyncPollBasis(t *testing.T) {
	entry := config.ModelPricingEntry{
		Model:       "bucketed",
		OutputPrice: "100",
		Billing: &config.BillingConfig{
			Mode: config.BillingModePerUnitTable,
			Table: []config.BillingUnitTier{
				{Resolution: "768P", Duration: 6, Units: 6},
				{Resolution: "1080P", Duration: 6, Units: 60},
			},
		},
		ModelInfo: &config.ModelInfo{DefaultParameters: map[string]interface{}{"seconds": 6, "size": "768P"}},
	}
	c := &Ctrl{logger: testLogger(), Service: newMultiModelService(t, "NATIVE", []config.ModelPricingEntry{entry}, "bucketed")}
	ctx := ginCtxWithResolvedModel("bucketed")

	for _, body := range []string{
		`{"model":"bucketed","seconds":6,"size":"1280x720"}`, // the OpenAI documented shape
		`{"model":"bucketed","seconds":6,"size":"2160P"}`,    // a tier this model prices nowhere
		`{"model":"bucketed","seconds":6}`,                   // size omitted
		`{"model":"bucketed","seconds":6,"size":"768P"}`,     // a tier it does price
	} {
		t.Run(body, func(t *testing.T) {
			reserve, err := c.videoReserveUnitsFromRequest([]byte(body), "application/json")
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			// Shaped like translate.FromGetTaskResponse: a duration, no size.
			pollBody := []byte(`{"id":"v1","status":"completed","usage":{"output_video_duration":6}}`)
			seconds, size, source := c.videoBillingBasis(ctx, pollBody, []byte(body), "application/json")
			if source == "" {
				t.Fatal("settlement resolved no basis")
			}
			settled := c.videoOutputUnits(ctx, seconds, size)
			if reserve < settled {
				t.Errorf("reserve %d < settled %d (basis size %q) — the gate admitted a request it cannot cover", reserve, settled, size)
			}
		})
	}
}

// TestExtractModelNameExactKeyMustBeUsable pins that the exact-key tie-break is gated on the value
// being usable, not merely present.
//
// This answer feeds two AUTHORIZATION gates — CheckLoRAOwnership, the only ownership check on a
// private fine-tuned adapter, and the model-expiry 410 — and both short-circuit on an empty model
// name. Winning the tie-break on presence and then failing to decode produced exactly the ""
// the preference exists to prevent.
func TestExtractModelNameExactKeyMustBeUsable(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{name: "usable exact key wins", body: `{"model":"cheap","Model":"dear"}`, want: "cheap"},
		{name: "wrong-typed exact key falls back to the folded match", body: `{"model":123,"Model":"ft-victim"}`, want: "ft-victim"},
		{name: "empty exact key falls back to the folded match", body: `{"model":"","Model":"ft-victim"}`, want: "ft-victim"},
		// No usable reading at all: both spellings are ambiguous, so nothing is claimed. The
		// allowlist also reads nothing here, so no gate can be fooled.
		{name: "two ambiguous variants", body: `{"Model":"a","MODEL":"b"}`, want: ""},
		{name: "wrong-typed sole key", body: `{"model":123}`, want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ExtractModelName([]byte(tt.body), "application/json"); got != tt.want {
				t.Errorf("ExtractModelName() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestVideoBillingBasisRespectsReportedSize pins that a resolution the UPSTREAM reported is never
// overwritten.
//
// resolveVideoBilling returns either the response's size (the vendor stating what it rendered —
// authoritative) or the request's (a client guess), and the substitution must only ever touch the
// second. Conflating them repriced a tier the vendor itself reported: an untabulated `size:"4K"` in a
// MiniMax poll response went from the table maximum plus a per_unit_table_uncovered alert to the
// cheap published default and silence.
func TestVideoBillingBasisRespectsReportedSize(t *testing.T) {
	entry := config.ModelPricingEntry{
		Model:       "bucketed",
		OutputPrice: "100",
		Billing: &config.BillingConfig{
			Mode: config.BillingModePerUnitTable,
			Table: []config.BillingUnitTier{
				{Resolution: "768P", Duration: 6, Units: 6},
				{Resolution: "1080P", Duration: 6, Units: 60},
			},
		},
		ModelInfo: &config.ModelInfo{DefaultParameters: map[string]interface{}{"seconds": 6, "size": "768P"}},
	}
	c := &Ctrl{logger: testLogger(), Service: newMultiModelService(t, "NATIVE", []config.ModelPricingEntry{entry}, "bucketed")}
	ctx := ginCtxWithResolvedModel("bucketed")
	reqBody := []byte(`{"model":"bucketed","seconds":6,"size":"1280x720"}`)

	// Response REPORTS a tier the table prices NOWHERE. Both readings are wrong in different
	// directions — billing it reaches the table maximum and over-charges the caller by the whole
	// tier spread for a gap in the operator's table, leaving it alone puts settlement above the
	// reserve, which priced the published tier. So it is substituted for the PRICE (agreeing with
	// the reserve) while the miss is still metered and logged, because the untabled tier is what
	// actually needs fixing.
	reported := []byte(`{"id":"v1","status":"completed","size":"4K","usage":{"output_video_duration":6}}`)
	if _, size, _ := c.videoBillingBasis(ctx, reported, reqBody, "application/json"); size != "768P" {
		t.Errorf("basis size = %q, want the published %q for an untabled reported tier", size, "768P")
	}
	// Response reports a tier the table DOES carry: left exactly as the upstream stated it — this
	// is the case where overwriting would have repriced a genuinely dearer rendered tier down.
	priced := []byte(`{"id":"v1","status":"completed","size":"1080P","usage":{"output_video_duration":6}}`)
	if _, size, _ := c.videoBillingBasis(ctx, priced, reqBody, "application/json"); size != "1080P" {
		t.Errorf("basis size = %q, want %q", size, "1080P")
	}
	// Response reports NOTHING: only then does the client's unpriceable size become the published
	// tier, which is what the reserve priced.
	silent := []byte(`{"id":"v1","status":"completed","usage":{"output_video_duration":6}}`)
	if _, size, _ := c.videoBillingBasis(ctx, silent, reqBody, "application/json"); size != "768P" {
		t.Errorf("basis size = %q, want the published %q", size, "768P")
	}
}

// TestVideoBillingBasisWildcardModel pins the substitution for a wildcard (`"*"`) pricing entry.
//
// videoBillingSize used to key the published-default lookup on entry.Model, which is `"*"` for a
// wildcard — a name ResolveRequestedModel refuses by design — so the substitution silently did
// nothing while the reserve, which asks with the REQUESTED name, applied it: reserve 6, async bill
// 60 on the same request.
func TestVideoBillingBasisWildcardModel(t *testing.T) {
	entry := config.ModelPricingEntry{
		Model:       config.ModelWildcard,
		OutputPrice: "100",
		Billing: &config.BillingConfig{
			Mode: config.BillingModePerUnitTable,
			Table: []config.BillingUnitTier{
				{Resolution: "768P", Duration: 6, Units: 6},
				{Resolution: "1080P", Duration: 6, Units: 60},
			},
		},
		ModelInfo: &config.ModelInfo{DefaultParameters: map[string]interface{}{"seconds": 6, "size": "768P"}},
	}
	c := &Ctrl{logger: testLogger(), Service: newMultiModelService(t, "NATIVE", []config.ModelPricingEntry{entry}, config.ModelWildcard)}
	ctx := ginCtxWithResolvedModel("anything-at-all")
	reqBody := []byte(`{"model":"anything-at-all","seconds":6,"size":"1280x720"}`)

	reserve, err := c.videoReserveUnitsFromRequest(reqBody, "application/json")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	seconds, size, source := c.videoBillingBasis(ctx, []byte(`{"status":"completed","usage":{"output_video_duration":6}}`), reqBody, "application/json")
	if source == "" {
		t.Fatal("settlement resolved no basis")
	}
	settled := c.videoOutputUnits(ctx, seconds, size)
	if reserve < settled {
		t.Errorf("reserve %d < settled %d (basis size %q) on a wildcard entry", reserve, settled, size)
	}
}

// TestVideoBillingBasisLeavesPerVideoSecondAlone pins that a per_video_second block is NOT
// substituted, and that its bills did not move.
//
// resolutionMultipliers ARE seconds multipliers there and answer a miss with the 1.0 baseline, which
// is directly comparable to the reserve's own clamped service-ratio basis. Substituting anyway
// raised bills: `{"seconds":5,"size":"1280x720"}` against {720p:1.0, 1080p:1.5} with a published
// 1080p default went from 5 units to 8.
func TestVideoBillingBasisLeavesPerVideoSecondAlone(t *testing.T) {
	entry := config.ModelPricingEntry{
		Model:       "tiered",
		OutputPrice: "100",
		Billing: &config.BillingConfig{
			Mode:                  config.BillingModePerVideoSecond,
			ResolutionMultipliers: map[string]float64{"720p": 1.0, "1080p": 1.5},
		},
		ModelInfo: &config.ModelInfo{DefaultParameters: map[string]interface{}{"seconds": 5, "size": "1080p"}},
	}
	c := &Ctrl{logger: testLogger(), Service: newMultiModelService(t, "NATIVE", []config.ModelPricingEntry{entry}, "tiered")}
	ctx := ginCtxWithResolvedModel("tiered")
	reqBody := []byte(`{"model":"tiered","seconds":5,"size":"1280x720"}`)

	seconds, size, _ := c.videoBillingBasis(ctx, []byte(`{"status":"completed","usage":{"output_video_duration":5}}`), reqBody, "application/json")
	if size != "1280x720" {
		t.Errorf("basis size = %q, want the client's %q left alone on a per_video_second model", size, "1280x720")
	}
	if got := c.videoOutputUnits(ctx, seconds, size); got != 5 {
		t.Errorf("settled units = %d, want 5 — the bill must not rise for a size this model prices at baseline", got)
	}
	// And an unlisted size is priced, not refused: the 503 is a per_unit_table-only answer.
	if _, err := c.videoReserveUnitsFromRequest(reqBody, "application/json"); err != nil {
		t.Errorf("a per_video_second model must price an unlisted size, not refuse it: %v", err)
	}
}

// TestVideoReserveCoversEchoedRequestSize pins the shape that reconciled the two competing rules:
// an upstream that ECHOES back a client size the model prices nowhere.
//
// "Never overwrite what the upstream reported" and "the reserve must cover the bill" collide there:
// leaving the echoed size alone sends settlement to the table maximum while the reserve priced the
// published tier. Neither live translator echoes a pixel size today (MiniMax reports its rendered
// tier, DashScope reports none), but the broker is vendor-agnostic, so the invariant has to hold for
// it.
func TestVideoReserveCoversEchoedRequestSize(t *testing.T) {
	entry := config.ModelPricingEntry{
		Model:       "bucketed",
		OutputPrice: "100",
		Billing: &config.BillingConfig{
			Mode: config.BillingModePerUnitTable,
			Table: []config.BillingUnitTier{
				{Resolution: "768P", Duration: 6, Units: 6},
				{Resolution: "1080P", Duration: 6, Units: 60},
			},
		},
		ModelInfo: &config.ModelInfo{DefaultParameters: map[string]interface{}{"seconds": 6, "size": "768P"}},
	}
	c := &Ctrl{logger: testLogger(), Service: newMultiModelService(t, "NATIVE", []config.ModelPricingEntry{entry}, "bucketed")}
	ctx := ginCtxWithResolvedModel("bucketed")

	for _, size := range []string{"1280x720", "2160P", "4K"} {
		t.Run(size, func(t *testing.T) {
			reqBody := []byte(`{"model":"bucketed","seconds":6,"size":"` + size + `"}`)
			reserve, err := c.videoReserveUnitsFromRequest(reqBody, "application/json")
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			respBody := []byte(`{"status":"completed","size":"` + size + `","usage":{"output_video_duration":6}}`)
			seconds, basis, source := c.videoBillingBasis(ctx, respBody, reqBody, "application/json")
			if source == "" {
				t.Fatal("settlement resolved no basis")
			}
			if settled := c.videoOutputUnits(ctx, seconds, basis); reserve < settled {
				t.Errorf("reserve %d < settled %d (basis %q) for an echoed unpriceable size", reserve, settled, basis)
			}
		})
	}
}
