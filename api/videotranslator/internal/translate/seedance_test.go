package translate

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/0glabs/0g-serving-broker/videotranslator/internal/seedance"
)

// hasRole returns the first content item with the given role, or nil.
func hasRole(content []seedance.ContentItem, role string) *seedance.ContentItem {
	for i := range content {
		if content[i].Role == role {
			return &content[i]
		}
	}
	return nil
}

func TestStatusFromSeedance(t *testing.T) {
	tests := []struct {
		status string
		want   string
	}{
		{seedance.TaskStatusQueued, StatusQueued},
		{seedance.TaskStatusRunning, StatusInProgress},
		{seedance.TaskStatusSucceeded, StatusCompleted},
		{seedance.TaskStatusFailed, StatusFailed},
		{seedance.TaskStatusExpired, StatusFailed},
		{seedance.TaskStatusCancelled, StatusFailed},
		{"something_undocumented", StatusFailed},
		{"", StatusFailed},
	}
	for _, tt := range tests {
		if got := StatusFromSeedance(tt.status); got != tt.want {
			t.Errorf("StatusFromSeedance(%q) = %q, want %q", tt.status, got, tt.want)
		}
	}
}

func TestIsRecognizedSeedanceStatus(t *testing.T) {
	for _, s := range []string{seedance.TaskStatusQueued, seedance.TaskStatusRunning, seedance.TaskStatusSucceeded,
		seedance.TaskStatusFailed, seedance.TaskStatusExpired, seedance.TaskStatusCancelled} {
		if !IsRecognizedSeedanceStatus(s) {
			t.Errorf("IsRecognizedSeedanceStatus(%q) = false, want true", s)
		}
	}
	if IsRecognizedSeedanceStatus("bogus") {
		t.Error("IsRecognizedSeedanceStatus(bogus) = true, want false")
	}
}

func TestNormalizeSeedanceResolution(t *testing.T) {
	tests := []struct {
		size string
		want string
	}{
		{"480p", "480p"},
		{"720P", "720p"}, // case-insensitive token match
		{"1080p", "1080p"},
		{"4K", "4k"},
		{"1280x720", "720p"},
		{"1920x1080", "1080p"},
		{"3840x2160", "4k"},
		{"", defaultSeedanceResolution},
		{"garbage", defaultSeedanceResolution},
	}
	for _, tt := range tests {
		if got := normalizeSeedanceResolution(tt.size); got != tt.want {
			t.Errorf("normalizeSeedanceResolution(%q) = %q, want %q", tt.size, got, tt.want)
		}
	}
}

func TestSizeToSeedanceRatio(t *testing.T) {
	tests := []struct {
		size string
		want string
	}{
		{"1920x1080", "16:9"},
		{"1080x1920", "9:16"},
		{"1024x1024", "1:1"},
		{"", ""},
		{"garbage", ""},
	}
	for _, tt := range tests {
		if got := sizeToSeedanceRatio(tt.size); got != tt.want {
			t.Errorf("sizeToSeedanceRatio(%q) = %q, want %q", tt.size, got, tt.want)
		}
	}
}

func TestParseSeedanceDuration(t *testing.T) {
	tests := []struct {
		seconds string
		want    int64
	}{
		{"", 0},      // absent -> omit, vendor default
		{"abc", 0},   // unparsable -> omit
		{"-5", 0},    // non-positive -> omit
		{"0", 0},     // non-positive -> omit
		{"5", 5},     // in range, passthrough
		{"4", 4},     // floor of range
		{"15", 15},   // ceiling of range
		{"1", 4},     // clamped UP into range
		{"3.2", 4},   // ceil(3.2)=4, already at floor
		{"20", 15},   // clamped DOWN into range
		{"7.1", 8},   // ceil, in range
		{"1e400", 0}, // overflow -> Inf -> omit, not garbage
	}
	for _, tt := range tests {
		if got := parseSeedanceDuration(tt.seconds); got != tt.want {
			t.Errorf("parseSeedanceDuration(%q) = %d, want %d", tt.seconds, got, tt.want)
		}
	}
}

func TestParseSeedanceSeed(t *testing.T) {
	if parseSeedanceSeed("") != nil {
		t.Error("empty seed should be nil")
	}
	if parseSeedanceSeed("abc") != nil {
		t.Error("unparsable seed should be nil")
	}
	if parseSeedanceSeed("-1") != nil {
		t.Error("negative seed should be nil")
	}
	if parseSeedanceSeed("5.5") != nil {
		t.Error("non-integral seed should be nil")
	}
	if got := parseSeedanceSeed("11"); got == nil || *got != 11 {
		t.Errorf("parseSeedanceSeed(11) = %v, want 11", got)
	}
	if parseSeedanceSeed("99999999999999999999") != nil {
		t.Error("absurdly large seed should be nil, not overflow")
	}
}

