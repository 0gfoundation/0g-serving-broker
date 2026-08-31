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
	assertErrorCode(t, w.Body.Bytes(), adapterKeyErrInvalidPayload)
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
	assertErrorCode(t, w.Body.Bytes(), adapterKeyErrInvalidPayload)
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
	assertErrorCode(t, w.Body.Bytes(), adapterKeyErrInvalidPayload)
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
	assertErrorCode(t, w.Body.Bytes(), adapterKeyErrInvalidPayload)
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
	assertErrorCode(t, w.Body.Bytes(), adapterKeyErrInvalidPayload)
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
	assertErrorCode(t, w.Body.Bytes(), adapterKeyErrInvalidPayload)
}

func TestReceiveAdapterKey_InvalidStorageHashFormat(t *testing.T) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)

	body, _ := json.Marshal(map[string]string{
		"taskId":           "task-001",
		"storageHash":      "not-a-valid-hash",
		"providerEncKey":   "0xabcdef",
		"teeSignerAddress": "0x71562b71999873DB5b286dF957af199Ec94617F7",
	})
	c.Request = httptest.NewRequest("POST", "/internal/v1/adapter-keys", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	h := &Handler{}
	h.ReceiveAdapterKey(c)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d for invalid storageHash format", w.Code, http.StatusBadRequest)
	}
	assertErrorCode(t, w.Body.Bytes(), adapterKeyErrInvalidHashSize)
}

func TestReceiveAdapterKey_StorageHashTooShort(t *testing.T) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)

	body, _ := json.Marshal(map[string]string{
		"taskId":           "task-001",
		"storageHash":      "0xabcdef",
		"providerEncKey":   "0xabcdef",
		"teeSignerAddress": "0x71562b71999873DB5b286dF957af199Ec94617F7",
	})
	c.Request = httptest.NewRequest("POST", "/internal/v1/adapter-keys", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	h := &Handler{}
	h.ReceiveAdapterKey(c)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d for short storageHash", w.Code, http.StatusBadRequest)
	}
	assertErrorCode(t, w.Body.Bytes(), adapterKeyErrInvalidHashSize)
}

func TestReceiveAdapterKey_InvalidStorageHashHex(t *testing.T) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)

	body, _ := json.Marshal(map[string]string{
		"taskId":           "task-001",
		"storageHash":      "0xZZZZ012345670123456789abcdef0123456789abcdef0123456789abcdef0123",
		"providerEncKey":   "0xabcdef",
		"teeSignerAddress": "0x71562b71999873DB5b286dF957af199Ec94617F7",
	})
	c.Request = httptest.NewRequest("POST", "/internal/v1/adapter-keys", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	h := &Handler{}
	h.ReceiveAdapterKey(c)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
	assertErrorCode(t, w.Body.Bytes(), adapterKeyErrInvalidHashHex)
}

func TestReceiveAdapterKey_InvalidProviderEncKeyHex(t *testing.T) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)

	body, _ := json.Marshal(map[string]string{
		"taskId":           "task-001",
		"storageHash":      "0xabcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789",
		"providerEncKey":   "not-hex-data!@#$",
		"teeSignerAddress": "0x71562b71999873DB5b286dF957af199Ec94617F7",
	})
	c.Request = httptest.NewRequest("POST", "/internal/v1/adapter-keys", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	h := &Handler{}
	h.ReceiveAdapterKey(c)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d for invalid providerEncKey hex", w.Code, http.StatusBadRequest)
	}
	assertErrorCode(t, w.Body.Bytes(), adapterKeyErrInvalidEncKey)
}

