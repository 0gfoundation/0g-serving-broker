// Package handler wires the OpenAI-shaped image-generation HTTP surface to
// the Kling client and the translate package. Unlike videotranslator's
// stateless-per-call handlers, CreateImage internally polls Kling to a
// terminal state before responding — the broker's existing image contract
// (POST /v1/async/images/generations) forces wait=true and expects an
// immediate {data:[{b64_json}]} response, but Kling is natively async-only
// with 1-2 minute latency and no broker-side deferred-poll billing
// scheduler for images to lean on (unlike video's VideoPollJob) — so this
// sidecar absorbs the entire create-to-terminal-state sequence inside one
// HTTP call from the broker's perspective.
//
// api/imagetranslator is a structurally separate package tree from
// api/videotranslator, sharing no code — this package therefore defines its
// own request struct/parsing (below) rather than reusing videotranslator's
// CreateVideoRequest/parseCreateVideoRequest, and its own reference-image
// scheme allowlist (translate.isAllowedKlingReferenceScheme) rather than
// importing videotranslator's identically-purposed one.
package handler

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/sync/errgroup"
	"golang.org/x/time/rate"

	"github.com/0glabs/0g-serving-broker/common/log"
	"github.com/0glabs/0g-serving-broker/imagetranslator/internal/kling"
	"github.com/0glabs/0g-serving-broker/imagetranslator/internal/translate"
)

// klingPollInterval matches Kling's documented recommendation ("建议采用轮询
// 机制，并设置合理的查询间隔（如 5 秒）" — poll with a reasonable interval,
// example given as 5 seconds).
const klingPollInterval = 5 * time.Second

// KlingPollBudget bounds the poll loop's total wall-clock time — generous
// over the vendor's documented "typically 1-2 minutes" generation latency.
const KlingPollBudget = 4 * time.Minute

// KlingMaxConcurrentImageFetches bounds how many of a job's (up to 9)
// generated images are fetched at once — bounded rather than
// one-goroutine-per-image since n can reach 9.
const KlingMaxConcurrentImageFetches = 4

// KlingImageFetchTimeout bounds a single image's fetch — a per-image budget
// appropriate for a small PNG (Kling images are not 15-second 2K video
// files), not derived from any confirmed vendor constant.
const KlingImageFetchTimeout = 30 * time.Second

// klingMaxImageBytes caps FetchImage's response-body read (raw bytes, before
// base64 encoding). Sized against the broker's own confirmed decode-side
// cap: extractB64Images enforces maxB64ImageBytes = 50 MiB per image on the
// BASE64-ENCODED payload, and base64 inflates raw bytes by ~33%. 36 MiB raw
// base64-encodes to exactly 48 MiB (36 * 4/3) — comfortably under the
// broker's 50 MiB ceiling, with a real ~2 MiB / ~4% margin absorbing the
// choice of a round 36 MiB cap rather than the exact byte-for-byte maximum.
// This margin is NOT "for encoding overhead/line-wrapping" (standard base64
// embedded in a JSON string inserts no line breaks — that MIME/PEM
// convention doesn't apply here).
const klingMaxImageBytes = 36 << 20 // 36 MiB

// KlingMaxN is the vendor's documented upper bound on requested image count
// (validateKlingCount enforces the same range) — used here only to size the
// image-fetch-batch count in the sidecar's own WriteTimeout formula
// (cmd/server/kling.go).
const KlingMaxN = 9

// maxCreateImageBodyBytes bounds the total POST /images/generations request
// body. Kling's own prompt limit is ~2500 characters (a few KB at most in
// UTF-8); this is generous relative to today's actual fields, not a
// deliberately roomy budget — mirrors videotranslator's identical
// reasoning for maxCreateVideoBodyBytes.
const maxCreateImageBodyBytes = 8 << 20 // 8 MiB

// klingMaxConcurrentPolls bounds how many CreateImage requests may be
// blocked in their internal poll loop at once, per sidecar instance — a
// cheap, per-instance concurrency backstop distinct from (and in addition
// to) the router-side per-user in-flight cap, which bounds one user's
// aggregate concurrency, not this process's total resource commitment.
const klingMaxConcurrentPolls = 50

// klingPollRateLimit is this sidecar's own sustained outbound GetTask rate,
// a conservative share of the vendor's documented 20 RPS default on the
// shared query endpoint (Vidu's videotranslator sidecar independently limits
// its own share of the same budget — see docs/design notes in that
// package). Not a dynamically shared cross-process budget; a static,
// per-process split, tracked as a known v1 simplification.
const klingPollRateLimit = 10 // requests per second

