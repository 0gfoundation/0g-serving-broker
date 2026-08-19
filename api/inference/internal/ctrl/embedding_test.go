package ctrl

import (
	"encoding/json"
	"testing"
)

func TestEmbeddingResponse_DecodeUsage(t *testing.T) {
	body := []byte(`{"object":"list","data":[{"object":"embedding","index":0,"embedding":[0.1,0.2]}],"model":"qwen3.7-text-embedding","usage":{"prompt_tokens":8,"total_tokens":8}}`)

	var parsed EmbeddingResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if parsed.Usage == nil {
		t.Fatal("usage must be populated")
	}
	if parsed.Usage.PromptTokens != 8 || parsed.Usage.TotalTokens != 8 {
		t.Errorf("got PromptTokens=%d TotalTokens=%d, want 8/8", parsed.Usage.PromptTokens, parsed.Usage.TotalTokens)
	}
}

func TestEmbeddingResponse_MissingUsage(t *testing.T) {
	// A non-conforming provider that omits `usage` entirely — must decode
	// without error, leaving Usage nil so the caller's fallback estimator runs.
	body := []byte(`{"object":"list","data":[{"object":"embedding","index":0,"embedding":[0.1]}],"model":"m"}`)

	var parsed EmbeddingResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if parsed.Usage != nil {
		t.Errorf("want nil Usage when the field is absent, got %+v", parsed.Usage)
	}
}

func TestEstimateEmbeddingUsageFromRequest(t *testing.T) {
	tests := []struct {
		name        string
		reqBody     []byte
		wantMinimum int // word-count-derived, so pin a floor rather than an exact value
	}{
		{"string input", []byte(`{"model":"m","input":"one two three four five"}`), 5},
		{"array input", []byte(`{"model":"m","input":["one two","three four"]}`), 5},
		{"empty input string", []byte(`{"model":"m","input":""}`), 1},
		{"invalid JSON", []byte("not json"), 1},
		{"nil body", nil, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			usage := estimateEmbeddingUsageFromRequest(tt.reqBody)
			if usage == nil {
				t.Fatal("usage must not be nil")
			}
			if usage.PromptTokens < tt.wantMinimum {
				t.Errorf("PromptTokens = %d, want >= %d", usage.PromptTokens, tt.wantMinimum)
			}
			if usage.PromptTokens != usage.TotalTokens {
				t.Errorf("PromptTokens (%d) must equal TotalTokens (%d) — embedding has no completion side", usage.PromptTokens, usage.TotalTokens)
			}
		})
	}
}
