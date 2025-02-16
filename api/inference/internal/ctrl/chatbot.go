package ctrl

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"compress/flate"
	"compress/gzip"

	"github.com/andybalholm/brotli"
	"github.com/gin-gonic/gin"

	"github.com/0glabs/0g-serving-broker/common/errors"
	"github.com/0glabs/0g-serving-broker/common/util"
	"github.com/0glabs/0g-serving-broker/inference/model"
)

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

func (c *Ctrl) handleChatbotResponse(ctx *gin.Context, resp *http.Response, account model.User, outputPrice int64, reqBody []byte) {
	isStream, err := isStream(reqBody)
	if err != nil {
		handleBrokerError(ctx, err, "check if stream")
		return
	}
	if !isStream {
		c.handleChargingResponse(ctx, resp, account, outputPrice)
	} else {
		c.handleChargingStreamResponse(ctx, resp, account, outputPrice)
	}
}

func (c *Ctrl) handleChargingResponse(ctx *gin.Context, resp *http.Response, account model.User, outputPrice int64) {
	defer resp.Body.Close()

	var rawBody bytes.Buffer
	reader := bufio.NewReader(io.TeeReader(resp.Body, &rawBody))

	_, err := reader.WriteTo(ctx.Writer)
	if err != nil {
		handleBrokerError(ctx, err, "read from body")
		return
	}

	c.decodeAndProcess(rawBody.Bytes(), resp.Header.Get("Content-Encoding"), account, outputPrice, false)
}

func (c *Ctrl) handleChargingStreamResponse(ctx *gin.Context, resp *http.Response, account model.User, outputPrice int64) {
	defer resp.Body.Close()

	var rawBody bytes.Buffer

	ctx.Stream(func(w io.Writer) bool {
		reader := bufio.NewReader(io.TeeReader(resp.Body, &rawBody))

		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				if err == io.EOF {
					return false
				}
				handleBrokerError(ctx, err, "read from body")
				return false
			}

			_, err = w.Write([]byte(line))
			if err != nil {
				handleBrokerError(ctx, err, "write to stream")
				return false
			}

			ctx.Writer.Flush()
		}
	})

	// Fully read and then start decoding and processing
	err := c.decodeAndProcess(rawBody.Bytes(), resp.Header.Get("Content-Encoding"), account, outputPrice, true)
	if err != nil {
		handleBrokerError(ctx, err, "decode and process")
	}
}
func (c *Ctrl) decodeAndProcess(data []byte, encodingType string, account model.User, outputPrice int64, isStream bool) error {
	// Decode the raw data
	decodeReader := initializeReader(bytes.NewReader(data), encodingType)
	decodedBody, err := io.ReadAll(decodeReader)
	if err != nil {
		return errors.Wrap(err, "Error decoding body")
	}

	var output string

	if !isStream {
		return c.processSingleResponse(decodedBody, outputPrice, account, &output)
	}

	// Parse and decode data line by line for streams
	lines := bytes.Split(decodedBody, []byte("\n"))

	for _, line := range lines {
		if isStreamDone(line) {
			return c.finalizeResponse(output, outputPrice, account)
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
	return nil
}

func (c *Ctrl) processSingleResponse(decodedBody []byte, outputPrice int64, account model.User, output *string) error {
	line := bytes.TrimPrefix(decodedBody, []byte("data: "))
	var chunk CompletionChunk
	if err := json.Unmarshal(line, &chunk); err != nil {
		return errors.Wrap(err, "Error unmarshaling JSON")
	}

	for _, choice := range chunk.Choices {
		*output += choice.Message.Content
	}
	return c.updateAccountWithOutput(*output, outputPrice, account)
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

func (c *Ctrl) finalizeResponse(output string, outputPrice int64, account model.User) error {
	return c.updateAccountWithOutput(output, outputPrice, account)
}

func (c *Ctrl) updateAccountWithOutput(output string, outputPrice int64, account model.User) error {
	outputCount := int64(len(strings.Fields(output)))
	lastResponseFee, err := util.Multiply(outputPrice, outputCount)
	if err != nil {
		return errors.Wrap(err, "Error calculating last response fee")
	}

	account.LastResponseFee = model.PtrOf(lastResponseFee.String())
	if err := c.UpdateUserAccount(account.User, account); err != nil {
		return errors.Wrap(err, "Error updating user account")
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
