package ctrl

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/0glabs/0g-serving-broker/inference/config"
)

func newRequestProfileTestCtrl() *Ctrl {
	return &Ctrl{Service: config.Service{Type: "chatbot", ModelType: "glm-5.3"}, logger: testLogger()}
}

func decodeProfileBody(t *testing.T, b []byte) map[string]interface{} {
	t.Helper()
	var m map[string]interface{}
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("invalid json %s: %v", b, err)
	}
	return m
}

// Claude Code's display values SGLang does not list are dropped; thinking itself,
// and so thinking on, stays.
func TestApplyRequestProfile_DropsUnknownThinkingDisplay(t *testing.T) {
	c := newRequestProfileTestCtrl()
	for _, display := range []string{"updates", "highlights"} {
		body := []byte(`{"model":"glm-5.3","max_tokens":64,"messages":[],"thinking":{"type":"adaptive","display":"` + display + `"}}`)
		got, err := c.ApplyRequestProfile(body, config.RequestProfileSGLang, config.APIFormatAnthropic, "glm-5.3", true)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		thinking, ok := decodeProfileBody(t, got)["thinking"].(map[string]interface{})
		if !ok {
			t.Fatalf("thinking dropped: %s", got)
		}
		if _, present := thinking["display"]; present {
			t.Errorf("display %q not dropped: %s", display, got)
		}
		if thinking["type"] != "adaptive" {
			t.Errorf("thinking.type = %v, want adaptive", thinking["type"])
		}
	}
}

// Accepted values, non-string values, absent fields and other surfaces leave the
// body byte-for-byte unchanged.
func TestApplyRequestProfile_KeepsWhatTheEngineAccepts(t *testing.T) {
	c := newRequestProfileTestCtrl()
	cases := []struct {
		name, surface, body string
	}{
		{"accepted display", config.APIFormatAnthropic, `{"thinking":{"type":"enabled","budget_tokens":2048,"display":"summarized"}}`},
		{"null display", config.APIFormatAnthropic, `{"thinking":{"type":"adaptive","display":null}}`},
		{"no thinking", config.APIFormatAnthropic, `{"messages":[]}`},
		{"thinking not an object", config.APIFormatAnthropic, `{"thinking":"on"}`},
		{"accepted effort", config.APIFormatAnthropic, `{"output_config":{"effort":"xhigh"}}`},
		{"numeric reasoning_effort", config.APIFormatOpenAI, `{"reasoning_effort":0.5}`},
		{"accepted reasoning_effort", config.APIFormatOpenAI, `{"reasoning_effort":"max"}`},
		{"numeric-string reasoning_effort", config.APIFormatOpenAI, `{"reasoning_effort":"0.5"}`},
		{"out-of-range numeric string is SGLang's to judge", config.APIFormatOpenAI, `{"reasoning_effort":"1.5"}`},
		{"underscored numeric string pydantic accepts", config.APIFormatOpenAI, `{"reasoning_effort":"0._5"}`},
		{"display on the openai surface", config.APIFormatOpenAI, `{"thinking":{"display":"updates"}}`},
		{"unrecognized surface", "", `{"thinking":{"display":"updates"}}`},
		{"not json", config.APIFormatAnthropic, `not json`},
	}
	for _, tc := range cases {
		got, err := c.ApplyRequestProfile([]byte(tc.body), config.RequestProfileSGLang, tc.surface, "glm-5.3", true)
		if err != nil {
			t.Fatalf("%s: unexpected error: %v", tc.name, err)
		}
		if string(got) != tc.body {
			t.Errorf("%s: body changed to %s", tc.name, got)
		}
	}
}

func TestApplyRequestProfile_DropsUnknownEffortTiers(t *testing.T) {
	c := newRequestProfileTestCtrl()

	got, err := c.ApplyRequestProfile([]byte(`{"output_config":{"effort":"ultra","task_budget":{"type":"tokens","total":1000}}}`), config.RequestProfileSGLang, config.APIFormatAnthropic, "glm-5.3", true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	oc, _ := decodeProfileBody(t, got)["output_config"].(map[string]interface{})
	if _, present := oc["effort"]; present || oc["task_budget"] == nil {
		t.Errorf("want only output_config.effort dropped, got %s", got)
	}

	got, err = c.ApplyRequestProfile([]byte(`{"reasoning_effort":"ultra","temperature":0.7}`), config.RequestProfileSGLang, config.APIFormatOpenAI, "glm-5.3", true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	m := decodeProfileBody(t, got)
	if _, present := m["reasoning_effort"]; present || m["temperature"] == nil {
		t.Errorf("want only reasoning_effort dropped, got %s", got)
	}
}

// Log-only mode forwards the body untouched even when a rule matches.
func TestApplyRequestProfile_LogOnlyLeavesBody(t *testing.T) {
	c := newRequestProfileTestCtrl()
	body := []byte(`{"thinking":{"type":"adaptive","display":"updates"}}`)
	got, err := c.ApplyRequestProfile(body, config.RequestProfileSGLang, config.APIFormatAnthropic, "glm-5.3", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !bytes.Equal(got, body) {
		t.Errorf("log-only mode changed the body to %s", got)
	}
}

// The profile only matters if the forward path runs it, with the surface taken
// from the request path and the per-model profile taking effect.
func TestPrepareHTTPRequest_AppliesRequestProfile(t *testing.T) {
	body := []byte(`{"model":"glm-5.3","max_tokens":64,"messages":[{"role":"user","content":"hi"}],"thinking":{"type":"adaptive","display":"updates"}}`)
	forward := func(svc config.Service) map[string]interface{} {
		t.Helper()
		c := newChatbotTestCtrl(t, svc)
		ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
		ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))
		req, err := c.PrepareHTTPRequest(ctx, "http://upstream.invalid/v1/messages", body, "chatbot")
		if err != nil {
			t.Fatalf("PrepareHTTPRequest: %v", err)
		}
		forwarded, _ := io.ReadAll(req.Body)
		thinking, _ := decodeProfileBody(t, forwarded)["thinking"].(map[string]interface{})
		return thinking
	}
	base := config.Service{Type: "chatbot", ModelType: "glm-5.3", ProviderType: "centralized", ProviderIdentity: "self"}

	enforced := base
	enforced.RequestProfile = config.RequestProfileSGLang
	enforced.RequestProfileEnforce = true
	if _, present := forward(enforced)["display"]; present {
		t.Errorf("enforced profile did not drop thinking.display on /v1/messages")
	}

	logOnly := base
	logOnly.RequestProfile = config.RequestProfileSGLang
	if forward(logOnly)["display"] != "updates" {
		t.Errorf("log-only profile changed thinking.display")
	}

	if forward(base)["display"] != "updates" {
		t.Errorf("no profile configured, yet thinking.display changed")
	}

	perModel := base
	perModel.RequestProfileEnforce = true
	perModel.ModelPricing = []config.ModelPricingEntry{{Model: "glm-5.3", RequestProfile: config.RequestProfileSGLang}}
	if err := perModel.BuildModelPricingMap(); err != nil {
		t.Fatalf("BuildModelPricingMap: %v", err)
	}
	if _, present := forward(perModel)["display"]; present {
		t.Errorf("per-model profile did not drop thinking.display")
	}
}
