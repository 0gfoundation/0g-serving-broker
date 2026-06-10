package ctrl

import (
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/0glabs/0g-serving-broker/common/errors"
	"github.com/0glabs/0g-serving-broker/common/util"
	"github.com/0glabs/0g-serving-broker/inference/config"
	"github.com/0glabs/0g-serving-broker/inference/model"
	"github.com/0glabs/0g-serving-broker/inference/monitor"
)

// parseMultipartField extracts a named field value from multipart/form-data body.
func parseMultipartField(bodyStr, fieldName string) string {
	pattern := `name="` + fieldName + `"`
	fieldStart := findSubstring(bodyStr, pattern)
	if fieldStart == -1 {
		return ""
	}

	valueStart := findSubstring(bodyStr[fieldStart:], "\r\n\r\n")
	if valueStart == -1 {
		valueStart = findSubstring(bodyStr[fieldStart:], "\n\n")
	}
	if valueStart == -1 {
		return ""
	}

	valueStart += fieldStart
	if bodyStr[valueStart] == '\r' {
		valueStart += 4
	} else {
		valueStart += 2
	}

	end := valueStart
	for end < len(bodyStr) {
		if bodyStr[end] == '\r' || bodyStr[end] == '\n' {
			break
		}
		end++
	}

	return strings.TrimSpace(bodyStr[valueStart:end])
}

// videoResponseFields holds the billing-relevant fields from a video generation
// response. seconds/size is the OpenAI-shaped top level; usage carries the
// actual output duration the way an OpenAI-compatible shim in front of an async
// vendor (e.g. Alibaba Wan2.7 → usage.output_video_duration) surfaces it. The
// same seconds/size shape is also the broker's request edge contract, so the
// struct doubles as the request-fallback parse — see resolveVideoBilling.
type videoResponseFields struct {
	Seconds json.Number `json:"seconds"`
	Size    string      `json:"size"`
	Usage   *videoUsage `json:"usage"`
}

// videoUsage is the optional usage block of a video response. output_video_duration
// is the canonical actual-output field (Wan2.7 / DashScope-style); duration is a
// common alias. Both are the ACTUAL generated length, which is what we bill on.
type videoUsage struct {
	OutputVideoDuration json.Number `json:"output_video_duration"`
	Duration            json.Number `json:"duration"`
}

// actualSeconds returns the upstream-reported ACTUAL output duration from the
// known response shapes (top-level seconds, then usage.output_video_duration,
// then usage.duration), or 0 when none is present. This is the authoritative
// billing basis — billing on the actual generated length, not the request.
func (f videoResponseFields) actualSeconds() int64 {
	if s, ok := ceilSeconds(f.Seconds); ok {
		return s
	}
	if f.Usage != nil {
		if s, ok := ceilSeconds(f.Usage.OutputVideoDuration); ok {
			return s
		}
		if s, ok := ceilSeconds(f.Usage.Duration); ok {
			return s
		}
	}
	return 0
}

// ceilSeconds parses a duration json.Number that may be integer- OR float-encoded
// (a JSON serializer / OpenAI-compatible shim may emit "5.0" or "7.5"), returning
// ceil(value) for a strictly-positive, finite, in-range value. json.Number.Int64()
// ERRORS on any float literal, which would silently drop a real actual-output
// duration and mis-bill — so parse as float and round up. Out-of-range guards
// against an absurd value overflowing the int64 conversion.
func ceilSeconds(n json.Number) (int64, bool) {
	f, err := n.Float64()
	if err != nil || !(f > 0) || math.IsInf(f, 0) || f > float64(maxVideoOutputUnits) {
		return 0, false
	}
	return int64(math.Ceil(f)), true
}

// Billing source for a resolved video duration. "response" is the upstream's
// actual output (authoritative); "request" is the requested duration — a
// DEGRADED fallback that can over-bill a partial generation, used only to avoid
// serving free when the upstream reports no duration at all.
const (
	videoSourceResponse = "response"
	videoSourceRequest  = "request"
)

// resolveVideoBilling picks the billable (seconds, size) for a video request and
// reports its source. It prefers the upstream RESPONSE's actual output duration
// (top-level seconds or a usage block — covering OpenAI-compatible shims over
// async vendors like Wan2.7), satisfying "bill actual output". Only when the
// upstream reports no duration does it fall back to the client request
// (videoSourceRequest), which bills the REQUESTED duration — the caller logs
// that as degraded. source is "" (and ok=false) only when neither yields a
// positive duration, in which case the caller skips billing loudly.
func resolveVideoBilling(respBody, reqBody []byte, contentType string) (seconds int64, size, source string) {
	var rf videoResponseFields
	_ = json.Unmarshal(respBody, &rf)
	// The request (multipart /v1/videos, occasionally JSON) supplies size when the
	// response omits it, and the duration only as a last-resort fallback.
	reqSec, reqSize := videoSecondsSizeFromRequest(reqBody, contentType)

	// Resolution: response's own size wins; else the requested size (baseline 1.0
	// when both empty). The resolution ratio is the same regardless of which
	// duration source we bill on.
	size = rf.Size
	if size == "" {
		size = reqSize
	}

	// Duration: the upstream's ACTUAL output (top-level seconds or usage) is
	// authoritative; only when it reports nothing do we bill the requested length.
	if s := rf.actualSeconds(); s > 0 {
		return s, size, videoSourceResponse
	}
	if reqSec > 0 {
		return reqSec, size, videoSourceRequest
	}
	return 0, "", ""
}

