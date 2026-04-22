package errors

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
)

func TestResponse_StatusMapping(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name       string
		err        error
		wantStatus int
		// wantBody is compared to the JSON "error" field. For 5xx statuses
		// the body is expected to be sanitized (generic status text) so the
		// underlying error message is not leaked to the client.
		wantBody string
	}{
		{"plain error defaults to 400", New("boom"), http.StatusBadRequest, "boom"},
		{"bad request", NewBadRequest("bad"), http.StatusBadRequest, "bad"},
		{"unauthorized", NewUnauthorized("no creds"), http.StatusUnauthorized, "no creds"},
		{"forbidden", NewForbidden("nope"), http.StatusForbidden, "nope"},
		{"not found", NewNotFound("missing"), http.StatusNotFound, "missing"},
		{"conflict", NewConflict("busy"), http.StatusConflict, "busy"},
		{"internal is sanitized", NewInternal("driver oops"), http.StatusInternalServerError, http.StatusText(http.StatusInternalServerError)},
		{"internal wrap is sanitized", Internal(New("schema column not found")), http.StatusInternalServerError, http.StatusText(http.StatusInternalServerError)},
		{"wrapped unauthorized", Wrap(NewUnauthorized("bad sig"), "provider"), http.StatusUnauthorized, "provider: bad sig"},
		{"wrap helper preserves chain status", Unauthorized(New("bad sig")), http.StatusUnauthorized, "bad sig"},
		{"wrap helper through Wrap", Wrap(Unauthorized(New("bad sig")), "provider"), http.StatusUnauthorized, "provider: bad sig"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(rec)
			ctx.Request = httptest.NewRequest(http.MethodGet, "/test", nil)
			Response(ctx, tt.err)
			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tt.wantStatus)
			}

			var body map[string]string
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("unmarshal body: %v", err)
			}
			if body["error"] != tt.wantBody {
				t.Fatalf("body error = %q, want %q", body["error"], tt.wantBody)
			}
		})
	}
}

// TestResponse_5xxLogsServerSide verifies that a 5xx Response logs the full
// underlying error to the server-side logger even though the client sees a
// generic message. This guards against the "silent on the server, chatty on
// the wire" failure mode.
func TestResponse_5xxLogsServerSide(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var buf bytes.Buffer
	prevOut := log.StandardLogger().Out
	prevLevel := log.StandardLogger().Level
	log.SetOutput(&buf)
	log.SetLevel(log.InfoLevel)
	t.Cleanup(func() {
		log.SetOutput(prevOut)
		log.SetLevel(prevLevel)
	})

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/test", nil)

	sensitive := "driver detail: connection refused at /var/lib/mysql.sock"
	Response(ctx, NewInternal("%s", sensitive))

	// Client sees sanitized body.
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}
	if strings.Contains(body["error"], "driver detail") {
		t.Fatalf("5xx body leaked internal detail: %q", body["error"])
	}

	// Server log captures the full error.
	if !strings.Contains(buf.String(), sensitive) {
		t.Fatalf("expected server log to contain %q, got %q", sensitive, buf.String())
	}
}

func TestWrapHelpersPreserveChain(t *testing.T) {
	sentinel := New("sentinel")
	cases := []error{
		Unauthorized(sentinel),
		Forbidden(sentinel),
		NotFound(sentinel),
		Conflict(sentinel),
		Internal(sentinel),
		Wrap(Unauthorized(sentinel), "provider"),
	}
	for _, err := range cases {
		if !Is(err, sentinel) {
			t.Fatalf("wrap helper broke error chain for %v", err)
		}
	}
}
