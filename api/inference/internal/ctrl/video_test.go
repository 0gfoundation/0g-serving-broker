package ctrl

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/0glabs/0g-serving-broker/inference/config"
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

// TestVideoResponseFields_CompletionTokens pins the ByteDance Seedance billing
// signal: usage.completion_tokens is read independently of the duration
// fields (output_video_duration/duration/top-level seconds) and never
// confused with them.
func TestVideoResponseFields_CompletionTokens(t *testing.T) {
	tests := []struct {
		name     string
		respJSON string
		want     int64
	}{
		{
			name:     "usage.completion_tokens present",
			respJSON: `{"status":"completed","usage":{"completion_tokens":246840,"total_tokens":246840}}`,
			want:     246840,
		},
		{
			name:     "usage present but no completion_tokens (DashScope/MiniMax shape)",
			respJSON: `{"status":"completed","usage":{"output_video_duration":5}}`,
			want:     0,
		},
		{
			name:     "no usage block at all",
			respJSON: `{"status":"completed","seconds":5}`,
			want:     0,
		},
		{
			name:     "zero completion_tokens is not billed as a positive count",
			respJSON: `{"status":"completed","usage":{"completion_tokens":0}}`,
			want:     0,
		},
		{
			name:     "float-encoded completion_tokens tolerated",
			respJSON: `{"status":"completed","usage":{"completion_tokens":246840.0}}`,
			want:     246840,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var fields videoResponseFields
			if err := json.Unmarshal([]byte(tt.respJSON), &fields); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if got := fields.completionTokens(); got != tt.want {
				t.Errorf("completionTokens() = %d, want %d", got, tt.want)
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

// TestVideoOutputUnits_PerVideoToken pins the ByteDance Seedance billing path:
// the vendor-reported completion-token count is billed directly, ignoring
// seconds/size entirely, and the variadic completionTokens argument is a pure
// backward-compatible addition (every pre-existing 3-arg call site above
// keeps compiling and behaving unchanged).
func TestVideoOutputUnits_PerVideoToken(t *testing.T) {
	entry := config.ModelPricingEntry{
		Model:       "bytedance-seedance",
		OutputPrice: "1",
		Billing:     &config.BillingConfig{Mode: config.BillingModePerVideoToken},
	}
	c := &Ctrl{logger: testLogger(), Service: newMultiModelService(t, "NATIVE", []config.ModelPricingEntry{entry}, "bytedance-seedance")}

	if got := c.videoOutputUnits(ginCtxWithResolvedModel("bytedance-seedance"), 5, "1080p", 246840); got != 246840 {
		t.Errorf("per_video_token units = %d, want the vendor's completion_tokens (246840) passed straight through", got)
	}
	// Omitting the variadic arg entirely (as every DashScope/MiniMax call site
	// does) must not panic and must resolve to 0 tokens, not some seconds-based
	// guess — the mode's whole point is that seconds/size are irrelevant to it.
	if got := c.videoOutputUnits(ginCtxWithResolvedModel("bytedance-seedance"), 5, "1080p"); got != 0 {
		t.Errorf("per_video_token units with no completionTokens arg = %d, want 0", got)
	}
}

// TestVideoOutputUnits_PerVideoToken_ZeroTokensLogsLoudly pins that a
// per_video_token request resolving to 0 completion tokens — which a real
// completed Seedance task should never do, per the vendor's documented
// minimum-token floor — is NOT silently billed for free the way a genuine
// per_video_token=0 config value would be: it must log an error (mirroring
// the sibling "billing indeterminate" loud-failure convention elsewhere in
// this file), while still returning 0 units rather than guessing a seconds-
// based fee.
func TestVideoOutputUnits_PerVideoToken_ZeroTokensLogsLoudly(t *testing.T) {
	entry := config.ModelPricingEntry{
		Model:       "bytedance-seedance",
		OutputPrice: "1",
		Billing:     &config.BillingConfig{Mode: config.BillingModePerVideoToken},
	}
	c := &Ctrl{logger: testLogger(), Service: newMultiModelService(t, "NATIVE", []config.ModelPricingEntry{entry}, "bytedance-seedance")}
	rec := &countingLogger{Logger: c.logger}
	c.logger = rec

	if got := c.videoOutputUnits(ginCtxWithResolvedModel("bytedance-seedance"), 5, "1080p", 0); got != 0 {
		t.Errorf("units = %d, want 0", got)
	}
	if rec.errors != 1 {
		t.Errorf("logged %d errors for a zero-completion-tokens per_video_token request, want 1 (a silent free bill must not go unnoticed)", rec.errors)
	}

	// A genuine positive token count must NOT trip the same warning.
	rec.errors = 0
	if got := c.videoOutputUnits(ginCtxWithResolvedModel("bytedance-seedance"), 5, "1080p", 246840); got != 246840 {
		t.Errorf("units = %d, want 246840", got)
	}
	if rec.errors != 0 {
		t.Errorf("logged %d errors for a normal positive-token request, want 0", rec.errors)
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
