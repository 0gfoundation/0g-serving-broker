package ctrl

import (
	"encoding/json"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestStripResponseLeakFields(t *testing.T) {
	c := &Ctrl{logger: testLogger()}
	t.Run("strips provider", func(t *testing.T) {
		in := []byte(`{"id":"x","provider":"DeepInfra","choices":[]}`)
		out, changed := c.stripResponseLeakFields(in)
		if !changed {
			t.Fatal("expected changed=true")
		}
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(out, &obj); err != nil {
			t.Fatalf("output not valid JSON: %v", err)
		}
		if _, ok := obj["provider"]; ok {
			t.Error("provider field not stripped")
		}
		if _, ok := obj["id"]; !ok {
			t.Error("unrelated field id was dropped")
		}
	})

	t.Run("strips usage.cost", func(t *testing.T) {
		in := []byte(`{"usage":{"prompt_tokens":10,"cost":0.0042,"cost_details":{"upstream":0.004}}}`)
		out, changed := c.stripResponseLeakFields(in)
		if !changed {
			t.Fatal("expected changed=true")
		}
		var obj struct {
			Usage map[string]json.RawMessage `json:"usage"`
		}
		if err := json.Unmarshal(out, &obj); err != nil {
			t.Fatalf("output not valid JSON: %v", err)
		}
		if _, ok := obj.Usage["cost"]; ok {
			t.Error("usage.cost not stripped")
		}
		if _, ok := obj.Usage["cost_details"]; ok {
			t.Error("usage.cost_details not stripped")
		}
		if _, ok := obj.Usage["prompt_tokens"]; !ok {
			t.Error("usage.prompt_tokens was dropped")
		}
	})

	t.Run("no-op on clean vLLM response", func(t *testing.T) {
		in := []byte(`{"id":"x","model":"qwen","usage":{"prompt_tokens":10,"completion_tokens":5}}`)
		out, changed := c.stripResponseLeakFields(in)
		if changed {
			t.Errorf("expected changed=false, got modified output %s", out)
		}
		if string(out) != string(in) {
			t.Errorf("body should be returned unchanged, got %s", out)
		}
	})

	t.Run("no-op on non-JSON", func(t *testing.T) {
		in := []byte(`[DONE]`)
		out, changed := c.stripResponseLeakFields(in)
		if changed || string(out) != string(in) {
			t.Errorf("non-JSON should pass through unchanged")
		}
	})
}

func TestSanitizeStreamLine(t *testing.T) {
	c := &Ctrl{}
	ctx := &gin.Context{} // no loraOriginalModel → model rewrite is a no-op

	t.Run("drops OpenRouter keepalive comment", func(t *testing.T) {
		_, forward := c.sanitizeStreamLine(ctx, ": OPENROUTER PROCESSING\n")
		if forward {
			t.Error("expected SSE comment line to be dropped (forward=false)")
		}
	})

	t.Run("preserves blank separator", func(t *testing.T) {
		line, forward := c.sanitizeStreamLine(ctx, "\n")
		if !forward || line != "\n" {
			t.Errorf("blank separator should pass through, got %q forward=%v", line, forward)
		}
	})

	t.Run("passes [DONE] through", func(t *testing.T) {
		line, forward := c.sanitizeStreamLine(ctx, "data: [DONE]\n")
		if !forward || line != "data: [DONE]\n" {
			t.Errorf("got %q forward=%v", line, forward)
		}
	})

	t.Run("strips provider from data chunk", func(t *testing.T) {
		line, forward := c.sanitizeStreamLine(ctx, `data: {"id":"x","provider":"DeepInfra","choices":[]}`+"\n")
		if !forward {
			t.Fatal("data chunk should be forwarded")
		}
		var obj map[string]json.RawMessage
		payload := line[len("data: ") : len(line)-1] // strip prefix and trailing newline
		if err := json.Unmarshal([]byte(payload), &obj); err != nil {
			t.Fatalf("sanitized chunk not valid JSON: %v (line=%q)", err, line)
		}
		if _, ok := obj["provider"]; ok {
			t.Error("provider not stripped from streamed chunk")
		}
	})
}
