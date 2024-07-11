package chatbot

import (
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"encoding/json"
	"io"

	"github.com/andybalholm/brotli"

	"github.com/0glabs/0g-serving-agent/common/errors"
)

// https://platform.openai.com/docs/api-reference/making-requests

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

func GetContent(resp []byte, contentEncoding string) (Content, error) {
	var reader io.ReadCloser
	var ret Content
	switch contentEncoding {
	case "br":
		reader = io.NopCloser(brotli.NewReader(bytes.NewReader(resp)))
	case "gzip":
		gzipReader, err := gzip.NewReader(bytes.NewReader(resp))
		if err != nil {
			return ret, err
		}
		defer gzipReader.Close()
		reader = gzipReader
	case "deflate":
		deflateReader, err := zlib.NewReader(bytes.NewReader(resp))
		if err != nil {
			return ret, err
		}
		defer deflateReader.Close()
		reader = deflateReader
	default:
		reader = io.NopCloser(bytes.NewReader(resp))
	}

	decompressed, err := io.ReadAll(reader)
	if err != nil {
		return ret, err
	}
	shaken := shakeStreamResponse(decompressed)
	if err := json.Unmarshal(shaken, &ret); err != nil {
		return ret, errors.Wrap(err, "unmarshal response")
	}
	var errRes ErrorResponse
	if len(ret.Choices) < 1 {
		if err := json.Unmarshal(shaken, &errRes); err != nil {
			return ret, errors.Wrap(err, "unmarshal response")
		}
		return ret, errors.New(errRes.Error.Message)
	}
	return ret, nil
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
