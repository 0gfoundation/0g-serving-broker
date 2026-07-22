package ctrl

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/0glabs/0g-serving-broker/common/errors"
	"github.com/0glabs/0g-serving-broker/common/middleware"
	"github.com/0glabs/0g-serving-broker/common/util"
	"github.com/0glabs/0g-serving-broker/inference/model"
	"github.com/0glabs/0g-serving-broker/inference/monitor"
)

// ImageEditingRequest defines the structure of an image editing request
// Following OpenAI /v1/images/edits API standard
type ImageEditingRequest struct {
	// Required fields
	Image  string `json:"image"`  // Base64-encoded input image (with or without data URI prefix)
	Prompt string `json:"prompt"` // Editing instruction (e.g., "change cat to dog")

	// Optional standard fields
	Mask           *string `json:"mask,omitempty"`            // Base64-encoded mask image
	NegativePrompt *string `json:"negative_prompt,omitempty"` // Negative prompt
	N              *int    `json:"n,omitempty"`               // Number of images to generate (default: 1)
	Size           *string `json:"size,omitempty"`            // Output image dimensions
	ResponseFormat *string `json:"response_format,omitempty"` // "url" or "b64_json"

	// Extended fields: model-specific parameters
	Extras *ImageEditingExtras `json:"extras,omitempty"`
}

// ImageEditingExtras contains extended parameters for specific models
type ImageEditingExtras struct {
	RefImages []string `json:"ref_images,omitempty"` // Reference images for multi-image models (e.g., Qwen)
	Strength  *float64 `json:"strength,omitempty"`   // Editing strength 0.0-1.0
	Seed      *int64   `json:"seed,omitempty"`       // Random seed for reproducibility
}

// GetImageEditingInputFeeAndImageNum extracts input fee and output image count
// from the request body. Supports JSON and multipart/form-data.
//
// The contentType arg is used to parse the multipart boundary. Without it, a
// byte-level scan of the body would misread a file part whose bytes happen to
// contain `name="n"\r\n\r\n<digits>` as the form's "n" field — producing the
// wrong billing count. Using mime/multipart.Reader respects the real boundary
// so adversarial file content cannot influence billing.
func (c *Ctrl) GetImageEditingInputFeeAndImageNum(reqBody []byte, contentType string) (string, int64, error) {
	imageNum := int64(1) // default

	if strings.HasPrefix(strings.ToLower(contentType), "multipart/") {
		if n, err := parseMultipartN(reqBody, contentType); err == nil && n > 0 {
			imageNum = n
		}
		// Parse failure is non-fatal: imageNum stays at the default of 1. The
		// upstream provider will surface any real structural issue.
	} else {
		// JSON path (or unknown content-type, assumed JSON).
		var request ImageEditingRequest
		if err := json.Unmarshal(reqBody, &request); err == nil {
			if request.N != nil && *request.N > 0 {
				imageNum = int64(*request.N)
			}
		}
	}

	return "0", imageNum, nil
}

// parseMultipartN walks the multipart body with mime/multipart.Reader and
// returns the integer value of the "n" form field, or an error if the body
// cannot be parsed or the field is absent / non-numeric.
func parseMultipartN(body []byte, contentType string) (int64, error) {
	_, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		return 0, fmt.Errorf("parse content-type %q: %w", contentType, err)
	}
	boundary, ok := params["boundary"]
	if !ok || boundary == "" {
		return 0, fmt.Errorf("content-type %q has no boundary", contentType)
	}

	reader := multipart.NewReader(bytes.NewReader(body), boundary)
	for {
		part, pErr := reader.NextPart()
		if pErr == io.EOF {
			return 0, fmt.Errorf("n field not found")
		}
		if pErr != nil {
			return 0, fmt.Errorf("read part: %w", pErr)
		}
		if part.FormName() != "n" {
			_ = part.Close()
			continue
		}
		// Cap the read — n is a small integer, not a file. 32 bytes is more
		// than enough for any legitimate int and prevents a malicious upload
		// that labels itself name="n" from streaming gigabytes into memory.
		raw, rErr := io.ReadAll(io.LimitReader(part, 32))
		_ = part.Close()
		if rErr != nil {
			return 0, fmt.Errorf("read n value: %w", rErr)
		}
		n, parseErr := strconv.ParseInt(strings.TrimSpace(string(raw)), 10, 64)
		if parseErr != nil {
			return 0, fmt.Errorf("parse n value %q: %w", raw, parseErr)
		}
		return n, nil
	}
}

