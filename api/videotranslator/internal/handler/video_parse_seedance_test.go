package handler

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestParseCreateVideoRequest_LastFrameReference_JSON(t *testing.T) {
	const body = `{"prompt":"p","input_reference":{"image_url":"https://cdn/a.png"},"last_frame_reference":{"image_url":"https://cdn/b.png"}}`
	req := httptest.NewRequest(http.MethodPost, "/videos", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	got, err := parseCreateVideoRequest(req)
	if err != nil {
		t.Fatalf("parseCreateVideoRequest: %v", err)
	}
	if got.LastFrameReferenceImageURL != "https://cdn/b.png" {
		t.Errorf("LastFrameReferenceImageURL = %q, want https://cdn/b.png", got.LastFrameReferenceImageURL)
	}
}

func TestParseCreateVideoRequest_LastFrameReference_Multipart(t *testing.T) {
	body, contentType := newMultipartBody(t, map[string]string{
		"prompt":               "p",
		"input_reference":      "https://cdn/a.png",
		"last_frame_reference": "https://cdn/b.png",
	})
	req := httptest.NewRequest(http.MethodPost, "/videos", body)
	req.Header.Set("Content-Type", contentType)

	got, err := parseCreateVideoRequest(req)
	if err != nil {
		t.Fatalf("parseCreateVideoRequest: %v", err)
	}
	if got.LastFrameReferenceImageURL != "https://cdn/b.png" {
		t.Errorf("LastFrameReferenceImageURL = %q, want https://cdn/b.png", got.LastFrameReferenceImageURL)
	}
}

func TestParseCreateVideoRequest_LastFrameReference_MultipartFilePart(t *testing.T) {
	buf := &bytes.Buffer{}
	w := multipart.NewWriter(buf)
	if err := w.WriteField("prompt", "p"); err != nil {
		t.Fatal(err)
	}
	fw, err := w.CreateFormFile("last_frame_reference", "frame.png")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fw.Write([]byte("PNGDATA")); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/videos", buf)
	req.Header.Set("Content-Type", w.FormDataContentType())

	got, err := parseCreateVideoRequest(req)
	if err != nil {
		t.Fatalf("parseCreateVideoRequest: %v", err)
	}
	if !strings.HasPrefix(got.LastFrameReferenceImageURL, "data:") {
		t.Errorf("LastFrameReferenceImageURL = %q, want a data: URI for an uploaded file part", got.LastFrameReferenceImageURL)
	}
}

func TestParseCreateVideoRequest_ReferenceArrays_JSON(t *testing.T) {
	const body = `{"prompt":"p","reference_images":["https://cdn/i1.png","https://cdn/i2.png"],"reference_videos":["https://cdn/v1.mp4"],"reference_audio":["https://cdn/a1.mp3"]}`
	req := httptest.NewRequest(http.MethodPost, "/videos", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	got, err := parseCreateVideoRequest(req)
	if err != nil {
		t.Fatalf("parseCreateVideoRequest: %v", err)
	}
	if !reflect.DeepEqual(got.ReferenceImageURLs, []string{"https://cdn/i1.png", "https://cdn/i2.png"}) {
		t.Errorf("ReferenceImageURLs = %v", got.ReferenceImageURLs)
	}
	if !reflect.DeepEqual(got.ReferenceVideoURLs, []string{"https://cdn/v1.mp4"}) {
		t.Errorf("ReferenceVideoURLs = %v", got.ReferenceVideoURLs)
	}
	if !reflect.DeepEqual(got.ReferenceAudioURLs, []string{"https://cdn/a1.mp3"}) {
		t.Errorf("ReferenceAudioURLs = %v", got.ReferenceAudioURLs)
	}
}

func TestParseCreateVideoRequest_ReferenceArrays_MultipartRepeatedFields(t *testing.T) {
	buf := &bytes.Buffer{}
	w := multipart.NewWriter(buf)
	fields := []struct{ k, v string }{
		{"prompt", "p"},
		{"reference_images", "https://cdn/i1.png"},
		{"reference_images", "https://cdn/i2.png"},
		{"reference_videos", "https://cdn/v1.mp4"},
	}
	for _, f := range fields {
		if err := w.WriteField(f.k, f.v); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/videos", buf)
	req.Header.Set("Content-Type", w.FormDataContentType())

	got, err := parseCreateVideoRequest(req)
	if err != nil {
		t.Fatalf("parseCreateVideoRequest: %v", err)
	}
	if !reflect.DeepEqual(got.ReferenceImageURLs, []string{"https://cdn/i1.png", "https://cdn/i2.png"}) {
		t.Errorf("ReferenceImageURLs = %v, want both repeated values", got.ReferenceImageURLs)
	}
	if !reflect.DeepEqual(got.ReferenceVideoURLs, []string{"https://cdn/v1.mp4"}) {
		t.Errorf("ReferenceVideoURLs = %v", got.ReferenceVideoURLs)
	}
	if len(got.ReferenceAudioURLs) != 0 {
		t.Errorf("ReferenceAudioURLs = %v, want empty", got.ReferenceAudioURLs)
	}
}

func TestParseCreateVideoRequest_NoReferenceArrays_YieldsNilNotPanic(t *testing.T) {
	body, contentType := newMultipartBody(t, map[string]string{"prompt": "p"})
	req := httptest.NewRequest(http.MethodPost, "/videos", body)
	req.Header.Set("Content-Type", contentType)

	got, err := parseCreateVideoRequest(req)
	if err != nil {
		t.Fatalf("parseCreateVideoRequest: %v", err)
	}
	if len(got.ReferenceImageURLs) != 0 || len(got.ReferenceVideoURLs) != 0 || len(got.ReferenceAudioURLs) != 0 {
		t.Errorf("want all reference arrays empty when absent, got %+v", got)
	}
}
