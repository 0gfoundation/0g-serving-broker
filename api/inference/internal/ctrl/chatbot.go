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
	"log"
	"math/big"
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
	"github.com/0glabs/0g-serving-broker/inference/zkclient/models"
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

func (c *Ctrl) GetChatbotInputFee(reqBody []byte) (string, error) {
	inputCount, err := getInputCount(reqBody)
	if err != nil {
		return "", errors.Wrap(err, "get input count")
	}

	expectedInputFee, err := util.Multiply(inputCount, c.Service.InputPrice)
	if err != nil {
		return "", errors.Wrap(err, "calculate input fee")
	}
	return expectedInputFee.String(), nil
}

func getReqContent(reqBody []byte) (RequestBody, error) {
	var ret RequestBody
	err := json.Unmarshal(reqBody, &ret)
	return ret, errors.Wrap(err, "unmarshal response")
}

func getInputCount(reqBody []byte) (int64, error) {
	reqContent, err := getReqContent(reqBody)
	if err != nil {
		return 0, err
	}
	var ret int64
	for _, m := range reqContent.Messages {
		ret += int64(len(strings.Fields(m.Content)))
	}
	return ret, nil
}

func (c *Ctrl) handleChatbotResponse(ctx *gin.Context, resp *http.Response, account model.User, outputPrice int64, reqBody []byte, reqModel model.Request) error {
	isStream, err := isStream(reqBody)
	if err != nil {
		handleBrokerError(ctx, err, "check if stream")
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
		handleBrokerError(ctx, err, "read from body")
		return err
	}

	if err := c.decodeAndProcess(ctx, rawBody.Bytes(), resp.Header.Get("Content-Encoding"), account, outputPrice, false, reqBody, reqModel, rawBody.Bytes()); err != nil {
		log.Printf("decode and process failed: %v", err)
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
				handleBrokerError(ctx, err, "read from body")
				streamErr = err
				return false
			}

			if responseChunk == nil {
				responseChunk = []byte(strings.TrimSpace(strings.TrimPrefix(line, "data: ")))
			}

			_, streamErr = w.Write([]byte(line))
			if streamErr != nil {
				handleBrokerError(ctx, err, "write to stream")
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
		handleBrokerError(ctx, err, "decode and process")
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

	if !isStream {
		if err := c.processSingleResponse(ctx, decodedBody, outputPrice, account, &output, reqModel.RequestHash); err != nil {
			return err
		}
	} else {
		// Parse and decode data line by line for streams
		lines := bytes.Split(decodedBody, []byte("\n"))

		for _, line := range lines {
			if isStreamDone(line) {
				return c.finalizeResponse(ctx, output, outputPrice, account, reqModel.RequestHash)
			}

			// Skip empty lines
			if isLineEmpty(line) {
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
	log.Printf("key: %v, chat signature: %v", key, chatSignature)
	c.svcCache.Set(key, chatSignature, c.chatCacheExpiration)
	return nil
}

func (*Ctrl) chatCacheKey(chatID string) string {
	return fmt.Sprintf("%s:%s", ChatPrefix, chatID)
}

func (c *Ctrl) processSingleResponse(ctx context.Context, decodedBody []byte, outputPrice int64, account model.User, output *string, requestHash string) error {
	line := bytes.TrimPrefix(decodedBody, []byte("data: "))
	var chunk CompletionChunk
	if err := json.Unmarshal(line, &chunk); err != nil {
		return errors.Wrap(err, "Error unmarshaling JSON")
	}

	for _, choice := range chunk.Choices {
		*output += choice.Message.Content
	}
	return c.updateAccountWithOutput(ctx, *output, outputPrice, account, requestHash)
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

func (c *Ctrl) finalizeResponse(ctx context.Context, output string, outputPrice int64, account model.User, requestHash string) error {
	return c.updateAccountWithOutput(ctx, output, outputPrice, account, requestHash)
}

func (c *Ctrl) updateAccountWithOutput(ctx context.Context, output string, outputPrice int64, account model.User, requestHash string) error {
	outputCount := int64(len(strings.Fields(output)))
	lastResponseFee, err := util.Multiply(outputPrice, outputCount)
	if err != nil {
		return errors.Wrap(err, "Error calculating last response fee")
	}

	requestFee, err := util.Add(lastResponseFee, account.UnsettledFee)
	if err != nil {
		return err
	}

	signature, err := c.generateSignature(ctx, lastResponseFee, account, requestHash)
	if err != nil {
		return err
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	dbAccount, err := c.db.GetUserAccount(account.User)
	if err != nil {
		return err
	}

	unsettledFee, err := util.Add(requestFee, dbAccount.UnsettledFee)
	if err != nil {
		return err
	}

	if err := c.db.UpdateOutputFeeWithSignature(requestHash, account.User, *account.LastRequestNonce, lastResponseFee.String(), requestFee.String(), unsettledFee.String(), signature); err != nil {
		return errors.Wrap(err, "Error updating request")
	}

	return nil
}

func (c *Ctrl) generateSignature(ctx context.Context, lastResponseFee *big.Int, account model.User, requestHash string) (string, error) {
	hash, err := hexutil.Decode(requestHash)
	if err != nil {
		return "", err
	}

	int64Hash := make([]int64, len(hash))
	for i, v := range hash {
		int64Hash[i] = int64(v)
	}

	reqInZK := &models.RequestResponse{
		ResFee:      lastResponseFee.String(),
		RequestHash: int64Hash,
	}

	log.Printf("request in ZK: %v", reqInZK)

	signatures, err := c.GenerateSignatures(ctx, reqInZK)
	if err != nil {
		return "", err
	}

	if len(signatures) != 1 {
		return "", fmt.Errorf("expected exactly one signature, while got %v", len(signatures))
	}

	sig, err := json.Marshal(signatures[0])
	if err != nil {
		return "", err
	}

	signature := string(sig)
	log.Printf("signature  %v", signature)

	return signature, nil
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
