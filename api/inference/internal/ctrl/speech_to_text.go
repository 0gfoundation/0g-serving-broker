package ctrl

import (
	"bufio"
	"bytes"
	"compress/flate"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"net/http"

	"github.com/andybalholm/brotli"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/0glabs/0g-serving-broker/common/errors"
	"github.com/0glabs/0g-serving-broker/common/util"
	"github.com/0glabs/0g-serving-broker/inference/model"
)

// SpeechToTextUsage represents the usage information from speech-to-text API
type SpeechToTextUsage struct {
	Type               string                    `json:"type"`
	TotalTokens        int                       `json:"total_tokens"`
	InputTokens        int                       `json:"input_tokens"`
	InputTokenDetails  SpeechToTextTokenDetails  `json:"input_token_details"`
	OutputTokens       int                       `json:"output_tokens"`
}

// SpeechToTextTokenDetails contains detailed token information
type SpeechToTextTokenDetails struct {
	TextTokens  int `json:"text_tokens"`
	AudioTokens int `json:"audio_tokens"`
}

// SpeechToTextResponse represents the transcription response
type SpeechToTextResponse struct {
	Text  string             `json:"text"`
	Usage *SpeechToTextUsage `json:"usage,omitempty"`
}

// SpeechToTextStreamChunk represents a streaming transcription chunk
type SpeechToTextStreamChunk struct {
	Type  string             `json:"type"`
	Delta string             `json:"delta,omitempty"`
	Text  string             `json:"text,omitempty"`
	Usage *SpeechToTextUsage `json:"usage,omitempty"`
}

// handleSpeechToTextResponse handles speech-to-text transcription response
func (c *Ctrl) handleSpeechToTextResponse(ctx *gin.Context, resp *http.Response, _ model.User, _ int64, reqBody []byte, reqModel model.Request) error {
	// Check if request is for streaming by parsing the request body
	isStream := c.isSpeechToTextStream(reqBody)
	
	if !isStream {
		return c.handleNonStreamingSpeechToText(ctx, resp, reqBody, reqModel)
	} else {
		return c.handleStreamingSpeechToText(ctx, resp, reqBody, reqModel)
	}
}

// handleNonStreamingSpeechToText handles non-streaming speech-to-text response
func (c *Ctrl) handleNonStreamingSpeechToText(ctx *gin.Context, resp *http.Response, reqBody []byte, reqModel model.Request) error {
	defer resp.Body.Close()

	chatKey := uuid.NewString()
	
	if !c.Service.TargetSeparated {
		c.logger.Debug("LLM server in the same network, setting ZG-Res-Key header")
		ctx.Writer.Header().Set("ZG-Res-Key", chatKey)
	}

	// Read response body
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		c.handleBrokerError(ctx, err, "read transcription response body")
		return err
	}

	// Write raw response to client first
	if _, err := ctx.Writer.Write(body); err != nil {
		c.handleBrokerError(ctx, err, "write transcription response")
		return err
	}

	// Decompress body if needed for parsing
	contentEncoding := resp.Header.Get("Content-Encoding")
	decompressedReader := initializeSpeechReader(bytes.NewReader(body), contentEncoding)
	decompressedBody, err := io.ReadAll(decompressedReader)
	if err != nil {
		c.logger.Warnf("Failed to decompress speech-to-text response: %v", err)
		// Fallback to estimated billing if decompression fails
		return c.updateSpeechToTextFallback(reqModel, string(decompressedBody))
	}

	// Debug: log response content
	c.logger.Debugf("Decompressed response length: %d", len(decompressedBody))
	if len(decompressedBody) > 0 {
		previewLen := len(decompressedBody)
		if previewLen > 200 {
			previewLen = 200
		}
		c.logger.Debugf("Response preview (first %d chars): %s", previewLen, string(decompressedBody[:previewLen]))
	}

	// Parse response to extract usage
	var transcriptionResp SpeechToTextResponse
	if err := json.Unmarshal(decompressedBody, &transcriptionResp); err != nil {
		c.logger.Warnf("Failed to parse speech-to-text response for usage extraction: %v", err)
		c.logger.Debugf("Raw response causing parse error: %s", string(decompressedBody))
		// Fallback to estimated billing if parsing fails
		return c.updateSpeechToTextFallback(reqModel, string(decompressedBody))
	}

	// Sign response if needed
	if !c.Service.TargetSeparated {
		c.logger.Debug("LLM server in the same network, signing speech-to-text response")
		_ = c.signChatWithKey(reqBody, body, chatKey)
	}

	// Update billing with actual usage data
	if transcriptionResp.Usage != nil {
		return c.updateSpeechToTextWithUsage(context.Background(), transcriptionResp.Usage, reqModel.RequestHash)
	}

	// Fallback if no usage data
	return c.updateSpeechToTextFallback(reqModel, string(decompressedBody))
}

