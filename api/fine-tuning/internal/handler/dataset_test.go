package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestDeleteDatasetMissingParams(t *testing.T) {
	gin.SetMode(gin.TestMode)

	h := &Handler{}

	tests := []struct {
		name       string
		url        string
		wantCode   int
		wantSubstr string
	}{
		{
			name:       "missing signature",
			url:        "/v1/user/0xABC/dataset/0xDEF?timestamp=12345",
			wantCode:   http.StatusBadRequest,
			wantSubstr: "signature is required",
		},
		{
			name:       "missing timestamp",
			url:        "/v1/user/0xABC/dataset/0xDEF?signature=0xabc",
			wantCode:   http.StatusBadRequest,
			wantSubstr: "timestamp is required",
		},
		{
			name:       "invalid timestamp",
			url:        "/v1/user/0xABC/dataset/0xDEF?signature=0xabc&timestamp=notanumber",
			wantCode:   http.StatusBadRequest,
			wantSubstr: "invalid timestamp format",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := gin.New()
			r.DELETE("/v1/user/:userAddress/dataset/:datasetHash", h.DeleteDataset)

			req := httptest.NewRequest(http.MethodDelete, tt.url, nil)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)

			if w.Code != tt.wantCode {
				t.Errorf("status = %d, want %d", w.Code, tt.wantCode)
			}
			body := w.Body.String()
			if tt.wantSubstr != "" && !contains(body, tt.wantSubstr) {
				t.Errorf("body %q does not contain %q", body, tt.wantSubstr)
			}
		})
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchSubstring(s, substr)
}

func searchSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
