package ctrl

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"compress/flate"
	"compress/gzip"

	"github.com/google/uuid"

	"github.com/andybalholm/brotli"
	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/gin-gonic/gin"

	"github.com/0glabs/0g-serving-broker/common/errors"
	"github.com/0glabs/0g-serving-broker/common/util"
	"github.com/0glabs/0g-serving-broker/inference/model"
)

const ChatPrefix = "chat"

type SigningAlgo int

const (
	ECDSA SigningAlgo = iota
)

func (r SigningAlgo) String() string {
	return [...]string{"ecdsa"}[r]
}

type ChatSignature struct {
	Text                string         `json:"text"`
	SignatureEcdsa      string         `json:"signature"`
	SigningAddressEcdsa common.Address `json:"signing_address"`
	SigningAlgo         string         `json:"signing_algo"`
}

type RequestBody struct {
	Messages []Message `json:"messages"`
}

type CompletionChunk struct {
	ID      string   `json:"id"`
	Object  string   `json:"object"`
	Created int64    `json:"created"`
	Model   string   `json:"model"`
	Choices []Choice `json:"choices"`
	Usage   *Usage   `json:"usage,omitempty"`
}

type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

type Choice struct {
	Message Message `json:"message"`
	Delta   struct {
		Content string `json:"content"`
	} `json:"delta"`
	FinishReason string `json:"finish_reason"`
}

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

func (c *Ctrl) handleChatbotResponse(ctx *gin.Context, resp *http.Response, account model.User, outputPrice string, reqBody []byte, reqModel model.Request) error {
	isStream, err := isStream(reqBody)
	if err != nil {
		c.handleBrokerError(ctx, err, "check if stream")
		return err
	}
	if !isStream {
		return c.handleChargingResponse(ctx, resp, account, outputPrice, reqBody, reqModel)
	} else {
		return c.handleChargingStreamResponse(ctx, resp, account, outputPrice, reqBody, reqModel)
	}
}

func (c *Ctrl) handleChargingResponse(ctx *gin.Context, resp *http.Response, account model.User, outputPrice string, reqBody []byte, reqModel model.Request) error {
	defer resp.Body.Close()

	chatKey := uuid.NewString()

	if !c.Service.TargetSeparated {
		c.logger.Debug("LLM server in the same network, setting ZG-Res-Key header")
		ctx.Writer.Header().Set("ZG-Res-Key", chatKey)
	}

	var rawBody bytes.Buffer
	reader := bufio.NewReader(io.TeeReader(resp.Body, &rawBody))

	_, err := reader.WriteTo(ctx.Writer)
	if err != nil {
		c.handleBrokerError(ctx, err, "read from body")
		return err
	}

	if err := c.decodeAndProcess(ctx, rawBody.Bytes(), resp.Header.Get("Content-Encoding"), account, outputPrice, false, reqBody, reqModel, rawBody.Bytes(), chatKey); err != nil {
		c.logger.Errorf("decode and process failed: %v", err)
		return err
	}

	return nil
}

