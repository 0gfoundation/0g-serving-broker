package translate

import (
	"testing"

	"github.com/0glabs/0g-serving-broker/videotranslator/internal/vidu"
)

func TestValidateViduDuration_ClampsPerModelVariant(t *testing.T) {
	tests := []struct {
		name    string
		model   string
		seconds string
		want    int64
	}{
		{"Q3 pro within range", ModelViduQ3Pro, "10", 10},
		{"Q3 pro clamped at max 16", ModelViduQ3Pro, "20", 16},
		{"Q3 turbo clamped at max 16", ModelViduQ3Turbo, "999", 16},
		{"Q2 pro within range", ModelViduQ2Pro, "8", 8},
		{"Q2 pro clamped at max 10", ModelViduQ2Pro, "20", 10},
		{"Q2 turbo clamped at max 10 (would fit Q3 range)", ModelViduQ2Turbo, "15", 10},
		{"absent seconds defaults to 5", ModelViduQ3Pro, "", 5},
		{"unparsable seconds defaults to 5", ModelViduQ2Pro, "not-a-number", 5},
		{"zero seconds defaults to 5", ModelViduQ3Pro, "0", 5},
		{"clamped at min 1", ModelViduQ2Pro, "0.1", 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := validateViduDuration(tt.model, tt.seconds)
			if got != tt.want {
				t.Errorf("validateViduDuration(%q, %q) = %d, want %d", tt.model, tt.seconds, got, tt.want)
			}
		})
	}
}

func TestValidateViduAudio_RejectsOnQ2(t *testing.T) {
	tests := []struct {
		name    string
		model   string
		audio   string
		wantErr bool
		want    *bool
	}{
		{"Q3 pro audio true allowed", ModelViduQ3Pro, "true", false, boolPtr(true)},
		{"Q3 turbo audio true allowed", ModelViduQ3Turbo, "true", false, boolPtr(true)},
		{"Q2 pro audio true REJECTED", ModelViduQ2Pro, "true", true, nil},
		{"Q2 turbo audio true REJECTED", ModelViduQ2Turbo, "true", true, nil},
		{"Q2 pro audio false allowed", ModelViduQ2Pro, "false", false, boolPtr(false)},
		{"absent audio omitted, no error", ModelViduQ2Pro, "", false, nil},
		{"unparsable audio treated as absent", ModelViduQ2Pro, "not-a-bool", false, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := validateViduAudio(tt.model, tt.audio)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if (got == nil) != (tt.want == nil) {
				t.Fatalf("got = %v, want %v", got, tt.want)
			}
			if got != nil && *got != *tt.want {
				t.Errorf("got = %v, want %v", *got, *tt.want)
			}
		})
	}
}

func boolPtr(b bool) *bool { return &b }

