package translate

import (
	"testing"
)

func TestSeedanceWireModelV2(t *testing.T) {
	if got := seedanceWireModelV2(seedanceV2CanonicalModelID); got != seedanceV2DefaultWireModel {
		t.Errorf("canonical id should remap to 2.0's own wire id, got %q", got)
	}
	if got := seedanceWireModelV2("dreamina-seedance-2-0-fast-260128"); got != "dreamina-seedance-2-0-fast-260128" {
		t.Errorf("an already-correct wire id must pass through unchanged, got %q", got)
	}
	// The 2.5 canonical id must NOT remap through the 2.0 function -- proves
	// the two version's remaps are actually independent, not one shared
	// lookup that happens to also answer for 2.5.
	if got := seedanceWireModelV2(seedanceCanonicalModelID); got != seedanceCanonicalModelID {
		t.Errorf("2.5's canonical id must pass through seedanceWireModelV2 unchanged (no cross-version remap), got %q", got)
	}
}

func TestParseSeedanceDurationV2(t *testing.T) {
	tests := []struct {
		seconds string
		want    int64
	}{
		{"", 0},    // absent -> omit, vendor default
		{"abc", 0}, // unparsable -> omit
		{"5", 5},   // in range, passthrough
		{"4", 4},   // floor of range (same floor as 2.5)
		{"15", 15}, // 2.0's own ceiling
		{"1", 4},   // clamped UP into range
		// The case that actually distinguishes 2.0 from 2.5: parseSeedanceDuration
		// (2.5) would resolve "20" and "30" in-range (ceiling 30); 2.0 must clamp
		// both down to its own ceiling, 15.
		{"20", 15},
		{"30", 15},
		{"31", 15}, // 2.5's own live-confirmed boundary is irrelevant here
	}
	for _, tt := range tests {
		if got := parseSeedanceDurationV2(tt.seconds); got != tt.want {
			t.Errorf("parseSeedanceDurationV2(%q) = %d, want %d", tt.seconds, got, tt.want)
		}
	}
}

func TestNormalizeSeedanceResolutionV2(t *testing.T) {
	tests := []struct {
		size string
		want string
	}{
		{"480p", "480p"},
		{"720P", "720p"},
		{"1080p", "1080p"},
		// The case that actually distinguishes 2.0 from 2.5: 4k is a real,
		// forwarded tier for 2.0 (normalizeSeedanceResolution, 2.5, would
		// instead fall through to the 720p default for this exact input --
		// see TestNormalizeSeedanceResolution's "4K" case).
		{"4K", "4k"},
		{"3840x2160", "4k"},
		{"1920x1080", "1080p"},
		{"", "720p"},
		{"garbage", "720p"},
	}
	for _, tt := range tests {
		if got := normalizeSeedanceResolutionV2(tt.size); got != tt.want {
			t.Errorf("normalizeSeedanceResolutionV2(%q) = %q, want %q", tt.size, got, tt.want)
		}
	}
}

