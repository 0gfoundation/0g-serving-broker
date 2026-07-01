package ctrl

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/0glabs/0g-serving-broker/inference/config"
)

// newErrorResp builds a minimal upstream *http.Response carrying an error status
// and a JSON body that names the upstream via a #184 leak key ("provider").
func newErrorResp(body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusBadRequest,
		Header:     http.Header{},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func runHandleServiceError(t *testing.T, svc config.Service, body string) string {
	t.Helper()
	gin.SetMode(gin.TestMode)
	c := newChatbotTestCtrl(t, svc)
	w := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(w)
	ctx.Request = httptest.NewRequest("POST", "/v1/proxy/chat/completions", nil)
	c.handleServiceError(ctx, newErrorResp(body))
	return w.Body.String()
}

func TestHandleServiceError_ForwarderSanitizesUpstreamErrorBody(t *testing.T) {
	// A standard forwarder must not leak its upstream via an error body. The
	// "provider" leak key is stripped before the body is re-emitted to the client.
	body := `{"error":{"message":"bad request"},"provider":"openai"}`
	out := runHandleServiceError(t, config.Service{
		ProviderType:    "standard",
		TargetSeparated: true,
		TargetURL:       "https://secret-upstream:8000",
	}, body)
	if strings.Contains(out, "openai") || strings.Contains(out, "\"provider\"") {
		t.Errorf("upstream identity leaked in forwarder error body: %s", out)
	}
	if !strings.Contains(out, "bad request") {
		t.Errorf("error message should be preserved, got: %s", out)
	}
}

func TestSanitizeSTTStreamLine(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c := newChatbotTestCtrl(t, config.Service{ProviderType: "standard", TargetSeparated: true})
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest("POST", "/v1/proxy/audio/transcriptions", nil)

	t.Run("bare ND-JSON line strips leak key, preserves usage", func(t *testing.T) {
		out := c.sanitizeSTTStreamLine(ctx, []byte(`{"type":"transcript.text.done","provider":"openai","usage":{"type":"duration","seconds":3}}`))
		if out == nil {
			t.Fatal("expected line forwarded, got dropped")
		}
		if bytes.Contains(out, []byte("openai")) || bytes.Contains(out, []byte("\"provider\"")) {
			t.Errorf("provider leaked in STT stream line: %s", out)
		}
		if !bytes.Contains(out, []byte("transcript.text.done")) || !bytes.Contains(out, []byte("duration")) {
			t.Errorf("type/usage must survive: %s", out)
		}
	})

	t.Run("SSE data: line strips leak key", func(t *testing.T) {
		out := c.sanitizeSTTStreamLine(ctx, []byte("data: {\"provider\":\"openai\",\"delta\":\"hi\"}\n"))
		if out == nil || bytes.Contains(out, []byte("openai")) {
			t.Errorf("SSE data line not sanitized: %s", out)
		}
		if !bytes.Contains(out, []byte("hi")) {
			t.Errorf("delta content must survive: %s", out)
		}
	})

	t.Run("upstream comment banner is dropped", func(t *testing.T) {
		if out := c.sanitizeSTTStreamLine(ctx, []byte(": OPENROUTER PROCESSING\n")); out != nil {
			t.Errorf("comment banner should be dropped, got: %s", out)
		}
	})

	t.Run("plain non-JSON line passes through", func(t *testing.T) {
		out := c.sanitizeSTTStreamLine(ctx, []byte("plain text\n"))
		if out == nil || !bytes.Contains(out, []byte("plain text")) {
			t.Errorf("plain line should pass through, got: %q", out)
		}
	})
}

func TestHandleServiceError_DecentralizedForwardsErrorBodyUnchanged(t *testing.T) {
	// For a decentralized provider the "upstream" is the provider's own service, so
	// the error body is forwarded verbatim (no forwarder sanitization applied).
	body := `{"error":{"message":"bad request"},"provider":"self"}`
	out := runHandleServiceError(t, config.Service{
		ProviderType: "decentralized",
	}, body)
	if !strings.Contains(out, "\"provider\":\"self\"") {
		t.Errorf("decentralized error body should be forwarded unchanged, got: %s", out)
	}
}
