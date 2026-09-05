package proxy

import (
	"encoding/json"
	"strings"
	"testing"
)

// The attack this exists to stop: legal JSON whitespace inflates the envelope
// without reaching the model. Charged on raw bytes, four megabytes of
// indentation reads as a million tokens, exceeds any budget, and — since an
// over-budget request is admitted alone — takes the whole chatbot surface with
// it while costing the sender nothing.
func TestPromptBytes_IgnoresEnvelopePadding(t *testing.T) {
	compact := []byte(`{"model":"m","messages":[{"role":"user","content":"hello"}]}`)

	padded, err := json.Marshal(map[string]interface{}{
		"model":    "m",
		"messages": []map[string]string{{"role": "user", "content": "hello"}},
		// A field the upstream ignores, carrying megabytes.
		"x": strings.Repeat(" ", 4<<20),
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	base, inflated := promptBytes(compact), promptBytes(padded)
	if inflated != base {
		t.Fatalf("padding changed the estimate: %d vs %d", inflated, base)
	}
	if inflated > 1000 {
		t.Fatalf("estimate %d is tracking the envelope, not the prompt", inflated)
	}
}

// Whitespace between tokens is free to the model and must be free here.
func TestPromptBytes_IgnoresIndentation(t *testing.T) {
	compact := []byte(`{"messages":[{"role":"user","content":"hi"}]}`)
	var v interface{}
	if err := json.Unmarshal(compact, &v); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	indented, err := json.MarshalIndent(v, "", strings.Repeat(" ", 200))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	if got, want := promptBytes(indented), promptBytes(compact); got != want {
		t.Fatalf("indented estimate %d, want %d", got, want)
	}
}

// Real prompt content must still be counted, or the gate measures nothing.
func TestPromptBytes_CountsPromptContent(t *testing.T) {
	small := promptBytes([]byte(`{"messages":[{"role":"user","content":"hi"}]}`))
	big := promptBytes([]byte(`{"messages":[{"role":"user","content":"` + strings.Repeat("x", 100000) + `"}]}`))

	if big-small < 99000 {
		t.Fatalf("a 100k-character prompt only added %d bytes", big-small)
	}
}

// system, tools and content-part arrays are prompt too.
func TestPromptBytes_CountsSystemToolsAndParts(t *testing.T) {
	body := []byte(`{"system":"` + strings.Repeat("s", 5000) + `",` +
		`"tools":[{"type":"function","function":{"name":"` + strings.Repeat("t", 5000) + `"}}],` +
		`"messages":[{"role":"user","content":[{"type":"text","text":"` + strings.Repeat("c", 5000) + `"}]}]}`)

	if got := promptBytes(body); got < 15000 {
		t.Fatalf("estimate %d, want at least the 15000 bytes of system+tools+content", got)
	}
}

// Many empty messages still cost chat-template scaffolding.
func TestPromptBytes_ChargesPerMessageOverhead(t *testing.T) {
	one := promptBytes([]byte(`{"messages":[{"role":"user","content":""}]}`))
	many := promptBytes([]byte(`{"messages":[` + strings.TrimSuffix(strings.Repeat(`{"role":"user","content":""},`, 100), ",") + `]}`))

	if many <= one {
		t.Fatalf("100 empty messages (%d) must weigh more than one (%d)", many, one)
	}
}

// A body the engine cannot parse is rejected before it holds any KV, so
// charging it for context would be charging for work that never happens.
func TestPromptBytes_UnparseableBodyIsFree(t *testing.T) {
	for _, body := range []string{``, `not json`, `[1,2,3]`, `null`} {
		if got := promptBytes([]byte(body)); got != 0 {
			t.Fatalf("body %q charged %d, want 0", body, got)
		}
	}
}

// Anthropic's prompt body lives in content blocks this code does not enumerate,
// and an agentic conversation is mostly tool_result. Counting those zero would
// have exempted the exact traffic this gate exists for — 19-glm5.3 advertises
// the anthropic surface — and made "hide the prompt in a tool_result" a
// one-line bypass.
func TestPromptBytes_CountsNonTextContentBlocks(t *testing.T) {
	payload := strings.Repeat("r", 200000)
	for _, block := range []string{
		`{"type":"tool_result","content":"` + payload + `"}`,
		`{"type":"thinking","thinking":"` + payload + `"}`,
		`{"type":"tool_use","input":{"q":"` + payload + `"}}`,
		`{"type":"document","source":{"data":"` + payload + `"}}`,
	} {
		body := []byte(`{"messages":[{"role":"user","content":[` + block + `]}]}`)
		if got := promptBytes(body); got < 200000 {
			t.Fatalf("block %.30s...: charged %d, want at least the 200000 bytes it carries", block, got)
		}
	}
}

// Whitespace inside tool definitions is as free to the engine as whitespace
// between top-level fields — it re-serializes them before templating — so
// measuring them raw would reopen the padding hole one level down.
func TestPromptBytes_IgnoresWhitespaceInsideTools(t *testing.T) {
	compact := []byte(`{"tools":[{"type":"function","function":{"name":"f"}}],"messages":[{"role":"user","content":"hi"}]}`)
	padded := []byte(`{"tools":[{"type":"function",` + strings.Repeat(" ", 4<<20) + `"function":{"name":"f"}}],"messages":[{"role":"user","content":"hi"}]}`)

	if got, want := promptBytes(padded), promptBytes(compact); got != want {
		t.Fatalf("padded tools charged %d, want %d — whitespace must not count", got, want)
	}
}

func TestPromptBytes_IgnoresWhitespaceInsideToolCalls(t *testing.T) {
	compact := []byte(`{"messages":[{"role":"assistant","tool_calls":[{"id":"a","type":"function"}]}]}`)
	padded := []byte(`{"messages":[{"role":"assistant","tool_calls":[{"id":"a",` + strings.Repeat(" ", 1<<20) + `"type":"function"}]}]}`)

	if got, want := promptBytes(padded), promptBytes(compact); got != want {
		t.Fatalf("padded tool_calls charged %d, want %d", got, want)
	}
}

// "We could not read it, so it is free" is an invitation to send exactly that.
func TestPromptBytes_UnreadableMessagesAreChargedNotExempted(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":"` + strings.Repeat("x", 200000) + `"},"junk"]}`)

	if got := promptBytes(body); got < 200000 {
		t.Fatalf("a messages array this parser cannot read charged %d, want its full length", got)
	}
}

// The union of the two: padding is free, real content is not, whatever shape
// it arrives in.
func TestPromptBytes_PaddingStaysFreeAcrossShapes(t *testing.T) {
	base := promptBytes([]byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`))
	padded := promptBytes([]byte(`{"messages":[{"role":"user","content":[{"type":"text",` + strings.Repeat(" ", 1<<20) + `"text":"hi"}]}]}`))

	if base != padded {
		t.Fatalf("padded text block charged %d, want %d", padded, base)
	}
}
