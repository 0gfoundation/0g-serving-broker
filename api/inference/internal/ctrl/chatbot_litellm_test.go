package ctrl

import "testing"

func TestLiteLLMUsageToUsage(t *testing.T) {
	cases := []struct {
		name             string
		in               LiteLLMUsage
		wantPrompt       int
		wantCompletion   int
		wantTotal        int
		wantCachedTokens int
		wantNilDetails   bool
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
			name:             "cache creation billed as full-price input (not cached)",
			in:               LiteLLMUsage{InputTokens: 20, OutputTokens: 10, CacheCreationInputTokens: 500, CacheReadInputTokens: 480},
			wantPrompt:       1000, // 20 + 500 + 480
			wantCompletion:   10,
			wantTotal:        1010,
			wantCachedTokens: 480, // only cache_read is discountable
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := c.in.toUsage()
			if got.PromptTokens != c.wantPrompt {
				t.Errorf("PromptTokens = %d, want %d", got.PromptTokens, c.wantPrompt)
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