// KlingHandler serves the OpenAI-shaped image-generation surface the broker
// expects for Kling, internally polling to a terminal state before ever
// responding (see package doc).
type KlingHandler struct {
	client      *kling.Client
	logger      log.Logger
	pollLimiter *rate.Limiter
	// pollSem bounds klingMaxConcurrentPolls concurrent in-flight
	// CreateImage calls (each holding a goroutine + memory for the
	// duration of its poll loop) — a cheap, per-instance capacity backstop.
	pollSem chan struct{}
}

// NewKlingHandler builds a KlingHandler.
func NewKlingHandler(client *kling.Client, logger log.Logger) *KlingHandler {
	return &KlingHandler{
		client:      client,
		logger:      logger,
		pollLimiter: rate.NewLimiter(rate.Limit(klingPollRateLimit), klingPollRateLimit),
		pollSem:     make(chan struct{}, klingMaxConcurrentPolls),
	}
}

// jsonCreateImageRequest is the JSON-shaped variant of a create request.
type jsonCreateImageRequest struct {
	Model     string `json:"model"`
	Prompt    string `json:"prompt"`
	N         int    `json:"n"`
	Size      string `json:"size"`
	Watermark *bool  `json:"watermark"`
	// InputReference mirrors videotranslator's identically-named JSON field
	// for image-to-video — here, image-to-image (a single reference image).
	InputReference *struct {
		ImageURL string `json:"image_url"`
	} `json:"input_reference"`
}

// parseCreateImageRequest reads a create request from either a
// multipart/form-data body or a plain JSON body — the broker's own
// request-side parsing tolerates both, mirroring videotranslator's
// parseCreateVideoRequest.
func parseCreateImageRequest(r *http.Request) (translate.CreateImageRequest, error) {
	contentType := r.Header.Get("Content-Type")
	if strings.HasPrefix(contentType, "multipart/form-data") {
		if err := r.ParseMultipartForm(32 << 20); err != nil {
			return translate.CreateImageRequest{}, err
		}
		n := 1
		if v := r.FormValue("n"); v != "" {
			parsed, err := strconv.Atoi(v)
			if err != nil {
				return translate.CreateImageRequest{}, fmt.Errorf("invalid n %q: %w", v, err)
			}
			n = parsed
		}
		watermark := false
		if v := r.FormValue("watermark"); v != "" {
			parsed, err := strconv.ParseBool(v)
			if err != nil {
				return translate.CreateImageRequest{}, fmt.Errorf("invalid watermark %q: %w", v, err)
			}
			watermark = parsed
		}
		req := translate.CreateImageRequest{
			Model:     r.FormValue("model"),
			Prompt:    r.FormValue("prompt"),
			N:         n,
			Size:      r.FormValue("size"),
			Watermark: watermark,
		}
		if v := r.FormValue("input_reference"); v != "" {
			req.InputReferenceImageURL = v
		} else if dataURI, ok := multipartFileDataURI(r, "input_reference"); ok {
			req.InputReferenceImageURL = dataURI
		}
		return req, nil
	}

	var jr jsonCreateImageRequest
	if err := json.NewDecoder(r.Body).Decode(&jr); err != nil {
		return translate.CreateImageRequest{}, err
	}
	n := jr.N
	if n == 0 {
		n = 1
	}
	req := translate.CreateImageRequest{
		Model:  jr.Model,
		Prompt: jr.Prompt,
		N:      n,
		Size:   jr.Size,
	}
	if jr.Watermark != nil {
		req.Watermark = *jr.Watermark
	}
	if jr.InputReference != nil {
		req.InputReferenceImageURL = jr.InputReference.ImageURL
	}
	return req, nil
}

// multipartFileDataURI reads a named multipart file part and returns it as a
// base64 data: URI. Returns ("", false) when the part is absent or
// unreadable. Mirrors videotranslator's identically-named helper (new code
// in this package, not shared — see package doc).
func multipartFileDataURI(r *http.Request, field string) (string, bool) {
	if r.MultipartForm == nil || len(r.MultipartForm.File[field]) == 0 {
		return "", false
	}
	fh := r.MultipartForm.File[field][0]
	f, err := fh.Open()
	if err != nil {
		return "", false
	}
	defer f.Close()
	data, err := io.ReadAll(f)
	if err != nil {
		return "", false
	}
	ct := fh.Header.Get("Content-Type")
	if ct == "" {
		ct = "image/png"
	}
	return "data:" + ct + ";base64," + base64.StdEncoding.EncodeToString(data), true
}

