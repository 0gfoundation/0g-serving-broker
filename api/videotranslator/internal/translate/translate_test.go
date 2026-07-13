package translate

import (
	"testing"

	"github.com/0glabs/0g-serving-broker/videotranslator/internal/dashscope"
)

func TestStatusFromDashScope(t *testing.T) {
	tests := []struct {
		name   string
		status string
		want   string
	}{
		{"pending maps to queued", dashscope.TaskStatusPending, StatusQueued},
		{"running maps to in_progress", dashscope.TaskStatusRunning, StatusInProgress},
		{"succeeded maps to completed", dashscope.TaskStatusSucceeded, StatusCompleted},
		{"failed maps to failed", dashscope.TaskStatusFailed, StatusFailed},
		{"canceled maps to failed", dashscope.TaskStatusCanceled, StatusFailed},
		{"unknown (expired task_id) maps to failed", dashscope.TaskStatusUnknown, StatusFailed},
		{"unrecognized status defaults to failed", "SOME_NEW_STATUS", StatusFailed},
		{"empty status defaults to failed", "", StatusFailed},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := StatusFromDashScope(tt.status); got != tt.want {
				t.Errorf("StatusFromDashScope(%q) = %q, want %q", tt.status, got, tt.want)
			}
		})
	}
}

func TestIsRecognizedDashScopeStatus(t *testing.T) {
	tests := []struct {
		status string
		want   bool
	}{
		{dashscope.TaskStatusPending, true},
		{dashscope.TaskStatusRunning, true},
		{dashscope.TaskStatusSucceeded, true},
		{dashscope.TaskStatusFailed, true},
		{dashscope.TaskStatusCanceled, true},
		{dashscope.TaskStatusUnknown, true},
		{"SOME_NEW_STATUS", false},
		{"", false},
	}
	for _, tt := range tests {
		t.Run(tt.status, func(t *testing.T) {
			if got := IsRecognizedDashScopeStatus(tt.status); got != tt.want {
				t.Errorf("IsRecognizedDashScopeStatus(%q) = %v, want %v", tt.status, got, tt.want)
			}
		})
	}
}

func TestSizeToDashScopeParams(t *testing.T) {
	tests := []struct {
		name           string
		size           string
		wantResolution string
		wantRatio      string
	}{
		// The two size pairs the real OpenAI Video API documents, split
		// cleanly across HappyHorse's two resolution tiers.
		{"1280x720 (openai landscape 720-tier)", "1280x720", "720P", "16:9"},
		{"720x1280 (openai portrait 720-tier)", "720x1280", "720P", "9:16"},
		{"1792x1024 (openai landscape 1080-tier)", "1792x1024", "1080P", "16:9"},
		{"1024x1792 (openai portrait 1080-tier)", "1024x1792", "1080P", "9:16"},
		{"square", "1024x1024", "720P", "1:1"},
		{"case-insensitive separator", "1280X720", "720P", "16:9"},
		{"empty size yields no override", "", "", ""},
		{"unparsable size yields no override", "not-a-size", "", ""},
		{"zero dimension yields no override", "0x720", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resolution, ratio := sizeToDashScopeParams(tt.size)
			if resolution != tt.wantResolution {
				t.Errorf("resolution = %q, want %q", resolution, tt.wantResolution)
			}
			if ratio != tt.wantRatio {
				t.Errorf("ratio = %q, want %q", ratio, tt.wantRatio)
			}
		})
	}
}

