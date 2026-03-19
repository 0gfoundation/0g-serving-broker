package ctrl

import (
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/0glabs/0g-serving-broker/common/errors"
	"github.com/0glabs/0g-serving-broker/common/util"
	"github.com/0glabs/0g-serving-broker/inference/model"
)

// defaultVideoSeconds is used when the request does not specify a duration.
const defaultVideoSeconds = 10

// defaultVideoSize is used when the request does not specify a resolution.
const defaultVideoSize = "720x1280"

// GetVideoGenerationInputFeeAndOutputCount extracts billing parameters from a video generation request.
//
// Billing formula: fee = seconds × sizeRatio × outputPrice
//
// To fit the existing billing infrastructure (outputPrice × outputCount), the "output count"
// is computed as: ceil(seconds × sizeRatio). This represents "effective seconds" at baseline
// resolution cost.
//
// Parameters are extracted from multipart/form-data (the only format supported by the OpenAI Video API):
//   - seconds: video duration (default: 10)
//   - size: output resolution (default: 720x1280)
func (c *Ctrl) GetVideoGenerationInputFeeAndOutputCount(reqBody []byte) (string, int64, error) {
	seconds, size := parseVideoSecondsAndSize(reqBody)

	sizeRatio := c.Service.GetVideoSizeRatio(size)

	// Effective output count = ceil(seconds × sizeRatio)
	effectiveCount := int64(math.Ceil(float64(seconds) * sizeRatio))
	if effectiveCount < 1 {
		effectiveCount = 1
	}

	return "0", effectiveCount, nil
}

// parseVideoSecondsAndSize extracts the "seconds" and "size" fields from a video generation request.
// Only multipart/form-data is supported (matching the OpenAI Video API).
// Returns defaults if fields are missing or unparseable.
func parseVideoSecondsAndSize(reqBody []byte) (seconds int64, size string) {
	seconds = defaultVideoSeconds
	size = defaultVideoSize

	if len(reqBody) == 0 {
		return
	}

	bodyStr := string(reqBody)
	if val := parseMultipartField(bodyStr, "seconds"); val != "" {
		if parsed, err := strconv.ParseInt(val, 10, 64); err == nil && parsed > 0 {
			seconds = parsed
		}
	}
	if val := parseMultipartField(bodyStr, "size"); val != "" {
		size = val
	}

	return
}

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

// handleVideoGenerationResponse handles the POST /videos response from the provider.
// The provider returns a JSON object with the video job metadata (id, status, etc.).
// Billing: fee = effectiveOutputCount × outputPrice (where effectiveOutputCount = seconds × sizeRatio).
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
		_ = c.signChatWithKey(reqBody, body, chatKey)
	}

	if reqModel.IsWhitelisted {
		return nil
	}

	// OutputCount was pre-computed as ceil(seconds × sizeRatio) during request extraction
	outputCount := reqModel.OutputCount
	outputFee, err := util.Multiply(outputPrice, outputCount)
	if err != nil {
		return errors.Wrap(err, "calculate output fee for video generation")
	}

	if err := c.db.UpdateRequestFeesAndCount(reqModel.RequestHash, outputFee.String(), outputFee.String(), outputCount); err != nil {
		return errors.Wrap(err, "update request fees and count in database")
	}

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
