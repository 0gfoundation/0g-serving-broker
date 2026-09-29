package ctrl

import (
	"context"
	"testing"

	"github.com/0glabs/0g-serving-broker/inference/config"
	"github.com/0glabs/0g-serving-broker/inference/model"
)

func TestLiteLLMUsageToUsage(t *testing.T) {
	cases := []struct {
		name              string
		in                LiteLLMUsage
		wantPrompt        int
		wantCompletion    int
		wantTotal         int
		wantCachedTokens  int
		wantWriteTokens   int
		wantWrite1hTokens int
		wantNilDetails    bool
	}{
		{
			name:           "no cache",
			in:             LiteLLMUsage{InputTokens: 100, OutputTokens: 40},
			wantPrompt:     100,
			wantCompletion: 40,
			wantTotal:      140,
			wantNilDetails: true,
		},
		{
			name:             "cache read sums into prompt and is discountable",
			in:               LiteLLMUsage{InputTokens: 20, OutputTokens: 10, CacheReadInputTokens: 980},
			wantPrompt:       1000, // 20 fresh + 980 cached
			wantCompletion:   10,
			wantTotal:        1010,
			wantCachedTokens: 980,
		},
		{
			name:             "cache creation surfaced as write tokens; only read is discountable",
			in:               LiteLLMUsage{InputTokens: 20, OutputTokens: 10, CacheCreationInputTokens: 500, CacheReadInputTokens: 480},
			wantPrompt:       1000, // 20 + 500 + 480
			wantCompletion:   10,
			wantTotal:        1010,
			wantCachedTokens: 480, // only cache_read is discountable
			wantWriteTokens:  500, // cache_creation surfaced for the write premium
		},
		{
			name: "cache_creation TTL breakdown splits into 5m and 1h write buckets",
			in: LiteLLMUsage{
				InputTokens: 20, OutputTokens: 10, CacheReadInputTokens: 480,
				CacheCreationInputTokens: 500,
				CacheCreation:            &LiteLLMCacheCreation{Ephemeral5mInputTokens: 200, Ephemeral1hInputTokens: 300},
			},
			wantPrompt:        1000, // 20 + 500 + 480
			wantCompletion:    10,
			wantTotal:         1010,
			wantCachedTokens:  480,
			wantWriteTokens:   200, // ephemeral_5m
			wantWrite1hTokens: 300, // ephemeral_1h
		},
		{
			name: "1h-only breakdown: 5m bucket zero, aggregate not double-counted",
			in: LiteLLMUsage{
				InputTokens: 20, OutputTokens: 10,
				CacheCreationInputTokens: 300,
				CacheCreation:            &LiteLLMCacheCreation{Ephemeral1hInputTokens: 300},
			},
			wantPrompt:        320, // 20 + 300
			wantCompletion:    10,
			wantTotal:         330,
			wantWriteTokens:   0,
			wantWrite1hTokens: 300,
			wantNilDetails:    true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := c.in.toUsage()
			if got.PromptTokens != c.wantPrompt {
				t.Errorf("PromptTokens = %d, want %d", got.PromptTokens, c.wantPrompt)
			}
			if got.CacheWriteTokens != c.wantWriteTokens {
				t.Errorf("CacheWriteTokens = %d, want %d", got.CacheWriteTokens, c.wantWriteTokens)
			}
			if got.CacheWrite1hTokens != c.wantWrite1hTokens {
				t.Errorf("CacheWrite1hTokens = %d, want %d", got.CacheWrite1hTokens, c.wantWrite1hTokens)
			}
			if got.CompletionTokens != c.wantCompletion {
				t.Errorf("CompletionTokens = %d, want %d", got.CompletionTokens, c.wantCompletion)
			}
			if got.TotalTokens != c.wantTotal {
				t.Errorf("TotalTokens = %d, want %d", got.TotalTokens, c.wantTotal)
			}
			if c.wantNilDetails {
				if got.PromptTokensDetails != nil {
					t.Errorf("PromptTokensDetails = %+v, want nil", got.PromptTokensDetails)
				}
				return
			}
			if got.PromptTokensDetails == nil {
				t.Fatalf("PromptTokensDetails = nil, want CachedTokens=%d", c.wantCachedTokens)
			}
			if got.PromptTokensDetails.CachedTokens != c.wantCachedTokens {
				t.Errorf("CachedTokens = %d, want %d", got.PromptTokensDetails.CachedTokens, c.wantCachedTokens)
			}
		})
	}
}

