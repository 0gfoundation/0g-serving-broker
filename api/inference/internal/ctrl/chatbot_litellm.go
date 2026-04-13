package ctrl

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"

	"github.com/0glabs/0g-serving-broker/common/errors"
	"github.com/0glabs/0g-serving-broker/inference/monitor"
)

// ResponseFormat represents the format of the LLM response
type ResponseFormat int

const (
	FormatOpenAI ResponseFormat = iota
	FormatLiteLLM
)

// LiteLLM (Claude API) response structures
type LiteLLMResponse struct {
	ID           string           `json:"id"`
	Type         string           `json:"type"`
	Role         string           `json:"role"`
	Model        string           `json:"model"`
	Content      []LiteLLMContent `json:"content"`
	StopReason   string           `json:"stop_reason"`
	StopSequence *string          `json:"stop_sequence"`
	Usage        *LiteLLMUsage    `json:"usage,omitempty"`
}

type LiteLLMContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type LiteLLMUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
	TotalTokens  int `json:"total_tokens,omitempty"`
}

// LiteLLM stream event structures
type LiteLLMStreamEvent struct {
	Type         string                `json:"type"`
	Message      *LiteLLMStreamMessage `json:"message,omitempty"`
	Index        int                   `json:"index,omitempty"`
	ContentBlock *LiteLLMContentBlock  `json:"content_block,omitempty"`
	Delta        *LiteLLMDelta         `json:"delta,omitempty"`
	Usage        *LiteLLMUsage         `json:"usage,omitempty"`
}

type LiteLLMStreamMessage struct {
	ID           string           `json:"id"`
	Type         string           `json:"type"`
	Role         string           `json:"role"`
	Content      []LiteLLMContent `json:"content"`
	Model        string           `json:"model"`
	StopReason   *string          `json:"stop_reason"`
	StopSequence *string          `json:"stop_sequence"`
	Usage        *LiteLLMUsage    `json:"usage"`
}

type LiteLLMContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type LiteLLMDelta struct {
	Type       string  `json:"type"`
	Text       string  `json:"text"`
	StopReason *string `json:"stop_reason"`
}

// processLiteLLMSingleResponse processes a non-stream LiteLLM response
func (c *Ctrl) processLiteLLMSingleResponse(ctx context.Context, decodedBody []byte, outputPrice string, output *string, requestHash string, usage **Usage, isWhitelisted bool) error {
	var response LiteLLMResponse
	if err := json.Unmarshal(decodedBody, &response); err != nil {
		return errors.Wrap(err, "Error unmarshaling LiteLLM JSON")
	}

	// Extract content from content array
	for _, content := range response.Content {
		if content.Type == "text" {
			*output += content.Text
		}
	}

	// Convert LiteLLM usage to standard Usage format
	if response.Usage != nil {
		*usage = &Usage{
			PromptTokens:     response.Usage.InputTokens,
			CompletionTokens: response.Usage.OutputTokens,
			TotalTokens:      response.Usage.InputTokens + response.Usage.OutputTokens,
		}

		// Skip billing for whitelisted users, but still record token metrics
		if isWhitelisted {
			monitor.RecordTokens("chatbot", int64(response.Usage.InputTokens), int64(response.Usage.OutputTokens))
			monitor.RecordTPSFromContext(ctx, "chatbot", int64(response.Usage.OutputTokens))
			return nil
		}

		prices, err := c.GetBillingPrices(ctx)
		if err != nil {
			return errors.Wrap(err, "get billing prices for LiteLLM single response billing")
		}
		return c.updateAccountWithUsage(ctx, *usage, prices.OutputPrice, requestHash, prices.InputPrice)
	}

	// Skip billing for whitelisted users
	if isWhitelisted {
		return nil
	}

	// Fallback to old logic if no usage info
	return c.updateAccountWithOutput(ctx, *output, outputPrice, requestHash)
}

// processLiteLLMStream processes a streaming LiteLLM response
func (c *Ctrl) processLiteLLMStream(ctx context.Context, lines [][]byte, outputPrice string, output *string, usage **Usage, requestHash string, isWhitelisted bool) error {
	for i := 0; i < len(lines); i++ {
		line := lines[i]

		// Skip empty lines
		if isLineEmpty(line) {
			continue
		}

		// LiteLLM uses SSE format: "event: xxx" followed by "data: {...}"
		if bytes.HasPrefix(line, []byte("event:")) {
			eventType := strings.TrimSpace(string(bytes.TrimPrefix(line, []byte("event:"))))

			// Next line should be the data
			if i+1 < len(lines) {
				i++
				dataLine := lines[i]
				if !bytes.HasPrefix(dataLine, []byte("data:")) {
					continue
				}

				dataJSON := bytes.TrimSpace(bytes.TrimPrefix(dataLine, []byte("data:")))

				switch eventType {
				case "content_block_delta":
					var event LiteLLMStreamEvent
					if err := json.Unmarshal(dataJSON, &event); err != nil {
						c.logger.Warnf("Failed to parse content_block_delta: %v", err)
						continue
					}
					if event.Delta != nil && event.Delta.Text != "" {
						*output += event.Delta.Text
					}

				case "message_delta":
					var event LiteLLMStreamEvent
					if err := json.Unmarshal(dataJSON, &event); err != nil {
						c.logger.Warnf("Failed to parse message_delta: %v", err)
						continue
					}
					if event.Usage != nil {
						*usage = &Usage{
							PromptTokens:     event.Usage.InputTokens,
							CompletionTokens: event.Usage.OutputTokens,
							TotalTokens:      event.Usage.InputTokens + event.Usage.OutputTokens,
						}
					}

				case "message_stop":
					// Skip billing for whitelisted users, but still record token metrics
					if isWhitelisted {
						if *usage != nil {
							monitor.RecordTokens("chatbot", int64((*usage).PromptTokens), int64((*usage).CompletionTokens))
							monitor.RecordTPSFromContext(ctx, "chatbot", int64((*usage).CompletionTokens))
						}
						return nil
					}

					// Stream finished
					if *usage != nil {
						prices, err := c.GetBillingPrices(ctx)
						if err != nil {
							return errors.Wrap(err, "get billing prices for LiteLLM stream response billing")
						}
						return c.finalizeResponseWithUsage(ctx, *usage, prices.OutputPrice, requestHash, prices.InputPrice)
					}
					return c.finalizeResponse(ctx, *output, outputPrice, requestHash)
				}
			}
		}
	}

	return nil
}
