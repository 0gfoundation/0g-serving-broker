package ctrl

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/0glabs/0g-serving-broker/inference/config"
)

func TestApiFormatForPath(t *testing.T) {
	cases := []struct {
		path string
		want string
	}{
		{"/v1/messages", config.APIFormatAnthropic},
		{"/messages", config.APIFormatAnthropic},
		{"/messages/", config.APIFormatAnthropic}, // trailing slash tolerated
		{"/v1/chat/completions", config.APIFormatOpenAI},
		{"/chat/completions", config.APIFormatOpenAI},
		// Production passes the ServicePrefix-prefixed URL.Path, not the bare route;
		// suffix matching must stay independent of that prefix.
		{"/v1/proxy/v1/messages", config.APIFormatAnthropic},
		{"/v1/proxy/chat/completions", config.APIFormatOpenAI},
		{"/v1/models", ""},
		{"/v1/images/generations", ""},
		{"/", ""},
	}
	for _, tt := range cases {
		if got := apiFormatForPath(tt.path); got != tt.want {
			t.Errorf("apiFormatForPath(%q) = %q, want %q", tt.path, got, tt.want)
		}
	}
}

func TestFormatAllowed(t *testing.T) {
	cases := []struct {
		name    string
		formats []string
		surface string
		want    bool
	}{
		{"unconstrained allows anything", nil, "anthropic", true},
		{"empty surface never gated", []string{"anthropic"}, "", true},
		{"declared surface allowed", []string{"anthropic"}, "anthropic", true},
		{"undeclared surface rejected", []string{"anthropic"}, "openai", false},
		{"one of several allowed", []string{"openai", "anthropic"}, "anthropic", true},
		{"case-insensitive", []string{"OpenAI"}, "openai", true},
		{"whitespace tolerated", []string{" anthropic "}, "anthropic", true},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			if got := formatAllowed(tt.formats, tt.surface); got != tt.want {
				t.Errorf("formatAllowed(%v, %q) = %v, want %v", tt.formats, tt.surface, got, tt.want)
			}
		})
	}
}

// ginCtxWithPathAndModel builds a gin.Context carrying an incoming request path
// and (optionally) a resolved model, as the request path would present them to
// enforceRequestFormat.
func ginCtxWithPathAndModel(path, model string) *gin.Context {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, path, nil)
	if model != "" {
		c.Set(CtxKeyResolvedModel, model)
	}
	return c
}

func TestEnforceRequestFormat(t *testing.T) {
	// "claude" is exposed only on the Anthropic surface; "open" declares nothing
	// (unconstrained → both surfaces accepted).
	svc := newMultiModelService(t, "NATIVE", []config.ModelPricingEntry{
		{Model: "claude", InputPrice: "1", OutputPrice: "2", ModelInfo: &config.ModelInfo{SupportedFormats: []string{"anthropic"}}},
		{Model: "open", InputPrice: "1", OutputPrice: "2"},
	}, "open")
	c := &Ctrl{logger: testLogger(), Service: svc}

	cases := []struct {
		name    string
		path    string
		model   string
		wantErr bool
	}{
		{"anthropic-only model on /v1/messages ok", "/v1/messages", "claude", false},
		{"anthropic-only model on /chat/completions rejected", "/v1/chat/completions", "claude", true},
		{"unconstrained model on /chat/completions ok", "/v1/chat/completions", "open", false},
		{"unconstrained model on /v1/messages ok", "/v1/messages", "open", false},
		{"non-chat path never gated", "/v1/models", "claude", false},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			err := c.enforceRequestFormat(ginCtxWithPathAndModel(tt.path, tt.model), tt.model)
			if (err != nil) != tt.wantErr {
				t.Fatalf("enforceRequestFormat(path=%q, model=%q): err=%v, wantErr=%v", tt.path, tt.model, err, tt.wantErr)
			}
		})
	}
}