// videoSecondsSizeFromRequest extracts a positive integer `seconds` and the
// `size` from a video request body, handling both JSON and multipart/form-data
// (the live transport for /v1/videos). Returns (0, "") when no positive seconds
// is present in either shape.
//
// The multipart path uses a real MIME reader (multipartFormField), NOT a
// substring scan: these fields drive the fee, and a substring scan would let a
// client embed a fake name="seconds" inside the prompt value to underbill.
func videoSecondsSizeFromRequest(reqBody []byte, contentType string) (int64, string) {
	if len(reqBody) == 0 {
		return 0, ""
	}
	// JSON shape (float-tolerant: a client may send "seconds": 5.0).
	var qf videoResponseFields
	if json.Unmarshal(reqBody, &qf) == nil {
		if s, ok := ceilSeconds(qf.Seconds); ok {
			return s, qf.Size
		}
	}
	// multipart/form-data shape.
	secStr := multipartFormField(reqBody, contentType, "seconds")
	if secStr == "" {
		return 0, ""
	}
	f, err := strconv.ParseFloat(strings.TrimSpace(secStr), 64)
	if err != nil || !(f > 0) || math.IsInf(f, 0) || f > float64(maxVideoOutputUnits) {
		return 0, ""
	}
	return int64(math.Ceil(f)), multipartFormField(reqBody, contentType, "size")
}

// videoOutputCount converts (seconds, sizeRatio) into the billable effective
// output count: ceil(seconds × ratio), floored at 1. Bounds the int64 conversion
// (an absurd seconds × ratio only over-charges the abusive caller, never wraps).
func videoOutputCount(seconds int64, sizeRatio float64) int64 {
	v := math.Ceil(float64(seconds) * sizeRatio)
	switch {
	case math.IsNaN(v) || v < 1:
		return 1
	case math.IsInf(v, 0) || v > float64(maxVideoOutputUnits):
		return maxVideoOutputUnits
	default:
		return int64(v)
	}
}

// maxVideoOutputUnits bounds the legacy/fallback video unit count, mirroring the
// engine's maxBillableUnits — far above any real clip (15s × 8 ratio = 120).
const maxVideoOutputUnits = 1 << 40

// videoOutputUnits computes the billable effective-output count for a video
// request. For a multi-model provider whose resolved model carries a per-model
// billing block, it uses that model's shape (per_video_second resolution ratios
// / per_unit_table); otherwise it falls back to the service-level size-ratio
// path (single-model — byte-for-byte unchanged).
//
// On a per_unit_table miss (a live (resolution, duration) the operator didn't
// table), it bills the table's MAX units — never the seconds×serviceRatio
// formula, which uses an unrelated resolution vocabulary and would underbill the
// bucket (a client could force this by requesting an unlisted combo). The miss
// is logged loudly so the operator adds the row.
func (c *Ctrl) videoOutputUnits(ctx context.Context, seconds int64, size string) int64 {
	if c.Service.HasMultiModelPricing() {
		if e := c.resolveModelPricing(ctx); e != nil && e.Billing != nil {
			units, err := e.Billing.OutputUnits(config.BillingObservables{Seconds: seconds, Resolution: size})
			if err == nil {
				return units
			}
			// Bucketed (per_unit_table) miss: bill the most expensive configured
			// bucket rather than dropping to the seconds-ratio formula (which would
			// underbill). Conservative + loud, never below the table.
			if e.Billing.Mode == config.BillingModePerUnitTable {
				if mx := e.Billing.MaxTableUnits(); mx > 0 {
					c.logger.Errorf("video per_unit_table miss (seconds=%d, size=%q): billing table-max %d units; operator should add this row: %v", seconds, size, mx, err)
					return mx
				}
			}
			c.logger.Errorf("video per-model OutputUnits failed (model billing misconfigured), falling back to service size-ratio: %v", err)
		}
	}
	return videoOutputCount(seconds, c.Service.GetVideoSizeRatio(size))
}

