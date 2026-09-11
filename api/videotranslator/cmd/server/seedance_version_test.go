package server

import "testing"

// TestIsSeedance20 pins the exact set of SEEDANCE_MODEL_VERSION spellings
// that select 2.0, and confirms everything else -- including the empty
// string, the pre-2.0 default -- falls through to 2.5.
func TestIsSeedance20(t *testing.T) {
	tests := []struct {
		version string
		want    bool
	}{
		{"", false}, // the default, unchanged since before 2.0 existed: 2.5
		{"2.5", false},
		{"2.0", true},
		{"2", true},
		{"v2", true},
		{"v2.0", true},
		{" 2.0 ", true}, // whitespace tolerated
		{"2.0.0", false},
		{"seedance-2.0", false},
		{"garbage", false},
	}
	for _, tt := range tests {
		if got := isSeedance20(tt.version); got != tt.want {
			t.Errorf("isSeedance20(%q) = %v, want %v", tt.version, got, tt.want)
		}
	}
}
