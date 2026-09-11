package translate

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/0glabs/0g-serving-broker/videotranslator/internal/kling"
)

func TestStatusFromKling(t *testing.T) {
	tests := []struct {
		status string
		want   string
	}{
		{kling.TaskStatusPending, StatusQueued},
		{kling.TaskStatusRunning, StatusInProgress},
		{kling.TaskStatusSucceeded, StatusCompleted},
		{kling.TaskStatusFailed, StatusFailed},
		{kling.TaskStatusCanceled, StatusFailed},
		{kling.TaskStatusUnknown, StatusFailed},
		{"something_undocumented", StatusFailed},
		{"", StatusFailed},
	}
	for _, tt := range tests {
		if got := StatusFromKling(tt.status); got != tt.want {
			t.Errorf("StatusFromKling(%q) = %q, want %q", tt.status, got, tt.want)
		}
	}
}

func TestIsRecognizedKlingStatus(t *testing.T) {
	for _, s := range []string{kling.TaskStatusPending, kling.TaskStatusRunning, kling.TaskStatusSucceeded,
		kling.TaskStatusFailed, kling.TaskStatusCanceled, kling.TaskStatusUnknown} {
		if !IsRecognizedKlingStatus(s) {
			t.Errorf("IsRecognizedKlingStatus(%q) = false, want true", s)
		}
	}
	if IsRecognizedKlingStatus("bogus") {
		t.Error("IsRecognizedKlingStatus(bogus) = true, want false")
	}
}

func TestKlingWireModel(t *testing.T) {
	if got := klingWireModel(""); got != klingDefaultModel {
		t.Errorf("klingWireModel(\"\") = %q, want default %q", got, klingDefaultModel)
	}
	if got := klingWireModel("   "); got != klingDefaultModel {
		t.Errorf("klingWireModel(whitespace) = %q, want default %q", got, klingDefaultModel)
	}
	if got := klingWireModel(klingDefaultModel); got != klingDefaultModel {
		t.Errorf("klingWireModel(default) = %q, want passthrough", got)
	}
	if got := klingWireModel("some-other-model"); got != "some-other-model" {
		t.Errorf("klingWireModel(other) = %q, want passthrough unchanged", got)
	}
}

func TestSizeToKlingAspectRatio(t *testing.T) {
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
		if got := sizeToKlingAspectRatio(tt.size); got != tt.want {
			t.Errorf("sizeToKlingAspectRatio(%q) = %q, want %q", tt.size, got, tt.want)
		}
	}
}

