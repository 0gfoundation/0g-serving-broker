package ctrl

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/0glabs/0g-serving-broker/common/errors"
	"github.com/0glabs/0g-serving-broker/common/util"
	"github.com/0glabs/0g-serving-broker/inference/model"
	"github.com/0glabs/0g-serving-broker/inference/monitor"
)

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

// handleTextToImageResponse handles image generation response
func (c *Ctrl) handleTextToImageResponse(ctx *gin.Context, resp *http.Response, account model.User, outputPrice string, reqBody []byte, reqModel model.Request) error {
	defer resp.Body.Close()

	chatKey := uuid.NewString()
	ctx.Writer.Header().Set("ZG-Res-Key", chatKey)

	// Read and return image data
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		c.handleBrokerError(ctx, err, "read image response body")
		return err
	}

	// Attempt to return image to client. If client disconnected, continue to billing.
	if _, writeErr := ctx.Writer.Write(body); writeErr != nil {
		if c.isClientDisconnectError(writeErr) {
			ctx.Set("ignoreError", true)
			c.logger.Warnf("Client disconnected during text-to-image response, billing for completed response (%d bytes)", len(body))
		} else {
			c.handleBrokerError(ctx, writeErr, "write image response")
			// Still proceed to billing below
		}
	}

	if !c.Service.TargetSeparated {
		c.logger.Debug("LLM server in the same network, signing text-to-image response")
		_ = c.signChatWithKey(reqBody, body, chatKey)
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
