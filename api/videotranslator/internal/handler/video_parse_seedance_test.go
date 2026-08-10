package handler

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// last_frame_reference and the reference_images/videos/audio arrays are not
// OpenAI Video API fields (see translate.ToSeedanceCreateRequest's doc for
// the compatibility principle: the client may only use fields that already
// exist in the OpenAI Video API), so parseCreateVideoRequest has no field to
// populate them into — a client that sends them anyway gets a request that
// silently ignores those keys and proceeds on whatever OpenAI-real fields
// were also present (prompt/input_reference/etc.), not a 400.
func TestParseCreateVideoRequest_NonOpenAIFields_SilentlyIgnored_JSON(t *testing.T) {
	const body = `{"prompt":"p","input_reference":{"image_url":"https://cdn/a.png"},"last_frame_reference":{"image_url":"https://cdn/b.png"},"reference_images":["https://cdn/i1.png"],"reference_videos":["https://cdn/v1.mp4"],"reference_audio":["https://cdn/a1.mp3"]}`
	req := httptest.NewRequest(http.MethodPost, "/videos", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	got, err := parseCreateVideoRequest(req)
	if err != nil {
		t.Fatalf("parseCreateVideoRequest: %v", err)
	}
	if got.InputReferenceImageURL != "https://cdn/a.png" {
		t.Errorf("InputReferenceImageURL = %q, want the OpenAI-real input_reference to still parse", got.InputReferenceImageURL)
	}
}

func TestParseCreateVideoRequest_NonOpenAIFields_SilentlyIgnored_Multipart(t *testing.T) {
	body, contentType := newMultipartBody(t, map[string]string{
		"prompt":               "p",
		"input_reference":      "https://cdn/a.png",
		"last_frame_reference": "https://cdn/b.png",
		"reference_images":     "https://cdn/i1.png",
	})
	req := httptest.NewRequest(http.MethodPost, "/videos", body)
	req.Header.Set("Content-Type", contentType)

	got, err := parseCreateVideoRequest(req)
	if err != nil {
		t.Fatalf("parseCreateVideoRequest: %v", err)
	}
	if got.InputReferenceImageURL != "https://cdn/a.png" {
		t.Errorf("InputReferenceImageURL = %q, want the OpenAI-real input_reference to still parse", got.InputReferenceImageURL)
	}
}

func TestParseCreateVideoRequest_CameraFixed_JSON(t *testing.T) {
	for _, want := range []bool{true, false} {
		body := `{"prompt":"p","camera_fixed":` + strconv.FormatBool(want) + `}`
		req := httptest.NewRequest(http.MethodPost, "/videos", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")

		got, err := parseCreateVideoRequest(req)
		if err != nil {
			t.Fatalf("parseCreateVideoRequest: %v", err)
		}
		if got.CameraFixed == nil || *got.CameraFixed != want {
			t.Errorf("camera_fixed=%v: got %+v", want, got.CameraFixed)
		}
	}
}

func TestParseCreateVideoRequest_CameraFixed_Absent_YieldsNil(t *testing.T) {
	const body = `{"prompt":"p"}`
	req := httptest.NewRequest(http.MethodPost, "/videos", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	got, err := parseCreateVideoRequest(req)
	if err != nil {
		t.Fatalf("parseCreateVideoRequest: %v", err)
	}
	if got.CameraFixed != nil {
		t.Errorf("camera_fixed absent must parse to nil (not a false default), got %+v", got.CameraFixed)
	}
}

func TestParseCreateVideoRequest_CameraFixed_Multipart(t *testing.T) {
	body, contentType := newMultipartBody(t, map[string]string{
		"prompt":       "p",
		"camera_fixed": "true",
	})
	req := httptest.NewRequest(http.MethodPost, "/videos", body)
	req.Header.Set("Content-Type", contentType)

	got, err := parseCreateVideoRequest(req)
	if err != nil {
		t.Fatalf("parseCreateVideoRequest: %v", err)
	}
	if got.CameraFixed == nil || *got.CameraFixed != true {
		t.Errorf("camera_fixed = %+v, want true", got.CameraFixed)
	}
}

func TestParseCreateVideoRequest_CameraFixed_MultipartUnparsable_YieldsNil(t *testing.T) {
	body, contentType := newMultipartBody(t, map[string]string{
		"prompt":       "p",
		"camera_fixed": "not-a-bool",
	})
	req := httptest.NewRequest(http.MethodPost, "/videos", body)
	req.Header.Set("Content-Type", contentType)

	got, err := parseCreateVideoRequest(req)
	if err != nil {
		t.Fatalf("parseCreateVideoRequest: %v", err)
	}
	if got.CameraFixed != nil {
		t.Errorf("unparsable camera_fixed must fall back to nil (vendor default), got %+v", got.CameraFixed)
	}
}

func TestParseCreateVideoRequest_OutputFormat_JSON(t *testing.T) {
	const body = `{"prompt":"p","output_format":"mp4"}`
	req := httptest.NewRequest(http.MethodPost, "/videos", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	got, err := parseCreateVideoRequest(req)
	if err != nil {
		t.Fatalf("parseCreateVideoRequest: %v", err)
	}
	if got.OutputFormat == nil || *got.OutputFormat != "mp4" {
		t.Errorf("output_format=mp4: got %+v", got.OutputFormat)
	}
}

func TestParseCreateVideoRequest_OutputFormat_Absent_YieldsNil(t *testing.T) {
	const body = `{"prompt":"p"}`
	req := httptest.NewRequest(http.MethodPost, "/videos", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	got, err := parseCreateVideoRequest(req)
	if err != nil {
		t.Fatalf("parseCreateVideoRequest: %v", err)
	}
	if got.OutputFormat != nil {
		t.Errorf("output_format absent must parse to nil (not an empty-string default), got %+v", got.OutputFormat)
	}
}

func TestParseCreateVideoRequest_OutputFormat_Multipart(t *testing.T) {
	body, contentType := newMultipartBody(t, map[string]string{
		"prompt":        "p",
		"output_format": "mp4",
	})
	req := httptest.NewRequest(http.MethodPost, "/videos", body)
	req.Header.Set("Content-Type", contentType)

	got, err := parseCreateVideoRequest(req)
	if err != nil {
		t.Fatalf("parseCreateVideoRequest: %v", err)
	}
	if got.OutputFormat == nil || *got.OutputFormat != "mp4" {
		t.Errorf("output_format = %+v, want mp4", got.OutputFormat)
	}
}

func TestParseCreateVideoRequest_OutputFormat_MultipartAbsent_YieldsNil(t *testing.T) {
	body, contentType := newMultipartBody(t, map[string]string{"prompt": "p"})
	req := httptest.NewRequest(http.MethodPost, "/videos", body)
	req.Header.Set("Content-Type", contentType)

	got, err := parseCreateVideoRequest(req)
	if err != nil {
		t.Fatalf("parseCreateVideoRequest: %v", err)
	}
	if got.OutputFormat != nil {
		t.Errorf("output_format absent must fall back to nil (vendor default), got %+v", got.OutputFormat)
	}
}