func TestMergeLiteLLMUsage(t *testing.T) {
	// Anthropic streaming: message_start carries input/cache, message_delta carries
	// cumulative output. A later zero must not clear an earlier non-zero count.
	var acc LiteLLMUsage
	mergeLiteLLMUsage(&acc, &LiteLLMUsage{InputTokens: 50, CacheReadInputTokens: 200, CacheCreationInputTokens: 30})
	mergeLiteLLMUsage(&acc, &LiteLLMUsage{OutputTokens: 17}) // message_delta: only output

	if acc.InputTokens != 50 || acc.CacheReadInputTokens != 200 || acc.CacheCreationInputTokens != 30 {
		t.Errorf("input/cache cleared by later delta: %+v", acc)
	}
	if acc.OutputTokens != 17 {
		t.Errorf("OutputTokens = %d, want 17", acc.OutputTokens)
	}

	usage := acc.toUsage()
	if usage.PromptTokens != 280 { // 50 + 30 + 200
		t.Errorf("PromptTokens = %d, want 280", usage.PromptTokens)
	}
	if usage.PromptTokensDetails == nil || usage.PromptTokensDetails.CachedTokens != 200 {
		t.Errorf("CachedTokens = %+v, want 200", usage.PromptTokensDetails)
	}
}

func TestMergeLiteLLMUsage_CacheCreationBreakdown(t *testing.T) {
	// The per-TTL cache_creation breakdown arrives in message_start; a later
	// message_delta carrying only output must not clear it, so the 5m/1h split
	// survives to billing.
	var acc LiteLLMUsage
	mergeLiteLLMUsage(&acc, &LiteLLMUsage{
		InputTokens:              20,
		CacheCreationInputTokens: 500,
		CacheCreation:            &LiteLLMCacheCreation{Ephemeral5mInputTokens: 200, Ephemeral1hInputTokens: 300},
	})
	mergeLiteLLMUsage(&acc, &LiteLLMUsage{OutputTokens: 17}) // message_delta: only output

	if acc.CacheCreation == nil {
		t.Fatal("CacheCreation cleared by later delta")
	}
	usage := acc.toUsage()
	if usage.CacheWriteTokens != 200 || usage.CacheWrite1hTokens != 300 {
		t.Errorf("write split = %d/%d, want 200/300", usage.CacheWriteTokens, usage.CacheWrite1hTokens)
	}
}

// An Anthropic /v1/messages stream whose FIRST line is an SSE comment — what
// RedPill emits (": PROCESSING") while a long prompt prefills — must still be
// detected as Anthropic. Detection used to look at the first non-empty line
// only, so the comment defaulted it to OpenAI and "event: message_start" hit
// json.Unmarshal ("invalid character 'e' looking for beginning of value"),
// dropping the whitelisted request's usage from reconciliation.
func TestDecodeAndProcess_AnthropicStreamLeadingComment(t *testing.T) {
	c := newChatbotTestCtrl(t, config.Service{})
	db := &mockReconciliationDB{}
	c.reconciliationDB = db

	body := ": PROCESSING\n\n" +
		"event: message_start\n" +
		`data: {"type":"message_start","message":{"id":"req_1","type":"message","role":"assistant","content":[],"model":"z-ai/glm-5.3","usage":{"input_tokens":0,"output_tokens":0}}}` + "\n\n" +
		"event: content_block_delta\n" +
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hi"}}` + "\n\n" +
		"event: message_delta\n" +
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"input_tokens":14,"output_tokens":45}}` + "\n\n" +
		"event: message_stop\n" +
		`data: {"type": "message_stop"}` + "\n\n"

	reqModel := model.Request{IsWhitelisted: true, ServiceName: "chatbot", RequestHash: "h"}
	if err := c.decodeAndProcess(context.Background(), []byte(body), "", model.User{}, "0", true, reqModel); err != nil {
		t.Fatalf("decodeAndProcess: %v", err)
	}
	if len(db.calls) != 1 {
		t.Fatalf("want 1 reconciliation row, got %d", len(db.calls))
	}
	if got := db.calls[0]; got.InputCount != 14 || got.OutputCount != 45 {
		t.Fatalf("usage = in %d / out %d, want 14 / 45", got.InputCount, got.OutputCount)
	}
}