func TestTruncateHash(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"", ""},
		{"0x", "0x"},
		{"0xabcdef", "0xabcdef"},
		{"0xabcdef0123456789abcdef", "0xabcdef01…"},
		{"0xabcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789", "0xabcdef01…"},
	}
	for _, tc := range cases {
		if got := truncateHash(tc.in); got != tc.want {
			t.Errorf("truncateHash(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// assertErrorCode parses a JSON error response body and verifies the `code`
// field equals the expected value. The fine-tuning broker (and SDK getLog
// surface) relies on this contract — see ReceiveAdapterKey godoc for details.
func assertErrorCode(t *testing.T, body []byte, want string) {
	t.Helper()
	var got struct {
		Error string `json:"error"`
		Code  string `json:"code"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("unmarshal error body %q: %v", body, err)
	}
	if got.Code != want {
		t.Errorf("error code = %q, want %q (body=%s)", got.Code, want, body)
	}
}

// teeSignerAddress gates TEE tag-signature verification on the consumption path
// (util.AesDecryptLargeFile), so the push must carry it and it must be a
// well-formed address. Without it the signature the fine-tuning broker writes
// into every artifact cannot be checked and the adapter must not be deployed.

// An older fine-tuning broker does not send teeSignerAddress. The push must still
// succeed: rejecting it burns pushAdapterKey's retries and makes Finalizer.Execute
// return before AddDeliverable, so training that was already paid for never reaches
// the contract. The adapter instead fails closed later, in lora.Manager.
func TestReceiveAdapterKey_OmittedTeeSignerAddressIsAccepted(t *testing.T) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)

	body, _ := json.Marshal(map[string]string{
		"taskId":         "task-001",
		"storageHash":    "0xabcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789",
		"providerEncKey": "0xabcdef",
	})
	c.Request = httptest.NewRequest("POST", "/internal/v1/adapter-keys", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	// Handler.ctrl is a concrete *ctrl.Ctrl, so this unit test cannot supply one and
	// a payload that PASSES validation necessarily panics on the nil controller at
	// the persistence step. That panic is therefore the evidence we want, and it is
	// asserted rather than swallowed: a deferred bare recover() would run only at
	// the end of the test function, after which the checks below never execute and
	// the test passes vacuously.
	h := &Handler{}
	reachedPersistence := func() (reached bool) {
		defer func() { reached = recover() != nil }()
		h.ReceiveAdapterKey(c)
		return false
	}()

	if w.Code == http.StatusBadRequest {
		t.Fatalf("an omitted teeSignerAddress was rejected: %s", w.Body.String())
	}
	if !reachedPersistence {
		t.Errorf("expected the request to pass validation and reach the controller; status = %d, body = %s",
			w.Code, w.Body.String())
	}
}

func TestReceiveAdapterKey_MalformedTeeSignerAddress(t *testing.T) {
	for _, bad := range []string{
		"not-an-address",
		"0x71562b71999873DB5b286dF957af199Ec94617",     // too short
		"0x71562b71999873DB5b286dF957af199Ec94617F7ff", // too long
		"0xZZ562b71999873DB5b286dF957af199Ec94617F7",   // non-hex
		// The all-zero address passes common.IsHexAddress but is not a signer any
		// enclave produces. Rejected here so it cannot act as a
		// skip-verification sentinel, and so the operator is told at the cheap
		// boundary instead of after a full download and decrypt.
		"0x0000000000000000000000000000000000000000",
		// A bare 40-hex string with no 0x prefix is deliberately NOT rejected:
		// common.IsHexAddress accepts it and it parses to the same address
		// unambiguously, so rejecting it would be gratuitous strictness. Our own
		// sender always emits Address.Hex(), which is prefixed.
	} {
		t.Run(bad, func(t *testing.T) {
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)

			body, _ := json.Marshal(map[string]string{
				"taskId":           "task-001",
				"storageHash":      "0xabcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789",
				"providerEncKey":   "0xabcdef",
				"teeSignerAddress": bad,
			})
			c.Request = httptest.NewRequest("POST", "/internal/v1/adapter-keys", bytes.NewReader(body))
			c.Request.Header.Set("Content-Type", "application/json")

			h := &Handler{}
			h.ReceiveAdapterKey(c)

			if w.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want %d; body = %s", w.Code, http.StatusBadRequest, w.Body.String())
			}
			assertErrorCode(t, w.Body.Bytes(), adapterKeyErrInvalidSigner)
		})
	}
}
