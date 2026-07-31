package translate

import (
	"strconv"
	"strings"
	"testing"

	"github.com/0glabs/0g-serving-broker/videotranslator/internal/minimax"
)

func TestToMiniMaxCreateRequest_InputReference(t *testing.T) {
	// helper: find the first_frame image content item, if any
	firstFrame := func(req minimax.CreateRequest) *minimax.ContentItem {
		for i := range req.Content {
			if req.Content[i].Role == "first_frame" {
				return &req.Content[i]
			}
		}
		return nil
	}

	t.Run("no reference → text-only content (plain T2V)", func(t *testing.T) {
		got := ToMiniMaxCreateRequest(CreateVideoRequest{Prompt: "a cat", Seconds: "5"}, "2K")
		if len(got.Content) != 1 || got.Content[0].Type != "text" {
			t.Fatalf("want single text item, got %+v", got.Content)
		}
	})

	t.Run("image_url → first_frame image item (used verbatim)", func(t *testing.T) {
		got := ToMiniMaxCreateRequest(CreateVideoRequest{
			Prompt: "animate", Seconds: "5", InputReferenceImageURL: "https://cdn/x.png",
		}, "2K")
		ff := firstFrame(got)
		if ff == nil || ff.Type != "image_url" || ff.ImageURL == nil || ff.ImageURL.URL != "https://cdn/x.png" {
			t.Fatalf("want first_frame image_url https://cdn/x.png, got %+v", got.Content)
		}
	})

	t.Run("file_id → mm_file:// first_frame handle", func(t *testing.T) {
		got := ToMiniMaxCreateRequest(CreateVideoRequest{
			Prompt: "animate", Seconds: "5", InputReferenceFileID: "abc123",
		}, "2K")
		ff := firstFrame(got)
		if ff == nil || ff.ImageURL == nil || ff.ImageURL.URL != "mm_file://abc123" {
			t.Fatalf("want first_frame mm_file://abc123, got %+v", got.Content)
		}
	})

	t.Run("mm_file:// in image_url is rejected (shared vendor file namespace)", func(t *testing.T) {
		got := ToMiniMaxCreateRequest(CreateVideoRequest{
			Prompt: "animate", Seconds: "5", InputReferenceImageURL: "mm_file://someone-elses-file",
		}, "2K")
		if ff := firstFrame(got); ff != nil {
			t.Fatalf("mm_file:// image_url must be dropped, got %+v", ff)
		}
	})

	t.Run("non-image scheme rejected, degrades to T2V", func(t *testing.T) {
		for _, u := range []string{"file:///etc/passwd", "data:text/html;base64,PHNjcmlwdD4=", "/local/path.png"} {
			if ff := firstFrame(ToMiniMaxCreateRequest(CreateVideoRequest{Prompt: "p", Seconds: "5", InputReferenceImageURL: u}, "2K")); ff != nil {
				t.Errorf("%q must be dropped, got %+v", u, ff)
			}
		}
	})

	t.Run("data:image URI accepted (multipart upload path)", func(t *testing.T) {
		got := ToMiniMaxCreateRequest(CreateVideoRequest{
			Prompt: "p", Seconds: "5", InputReferenceImageURL: "data:image/png;base64,iVBORw0KGgo=",
		}, "2K")
		if ff := firstFrame(got); ff == nil || !strings.HasPrefix(ff.ImageURL.URL, "data:image/png") {
			t.Fatalf("data:image URI should be kept, got %+v", ff)
		}
	})

	t.Run("image_url wins over file_id", func(t *testing.T) {
		got := ToMiniMaxCreateRequest(CreateVideoRequest{
			Prompt: "animate", Seconds: "5", InputReferenceImageURL: "https://cdn/x.png", InputReferenceFileID: "abc123",
		}, "2K")
		ff := firstFrame(got)
		if ff == nil || ff.ImageURL.URL != "https://cdn/x.png" {
			t.Fatalf("image_url should win, got %+v", ff)
		}
	})
}

func TestStatusFromMiniMax(t *testing.T) {
	tests := []struct {
		name   string
		status string
		want   string
	}{
		{"queued maps to queued", minimax.TaskStatusQueued, StatusQueued},
		{"running maps to in_progress", minimax.TaskStatusRunning, StatusInProgress},
		{"succeeded maps to completed", minimax.TaskStatusSucceeded, StatusCompleted},
		{"failed maps to failed", minimax.TaskStatusFailed, StatusFailed},
		{"cancelled maps to failed", minimax.TaskStatusCancelled, StatusFailed},
		{"expired maps to failed", minimax.TaskStatusExpired, StatusFailed},
		{"unrecognized status defaults to failed", "SOME_NEW_STATUS", StatusFailed},
		{"empty status defaults to failed", "", StatusFailed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := StatusFromMiniMax(tt.status); got != tt.want {
				t.Errorf("StatusFromMiniMax(%q) = %q, want %q", tt.status, got, tt.want)
			}
		})
	}
}