func TestSeedanceWireModel(t *testing.T) {
	if got := seedanceWireModel(seedanceCanonicalModelID); got != seedanceDefaultWireModel {
		t.Errorf("canonical id should remap to the wire id, got %q", got)
	}
	if got := seedanceWireModel("dreamina-seedance-2-0-fast-260128"); got != "dreamina-seedance-2-0-fast-260128" {
		t.Errorf("an already-correct wire id must pass through unchanged, got %q", got)
	}
}

func TestToSeedanceCreateRequest(t *testing.T) {
	t.Run("text only -> single text item, no ratio guess needed", func(t *testing.T) {
		got := ToSeedanceCreateRequest(CreateVideoRequest{Prompt: "a cat", Seconds: "5"})
		if len(got.Content) != 1 || got.Content[0].Type != "text" || got.Content[0].Text != "a cat" {
			t.Fatalf("want single text item, got %+v", got.Content)
		}
		if got.Watermark == nil || *got.Watermark != false {
			t.Errorf("watermark must always be forced off, got %+v", got.Watermark)
		}
		if got.GenerateAudio != nil {
			t.Errorf("generate_audio must be omitted (nil), got %+v", got.GenerateAudio)
		}
	})

	t.Run("first_frame only -> [text, first_frame], ratio forced adaptive", func(t *testing.T) {
		got := ToSeedanceCreateRequest(CreateVideoRequest{
			Prompt: "animate", Seconds: "5", Size: "1920x1080",
			InputReferenceImageURL: "https://cdn/a.png",
		})
		if len(got.Content) != 2 {
			t.Fatalf("want 2 content items, got %+v", got.Content)
		}
		ff := hasRole(got.Content, "first_frame")
		if ff == nil || ff.ImageURL == nil || ff.ImageURL.URL != "https://cdn/a.png" {
			t.Fatalf("first_frame missing/wrong: %+v", got.Content)
		}
		if hasRole(got.Content, "last_frame") != nil {
			t.Error("no last_frame should be present")
		}
		if got.Ratio != "adaptive" {
			t.Errorf("ratio = %q, want adaptive (overrides the 16:9 a pixel size would otherwise guess)", got.Ratio)
		}
	})

	t.Run("first_frame + last_frame -> both items, correct roles + order", func(t *testing.T) {
		got := ToSeedanceCreateRequest(CreateVideoRequest{
			Prompt: "p", Seconds: "5",
			InputReferenceImageURL:     "https://cdn/a.png",
			LastFrameReferenceImageURL: "https://cdn/b.png",
		})
		if len(got.Content) != 3 {
			t.Fatalf("want [text, first_frame, last_frame], got %+v", got.Content)
		}
		if got.Content[0].Type != "text" {
			t.Errorf("content[0] must be text, got %+v", got.Content[0])
		}
		ff := hasRole(got.Content, "first_frame")
		lf := hasRole(got.Content, "last_frame")
		if ff == nil || ff.ImageURL.URL != "https://cdn/a.png" {
			t.Fatalf("first_frame wrong: %+v", ff)
		}
		if lf == nil || lf.ImageURL.URL != "https://cdn/b.png" {
			t.Fatalf("last_frame wrong: %+v", lf)
		}
		if got.Ratio != "adaptive" {
			t.Errorf("ratio = %q, want adaptive", got.Ratio)
		}
	})

	t.Run("data:image last frame accepted (not Vidu's http-only rule)", func(t *testing.T) {
		got := ToSeedanceCreateRequest(CreateVideoRequest{
			Prompt: "p", Seconds: "5",
			InputReferenceImageURL:     "https://cdn/a.png",
			LastFrameReferenceImageURL: "data:image/png;base64,AAA=",
		})
		lf := hasRole(got.Content, "last_frame")
		if lf == nil || lf.ImageURL.URL != "data:image/png;base64,AAA=" {
			t.Fatalf("data:image last frame should be accepted, got %+v", got.Content)
		}
	})

	t.Run("last_frame with unsupported non-asset scheme is ignored, first_frame preserved", func(t *testing.T) {
		got := ToSeedanceCreateRequest(CreateVideoRequest{
			Prompt: "p", Seconds: "5",
			InputReferenceImageURL:     "https://cdn/a.png",
			LastFrameReferenceImageURL: "ftp://x/b.png",
		})
		if len(got.Content) != 2 {
			t.Fatalf("want [text, first_frame] only, got %+v", got.Content)
		}
		if hasRole(got.Content, "last_frame") != nil {
			t.Error("ftp:// last_frame must be dropped")
		}
	})

	t.Run("last_frame alone (no first_frame) never emits a bare last item", func(t *testing.T) {
		got := ToSeedanceCreateRequest(CreateVideoRequest{
			Prompt: "p", Seconds: "5",
			LastFrameReferenceImageURL: "https://cdn/b.png",
		})
		if len(got.Content) != 1 || got.Content[0].Type != "text" {
			t.Fatalf("want text-only (defense in case validation is bypassed), got %+v", got.Content)
		}
	})

	t.Run("asset:// first_frame is dropped (not sent to the vendor)", func(t *testing.T) {
		got := ToSeedanceCreateRequest(CreateVideoRequest{
			Prompt: "p", Seconds: "5", InputReferenceImageURL: "asset://abc123",
		})
		if len(got.Content) != 1 {
			t.Fatalf("asset:// scheme must be dropped, got %+v", got.Content)
		}
	})

	t.Run("model remap: canonical id -> ByteDance wire id", func(t *testing.T) {
		got := ToSeedanceCreateRequest(CreateVideoRequest{Model: "bytedance/seedance-2.0", Prompt: "p"})
		if got.Model != seedanceDefaultWireModel {
			t.Errorf("Model = %q, want %q", got.Model, seedanceDefaultWireModel)
		}
	})

	t.Run("model passthrough: already-wire id unchanged", func(t *testing.T) {
		got := ToSeedanceCreateRequest(CreateVideoRequest{Model: "dreamina-seedance-2-0-fast-260128", Prompt: "p"})
		if got.Model != "dreamina-seedance-2-0-fast-260128" {
			t.Errorf("Model = %q, want passthrough", got.Model)
		}
	})

	t.Run("duration clamped into [4,15]", func(t *testing.T) {
		got := ToSeedanceCreateRequest(CreateVideoRequest{Prompt: "p", Seconds: "100"})
		if got.Duration != maxSeedanceDuration {
			t.Errorf("Duration = %d, want clamped to %d", got.Duration, maxSeedanceDuration)
		}
	})

	t.Run("reference arrays: image/video/audio items appended with correct roles, order, and resolved URLs", func(t *testing.T) {
		got := ToSeedanceCreateRequest(CreateVideoRequest{
			Prompt:             "p",
			Seconds:            "10",
			ReferenceImageURLs: []string{"https://cdn/i1.png", "data:image/png;base64,AAA="},
			ReferenceVideoURLs: []string{"https://cdn/v1.mp4"},
			ReferenceAudioURLs: []string{"https://cdn/a1.mp3"},
		})
		// [text, reference_image, reference_image, reference_video, reference_audio]
		if len(got.Content) != 5 {
			t.Fatalf("want 5 content items, got %+v", got.Content)
		}
		if got.Content[1].Role != "reference_image" || got.Content[1].ImageURL.URL != "https://cdn/i1.png" {
			t.Errorf("content[1] wrong: %+v", got.Content[1])
		}
		if got.Content[2].Role != "reference_image" || got.Content[2].Type != "image_url" || got.Content[2].ImageURL.URL != "data:image/png;base64,AAA=" {
			t.Errorf("content[2] wrong: %+v", got.Content[2])
		}
		if got.Content[3].Role != "reference_video" || got.Content[3].Type != "video_url" || got.Content[3].VideoURL.URL != "https://cdn/v1.mp4" {
			t.Errorf("content[3] wrong: %+v", got.Content[3])
		}
		if got.Content[4].Role != "reference_audio" || got.Content[4].Type != "audio_url" || got.Content[4].AudioURL.URL != "https://cdn/a1.mp3" {
			t.Errorf("content[4] wrong: %+v", got.Content[4])
		}
		if got.Ratio != "" {
			t.Errorf("ratio should not be forced adaptive for reference-array requests (no first_frame), got %q", got.Ratio)
		}
	})

	t.Run("reference_video: data:image URI rejected (narrower allowlist than reference_image)", func(t *testing.T) {
		got := ToSeedanceCreateRequest(CreateVideoRequest{
			Prompt:             "p",
			ReferenceVideoURLs: []string{"data:video/mp4;base64,AAA="},
		})
		if hasRole(got.Content, "reference_video") != nil {
			t.Fatalf("data: URI must be rejected for reference_video, got %+v", got.Content)
		}
	})

	t.Run("reference_audio: http (not just https) still accepted, ftp rejected", func(t *testing.T) {
		got := ToSeedanceCreateRequest(CreateVideoRequest{
			Prompt:             "p",
			ReferenceAudioURLs: []string{"http://cdn/a.mp3", "ftp://cdn/b.mp3"},
		})
		var count int
		for _, c := range got.Content {
			if c.Role == "reference_audio" {
				count++
			}
		}
		if count != 1 {
			t.Fatalf("want exactly 1 resolved reference_audio item, got %d in %+v", count, got.Content)
		}
	})
}

