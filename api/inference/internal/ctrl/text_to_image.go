package ctrl

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/0glabs/0g-serving-broker/common/errors"
	"github.com/0glabs/0g-serving-broker/common/util"
	constant "github.com/0glabs/0g-serving-broker/inference/const"
	"github.com/0glabs/0g-serving-broker/inference/model"
	"github.com/0glabs/0g-serving-broker/inference/monitor"
)

// imageResponseData mirrors the OpenAI image object inside data[].
type imageResponseData struct {
	B64JSON       string `json:"b64_json,omitempty"`
	URL           string `json:"url,omitempty"`
	RevisedPrompt string `json:"revised_prompt,omitempty"`
}

// imageResponseEnvelope is the top-level OpenAI image response shape.
type imageResponseEnvelope struct {
	Created int64               `json:"created"`
	Data    []imageResponseData `json:"data"`
}

// GetTextToImageInputFeeAndImageNum gets input fee and imageNum for text-to-image generation
func (c *Ctrl) GetTextToImageInputFeeAndImageNum(reqBody []byte) (string, int64, error) {
	var request map[string]interface{}
	if err := json.Unmarshal(reqBody, &request); err != nil {
		return "", 0, errors.Wrap(err, "failed to unmarshal request body")
	}

	// Get imageNum parameter (prefer "imageNum", fallback to "num_inference_imageNum")
	imageNum := int64(1) // default to 1
	if imageNumVal, exists := request["n"]; exists {
		if imageNumFloat, ok := imageNumVal.(float64); ok {
			imageNum = int64(imageNumFloat)
		}
	}

	// Input fee is fixed at 0 (like zgStorage)
	expectedInputFee := "0"

	return expectedInputFee, imageNum, nil
}

// handleTextToImageResponse handles image generation response.
//
// Flow:
//  1. Read the provider response (always b64_json — enforced in PrepareHTTPRequest).
//  2. Decode each image and sign sha256(originalClientReq):sha256(img0),...
//  3. If the original client requested URL format, persist images to the local
//     image store and rewrite the response with broker-served URLs before sending
//     to the client.  Otherwise pass the b64 response through unchanged.
func (c *Ctrl) handleTextToImageResponse(ctx *gin.Context, resp *http.Response, account model.User, outputPrice string, reqBody []byte, reqModel model.Request) error {
	defer resp.Body.Close()

	chatKey := uuid.NewString()
	ctx.Writer.Header().Set("ZG-Res-Key", chatKey)

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		c.handleBrokerError(ctx, err, "read image response body")
		return err
	}

	// Resolve the original client request body (pre-b64 rewrite) for signing.
	sigReqBody := reqBody
	if v, ok := ctx.Get("clientReqBody"); ok {
		if orig, ok := v.([]byte); ok {
			sigReqBody = orig
		}
	}

	// Extract decoded image bytes; fall back to signing the full response body
	// if the provider did not return b64_json (e.g. multipart provider quirks).
	images, extractErr := extractB64Images(body)

	// Determine what the client originally asked for.
	originalFormat, _ := ctx.Get("clientResponseFormat")
	wantURL := originalFormat == "url"

	// If the client asked for url but the provider returned something we can't
	// decode (non-b64 envelope, empty array), refuse the response rather than
	// passing provider bytes through — they may contain LAN-private URLs.
	if wantURL && (extractErr != nil || len(images) == 0) {
		ctx.Set("ignoreError", true)
		err := fmt.Errorf("provider returned non-b64 image response, refusing to forward (may contain LAN-private URLs): %w", extractErr)
		c.handleBrokerError(ctx, err, "image response for response_format=url")
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

	// Attempt to return image to client. If client disconnected, continue to billing.
	if _, writeErr := ctx.Writer.Write(clientBody); writeErr != nil {
		if c.isClientDisconnectError(writeErr) {
			ctx.Set("ignoreError", true)
			c.logger.Warnf("Client disconnected during text-to-image response, billing for completed response (%d bytes)", len(body))
		} else {
			c.handleBrokerError(ctx, writeErr, "write image response")
		}
	}

	if !c.Service.TargetSeparated {
		c.logger.Debug("LLM server in the same network, signing text-to-image response")
		if extractErr == nil && len(images) > 0 {
			_ = c.signImageResponse(sigReqBody, images, chatKey)
		} else {
			c.logger.Warnf("No b64 images extracted, falling back to full-body signature: %v", extractErr)
			_ = c.signChatWithKey(sigReqBody, body, chatKey)
		}
	}

	// Skip billing for whitelisted users, but record whitelist traffic metrics
	if reqModel.IsWhitelisted {
		monitor.RecordTokens("text-to-image", 0, reqModel.OutputCount)
		monitor.RecordWhitelistTokens("text-to-image", 0, reqModel.OutputCount)
		return nil
	}

	// Get imageNum from request for billing
	imageNum := reqModel.OutputCount // previously stored imageNum count

	// Calculate output fee: imageNum × price per image
	outputFee, err := util.Multiply(outputPrice, imageNum)
	if err != nil {
		return errors.Wrap(err, "calculate output fee based on imageNum")
	}

	if err := c.db.UpdateRequestFeesAndCount(reqModel.RequestHash, outputFee.String(), outputFee.String(), imageNum); err != nil {
		return errors.Wrap(err, "update request fees and count in database")
	}

	monitor.RecordTokens("text-to-image", 0, imageNum)
	return nil
}

// extractB64Images parses an OpenAI-style image response envelope and decodes
// each data[i].b64_json into raw bytes.  Returns an error if parsing fails or
// no b64 images are present.
func extractB64Images(body []byte) ([][]byte, error) {
	var envelope imageResponseEnvelope
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, fmt.Errorf("unmarshal image response: %w", err)
	}
	if len(envelope.Data) == 0 {
		return nil, fmt.Errorf("image response has empty data array")
	}
	images := make([][]byte, 0, len(envelope.Data))
	for i, d := range envelope.Data {
		if d.B64JSON == "" {
			return nil, fmt.Errorf("data[%d] missing b64_json field", i)
		}
		img, err := base64.StdEncoding.DecodeString(d.B64JSON)
		if err != nil {
			return nil, fmt.Errorf("decode data[%d].b64_json: %w", i, err)
		}
		images = append(images, img)
	}
	return images, nil
}

// buildURLResponse replaces each data[i].b64_json with a broker-served URL while
// preserving all other fields (e.g. revised_prompt, created).
//
// The URL base is derived from the operator-configured service.servingUrl so it
// matches the public URL the provider registered on-chain. Returns an error if
// servingUrl is missing or malformed; the caller falls back to b64 on error.
func buildURLResponse(body []byte, chatKey string, count int, servingURL string) ([]byte, error) {
	var envelope imageResponseEnvelope
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, fmt.Errorf("unmarshal image response: %w", err)
	}

	if servingURL == "" {
		return nil, fmt.Errorf("service.servingUrl is not configured")
	}
	u, err := url.Parse(servingURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("service.servingUrl %q is not a valid absolute URL", servingURL)
	}
	base := strings.TrimRight(servingURL, "/") + constant.ServicePrefix + "/images/" + chatKey + "/"

	for i := range envelope.Data {
		if i >= count {
			break
		}
		envelope.Data[i] = imageResponseData{
			URL:           base + strconv.Itoa(i),
			RevisedPrompt: envelope.Data[i].RevisedPrompt,
		}
	}

	out, err := json.Marshal(envelope)
	if err != nil {
		return nil, fmt.Errorf("marshal URL response: %w", err)
	}
	return out, nil
}