func (c *Ctrl) handleChargingStreamResponse(ctx *gin.Context, resp *http.Response, account model.User, outputPrice string, reqBody []byte, reqModel model.Request) error {
	defer resp.Body.Close()

	chatKey := uuid.NewString()

	if !c.Service.TargetSeparated {
		c.logger.Debug("LLM server in the same network, setting ZG-Res-Key header for streaming response")
		ctx.Writer.Header().Set("ZG-Res-Key", chatKey)
	}

	var rawBody bytes.Buffer

	var streamErr error = nil
	var responseChunk []byte = nil
	var clientDisconnected bool = false
	var silentReadBytes int64 = 0
	const maxSilentReadBytes int64 = 10 * 1024 * 1024 // 10MB limit to prevent abuse

	ctx.Stream(func(w io.Writer) bool {
		reader := bufio.NewReader(io.TeeReader(resp.Body, &rawBody))

		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				if err == io.EOF {
					return false
				}
				c.handleBrokerError(ctx, err, "read from body")
				streamErr = err
				return false
			}

			if responseChunk == nil {
				responseChunk = []byte(strings.TrimSpace(strings.TrimPrefix(line, "data: ")))
			}

			// Only write to client if still connected
			if !clientDisconnected {
				_, streamErr = w.Write([]byte(line))
				if streamErr != nil {
					// Check if this is a client disconnection error
					if c.isClientDisconnectError(streamErr) {
						// Mark as ignorable and continue reading silently for complete billing
						ctx.Set("ignoreError", true)
						clientDisconnected = true
						c.logger.Warnf("Client disconnected, continuing to read from backend for accurate billing")
						// Don't return false, continue reading
					} else {
						// For other errors, stop immediately
						c.handleBrokerError(ctx, streamErr, "write to stream")
						return false
					}
				} else {
					ctx.Writer.Flush()
				}
			}

			// If client disconnected, count silent read bytes and check limit
			if clientDisconnected {
				silentReadBytes += int64(len(line))
				if silentReadBytes > maxSilentReadBytes {
					c.logger.Warnf("Reached max silent read limit (%d bytes), stopping to prevent abuse", maxSilentReadBytes)
					return false
				}
			}
		}
	})

	// Process billing regardless of stream error
	// If client disconnected but we continued reading, we have complete data for accurate billing
	// If there was a read error from backend, we still bill for what we received
	if rawBody.Len() > 0 {
		if streamErr != nil {
			if clientDisconnected {
				c.logger.Infof("Client disconnected but backend response completed, billing with accurate usage data (%d bytes received)", rawBody.Len())
			} else {
				c.logger.Warnf("Stream error occurred, billing for partial response (received %d bytes): %v", rawBody.Len(), streamErr)
			}
		}

		if err := c.decodeAndProcess(ctx, rawBody.Bytes(), resp.Header.Get("Content-Encoding"), account, outputPrice, true, reqBody, reqModel, responseChunk, chatKey); err != nil {
			c.logger.Errorf("Failed to process response for billing: %v", err)
			// If we had a stream error, return it; otherwise return the decode error
			if streamErr != nil {
				return streamErr
			}
			c.handleBrokerError(ctx, err, "decode and process")
			return err
		}
	}

	// If there was a stream error but no data, return the error
	if streamErr != nil && rawBody.Len() == 0 {
		return streamErr
	}

	return nil
}
func (c *Ctrl) decodeAndProcess(ctx context.Context, data []byte, encodingType string, account model.User, outputPrice string, isStream bool, reqBody []byte, reqModel model.Request, respChunk []byte, chatKey string) error {
	// Decode the raw data
	decodeReader := initializeReader(bytes.NewReader(data), encodingType)
	decodedBody, err := io.ReadAll(decodeReader)
	if err != nil {
		return errors.Wrap(err, "Error decoding body")
	}

	var output string
	var usage *Usage

	if !isStream {
		// Detect format for non-stream response
		format := detectResponseFormat(decodedBody)
		if format == FormatLiteLLM {
			if err := c.processLiteLLMSingleResponse(ctx, decodedBody, outputPrice, &output, reqModel.RequestHash, &usage, reqModel.IsWhitelisted); err != nil {
				return err
			}
		} else {
			if err := c.processSingleResponse(ctx, decodedBody, outputPrice, &output, reqModel.RequestHash, &usage, reqModel.IsWhitelisted); err != nil {
				return err
			}
		}
	} else {
		// Parse and decode data line by line for streams
		lines := bytes.Split(decodedBody, []byte("\n"))

		// Detect stream format from first non-empty line
		var format ResponseFormat = FormatOpenAI
		for _, line := range lines {
			if !isLineEmpty(line) {
				format = detectStreamFormat(line)
				break
			}
		}

		if format == FormatLiteLLM {
			if err := c.processLiteLLMStream(ctx, lines, outputPrice, &output, &usage, reqModel.RequestHash, reqModel.IsWhitelisted); err != nil {
				return err
			}
		} else {
			if err := c.processOpenAIStream(ctx, lines, outputPrice, &output, &usage, reqModel.RequestHash, reqModel.IsWhitelisted); err != nil {
				return err
			}
		}
	}

	if !c.Service.TargetSeparated {
		c.logger.Debug("LLM server in the same network, signing chat response")
		if err := c.signChatWithKey(reqBody, data, chatKey); err != nil {
			return err
		}
	}

	return nil
}

func (c *Ctrl) signChatWithKey(reqBody, respData []byte, chatKey string) error {
	hashAndEncode := func(b []byte) string {
		h := sha256.Sum256(b)
		return hex.EncodeToString(h[:])
	}

	requestSha256 := hashAndEncode(reqBody)
	responseSha256 := hashAndEncode(respData)

	c.logger.Debugf("requestSha256: %s, responseSha256: %s, signer address %s", requestSha256, responseSha256, c.teeService.Address.Hex())
	text := fmt.Sprintf("%s:%s", requestSha256, responseSha256)
	sig, err := crypto.Sign(accounts.TextHash([]byte(text)), c.teeService.ProviderSigner)
	if err != nil {
		return err
	}

	if sig[64] == 0 || sig[64] == 1 {
		sig[64] += 27
	}

	chatSignature := ChatSignature{
		Text:                text,
		SignatureEcdsa:      hexutil.Encode(sig),
		SigningAddressEcdsa: c.teeService.Address,
		SigningAlgo:         ECDSA.String(),
	}

	key := c.chatCacheKey(chatKey)
	c.logger.Debugf("key: %v, chat signature: %v", key, chatSignature)
	c.svcCache.Set(key, chatSignature, c.chatCacheExpiration)
	return nil
}

