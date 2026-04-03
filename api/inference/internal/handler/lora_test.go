package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/0glabs/0g-serving-broker/inference/config"
	"github.com/0glabs/0g-serving-broker/inference/internal/ctrl"
	"github.com/0glabs/0g-serving-broker/inference/internal/lora"
	"github.com/0glabs/0g-serving-broker/inference/model"
)

func init() {
	gin.SetMode(gin.TestMode)
}

func newHandlerNoLoRA(t *testing.T) *Handler {
	t.Helper()
	return &Handler{ctrl: &ctrl.Ctrl{Service: config.Service{ModelType: "Qwen2.5-7B"}}}
}

func TestToAdapterResponse(t *testing.T) {
	info := &lora.AdapterInfo{
		AdapterName:     "ft-base-task001",
		TaskID:          "task-001",
		BaseModel:       "Qwen2.5-7B",
		State:           model.AdapterStateActive,
		UserAddress:     "0xAlice",
		StorageRootHash: "0xdeadbeef",
	}

	resp := toAdapterResponse(info)
	if resp.AdapterName != "ft-base-task001" {
		t.Errorf("AdapterName = %q", resp.AdapterName)
	}
	if resp.State != "active" {
		t.Errorf("State = %q, want active", resp.State)
	}
	if resp.TaskID != "task-001" {
		t.Errorf("TaskID = %q", resp.TaskID)
	}
}

func TestDeployAdapter_RequiresAuth(t *testing.T) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)

	body, _ := json.Marshal(map[string]string{"adapterName": "ft-test"})
	c.Request = httptest.NewRequest("POST", "/v1/lora/adapters/deploy", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	h := newHandlerNoLoRA(t)
	h.DeployAdapter(c)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d; body = %s", w.Code, http.StatusUnauthorized, w.Body.String())
	}
}

func TestGetAdapterStatus_RequiresAuth(t *testing.T) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/v1/lora/adapters/ft-test", nil)
	c.Params = gin.Params{{Key: "name", Value: "ft-test"}}

	h := newHandlerNoLoRA(t)
	h.GetAdapterStatus(c)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", w.Code, http.StatusUnauthorized)
	}
}

func TestListAdapters_RequiresAuth(t *testing.T) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/v1/lora/adapters", nil)

	h := newHandlerNoLoRA(t)
	h.ListAdapters(c)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", w.Code, http.StatusUnauthorized)
	}
}

func TestAdapterKeyRequest_Validation(t *testing.T) {
	tests := []struct {
		name       string
		body       interface{}
		wantStatus int
	}{
		{
			name:       "missing all fields",
			body:       map[string]string{},
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "missing providerEncKey",
			body:       map[string]string{"taskId": "t1", "storageHash": "0x1"},
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "missing storageHash",
			body:       map[string]string{"taskId": "t1", "providerEncKey": "0xkey"},
			wantStatus: http.StatusBadRequest,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)

			body, _ := json.Marshal(tt.body)
			c.Request = httptest.NewRequest("POST", "/internal/v1/adapter-keys", bytes.NewReader(body))
			c.Request.Header.Set("Content-Type", "application/json")

			h := &Handler{}
			h.ReceiveAdapterKey(c)

			if w.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d; body = %s", w.Code, tt.wantStatus, w.Body.String())
			}
		})
	}
}