// handleVideoGenerationResponse handles the POST /videos response from the provider.
// Billing prefers the provider's response (actual seconds/size) and falls back to
// the client request when the upstream doesn't echo them (see resolveVideoBilling).
// Fee = ceil(seconds × sizeRatio) × outputPrice.
func (c *Ctrl) handleVideoGenerationResponse(ctx *gin.Context, resp *http.Response, account model.User, outputPrice string, reqBody []byte, reqModel model.Request) error {
	defer resp.Body.Close()

	chatKey := uuid.NewString()
	ctx.Writer.Header().Set("ZG-Res-Key", chatKey)

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		c.handleBrokerError(ctx, err, "read video generation response body")
		return err
	}

	if _, err := ctx.Writer.Write(body); err != nil {
		c.handleBrokerError(ctx, err, "write video generation response")
		return err
	}

	if !c.Service.TargetSeparated {
		c.logger.Debug("LLM server in the same network, signing video-generation response")
		if err := c.signChatWithKey(reqBody, body, chatKey); err != nil {
			c.logger.Warnf("Failed to sign video-generation response (TEE verification will be unavailable): %v", err)
		}
	}

	if reqModel.IsWhitelisted {
		// Whitelist traffic is unbilled; record metrics only (same response→request fallback).
		if sec, size, source := resolveVideoBilling(body, reqBody, ctx.Request.Header.Get("Content-Type")); source != "" {
			outputCount := c.videoOutputUnits(ctx, sec, size)
			monitor.RecordTokens("video-generation", 0, outputCount)
			monitor.RecordWhitelistTokens("video-generation", 0, outputCount)
		} else {
			c.logger.Warnf("whitelist video: no usable seconds in response or request, skipping metrics for %s", reqModel.RequestHash)
		}
		return nil
	}

	// Resolve billable seconds/size, preferring the upstream response (actual
	// output) and falling back to the client request.
	seconds, size, source := resolveVideoBilling(body, reqBody, ctx.Request.Header.Get("Content-Type"))
	if source == "" {
		// Returning here would serve the video FREE — make it loud + metered,
		// not a silent skip (this was a Warnf that hid Wan2.7 mis-parsing).
		c.logger.Errorf("video billing indeterminate: no positive seconds in response or request, NOT billing request %s (free output)", reqModel.RequestHash)
		monitor.RecordVideoBillingSkipped()
		return nil
	}
	if source == videoSourceRequest {
		// Billed the REQUESTED duration because the upstream reported no actual
		// output duration — this violates "bill actual output" and can over-bill a
		// partial generation. Surface it so the operator fixes the upstream/shim to
		// echo seconds (or usage.output_video_duration).
		c.logger.Warnf("video billed on REQUESTED duration (upstream did not report actual output) for request %s; configure the upstream/shim to echo seconds or usage.output_video_duration", reqModel.RequestHash)
	}

	outputCount := c.videoOutputUnits(ctx, seconds, size)

	outputFee, err := util.Multiply(outputPrice, outputCount)
	if err != nil {
		return errors.Wrap(err, "calculate output fee for video generation")
	}

	if err := c.db.UpdateRequestFeesAndCount(reqModel.RequestHash, outputFee.String(), outputFee.String(), outputCount); err != nil {
		return errors.Wrap(err, "update request fees and count in database")
	}

	monitor.RecordTokens("video-generation", 0, outputCount)
	return nil
}

// ensureMultipartWaitField ensures the "wait" field is present in a multipart/form-data body.
// If missing, appends wait=false before the closing boundary.
func ensureMultipartWaitField(reqBody []byte) []byte {
	bodyStr := string(reqBody)
	if parseMultipartField(bodyStr, "wait") != "" {
		return reqBody
	}

	// Find the closing boundary (e.g., "--boundary--") and insert the field before it
	closingIdx := strings.LastIndex(bodyStr, "--")
	if closingIdx <= 0 {
		return reqBody
	}

	// Walk back to find the start of the closing boundary line
	lineStart := closingIdx
	for lineStart > 0 && bodyStr[lineStart-1] != '\n' {
		lineStart--
	}
	closingBoundary := bodyStr[lineStart:]

	// Extract boundary marker from closing line (strip trailing "--" and leading "--")
	boundaryLine := strings.TrimSpace(closingBoundary)
	if !strings.HasSuffix(boundaryLine, "--") || !strings.HasPrefix(boundaryLine, "--") {
		return reqBody
	}
	boundary := boundaryLine[:len(boundaryLine)-2] // e.g., "--boundary"

	// Insert wait=false field before closing boundary
	waitField := boundary + "\r\nContent-Disposition: form-data; name=\"wait\"\r\n\r\nfalse\r\n"
	return []byte(bodyStr[:lineStart] + waitField + closingBoundary)
}

// parseVideoGenerationModel extracts the model field from a multipart/form-data video generation request.
func parseVideoGenerationModel(reqBody []byte) string {
	if len(reqBody) == 0 {
		return ""
	}
	return parseMultipartField(string(reqBody), "model")
}