func TestIsRecognizedMiniMaxStatus(t *testing.T) {
	for _, s := range []string{
		minimax.TaskStatusQueued, minimax.TaskStatusRunning, minimax.TaskStatusSucceeded,
		minimax.TaskStatusFailed, minimax.TaskStatusCancelled, minimax.TaskStatusExpired,
	} {
		if !IsRecognizedMiniMaxStatus(s) {
			t.Errorf("IsRecognizedMiniMaxStatus(%q) = false, want true", s)
		}
	}
	for _, s := range []string{"", "SUCCEEDED", "unknown"} {
		if IsRecognizedMiniMaxStatus(s) {
			t.Errorf("IsRecognizedMiniMaxStatus(%q) = true, want false", s)
		}
	}
}

func TestSizeToMiniMaxRatio(t *testing.T) {
	tests := []struct {
		size string
		want string
	}{
		{"1280x720", "16:9"},
		{"720x1280", "9:16"},
		{"1024x1024", "1:1"},
		{"1200x900", "4:3"},
		{"", ""},
		{"garbage", ""},
		{"2K", ""}, // a resolution token, not pixel dims
	}
	for _, tt := range tests {
		t.Run(tt.size, func(t *testing.T) {
			if got := sizeToMiniMaxRatio(tt.size); got != tt.want {
				t.Errorf("sizeToMiniMaxRatio(%q) = %q, want %q", tt.size, got, tt.want)
			}
		})
	}
}

func TestNormalizeMiniMaxResolution(t *testing.T) {
	tests := []struct {
		size string
		want string
	}{
		{"2k", "2K"},
		{"2K", "2K"},
		{" 1080p ", "1080P"},
		{"768P", "768P"},
		{"1280x720", ""}, // pixel dims are not a resolution token
		{"", ""},
	}
	for _, tt := range tests {
		t.Run(tt.size, func(t *testing.T) {
			if got := normalizeMiniMaxResolution(tt.size); got != tt.want {
				t.Errorf("normalizeMiniMaxResolution(%q) = %q, want %q", tt.size, got, tt.want)
			}
		})
	}
}

