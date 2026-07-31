package translate

import (
	"testing"

	"github.com/0glabs/0g-serving-broker/imagetranslator/internal/kling"
)

func TestValidateKlingCreateRequest_RejectsOutOfRangeCount(t *testing.T) {
	tests := []struct {
		name    string
		n       int
		wantErr bool
	}{
		{"n=1 valid", 1, false},
		{"n=9 valid (max)", 9, false},
		{"n=0 rejected", 0, true},
		{"n=10 rejected (not clamped to 9)", 10, true},
		{"n=15 rejected (not clamped to 9)", 15, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := CreateImageRequest{Model: ModelKlingV3ImageGeneration, Prompt: "x", N: tt.n}
			err := ValidateKlingCreateRequest(req)
			if (err != nil) != tt.wantErr {
				t.Errorf("err = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestValidateKlingResolution_RejectsNotClamps(t *testing.T) {
	tests := []struct {
		name       string
		resolution string
		wantErr    bool
	}{
		{"1k valid", "1k", false},
		{"2k valid", "2k", false},
		{"4k REJECTED (only valid for omni model, not registered in v1)", "4k", true},
		{"absent valid (falls to default)", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateKlingResolution(ModelKlingV3ImageGeneration, tt.resolution)
			if (err != nil) != tt.wantErr {
				t.Errorf("err = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestSizeToKlingAspectRatio(t *testing.T) {
	tests := []struct {
		size string
		want string
	}{
		{"1280x720", "16:9"},
		{"720x1280", "9:16"},
		{"1024x1024", "1:1"},
		{"", "16:9"},        // absent -> vendor default
		{"garbage", "16:9"}, // unmappable -> vendor default
	}
	for _, tt := range tests {
		t.Run(tt.size, func(t *testing.T) {
			if got := sizeToKlingAspectRatio(tt.size); got != tt.want {
				t.Errorf("sizeToKlingAspectRatio(%q) = %q, want %q", tt.size, got, tt.want)
			}
		})
	}
}

func TestNormalizeKlingResolution(t *testing.T) {
	tests := []struct {
		size string
		want string
	}{
		{"1280x720", "1k"},
		{"1920x1080", "2k"},
		{"", "1k"},        // absent -> vendor default
		{"garbage", "1k"}, // unmappable -> vendor default
	}
	for _, tt := range tests {
		t.Run(tt.size, func(t *testing.T) {
			if got := normalizeKlingResolution(tt.size); got != tt.want {
				t.Errorf("normalizeKlingResolution(%q) = %q, want %q", tt.size, got, tt.want)
			}
		})
	}
}

func TestIsAllowedKlingReferenceScheme(t *testing.T) {
	tests := []struct {
		url  string
		want bool
	}{
		{"https://example.com/a.png", true},
		{"http://example.com/a.png", true},
		{"data:image/png;base64,aGVsbG8=", false}, // no base64/data-URI form documented for Kling
		{"mm_file://abc", false},
		{"", false},
	}
	for _, tt := range tests {
		t.Run(tt.url, func(t *testing.T) {
			if got := isAllowedKlingReferenceScheme(tt.url); got != tt.want {
				t.Errorf("isAllowedKlingReferenceScheme(%q) = %v, want %v", tt.url, got, tt.want)
			}
		})
	}
}

func TestReferenceWasDroppedForScheme(t *testing.T) {
	tests := []struct {
		name string
		req  CreateImageRequest
		want bool
	}{
		{"no reference supplied", CreateImageRequest{}, false},
		{"http reference supplied, not dropped", CreateImageRequest{InputReferenceImageURL: "https://x/a.png"}, false},
		{"data: URI supplied, dropped", CreateImageRequest{InputReferenceImageURL: "data:image/png;base64,aGVsbG8="}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ReferenceWasDroppedForScheme(tt.req); got != tt.want {
				t.Errorf("ReferenceWasDroppedForScheme = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestFromKlingGetTaskResponse_EmptyChoicesGuard(t *testing.T) {
	// A SUCCEEDED status with an empty choices array must be treated as
	// transient (retry-worthy), never a panic, never a terminal failure.
	req := CreateImageRequest{Model: ModelKlingV3ImageGeneration, N: 2}
	resp := kling.GetTaskResponse{
		Output: kling.TaskOutput{TaskID: "t1", TaskStatus: kling.TaskStatusSucceeded, Choices: []kling.Choice{}},
	}
	got := FromKlingGetTaskResponse(req, resp)
	if !got.Transient {
		t.Errorf("expected Transient=true for empty-choices SUCCEEDED response, got %+v", got)
	}
}

func TestFromKlingGetTaskResponse_VendorPartialSuccess(t *testing.T) {
	req := CreateImageRequest{Model: ModelKlingV3ImageGeneration, N: 3}
	resp := kling.GetTaskResponse{
		Output: kling.TaskOutput{
			TaskID:     "t1",
			TaskStatus: kling.TaskStatusSucceeded,
			Choices: []kling.Choice{{
				Message: kling.ChoiceMessage{Content: []kling.ImageContentItem{
					{Type: "image", Image: "https://x/1.png"},
					{Type: "image", Image: "https://x/2.png"},
				}},
			}},
		},
		Usage: &kling.TaskUsage{ImageCount: 2},
	}
	got := FromKlingGetTaskResponse(req, resp)
	if got.Status != StatusFailed {
		t.Fatalf("status = %q, want failed (vendor delivered 2 of 3 requested images)", got.Status)
	}
	if got.Error == nil || got.Error.Code != "kling_vendor_partial_success" {
		t.Errorf("error = %+v, want kling_vendor_partial_success", got.Error)
	}
}

func TestFromKlingGetTaskResponse_FullSuccessCompletes(t *testing.T) {
	req := CreateImageRequest{Model: ModelKlingV3ImageGeneration, N: 2}
	resp := kling.GetTaskResponse{
		Output: kling.TaskOutput{
			TaskID:     "t1",
			TaskStatus: kling.TaskStatusSucceeded,
			Choices: []kling.Choice{{
				Message: kling.ChoiceMessage{Content: []kling.ImageContentItem{
					{Type: "image", Image: "https://x/1.png"},
					{Type: "image", Image: "https://x/2.png"},
				}},
			}},
		},
		Usage: &kling.TaskUsage{ImageCount: 2},
	}
	got := FromKlingGetTaskResponse(req, resp)
	if got.Status != StatusCompleted {
		t.Fatalf("status = %q, want completed", got.Status)
	}
	urls := GeneratedImageURLs(resp)
	if len(urls) != 2 || urls[0] != "https://x/1.png" || urls[1] != "https://x/2.png" {
		t.Errorf("urls = %v, want the two generated image URLs in order", urls)
	}
}

func TestFromKlingGetTaskResponse_FlatQueryTimeFailure(t *testing.T) {
	// Dossier 3's own verbatim "Task-level failure response" example: flat
	// top-level {code, message, request_id}, no output wrapper at all.
	req := CreateImageRequest{Model: ModelKlingV3ImageGeneration, N: 1}
	resp := kling.GetTaskResponse{Code: "InvalidParameter", Message: "num_images_per_prompt must be 1"}
	got := FromKlingGetTaskResponse(req, resp)
	if got.Status != StatusFailed {
		t.Fatalf("status = %q, want failed", got.Status)
	}
	if got.Transient {
		t.Error("flat query-time failure must NOT fall through to the transient-502 default")
	}
	if got.Error == nil || got.Error.Code != "InvalidParameter" {
		t.Errorf("error = %+v, want InvalidParameter", got.Error)
	}
}

func TestFromKlingGetTaskResponse_AmbiguousShapeIsTransient(t *testing.T) {
	req := CreateImageRequest{Model: ModelKlingV3ImageGeneration, N: 1}
	resp := kling.GetTaskResponse{} // no output.task_status, no top-level code
	got := FromKlingGetTaskResponse(req, resp)
	if !got.Transient {
		t.Errorf("expected Transient=true for a genuinely ambiguous response, got %+v", got)
	}
}

func TestFromKlingGetTaskResponse_UnknownIsTerminalFailed(t *testing.T) {
	req := CreateImageRequest{Model: ModelKlingV3ImageGeneration, N: 1}
	resp := kling.GetTaskResponse{Output: kling.TaskOutput{TaskID: "t1", TaskStatus: kling.TaskStatusUnknown}}
	got := FromKlingGetTaskResponse(req, resp)
	if got.Status != StatusFailed || got.Transient {
		t.Errorf("UNKNOWN must be a terminal failure, not transient/retried: got %+v", got)
	}
	if got.Error == nil || got.Error.Code != "kling_task_unknown" {
		t.Errorf("error = %+v, want kling_task_unknown", got.Error)
	}
}

func TestToKlingCreateRequest_ExactlyOneTextContentItem(t *testing.T) {
	req := CreateImageRequest{Model: ModelKlingV3ImageGeneration, Prompt: "a cat", N: 2}
	got := ToKlingCreateRequest(req)
	if len(got.Input.Messages) != 1 {
		t.Fatalf("messages length = %d, want 1", len(got.Input.Messages))
	}
	content := got.Input.Messages[0].Content
	textCount := 0
	for _, c := range content {
		if c.Text != nil {
			textCount++
		}
	}
	if textCount != 1 {
		t.Errorf("text content items = %d, want exactly 1 (vendor hard constraint)", textCount)
	}
}

func TestToKlingCreateRequest_ReferenceImageIncluded(t *testing.T) {
	req := CreateImageRequest{Model: ModelKlingV3ImageGeneration, Prompt: "x", N: 1, InputReferenceImageURL: "https://x/ref.png"}
	got := ToKlingCreateRequest(req)
	content := got.Input.Messages[0].Content
	found := false
	for _, c := range content {
		if c.Image != nil && *c.Image == "https://x/ref.png" {
			found = true
		}
	}
	if !found {
		t.Error("reference image not found in content array")
	}
}

func TestIsRecognizedKlingStatus(t *testing.T) {
	recognized := []string{kling.TaskStatusPending, kling.TaskStatusRunning, kling.TaskStatusSucceeded, kling.TaskStatusFailed, kling.TaskStatusCanceled, kling.TaskStatusUnknown}
	for _, s := range recognized {
		if !IsRecognizedKlingStatus(s) {
			t.Errorf("IsRecognizedKlingStatus(%q) = false, want true", s)
		}
	}
	if IsRecognizedKlingStatus("SOME_FUTURE_STATUS") {
		t.Error("IsRecognizedKlingStatus should be false for an undocumented status")
	}
}

func TestPromptExceedsVendorLimit(t *testing.T) {
	short := CreateImageRequest{Prompt: "a short prompt"}
	if PromptExceedsVendorLimit(short) {
		t.Error("short prompt should not exceed the limit")
	}
	long := CreateImageRequest{Prompt: stringOfLength(2501)}
	if !PromptExceedsVendorLimit(long) {
		t.Error("2501-rune prompt should exceed the 2500-character limit")
	}
}

func stringOfLength(n int) string {
	b := make([]rune, n)
	for i := range b {
		b[i] = 'a'
	}
	return string(b)
}