func (*Ctrl) chatCacheKey(chatID string) string {
	return fmt.Sprintf("%s:%s", ChatPrefix, chatID)
}

func (c *Ctrl) processSingleResponse(ctx context.Context, decodedBody []byte, outputPrice string, output *string, requestHash string, usage **Usage, isWhitelisted bool) error {
	line := bytes.TrimPrefix(decodedBody, []byte("data: "))
	var chunk CompletionChunk
	if err := json.Unmarshal(line, &chunk); err != nil {
		return errors.Wrap(err, "Error unmarshaling JSON")
	}

	for _, choice := range chunk.Choices {
		*output += choice.Message.Content
	}

	// Skip billing for whitelisted users
	if isWhitelisted {
		if chunk.Usage != nil {
			*usage = chunk.Usage
		}
		return nil
	}

	// For non-stream responses, usage info is in the same response
	if chunk.Usage != nil {
		*usage = chunk.Usage
		// Get service price from cache/contract instead of config
		service, err := c.GetCachedService(ctx)
		if err != nil {
			return errors.Wrap(err, "get cached service for single response billing")
		}
		return c.updateAccountWithUsage(ctx, chunk.Usage, service.OutputPrice, requestHash, service.InputPrice)
	}

	// Fallback to old logic if no usage info
	// This check is defensive - whitelisted users should have already returned at L326-331
	// but we add this for code clarity and future-proofing
	if isWhitelisted {
		return nil
	}
	return c.updateAccountWithOutput(ctx, *output, outputPrice, requestHash)
}

func (c *Ctrl) processLine(line []byte) (string, error) {
	line = bytes.TrimPrefix(line, []byte("data: "))
	var chunk CompletionChunk
	if err := json.Unmarshal(line, &chunk); err != nil {
		return "", errors.Wrap(err, "Error unmarshaling JSON")
	}

	var outputChunk string
	for _, choice := range chunk.Choices {
		outputChunk += choice.Delta.Content
	}
	return outputChunk, nil
}

func (c *Ctrl) finalizeResponse(ctx context.Context, output string, outputPrice string, requestHash string) error {
	return c.updateAccountWithOutput(ctx, output, outputPrice, requestHash)
}

// extractUsageFromLine extracts usage information from a stream response line
func (c *Ctrl) extractUsageFromLine(line []byte) *Usage {
	line = bytes.TrimPrefix(line, []byte("data: "))
	var chunk CompletionChunk
	if err := json.Unmarshal(line, &chunk); err != nil {
		return nil
	}
	return chunk.Usage
}

// finalizeResponseWithUsage updates the account with accurate token counts from LLM
func (c *Ctrl) finalizeResponseWithUsage(ctx context.Context, usage *Usage, outputPrice string, requestHash string, inputPrice string) error {
	return c.updateAccountWithUsage(ctx, usage, outputPrice, requestHash, inputPrice)
}

// updateAccountWithUsage updates the request with accurate token counts from the LLM response
func (c *Ctrl) updateAccountWithUsage(_ context.Context, usage *Usage, outputPrice string, requestHash string, inputPrice string) error {
	// Calculate actual fees based on LLM-provided token counts
	inputFee, err := util.Multiply(inputPrice, int64(usage.PromptTokens))
	if err != nil {
		return errors.Wrap(err, "Error calculating input fee from actual tokens")
	}

	outputFee, err := util.Multiply(outputPrice, int64(usage.CompletionTokens))
	if err != nil {
		return errors.Wrap(err, "Error calculating output fee from actual tokens")
	}

	totalFee, err := util.Add(inputFee, outputFee)
	if err != nil {
		return errors.Wrap(err, "Error calculating total fee")
	}

	// Update the request with accurate token counts and fees
	if err := c.db.UpdateRequestWithAccurateTokens(requestHash, inputFee.String(), outputFee.String(), totalFee.String(),
		int64(usage.PromptTokens), int64(usage.CompletionTokens)); err != nil {
		return errors.Wrap(err, "Error updating request with accurate tokens")
	}

	return nil
}

