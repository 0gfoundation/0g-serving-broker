package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/0glabs/0g-serving-broker/common/errors"
)

// TestHandleBrokerError_StatusCodes exercises the handler -> errors.Response
// plumbing end-to-end: a handler that calls handleBrokerError with a typed
// error must surface the attached HTTP status through the gin response,
// while plain errors still default to 400. This guards against regressions
// where a call site forgets to route through errors.Response or drops the
// error chain.
func TestHandleBrokerError_StatusCodes(t *testing.T) {
	gin.SetMode(gin.TestMode)

	const routeContext = "test route"

	tests := []struct {
		name       string
		err        error
		wantStatus int
		// wantBodyContains is a substring that must appear in the JSON error
		// field. For 5xx the body is sanitized, so the field holds the
		// generic status-text rather than the underlying error message.
		wantBodyContains string
		// sanitized5xx marks cases where the body must not include the
		// underlying error text — the handler swaps it for a generic status
		// message and logs details server-side.
		sanitized5xx bool
	}{
		{
			name:             "plain error defaults to 400",
			err:              errors.New("oops"),
			wantStatus:       http.StatusBadRequest,
			wantBodyContains: "oops",
		},
		{
			name:             "NewBadRequest -> 400",
			err:              errors.NewBadRequest("bad input"),
			wantStatus:       http.StatusBadRequest,
			wantBodyContains: "bad input",
		},
		{
			name:             "NewUnauthorized -> 401",
			err:              errors.NewUnauthorized("bad signature"),
			wantStatus:       http.StatusUnauthorized,
			wantBodyContains: "bad signature",
		},
		{
			name:             "NewForbidden -> 403",
			err:              errors.NewForbidden("not your task"),
			wantStatus:       http.StatusForbidden,
			wantBodyContains: "not your task",
		},
		{
			name:             "NewNotFound -> 404",
			err:              errors.NewNotFound("task %s", "abc"),
			wantStatus:       http.StatusNotFound,
			wantBodyContains: "task abc",
		},
		{
			name:             "NewConflict -> 409 (simulates terminal-state cancel)",
			err:              errors.NewConflict("task cannot be cancelled in its current state (Trained)"),
			wantStatus:       http.StatusConflict,
			wantBodyContains: "Trained",
		},
		{
			name:             "NewInternal -> 500 (body sanitized)",
			err:              errors.NewInternal("broker inconsistency"),
			wantStatus:       http.StatusInternalServerError,
			wantBodyContains: http.StatusText(http.StatusInternalServerError),
			sanitized5xx:     true,
		},
		{
			name:             "chain-preserving wrap -> 401",
			err:              errors.Unauthorized(errors.New("invalid sig v")),
			wantStatus:       http.StatusUnauthorized,
			wantBodyContains: "invalid sig v",
		},
		{
			// Sends a typed error through handleBrokerError's errors.Wrap and
			// asserts the HTTPError status + underlying message both survive
			// the wrap — this is the effective chain-preservation guarantee.
			name:             "typed error survives errors.Wrap inside handleBrokerError",
			err:              errors.NotFound(gorm.ErrRecordNotFound),
			wantStatus:       http.StatusNotFound,
			wantBodyContains: gorm.ErrRecordNotFound.Error(),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := gin.New()
			r.GET("/boom", func(c *gin.Context) {
				handleBrokerError(c, tt.err, routeContext)
			})

			req := httptest.NewRequest(http.MethodGet, "/boom", nil)
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d (body=%s)", rec.Code, tt.wantStatus, rec.Body.String())
			}

			var body map[string]string
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("unmarshal body: %v (raw=%q)", err, rec.Body.String())
			}
			if !strings.Contains(body["error"], tt.wantBodyContains) {
				t.Fatalf("body error %q does not contain %q", body["error"], tt.wantBodyContains)
			}
			// No handler should synthesise a "Provider" prefix — bodies must
			// be uniform across direct errors.Response calls and handleBrokerError.
			if strings.HasPrefix(body["error"], "Provider") {
				t.Fatalf("body error %q contains legacy Provider prefix", body["error"])
			}
			if tt.sanitized5xx {
				// 5xx responses must not leak the underlying error text.
				if strings.Contains(body["error"], "broker inconsistency") {
					t.Fatalf("5xx body leaked internal detail: %q", body["error"])
				}
			} else {
				// Non-5xx bodies should carry the handleBrokerError context
				// so the caller can identify which handler failed.
				if !strings.Contains(body["error"], routeContext) {
					t.Fatalf("body error %q missing route context %q", body["error"], routeContext)
				}
			}
		})
	}
}
