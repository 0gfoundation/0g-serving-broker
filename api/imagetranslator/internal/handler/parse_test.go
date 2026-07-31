package handler

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"
)

// newMultipartRequest builds a POST request with the given plain form
// fields, ready for parseCreateImageRequest's multipart branch.
func newMultipartRequest(t *testing.T, fields map[string]string) *http.Request {
	t.Helper()
	body := &bytes.Buffer{}
	w := multipart.NewWriter(body)
	for k, v := range fields {
		if err := w.WriteField(k, v); err != nil {
			t.Fatalf("write field %s: %v", k, err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/async/images/generations", body)
	req.Header.Set("Content-Type", w.FormDataContentType())
	return req
}

// TestParseCreateImageRequest_InvalidMultipartNIsRejected pins the fix for a
// real bug: a non-numeric "n" form field must be a 400 (parseCreateImageRequest
// returns an error the CreateImage handler maps to 400), not silently default
// to n=1 — a typo'd count must never be silently substituted the way
// translate.validateKlingCount's own doc comment explicitly argues against
// (never hand the client fewer/different images than asked with no error).
func TestParseCreateImageRequest_InvalidMultipartNIsRejected(t *testing.T) {
	req := newMultipartRequest(t, map[string]string{
		"model":  "kling/kling-v3-image-generation",
		"prompt": "a cat",
		"n":      "not-a-number",
	})
	if _, err := parseCreateImageRequest(req); err == nil {
		t.Fatal("expected an error for a non-numeric n, got nil (silently defaulted)")
	}
}

// TestParseCreateImageRequest_InvalidMultipartWatermarkIsRejected mirrors the
// n fix for the watermark field.
func TestParseCreateImageRequest_InvalidMultipartWatermarkIsRejected(t *testing.T) {
	req := newMultipartRequest(t, map[string]string{
		"model":     "kling/kling-v3-image-generation",
		"prompt":    "a cat",
		"watermark": "not-a-bool",
	})
	if _, err := parseCreateImageRequest(req); err == nil {
		t.Fatal("expected an error for a non-boolean watermark, got nil (silently defaulted)")
	}
}

// TestParseCreateImageRequest_ValidMultipartFieldsParse confirms the fix
// didn't break the valid-input path.
func TestParseCreateImageRequest_ValidMultipartFieldsParse(t *testing.T) {
	req := newMultipartRequest(t, map[string]string{
		"model":     "kling/kling-v3-image-generation",
		"prompt":    "a cat",
		"n":         "3",
		"watermark": "true",
	})
	got, err := parseCreateImageRequest(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.N != 3 || !got.Watermark {
		t.Errorf("got N=%d Watermark=%v, want N=3 Watermark=true", got.N, got.Watermark)
	}
}
