package chatbot

import (
	"encoding/json"
	"strings"

	"github.com/0glabs/0g-serving-broker/common/errors"
)

type ProviderChatBot struct{}

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

// func (c *ProviderChatBot) GetOutputCount(outputStr string) (int64, error) {
// 	return int64(len(strings.Fields(outputStr))), nil
// }

// func (c *ProviderChatBot) StreamCompleted(output []byte) (bool, error) {
// 	var content Content
// 	if err := json.Unmarshal(output, &content); err != nil {
// 		if strings.Contains(string(output), `"finish_reason":"stop"`) {
// 			return true, nil
// 		}
// 		return true, errors.Wrap(err, "unmarshal response")
// 	}
// 	if content.Choices[0].FinishReason != nil {
// 		return true, nil
// 	}
// 	return false, nil
// }

// func (c *ProviderChatBot) GetRespContent(resp []byte) ([]byte, error) {
// 	var reader io.ReadCloser
// 	switch encodingType {
// 	case "br":
// 		reader = io.NopCloser(brotli.NewReader(bytes.NewReader(resp)))
// 	case "gzip":
// 		gzipReader, err := gzip.NewReader(bytes.NewReader(resp))
// 		if err != nil {
// 			return nil, err
// 		}
// 		defer gzipReader.Close()
// 		reader = gzipReader
// 	case "deflate":
// 		deflateReader, err := zlib.NewReader(bytes.NewReader(resp))
// 		if err != nil {
// 			return nil, err
// 		}
// 		defer deflateReader.Close()
// 		reader = deflateReader
// 	default:
// 		reader = io.NopCloser(bytes.NewReader(resp))
// 	}

// 	decompressed, err := io.ReadAll(reader)
// 	if err != nil {
// 		return nil, err
// 	}
// 	return shakeStreamResponse(decompressed), nil
// }

func getReqContent(reqBody []byte) (RequestBody, error) {
	var ret RequestBody
	err := json.Unmarshal(reqBody, &ret)
	return ret, errors.Wrap(err, "unmarshal response")
}

// // shakeStreamResponse remove prefix and spaces from openAI response
// func shakeStreamResponse(input []byte) []byte {
// 	const prefix = "data: "
// 	if len(input) < len(prefix) {
// 		return input
// 	}
// 	if !bytes.HasPrefix(input, []byte(prefix)) {
// 		return input
// 	}
// 	return input[len(prefix):]
// }
