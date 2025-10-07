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

// GetChatbotInputFeeAndCount returns both the input fee and count for efficient request creation
// Note: This returns an ESTIMATE based on message byte size for validation purposes
// The actual token count will be obtained from the LLM response
func (c *Ctrl) GetChatbotInputFeeAndCount(reqBody []byte) (string, int64, error) {
	inputCount, err := getInputCount(reqBody)
	if err != nil {
		return "", 0, errors.Wrap(err, "get input count")
	}

	expectedInputFee, err := util.Multiply(inputCount, c.Service.InputPrice)
	if err != nil {
		return "", 0, errors.Wrap(err, "calculate input fee")
	}
	return expectedInputFee.String(), inputCount, nil
}

// getInputCount provides an estimation of input tokens based on message byte size
// This is used ONLY for initial balance validation before the request is sent to LLM
// The actual token count from LLM response will replace this estimate
func getInputCount(reqBody []byte) (int64, error) {
	var bodyMap map[string]interface{}
	if err := json.Unmarshal(reqBody, &bodyMap); err != nil {
		return 0, fmt.Errorf("failed to unmarshal reqBody: %w", err)
	}
	messages, ok := bodyMap["messages"]
	if !ok {
		return 0, fmt.Errorf("messages field not found in reqBody")
	}
	messagesBytes, err := json.Marshal(messages)
	if err != nil {
		return 0, fmt.Errorf("failed to marshal messages: %w", err)
	}
	// Estimation based on message byte length for validation only
	return int64(len(messagesBytes)), nil
}

func (c *Ctrl) handleChatbotResponse(ctx *gin.Context, resp *http.Response, account model.User, outputPrice int64, reqBody []byte, reqModel model.Request) error {
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

func (c *Ctrl) handleChargingResponse(ctx *gin.Context, resp *http.Response, account model.User, outputPrice int64, reqBody []byte, reqModel model.Request) error {
	defer resp.Body.Close()

	var rawBody bytes.Buffer
	reader := bufio.NewReader(io.TeeReader(resp.Body, &rawBody))

	_, err := reader.WriteTo(ctx.Writer)
	if err != nil {
		c.handleBrokerError(ctx, err, "read from body")
		return err
	}

	if err := c.decodeAndProcess(ctx, rawBody.Bytes(), resp.Header.Get("Content-Encoding"), account, outputPrice, false, reqBody, reqModel, rawBody.Bytes()); err != nil {
		c.logger.Errorf("decode and process failed: %v", err)
		return err
	}

	return nil
}

func (c *Ctrl) handleChargingStreamResponse(ctx *gin.Context, resp *http.Response, account model.User, outputPrice int64, reqBody []byte, reqModel model.Request) error {
	defer resp.Body.Close()

	var rawBody bytes.Buffer

	var streamErr error = nil
	var responseChunk []byte = nil
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

			_, streamErr = w.Write([]byte(line))
			if streamErr != nil {
				c.handleBrokerError(ctx, err, "write to stream")
				return false
			}

			ctx.Writer.Flush()
		}
	})

	if streamErr != nil {
		return streamErr
	}

	// Fully read and then start decoding and processing
	if err := c.decodeAndProcess(ctx, rawBody.Bytes(), resp.Header.Get("Content-Encoding"), account, outputPrice, true, reqBody, reqModel, responseChunk); err != nil {
		c.handleBrokerError(ctx, err, "decode and process")
		return err
	}

	return nil
}
func (c *Ctrl) decodeAndProcess(ctx context.Context, data []byte, encodingType string, account model.User, outputPrice int64, isStream bool, reqBody []byte, reqModel model.Request, respChunk []byte) error {
	// Decode the raw data
	decodeReader := initializeReader(bytes.NewReader(data), encodingType)
	decodedBody, err := io.ReadAll(decodeReader)
	if err != nil {
		return errors.Wrap(err, "Error decoding body")
	}

	var output string
	var usage *Usage

	if !isStream {
		if err := c.processSingleResponse(ctx, decodedBody, outputPrice, &output, reqModel.RequestHash, &usage); err != nil {
			return err
		}
	} else {
		// Parse and decode data line by line for streams
		lines := bytes.Split(decodedBody, []byte("\n"))

		for _, line := range lines {
			if isStreamDone(line) {
				// For stream responses, usage info comes before [DONE]
				if usage != nil {
					return c.finalizeResponseWithUsage(ctx, usage, outputPrice, reqModel.RequestHash, c.Service.InputPrice)
				}
				return c.finalizeResponse(ctx, output, outputPrice, reqModel.RequestHash)
			}

			// Skip empty lines
			if isLineEmpty(line) {
				continue
			}

			// Check if this line contains usage information
			if extractedUsage := c.extractUsageFromLine(line); extractedUsage != nil {
				usage = extractedUsage
				continue
			}

			chunkOutput, err := c.processLine(line)
			if err != nil {
				return err
			}
			output += chunkOutput
		}
	}

	if !reqModel.VLLMProxy {
		if err := c.signChat(reqBody, data, respChunk); err != nil {
			return err
		}
	}

	return nil
}

func (c *Ctrl) signChat(reqBody, respData, respChunk []byte) error {
	hashAndEncode := func(b []byte) string {
		h := sha256.Sum256(b)
		return hex.EncodeToString(h[:])
	}

	requestSha256 := hashAndEncode(reqBody)
	responseSha256 := hashAndEncode(respData)

	var chatResp CompletionChunk
	err := json.Unmarshal(respChunk, &chatResp)
	if err != nil {
		return errors.Wrap(err, "Chat id could not be extracted from the response")
	}
	chatID := chatResp.ID

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

	key := c.chatCacheKey(chatID)
	c.logger.Debugf("key: %v, chat signature: %v", key, chatSignature)
	c.svcCache.Set(key, chatSignature, c.chatCacheExpiration)
	return nil
}

func (*Ctrl) chatCacheKey(chatID string) string {
	return fmt.Sprintf("%s:%s", ChatPrefix, chatID)
}

func (c *Ctrl) processSingleResponse(ctx context.Context, decodedBody []byte, outputPrice int64, output *string, requestHash string, usage **Usage) error {
	line := bytes.TrimPrefix(decodedBody, []byte("data: "))
	var chunk CompletionChunk
	if err := json.Unmarshal(line, &chunk); err != nil {
		return errors.Wrap(err, "Error unmarshaling JSON")
	}

	for _, choice := range chunk.Choices {
		*output += choice.Message.Content
	}
	
	// For non-stream responses, usage info is in the same response
	if chunk.Usage != nil {
		*usage = chunk.Usage
		return c.updateAccountWithUsage(ctx, chunk.Usage, outputPrice, requestHash, c.Service.InputPrice)
	}
	
	// Fallback to old logic if no usage info
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

func (c *Ctrl) finalizeResponse(ctx context.Context, output string, outputPrice int64, requestHash string) error {
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
func (c *Ctrl) finalizeResponseWithUsage(ctx context.Context, usage *Usage, outputPrice int64, requestHash string, inputPrice int64) error {
	return c.updateAccountWithUsage(ctx, usage, outputPrice, requestHash, inputPrice)
}

// updateAccountWithUsage updates the request with accurate token counts from the LLM response
func (c *Ctrl) updateAccountWithUsage(_ context.Context, usage *Usage, outputPrice int64, requestHash string, inputPrice int64) error {
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
func (c *Ctrl) updateAccountWithOutput(_ context.Context, output string, outputPrice int64, requestHash string) error {
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