func TestToMiniMaxCreateRequest(t *testing.T) {
	t.Run("pixel size sets ratio, resolution stays the default", func(t *testing.T) {
		got := ToMiniMaxCreateRequest(CreateVideoRequest{
			Model: "MiniMax-H3", Prompt: "a cat", Seconds: "5", Size: "1280x720",
		}, "2K")
		if got.Model != "MiniMax-H3" || got.Resolution != "2K" || got.Ratio != "16:9" || got.Duration != 5 {
			t.Fatalf("unexpected request: %+v", got)
		}
		if len(got.Content) != 1 || got.Content[0].Type != "text" || got.Content[0].Text != "a cat" {
			t.Fatalf("unexpected content: %+v", got.Content)
		}
	})

	t.Run("resolution token in size overrides the default, ratio falls to the t2v default", func(t *testing.T) {
		got := ToMiniMaxCreateRequest(CreateVideoRequest{Model: "m", Seconds: "6", Size: "1080p"}, "2K")
		if got.Resolution != "1080P" || got.Ratio != defaultMiniMaxRatio {
			t.Fatalf("unexpected resolution/ratio: %+v", got)
		}
	})

	// The outage this defends against: H3 rejects a text-only request with no
	// ratio ("ratio is required for t2va ... and cannot be 'adaptive'"), and the
	// shipped config publishes defaultParameters.size = "2K" — a resolution
	// token, which carries no aspect ratio. So the DEFAULT request shape sent no
	// ratio and 400'd, and the service could not serve at all. Every text-only
	// shape must leave here with one.
	t.Run("every text-only request carries a ratio", func(t *testing.T) {
		for _, size := range []string{"", "2K", "1080p", "768P", "not-a-size", "0x720"} {
			got := ToMiniMaxCreateRequest(CreateVideoRequest{Model: "MiniMax-H3", Prompt: "a cat", Seconds: "4", Size: size}, "2K")
			if got.Ratio == "" {
				t.Errorf("size=%q produced no ratio — H3 rejects a t2v request without one", size)
			}
		}
	})

	// ...but NOT when a first frame is supplied: there the ratio follows the
	// image, and a pixel size the client happened to send is the only reason to
	// state one.
	t.Run("image-to-video with no derivable ratio sends none", func(t *testing.T) {
		got := ToMiniMaxCreateRequest(CreateVideoRequest{
			Model: "MiniMax-H3", Prompt: "a cat", Seconds: "4", Size: "2K",
			InputReferenceImageURL: "https://example.com/frame.png",
		}, "2K")
		if got.Ratio != "" {
			t.Errorf("Ratio = %q, want none — the first frame defines it", got.Ratio)
		}
	})

	t.Run("fractional seconds round up", func(t *testing.T) {
		if got := ToMiniMaxCreateRequest(CreateVideoRequest{Seconds: "7.2"}, "2K"); got.Duration != 8 {
			t.Errorf("Duration = %d, want 8", got.Duration)
		}
	})

	t.Run("invalid/absent seconds defaults to H3 minimum (5)", func(t *testing.T) {
		for _, s := range []string{"", "0", "-3", "abc"} {
			if got := ToMiniMaxCreateRequest(CreateVideoRequest{Seconds: s}, "2K"); got.Duration != minMiniMaxDuration {
				t.Errorf("Seconds=%q: Duration = %d, want %d", s, got.Duration, minMiniMaxDuration)
			}
		}
	})

	t.Run("clamps into H3's [4,15] range (OpenAI default 4 passes through, oversized → 15)", func(t *testing.T) {
		// 4 is OpenAI's default and inside H3's range, so it must NOT be rounded up:
		// billing is on generated seconds, so clamping up would over-bill the most
		// common request shape.
		if got := ToMiniMaxCreateRequest(CreateVideoRequest{Seconds: "4"}, "2K"); got.Duration != 4 {
			t.Errorf("seconds=4 → Duration %d, want 4 (H3 floor)", got.Duration)
		}
		if got := ToMiniMaxCreateRequest(CreateVideoRequest{Seconds: "20"}, "2K"); got.Duration != 15 {
			t.Errorf("seconds=20 → Duration %d, want 15 (H3 ceil)", got.Duration)
		}
		if got := ToMiniMaxCreateRequest(CreateVideoRequest{Seconds: "12"}, "2K"); got.Duration != 12 {
			t.Errorf("seconds=12 → Duration %d, want 12 (in range)", got.Duration)
		}
	})
}

func TestFromMiniMaxCreateResponse_AlwaysQueued(t *testing.T) {
	// Load-bearing: the create response has no status, but the broker must see
	// "queued" (defer-to-poll), never absent (which it reads as a synchronous
	// completion and mis-bills on requested duration).
	got, err := FromMiniMaxCreateResponse(
		CreateVideoRequest{Model: "MiniMax-H3", Prompt: "p", Seconds: "5", Size: "1280x720"},
		minimax.CreateResponse{TaskID: "task-123"},
	)
	if err != nil {
		t.Fatalf("FromMiniMaxCreateResponse: %v", err)
	}
	// The published id is the ENCODED form — the vendor's task_id is ours to shape,
	// because consumers persist and key on what we hand out (see EncodeJobID).
	if got.ID != "v0_task-123" || got.Status != StatusQueued {
		t.Fatalf("id/status = %q/%q, want v0_task-123/%q", got.ID, got.Status, StatusQueued)
	}
	if got.Seconds != "5" || got.Size != "1280x720" || got.Prompt != "p" {
		t.Fatalf("echoed fields wrong: %+v", got)
	}
}

