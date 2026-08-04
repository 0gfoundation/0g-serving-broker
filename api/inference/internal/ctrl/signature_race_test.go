package ctrl

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	cerrors "github.com/0glabs/0g-serving-broker/common/errors"
	"github.com/0glabs/0g-serving-broker/inference/config"
	"github.com/0glabs/0g-serving-broker/inference/model"
)

// sigProbeWriter is a minimal http.ResponseWriter that runs onFirstWrite exactly
// once, at the instant the handler flushes the first body bytes. It lets a test
// observe broker state (here: whether the response signature is already cached)
// at flush time rather than only after the handler returns.
type sigProbeWriter struct {
	header       http.Header
	body         bytes.Buffer
	status       int
	wrote        bool
	onFirstWrite func()
}

func (w *sigProbeWriter) Header() http.Header {
	if w.header == nil {
		w.header = http.Header{}
	}
	return w.header
}

func (w *sigProbeWriter) WriteHeader(status int) { w.status = status }

func (w *sigProbeWriter) Write(b []byte) (int, error) {
	if !w.wrote {
		w.wrote = true
		if w.onFirstWrite != nil {
			w.onFirstWrite()
		}
	}
	return w.body.Write(b)
}

// TestHandleChargingResponse_SignsBeforeFlush is the regression guard for issue
// #619 defect 2 (non-streaming path): the response signature must be cached
// BEFORE the body is flushed, so a client that reads ZG-Res-Key and immediately
// fetches GET /v1/proxy/signature/{chatID} cannot lose a race against a post-flush
// cache write. The probe checks the cache at the moment the first body bytes go
// out — before the fix the signature was cached inside decodeAndProcess, which
// runs after the flush, so this observed a miss.
func TestHandleChargingResponse_SignsBeforeFlush(t *testing.T) {
	c := newChatbotTestCtrl(t, config.Service{}) // zero service: decentralized, in-network signing
	c.reconciliationDB = &mockReconciliationDB{}

	probe := &sigProbeWriter{}
	var sigCachedAtFlush bool
	var chatKeyAtFlush string
	probe.onFirstWrite = func() {
		chatKeyAtFlush = probe.Header().Get("ZG-Res-Key")
		if chatKeyAtFlush == "" {
			return
		}
		if _, err := c.GetChatSignature(chatKeyAtFlush); err == nil {
			sigCachedAtFlush = true
		}
	}

	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(probe)
	ctx.Request = httptest.NewRequest("POST", "/v1/chat/completions", nil)

	// No usage field: the whitelisted billing path then skips GetBillingPrices (which
	// needs an on-chain service cache) and just records the request. This test is about
	// signature ordering, not billing.
	respJSON := `{"id":"up-id","model":"gpt-4o","choices":[{"index":0,"message":{"role":"assistant","content":"hi"}}]}`
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{},
		Body:       io.NopCloser(bytes.NewReader([]byte(respJSON))),
	}

	if err := c.handleChargingResponse(ctx, resp, model.User{}, "0",
		[]byte(`{"model":"gpt-4o","messages":[]}`), whitelistReq()); err != nil {
		t.Fatalf("handleChargingResponse: %v", err)
	}

	if chatKeyAtFlush == "" {
		t.Fatal("ZG-Res-Key was not set before the body flush")
	}
	if !sigCachedAtFlush {
		t.Fatal("signature was not cached before the response body was flushed (issue #619 race)")
	}
	// Sanity: the same key still resolves after the handler returns.
	if _, err := c.GetChatSignature(chatKeyAtFlush); err != nil {
		t.Fatalf("GetChatSignature after handler: %v", err)
	}
}

// TestGetChatSignature_MissMapsTo404 is the regression guard for issue #619
// defect 1: a signature cache miss must surface as HTTP 404 (the E2EE client
// retries on 404, not on 400), while still matching the ErrChatIDNotFound
// sentinel so callers can special-case the miss.
func TestGetChatSignature_MissMapsTo404(t *testing.T) {
	c := newChatbotTestCtrl(t, config.Service{})

	_, err := c.GetChatSignature("never-issued-chat-id")
	if err == nil {
		t.Fatal("expected an error on cache miss")
	}
	if !cerrors.Is(err, ErrChatIDNotFound) {
		t.Fatalf("miss error must still match ErrChatIDNotFound sentinel, got %v", err)
	}
	var he *cerrors.HTTPError
	if !cerrors.As(err, &he) {
		t.Fatalf("miss error must carry an *HTTPError, got %T", err)
	}
	if he.Status() != http.StatusNotFound {
		t.Fatalf("miss must map to 404, got %d", he.Status())
	}

	// End-to-end through errors.Response, including the extra Wrap the proxy layer
	// adds (proxy.go handleBrokerError): the client must receive 404, not the
	// default 400, with the chat_id_not_found marker intact.
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	cerrors.Response(ctx, cerrors.Wrap(err, "prepare HTTP request"))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("errors.Response wrote status %d, want 404", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "chat_id_not_found") {
		t.Fatalf("404 body missing chat_id_not_found marker: %s", rec.Body.String())
	}
}
