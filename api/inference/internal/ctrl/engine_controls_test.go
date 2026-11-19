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
	out, err := guardEngineControls([]byte(in), true)
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
	out, err := guardEngineControls([]byte(in), false)
	if err != nil || string(out) != in {
		t.Fatalf("clean body changed: err=%v out=%s", err, out)
	}
	inline := `{"model":"m","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,iVBORw0KGgo="}}]}],"chat_template_kwargs":{"enable_thinking":true}}`
	if out, err := guardEngineControls([]byte(inline), true); err != nil || string(out) != inline {
		t.Fatalf("self-hosted clean body changed: err=%v out=%s", err, out)
	}
	if out, err := guardEngineControls([]byte("not json"), true); err != nil || string(out) != "not json" {
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
			if _, err := guardEngineControls([]byte(body), false); err == nil {
				t.Errorf("%s %q accepted", key, link)
			}
		}
	}
	// Responses API shape: string image_url inside input.
	if _, err := guardEngineControls([]byte(`{"input":[{"role":"user","content":[{"type":"input_image","image_url":"file:///etc/passwd"}]}]}`), false); err == nil {
		t.Error("responses input_image file:// accepted")
	}

	allowed := []string{"https://example.com/a.png", "http://cdn.example.org:8080/v.mp4", "data:image/png;base64,iVBORw0KGgo=", "https://8.8.8.8/x.png"}
	for _, link := range allowed {
		body := `{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"` + link + `"}}]}]}`
		if _, err := guardEngineControls([]byte(body), false); err != nil {
			t.Errorf("%q refused: %v", link, err)
		}
	}
	// A link-shaped string inside tool-call arguments is data, not a fetch.
	body := `{"messages":[{"role":"assistant","tool_calls":[{"function":{"name":"f","arguments":"{\"image_url\":\"file:///etc/passwd\"}"}}]}]}`
	if _, err := guardEngineControls([]byte(body), false); err != nil {
		t.Errorf("tool arguments refused: %v", err)
	}
	_, err := guardEngineControls([]byte(`{"messages":[{"content":[{"image_url":{"url":"file:///etc/passwd"}}]}]}`), false)
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

func TestGuardEngineControls_TemplateKwargsAllowlist(t *testing.T) {
	in := `{"messages":[],"chat_template_kwargs":{"enable_thinking":true,"thinking_budget":64,"add_generation_prompt":false,"documents":[],"bos_token":"x","custom_tools":"y"}}`
	for _, selfHosted := range []bool{true, false} {
		out, err := guardEngineControls([]byte(in), selfHosted)
		if err != nil {
			t.Fatal(err)
		}
		var got struct {
			KW map[string]any `json:"chat_template_kwargs"`
		}
		if err := json.Unmarshal(out, &got); err != nil {
			t.Fatal(err)
		}
		if len(got.KW) != 2 || got.KW["enable_thinking"] != true || got.KW["thinking_budget"] != float64(64) {
			t.Errorf("selfHosted=%v: kwargs = %v, want only enable_thinking + thinking_budget", selfHosted, got.KW)
		}
	}
}

func TestGuardEngineControls_TopLevelFields(t *testing.T) {
	in := `{"model":"m","messages":[],"temperature":0.2,"tools":[],"ebnf":"root ::= x","enable_search":true,"chat_template":"{{x}}","system":"s","input":[]}`
	keys := func(selfHosted bool) map[string]bool {
		out, err := guardEngineControls([]byte(in), selfHosted)
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		if err := json.Unmarshal(out, &m); err != nil {
			t.Fatal(err)
		}
		k := map[string]bool{}
		for f := range m {
			k[f] = true
		}
		return k
	}
	self := keys(true)
	for _, f := range []string{"model", "messages", "temperature", "tools", "system", "input"} {
		if !self[f] {
			t.Errorf("self-hosted dropped allowed field %s", f)
		}
	}
	for _, f := range []string{"ebnf", "enable_search", "chat_template"} {
		if self[f] {
			t.Errorf("self-hosted forwarded unknown field %s", f)
		}
	}
	fwd := keys(false)
	if !fwd["enable_search"] || !fwd["ebnf"] {
		t.Errorf("forwarder dropped an upstream extension: %v", fwd)
	}
	if fwd["chat_template"] {
		t.Error("forwarder kept chat_template")
	}
}

