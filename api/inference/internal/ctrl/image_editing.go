package ctrl

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/0glabs/0g-serving-broker/common/errors"
	"github.com/0glabs/0g-serving-broker/common/util"
	"github.com/0glabs/0g-serving-broker/inference/model"
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

// parseMultipartImageNum extracts the "n" parameter from multipart/form-data
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

// handleImageEditingResponse handles the image editing response
// This function:
// 1. Reads and returns the edited image data
// 2. Signs the response (if TEE is in the same network)
// 3. Calculates fees based on output image count
// 4. Updates the database with fee records
func (c *Ctrl) handleImageEditingResponse(ctx *gin.Context, resp *http.Response, account model.User, outputPrice string, reqBody []byte, reqModel model.Request) error {
	defer resp.Body.Close()

	// Generate unique response key for signature verification
	chatKey := uuid.NewString()
	ctx.Writer.Header().Set("ZG-Res-Key", chatKey)

	// Read image response data
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		c.handleBrokerError(ctx, err, "read image editing response body")
		return err
	}

	// Log response size for monitoring and debugging
	responseSizeMB := float64(len(body)) / (1024 * 1024)
	c.logger.Infof("Image editing response: size=%.2f MB, user=%s, imageCount=%d",
		responseSizeMB, reqModel.UserAddress, reqModel.OutputCount)

	// Return image data to client
	if _, err := ctx.Writer.Write(body); err != nil {
		// Check if error is due to client disconnection (broken pipe, connection reset)
		// These are expected errors when client times out or cancels request
		errMsg := err.Error()
		if isClientDisconnectError(errMsg) {
			c.logger.Warnf("Client disconnected before receiving full response: %v", err)
			// Don't return error - continue with billing since response was generated
		} else {
			// Other write errors are unexpected
			c.handleBrokerError(ctx, err, "write image editing response")
			return err
		}
	}

	// Sign response if LLM server is in the same network
	// This allows clients to verify the response is from an authorized provider
	if !c.Service.TargetSeparated {
		c.logger.Debug("LLM server in the same network, signing image-editing response")
		_ = c.signChatWithKey(reqBody, body, chatKey)
	}

	// Skip billing for whitelisted users
	if reqModel.IsWhitelisted {
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

	return nil
}

// isClientDisconnectError checks if an error is due to client disconnection
// Returns true for errors like "broken pipe", "connection reset by peer", etc.
func isClientDisconnectError(errMsg string) bool {
	// Common client disconnect error patterns
	disconnectPatterns := []string{
		"broken pipe",
		"connection reset by peer",
		"write: connection reset",
		"write: broken pipe",
		"EOF",
		"client disconnected",
	}

	// Convert to lowercase for case-insensitive matching
	errMsgLower := strings.ToLower(errMsg)
	for _, pattern := range disconnectPatterns {
		if strings.Contains(errMsgLower, pattern) {
			return true
		}
	}
	return false
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
