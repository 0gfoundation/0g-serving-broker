package ctrl

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"testing"

	"github.com/gin-gonic/gin"
)

func gzipBytes(t *testing.T, b []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	if _, err := w.Write(b); err != nil {
		t.Fatalf("gzip write: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	return buf.Bytes()
}

func TestDecodeBody(t *testing.T) {
	plain := []byte(`{"id":"x","choices":[]}`)

	t.Run("identity passthrough", func(t *testing.T) {
		out, err := decodeBody(plain, "")
		if err != nil || !bytes.Equal(out, plain) {
			t.Errorf("identity: out=%s err=%v", out, err)
		}
	})

	t.Run("gzip decodes", func(t *testing.T) {
		out, err := decodeBody(gzipBytes(t, plain), "gzip")
		if err != nil || !bytes.Equal(out, plain) {
			t.Errorf("gzip: out=%s err=%v", out, err)
		}
	})

	t.Run("case-insensitive encoding", func(t *testing.T) {
		out, err := decodeBody(gzipBytes(t, plain), "GZIP")
		if err != nil || !bytes.Equal(out, plain) {
			t.Errorf("GZIP: out=%s err=%v", out, err)
		}
	})

	t.Run("corrupt gzip errors (no silent raw passthrough)", func(t *testing.T) {
		if _, err := decodeBody([]byte("not-gzip-at-all"), "gzip"); err == nil {
			t.Error("expected error for corrupt gzip body")
		}
	})

	t.Run("unsupported encoding errors", func(t *testing.T) {
		if _, err := decodeBody(plain, "zstd"); err == nil {
			t.Error("expected error for unsupported encoding zstd")
		}
	})
}

// decode is a small helper to parse sanitized output back into a generic map.
func decode(t *testing.T, b []byte) map[string]interface{} {
	t.Helper()
	var m map[string]interface{}
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("output not valid JSON: %v (%s)", err, b)
	}
	return m
}

func TestSanitizeResponseBody(t *testing.T) {
	c := &Ctrl{logger: testLogger()}

	t.Run("strips identity + cost + fingerprint fields (incl. nested)", func(t *testing.T) {
		in := []byte(`{
			"id":"gen-123",
			"provider":"DeepInfra",
			"is_byok":false,
			"model":"glm-5",
			"choices":[{"index":0,"finish_reason":"stop","native_finish_reason":"stop",
			            "message":{"role":"assistant","content":"hi","reasoning_details":[{"x":1}]}}],
			"usage":{"prompt_tokens":10,"completion_tokens":5,"cost":0.0001008,
			         "cost_details":{"upstream_inference_cost":0.0001}}
		}`)
		out, changed := c.sanitizeResponseBody(in, "")
		if !changed {
			t.Fatal("expected changed=true")
		}
		obj := decode(t, out)
		if _, ok := obj["provider"]; ok {
			t.Error("provider not stripped")
		}
		if _, ok := obj["is_byok"]; ok {
			t.Error("is_byok not stripped")
		}
		if obj["model"] != "glm-5" {
			t.Error("model must be preserved")
		}
		choice := obj["choices"].([]interface{})[0].(map[string]interface{})
		if _, ok := choice["native_finish_reason"]; ok {
			t.Error("choices[].native_finish_reason not stripped")
		}
		if choice["finish_reason"] != "stop" {
			t.Error("standard finish_reason must be preserved")
		}
		msg := choice["message"].(map[string]interface{})
		if _, ok := msg["reasoning_details"]; ok {
			t.Error("message.reasoning_details not stripped")
		}
		if msg["content"] != "hi" {
			t.Error("message.content must be preserved")
		}
		usage := obj["usage"].(map[string]interface{})
		if _, ok := usage["cost"]; ok {
			t.Error("usage.cost not stripped")
		}
		if _, ok := usage["cost_details"]; ok {
			t.Error("usage.cost_details not stripped")
		}
		if usage["prompt_tokens"].(float64) != 10 {
			t.Error("usage.prompt_tokens must be preserved")
		}
	})

	t.Run("zero-valued normalization: drop zero token-detail sub-fields, keep real ones", func(t *testing.T) {
		in := []byte(`{"usage":{
			"prompt_tokens":10,
			"prompt_tokens_details":{"cached_tokens":4,"audio_tokens":0,"video_tokens":0},
			"cache_write_tokens":0,
			"completion_tokens_details":{"reasoning_tokens":7,"image_tokens":0,"audio_tokens":3}
		}}`)
		out, changed := c.sanitizeResponseBody(in, "")
		if !changed {
			t.Fatal("expected changed=true")
		}
		usage := decode(t, out)["usage"].(map[string]interface{})
		if _, ok := usage["cache_write_tokens"]; ok {
			t.Error("zero cache_write_tokens should be dropped")
		}
		ptd := usage["prompt_tokens_details"].(map[string]interface{})
		if _, ok := ptd["audio_tokens"]; ok {
			t.Error("zero prompt audio_tokens should be dropped")
		}
		if _, ok := ptd["video_tokens"]; ok {
			t.Error("zero prompt video_tokens should be dropped")
		}
		if ptd["cached_tokens"].(float64) != 4 {
			t.Error("cached_tokens must be preserved (real data)")
		}
		ctd := usage["completion_tokens_details"].(map[string]interface{})
		if _, ok := ctd["image_tokens"]; ok {
			t.Error("zero completion image_tokens should be dropped")
		}
		if ctd["audio_tokens"].(float64) != 3 {
			t.Error("non-zero audio_tokens must be preserved (real usage)")
		}
		if ctd["reasoning_tokens"].(float64) != 7 {
			t.Error("reasoning_tokens must be preserved")
		}
	})

	t.Run("rewrites top-level id when newID given", func(t *testing.T) {
		in := []byte(`{"id":"gen-abc","object":"chat.completion","choices":[]}`)
		out, changed := c.sanitizeResponseBody(in, "chatcmpl-xyz")
		if !changed {
			t.Fatal("expected changed=true (id rewrite)")
		}
		if decode(t, out)["id"] != "chatcmpl-xyz" {
			t.Errorf("id not rewritten, got %v", decode(t, out)["id"])
		}
	})

	t.Run("preserves content with HTML chars (no escaping)", func(t *testing.T) {
		in := []byte(`{"id":"gen-1","provider":"x","choices":[{"message":{"content":"a < b && c > d"}}]}`)
		out, _ := c.sanitizeResponseBody(in, "")
		msg := decode(t, out)["choices"].([]interface{})[0].(map[string]interface{})["message"].(map[string]interface{})
		if msg["content"] != "a < b && c > d" {
			t.Errorf("content altered: %v", msg["content"])
		}
	})

	t.Run("no-op on clean vLLM response", func(t *testing.T) {
		in := []byte(`{"id":"x","model":"qwen","usage":{"prompt_tokens":10,"completion_tokens":5}}`)
		out, changed := c.sanitizeResponseBody(in, "")
		if changed {
			t.Errorf("expected changed=false, got %s", out)
		}
		if string(out) != string(in) {
			t.Errorf("clean body should be returned unchanged, got %s", out)
		}
	})

	t.Run("no-op on non-JSON", func(t *testing.T) {
		in := []byte(`[DONE]`)
		out, changed := c.sanitizeResponseBody(in, "")
		if changed || string(out) != string(in) {
			t.Errorf("non-JSON should pass through unchanged")
		}
	})

	t.Run("preserves large integers (no float64 precision loss)", func(t *testing.T) {
		// 9007199254740993 = 2^53 + 1, not representable as float64.
		in := []byte(`{"id":"gen-1","provider":"x","created":9007199254740993,"usage":{"total_tokens":12345678901234567}}`)
		out, changed := c.sanitizeResponseBody(in, "")
		if !changed {
			t.Fatal("expected provider strip → changed")
		}
		// Compare the raw number bytes survive (json.Number round-trip).
		if !bytes.Contains(out, []byte(`9007199254740993`)) {
			t.Errorf("created lost precision: %s", out)
		}
		if !bytes.Contains(out, []byte(`12345678901234567`)) {
			t.Errorf("total_tokens lost precision: %s", out)
		}
	})

	t.Run("does not descend into structured message.content", func(t *testing.T) {
		// A structured content part literally named "cost"/"provider" is user data
		// and must survive; only response-metadata leak fields are stripped.
		in := []byte(`{"id":"gen-1","provider":"DeepInfra",` +
			`"choices":[{"message":{"role":"assistant","content":[{"type":"text","provider":"keep-me","cost":42}]}}]}`)
		out, changed := c.sanitizeResponseBody(in, "")
		if !changed {
			t.Fatal("top-level provider should still be stripped")
		}
		obj := decode(t, out)
		if _, ok := obj["provider"]; ok {
			t.Error("top-level provider must be stripped")
		}
		part := obj["choices"].([]interface{})[0].(map[string]interface{})["message"].(map[string]interface{})["content"].([]interface{})[0].(map[string]interface{})
		if part["provider"] != "keep-me" {
			t.Errorf("structured content provider field was wrongly stripped: %v", part)
		}
		if part["cost"].(float64) != 42 {
			t.Errorf("structured content cost field was wrongly stripped: %v", part)
		}
	})
}

func TestSanitizeStreamLine(t *testing.T) {
	c := &Ctrl{logger: testLogger()}
	ctx := &gin.Context{} // no loraOriginalModel → model rewrite is a no-op
	const id = "chatcmpl-stream"

	t.Run("drops OpenRouter keepalive comment", func(t *testing.T) {
		_, forward := c.sanitizeStreamLine(ctx, ": OPENROUTER PROCESSING\n", id)
		if forward {
			t.Error("expected SSE comment line to be dropped (forward=false)")
		}
	})

	t.Run("preserves blank separator", func(t *testing.T) {
		line, forward := c.sanitizeStreamLine(ctx, "\n", id)
		if !forward || line != "\n" {
			t.Errorf("blank separator should pass through, got %q forward=%v", line, forward)
		}
	})

	t.Run("passes [DONE] through", func(t *testing.T) {
		line, forward := c.sanitizeStreamLine(ctx, "data: [DONE]\n", id)
		if !forward || line != "data: [DONE]\n" {
			t.Errorf("got %q forward=%v", line, forward)
		}
	})

	t.Run("strips fields and rewrites id in a data chunk", func(t *testing.T) {
		line, forward := c.sanitizeStreamLine(ctx,
			`data: {"id":"gen-1","provider":"DeepInfra","choices":[{"delta":{"content":"hi"},"native_finish_reason":"stop"}]}`+"\n", id)
		if !forward {
			t.Fatal("data chunk should be forwarded")
		}
		payload := line[len("data: ") : len(line)-1] // strip prefix + trailing newline
		obj := decode(t, []byte(payload))
		if _, ok := obj["provider"]; ok {
			t.Error("provider not stripped from streamed chunk")
		}
		if obj["id"] != id {
			t.Errorf("chunk id not rewritten, got %v", obj["id"])
		}
		choice := obj["choices"].([]interface{})[0].(map[string]interface{})
		if _, ok := choice["native_finish_reason"]; ok {
			t.Error("native_finish_reason not stripped from chunk")
		}
	})
}

func TestIsUpstreamLeakHeader(t *testing.T) {
	cases := []struct {
		key  string
		want bool
	}{
		{"X-Openrouter-Id", true},
		{"Openrouter-Processing-Ms", true},
		{"X-Or-Cost", true},
		{"Provider", true},
		{"Server", true},
		{"Via", true},
		{"X-Powered-By", true},
		{"X-Ratelimit-Remaining", true},
		{"X-Clerk-Auth", true},
		{"Content-Type", false},
		{"Content-Encoding", false},
		{"ZG-Res-Key", false},
		{"X-Request-Id", false},
	}
	for _, c := range cases {
		if got := isUpstreamLeakHeader(c.key); got != c.want {
			t.Errorf("isUpstreamLeakHeader(%q) = %v, want %v", c.key, got, c.want)
		}
	}
}
