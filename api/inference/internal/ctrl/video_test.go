package ctrl

import (
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
// GetVideoGenerationInputFeeAndOutputCount
// ==========================================================================

func TestGetVideoGenerationInputFeeAndOutputCount(t *testing.T) {
	ctrl := &Ctrl{logger: &testAsyncLoggerImpl{}}

	tests := []struct {
		name            string
		reqBody         []byte
		wantInputFee    string
		wantOutputCount int64
	}{
		{
			name:            "8 seconds, default size (720x1280, ratio 1.0)",
			reqBody:         multipartBody(map[string]string{"model": "sora-2", "prompt": "A cat", "seconds": "8"}),
			wantInputFee:    "0",
			wantOutputCount: 8, // 8 × 1.0 = 8
		},
		{
			name:            "8 seconds, high-res size (1024x1792, ratio 2.0)",
			reqBody:         multipartBody(map[string]string{"model": "sora-2", "prompt": "A cat", "seconds": "8", "size": "1024x1792"}),
			wantInputFee:    "0",
			wantOutputCount: 16, // 8 × 2.0 = 16
		},
		{
			name:            "landscape HD (1280x720, ratio 1.0)",
			reqBody:         multipartBody(map[string]string{"seconds": "10", "size": "1280x720"}),
			wantInputFee:    "0",
			wantOutputCount: 10, // 10 × 1.0 = 10
		},
		{
			name:            "wide resolution (1792x1024, ratio 2.0)",
			reqBody:         multipartBody(map[string]string{"seconds": "5", "size": "1792x1024"}),
			wantInputFee:    "0",
			wantOutputCount: 10, // 5 × 2.0 = 10
		},
		{
			name:            "no seconds field (defaults to 10)",
			reqBody:         multipartBody(map[string]string{"model": "sora-2", "prompt": "A cat"}),
			wantInputFee:    "0",
			wantOutputCount: 10, // 10 × 1.0 = 10
		},
		{
			name:            "unknown size falls back to ratio 1.0",
			reqBody:         multipartBody(map[string]string{"seconds": "8", "size": "4096x2160"}),
			wantInputFee:    "0",
			wantOutputCount: 8, // 8 × 1.0 = 8
		},
		{
			name:            "empty body (defaults: 10 seconds, 720x1280)",
			reqBody:         []byte{},
			wantInputFee:    "0",
			wantOutputCount: 10, // 10 × 1.0 = 10
		},
		{
			name:            "nil body",
			reqBody:         nil,
			wantInputFee:    "0",
			wantOutputCount: 10,
		},
		{
			name:            "only seconds, no size",
			reqBody:         multipartBody(map[string]string{"seconds": "20"}),
			wantInputFee:    "0",
			wantOutputCount: 20, // 20 × 1.0 = 20
		},
		{
			name:            "invalid seconds value uses default",
			reqBody:         multipartBody(map[string]string{"seconds": "abc"}),
			wantInputFee:    "0",
			wantOutputCount: 10, // default 10 × 1.0 = 10
		},
		{
			name:            "zero seconds uses default",
			reqBody:         multipartBody(map[string]string{"seconds": "0"}),
			wantInputFee:    "0",
			wantOutputCount: 10, // default 10 × 1.0 = 10
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inputFee, outputCount, err := ctrl.GetVideoGenerationInputFeeAndOutputCount(tt.reqBody)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if inputFee != tt.wantInputFee {
				t.Errorf("inputFee = %v, want %v", inputFee, tt.wantInputFee)
			}
			if outputCount != tt.wantOutputCount {
				t.Errorf("outputCount = %v, want %v", outputCount, tt.wantOutputCount)
			}
		})
	}
}

func TestGetVideoGenerationInputFeeAndOutputCount_CustomRatios(t *testing.T) {
	ctrl := &Ctrl{logger: &testAsyncLoggerImpl{}}
	ctrl.Service.ModelInfo = &config.ModelInfo{
		VideoSizeRatios: map[string]float64{
			"720x1280":  1.0,
			"1280x720":  1.5, // custom: landscape costs more
			"1024x1792": 3.0, // custom: tall costs 3x
		},
	}

	tests := []struct {
		name            string
		reqBody         []byte
		wantOutputCount int64
	}{
		{
			name:            "custom ratio landscape 1.5x",
			reqBody:         multipartBody(map[string]string{"seconds": "10", "size": "1280x720"}),
			wantOutputCount: 15, // 10 × 1.5 = 15
		},
		{
			name:            "custom ratio tall 3.0x",
			reqBody:         multipartBody(map[string]string{"seconds": "8", "size": "1024x1792"}),
			wantOutputCount: 24, // 8 × 3.0 = 24
		},
		{
			name:            "unknown size falls back to 1.0",
			reqBody:         multipartBody(map[string]string{"seconds": "6", "size": "unknown"}),
			wantOutputCount: 6, // 6 × 1.0 = 6
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, outputCount, err := ctrl.GetVideoGenerationInputFeeAndOutputCount(tt.reqBody)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if outputCount != tt.wantOutputCount {
				t.Errorf("outputCount = %v, want %v", outputCount, tt.wantOutputCount)
			}
		})
	}
}

// ==========================================================================
// parseVideoSecondsAndSize
// ==========================================================================

func TestParseVideoSecondsAndSize(t *testing.T) {
	tests := []struct {
		name        string
		reqBody     []byte
		wantSeconds int64
		wantSize    string
	}{
		{
			name:        "both fields present",
			reqBody:     multipartBody(map[string]string{"seconds": "16", "size": "1024x1792"}),
			wantSeconds: 16,
			wantSize:    "1024x1792",
		},
		{
			name:        "missing seconds",
			reqBody:     multipartBody(map[string]string{"size": "1280x720"}),
			wantSeconds: 10,
			wantSize:    "1280x720",
		},
		{
			name:        "missing size",
			reqBody:     multipartBody(map[string]string{"seconds": "5"}),
			wantSeconds: 5,
			wantSize:    "720x1280",
		},
		{
			name:        "missing both",
			reqBody:     multipartBody(map[string]string{"model": "sora-2"}),
			wantSeconds: 10,
			wantSize:    "720x1280",
		},
		{
			name:        "invalid seconds",
			reqBody:     multipartBody(map[string]string{"seconds": "abc"}),
			wantSeconds: 10,
			wantSize:    "720x1280",
		},
		{
			name:        "zero seconds",
			reqBody:     multipartBody(map[string]string{"seconds": "0"}),
			wantSeconds: 10,
			wantSize:    "720x1280",
		},
		{
			name:        "negative seconds",
			reqBody:     multipartBody(map[string]string{"seconds": "-5"}),
			wantSeconds: 10,
			wantSize:    "720x1280",
		},
		{
			name:        "empty body",
			reqBody:     []byte{},
			wantSeconds: 10,
			wantSize:    "720x1280",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			seconds, size := parseVideoSecondsAndSize(tt.reqBody)
			if seconds != tt.wantSeconds {
				t.Errorf("seconds = %d, want %d", seconds, tt.wantSeconds)
			}
			if size != tt.wantSize {
				t.Errorf("size = %s, want %s", size, tt.wantSize)
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

