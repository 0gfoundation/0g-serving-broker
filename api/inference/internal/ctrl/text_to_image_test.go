package ctrl

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
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

	images, err := extractB64Images(body, 2)
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
	images, err := extractB64Images(body, 1)
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
	if _, err := extractB64Images(body, 1); err == nil {
		t.Error("expected error for URL-only response, got nil")
	}
}

func TestExtractB64Images_EmptyData(t *testing.T) {
	if _, err := extractB64Images([]byte(`{"created":1,"data":[]}`), 1); err == nil {
		t.Error("expected error for empty data array")
	}
}

func TestExtractB64Images_BadBase64(t *testing.T) {
	body := []byte(`{"created":1,"data":[{"b64_json":"!!!not-valid-base64!!!"}]}`)
	if _, err := extractB64Images(body, 1); err == nil {
		t.Error("expected error for invalid base64")
	}
}

func TestExtractB64Images_InvalidJSON(t *testing.T) {
	if _, err := extractB64Images([]byte("{not json}"), 1); err == nil {
		t.Error("expected error for invalid JSON")
	}
}

// TestExtractB64Images_RejectsMoreThanRequested pins the provider-OOM guard:
// a compromised provider returning 50 images when the client asked for 1 is
// a billing bug AND a memory risk (all 50 b64 blobs would be decoded). The
// extractor must refuse before decoding anything past the cap.
func TestExtractB64Images_RejectsMoreThanRequested(t *testing.T) {
	img := base64.StdEncoding.EncodeToString([]byte("px"))
	many := make([]imageResponseData, 50)
	for i := range many {
		many[i] = imageResponseData{B64JSON: img}
	}
	body, _ := json.Marshal(imageResponseEnvelope{Data: many})

	// Client asked for 1 (maxImages=1); provider returned 50 → error.
	if _, err := extractB64Images(body, 1); err == nil {
		t.Error("expected error when envelope exceeds maxImages, got nil")
	}
	// Same body with a permissive cap (test-only usage) decodes fine.
	if _, err := extractB64Images(body, 100); err != nil {
		t.Errorf("permissive cap must not reject: %v", err)
	}
	// maxImages <= 0 disables the cap (tests only).
	if _, err := extractB64Images(body, 0); err != nil {
		t.Errorf("maxImages=0 should disable the cap, got: %v", err)
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
	// Use a key-ordering the Go map remarshaller would definitely change
	// (`z_last` after `a_first`, with response_format deliberately in the
	// middle) so if the fast path regresses we'd see a different byte order.
	body := []byte(`{"a_first":1,"response_format":"b64_json","z_last":"tail"}`)
	orig, modified, err := forceB64ResponseFormat(body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if orig != "b64_json" {
		t.Errorf("orig = %q, want b64_json", orig)
	}
	// Fast path must return the input bytes verbatim (no remarshal).
	if string(modified) != string(body) {
		t.Errorf("fast path must return body byte-for-byte.\n got:  %s\nwant: %s", modified, body)
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

// TestForceB64ResponseFormat_PreservesLargeIntegers pins the UseNumber fix: a
// seed > 2^53 would be silently coerced to float64 and emitted in exponential
// notation if we decoded without json.Number. The signed body is pre-rewrite
// so this doesn't affect TEE proofs today, but a provider that echoes numeric
// parameters would see corrupted seeds.
func TestForceB64ResponseFormat_PreservesLargeIntegers(t *testing.T) {
	// 2^53 + 1 is the smallest positive integer that float64 cannot represent exactly.
	const bigSeed = `9007199254740993`
	body := []byte(`{"prompt":"cat","seed":` + bigSeed + `,"response_format":"url"}`)

	_, modified, err := forceB64ResponseFormat(body)
	if err != nil {
		t.Fatalf("forceB64ResponseFormat: %v", err)
	}
	if !strings.Contains(string(modified), bigSeed) {
		t.Errorf("large integer seed not preserved; got body: %s", modified)
	}
	if strings.Contains(string(modified), "9.007") || strings.Contains(string(modified), "e+") {
		t.Errorf("seed was coerced to float64 exponential form: %s", modified)
	}
}

// ==========================================================================
// rewriteMultipartResponseFormat
// ==========================================================================

const multipartCT = `multipart/form-data; boundary=b`

func TestRewriteMultipartResponseFormat_RewritesURLToB64(t *testing.T) {
	body := []byte(
		"--b\r\nContent-Disposition: form-data; name=\"prompt\"\r\n\r\nhello\r\n" +
			"--b\r\nContent-Disposition: form-data; name=\"response_format\"\r\n\r\nurl\r\n" +
			"--b--\r\n",
	)

	orig, modified, err := rewriteMultipartResponseFormat(body, multipartCT)
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

// TestRewriteMultipartResponseFormat_FieldAbsent_InjectsB64 pins the absent-
// field leak fix and the broker's chosen default: when the client omits the
// field, the broker MUST force b64_json upstream (otherwise the provider
// returns LAN-private URLs), AND it reports originalFormat="" so the handler
// passes the b64 body through. Diverges from OpenAI's per-endpoint default
// (which is "url" for /v1/images/edits), by design — matches the JSON path's
// behaviour so clients see consistent defaults regardless of transport.
func TestRewriteMultipartResponseFormat_FieldAbsent_InjectsB64(t *testing.T) {
	body := []byte(
		"--b\r\nContent-Disposition: form-data; name=\"prompt\"\r\n\r\nhello\r\n" +
			"--b--\r\n",
	)

	orig, modified, err := rewriteMultipartResponseFormat(body, multipartCT)
	if err != nil {
		t.Fatalf("rewriteMultipartResponseFormat: %v", err)
	}
	if orig != "" {
		t.Errorf("original format = %q, want \"\" (broker default is b64 across JSON + multipart)", orig)
	}
	if !strings.Contains(string(modified), "name=\"response_format\"\r\n\r\nb64_json") {
		t.Errorf("injected response_format part missing:\n%s", modified)
	}
	// Prompt part must still be present.
	if !strings.Contains(string(modified), "name=\"prompt\"\r\n\r\nhello") {
		t.Error("modified body should preserve the prompt part")
	}
	// Closing boundary must still terminate the body.
	if !strings.HasSuffix(string(modified), "--b--\r\n") {
		t.Errorf("modified body must end with closing boundary, got suffix: %q", string(modified)[len(modified)-20:])
	}
	// The new part must be placed BEFORE the closing delimiter (not after).
	closeIdx := strings.Index(string(modified), "\r\n--b--\r\n")
	injectIdx := strings.Index(string(modified), "name=\"response_format\"")
	if injectIdx < 0 || injectIdx >= closeIdx {
		t.Errorf("injected part must sit before closing boundary: injectIdx=%d closeIdx=%d", injectIdx, closeIdx)
	}
}

func TestRewriteMultipartResponseFormat_FieldAbsent_RejectsMissingBoundary(t *testing.T) {
	body := []byte("--b\r\nContent-Disposition: form-data; name=\"prompt\"\r\n\r\nhi\r\n--b--\r\n")
	if _, _, err := rewriteMultipartResponseFormat(body, "multipart/form-data"); err == nil {
		t.Error("expected error when content-type has no boundary")
	}
}

func TestRewriteMultipartResponseFormat_AlreadyB64_NoChange(t *testing.T) {
	body := []byte(
		"--b\r\nContent-Disposition: form-data; name=\"response_format\"\r\n\r\nb64_json\r\n" +
			"--b--\r\n",
	)
	orig, modified, err := rewriteMultipartResponseFormat(body, multipartCT)
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

// TestRewriteMultipartResponseFormat_AdversarialFilePayload pins the fix to
// the byte-scanner vulnerability: a file part whose body contains the literal
// string `name="response_format"` plus a fake boundary terminator used to
// anchor the old bytes.Index scanner on the wrong location, corrupting the
// uploaded file bytes. The mime/multipart.Reader respects boundaries, so the
// adversarial substring inside a file part is ignored entirely.
func TestRewriteMultipartResponseFormat_AdversarialFilePayload(t *testing.T) {
	// File body deliberately contains the marker the old byte scanner searched
	// for AND a \r\n-- sequence it used as the value-end anchor. The suffix
	// "--XYZ" is NOT the real boundary "b", so a real multipart reader keeps
	// these bytes inside the file part; the old byte scanner would treat them
	// as a form-field terminator and splice "adversarial" as the value.
	adversarial := []byte(
		"PNG-HEADER" +
			"name=\"response_format\"\r\n\r\nadversarial\r\n--XYZ " +
			"more-bytes-after",
	)
	// File FIRST so its adversarial bytes come before the legitimate
	// response_format field. The old byte scanner did bytes.Index over the
	// whole body and would match the name="response_format" substring INSIDE
	// this file part, then splice "adversarial" as the value — corrupting
	// the file on readback.
	var body []byte
	body = append(body, []byte("--b\r\nContent-Disposition: form-data; name=\"image\"; filename=\"x.bin\"\r\nContent-Type: application/octet-stream\r\n\r\n")...)
	body = append(body, adversarial...)
	body = append(body, []byte("\r\n--b\r\nContent-Disposition: form-data; name=\"prompt\"\r\n\r\nhello\r\n")...)
	body = append(body, []byte("--b\r\nContent-Disposition: form-data; name=\"response_format\"\r\n\r\nurl\r\n")...)
	body = append(body, []byte("--b--\r\n")...)

	orig, modified, err := rewriteMultipartResponseFormat(body, multipartCT)
	if err != nil {
		t.Fatalf("rewriteMultipartResponseFormat: %v", err)
	}
	if orig != "url" {
		t.Errorf("original format = %q, want url", orig)
	}

	// Decode the output and confirm: the response_format part was rewritten to
	// b64_json, AND the adversarial file bytes survived verbatim.
	_, params, _ := mime.ParseMediaType(multipartCT)
	reader := multipart.NewReader(bytes.NewReader(modified), params["boundary"])
	seenResponseFormat := false
	seenImageVerbatim := false
	for {
		part, err := reader.NextPart()
		if err != nil {
			break
		}
		partBody, _ := io.ReadAll(part)
		switch part.FormName() {
		case "response_format":
			seenResponseFormat = true
			if string(partBody) != "b64_json" {
				t.Errorf("response_format part = %q, want b64_json", partBody)
			}
		case "image":
			seenImageVerbatim = bytes.Equal(partBody, adversarial)
		}
	}
	if !seenResponseFormat {
		t.Error("response_format part missing from rewritten body")
	}
	if !seenImageVerbatim {
		t.Error("adversarial file bytes were corrupted — the byte scanner bug regressed")
	}
}

func TestRewriteMultipartResponseFormat_PreservesBinaryBytes(t *testing.T) {
	binary := []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A, 0x00, 0xFF, 0x00, 0xFF}
	var body []byte
	body = append(body, []byte("--b\r\nContent-Disposition: form-data; name=\"response_format\"\r\n\r\nurl\r\n")...)
	body = append(body, []byte("--b\r\nContent-Disposition: form-data; name=\"image\"; filename=\"x.png\"\r\nContent-Type: image/png\r\n\r\n")...)
	body = append(body, binary...)
	body = append(body, []byte("\r\n--b--\r\n")...)

	_, modified, err := rewriteMultipartResponseFormat(body, multipartCT)
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

// TestBuildURLResponse_RejectsEnvelopeCountMismatch pins the guard against
// emitting a mixed b64/url envelope: if the provider's data array has more
// (or fewer) entries than the images we actually stored, the function MUST
// refuse rather than leave tail entries in b64 form. Caller downgrades on
// error, which is the safer failure mode.
func TestBuildURLResponse_RejectsEnvelopeCountMismatch(t *testing.T) {
	b64 := base64.StdEncoding.EncodeToString([]byte("px"))
	// Envelope advertises THREE images but the caller only stored TWO.
	body, _ := json.Marshal(imageResponseEnvelope{
		Data: []imageResponseData{
			{B64JSON: b64},
			{B64JSON: b64},
			{B64JSON: b64},
		},
	})
	if _, err := buildURLResponse(body, "k", 2, "https://broker.example.com"); err == nil {
		t.Error("expected error when envelope length != stored image count, got nil")
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