// findSubstring returns the index of substr in s, or -1 if not found
func findSubstring(s, substr string) int {
	if len(substr) == 0 {
		return 0
	}
	if len(s) < len(substr) {
		return -1
	}
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}

// handleImageEditingResponse handles the image editing response.
// Mirrors handleTextToImageResponse: extracts b64 image bytes, signs per-image
// hashes, and optionally rewrites the response with broker-served URLs.
func (c *Ctrl) handleImageEditingResponse(ctx *gin.Context, resp *http.Response, account model.User, outputPrice string, reqBody []byte, reqModel model.Request) error {
	defer resp.Body.Close()

	// chatKey always backs the image store / broker-served URLs; ZG-Res-Key (the
	// signature-lookup handle) is only advertised when the response is signed. A
	// standard/TargetSeparated provider produces no signature, so advertising it
	// would point clients at a signature endpoint that only 404s.
	chatKey := uuid.NewString()
	if !c.Service.TargetSeparated || c.Service.IsCentralized() {
		ctx.Writer.Header().Set("ZG-Res-Key", chatKey)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		c.handleBrokerError(ctx, err, "read image editing response body")
		return err
	}

	// For forwarder providers, strip #184 upstream identity/cost leak fields before
	// the envelope is used for extraction, URL rewrite, signing, or forwarding
	// (sanitize-before-sign keeps the signature bound to what the client receives).
	// Decode a compressed body first (the sync path forces identity upstream; an
	// upstream that ignores it would otherwise slip the leak past the JSON parse).
	// sanitizeResponseBody preserves data[] (image payloads).
	if c.Service.IsForwarder() {
		body = c.sanitizeForwarderResponseBody(ctx, body, resp.Header.Get("Content-Encoding"))
	}

	responseSizeMB := float64(len(body)) / (1024 * 1024)
	c.logger.Infof("Image editing response: size=%.2f MB, user=%s, imageCount=%d",
		responseSizeMB, reqModel.UserAddress, reqModel.OutputCount)

	// Resolve the original client request body (pre-b64 rewrite) for signing.
	sigReqBody := reqBody
	if v, ok := ctx.Get("clientReqBody"); ok {
		if orig, ok := v.([]byte); ok {
			sigReqBody = orig
		}
	}

	images, extractErr := extractB64Images(body, int(reqModel.OutputCount))

	originalFormat, _ := ctx.Get("clientResponseFormat")
	wantURL := originalFormat == "url"

	// If the client asked for url but the provider returned something we can't
	// decode (non-b64 envelope, empty array), refuse the response rather than
	// passing provider bytes through — they may contain LAN-private URLs.
	if wantURL && (extractErr != nil || len(images) == 0) {
		ctx.Set("ignoreError", true)
		// Provider misbehaviour (200 with an undecodable envelope), not a client
		// error — attribute to upstream so it trips the upstream-fault alert. See
		// handleTextToImageResponse.
		ctx.Set(monitor.CtxKeyFailureSource, monitor.FailureSourceUpstream)
		err := fmt.Errorf("provider returned non-b64 image response, refusing to forward (may contain LAN-private URLs): %w", extractErr)
		c.handleBrokerError(ctx, err, "image-editing response for response_format=url")
		return err
	}

	// URL requested but store disabled — fail-closed, see handleTextToImageResponse.
	if wantURL && c.imageStore == nil {
		ctx.Set("ignoreError", true)
		// Disabled image store is a broker config failure, not a client error —
		// keep it in the broker bucket. See handleTextToImageResponse.
		ctx.Set(monitor.CtxKeyFailureSource, monitor.FailureSourceBroker)
		err := fmt.Errorf("response_format=url requested but image store is disabled (check startup logs for newImageStore error)")
		c.logger.Errorf("image-editing URL request while imageStore is nil")
		c.handleBrokerError(ctx, err, "image-editing response for response_format=url")
		return err
	}

	// Build the body to send to the client. store + rewrite; any failure here
	// downgrades to b64 (safe — body is confirmed b64 above).
	clientBody := body
	if wantURL {
		if storeErr := c.imageStore.store(chatKey, images); storeErr != nil {
			c.logger.Warnf("Failed to store images for URL rewrite, sending b64: %v", storeErr)
		} else {
			rewritten, buildErr := buildURLResponse(body, chatKey, len(images), c.Service.ServingURL)
			if buildErr != nil {
				c.logger.Warnf("Failed to build URL response, sending b64: %v", buildErr)
			} else {
				clientBody = rewritten
			}
		}
	}

	if _, writeErr := ctx.Writer.Write(clientBody); writeErr != nil {
		if c.isClientDisconnectError(writeErr) {
			// Matches handleTextToImageResponse: downstream middleware keys off
			// ignoreError to suppress noisy error logs / metrics for expected
			// client-disconnect cases. Without this, image-editing disconnects
			// would surface as errors while text-to-image disconnects wouldn't.
			ctx.Set("ignoreError", true)
			c.logger.Warnf("Client disconnected during image-editing response, billing for completed response (%d bytes)", len(body))
		} else {
			c.handleBrokerError(ctx, writeErr, "write image editing response")
			return writeErr
		}
	}

	// TEE signing — see handleTextToImageResponse for trust-model rationale.
	switch {
	case c.Service.IsCentralized():
		fingerprint := ctx.GetString(CtxKeyUpstreamCertFingerprint)
		c.logger.Debug("Centralized provider, signing image-editing routing proof")
		// Image modalities reject modelPricing at config load, so "" falls back to
		// the service-level providerIdentity inside the signer (unchanged behaviour).
		if err := c.signCentralizedRoutingProof(sigReqBody, body, chatKey, fingerprint, ""); err != nil {
			c.logger.Errorf("routing proof not created: %v", err)
		}
	case !c.Service.TargetSeparated:
		c.logger.Debug("LLM server in the same network, signing image-editing content")
		if extractErr == nil && len(images) > 0 {
			// Logged, not discarded: signing is a call to the controller now, not an
			// in-process crypto.Sign, so it fails whenever the controller cannot pin the
			// running image. Swallowed, the client's later GET of the signature 404s with
			// nothing pointing back at why. Still not fatal — the response is already served.
			if err := c.signImageResponse(sigReqBody, images, chatKey); err != nil {
				c.logger.Errorf("could not sign the image response for %s: %v", chatKey, err)
			}
		} else {
			c.logger.Warnf("No b64 images extracted, falling back to full-body signature: %v", extractErr)
			if err := c.signChatWithKey(sigReqBody, body, chatKey); err != nil {
				c.logger.Errorf("could not sign the image response for %s: %v", chatKey, err)
			}
		}
	}

	// Skip billing for whitelisted users, but record whitelist traffic metrics and
	// count the images into the reconciliation rollup (they hit the upstream).
	if reqModel.IsWhitelisted {
		metricModel := c.metricModel(ctx)
		monitor.RecordTokens("image-editing", metricModel, 0, reqModel.OutputCount)
		monitor.RecordWhitelistTokens("image-editing", metricModel, 0, reqModel.OutputCount)
		c.recordWhitelistedUsage(reqModel, 0, reqModel.OutputCount, 0, 0, "")
		return nil
	}

	// Get image count from request model (stored during preprocessing)
	imageNum := reqModel.OutputCount

	// Calculate output fee: image count × price per image
	outputFee, err := util.Multiply(outputPrice, imageNum)
	if err != nil {
		return errors.Wrap(err, "calculate output fee for image editing")
	}

	// Calculate total fee: input fee + output fee
	totalFee, err := util.Add(reqModel.InputFee, outputFee.String())
	if err != nil {
		return errors.Wrap(err, "calculate total fee for image editing")
	}

	// Update database with request fees and count
	if err := c.db.UpdateRequestFeesAndCount(reqModel.RequestHash, outputFee.String(), totalFee.String(), imageNum); err != nil {
		return errors.Wrap(err, "update request fees and count in database")
	}

	monitor.RecordTokens("image-editing", c.metricModel(ctx), 0, imageNum)

	// Update IPM limiter with actual image consumption
	if ipmLimiter, exists := ctx.Get("ipmLimiter"); exists {
		if limiter, ok := ipmLimiter.(*middleware.PerUserTPMLimiter); ok {
			limiter.ConsumeTokens(reqModel.UserAddress, int(imageNum))
		}
	}
	return nil
}

/*
// calculateImageInputFee calculates input fee based on image size (optional feature)
func (c *Ctrl) calculateImageInputFee(imageSize int64) (*big.Int, error) {
	// Charge per 100KB
	const bytesPerUnit = 100 * 1024
	const pricePerUnit = "1000000000000000" // 0.001 0G

	units := (imageSize + bytesPerUnit - 1) / bytesPerUnit // Round up
	return util.Multiply(pricePerUnit, units)
}
*/
