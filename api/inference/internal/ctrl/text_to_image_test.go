package ctrl

import (
	"encoding/base64"
	"encoding/json"
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
// rewriteMultipartResponseFormat
// ==========================================================================

func TestRewriteMultipartResponseFormat_RewritesURLToB64(t *testing.T) {
	body := []byte(
		"--b\r\nContent-Disposition: form-data; name=\"prompt\"\r\n\r\nhello\r\n" +
			"--b\r\nContent-Disposition: form-data; name=\"response_format\"\r\n\r\nurl\r\n" +
			"--b--\r\n",
	)

	orig, modified, err := rewriteMultipartResponseFormat(body)
	if err != nil {
		t.Fatalf("rewriteMultipartResponseFormat: %v", err)
	}
	if orig != "url" {
		t.Errorf("original format = %q, want url", orig)
	}
	if !strings.Contains(string(modified), "name=\"response_format\"\r\n\r\nb64_json\r\n") {
		t.Errorf("modified body missing rewritten response_format:\n%s", modified)
	}
	if strings.Contains(string(modified), "name=\"response_format\"\r\n\r\nurl\r\n") {
		t.Error("modified body still contains url value")
	}
	// prompt part must be intact.
	if !strings.Contains(string(modified), "name=\"prompt\"\r\n\r\nhello\r\n") {
		t.Error("modified body should preserve the prompt part")
	}
}

func TestRewriteMultipartResponseFormat_FieldAbsent_NoOp(t *testing.T) {
	body := []byte(
		"--b\r\nContent-Disposition: form-data; name=\"prompt\"\r\n\r\nhello\r\n" +
			"--b--\r\n",
	)

	orig, modified, err := rewriteMultipartResponseFormat(body)
	if err != nil {
		t.Fatalf("rewriteMultipartResponseFormat: %v", err)
	}
	if orig != "" {
		t.Errorf("original format = %q, want empty", orig)
	}
	if string(modified) != string(body) {
		t.Error("body should be unchanged when response_format is absent")
	}
}

func TestRewriteMultipartResponseFormat_AlreadyB64_NoChange(t *testing.T) {
	body := []byte(
		"--b\r\nContent-Disposition: form-data; name=\"response_format\"\r\n\r\nb64_json\r\n" +
			"--b--\r\n",
	)
	orig, modified, err := rewriteMultipartResponseFormat(body)
	if err != nil {
		t.Fatalf("rewriteMultipartResponseFormat: %v", err)
	}
	if orig != "b64_json" {
		t.Errorf("orig = %q, want b64_json", orig)
	}
	if string(modified) != string(body) {
		t.Error("body should be byte-identical when already b64_json")
	}
}

func TestRewriteMultipartResponseFormat_PreservesBinaryBytes(t *testing.T) {
	binary := []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A, 0x00, 0xFF, 0x00, 0xFF}
	var body []byte
	body = append(body, []byte("--b\r\nContent-Disposition: form-data; name=\"response_format\"\r\n\r\nurl\r\n")...)
	body = append(body, []byte("--b\r\nContent-Disposition: form-data; name=\"image\"; filename=\"x.png\"\r\nContent-Type: image/png\r\n\r\n")...)
	body = append(body, binary...)
	body = append(body, []byte("\r\n--b--\r\n")...)

	_, modified, err := rewriteMultipartResponseFormat(body)
	if err != nil {
		t.Fatalf("rewriteMultipartResponseFormat: %v", err)
	}
	// Binary image bytes must appear unchanged in the modified body.
	if !bytesContains(modified, binary) {
		t.Error("binary image bytes were corrupted by the rewrite")
	}
}