func TestToDashScopeCreateRequest(t *testing.T) {
	tests := []struct {
		name           string
		req            CreateVideoRequest
		wantDuration   int64
		wantResolution string
		wantRatio      string
	}{
		{
			name:           "integer seconds and size mapped to resolution/ratio",
			req:            CreateVideoRequest{Model: "happyhorse", Prompt: "a cat", Seconds: "5", Size: "1280x720"},
			wantDuration:   5,
			wantResolution: "720P",
			wantRatio:      "16:9",
		},
		{
			name:         "float seconds rounds up",
			req:          CreateVideoRequest{Seconds: "5.5"},
			wantDuration: 6,
		},
		{
			name:         "zero seconds omitted",
			req:          CreateVideoRequest{Seconds: "0"},
			wantDuration: 0,
		},
		{
			name:         "negative seconds omitted",
			req:          CreateVideoRequest{Seconds: "-5"},
			wantDuration: 0,
		},
		{
			name:         "unparsable seconds omitted",
			req:          CreateVideoRequest{Seconds: "not-a-number"},
			wantDuration: 0,
		},
		{
			name:         "empty seconds omitted",
			req:          CreateVideoRequest{Seconds: ""},
			wantDuration: 0,
		},
		{
			name:         "excessive finite seconds omitted rather than overflowing int64",
			req:          CreateVideoRequest{Seconds: "1e20"},
			wantDuration: 0,
		},
		{
			name:         "seconds exactly at the bound is honored",
			req:          CreateVideoRequest{Seconds: "1099511627776"}, // 1<<40
			wantDuration: maxDashScopeSeconds,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ToDashScopeCreateRequest(tt.req)
			if got.Model != tt.req.Model {
				t.Errorf("Model = %q, want %q", got.Model, tt.req.Model)
			}
			if got.Input.Prompt != tt.req.Prompt {
				t.Errorf("Prompt = %q, want %q", got.Input.Prompt, tt.req.Prompt)
			}
			if got.Parameters.Resolution != tt.wantResolution {
				t.Errorf("Resolution = %q, want %q", got.Parameters.Resolution, tt.wantResolution)
			}
			if got.Parameters.Ratio != tt.wantRatio {
				t.Errorf("Ratio = %q, want %q", got.Parameters.Ratio, tt.wantRatio)
			}
			if got.Parameters.Duration != tt.wantDuration {
				t.Errorf("Duration = %d, want %d", got.Parameters.Duration, tt.wantDuration)
			}
			if got.Parameters.Watermark != false {
				t.Errorf("Watermark = %v, want false (always disabled, regardless of request content)", got.Parameters.Watermark)
			}
		})
	}
}

func TestFromCreateResponse(t *testing.T) {
	req := CreateVideoRequest{Model: "happyhorse", Prompt: "a cat", Seconds: "5", Size: "720p"}
	resp := dashscope.CreateResponse{
		RequestID: "req-1",
		Output:    dashscope.CreateOutput{TaskID: "task-123", TaskStatus: dashscope.TaskStatusPending},
	}

	got := FromCreateResponse(req, resp)

	want := VideoResponse{
		ID:      "task-123",
		Object:  "video",
		Model:   "happyhorse",
		Status:  StatusQueued,
		Seconds: "5",
		Size:    "720p",
	}
	if got != want {
		t.Errorf("FromCreateResponse() = %+v, want %+v", got, want)
	}
}

