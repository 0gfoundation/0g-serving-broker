package ctrl

import (
	"encoding/json"
	"testing"

	"github.com/0glabs/0g-serving-broker/inference/config"
)

// TestUsageDecodeOpenAICacheWriteTokens locks in the OpenAI-path field mapping:
// billing reads cache-write tokens from a top-level usage field named
// cache_write_tokens (the field OpenRouter emits, which sanitize.go also strips
// when zero). If the upstream field name/nesting ever drifts, the write premium
// would silently never apply on the OpenAI path — this decode test fails first.
func TestUsageDecodeOpenAICacheWriteTokens(t *testing.T) {
	// Representative OpenAI/OpenRouter chat-completion usage object: prompt_tokens
	// is the inclusive total, cached_tokens (read) is nested in
	// prompt_tokens_details, cache_write_tokens (creation) is top-level.
	const body = `{
		"id": "gen-abc",
		"usage": {
			"prompt_tokens": 1000,
			"completion_tokens": 50,
			"total_tokens": 1050,
			"prompt_tokens_details": {"cached_tokens": 200},
			"cache_write_tokens": 300
		}
	}`
	var chunk CompletionChunk
	if err := json.Unmarshal([]byte(body), &chunk); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if chunk.Usage == nil {
		t.Fatal("Usage = nil")
	}
	if chunk.Usage.CacheWriteTokens != 300 {
		t.Errorf("CacheWriteTokens = %d, want 300 (field name/nesting drifted?)", chunk.Usage.CacheWriteTokens)
	}
	if chunk.Usage.PromptTokensDetails == nil || chunk.Usage.PromptTokensDetails.CachedTokens != 200 {
		t.Errorf("cached_tokens = %+v, want 200", chunk.Usage.PromptTokensDetails)
	}
}

// TestComputeInputFee exercises the three-bucket input-fee split: cache-read
// tokens at the discount (inputPrice/Divisor), cache-write tokens at the premium
// (inputPrice*num/den), and everything else at full price. inputPrice is 100
// wei/token throughout so the arithmetic is easy to read.
func TestComputeInputFee(t *testing.T) {
	const inputPrice = "100"

	discount := config.CacheTokenBillingConfig{Enabled: true, Divisor: 10}
	premium := config.CacheTokenBillingConfig{Enabled: true, Divisor: 10, WriteMultiplierNumerator: 5, WriteMultiplierDenominator: 4}

	cases := []struct {
		name       string
		usage      *Usage
		cfg        config.CacheTokenBillingConfig
		wantTotal  string
		wantCached int
		wantWrite  int
		wantFull   int
	}{
		{
			name:      "caching disabled: all full price",
			usage:     &Usage{PromptTokens: 1000},
			cfg:       config.CacheTokenBillingConfig{Enabled: false},
			wantTotal: "100000", // 100 * 1000
			wantFull:  1000,
		},
		{
			name:       "cache read only: discount applied",
			usage:      &Usage{PromptTokens: 1000, PromptTokensDetails: &PromptTokensDetails{CachedTokens: 200}},
			cfg:        discount,
			wantTotal:  "82000", // 800*100 + 200*100/10
			wantCached: 200,
			wantFull:   800,
		},
		{
			name:      "write tokens present but no premium configured: billed full price",
			usage:     &Usage{PromptTokens: 1000, CacheWriteTokens: 400},
			cfg:       discount, // WriteMultiplier unset
			wantTotal: "100000", // all 1000 at full price
			wantFull:  1000,
		},
		{
			name:      "cache write only: premium applied",
			usage:     &Usage{PromptTokens: 1000, CacheWriteTokens: 400},
			cfg:       premium,
			wantTotal: "110000", // 600*100 + (400*100)*5/4
			wantWrite: 400,
			wantFull:  600,
		},
		{
			name:       "cache read and write together",
			usage:      &Usage{PromptTokens: 1000, PromptTokensDetails: &PromptTokensDetails{CachedTokens: 200}, CacheWriteTokens: 300},
			cfg:        premium,
			wantTotal:  "89500", // 500*100 + 200*100/10 + (300*100)*5/4
			wantCached: 200,
			wantWrite:  300,
			wantFull:   500,
		},
		{
			name:      "premium configured but caching disabled: all full price",
			usage:     &Usage{PromptTokens: 1000, CacheWriteTokens: 400},
			cfg:       config.CacheTokenBillingConfig{Enabled: false, Divisor: 10, WriteMultiplierNumerator: 5, WriteMultiplierDenominator: 4},
			wantTotal: "100000",
			wantFull:  1000,
		},
		{
			name:       "read+write clamped to prompt total (full never negative)",
			usage:      &Usage{PromptTokens: 1000, PromptTokensDetails: &PromptTokensDetails{CachedTokens: 900}, CacheWriteTokens: 500},
			cfg:        premium,
			wantTotal:  "21500", // full=0; 900*100/10=9000; write=100 → (100*100)*5/4=12500
			wantCached: 900,
			wantWrite:  100, // clamped from 500 to 1000-900
			wantFull:   0,
		},
		{
			name:       "cached alone exceeds prompt: clamped, no write",
			usage:      &Usage{PromptTokens: 1000, PromptTokensDetails: &PromptTokensDetails{CachedTokens: 1500}},
			cfg:        discount,
			wantTotal:  "10000", // 1000*100/10
			wantCached: 1000,
			wantFull:   0,
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			got, err := computeInputFee(inputPrice, tt.usage, tt.cfg)
			if err != nil {
				t.Fatalf("computeInputFee error: %v", err)
			}
			if got.Total.String() != tt.wantTotal {
				t.Errorf("Total = %s, want %s", got.Total.String(), tt.wantTotal)
			}
			if got.CachedTokens != tt.wantCached {
				t.Errorf("CachedTokens = %d, want %d", got.CachedTokens, tt.wantCached)
			}
			if got.WriteTokens != tt.wantWrite {
				t.Errorf("WriteTokens = %d, want %d", got.WriteTokens, tt.wantWrite)
			}
			if got.FullTokens != tt.wantFull {
				t.Errorf("FullTokens = %d, want %d", got.FullTokens, tt.wantFull)
			}
		})
	}
}