func TestFromSeedanceCreateResponse(t *testing.T) {
	req := CreateVideoRequest{Model: "bytedance/seedance-2.0", Prompt: "p", Seconds: "5", Size: "1080p"}
	resp := seedance.CreateResponse{ID: "cgt-20260606160057-6bbjd"}

	out, err := FromSeedanceCreateResponse(req, resp)
	if err != nil {
		t.Fatalf("FromSeedanceCreateResponse: %v", err)
	}
	if out.Status != StatusQueued {
		t.Errorf("Status = %q, want queued", out.Status)
	}
	if out.Seconds != "5" || out.Size != "1080p" || out.Model != "bytedance/seedance-2.0" {
		t.Errorf("echoed fields wrong: %+v", out)
	}
	if !strings.HasPrefix(out.ID, "v0_") {
		t.Errorf("ID = %q, want v0_ tagged passthrough", out.ID)
	}
}

func TestFromSeedanceCreateResponse_EmptyIDIsError(t *testing.T) {
	if _, err := FromSeedanceCreateResponse(CreateVideoRequest{}, seedance.CreateResponse{ID: ""}); err == nil {
		t.Fatal("want an error when the vendor id cannot be encoded")
	}
}

func TestFromSeedanceGetTaskResponse_SucceededBillsOnCompletionTokens(t *testing.T) {
	resp := seedance.GetTaskResponse{
		Status:     seedance.TaskStatusSucceeded,
		Resolution: "1080p",
		Duration:   json.Number("5"),
		Usage:      &seedance.TaskUsage{CompletionTokens: json.Number("246840"), TotalTokens: json.Number("246840")},
	}
	out := FromSeedanceGetTaskResponse("v0_x", resp)
	if out.Status != StatusCompleted {
		t.Errorf("Status = %q, want completed", out.Status)
	}
	if out.Size != "1080p" {
		t.Errorf("Size = %q, want 1080p", out.Size)
	}
	if out.Seconds != "5" {
		t.Errorf("Seconds = %q, want the plain informational/fallback echo '5'", out.Seconds)
	}
	if out.Usage == nil || out.Usage.CompletionTokens.String() != "246840" {
		t.Fatalf("Usage.CompletionTokens = %+v, want 246840 (the billing signal, not OutputVideoDuration)", out.Usage)
	}
	if out.Usage.OutputVideoDuration != "" {
		t.Errorf("Usage.OutputVideoDuration should stay unset for Seedance, got %q", out.Usage.OutputVideoDuration)
	}
	if out.Error != nil {
		t.Errorf("succeeded task must not carry an Error, got %+v", out.Error)
	}
}

