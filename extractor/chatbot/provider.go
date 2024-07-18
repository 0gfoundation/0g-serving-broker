package chatbot

import (
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"encoding/json"
	"io"
	"strings"

	"github.com/andybalholm/brotli"

	"github.com/0glabs/0g-serving-agent/common/errors"
)

type ProviderChatBot struct{}

func (c *ProviderChatBot) GetInputCount(reqBody []byte) (int64, error) {
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

func (c *ProviderChatBot) GetOutputCount(outputs [][]byte) (int64, error) {
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

func (c *ProviderChatBot) StreamCompleted(output []byte) (bool, error) {
	var content Content
	if err := json.Unmarshal(output, &content); err != nil {
		return true, errors.Wrap(err, "unmarshal response")
	}
	return content.Choices[0].FinishReason != nil, nil
}

func (c *ProviderChatBot) GetRespContent(resp []byte, encodingType string) ([]byte, error) {
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

func (c *ProviderChatBot) ErrMsg(body io.Reader) error {
	msg := struct {
		Error string `json:"error"`
	}{}

	if err := json.NewDecoder(body).Decode(&msg); err != nil {
		return errors.Wrap(err, "decode error message")
	}

	return errors.New(msg.Error)
}