// imageDataItem is one element of the broker-expected {data:[{b64_json}]}
// envelope.
type imageDataItem struct {
	B64JSON string `json:"b64_json"`
}

// createImageResponse is the broker-expected response envelope for a
// completed image-generation job (mirrors OpenAI's images/generations
// response shape, per Dossier 5's documented broker contract).
type createImageResponse struct {
	Created int64           `json:"created"`
	Data    []imageDataItem `json:"data"`
}

// CreateImage handles POST /images/generations. It blocks for the entire
// create-to-terminal-state sequence (see package doc) before responding.
func (h *KlingHandler) CreateImage(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxCreateImageBodyBytes)

	req, err := parseCreateImageRequest(c.Request)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"message": err.Error()}})
		return
	}
	if err := translate.ValidateKlingCreateRequest(req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"message": err.Error()}})
		return
	}
	if translate.PromptExceedsVendorLimit(req) {
		h.logger.Warnf("kling_prompt_truncation_risk: outbound prompt is %d runes, exceeds vendor's documented 2500-character limit and will be auto-truncated vendor-side", len([]rune(req.Prompt)))
	}
	if translate.ReferenceWasDroppedForScheme(req) {
		h.logger.Warnf("kling_reference_scheme_rejected: input_reference %q was non-empty but scheme-rejected (only http(s):// is supported for Kling); request will proceed text-only", req.InputReferenceImageURL)
	}

	select {
	case h.pollSem <- struct{}{}:
		defer func() { <-h.pollSem }()
	default:
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": gin.H{"message": "too many concurrent Kling image-generation requests in flight on this instance"}})
		return
	}

	authHeader := c.GetHeader("Authorization")
	ctx := c.Request.Context()

	klingReq := translate.ToKlingCreateRequest(req)
	createResp, err := h.client.CreateTask(ctx, authHeader, klingReq)
	if err != nil {
		h.writeKlingError(c, "kling create task failed", "failed to create image generation task", err)
		return
	}
	taskID := createResp.Output.TaskID

	imgResp, getResp, err := h.pollToTerminal(ctx, authHeader, taskID, req)
	if err != nil {
		h.writeKlingError(c, fmt.Sprintf("kling poll failed for %s", taskID), "failed to poll image generation task", err)
		return
	}
	if imgResp == nil {
		// Poll budget exhausted without reaching a terminal state — a
		// distinct, named failure mode (not a download failure, not a
		// vendor-reported terminal status) so an operator watching sidecar
		// error rates can tell "Kling is chronically slower than the poll
		// budget assumes" apart from other 502 causes.
		h.logger.Errorf("kling_poll_budget_exhausted: task %s did not reach a terminal state within %s", taskID, KlingPollBudget)
		c.JSON(http.StatusBadGateway, gin.H{"error": gin.H{"message": "image generation did not complete within the allotted time"}})
		return
	}

	if imgResp.Status == translate.StatusFailed {
		h.writeKlingImageError(c, imgResp)
		return
	}

	urls := translate.GeneratedImageURLs(*getResp)
	images, err := h.fetchAllImages(ctx, authHeader, urls)
	if err != nil {
		h.logger.Errorf("kling image download failed for task %s: %v", taskID, err)
		c.JSON(http.StatusBadGateway, gin.H{"error": gin.H{"message": "failed to download one or more generated images"}})
		return
	}

	data := make([]imageDataItem, len(images))
	for i, img := range images {
		data[i] = imageDataItem{B64JSON: base64.StdEncoding.EncodeToString(img)}
	}
	c.JSON(http.StatusOK, createImageResponse{Data: data})
}

// pollToTerminal polls taskID every klingPollInterval, respecting
// h.pollLimiter's rate cap and the request context, until a terminal
// ImageResponse is reached or KlingPollBudget elapses (returning nil, nil,
// nil in that case — the caller reports poll-budget exhaustion). Each
// iteration selects on ctx.Done() alongside its sleep, so a disconnected
// caller, an intermediary timeout, or the sidecar's own WriteTimeout closing
// the connection stops the loop immediately rather than continuing to poll
// for the full worst-case budget — the same discipline already applied to
// each per-image fetch in fetchAllImages.
func (h *KlingHandler) pollToTerminal(ctx context.Context, authHeader, taskID string, req translate.CreateImageRequest) (*translate.ImageResponse, *kling.GetTaskResponse, error) {
	deadline := time.Now().Add(KlingPollBudget)
	ticker := time.NewTicker(klingPollInterval)
	defer ticker.Stop()

	for {
		if err := h.pollLimiter.Wait(ctx); err != nil {
			return nil, nil, err
		}
		getResp, err := h.client.GetTask(ctx, authHeader, taskID)
		if err != nil {
			return nil, nil, err
		}
		imgResp := translate.FromKlingGetTaskResponse(req, *getResp)
		if !imgResp.Transient && imgResp.Status != translate.StatusQueued && imgResp.Status != translate.StatusInProgress {
			return &imgResp, getResp, nil
		}

		if time.Now().After(deadline) {
			return nil, nil, nil
		}
		select {
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		case <-ticker.C:
		}
	}
}

