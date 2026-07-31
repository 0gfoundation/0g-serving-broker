package handler

import (
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// TestWriteProviderError_ValidationErrorIs400 pins the mapping newValidationError
// exists for: a Provider method returning a validationError (e.g. Vidu's
// pre-flight request checks) must surface as 400, not the generic 502 every
// other non-vendor-4xx error gets — the request itself is wrong and retrying
// it identically can never succeed.
func TestWriteProviderError_ValidationErrorIs400(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)

	h := &GenericVideoHandler{logger: newTestLogger(t)}
	h.writeProviderError(c, "test", "fallback message", newValidationError(errors.New("both first_frame and last_frame reference images are required")))

	if rec.Code != 400 {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "both first_frame and last_frame") {
		t.Errorf("body = %q, want the validation error's own message, not the generic fallback", rec.Body.String())
	}
}

// TestNewValidationError_NilIsNil pins newValidationError(nil)'s
// nil-passthrough — writeProviderError is never actually called with a nil
// err in production, but this guards against a future change making
// newValidationError wrap a nil inner error, which would panic on
// Error()/Unwrap() elsewhere.
func TestNewValidationError_NilIsNil(t *testing.T) {
	if err := newValidationError(nil); err != nil {
		t.Errorf("newValidationError(nil) = %v, want nil", err)
	}
}
