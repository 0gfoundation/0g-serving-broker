package ctrl

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/0glabs/0g-serving-broker/inference/config"
	"github.com/gin-gonic/gin"
)

func rolesOf(t *testing.T, body []byte) []string {
	t.Helper()
	var b struct {
		Messages []struct {
			Role string `json:"role"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &b); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	var out []string
	for _, m := range b.Messages {
		out = append(out, m.Role)
	}
	return out
}

func TestDeveloperAsSystem(t *testing.T) {
	// Every developer message becomes system, wherever it sits; nothing else
	// changes, including numbers float64 would round and fields the broker
	// does not know.
	in := []byte(`{"model":"glm-5.3","seed":12345678901234567890,"messages":[` +
		`{"role":"developer","content":"Reply BANANA."},` +
		`{"role":"user","content":[{"type":"text","text":"the word developer"}]},` +
		`{"role":"assistant","content":null,"reasoning_content":"r","tool_calls":[{"id":"c1","type":"function","function":{"name":"f","arguments":"{}"}}]},` +
		`{"role":"tool","tool_call_id":"c1","content":"ok"},` +
		`{"role":"developer","content":"Later rule."}]}`)
	out := developerAsSystem(in)
	if got := rolesOf(t, out); len(got) != 5 || got[0] != "system" || got[4] != "system" || got[1] != "user" || got[2] != "assistant" || got[3] != "tool" {
		t.Fatalf("roles = %v", got)
	}
	var a, b map[string]any
	dec := func(raw []byte, v *map[string]any) {
		d := json.NewDecoder(bytes.NewReader(raw))
		d.UseNumber()
		if err := d.Decode(v); err != nil {
			t.Fatal(err)
		}
	}
	dec(in, &a)
	dec(out, &b)
	for _, m := range a["messages"].([]any) {
		if mm := m.(map[string]any); mm["role"] == "developer" {
			mm["role"] = "system"
		}
	}
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	if !bytes.Equal(ja, jb) {
		t.Fatalf("body changed beyond the role:\n in  %s\n out %s", ja, jb)
	}

	// Bodies with nothing to rename are forwarded byte for byte.
	for _, same := range []string{
		`{"model":"m","messages":[{"role":"system","content":"developer"}]}`,
		`{"model":"m","input":[{"role":"developer","content":"x"}]}`,
		`{"model":"m","messages":"developer"}`,
		`not json "developer"`,
	} {
		if got := developerAsSystem([]byte(same)); string(got) != same {
			t.Fatalf("%s changed to %s", same, got)
		}
	}
}

// The rename is only worth anything if the forward path runs it.
func TestPrepareHTTPRequest_SendsDeveloperAsSystem(t *testing.T) {
	c := newChatbotTestCtrl(t, config.Service{Type: "chatbot", ModelType: "glm-5.3", ProviderType: "centralized", ProviderIdentity: "tencent"})
	c.Service.Type = "chatbot"
	body := []byte(`{"model":"glm-5.3","messages":[{"role":"developer","content":"Reply BANANA."},{"role":"user","content":"2+2?"}]}`)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))

	req, err := c.PrepareHTTPRequest(ctx, "http://upstream.invalid/v1/chat/completions", body, "chatbot")
	if err != nil {
		t.Fatalf("PrepareHTTPRequest: %v", err)
	}
	forwarded, _ := io.ReadAll(req.Body)
	if got := rolesOf(t, forwarded); got[0] != "system" {
		t.Fatalf("forwarded roles = %v, want the developer message sent as system", got)
	}
}
