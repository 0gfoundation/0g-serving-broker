package chatbot

import (
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"encoding/json"
	"io"
	"strings"

	"github.com/andybalholm/brotli"

	"github.com/0glabs/0g-serving-broker/inference-router/contract"
	"github.com/0glabs/0g-serving-broker/common/errors"
)

type ChatBot struct {
	SvcInfo contract.Service
}

// https://platform.openai.com/docs/api-reference/making-requests

type RequestBody struct {
	Messages []Message `json:"messages"`
}

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type Choice struct {
	Message      Message `json:"message"`
	Delta        Message `json:"delta"`
	FinishReason *string `json:"finish_reason"`
}

type Content struct {
	Choices []Choice `json:"choices"`
}

type ErrorResponse struct {
	Error struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Param   string `json:"param"`
		Code    string `json:"code"`
	} `json:"error"`
}

func (c *ChatBot) GetSvcInfo() contract.Service {
	return c.SvcInfo
}

func (c *ChatBot) GetInputCount(reqBody []byte) (int64, error) {
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

func (c *ChatBot) GetOutputCount(outputs [][]byte) (int64, error) {
	var outputStr string
	for _, output := range outputs {
		var content Content
		if err := json.Unmarshal(output, &content); err != nil {
			return 0, errors.Wrap(err, "unmarshal response")
		}
		var errRes ErrorResponse
		if len(content.Choices) < 1 {
			if err := json.Unmarshal(output, &errRes); err != nil {
				return 0, errors.Wrap(err, "unmarshal response")
			}
			return 0, errors.New(errRes.Error.Message)
		}
		if content.Choices[0].Message.Content != "" {
			outputStr += content.Choices[0].Message.Content
		} else {
			outputStr += content.Choices[0].Delta.Content
		}
	}

	return int64(len(strings.Fields(outputStr))), nil
}

func (c *ChatBot) StreamCompleted(output []byte) (bool, error) {
	var content Content
	if err := json.Unmarshal(output, &content); err != nil {
		return true, errors.Wrap(err, "unmarshal response")
	}
	return content.Choices[0].FinishReason != nil, nil
}

func (c *ChatBot) GetRespContent(resp []byte, encodingType string) ([]byte, error) {
	var reader io.ReadCloser
	switch encodingType {
	case "br":
		reader = io.NopCloser(brotli.NewReader(bytes.NewReader(resp)))
	case "gzip":
		gzipReader, err := gzip.NewReader(bytes.NewReader(resp))
		if err != nil {
			return nil, err
		}
		defer gzipReader.Close()
		reader = gzipReader
	case "deflate":
		deflateReader, err := zlib.NewReader(bytes.NewReader(resp))
		if err != nil {
			return nil, err
		}
		defer deflateReader.Close()
		reader = deflateReader
	default:
		reader = io.NopCloser(bytes.NewReader(resp))
	}

	decompressed, err := io.ReadAll(reader)
	if err != nil {
		return nil, err
	}
	return shakeStreamResponse(decompressed), nil
}

// shakeStreamResponse remove prefix and spaces from openAI response
func shakeStreamResponse(input []byte) []byte {
	const prefix = "data: "
	if len(input) < len(prefix) {
		return input
	}
	if !bytes.HasPrefix(input, []byte(prefix)) {
		return input
	}
	return input[len(prefix):]
}

func getReqContent(reqBody []byte) (RequestBody, error) {
	var ret RequestBody
	err := json.Unmarshal(reqBody, &ret)
	return ret, errors.Wrap(err, "unmarshal response")
}
