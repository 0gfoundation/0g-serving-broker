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

// TestSeedanceVersionWarning confirms seedanceVersionWarning -- the source
// of SeedanceMain's else-branch warning log -- distinguishes "correctly
// empty, default to 2.5" (no warning) from "set but not a recognized 2.0
// spelling, silently falling back to 2.5" (warning), and stays silent for
// values isSeedance20 already accepts as 2.0.
func TestSeedanceVersionWarning(t *testing.T) {
	tests := []struct {
		version   string
		wantEmpty bool
	}{
		{"", true},      // correct default: nothing to warn about
		{"2.0", true},   // recognized 2.0 spelling: isSeedance20 handles it, no warning
		{"2", true},
		{"v2", true},
		{"v2.0", true},
		{" 2.0 ", true},
		{"2.5", false},          // non-empty and not a 2.0 spelling: likely-typo case
		{"2.0.0", false},        // the exact typo this finding was about
		{"seedance-2.0", false}, // another plausible fat-finger
		{"garbage", false},
		{"   ", true}, // whitespace-only trims to empty: treated as unset, not a typo
	}
	for _, tt := range tests {
		got := seedanceVersionWarning(tt.version)
		if gotEmpty := got == ""; gotEmpty != tt.wantEmpty {
			t.Errorf("seedanceVersionWarning(%q) = %q, want empty=%v", tt.version, got, tt.wantEmpty)
		}
	}
}
