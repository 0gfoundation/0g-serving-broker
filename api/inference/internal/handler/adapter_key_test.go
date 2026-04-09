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
	hash := "0x" + "ab" + "cd" + "ef" + "01" + "23" + "45" + "67" + "89" + "ab" + "cd" + "ef" + "01" + "23" + "45" + "67" + "89" + "ab" + "cd" + "ef" + "01" + "23" + "45" + "67" + "89" + "ab" + "cd" + "ef" + "01" + "23" + "45" + "67"
	payload := `{"taskId":"t1","storageHash":"` + hash + `","providerEncKey":"0xabcdef"}`
	var req adapterKeyRequest
	if err := json.Unmarshal([]byte(payload), &req); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if req.TaskID != "t1" {
		t.Errorf("TaskID = %q, want t1", req.TaskID)
	}
	if req.StorageHash != hash {
		t.Errorf("StorageHash = %q, want %s", req.StorageHash, hash)
	}
	if req.ProviderEncKey != "0xabcdef" {
		t.Errorf("ProviderEncKey = %q, want 0xabcdef", req.ProviderEncKey)
	}
}

func TestAdapterKeyRequest_ExtraFieldsIgnored(t *testing.T) {
	hash := "0xabcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"
	payload := `{"taskId":"t1","storageHash":"` + hash + `","providerEncKey":"0xabcdef","unknown":"val"}`
	var req adapterKeyRequest
	if err := json.Unmarshal([]byte(payload), &req); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if req.TaskID != "t1" {
		t.Errorf("TaskID = %q", req.TaskID)
	}
}

func TestReceiveAdapterKey_WhitespaceOnlyFields(t *testing.T) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)

	body, _ := json.Marshal(map[string]string{
		"taskId":         "   ",
		"storageHash":    "0xabcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789",
		"providerEncKey": "0xabcdef",
	})
	c.Request = httptest.NewRequest("POST", "/internal/v1/adapter-keys", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	h := &Handler{}
	h.ReceiveAdapterKey(c)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d for whitespace-only taskId", w.Code, http.StatusBadRequest)
	}
}

func TestReceiveAdapterKey_InvalidStorageHashFormat(t *testing.T) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)

	body, _ := json.Marshal(map[string]string{
		"taskId":         "task-001",
		"storageHash":    "not-a-valid-hash",
		"providerEncKey": "0xabcdef",
	})
	c.Request = httptest.NewRequest("POST", "/internal/v1/adapter-keys", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	h := &Handler{}
	h.ReceiveAdapterKey(c)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d for invalid storageHash format", w.Code, http.StatusBadRequest)
	}
}

func TestReceiveAdapterKey_StorageHashTooShort(t *testing.T) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)

	body, _ := json.Marshal(map[string]string{
		"taskId":         "task-001",
		"storageHash":    "0xabcdef",
		"providerEncKey": "0xabcdef",
	})
	c.Request = httptest.NewRequest("POST", "/internal/v1/adapter-keys", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	h := &Handler{}
	h.ReceiveAdapterKey(c)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d for short storageHash", w.Code, http.StatusBadRequest)
	}
}

func TestReceiveAdapterKey_InvalidProviderEncKeyHex(t *testing.T) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)

	body, _ := json.Marshal(map[string]string{
		"taskId":         "task-001",
		"storageHash":    "0xabcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789",
		"providerEncKey": "not-hex-data!@#$",
	})
	c.Request = httptest.NewRequest("POST", "/internal/v1/adapter-keys", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	h := &Handler{}
	h.ReceiveAdapterKey(c)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d for invalid providerEncKey hex", w.Code, http.StatusBadRequest)
	}
}