func TestFromMiniMaxGetTaskResponse(t *testing.T) {
	t.Run("succeeded maps total_seconds to output_video_duration and resolution to size", func(t *testing.T) {
		got := FromMiniMaxGetTaskResponse("v0_pub", minimax.GetTaskResponse{Task: &minimax.Task{
			ID:         "424010985738629",
			Status:     minimax.TaskStatusSucceeded,
			CreatedAt:  1785125529,
			Resolution: "2K",
			Content:    &minimax.TaskContent{URL: "https://cdn/output.mp4"},
			Usage:      &minimax.TaskUsage{TotalSeconds: "5", InputSeconds: "0", OutputSeconds: "5"},
		}})
		if got.Status != StatusCompleted {
			t.Fatalf("status = %q, want completed", got.Status)
		}
		if got.Usage == nil || got.Usage.OutputVideoDuration.String() != "5" {
			t.Fatalf("output_video_duration wrong: %+v", got.Usage)
		}
		if got.Size != "2K" {
			t.Errorf("size = %q, want 2K", got.Size)
		}
		if got.CreatedAt != 1785125529 || got.ExpiresAt != 1785125529+miniMaxTaskValiditySeconds {
			t.Errorf("created/expires = %d/%d", got.CreatedAt, got.ExpiresAt)
		}
	})

	t.Run("prefers total_seconds over output_seconds for reference-video billing", func(t *testing.T) {
		got := FromMiniMaxGetTaskResponse("v0_pub", minimax.GetTaskResponse{Task: &minimax.Task{
			Status: minimax.TaskStatusSucceeded,
			Usage:  &minimax.TaskUsage{TotalSeconds: "12", InputSeconds: "7", OutputSeconds: "5"},
		}})
		if got.Usage == nil || got.Usage.OutputVideoDuration.String() != "12" {
			t.Fatalf("want total_seconds 12, got %+v", got.Usage)
		}
	})

	t.Run("failed carries the vendor error", func(t *testing.T) {
		got := FromMiniMaxGetTaskResponse("v0_pub", minimax.GetTaskResponse{Task: &minimax.Task{
			Status: minimax.TaskStatusFailed,
			Error:  &minimax.TaskError{Code: "1027", Message: "content risk"},
		}})
		if got.Status != StatusFailed || got.Error == nil || got.Error.Code != "1027" {
			t.Fatalf("unexpected: %+v / %+v", got, got.Error)
		}
	})

	t.Run("cancelled/expired synthesize an error when the vendor gave none", func(t *testing.T) {
		for _, s := range []string{minimax.TaskStatusCancelled, minimax.TaskStatusExpired} {
			got := FromMiniMaxGetTaskResponse("v0_pub", minimax.GetTaskResponse{Task: &minimax.Task{Status: s}})
			if got.Status != StatusFailed || got.Error == nil || got.Error.Message == "" {
				t.Fatalf("status %q: want failed with synthesized error, got %+v", s, got.Error)
			}
		}
	})

	t.Run("nil task is a terminal failure, not a hang", func(t *testing.T) {
		got := FromMiniMaxGetTaskResponse("v0_pub", minimax.GetTaskResponse{})
		if got.Status != StatusFailed || got.Error == nil {
			t.Fatalf("want failed with error, got %+v", got)
		}
	})

	t.Run("no positive usage omits the usage block", func(t *testing.T) {
		got := FromMiniMaxGetTaskResponse("v0_pub", minimax.GetTaskResponse{Task: &minimax.Task{
			Status: minimax.TaskStatusRunning,
			Usage:  &minimax.TaskUsage{TotalSeconds: "0", OutputSeconds: "0"},
		}})
		if got.Usage != nil {
			t.Fatalf("want nil usage, got %+v", got.Usage)
		}
	})
}

// TestDurationIsNeverClampedUpwards pins the billing-relevant half of the clamp.
// The broker bills the seconds the vendor reports GENERATING, so raising a
// caller's requested duration raises their bill above what they asked for. Only
// clamping DOWN is safe in that respect, and it is bounded by what they requested.
func TestDurationIsNeverClampedUpwards(t *testing.T) {
	for _, seconds := range []string{"4", "5", "8", "12", "15"} {
		req := ToMiniMaxCreateRequest(CreateVideoRequest{Seconds: seconds}, "2K")
		want, _ := strconv.ParseInt(seconds, 10, 64)
		if req.Duration != want {
			t.Errorf("Seconds=%q sent Duration=%d — a value inside H3's range must go through untouched, or the caller is billed for seconds they did not ask for", seconds, req.Duration)
		}
	}

	// Above the ceiling is the one case that changes the value, and it can only
	// lower it.
	if req := ToMiniMaxCreateRequest(CreateVideoRequest{Seconds: "20"}, "2K"); req.Duration != maxMiniMaxDuration {
		t.Errorf("Seconds=20 sent Duration=%d, want %d", req.Duration, maxMiniMaxDuration)
	}

	// The two residuals that DO bill above the request, pinned so they stay a stated
	// trade-off rather than being rediscovered as a bug. Neither is reachable from a
	// conforming OpenAI client, whose seconds enum is {4,8,12}.
	for _, tc := range []struct {
		seconds string
		want    int64
		why     string
	}{
		{"3", 4, "below H3's floor: unsatisfiable, so the caller gets and pays for the 4s minimum"},
		{"0.5", 4, "ditto"},
		{"4.1", 5, "H3 takes an integer, so ceil is forced"},
		{"1e30", 15, "clamped to the ceiling before the int64 conversion, not wrapped below the floor"},
	} {
		if got := ToMiniMaxCreateRequest(CreateVideoRequest{Seconds: tc.seconds}, "2K").Duration; got != tc.want {
			t.Errorf("Seconds=%q sent Duration=%d, want %d (%s)", tc.seconds, got, tc.want, tc.why)
		}
	}
}
