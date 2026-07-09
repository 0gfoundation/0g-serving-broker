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
		{"unknown status defaults to failed", "SOME_NEW_STATUS", StatusFailed},
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

func TestToDashScopeCreateRequest(t *testing.T) {
	tests := []struct {
		name         string
		req          CreateVideoRequest
		wantDuration int64
	}{
		{
			name:         "integer seconds",
			req:          CreateVideoRequest{Model: "happyhorse", Prompt: "a cat", Seconds: "5", Size: "720p"},
			wantDuration: 5,
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
			if got.Parameters.Resolution != tt.req.Size {
				t.Errorf("Resolution = %q, want %q", got.Parameters.Resolution, tt.req.Size)
			}
			if got.Parameters.Duration != tt.wantDuration {
				t.Errorf("Duration = %d, want %d", got.Parameters.Duration, tt.wantDuration)
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
				Usage:  &dashscope.TaskUsage{VideoDuration: "5"},
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
				Usage:  &dashscope.TaskUsage{VideoDuration: "5.5"},
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
