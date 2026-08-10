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
		// 1080p/4k are no longer exact-match tokens (2.5 only supports
		// 480p/720p, live-confirmed: 1080p/4k are rejected with
		// InvalidParameter) -- these fall through to the unparsable-size
		// default (which happens to also be 720p).
		{"1080p", defaultSeedanceResolution},
		{"4K", defaultSeedanceResolution},
		{"1280x720", "720p"},
		// This codebase's own documented standard 480p pixel size (see
		// DefaultVideoSizeRatios in api/inference/config/model_pricing.go)
		// must snap to 480p, not 720p -- a client asking for the cheap tier
		// via pixel dimensions must not be silently billed at the pricier
		// one. A fixed "<=640" cutover misclassified this; nearest-match by
		// longer side (832 vs. 1280) gets it right.
		{"832x480", "480p"},
		{"480x832", "480p"},
		// Pixel sizes that used to snap to 1080p/4K now collapse to 720p —
		// the nearest tier the model actually supports (§5.2: a conscious
		// silent-downgrade, not a 400).
		{"1920x1080", "720p"},
		{"3840x2160", "720p"},
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
		{"30", 30},   // ceiling of range
		{"1", 4},     // clamped UP into range
		{"3.2", 4},   // ceil(3.2)=4, already at floor
		{"40", 30},   // clamped DOWN into range
		{"31", 30},   // clamped DOWN into range, at the live-confirmed boundary (31 rejected, 30 accepted)
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
	if got := seedanceWireModel("dreamina-seedance-2-5-fast-260628"); got != "dreamina-seedance-2-5-fast-260628" {
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
		if got.CameraFixed != nil {
			t.Errorf("camera_fixed must be omitted (nil) when the client didn't specify it, got %+v", got.CameraFixed)
		}
	})

	t.Run("camera_fixed is passed through unchanged, both true and false", func(t *testing.T) {
		trueVal, falseVal := true, false
		got := ToSeedanceCreateRequest(CreateVideoRequest{Prompt: "a cat", Seconds: "5", CameraFixed: &trueVal})
		if got.CameraFixed == nil || *got.CameraFixed != true {
			t.Fatalf("camera_fixed=true not passed through, got %+v", got.CameraFixed)
		}
		got = ToSeedanceCreateRequest(CreateVideoRequest{Prompt: "a cat", Seconds: "5", CameraFixed: &falseVal})
		if got.CameraFixed == nil || *got.CameraFixed != false {
			t.Fatalf("camera_fixed=false not passed through (must be distinguishable from omitted), got %+v", got.CameraFixed)
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

	t.Run("asset:// first_frame is dropped (not sent to the vendor)", func(t *testing.T) {
		got := ToSeedanceCreateRequest(CreateVideoRequest{
			Prompt: "p", Seconds: "5", InputReferenceImageURL: "asset://abc123",
		})
		if len(got.Content) != 1 {
			t.Fatalf("asset:// scheme must be dropped, got %+v", got.Content)
		}
	})

	t.Run("model remap: canonical id -> ByteDance wire id", func(t *testing.T) {
		got := ToSeedanceCreateRequest(CreateVideoRequest{Model: "bytedance/seedance-2.5", Prompt: "p"})
		if got.Model != seedanceDefaultWireModel {
			t.Errorf("Model = %q, want %q", got.Model, seedanceDefaultWireModel)
		}
	})

	t.Run("model passthrough: already-wire id unchanged", func(t *testing.T) {
		got := ToSeedanceCreateRequest(CreateVideoRequest{Model: "dreamina-seedance-2-5-fast-260628", Prompt: "p"})
		if got.Model != "dreamina-seedance-2-5-fast-260628" {
			t.Errorf("Model = %q, want passthrough", got.Model)
		}
	})

	t.Run("duration clamped into [4,30]", func(t *testing.T) {
		got := ToSeedanceCreateRequest(CreateVideoRequest{Prompt: "p", Seconds: "100"})
		if got.Duration != maxSeedanceDuration {
			t.Errorf("Duration = %d, want clamped to %d", got.Duration, maxSeedanceDuration)
		}
	})

	t.Run("output_format is omitted (nil) when the client didn't specify it", func(t *testing.T) {
		got := ToSeedanceCreateRequest(CreateVideoRequest{Prompt: "p", Seconds: "5"})
		if got.OutputFormat != nil {
			t.Errorf("output_format must be omitted (nil) when the client didn't specify it, got %+v", got.OutputFormat)
		}
	})

	t.Run("output_format is passed through unchanged", func(t *testing.T) {
		mp4 := "mp4"
		got := ToSeedanceCreateRequest(CreateVideoRequest{Prompt: "p", Seconds: "5", OutputFormat: &mp4})
		if got.OutputFormat == nil || *got.OutputFormat != "mp4" {
			t.Fatalf("output_format not passed through, got %+v", got.OutputFormat)
		}
	})

}

func TestFromSeedanceCreateResponse(t *testing.T) {
	req := CreateVideoRequest{Model: "bytedance/seedance-2.5", Prompt: "p", Seconds: "5", Size: "720p"}
	resp := seedance.CreateResponse{ID: "cgt-20260606160057-6bbjd"}

	out, err := FromSeedanceCreateResponse(req, resp)
	if err != nil {
		t.Fatalf("FromSeedanceCreateResponse: %v", err)
	}
	if out.Status != StatusQueued {
		t.Errorf("Status = %q, want queued", out.Status)
	}
	if out.Seconds != "5" || out.Size != "720p" || out.Model != "bytedance/seedance-2.5" {
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
		Resolution: "720p",
		Duration:   json.Number("5"),
		Usage:      &seedance.TaskUsage{CompletionTokens: json.Number("246840"), TotalTokens: json.Number("246840")},
	}
	out := FromSeedanceGetTaskResponse("v0_x", resp)
	if out.Status != StatusCompleted {
		t.Errorf("Status = %q, want completed", out.Status)
	}
	if out.Size != "720p" {
		t.Errorf("Size = %q, want 720p", out.Size)
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
		{"first_frame asset:// is rejected", CreateVideoRequest{InputReferenceImageURL: "asset://x"}, true},
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