func TestFromGetTaskResponse(t *testing.T) {
	tests := []struct {
		name string
		resp dashscope.GetTaskResponse
		want VideoResponse
	}{
		{
			name: "succeeded with usage renames video_duration to output_video_duration",
			resp: dashscope.GetTaskResponse{
				Output: dashscope.TaskOutput{TaskID: "task-123", TaskStatus: dashscope.TaskStatusSucceeded, VideoURL: "https://x/y.mp4"},
				Usage:  &dashscope.TaskUsage{OutputVideoDuration: "5"},
			},
			want: VideoResponse{
				ID:     "task-123",
				Object: "video",
				Status: StatusCompleted,
				Usage:  &Usage{OutputVideoDuration: "5"},
			},
		},
		{
			name: "float video_duration preserved",
			resp: dashscope.GetTaskResponse{
				Output: dashscope.TaskOutput{TaskID: "task-123", TaskStatus: dashscope.TaskStatusSucceeded},
				Usage:  &dashscope.TaskUsage{OutputVideoDuration: "5.5"},
			},
			want: VideoResponse{
				ID:     "task-123",
				Object: "video",
				Status: StatusCompleted,
				Usage:  &Usage{OutputVideoDuration: "5.5"},
			},
		},
		{
			name: "running has no usage yet",
			resp: dashscope.GetTaskResponse{
				Output: dashscope.TaskOutput{TaskID: "task-123", TaskStatus: dashscope.TaskStatusRunning},
			},
			want: VideoResponse{
				ID:     "task-123",
				Object: "video",
				Status: StatusInProgress,
			},
		},
		{
			name: "failed maps error code and message",
			resp: dashscope.GetTaskResponse{
				Output: dashscope.TaskOutput{
					TaskID:     "task-123",
					TaskStatus: dashscope.TaskStatusFailed,
					Code:       "InvalidParameter",
					Message:    "prompt violates content policy",
				},
			},
			want: VideoResponse{
				ID:     "task-123",
				Object: "video",
				Status: StatusFailed,
				Error:  &Error{Code: "InvalidParameter", Message: "prompt violates content policy"},
			},
		},
		{
			name: "empty usage block omitted",
			resp: dashscope.GetTaskResponse{
				Output: dashscope.TaskOutput{TaskID: "task-123", TaskStatus: dashscope.TaskStatusSucceeded},
				Usage:  &dashscope.TaskUsage{},
			},
			want: VideoResponse{
				ID:     "task-123",
				Object: "video",
				Status: StatusCompleted,
			},
		},
		{
			name: "unrecognized status defaults to failed WITH a synthetic diagnostic error",
			resp: dashscope.GetTaskResponse{
				Output: dashscope.TaskOutput{TaskID: "task-123", TaskStatus: "SOME_NEW_STATUS"},
			},
			want: VideoResponse{
				ID:     "task-123",
				Object: "video",
				Status: StatusFailed,
				Error:  &Error{Code: "unrecognized_dashscope_status", Message: `dashscope reported unrecognized task_status "SOME_NEW_STATUS"`},
			},
		},
		{
			name: "canceled maps to failed with a distinct diagnostic code",
			resp: dashscope.GetTaskResponse{
				Output: dashscope.TaskOutput{TaskID: "task-123", TaskStatus: dashscope.TaskStatusCanceled},
			},
			want: VideoResponse{
				ID:     "task-123",
				Object: "video",
				Status: StatusFailed,
				Error:  &Error{Code: "dashscope_task_canceled", Message: "dashscope reported task_status CANCELED"},
			},
		},
		{
			name: "unknown (expired task_id) maps to failed with a distinct diagnostic code",
			resp: dashscope.GetTaskResponse{
				Output: dashscope.TaskOutput{TaskID: "task-123", TaskStatus: dashscope.TaskStatusUnknown},
			},
			want: VideoResponse{
				ID:     "task-123",
				Object: "video",
				Status: StatusFailed,
				Error:  &Error{Code: "dashscope_task_unknown", Message: "dashscope reported task_status UNKNOWN (task expired past its 24h validity, or never existed)"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FromGetTaskResponse(tt.resp)
			if got.ID != tt.want.ID || got.Object != tt.want.Object || got.Status != tt.want.Status {
				t.Errorf("FromGetTaskResponse() = %+v, want %+v", got, tt.want)
			}
			if (got.Usage == nil) != (tt.want.Usage == nil) {
				t.Errorf("Usage presence = %v, want %v", got.Usage != nil, tt.want.Usage != nil)
			} else if got.Usage != nil && *got.Usage != *tt.want.Usage {
				t.Errorf("Usage = %+v, want %+v", *got.Usage, *tt.want.Usage)
			}
			if (got.Error == nil) != (tt.want.Error == nil) {
				t.Errorf("Error presence = %v, want %v", got.Error != nil, tt.want.Error != nil)
			} else if got.Error != nil && *got.Error != *tt.want.Error {
				t.Errorf("Error = %+v, want %+v", *got.Error, *tt.want.Error)
			}
		})
	}
}
