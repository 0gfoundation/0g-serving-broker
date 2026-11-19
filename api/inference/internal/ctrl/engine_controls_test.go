package ctrl

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/0glabs/0g-serving-broker/inference/config"
	"github.com/gin-gonic/gin"
)

func TestGuardEngineControls_StripsControlFields(t *testing.T) {
	in := `{"model":"m","messages":[{"role":"user","content":"hi"}],` +
		`"chat_template":"{{x}}","custom_logit_processor":"gASV","lora_path":"/etc","lora_adapter_name":"a","return_hidden_states":true,` +
		`"chat_template_kwargs":{"chat_template":"{{ raise_exception('HELLO_XYZ') }}","tools":[],"enable_thinking":false,"reasoning_effort":"high"}}`
	out, err := guardEngineControls([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	for _, f := range engineControlFields {
		if _, ok := got[f]; ok {
			t.Errorf("%s not stripped", f)
		}
	}
	kw := got["chat_template_kwargs"].(map[string]any)
	if _, ok := kw["chat_template"]; ok {
		t.Error("chat_template_kwargs.chat_template not stripped")
	}
	if _, ok := kw["tools"]; ok {
		t.Error("chat_template_kwargs.tools not stripped")
	}
	if kw["enable_thinking"] != false || kw["reasoning_effort"] != "high" {
		t.Errorf("template variables lost: %v", kw)
	}
	if got["model"] != "m" || got["messages"] == nil {
		t.Errorf("other fields lost: %v", got)
	}
}

func TestGuardEngineControls_CleanBodyUnchanged(t *testing.T) {
	in := `{"model":"m","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"https://example.com/a.png"}}]}],"chat_template_kwargs":{"enable_thinking":true}}`
	out, err := guardEngineControls([]byte(in))
	if err != nil || string(out) != in {
		t.Fatalf("clean body changed: err=%v out=%s", err, out)
	}
	if out, err := guardEngineControls([]byte("not json")); err != nil || string(out) != "not json" {
		t.Fatalf("non-JSON body: err=%v out=%s", err, out)
	}
}

func TestGuardEngineControls_MediaLinks(t *testing.T) {
	refused := []string{
		// every shape seen on 2026-11-19
		"file:///etc/passwd", "file:///proc/self/environ", "/etc/passwd",
		"http://127.0.0.1:8000/get_server_info", "http://localhost:8000/health",
		"http://169.254.169.254/latest/meta-data/", "http://metadata.google.internal/",
		"http://100.100.100.200/", "http://kms:9090/", "http://host.docker.internal/",
		"http://172.17.0.1/", "http://10.128.0.5:9092/",
		// equivalent spellings
		"http://127.1/", "http://0x7f.0.0.1/", "http://2130706433/", "http://[::1]/",
		"http://[::ffff:127.0.0.1]/", "http://0.0.0.0:8000/", "http://user:pw@example.com/a.png",
		"http://svc.default.svc/", "gopher://example.com/", "ftp://example.com/a.png", "relative.png",
	}
	for _, link := range refused {
		for _, key := range []string{"image_url", "video_url"} {
			body := `{"messages":[{"role":"user","content":[{"type":"` + key + `","` + key + `":{"url":"` + link + `"}}]}]}`
			if _, err := guardEngineControls([]byte(body)); err == nil {
				t.Errorf("%s %q accepted", key, link)
			}
		}
	}
	// Responses API shape: string image_url inside input.
	if _, err := guardEngineControls([]byte(`{"input":[{"role":"user","content":[{"type":"input_image","image_url":"file:///etc/passwd"}]}]}`)); err == nil {
		t.Error("responses input_image file:// accepted")
	}

	allowed := []string{"https://example.com/a.png", "http://cdn.example.org:8080/v.mp4", "data:image/png;base64,iVBORw0KGgo=", "https://8.8.8.8/x.png"}
	for _, link := range allowed {
		body := `{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"` + link + `"}}]}]}`
		if _, err := guardEngineControls([]byte(body)); err != nil {
			t.Errorf("%q refused: %v", link, err)
		}
	}
	// A link-shaped string inside tool-call arguments is data, not a fetch.
	body := `{"messages":[{"role":"assistant","tool_calls":[{"function":{"name":"f","arguments":"{\"image_url\":\"file:///etc/passwd\"}"}}]}]}`
	if _, err := guardEngineControls([]byte(body)); err != nil {
		t.Errorf("tool arguments refused: %v", err)
	}
	_, err := guardEngineControls([]byte(`{"messages":[{"content":[{"image_url":{"url":"file:///etc/passwd"}}]}]}`))
	if err == nil || !strings.Contains(err.Error(), "image_url") {
		t.Errorf("error should name the field: %v", err)
	}
}

// The guard is only worth anything if the forward path runs it.
func TestPrepareHTTPRequest_GuardsEngineControls(t *testing.T) {
	c := newChatbotTestCtrl(t, config.Service{Type: "chatbot", ModelType: "m", ProviderType: "centralized"})
	c.Service.Type = "chatbot"
	prepare := func(body string) ([]byte, error) {
		ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
		ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
		req, err := c.PrepareHTTPRequest(ctx, "http://upstream.invalid/v1/chat/completions", []byte(body), "chatbot")
		if err != nil {
			return nil, err
		}
		return io.ReadAll(req.Body)
	}

	fwd, err := prepare(`{"model":"m","messages":[{"role":"user","content":"hi"}],"chat_template_kwargs":{"chat_template":"{{ raise_exception(messages) }}"},"custom_logit_processor":"x"}`)
	if err != nil {
		t.Fatalf("PrepareHTTPRequest: %v", err)
	}
	if bytes.Contains(fwd, []byte("raise_exception")) || bytes.Contains(fwd, []byte("custom_logit_processor")) {
		t.Fatalf("engine-control fields forwarded: %s", fwd)
	}
	if _, err := prepare(`{"model":"m","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"file:///etc/passwd"}}]}]}`); err == nil {
		t.Fatal("file:// image_url forwarded")
	}
}