// updateAccountWithOutput is the FALLBACK method when LLM doesn't provide usage information
// It estimates tokens by counting space-separated words (inaccurate but better than nothing)
// This should only be used when the LLM response doesn't include usage data
func (c *Ctrl) updateAccountWithOutput(_ context.Context, output string, outputPrice string, requestHash string) error {
	// WARNING: This is a rough estimation based on word count, not actual tokens
	outputCount := int64(len(strings.Fields(output)))
	lastResponseFee, err := util.Multiply(outputPrice, outputCount)
	if err != nil {
		return errors.Wrap(err, "Error calculating last response fee")
	}

	request, err := c.db.GetRequest(requestHash)
	if err != nil {
		return errors.Wrap(err, "Error fetching request")
	}

	fee, err := util.Add(lastResponseFee, request.InputFee)
	if err != nil {
		return err
	}

	// Update the request's output fee, total fee, and output count
	// No longer update unsettled fee in user table to avoid concurrency issues
	if err := c.db.UpdateRequestFeesAndCount(requestHash, lastResponseFee.String(), fee.String(), outputCount); err != nil {
		return errors.Wrap(err, "Error updating request fees and count")
	}

	return nil
}

func isStreamDone(line []byte) bool {
	return bytes.Equal(line, []byte("data: [DONE]"))
}

func isLineEmpty(line []byte) bool {
	return bytes.Equal(line, []byte(""))
}

func isStream(body []byte) (bool, error) {
	var bodyMap map[string]interface{}

	err := json.Unmarshal(body, &bodyMap)
	if err != nil {
		return false, errors.Wrap(err, "failed to parse JSON body")
	}

	if stream, ok := bodyMap["stream"]; ok {
		if streamBool, ok := stream.(bool); ok && streamBool {
			return true, nil
		}
	}

	return false, nil
}

func initializeReader(rawReader io.Reader, encodingType string) io.Reader {
	switch encodingType {
	case "br":
		return brotli.NewReader(rawReader)
	case "gzip":
		gzReader, err := gzip.NewReader(rawReader)
		if err != nil {
			return rawReader // 回退到未压缩的内容处理
		}
		return gzReader
	case "deflate":
		return flate.NewReader(rawReader)
	default:
		return rawReader
	}
}

// detectResponseFormat determines the format of the response
func detectResponseFormat(data []byte) ResponseFormat {
	// Try to parse as JSON first
	var temp map[string]interface{}
	if err := json.Unmarshal(data, &temp); err != nil {
		return FormatOpenAI // default to OpenAI for non-JSON (stream)
	}

	// Check for LiteLLM specific fields
	if _, hasType := temp["type"]; hasType {
		if _, hasContent := temp["content"]; hasContent {
			// LiteLLM format has both "type" and "content" fields
			return FormatLiteLLM
		}
	}

	// Check for OpenAI specific fields
	if _, hasChoices := temp["choices"]; hasChoices {
		return FormatOpenAI
	}

	return FormatOpenAI // default to OpenAI
}

// detectStreamFormat detects stream format from first line
func detectStreamFormat(line []byte) ResponseFormat {
	// LiteLLM uses SSE format with "event:" prefix
	if bytes.HasPrefix(line, []byte("event:")) {
		return FormatLiteLLM
	}
	// OpenAI uses "data:" prefix
	if bytes.HasPrefix(line, []byte("data:")) {
		return FormatOpenAI
	}
	return FormatOpenAI // default
}

// isClientDisconnectError checks if the error is caused by client disconnection
func (c *Ctrl) isClientDisconnectError(err error) bool {
	if err == nil {
		return false
	}
	errMsg := err.Error()
	// Common client disconnection error patterns
	return strings.Contains(errMsg, "broken pipe") ||
		strings.Contains(errMsg, "connection reset by peer") ||
		strings.Contains(errMsg, "client disconnected") ||
		strings.Contains(errMsg, "http: request body too large")
}

// processOpenAIStream processes OpenAI-format streaming responses
func (c *Ctrl) processOpenAIStream(ctx context.Context, lines [][]byte, outputPrice string, output *string, usage **Usage, requestHash string, isWhitelisted bool) error {
	for _, line := range lines {
		if isStreamDone(line) {
			// Skip billing for whitelisted users
			if isWhitelisted {
				break
			}

			// For stream responses, usage info comes before [DONE]
			if *usage != nil {
				// Get service price from cache/contract instead of config
				service, err := c.GetCachedService(ctx)
				if err != nil {
					return errors.Wrap(err, "get cached service for stream response billing")
				}
				c.finalizeResponseWithUsage(ctx, *usage, service.OutputPrice, requestHash, service.InputPrice)
				break
			}
			c.finalizeResponse(ctx, *output, outputPrice, requestHash)
			break
		}

		// Skip empty lines
		if isLineEmpty(line) {
			continue
		}

		// Check if this line contains usage information
		if extractedUsage := c.extractUsageFromLine(line); extractedUsage != nil {
			*usage = extractedUsage
			continue
		}

		chunkOutput, err := c.processLine(line)
		if err != nil {
			return err
		}
		*output += chunkOutput
	}

	return nil
}