func TestValidateViduBothFramesPresent(t *testing.T) {
	tests := []struct {
		name    string
		req     CreateVideoRequest
		wantErr bool
	}{
		{
			name: "both frames present as http URLs",
			req: CreateVideoRequest{
				InputReferenceImageURL:     "https://example.com/first.png",
				LastFrameReferenceImageURL: "https://example.com/last.png",
			},
			wantErr: false,
		},
		{
			name:    "both frames missing",
			req:     CreateVideoRequest{},
			wantErr: true,
		},
		{
			name: "only first frame present",
			req: CreateVideoRequest{
				InputReferenceImageURL: "https://example.com/first.png",
			},
			wantErr: true,
		},
		{
			name: "only last frame present",
			req: CreateVideoRequest{
				LastFrameReferenceImageURL: "https://example.com/last.png",
			},
			wantErr: true,
		},
		{
			name: "first frame is a data: URI (raw multipart upload degraded) — REJECTED, not silently forwarded",
			req: CreateVideoRequest{
				InputReferenceImageURL:     "data:image/png;base64,aGVsbG8=",
				LastFrameReferenceImageURL: "https://example.com/last.png",
			},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateViduBothFramesPresent(tt.req)
			if (err != nil) != tt.wantErr {
				t.Errorf("err = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestToViduCreateRequest_MediaFrameOrder(t *testing.T) {
	req := CreateVideoRequest{
		Model:                      ModelViduQ3Turbo,
		Prompt:                     "a cat jumping",
		InputReferenceImageURL:     "https://example.com/FIRST.png",
		LastFrameReferenceImageURL: "https://example.com/LAST.png",
	}
	got := ToViduCreateRequest(req)
	if len(got.Input.Media) != 2 {
		t.Fatalf("media length = %d, want 2", len(got.Input.Media))
	}
	if got.Input.Media[0].URL != "https://example.com/FIRST.png" {
		t.Errorf("media[0].url = %q, want first frame", got.Input.Media[0].URL)
	}
	if got.Input.Media[1].URL != "https://example.com/LAST.png" {
		t.Errorf("media[1].url = %q, want last frame", got.Input.Media[1].URL)
	}
	if got.Model != ModelViduQ3Turbo {
		t.Errorf("model = %q, want full vidu/... wire-format string preserved", got.Model)
	}
}

func TestToViduCreateRequest_ModelWireFormatPreserved(t *testing.T) {
	models := []string{ModelViduQ3Pro, ModelViduQ3Turbo, ModelViduQ2Pro, ModelViduQ2Turbo}
	for _, m := range models {
		t.Run(m, func(t *testing.T) {
			req := CreateVideoRequest{Model: m, InputReferenceImageURL: "https://x/a.png", LastFrameReferenceImageURL: "https://x/b.png"}
			got := ToViduCreateRequest(req)
			if got.Model != m {
				t.Errorf("model = %q, want %q (no prefix-stripping/prepending)", got.Model, m)
			}
		})
	}
}

func TestNormalizeViduResolution(t *testing.T) {
	tests := []struct {
		size string
		want string
	}{
		{"540P", "540P"},
		{"720p", "720P"},
		{"1080P", "1080P"},
		{"960x540", "540P"},
		{"1280x720", "720P"},
		{"1920x1080", "1080P"},
		{"", "720P"},        // absent -> vendor default
		{"garbage", "720P"}, // unmappable -> vendor default
	}
	for _, tt := range tests {
		t.Run(tt.size, func(t *testing.T) {
			if got := normalizeViduResolution(tt.size); got != tt.want {
				t.Errorf("normalizeViduResolution(%q) = %q, want %q", tt.size, got, tt.want)
			}
		})
	}
}

// TestFromViduCreateResponse_EncodesID pins the fix for a real gap found
// during a main-branch merge: Vidu shares the same DashScope-family
// transport/task-tracking as DashScope itself, whose task_id needed
// EncodeJobID (a canonical UUID, over budget once the published contract's
// tag is added) — Vidu's own create response must go through the same
// encoding, not publish its vendor task_id verbatim the way it did before
// this fix.
func TestFromViduCreateResponse_EncodesID(t *testing.T) {
	got, err := FromViduCreateResponse(
		CreateVideoRequest{Model: ModelViduQ3Turbo, Prompt: "a cat", Seconds: "5", Size: "1280x720"},
		vidu.CreateResponse{Output: vidu.CreateOutput{TaskID: "task-123", TaskStatus: vidu.TaskStatusPending}},
	)
	if err != nil {
		t.Fatalf("FromViduCreateResponse: %v", err)
	}
	// The published id is the ENCODED form — the vendor's task_id is ours to
	// shape, because consumers persist and key on what we hand out (see
	// EncodeJobID).
	if got.ID != "v0_task-123" || got.Status != StatusQueued {
		t.Fatalf("id/status = %q/%q, want v0_task-123/%q", got.ID, got.Status, StatusQueued)
	}
}

// TestFromViduCreateResponse_UUIDTaskIDCompacted covers the shape DashScope's
// own task_id actually takes (a canonical UUID) — Vidu, on the same
// DashScope-family platform, is expected to mint ids the same way.
func TestFromViduCreateResponse_UUIDTaskIDCompacted(t *testing.T) {
	got, err := FromViduCreateResponse(
		CreateVideoRequest{Model: ModelViduQ3Turbo, Prompt: "a cat", Seconds: "5"},
		vidu.CreateResponse{Output: vidu.CreateOutput{TaskID: "0385dc79-5ff8-4073-9d5a-1a7bc7f3e01d"}},
	)
	if err != nil {
		t.Fatalf("FromViduCreateResponse: %v", err)
	}
	if got.ID != "v1_0385dc795ff840739d5a1a7bc7f3e01d" {
		t.Errorf("id = %q, want the compacted-UUID (v1_) encoding", got.ID)
	}
}

func TestFromViduGetTaskResponse_DurationPrecedence(t *testing.T) {
	// usage.duration (billed) must win over usage.output_video_duration
	// (clip length) — simulating audio-overhead rounding where they diverge.
	resp := vidu.GetTaskResponse{
		Output: vidu.TaskOutput{TaskID: "t1", TaskStatus: vidu.TaskStatusSucceeded},
		Usage:  &vidu.TaskUsage{Duration: "6", OutputVideoDuration: "5", SR: "540"},
	}
	got := FromViduGetTaskResponse("v0_t1", resp)
	if got.Usage == nil {
		t.Fatal("usage missing from response")
	}
	if got.Usage.OutputVideoDuration.String() != "6" {
		t.Errorf("billed duration = %s, want 6 (usage.duration, not usage.output_video_duration=5)", got.Usage.OutputVideoDuration)
	}
}

func TestFromViduGetTaskResponse_DurationFallback(t *testing.T) {
	// usage.duration absent -> falls back to usage.output_video_duration
	// rather than skipping the bill entirely.
	resp := vidu.GetTaskResponse{
		Output: vidu.TaskOutput{TaskID: "t1", TaskStatus: vidu.TaskStatusSucceeded},
		Usage:  &vidu.TaskUsage{OutputVideoDuration: "5"},
	}
	got := FromViduGetTaskResponse("v0_t1", resp)
	if got.Usage == nil || got.Usage.OutputVideoDuration.String() != "5" {
		t.Errorf("expected fallback to output_video_duration=5, got %+v", got.Usage)
	}
}

func TestFromViduGetTaskResponse_ResolutionFromSR(t *testing.T) {
	resp := vidu.GetTaskResponse{
		Output: vidu.TaskOutput{TaskID: "t1", TaskStatus: vidu.TaskStatusSucceeded},
		Usage:  &vidu.TaskUsage{Duration: "5", SR: "540"},
	}
	got := FromViduGetTaskResponse("v0_t1", resp)
	if got.Size != "540P" {
		t.Errorf("Size = %q, want 540P (derived from usage.SR + \"P\")", got.Size)
	}
}

func TestFromViduGetTaskResponse_UnknownIsTerminalFailed(t *testing.T) {
	resp := vidu.GetTaskResponse{Output: vidu.TaskOutput{TaskID: "t1", TaskStatus: vidu.TaskStatusUnknown}}
	got := FromViduGetTaskResponse("v0_t1", resp)
	if got.Status != StatusFailed {
		t.Errorf("status = %q, want failed (UNKNOWN must be terminal, never retried)", got.Status)
	}
	if got.Error == nil || got.Error.Code != "vidu_task_unknown" {
		t.Errorf("error = %+v, want vidu_task_unknown", got.Error)
	}
}

func TestFromViduGetTaskResponse_FlatFailureShape(t *testing.T) {
	// Structurally identical to Kling's confirmed flat query-time failure
	// shape — no output.task_status, top-level code/message instead.
	resp := vidu.GetTaskResponse{Code: "InvalidParameter", Message: "bad request"}
	got := FromViduGetTaskResponse("v0_t1", resp)
	if got.Status != StatusFailed {
		t.Errorf("status = %q, want failed", got.Status)
	}
	if got.Error == nil || got.Error.Code != "InvalidParameter" {
		t.Errorf("error = %+v, want InvalidParameter", got.Error)
	}
}

func TestIsRecognizedViduStatus(t *testing.T) {
	recognized := []string{vidu.TaskStatusPending, vidu.TaskStatusRunning, vidu.TaskStatusSucceeded, vidu.TaskStatusFailed, vidu.TaskStatusCanceled, vidu.TaskStatusUnknown}
	for _, s := range recognized {
		if !IsRecognizedViduStatus(s) {
			t.Errorf("IsRecognizedViduStatus(%q) = false, want true", s)
		}
	}
	if IsRecognizedViduStatus("SOME_FUTURE_STATUS") {
		t.Error("IsRecognizedViduStatus should be false for an undocumented status")
	}
}