func TestToKlingCreateRequest(t *testing.T) {
	t.Run("text only -> no media, audio and watermark forced false", func(t *testing.T) {
		got, err := ToKlingCreateRequest(CreateVideoRequest{Prompt: "a cat", Seconds: "5"})
		if err != nil {
			t.Fatalf("ToKlingCreateRequest: %v", err)
		}
		if len(got.Input.Media) != 0 {
			t.Fatalf("want no media items, got %+v", got.Input.Media)
		}
		if got.Input.Prompt != "a cat" {
			t.Errorf("Prompt = %q, want %q", got.Input.Prompt, "a cat")
		}
		if got.Parameters.Watermark == nil || *got.Parameters.Watermark != false {
			t.Errorf("watermark must always be forced off, got %+v", got.Parameters.Watermark)
		}
		if got.Parameters.Audio == nil || *got.Parameters.Audio != false {
			t.Errorf("audio must always be forced off, got %+v", got.Parameters.Audio)
		}
		if got.Parameters.Duration != 5 {
			t.Errorf("Duration = %d, want 5", got.Parameters.Duration)
		}
	})

	t.Run("model defaults when empty", func(t *testing.T) {
		got, err := ToKlingCreateRequest(CreateVideoRequest{Prompt: "p"})
		if err != nil {
			t.Fatalf("ToKlingCreateRequest: %v", err)
		}
		if got.Model != klingDefaultModel {
			t.Errorf("Model = %q, want default %q", got.Model, klingDefaultModel)
		}
	})

	t.Run("model passthrough when set", func(t *testing.T) {
		got, err := ToKlingCreateRequest(CreateVideoRequest{Model: "kling/kling-v3-video-generation", Prompt: "p"})
		if err != nil {
			t.Fatalf("ToKlingCreateRequest: %v", err)
		}
		if got.Model != "kling/kling-v3-video-generation" {
			t.Errorf("Model = %q, want passthrough", got.Model)
		}
	})

	t.Run("absent duration is omitted (vendor default 5s)", func(t *testing.T) {
		got, err := ToKlingCreateRequest(CreateVideoRequest{Prompt: "p"})
		if err != nil {
			t.Fatalf("ToKlingCreateRequest: %v", err)
		}
		if got.Parameters.Duration != 0 {
			t.Errorf("Duration = %d, want 0 (omitted)", got.Parameters.Duration)
		}
	})

	t.Run("duration clamped into [3,15]", func(t *testing.T) {
		got, err := ToKlingCreateRequest(CreateVideoRequest{Prompt: "p", Seconds: "100"})
		if err != nil {
			t.Fatalf("ToKlingCreateRequest: %v", err)
		}
		if got.Parameters.Duration != 15 {
			t.Errorf("Duration = %d, want clamped to 15", got.Parameters.Duration)
		}
	})

	t.Run("an absurd seconds magnitude is rejected", func(t *testing.T) {
		_, err := ToKlingCreateRequest(CreateVideoRequest{Prompt: "p", Seconds: "1e30"})
		if err != ErrSecondsOutOfRange {
			t.Fatalf("err = %v, want ErrSecondsOutOfRange", err)
		}
	})

	t.Run("first_frame only -> [first_frame], mode/aspect_ratio from size", func(t *testing.T) {
		got, err := ToKlingCreateRequest(CreateVideoRequest{
			Prompt: "animate", Seconds: "5", Size: "1920x1080",
			InputReferenceImageURL: "https://cdn/a.png",
		})
		if err != nil {
			t.Fatalf("ToKlingCreateRequest: %v", err)
		}
		if len(got.Input.Media) != 1 {
			t.Fatalf("want 1 media item, got %+v", got.Input.Media)
		}
		if got.Input.Media[0].Type != "first_frame" || got.Input.Media[0].URL != "https://cdn/a.png" {
			t.Errorf("media[0] wrong: %+v", got.Input.Media[0])
		}
		if got.Parameters.Mode != "pro" {
			t.Errorf("Mode = %q, want pro (1920x1080 snaps to pro)", got.Parameters.Mode)
		}
		if got.Parameters.AspectRatio != "16:9" {
			t.Errorf("AspectRatio = %q, want 16:9", got.Parameters.AspectRatio)
		}
	})

	t.Run("unparsable size on text-to-video omits mode but EXPLICITLY sends aspect_ratio (vendor requires it present for t2v, unlike mode)", func(t *testing.T) {
		got, err := ToKlingCreateRequest(CreateVideoRequest{Prompt: "p", Size: "garbage"})
		if err != nil {
			t.Fatalf("ToKlingCreateRequest: %v", err)
		}
		if got.Parameters.Mode != "" {
			t.Errorf("Mode = %q, want omitted", got.Parameters.Mode)
		}
		if got.Parameters.AspectRatio != "16:9" {
			t.Errorf("AspectRatio = %q, want 16:9 (aspect_ratio must be explicitly present for text-to-video, unlike mode)", got.Parameters.AspectRatio)
		}
	})

	t.Run("unparsable size on image-to-video omits aspect_ratio (vendor derives it from the first frame)", func(t *testing.T) {
		got, err := ToKlingCreateRequest(CreateVideoRequest{Prompt: "p", Size: "garbage", InputReferenceImageURL: "https://cdn/a.png"})
		if err != nil {
			t.Fatalf("ToKlingCreateRequest: %v", err)
		}
		if got.Parameters.AspectRatio != "" {
			t.Errorf("AspectRatio = %q, want omitted — image-to-video's aspect_ratio is genuinely optional (vendor follows the first frame)", got.Parameters.AspectRatio)
		}
	})

	t.Run("input_reference.file_id alone -> no media (ValidateKlingCreateRequest rejects it before this runs in the real handler, but this function itself has no case for it)", func(t *testing.T) {
		got, err := ToKlingCreateRequest(CreateVideoRequest{Prompt: "p", InputReferenceFileID: "file-abc123"})
		if err != nil {
			t.Fatalf("ToKlingCreateRequest: %v", err)
		}
		if len(got.Input.Media) != 0 {
			t.Fatalf("file_id has no Kling mapping, want no media, got %+v", got.Input.Media)
		}
	})

	t.Run("ftp:// first_frame silently degrades to text-to-video, by design (not a 400)", func(t *testing.T) {
		got, err := ToKlingCreateRequest(CreateVideoRequest{Prompt: "p", InputReferenceImageURL: "ftp://cdn/a.png"})
		if err != nil {
			t.Fatalf("ToKlingCreateRequest: %v", err)
		}
		if len(got.Input.Media) != 0 {
			t.Fatalf("unusable scheme must silently degrade to no media, got %+v", got.Input.Media)
		}
		if err := ValidateKlingCreateRequest(CreateVideoRequest{InputReferenceImageURL: "ftp://cdn/a.png"}); err != nil {
			t.Errorf("ftp:// must NOT be rejected by ValidateKlingCreateRequest (silent degrade is the documented behavior) — got %v", err)
		}
	})

	t.Run("data:image first_frame also silently degrades — Kling's media.url is documented HTTP/HTTPS only, unlike MiniMax/Seedance", func(t *testing.T) {
		got, err := ToKlingCreateRequest(CreateVideoRequest{Prompt: "p", InputReferenceImageURL: "data:image/png;base64,aGVsbG8="})
		if err != nil {
			t.Fatalf("ToKlingCreateRequest: %v", err)
		}
		if len(got.Input.Media) != 0 {
			t.Fatalf("data: URI must silently degrade to no media (Kling has no documented base64 support), got %+v", got.Input.Media)
		}
	})
}

