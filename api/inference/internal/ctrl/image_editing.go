package ctrl

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

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

// GetImageEditingInputFeeAndImageNum extracts input fee and output image count from request body
// Supports both JSON and multipart/form-data formats
// Parameters:
//   - reqBody: Raw request body bytes
//
// Returns:
//   - string: Expected input fee (as big integer string)
//   - int64: Number of output images
//   - error: Parse error
func (c *Ctrl) GetImageEditingInputFeeAndImageNum(reqBody []byte) (string, int64, error) {
	// Get output image count (default to 1)
	imageNum := int64(1)

	// Try JSON format first
	var request ImageEditingRequest
	if err := json.Unmarshal(reqBody, &request); err == nil {
		// Successfully parsed as JSON
		if request.N != nil && *request.N > 0 {
			imageNum = int64(*request.N)
		}
	} else {
		// Not JSON, try to parse as multipart/form-data
		bodyStr := string(reqBody)

		// Look for "n" parameter in multipart data
		// Pattern: name="n"\r\n\r\n<value>
		imageNum = c.parseMultipartImageNum(bodyStr)
	}

	// Input fee calculation
	// Current design: fixed at 0 (similar to text-to-image)
	expectedInputFee := "0"

	return expectedInputFee, imageNum, nil
}

// parseMultipartImageNum extracts the "n" parameter from multipart/form-data.
//
// FIXME: this is a hand-rolled byte scanner with the same adversarial-content
// risk that bit rewriteMultipartResponseFormat — a file part whose bytes happen
// to contain name="n"\r\n\r\n would be parsed as the "n" field, producing a
// wrong billing count. Because billing is derived from this value, the impact
// is worse than the rewriter bug (which was a silent correctness miss). Should
// be migrated to mime/multipart.Reader — the same infra proxy.go now uses for
// response_format. The fix was deferred from the response_format PR to keep
// scope focused; billing-path changes deserve their own review surface.
func (c *Ctrl) parseMultipartImageNum(bodyStr string) int64 {
	// Look for name="n" in the multipart body
	nFieldStart := findSubstring(bodyStr, `name="n"`)
	if nFieldStart == -1 {
		// Try without quotes
		nFieldStart = findSubstring(bodyStr, `name=n`)
	}

	if nFieldStart == -1 {
		return 1 // Default value if not found
	}

	// Find the value after the field declaration
	// Multipart format: name="n"\r\n\r\n<value>
	valueStart := findSubstring(bodyStr[nFieldStart:], "\r\n\r\n")
	if valueStart == -1 {
		valueStart = findSubstring(bodyStr[nFieldStart:], "\n\n")
	}

	if valueStart == -1 {
		return 1
	}

	valueStart += nFieldStart
	if bodyStr[valueStart] == '\r' {
		valueStart += 4 // Skip \r\n\r\n
	} else {
		valueStart += 2 // Skip \n\n
	}

	// Extract digits until we hit a non-digit or boundary
	var numStr string
	for i := valueStart; i < len(bodyStr); i++ {
		if bodyStr[i] >= '0' && bodyStr[i] <= '9' {
			numStr += string(bodyStr[i])
		} else {
			break
		}
	}

	if numStr == "" {
		return 1
	}

	// Parse the number
	var result int64 = 0
	for _, digit := range numStr {
		result = result*10 + int64(digit-'0')
	}

	if result <= 0 {
		return 1
	}

	return result
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

	chatKey := uuid.NewString()
	ctx.Writer.Header().Set("ZG-Res-Key", chatKey)

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		c.handleBrokerError(ctx, err, "read image editing response body")
		return err
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

	images, extractErr := extractB64Images(body)

	originalFormat, _ := ctx.Get("clientResponseFormat")
	wantURL := originalFormat == "url"

	// If the client asked for url but the provider returned something we can't
	// decode (non-b64 envelope, empty array), refuse the response rather than
	// passing provider bytes through — they may contain LAN-private URLs.
	if wantURL && (extractErr != nil || len(images) == 0) {
		ctx.Set("ignoreError", true)
		err := fmt.Errorf("provider returned non-b64 image response, refusing to forward (may contain LAN-private URLs): %w", extractErr)
		c.handleBrokerError(ctx, err, "image-editing response for response_format=url")
		return err
	}

	// Build the body to send to the client. For wantURL, store + rewrite; any
	// failure here downgrades to b64 (safe — body is confirmed b64 above).
	clientBody := body
	if wantURL && c.imageStore != nil {
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
			c.logger.Warnf("Client disconnected before receiving full response: %v", writeErr)
		} else {
			c.handleBrokerError(ctx, writeErr, "write image editing response")
			return writeErr
		}
	}

	// TEE signing — see handleTextToImageResponse for trust-model rationale.
	switch {
	case c.Service.IsCentralized():
		var tlsState *tls.ConnectionState
		if v, exists := ctx.Get("tlsState"); exists {
			tlsState, _ = v.(*tls.ConnectionState)
		}
		c.logger.Debug("Centralized provider, signing image-editing routing proof")
		if err := c.signCentralizedRoutingProof(sigReqBody, body, chatKey, tlsState); err != nil {
			c.logger.Errorf("routing proof not created: %v", err)
		}
	case !c.Service.TargetSeparated:
		c.logger.Debug("LLM server in the same network, signing image-editing content")
		if extractErr == nil && len(images) > 0 {
			_ = c.signImageResponse(sigReqBody, images, chatKey)
		} else {
			c.logger.Warnf("No b64 images extracted, falling back to full-body signature: %v", extractErr)
			_ = c.signChatWithKey(sigReqBody, body, chatKey)
		}
	}

	// Skip billing for whitelisted users, but record whitelist traffic metrics
	if reqModel.IsWhitelisted {
		monitor.RecordTokens("image-editing", 0, reqModel.OutputCount)
		monitor.RecordWhitelistTokens("image-editing", 0, reqModel.OutputCount)
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

	monitor.RecordTokens("image-editing", 0, imageNum)

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