// handleStreamingSpeechToText handles streaming speech-to-text response
func (c *Ctrl) handleStreamingSpeechToText(ctx *gin.Context, resp *http.Response, reqBody []byte, reqModel model.Request) error {
	defer resp.Body.Close()

	chatKey := uuid.NewString()

	if !c.Service.TargetSeparated {
		c.logger.Debug("LLM server in the same network, setting ZG-Res-Key header for streaming response")
		ctx.Writer.Header().Set("ZG-Res-Key", chatKey)
	}

	var rawBody bytes.Buffer
	var usage *SpeechToTextUsage
	var streamErr error

	ctx.Stream(func(w io.Writer) bool {
		// Apply decompression if needed
		contentEncoding := resp.Header.Get("Content-Encoding")
		decompressedReader := initializeSpeechReader(resp.Body, contentEncoding)
		reader := bufio.NewReader(io.TeeReader(decompressedReader, &rawBody))

		for {
			line, err := reader.ReadBytes('\n')
			if err != nil {
				if err == io.EOF {
					return false
				}
				c.handleBrokerError(ctx, err, "read from streaming body")
				streamErr = err
				return false
			}

			// Parse JSON chunk
			var chunk SpeechToTextStreamChunk
			if err := json.Unmarshal(line, &chunk); err == nil {
				// Check if this is the final chunk with usage info
				if chunk.Type == "transcript.text.done" && chunk.Usage != nil {
					usage = chunk.Usage
				}
			}

			// Write line to client
			_, streamErr = w.Write(line)
			if streamErr != nil {
				c.handleBrokerError(ctx, streamErr, "write to stream")
				return false
			}

			ctx.Writer.Flush()

			// Check if stream is done
			if chunk.Type == "transcript.text.done" {
				return false
			}
		}
	})

	if streamErr != nil {
		return streamErr
	}

	// Sign response if needed
	if !c.Service.TargetSeparated {
		c.logger.Debug("LLM server in the same network, signing streaming speech-to-text response")
		_ = c.signChatWithKey(reqBody, rawBody.Bytes(), chatKey)
	}

	// Update billing
	if usage != nil {
		return c.updateSpeechToTextWithUsage(context.Background(), usage, reqModel.RequestHash)
	}

	// Fallback if no usage data
	return c.updateSpeechToTextFallback(reqModel, rawBody.String())
}

// updateSpeechToTextWithUsage updates the request with accurate token counts from the API response
func (c *Ctrl) updateSpeechToTextWithUsage(_ context.Context, usage *SpeechToTextUsage, requestHash string) error {
	// Calculate actual fees based on API-provided token counts
	inputFee, err := util.Multiply(c.Service.InputPrice, int64(usage.InputTokens))
	if err != nil {
		return errors.Wrap(err, "calculate input fee from actual tokens")
	}

	outputFee, err := util.Multiply(c.Service.OutputPrice, int64(usage.OutputTokens))
	if err != nil {
		return errors.Wrap(err, "calculate output fee from actual tokens")
	}

	totalFee, err := util.Add(inputFee, outputFee)
	if err != nil {
		return errors.Wrap(err, "calculate total fee")
	}

	// Update the request with accurate token counts and fees
	if err := c.db.UpdateRequestWithAccurateTokens(requestHash, inputFee.String(), outputFee.String(), totalFee.String(),
		int64(usage.InputTokens), int64(usage.OutputTokens)); err != nil {
		return errors.Wrap(err, "update request with accurate tokens")
	}

	return nil
}

/*
updateSpeechToTextFallback is used when no usage data is available.
If text is provided, billing is based on the number of words in the text.
Otherwise, falls back to a default estimation.
*/
func (c *Ctrl) updateSpeechToTextFallback(reqModel model.Request, text string) error {
	var estimatedOutputTokens int64 = 100 // default estimation

	if len(text) > 0 {
		// Count words: split by whitespace
		wordCount := int64(0)
		inWord := false
		for _, r := range text {
			if r == ' ' || r == '\n' || r == '\t' || r == '\r' {
				inWord = false
			} else if !inWord {
				inWord = true
				wordCount++
			}
		}
		if wordCount > 0 {
			estimatedOutputTokens = wordCount * 2 // assume 2 tokens per word
		}
	}

	outputFee, err := util.Multiply(c.Service.OutputPrice, estimatedOutputTokens)
	if err != nil {
		return errors.Wrap(err, "calculate output fee")
	}

	totalFee, err := util.Add(outputFee, reqModel.InputFee)
	if err != nil {
		return errors.Wrap(err, "calculate total fee")
	}

	if err := c.db.UpdateRequestFeesAndCount(reqModel.RequestHash, outputFee.String(), totalFee.String(), estimatedOutputTokens); err != nil {
		return errors.Wrap(err, "update request fees and count")
	}

	return nil
}

// isSpeechToTextStream checks if the request body contains stream parameter
func (c *Ctrl) isSpeechToTextStream(reqBody []byte) bool {
	// Parse multipart body to find stream parameter
	bodyStr := string(reqBody)
	
	// Look for stream parameter in multipart data
	// Pattern: name="stream"\r\n\r\ntrue
	isStream := contains(bodyStr, `name="stream"`) && 
		(contains(bodyStr, "\r\n\r\ntrue") || contains(bodyStr, "\ntrue"))
	
	c.logger.Debugf("Is streaming request: %t", isStream)
	return isStream
}

// contains is a simple helper to check if string contains substring
func contains(s, substr string) bool {
	return len(s) >= len(substr) && 
		(s == substr || len(s) > len(substr) && 
		(hasSubstring(s, substr)))
}

// hasSubstring checks if s contains substr
func hasSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// initializeSpeechReader returns a reader that handles compressed content
func initializeSpeechReader(rawReader io.Reader, encodingType string) io.Reader {
	switch encodingType {
	case "br":
		return brotli.NewReader(rawReader)
	case "gzip":
		gzReader, err := gzip.NewReader(rawReader)
		if err != nil {
			return rawReader // Fallback to uncompressed content
		}
		return gzReader
	case "deflate":
		return flate.NewReader(rawReader)
	default:
		return rawReader
	}
}
