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
// response. The same shape (seconds/size) is the broker's edge contract for the
// request too, so it doubles as the request-fallback parse — see resolveVideoBilling.
type videoResponseFields struct {
	Seconds json.Number `json:"seconds"`
	Size    string      `json:"size"`
}

// resolveVideoBilling picks the billable (seconds, size) for a video request,
// preferring the upstream response (actual generated output) and falling back to
// the client request, which carries the same seconds/size the caller specified.
// Many async video upstreams (e.g. Alibaba Wan2.7) return a 200 whose body does
// NOT echo seconds/size in this shape — without the request fallback the caller
// skips billing and serves the video for free. ok is false only when neither
// source yields a positive duration.
//
// Limitation: the request fallback bills the REQUESTED duration, which can
// exceed a partially-generated output. A per-providerIdentity response
// normalizer (reading actual output + success) is the proper fix — see
// docs/design/multimodal-billing.md.
func resolveVideoBilling(respBody, reqBody []byte) (seconds int64, size string, ok bool) {
	// Response: the upstream returns JSON; prefer the actual generated output.
	var rf videoResponseFields
	_ = json.Unmarshal(respBody, &rf)
	if s, err := rf.Seconds.Int64(); err == nil && s > 0 {
		return s, rf.Size, true
	}
	// Request fallback: the broker's video edge is multipart/form-data
	// (OpenAI /v1/videos), occasionally JSON — read seconds/size from whichever
	// shape the body actually is. JSON-only parsing here was a bug: it never
	// matched the live multipart transport, so this fallback recovered nothing.
	if s, reqSize := videoSecondsSizeFromRequest(reqBody); s > 0 {
		sz := reqSize
		if sz == "" {
			sz = rf.Size // response size if the request omitted it (baseline ratio if both empty)
		}
		return s, sz, true
	}
	return 0, "", false
}

// videoSecondsSizeFromRequest extracts a positive integer `seconds` and the
// `size` from a video request body, handling both JSON and multipart/form-data
// (the live transport for /v1/videos). Returns (0, "") when no positive seconds
// is present in either shape.
func videoSecondsSizeFromRequest(reqBody []byte) (int64, string) {
	if len(reqBody) == 0 {
		return 0, ""
	}
	// JSON shape.
	var qf videoResponseFields
	if json.Unmarshal(reqBody, &qf) == nil {
		if s, err := qf.Seconds.Int64(); err == nil && s > 0 {
			return s, qf.Size
		}
	}
	// multipart/form-data shape (same parser the model/wait fields use).
	body := string(reqBody)
	secStr := parseMultipartField(body, "seconds")
	if secStr == "" {
		return 0, ""
	}
	s, err := strconv.ParseInt(strings.TrimSpace(secStr), 10, 64)
	if err != nil || s <= 0 {
		return 0, ""
	}
	return s, parseMultipartField(body, "size")
}

// videoOutputCount converts (seconds, sizeRatio) into the billable effective
// output count: ceil(seconds × ratio), floored at 1.
func videoOutputCount(seconds int64, sizeRatio float64) int64 {
	count := int64(math.Ceil(float64(seconds) * sizeRatio))
	if count < 1 {
		count = 1
	}
	return count
}

// videoOutputUnits computes the billable effective-output count for a video
// request. For a multi-model provider whose resolved model carries a per-model
// billing block, it uses that model's shape (per_video_second resolution ratios
// / per_unit_table); otherwise it falls back to the service-level size-ratio
// path (single-model — byte-for-byte unchanged). On a per-model billing error
// (e.g. a per_unit_table miss or out-of-range product) it logs and falls back to
// the service ratio so the request is still billed rather than served free.
func (c *Ctrl) videoOutputUnits(ctx context.Context, seconds int64, size string) int64 {
	if c.Service.HasMultiModelPricing() {
		if e := c.resolveModelPricing(ctx); e != nil && e.Billing != nil {
			units, err := e.Billing.OutputUnits(config.BillingObservables{Seconds: seconds, Resolution: size})
			if err == nil {
				return units
			}
			c.logger.Errorf("video per-model OutputUnits failed (model billing misconfigured for this request), falling back to service size-ratio: %v", err)
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
		if sec, size, ok := resolveVideoBilling(body, reqBody); ok {
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
	seconds, size, ok := resolveVideoBilling(body, reqBody)
	if !ok {
		// Returning here would serve the video FREE — make it loud + metered,
		// not a silent skip (this was a Warnf that hid Wan2.7 mis-parsing).
		c.logger.Errorf("video billing indeterminate: no positive seconds in response or request, NOT billing request %s (free output)", reqModel.RequestHash)
		monitor.RecordVideoBillingSkipped()
		return nil
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
