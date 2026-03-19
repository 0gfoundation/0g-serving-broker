package ctrl

import (
	"encoding/json"
	"testing"

	"github.com/0glabs/0g-serving-broker/inference/config"
)

// multipartBody is a helper to build multipart/form-data body for tests.
func multipartBody(fields map[string]string) []byte {
	var body string
	for name, value := range fields {
		body += "--boundary\r\nContent-Disposition: form-data; name=\"" + name + "\"\r\n\r\n" + value + "\r\n"
	}
	body += "--boundary--"
	return []byte(body)
}

// ==========================================================================
// videoResponseFields JSON parsing (used by handleVideoGenerationResponse)
// ==========================================================================

func TestVideoResponseFieldsParsing(t *testing.T) {
	tests := []struct {
		name        string
		respJSON    string
		wantSeconds int64
		wantSize    string
		wantErr     bool
	}{
		{
			name:        "full response with seconds and size",
			respJSON:    `{"id":"video-001","status":"queued","seconds":8,"size":"1024x1792"}`,
			wantSeconds: 8,
			wantSize:    "1024x1792",
		},
		{
			name:        "seconds only, no size",
			respJSON:    `{"id":"video-001","status":"queued","seconds":5}`,
			wantSeconds: 5,
			wantSize:    "",
		},
		{
			name:        "seconds as string number",
			respJSON:    `{"id":"video-001","seconds":"10","size":"720x1280"}`,
			wantSeconds: 10,
			wantSize:    "720x1280",
		},
		{
			name:     "missing seconds field",
			respJSON: `{"id":"video-001","status":"queued","size":"720x1280"}`,
			wantErr:  true, // seconds will be 0, treated as invalid
		},
		{
			name:     "zero seconds",
			respJSON: `{"id":"video-001","seconds":0}`,
			wantErr:  true, // seconds <= 0 is invalid
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var fields videoResponseFields
			if err := json.Unmarshal([]byte(tt.respJSON), &fields); err != nil {
				t.Fatalf("unexpected unmarshal error: %v", err)
			}

			seconds, err := fields.Seconds.Int64()
			if tt.wantErr {
				if err == nil && seconds > 0 {
					t.Errorf("expected invalid seconds, got %d", seconds)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error parsing seconds: %v", err)
			}
			if seconds != tt.wantSeconds {
				t.Errorf("seconds = %d, want %d", seconds, tt.wantSeconds)
			}
			if fields.Size != tt.wantSize {
				t.Errorf("size = %q, want %q", fields.Size, tt.wantSize)
			}
		})
	}
}

// ==========================================================================
// parseVideoGenerationModel
// ==========================================================================

func TestParseVideoGenerationModel(t *testing.T) {
	tests := []struct {
		name    string
		reqBody []byte
		want    string
	}{
		{
			name:    "multipart with model field",
			reqBody: multipartBody(map[string]string{"model": "sora-2-pro", "prompt": "A cat"}),
			want:    "sora-2-pro",
		},
		{
			name:    "multipart without model field",
			reqBody: multipartBody(map[string]string{"prompt": "A cat"}),
			want:    "",
		},
		{
			name:    "empty body",
			reqBody: []byte{},
			want:    "",
		},
		{
			name:    "nil body",
			reqBody: nil,
			want:    "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseVideoGenerationModel(tt.reqBody)
			if got != tt.want {
				t.Errorf("parseVideoGenerationModel() = %q, want %q", got, tt.want)
			}
		})
	}
}

// ==========================================================================
// ensureMultipartWaitField
// ==========================================================================

func TestEnsureMultipartWaitField(t *testing.T) {
	tests := []struct {
		name     string
		reqBody  []byte
		wantWait string
	}{
		{
			name:     "no wait field — inserts wait=false",
			reqBody:  multipartBody(map[string]string{"model": "sora-2", "prompt": "A cat"}),
			wantWait: "false",
		},
		{
			name:     "wait=true already present — unchanged",
			reqBody:  multipartBody(map[string]string{"model": "sora-2", "wait": "true"}),
			wantWait: "true",
		},
		{
			name:     "wait=false already present — unchanged",
			reqBody:  multipartBody(map[string]string{"model": "sora-2", "wait": "false"}),
			wantWait: "false",
		},
		{
			name:     "empty body — returned as-is",
			reqBody:  []byte{},
			wantWait: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ensureMultipartWaitField(tt.reqBody)
			got := parseMultipartField(string(result), "wait")
			if got != tt.wantWait {
				t.Errorf("wait field = %q, want %q\nbody:\n%s", got, tt.wantWait, string(result))
			}
		})
	}
}

// ==========================================================================
// config.Service.GetVideoSizeRatio (delegates to ModelInfo.VideoSizeRatios)
// ==========================================================================

func TestGetVideoSizeRatio(t *testing.T) {
	tests := []struct {
		name         string
		customRatios map[string]float64
		size         string
		want         float64
	}{
		{"default 832x480", nil, "832x480", 0.5},
		{"default 480x832", nil, "480x832", 0.5},
		{"default 720x1280", nil, "720x1280", 1.0},
		{"default 1280x720", nil, "1280x720", 1.0},
		{"default 1024x1792", nil, "1024x1792", 2.0},
		{"default 1792x1024", nil, "1792x1024", 2.0},
		{"default unknown size", nil, "4096x2160", 1.0},
		{"custom ratio", map[string]float64{"720x1280": 1.0, "4k": 4.0}, "4k", 4.0},
		{"custom missing size", map[string]float64{"720x1280": 1.0}, "unknown", 1.0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := &config.Service{}
			if tt.customRatios != nil {
				svc.ModelInfo = &config.ModelInfo{VideoSizeRatios: tt.customRatios}
			}
			got := svc.GetVideoSizeRatio(tt.size)
			if got != tt.want {
				t.Errorf("GetVideoSizeRatio(%q) = %v, want %v", tt.size, got, tt.want)
			}
		})
	}
}
