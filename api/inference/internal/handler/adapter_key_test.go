package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestReceiveAdapterKey_MissingAllFields(t *testing.T) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)

	body, _ := json.Marshal(map[string]string{})
	c.Request = httptest.NewRequest("POST", "/internal/v1/adapter-keys", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	h := &Handler{}
	h.ReceiveAdapterKey(c)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d; body = %s", w.Code, http.StatusBadRequest, w.Body.String())
	}
}

func TestReceiveAdapterKey_MissingTaskID(t *testing.T) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)

	body, _ := json.Marshal(map[string]string{
		"storageHash":    "0xhash",
		"providerEncKey": "0xkey",
	})
	c.Request = httptest.NewRequest("POST", "/internal/v1/adapter-keys", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	h := &Handler{}
	h.ReceiveAdapterKey(c)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestReceiveAdapterKey_MissingStorageHash(t *testing.T) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)

	body, _ := json.Marshal(map[string]string{
		"taskId":         "task-001",
		"providerEncKey": "0xkey",
	})
	c.Request = httptest.NewRequest("POST", "/internal/v1/adapter-keys", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	h := &Handler{}
	h.ReceiveAdapterKey(c)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestReceiveAdapterKey_MissingProviderEncKey(t *testing.T) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)

	body, _ := json.Marshal(map[string]string{
		"taskId":      "task-001",
		"storageHash": "0xhash",
	})
	c.Request = httptest.NewRequest("POST", "/internal/v1/adapter-keys", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	h := &Handler{}
	h.ReceiveAdapterKey(c)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestReceiveAdapterKey_InvalidJSON(t *testing.T) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)

	c.Request = httptest.NewRequest("POST", "/internal/v1/adapter-keys", bytes.NewReader([]byte("not json")))
	c.Request.Header.Set("Content-Type", "application/json")

	h := &Handler{}
	h.ReceiveAdapterKey(c)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestReceiveAdapterKey_EmptyBody(t *testing.T) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)

	c.Request = httptest.NewRequest("POST", "/internal/v1/adapter-keys", bytes.NewReader([]byte{}))
	c.Request.Header.Set("Content-Type", "application/json")

	h := &Handler{}
	h.ReceiveAdapterKey(c)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestReceiveAdapterKey_NullBody(t *testing.T) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)

	c.Request = httptest.NewRequest("POST", "/internal/v1/adapter-keys", bytes.NewReader([]byte("null")))
	c.Request.Header.Set("Content-Type", "application/json")

	h := &Handler{}
	h.ReceiveAdapterKey(c)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d for null body", w.Code, http.StatusBadRequest)
	}
}

func TestReceiveAdapterKey_EmptyStringFields(t *testing.T) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)

	body, _ := json.Marshal(map[string]string{
		"taskId":         "",
		"storageHash":    "",
		"providerEncKey": "",
	})
	c.Request = httptest.NewRequest("POST", "/internal/v1/adapter-keys", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	h := &Handler{}
	h.ReceiveAdapterKey(c)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d for empty required fields", w.Code, http.StatusBadRequest)
	}
}

func TestAdapterKeyRequest_FieldMapping(t *testing.T) {
	payload := `{"taskId":"t1","storageHash":"0xhash","providerEncKey":"0xkey"}`
	var req adapterKeyRequest
	if err := json.Unmarshal([]byte(payload), &req); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if req.TaskID != "t1" {
		t.Errorf("TaskID = %q, want t1", req.TaskID)
	}
	if req.StorageHash != "0xhash" {
		t.Errorf("StorageHash = %q, want 0xhash", req.StorageHash)
	}
	if req.ProviderEncKey != "0xkey" {
		t.Errorf("ProviderEncKey = %q, want 0xkey", req.ProviderEncKey)
	}
}

func TestAdapterKeyRequest_ExtraFieldsIgnored(t *testing.T) {
	payload := `{"taskId":"t1","storageHash":"0xhash","providerEncKey":"0xkey","unknown":"val"}`
	var req adapterKeyRequest
	if err := json.Unmarshal([]byte(payload), &req); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if req.TaskID != "t1" {
		t.Errorf("TaskID = %q", req.TaskID)
	}
}