func TestToSeedanceV2CreateRequest(t *testing.T) {
	t.Run("output_format is never sent, even when the client supplies one", func(t *testing.T) {
		mp4 := "mp4"
		got := ToSeedanceV2CreateRequest(CreateVideoRequest{Prompt: "p", Seconds: "5", OutputFormat: &mp4})
		if got.OutputFormat != nil {
			t.Errorf("output_format must always be omitted for Seedance 2.0, got %+v", got.OutputFormat)
		}
	})

	t.Run("duration clamped into 2.0's own [4,15], not 2.5's [4,30]", func(t *testing.T) {
		got := ToSeedanceV2CreateRequest(CreateVideoRequest{Prompt: "p", Seconds: "100"})
		if got.Duration != 15 {
			t.Errorf("Duration = %d, want clamped to 2.0's ceiling 15", got.Duration)
		}
	})

	t.Run("4k resolution is forwarded, not downgraded", func(t *testing.T) {
		got := ToSeedanceV2CreateRequest(CreateVideoRequest{Prompt: "p", Seconds: "5", Size: "4k"})
		if got.Resolution != "4k" {
			t.Errorf("Resolution = %q, want 4k forwarded (2.0 serves it, unlike 2.5)", got.Resolution)
		}
	})

	t.Run("camera_fixed is still passed through unchanged (shared with 2.5)", func(t *testing.T) {
		trueVal := true
		got := ToSeedanceV2CreateRequest(CreateVideoRequest{Prompt: "p", Seconds: "5", CameraFixed: &trueVal})
		if got.CameraFixed == nil || *got.CameraFixed != true {
			t.Fatalf("camera_fixed not passed through, got %+v", got.CameraFixed)
		}
	})

	t.Run("watermark always forced off (shared with 2.5)", func(t *testing.T) {
		got := ToSeedanceV2CreateRequest(CreateVideoRequest{Prompt: "p", Seconds: "5"})
		if got.Watermark == nil || *got.Watermark != false {
			t.Errorf("watermark must always be forced off, got %+v", got.Watermark)
		}
	})

	t.Run("model remap: 2.0 canonical id -> 2.0 wire id", func(t *testing.T) {
		got := ToSeedanceV2CreateRequest(CreateVideoRequest{Model: "bytedance/seedance-2.0", Prompt: "p"})
		if got.Model != seedanceV2DefaultWireModel {
			t.Errorf("Model = %q, want %q", got.Model, seedanceV2DefaultWireModel)
		}
	})

	t.Run("first_frame image-to-video works the same as 2.5", func(t *testing.T) {
		got := ToSeedanceV2CreateRequest(CreateVideoRequest{
			Prompt: "animate", Seconds: "5", InputReferenceImageURL: "https://cdn/a.png",
		})
		if len(got.Content) != 2 {
			t.Fatalf("want 2 content items, got %+v", got.Content)
		}
		if got.Ratio != "adaptive" {
			t.Errorf("ratio = %q, want adaptive", got.Ratio)
		}
	})

	t.Run("asset:// first_frame is dropped, same as 2.5", func(t *testing.T) {
		got := ToSeedanceV2CreateRequest(CreateVideoRequest{
			Prompt: "p", Seconds: "5", InputReferenceImageURL: "asset://abc123",
		})
		if len(got.Content) != 1 {
			t.Fatalf("asset:// scheme must be dropped, got %+v", got.Content)
		}
	})
}

func TestValidateSeedanceV2CreateRequest(t *testing.T) {
	tests := []struct {
		name    string
		req     CreateVideoRequest
		wantErr bool
	}{
		{"text-only is valid", CreateVideoRequest{Prompt: "p"}, false},
		{"first_frame-only is valid", CreateVideoRequest{InputReferenceImageURL: "https://cdn/a.png"}, false},
		{"first_frame asset:// is rejected", CreateVideoRequest{InputReferenceImageURL: "asset://x"}, true},
		{"input_reference.file_id alone is rejected", CreateVideoRequest{InputReferenceFileID: "file-abc123"}, true},
		{"an absurd seconds magnitude is rejected, not clamped", CreateVideoRequest{Prompt: "p", Seconds: "1e30"}, true},
		// 40 is out of range for BOTH versions, but only 2.0's OWN ceiling (15)
		// governs the clamp -- this is still accepted (clamped, not rejected),
		// exactly as an out-of-range-for-2.5-too value would be.
		{"an out-of-2.0-range seconds is still accepted (clamped, not rejected)", CreateVideoRequest{Prompt: "p", Seconds: "40"}, false},
		{"an unreadable seconds is accepted (the vendor picks the length)", CreateVideoRequest{Prompt: "p", Seconds: "abc"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateSeedanceV2CreateRequest(tt.req)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateSeedanceV2CreateRequest(%+v) error = %v, wantErr %v", tt.req, err, tt.wantErr)
			}
		})
	}
}

// TestSeedanceV2AndV25AreIndependent is a sanity cross-check that the two
// request builders really do disagree where they should, using the SAME
// input -- proving createFn selection in the handler layer actually matters
// rather than both paths coincidentally producing the same wire request.
func TestSeedanceV2AndV25AreIndependent(t *testing.T) {
	req := CreateVideoRequest{Prompt: "p", Seconds: "20", Size: "4k"}

	v2 := ToSeedanceV2CreateRequest(req)
	v25 := ToSeedanceCreateRequest(req)

	if v2.Duration != 15 {
		t.Errorf("2.0 Duration = %d, want 15 (its own ceiling)", v2.Duration)
	}
	if v25.Duration != 20 {
		t.Errorf("2.5 Duration = %d, want 20 (in range for 2.5's [4,30])", v25.Duration)
	}
	if v2.Resolution != "4k" {
		t.Errorf("2.0 Resolution = %q, want 4k (served)", v2.Resolution)
	}
	if v25.Resolution == "4k" {
		t.Errorf("2.5 Resolution = %q, must NOT be 4k (2.5 does not serve it)", v25.Resolution)
	}
}
