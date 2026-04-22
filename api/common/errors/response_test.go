package errors

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestResponse_StatusMapping(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name       string
		err        error
		wantStatus int
	}{
		{"plain error defaults to 400", New("boom"), http.StatusBadRequest},
		{"bad request", NewBadRequest("bad"), http.StatusBadRequest},
		{"unauthorized", NewUnauthorized("no creds"), http.StatusUnauthorized},
		{"forbidden", NewForbidden("nope"), http.StatusForbidden},
		{"not found", NewNotFound("missing"), http.StatusNotFound},
		{"conflict", NewConflict("busy"), http.StatusConflict},
		{"internal", NewInternal("oops"), http.StatusInternalServerError},
		{"wrapped unauthorized", Wrap(NewUnauthorized("bad sig"), "provider"), http.StatusUnauthorized},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(rec)
			Response(ctx, tt.err)
			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tt.wantStatus)
			}

			var body map[string]string
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("unmarshal body: %v", err)
			}
			if body["error"] != tt.err.Error() {
				t.Fatalf("body error = %q, want %q", body["error"], tt.err.Error())
			}
		})
	}
}