// Bypass shapes found in router review round 3 (0g-router#1589).
func TestGuardEngineControls_MediaShapes(t *testing.T) {
	refused := []string{
		`{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"type":"x","url":"file:///proc/self/environ"}}]}]}`,
		`{"messages":[{"role":"user","content":[{"type":"image","source":{"type":"file","url":"http://127.0.0.1:8000/get_server_info"}}]}]}`,
		`{"messages":[{"role":"user","content":[{"type":"tool_result","content":[{"type":"image","source":{"type":"url","url":"http://169.254.169.254/"}}]}]}]}`,
		`{"messages":[{"role":"user","content":[{"image_url":"http://127.0.0.1:8000/metrics"}]}]}`,
		`{"messages":[{"role":"user","content":[{"type":null,"video_url":{"url":"file:///etc/passwd"}}]}]}`,
		`{"messages":[{"role":"user","content":[{"\u0069mage_url":{"url":"file:///etc/passwd"}}]}]}`,
		`{"messages":[{"role":"user","content":[{"type":"file","file":{"file_data":"/etc/passwd"}}]}]}`,
		`{"messages":[{"role":"user","content":[{"type":"image","image":"http://10.0.0.1/a.png"}]}]}`,
		`{"messages":[{"role":"user","content":[{"type":"video","video":["data:image/png;base64,AA==","file:///etc/passwd"]}]}]}`,
		`{"messages":[{"role":"user","content":[{"type":"video_url","video_url":{"url":"proc/self/environ"}}]}]}`,
		`{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"x.png"}}]}]}`,
	}
	for _, b := range refused {
		if _, err := guardEngineControls([]byte(b), false); err == nil {
			t.Errorf("accepted: %s", b)
		}
	}
	allowed := []string{
		`{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"iVBORw0KGgoAAAANSUhEUgAAAAEAAAAB"}}]}]}`,
		`{"messages":[{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"iVBORw0KGgo="}}]}]}`,
		`{"messages":[{"role":"user","content":[{"type":"image","source":{"type":"url","url":"https://example.com/a.png"}}]}]}`,
		`{"messages":[{"role":"user","content":[{"type":"search_result","source":"doc.pdf","title":"t","content":[]}]}]}`,
		`{"messages":[{"role":"user","content":[{"type":"file","file":{"file_data":"JVBERi0xLjQK"}}]}]}`,
	}
	for _, b := range allowed {
		if _, err := guardEngineControls([]byte(b), false); err != nil {
			t.Errorf("refused %s: %v", b, err)
		}
	}
}

// Self-hosted engines get media inline only: a public link may redirect inside the CVM.
func TestGuardEngineControls_SelfHostedInlineOnly(t *testing.T) {
	link := `{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"https://example.com/a.png"}}]}]}`
	if _, err := guardEngineControls([]byte(link), true); err == nil || !strings.Contains(err.Error(), "inline") {
		t.Errorf("self-hosted accepted a public link: %v", err)
	}
	if _, err := guardEngineControls([]byte(link), false); err != nil {
		t.Errorf("forwarder refused a public link: %v", err)
	}
	for _, ok := range []string{
		`{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,iVBORw0KGgo="}}]}]}`,
		`{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"iVBORw0KGgoAAAANSUhEUgAAAAEAAAAB"}}]}]}`,
	} {
		if _, err := guardEngineControls([]byte(ok), true); err != nil {
			t.Errorf("self-hosted refused inline media %s: %v", ok, err)
		}
	}
}
