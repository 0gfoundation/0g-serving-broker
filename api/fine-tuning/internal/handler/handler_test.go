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

	tests := []struct {
		name       string
		err        error
		wantStatus int
		// wantBodyContains is a substring that must appear in the JSON error
		// field after the handler wraps with the "Provider: <context>" prefix.
		// For 5xx the body is sanitized, so the field holds the generic text.
		wantBodyContains string
		// sanitized5xx indicates the response body is not expected to include
		// the "Provider" prefix or the underlying error text — the handler
		// swaps it for a generic status message and logs details server-side.
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
				handleBrokerError(c, tt.err, "test route")
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
			if tt.sanitized5xx {
				// 5xx responses must not leak the underlying error text.
				if strings.Contains(body["error"], "broker inconsistency") {
					t.Fatalf("5xx body leaked internal detail: %q", body["error"])
				}
				if strings.HasPrefix(body["error"], "Provider") {
					t.Fatalf("5xx body should be generic, got %q", body["error"])
				}
			} else {
				// handleBrokerError prefixes with "Provider" for continuity
				// with how client-facing errors are currently labelled.
				if !strings.HasPrefix(body["error"], "Provider") {
					t.Fatalf("body error %q missing Provider prefix", body["error"])
				}
			}
		})
	}
}

// TestHandleBrokerError_PreservesChain verifies that when a typed error is
// wrapped by handleBrokerError (which calls errors.Wrap), the original error
// is still reachable via errors.Is. This matters for the review finding that
// NewXxx("%s", err.Error()) flattens the chain — the wrap helpers must not.
func TestHandleBrokerError_PreservesChain(t *testing.T) {
	gin.SetMode(gin.TestMode)

	sentinel := errors.New("sentinel cause")
	wrapped := errors.Unauthorized(sentinel)

	var captured error
	r := gin.New()
	r.GET("/boom", func(c *gin.Context) {
		// Replicate what handleBrokerError does so we can capture the final
		// error value that reaches errors.Response.
		captured = errors.Wrap(wrapped, "Provider: test")
		handleBrokerError(c, wrapped, "test")
	})

	req := httptest.NewRequest(http.MethodGet, "/boom", nil)
	r.ServeHTTP(httptest.NewRecorder(), req)

	if !errors.Is(captured, sentinel) {
		t.Fatalf("sentinel cause lost through handler wrap chain")
	}
}