func TestFromSeedanceGetTaskResponse_FramesFallback(t *testing.T) {
	// Defensive: the response returns duration XOR frames; this integration
	// always sends `duration`, but frames/fps must still be usable.
	resp := seedance.GetTaskResponse{
		Status:          seedance.TaskStatusSucceeded,
		Frames:          json.Number("120"),
		FramesPerSecond: json.Number("24"),
	}
	out := FromSeedanceGetTaskResponse("v0_x", resp)
	if out.Seconds != "5" {
		t.Errorf("Seconds = %q, want 5 (120/24)", out.Seconds)
	}
}

func TestFromSeedanceGetTaskResponse_ErrorMapping(t *testing.T) {
	tests := []struct {
		name     string
		resp     seedance.GetTaskResponse
		wantCode string
	}{
		{"vendor error object wins", seedance.GetTaskResponse{Status: seedance.TaskStatusFailed, Error: &seedance.TaskError{Code: "ContentModeration", Message: "blocked"}}, "ContentModeration"},
		{"expired synthesizes a code", seedance.GetTaskResponse{Status: seedance.TaskStatusExpired}, "seedance_task_expired"},
		{"cancelled synthesizes a code", seedance.GetTaskResponse{Status: seedance.TaskStatusCancelled}, "seedance_task_cancelled"},
		{"failed with no body synthesizes a code", seedance.GetTaskResponse{Status: seedance.TaskStatusFailed}, "seedance_task_failed"},
		{"unrecognized status synthesizes a code", seedance.GetTaskResponse{Status: "something_new"}, "unrecognized_seedance_status"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := FromSeedanceGetTaskResponse("v0_x", tt.resp)
			if out.Status != StatusFailed {
				t.Fatalf("Status = %q, want failed", out.Status)
			}
			if out.Error == nil || out.Error.Code != tt.wantCode {
				t.Errorf("Error = %+v, want code %q", out.Error, tt.wantCode)
			}
		})
	}
}

