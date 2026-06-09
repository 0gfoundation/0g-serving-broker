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
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	TotalTokens              int `json:"total_tokens,omitempty"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens,omitempty"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens,omitempty"`
}

// toUsage converts an Anthropic/LiteLLM usage report into the broker's canonical
// Usage struct.
//
// Anthropic reports input_tokens, cache_creation_input_tokens and
// cache_read_input_tokens as three disjoint buckets: input_tokens EXCLUDES
// cached tokens (unlike OpenAI's prompt_tokens, which is the total with cached
// as a subset). The canonical prompt_tokens is therefore their sum. Only
// cache_read tokens are eligible for the cached-token discount; freshly written
// cache_creation tokens are billed at full input price (the broker's billing
// model has no separate cache-write premium).
//
// Populating PromptTokensDetails is what lets cacheTokenBilling engage on the
// Anthropic /v1/messages path (see updateAccountWithUsage / finalizeResponseWithUsage,
// which gate the discount on usage.PromptTokensDetails != nil).
func (u *LiteLLMUsage) toUsage() *Usage {
	promptTokens := u.InputTokens + u.CacheCreationInputTokens + u.CacheReadInputTokens
	usage := &Usage{
		PromptTokens:     promptTokens,
		CompletionTokens: u.OutputTokens,
		TotalTokens:      promptTokens + u.OutputTokens,
	}
	if u.CacheReadInputTokens > 0 {
		usage.PromptTokensDetails = &PromptTokensDetails{CachedTokens: u.CacheReadInputTokens}
	}
	return usage
}

// mergeLiteLLMUsage folds a usage fragment from one Anthropic stream event into
// an accumulator. Input/cache counts are reported in message_start and the
// cumulative output count in message_delta, so each field is taken from
// whichever event reports a non-zero value (a later zero never clears an
// earlier count).
func mergeLiteLLMUsage(acc, in *LiteLLMUsage) {
	if in.InputTokens > 0 {
		acc.InputTokens = in.InputTokens
	}
	if in.OutputTokens > 0 {
		acc.OutputTokens = in.OutputTokens
	}
	if in.CacheReadInputTokens > 0 {
		acc.CacheReadInputTokens = in.CacheReadInputTokens
	}
	if in.CacheCreationInputTokens > 0 {
		acc.CacheCreationInputTokens = in.CacheCreationInputTokens
	}
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

	// Convert LiteLLM usage to standard Usage format (sums input + cache buckets
	// and surfaces cached tokens so cacheTokenBilling can discount them).
	if response.Usage != nil {
		*usage = response.Usage.toUsage()

		// Skip billing for whitelisted users, but still record token metrics
		if isWhitelisted {
			monitor.RecordTokens("chatbot", int64((*usage).PromptTokens), int64((*usage).CompletionTokens))
			monitor.RecordWhitelistTokens("chatbot", int64((*usage).PromptTokens), int64((*usage).CompletionTokens))
			monitor.RecordTPSFromContext(ctx, "chatbot", int64((*usage).CompletionTokens))
			return nil
		}

		prices, err := c.GetBillingPrices(ctx)
		if err != nil {
			return errors.Wrap(err, "get billing prices for LiteLLM single response billing")
		}
		return c.updateAccountWithUsage(ctx, *usage, prices.OutputPrice, requestHash, prices.InputPrice, prices.Tiers, prices.CacheTokenBilling)
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
	// Anthropic streams split usage across events: input/cache counts arrive in
	// message_start, cumulative output counts in message_delta. Accumulate both
	// and build the canonical Usage at message_stop.
	var acc LiteLLMUsage
	haveUsage := false
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

				case "message_start":
					var event LiteLLMStreamEvent
					if err := json.Unmarshal(dataJSON, &event); err != nil {
						c.logger.Warnf("Failed to parse message_start: %v", err)
						continue
					}
					if event.Message != nil && event.Message.Usage != nil {
						mergeLiteLLMUsage(&acc, event.Message.Usage)
						haveUsage = true
					}

				case "message_delta":
					var event LiteLLMStreamEvent
					if err := json.Unmarshal(dataJSON, &event); err != nil {
						c.logger.Warnf("Failed to parse message_delta: %v", err)
						continue
					}
					if event.Usage != nil {
						mergeLiteLLMUsage(&acc, event.Usage)
						haveUsage = true
					}

				case "message_stop":
					if haveUsage {
						*usage = acc.toUsage()
					}
					// Stream finished — bill via the shared finalizer (same path as
					// the OpenAI [DONE] handler in chatbot.go).
					return c.finalizeChatStream(ctx, *output, *usage, outputPrice, requestHash, isWhitelisted)
				}
			}
		}
	}

	// No message_stop event — the stream was truncated/dropped or an
	// OpenAI-compatible shim omitted the terminal Anthropic event. The client
	// already received the accumulated output, so bill it rather than serving
	// free, logging loudly. haveUsage may be true from message_start/message_delta.
	if haveUsage {
		*usage = acc.toUsage()
	}
	c.logger.Errorf("LiteLLM stream ended without a message_stop event for request %s; finalizing on accumulated usage/output (haveUsage=%t)", requestHash, haveUsage)
	return c.finalizeChatStream(ctx, *output, *usage, outputPrice, requestHash, isWhitelisted)
}
