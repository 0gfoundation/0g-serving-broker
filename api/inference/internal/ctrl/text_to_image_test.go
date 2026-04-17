package ctrl

import (
	"encoding/base64"
	"encoding/json"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// ==========================================================================
// extractB64Images
// ==========================================================================

func TestExtractB64Images_HappyPath(t *testing.T) {
	img0 := []byte("fake-png-bytes-zero")
	img1 := []byte("fake-png-bytes-one")
	envelope := imageResponseEnvelope{
		Created: 1000,
		Data: []imageResponseData{
			{B64JSON: base64.StdEncoding.EncodeToString(img0)},
			{B64JSON: base64.StdEncoding.EncodeToString(img1)},
		},
	}
	body, _ := json.Marshal(envelope)

	images, err := extractB64Images(body)
	if err != nil {
		t.Fatalf("extractB64Images: %v", err)
	}
	if len(images) != 2 {
		t.Fatalf("len(images) = %d, want 2", len(images))
	}
	if string(images[0]) != string(img0) {
		t.Errorf("image[0] = %q, want %q", images[0], img0)
	}
	if string(images[1]) != string(img1) {
		t.Errorf("image[1] = %q, want %q", images[1], img1)
	}
}

func TestExtractB64Images_SingleImage(t *testing.T) {
	img := []byte("single-image-data")
	body, _ := json.Marshal(imageResponseEnvelope{
		Data: []imageResponseData{{B64JSON: base64.StdEncoding.EncodeToString(img)}},
	})
	images, err := extractB64Images(body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(images) != 1 {
		t.Fatalf("len(images) = %d, want 1", len(images))
	}
}

func TestExtractB64Images_URLOnly_ReturnsError(t *testing.T) {
	body, _ := json.Marshal(imageResponseEnvelope{
		Data: []imageResponseData{{URL: "http://provider/img.png"}},
	})
	if _, err := extractB64Images(body); err == nil {
		t.Error("expected error for URL-only response, got nil")
	}
}

func TestExtractB64Images_EmptyData(t *testing.T) {
	if _, err := extractB64Images([]byte(`{"created":1,"data":[]}`)); err == nil {
		t.Error("expected error for empty data array")
	}
}

func TestExtractB64Images_BadBase64(t *testing.T) {
	body := []byte(`{"created":1,"data":[{"b64_json":"!!!not-valid-base64!!!"}]}`)
	if _, err := extractB64Images(body); err == nil {
		t.Error("expected error for invalid base64")
	}
}

func TestExtractB64Images_InvalidJSON(t *testing.T) {
	if _, err := extractB64Images([]byte("{not json}")); err == nil {
		t.Error("expected error for invalid JSON")
	}
}

// ==========================================================================
// forceB64ResponseFormat
// ==========================================================================

func TestForceB64ResponseFormat_RewritesURLToB64(t *testing.T) {
	body := []byte(`{"prompt":"a cat","response_format":"url","n":2}`)

	orig, modified, err := forceB64ResponseFormat(body)
	if err != nil {
		t.Fatalf("forceB64ResponseFormat: %v", err)
	}
	if orig != "url" {
		t.Errorf("original format = %q, want url", orig)
	}

	var m map[string]interface{}
	if err := json.Unmarshal(modified, &m); err != nil {
		t.Fatalf("unmarshal modified body: %v", err)
	}
	if m["response_format"] != "b64_json" {
		t.Errorf("response_format = %v, want b64_json", m["response_format"])
	}
	// Other fields must be preserved.
	if m["prompt"] != "a cat" {
		t.Errorf("prompt field should be preserved, got %v", m["prompt"])
	}
}

func TestForceB64ResponseFormat_AddsFieldWhenAbsent(t *testing.T) {
	body := []byte(`{"prompt":"a dog","n":1}`)

	orig, modified, err := forceB64ResponseFormat(body)
	if err != nil {
		t.Fatalf("forceB64ResponseFormat: %v", err)
	}
	if orig != "" {
		t.Errorf("original format = %q, want empty", orig)
	}

	var m map[string]interface{}
	json.Unmarshal(modified, &m)
	if m["response_format"] != "b64_json" {
		t.Errorf("response_format should be added as b64_json, got %v", m["response_format"])
	}
}

func TestForceB64ResponseFormat_AlreadyB64_NoChange(t *testing.T) {
	body := []byte(`{"prompt":"x","response_format":"b64_json"}`)
	orig, modified, err := forceB64ResponseFormat(body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if orig != "b64_json" {
		t.Errorf("orig = %q, want b64_json", orig)
	}
	var m map[string]interface{}
	json.Unmarshal(modified, &m)
	if m["response_format"] != "b64_json" {
		t.Errorf("response_format = %v, want b64_json", m["response_format"])
	}
}

func TestForceB64ResponseFormat_NonJSON_ReturnsError(t *testing.T) {
	multipart := []byte("--boundary\r\nContent-Disposition: form-data; name=\"prompt\"\r\n\r\nhello\r\n--boundary--")
	_, got, err := forceB64ResponseFormat(multipart)
	if err == nil {
		t.Error("expected error for non-JSON body, got nil")
	}
	// Original body returned unchanged on error.
	if string(got) != string(multipart) {
		t.Errorf("expected original multipart body returned on error")
	}
}

// ==========================================================================
// buildURLResponse
// ==========================================================================

func TestBuildURLResponse_URLsContainChatKeyAndIndex(t *testing.T) {
	img := []byte("pixels")
	b64 := base64.StdEncoding.EncodeToString(img)
	envelope := imageResponseEnvelope{
		Created: 999,
		Data: []imageResponseData{
			{B64JSON: b64, RevisedPrompt: "a fluffy cat"},
			{B64JSON: b64},
		},
	}
	body, _ := json.Marshal(envelope)
	chatKey := "chat-uuid-abc123"
	req := httptest.NewRequest("POST", "/v1/proxy/images/generations", nil)
	req.Host = "broker.example.com"

	out, err := buildURLResponse(body, chatKey, 2, req)
	if err != nil {
		t.Fatalf("buildURLResponse: %v", err)
	}

	var result imageResponseEnvelope
	if err := json.Unmarshal(out, &result); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if len(result.Data) != 2 {
		t.Fatalf("len(data) = %d, want 2", len(result.Data))
	}
	for i, d := range result.Data {
		if d.B64JSON != "" {
			t.Errorf("data[%d].b64_json should be empty after URL rewrite, got %q", i, d.B64JSON)
		}
		if !strings.Contains(d.URL, chatKey) {
			t.Errorf("data[%d].url %q should contain chatKey %q", i, d.URL, chatKey)
		}
		if !strings.HasSuffix(d.URL, "/"+strconv.Itoa(i)) {
			t.Errorf("data[%d].url %q should end with index /%d", i, d.URL, i)
		}
	}
	if result.Data[0].RevisedPrompt != "a fluffy cat" {
		t.Errorf("revised_prompt should be preserved, got %q", result.Data[0].RevisedPrompt)
	}
	if result.Created != 999 {
		t.Errorf("created should be preserved, got %d", result.Created)
	}
}

func TestBuildURLResponse_HTTPScheme(t *testing.T) {
	img := base64.StdEncoding.EncodeToString([]byte("px"))
	body, _ := json.Marshal(imageResponseEnvelope{
		Data: []imageResponseData{{B64JSON: img}},
	})
	req := httptest.NewRequest("POST", "/v1/proxy/images/generations", nil)
	req.Host = "localhost:8080"
	// req.TLS is nil → expect http://

	out, err := buildURLResponse(body, "key", 1, req)
	if err != nil {
		t.Fatalf("buildURLResponse: %v", err)
	}
	var result imageResponseEnvelope
	json.Unmarshal(out, &result)
	if !strings.HasPrefix(result.Data[0].URL, "http://") {
		t.Errorf("expected http:// scheme, got %q", result.Data[0].URL)
	}
}

func TestBuildURLResponse_InvalidJSON(t *testing.T) {
	if _, err := buildURLResponse([]byte("{bad}"), "key", 1, httptest.NewRequest("POST", "/", nil)); err == nil {
		t.Error("expected error for invalid JSON, got nil")
	}
}