func TestFromKlingCreateResponse(t *testing.T) {
	req := CreateVideoRequest{Model: "kling/kling-v3-video-generation", Prompt: "p", Seconds: "5", Size: "1280x720"}
	resp := kling.CreateResponse{Output: kling.CreateOutput{TaskID: "abc123", TaskStatus: kling.TaskStatusPending}}

	out, err := FromKlingCreateResponse(req, resp)
	if err != nil {
		t.Fatalf("FromKlingCreateResponse: %v", err)
	}
	if out.Status != StatusQueued {
		t.Errorf("Status = %q, want queued", out.Status)
	}
	if out.Seconds != "5" || out.Size != "1280x720" || out.Model != "kling/kling-v3-video-generation" {
		t.Errorf("echoed fields wrong: %+v", out)
	}
	if !strings.HasPrefix(out.ID, "v0_") {
		t.Errorf("ID = %q, want v0_ tagged passthrough", out.ID)
	}
}

func TestFromKlingCreateResponse_EmptyTaskIDIsError(t *testing.T) {
	if _, err := FromKlingCreateResponse(CreateVideoRequest{}, kling.CreateResponse{}); err == nil {
		t.Fatal("want an error when the vendor task id cannot be encoded")
	}
}

func TestKlingTierFromUsageSize(t *testing.T) {
	tests := []struct {
		raw  string
		want string
	}{
		{"1280*720", "std"},
		{"1920*1080", "pro"},
		{"", ""},
		{"garbage", ""},
	}
	for _, tt := range tests {
		if got := klingTierFromUsageSize(tt.raw); got != tt.want {
			t.Errorf("klingTierFromUsageSize(%q) = %q, want %q", tt.raw, got, tt.want)
		}
	}
}

func TestFromKlingGetTaskResponse_SucceededBillsOnUsageDuration(t *testing.T) {
	resp := kling.GetTaskResponse{
		Output: kling.GetOutput{TaskStatus: kling.TaskStatusSucceeded},
		Usage:  &kling.GetUsage{Duration: json.Number("5"), Size: "1280*720"},
	}
	out := FromKlingGetTaskResponse("v0_x", resp)
	if out.Status != StatusCompleted {
		t.Errorf("Status = %q, want completed", out.Status)
	}
	if out.Size != "std" {
		t.Errorf("Size = %q, want std (no usage.SR here, falls back to usage.size)", out.Size)
	}
	if out.Seconds != "5" {
		t.Errorf("Seconds = %q, want 5", out.Seconds)
	}
	if out.Usage == nil || out.Usage.OutputVideoDuration.String() != "5" {
		t.Fatalf("Usage.OutputVideoDuration = %+v, want 5", out.Usage)
	}
	if out.Error != nil {
		t.Errorf("succeeded task must not carry an Error, got %+v", out.Error)
	}
}

func TestFromKlingGetTaskResponse_SubmitTimePopulatesCreatedAtExpiresAt(t *testing.T) {
	wantSubmitTime := time.Date(2026, 4, 20, 17, 55, 17, 75_000_000, time.FixedZone("UTC+8", 8*3600))

	t.Run("submit_time present", func(t *testing.T) {
		resp := kling.GetTaskResponse{
			Output: kling.GetOutput{TaskStatus: kling.TaskStatusRunning, SubmitTime: "2026-04-20 17:55:17.075"},
		}
		out := FromKlingGetTaskResponse("v0_x", resp)
		if out.CreatedAt != wantSubmitTime.Unix() {
			t.Errorf("CreatedAt = %d, want %d", out.CreatedAt, wantSubmitTime.Unix())
		}
		wantExpiresAt := wantSubmitTime.Unix() + int64(24*time.Hour/time.Second)
		if out.ExpiresAt != wantExpiresAt {
			t.Errorf("ExpiresAt = %d, want %d (submit_time + 24h)", out.ExpiresAt, wantExpiresAt)
		}
	})

	t.Run("submit_time missing leaves created_at/expires_at at zero, not a guessed 'now'", func(t *testing.T) {
		resp := kling.GetTaskResponse{Output: kling.GetOutput{TaskStatus: kling.TaskStatusRunning}}
		out := FromKlingGetTaskResponse("v0_x", resp)
		if out.CreatedAt != 0 || out.ExpiresAt != 0 {
			t.Errorf("CreatedAt/ExpiresAt = %d/%d, want 0/0 when submit_time is absent", out.CreatedAt, out.ExpiresAt)
		}
	})
}