func TestValidateSeedanceCreateRequest(t *testing.T) {
	tests := []struct {
		name    string
		req     CreateVideoRequest
		wantErr bool
	}{
		{"text-only is valid", CreateVideoRequest{Prompt: "p"}, false},
		{"first_frame-only is valid", CreateVideoRequest{InputReferenceImageURL: "https://cdn/a.png"}, false},
		{"first_frame + last_frame is valid", CreateVideoRequest{InputReferenceImageURL: "https://cdn/a.png", LastFrameReferenceImageURL: "https://cdn/b.png"}, false},
		{"first_frame asset:// is rejected", CreateVideoRequest{InputReferenceImageURL: "asset://x"}, true},
		{"last_frame asset:// is rejected", CreateVideoRequest{InputReferenceImageURL: "https://cdn/a.png", LastFrameReferenceImageURL: "asset://x"}, true},
		{"last_frame without first_frame is rejected", CreateVideoRequest{LastFrameReferenceImageURL: "https://cdn/b.png"}, true},
		{"last_frame with UNUSABLE first_frame is rejected (resolved-value check, not raw)", CreateVideoRequest{InputReferenceImageURL: "ftp://cdn/a.png", LastFrameReferenceImageURL: "https://cdn/b.png"}, true},
		{"reference_image alone is valid", CreateVideoRequest{ReferenceImageURLs: []string{"https://cdn/a.png"}}, false},
		{"reference_video alone is valid", CreateVideoRequest{ReferenceVideoURLs: []string{"https://cdn/a.mp4"}}, false},
		{"reference_audio ALONE is rejected", CreateVideoRequest{ReferenceAudioURLs: []string{"https://cdn/a.mp3"}}, true},
		{"reference_audio with reference_image is valid", CreateVideoRequest{ReferenceImageURLs: []string{"https://cdn/a.png"}, ReferenceAudioURLs: []string{"https://cdn/a.mp3"}}, false},
		{"reference_audio with reference_video is valid", CreateVideoRequest{ReferenceVideoURLs: []string{"https://cdn/a.mp4"}, ReferenceAudioURLs: []string{"https://cdn/a.mp3"}}, false},
		{"10 reference images exceeds the cap of 9", CreateVideoRequest{ReferenceImageURLs: repeatURL("https://cdn/i.png", 10)}, true},
		{"9 reference images is exactly at the cap", CreateVideoRequest{ReferenceImageURLs: repeatURL("https://cdn/i.png", 9)}, false},
		{"4 reference videos exceeds the cap of 3", CreateVideoRequest{ReferenceVideoURLs: repeatURL("https://cdn/v.mp4", 4)}, true},
		{"4 reference audio exceeds the cap of 3", CreateVideoRequest{ReferenceImageURLs: []string{"https://cdn/i.png"}, ReferenceAudioURLs: repeatURL("https://cdn/a.mp3", 4)}, true},
		{"cardinality caps count RESOLVED urls, not raw: unusable entries don't count against the cap", CreateVideoRequest{ReferenceImageURLs: append(repeatURL("https://cdn/i.png", 9), "asset://dropped")}, false},
		{"mutual exclusivity: first_frame + reference_image together is rejected", CreateVideoRequest{InputReferenceImageURL: "https://cdn/a.png", ReferenceImageURLs: []string{"https://cdn/b.png"}}, true},
		{"mutual exclusivity: last_frame + reference_video together is rejected", CreateVideoRequest{InputReferenceImageURL: "https://cdn/a.png", LastFrameReferenceImageURL: "https://cdn/b.png", ReferenceVideoURLs: []string{"https://cdn/v.mp4"}}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateSeedanceCreateRequest(tt.req)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateSeedanceCreateRequest(%+v) error = %v, wantErr %v", tt.req, err, tt.wantErr)
			}
		})
	}
}

// repeatURL returns n copies of u — a convenience for the cardinality-cap tests.
func repeatURL(u string, n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = u
	}
	return out
}