func bytesContains(haystack, needle []byte) bool {
	if len(needle) == 0 {
		return true
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		match := true
		for j := 0; j < len(needle); j++ {
			if haystack[i+j] != needle[j] {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

// ==========================================================================
// buildURLResponse
// ==========================================================================

func TestBuildURLResponse_PreservesMetadataFields(t *testing.T) {
	b64 := base64.StdEncoding.EncodeToString([]byte("pixels"))
	envelope := imageResponseEnvelope{
		Created: 999,
		Data: []imageResponseData{
			{B64JSON: b64, RevisedPrompt: "a fluffy cat"},
			{B64JSON: b64},
		},
	}
	body, _ := json.Marshal(envelope)

	out, err := buildURLResponse(body, "chat-uuid-abc123", 2, "https://broker.example.com")
	if err != nil {
		t.Fatalf("buildURLResponse: %v", err)
	}

	var result imageResponseEnvelope
	if err := json.Unmarshal(out, &result); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	for i, d := range result.Data {
		if d.B64JSON != "" {
			t.Errorf("data[%d].b64_json should be empty after URL rewrite, got %q", i, d.B64JSON)
		}
	}
	if result.Data[0].RevisedPrompt != "a fluffy cat" {
		t.Errorf("revised_prompt should be preserved, got %q", result.Data[0].RevisedPrompt)
	}
	if result.Created != 999 {
		t.Errorf("created should be preserved, got %d", result.Created)
	}
}

func TestBuildURLResponse_InvalidJSON(t *testing.T) {
	if _, err := buildURLResponse([]byte("{bad}"), "key", 1, "https://broker.example.com"); err == nil {
		t.Error("expected error for invalid JSON, got nil")
	}
}

// TestBuildURLResponse_UsesServingURL pins the scheme/host/path source:
// broker-served image URLs are derived from service.servingUrl (the public URL
// the provider registered on-chain), not from the incoming request. This is
// what makes the rewrite correct behind a TLS-terminating ingress that doesn't
// forward X-Forwarded-Proto.
func TestBuildURLResponse_UsesServingURL(t *testing.T) {
	body, _ := json.Marshal(imageResponseEnvelope{
		Data: []imageResponseData{{B64JSON: base64.StdEncoding.EncodeToString([]byte("px"))}},
	})

	tests := []struct {
		name       string
		servingURL string
		want       string
	}{
		{
			name:       "https public URL",
			servingURL: "https://compute-network-dev-99.integratenetwork.work",
			want:       "https://compute-network-dev-99.integratenetwork.work/v1/proxy/images/k/0",
		},
		{
			name:       "http local URL with port",
			servingURL: "http://localhost:3080",
			want:       "http://localhost:3080/v1/proxy/images/k/0",
		},
		{
			name:       "trailing slash is normalised (no double /)",
			servingURL: "https://broker.example.com/",
			want:       "https://broker.example.com/v1/proxy/images/k/0",
		},
		{
			name:       "path prefix is preserved",
			servingURL: "https://edge.example.com/provider-42",
			want:       "https://edge.example.com/provider-42/v1/proxy/images/k/0",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := buildURLResponse(body, "k", 1, tt.servingURL)
			if err != nil {
				t.Fatalf("buildURLResponse: %v", err)
			}
			var result imageResponseEnvelope
			if err := json.Unmarshal(out, &result); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if result.Data[0].URL != tt.want {
				t.Errorf("URL = %q, want %q", result.Data[0].URL, tt.want)
			}
		})
	}
}

// TestBuildURLResponse_RejectsInvalidServingURL ensures the caller falls back
// to b64 (its only error path) when servingUrl is missing or malformed, rather
// than emitting a broken URL.
func TestBuildURLResponse_RejectsInvalidServingURL(t *testing.T) {
	body, _ := json.Marshal(imageResponseEnvelope{
		Data: []imageResponseData{{B64JSON: base64.StdEncoding.EncodeToString([]byte("px"))}},
	})

	for _, servingURL := range []string{"", "not-a-url", "//no-scheme.example.com", "https://"} {
		t.Run("servingURL="+servingURL, func(t *testing.T) {
			if _, err := buildURLResponse(body, "k", 1, servingURL); err == nil {
				t.Errorf("expected error for servingUrl %q, got nil", servingURL)
			}
		})
	}
}
