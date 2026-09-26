package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/0glabs/0g-serving-broker/controller/internal/ctrl"
)

// PUT /v1/config/core as a client sees it: the success body lists what the broker
// ignores, and a refusal still maps to 400.
func TestUpdateCoreConfigResponse(t *testing.T) {
	gin.SetMode(gin.TestMode)
	put := func(t *testing.T, apply func(context.Context, string) error, content string) (int, map[string]any) {
		t.Helper()
		h := &Handler{applyCoreConfig: apply}
		r := gin.New()
		r.PUT("/v1/config/core", h.UpdateCoreConfig)
		reqBody, _ := json.Marshal(map[string]string{"config": content})
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodPut, "/v1/config/core", strings.NewReader(string(reqBody))))
		var body map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &body)
		return w.Code, body
	}
	ok := func(context.Context, string) error { return nil }

	code, body := put(t, ok, "service:\n  model: x\nfutureFeature: 1\n")
	keys, _ := body["ignoredKeys"].([]any)
	if code != http.StatusOK || len(keys) != 1 || !strings.Contains(keys[0].(string), `"futureFeature"`) || body["warning"] == nil {
		t.Fatalf("status=%d body=%v, want 200 listing the one ignored key with a warning", code, body)
	}

	code, body = put(t, ok, "service:\n  model: x\n")
	if _, has := body["ignoredKeys"]; code != http.StatusOK || has || body["warning"] != nil {
		t.Fatalf("status=%d body=%v, want a plain 200 when nothing is ignored", code, body)
	}

	refuse := func(context.Context, string) error { return &ctrl.InvalidConfigError{Err: errors.New("nope")} }
	if code, _ := put(t, refuse, "service:\n  model: x\n"); code != http.StatusBadRequest {
		t.Fatalf("refusal status = %d, want 400", code)
	}
}