// fetchAllImages downloads each of urls concurrently (bounded to
// KlingMaxConcurrentImageFetches), each under its own KlingImageFetchTimeout
// and klingMaxImageBytes cap. All-or-nothing: if any single fetch fails
// (including hitting the size cap), the whole batch fails and no partial
// data[] array is ever returned, consistent with this codebase's
// established preference for loud failure over silent under/over-serving.
func (h *KlingHandler) fetchAllImages(ctx context.Context, authHeader string, urls []string) ([][]byte, error) {
	images := make([][]byte, len(urls))
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(KlingMaxConcurrentImageFetches)

	for i, u := range urls {
		i, u := i, u
		g.Go(func() error {
			fetchCtx, cancel := context.WithTimeout(gctx, KlingImageFetchTimeout)
			defer cancel()

			resp, err := h.client.FetchImage(fetchCtx, u)
			if err != nil {
				return err
			}
			defer resp.Body.Close()

			data, err := io.ReadAll(io.LimitReader(resp.Body, klingMaxImageBytes+1))
			if err != nil {
				return err
			}
			if len(data) > klingMaxImageBytes {
				return fmt.Errorf("image at %s exceeds the %d-byte fetch cap", u, klingMaxImageBytes)
			}
			images[i] = data
			return nil
		})
	}

	if err := g.Wait(); err != nil {
		return nil, err
	}
	return images, nil
}

// writeKlingImageError writes a terminal StatusFailed ImageResponse's vendor
// error as the HTTP response. Distinguishes the vendor-partial-success case
// (its own named log line, kept as a 502 per the all-or-nothing billing
// policy — no downloads are attempted, no bill issued) from a genuine
// vendor-reported terminal failure (also 502 — Kling's terminal failures
// have no OpenAI-standard 4xx equivalent the way a create-time vendor
// rejection does).
func (h *KlingHandler) writeKlingImageError(c *gin.Context, imgResp *translate.ImageResponse) {
	if imgResp.Error != nil && imgResp.Error.Code == "kling_vendor_partial_success" {
		h.logger.Errorf("%s", imgResp.Error.Message)
	} else if imgResp.Error != nil {
		h.logger.Errorf("kling task failed: code=%q message=%q", imgResp.Error.Code, imgResp.Error.Message)
	}
	message := "image generation failed"
	code := ""
	if imgResp.Error != nil {
		code = imgResp.Error.Code
		if imgResp.Error.Message != "" {
			message = imgResp.Error.Message
		}
	}
	c.JSON(http.StatusBadGateway, gin.H{"error": gin.H{"code": code, "message": message}})
}

// writeKlingError maps a kling client error to the HTTP response the caller
// sees. A Kling 4xx (*kling.APIError with a 4xx status — the vendor
// rejected the request outright) surfaces the vendor's own status/code/
// message. Anything else (5xx, a plain transport error, or an
// adapter-internal ambiguous-shape signal) is reported as 502 without
// vendor detail.
func (h *KlingHandler) writeKlingError(c *gin.Context, logContext, fallbackMessage string, err error) {
	var apiErr *kling.APIError
	if errors.As(err, &apiErr) && apiErr.StatusCode >= 400 && apiErr.StatusCode < 500 {
		message := apiErr.Message
		if message == "" {
			message = fmt.Sprintf("kling rejected the request (status %d)", apiErr.StatusCode)
		}
		h.logger.Errorf("%s: kling rejected request: status %d code=%q message=%q", logContext, apiErr.StatusCode, apiErr.Code, apiErr.Message)
		c.JSON(apiErr.StatusCode, gin.H{"error": gin.H{"code": apiErr.Code, "message": message}})
		return
	}
	h.logger.Errorf("%s: %v", logContext, err)
	c.JSON(http.StatusBadGateway, gin.H{"error": gin.H{"message": fallbackMessage}})
}