func TestKlingBillingTier(t *testing.T) {
	tests := []struct {
		name string
		sr   string
		size string
		want string
	}{
		{"SR=720 wins outright, no size needed", "720", "", "std"},
		{"SR=1080 wins outright, no size needed", "1080", "", "pro"},
		{"SR present but conflicts with size — SR (the vendor's own reported tier) wins", "1080", "1280*720", "pro"},
		{"SR absent falls back to size-derived guess", "", "1920*1080", "pro"},
		{"SR unrecognized falls back to size-derived guess", "4k", "1280*720", "std"},
		{"neither usable -> empty", "", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := klingBillingTier(tt.sr, tt.size); got != tt.want {
				t.Errorf("klingBillingTier(%q, %q) = %q, want %q", tt.sr, tt.size, got, tt.want)
			}
		})
	}
}

func TestFromKlingGetTaskResponse_PrefersUsageSROverSize(t *testing.T) {
	// A deliberately conflicting size (would derive "std" alone) to prove SR,
	// not size, is what actually wins in the full response path.
	resp := kling.GetTaskResponse{
		Output: kling.GetOutput{TaskStatus: kling.TaskStatusSucceeded},
		Usage:  &kling.GetUsage{Duration: json.Number("5"), Size: "1280*720", SR: "1080"},
	}
	out := FromKlingGetTaskResponse("v0_x", resp)
	if out.Size != "pro" {
		t.Errorf("Size = %q, want pro (usage.SR must win over the conflicting usage.size)", out.Size)
	}
}

func TestFromKlingGetTaskResponse_ErrorMapping(t *testing.T) {
	tests := []struct {
		name     string
		resp     kling.GetTaskResponse
		wantCode string
	}{
		{"vendor error object wins", kling.GetTaskResponse{Output: kling.GetOutput{TaskStatus: kling.TaskStatusFailed, Code: "ContentModeration", Message: "blocked"}}, "ContentModeration"},
		{"canceled synthesizes a code", kling.GetTaskResponse{Output: kling.GetOutput{TaskStatus: kling.TaskStatusCanceled}}, "kling_task_canceled"},
		{"unknown synthesizes a code", kling.GetTaskResponse{Output: kling.GetOutput{TaskStatus: kling.TaskStatusUnknown}}, "kling_task_unknown"},
		{"failed with no body synthesizes a code", kling.GetTaskResponse{Output: kling.GetOutput{TaskStatus: kling.TaskStatusFailed}}, "kling_task_failed"},
		{"unrecognized status synthesizes a code", kling.GetTaskResponse{Output: kling.GetOutput{TaskStatus: "something_new"}}, "unrecognized_kling_status"},
		{"vendor error object wins over canceled's synthesized code too", kling.GetTaskResponse{Output: kling.GetOutput{TaskStatus: kling.TaskStatusCanceled, Code: "ContentModeration", Message: "blocked"}}, "ContentModeration"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := FromKlingGetTaskResponse("v0_x", tt.resp)
			if out.Status != StatusFailed {
				t.Fatalf("Status = %q, want failed", out.Status)
			}
			if out.Error == nil || out.Error.Code != tt.wantCode {
				t.Errorf("Error = %+v, want code %q", out.Error, tt.wantCode)
			}
		})
	}
}

func TestValidateKlingCreateRequest(t *testing.T) {
	tests := []struct {
		name    string
		req     CreateVideoRequest
		wantErr bool
	}{
		{"text-only is valid", CreateVideoRequest{Prompt: "p"}, false},
		{"first_frame-only is valid", CreateVideoRequest{InputReferenceImageURL: "https://cdn/a.png"}, false},
		{"input_reference.file_id alone is rejected", CreateVideoRequest{InputReferenceFileID: "file-abc123"}, true},
		{"input_reference.file_id alongside image_url is still rejected", CreateVideoRequest{InputReferenceImageURL: "https://cdn/a.png", InputReferenceFileID: "file-abc123"}, true},
		{"an absurd seconds magnitude is rejected, not clamped", CreateVideoRequest{Prompt: "p", Seconds: "1e30"}, true},
		{"clamped out-of-range seconds are still accepted", CreateVideoRequest{Prompt: "p", Seconds: "31"}, false},
		{"an unreadable seconds is accepted (the vendor picks the length)", CreateVideoRequest{Prompt: "p", Seconds: "abc"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateKlingCreateRequest(tt.req)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateKlingCreateRequest(%+v) error = %v, wantErr %v", tt.req, err, tt.wantErr)
			}
		})
	}
}
