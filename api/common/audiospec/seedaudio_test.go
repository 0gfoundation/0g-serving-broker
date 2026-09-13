package audiospec

import "testing"

func TestSeedAudioMaxOutputSeconds(t *testing.T) {
	if got := SeedAudio.MaxOutputSeconds(); got != 120 {
		t.Fatalf("MaxOutputSeconds() = %d, want 120 (Seed Audio 1.0's published per-request ceiling)", got)
	}
}

func TestSeedAudioReserveSeconds(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want int64
	}{
		{name: "a short request reserves what it asked for", raw: "10", want: 10},
		{name: "the ceiling itself", raw: "120", want: 120},

		// Rounded UP: max_duration bounds a quantity billed in whole seconds, so
		// reserving 12 for a 12.1-second bound would sit below a fee the same
		// request can legitimately produce.
		{name: "fractional rounds up", raw: "12.1", want: 13},
		{name: "a hair over a whole second still rounds up", raw: "1.0001", want: 2},
		{name: "a whole number expressed fractionally does not inflate", raw: "12.0", want: 12},

		// ceil() runs before the ceiling comparison. Comparing the raw float first
		// returns 119 here, under-reserving by exactly the rounding the fee applies.
		{name: "a fraction just under the ceiling reaches the ceiling", raw: "119.4", want: 120},

		// Above the ceiling the vendor caps the output, so the ceiling is still the
		// whole bound — it is not an error to ask for more, just not more audio.
		{name: "above the ceiling clamps down", raw: "300", want: 120},
		{name: "absurdly above the ceiling clamps down", raw: "1e300", want: 120},

		// Every unusable field means the same thing — the vendor picks the length —
		// so every one of them reserves the ceiling.
		{name: "absent", raw: "", want: 120},
		{name: "unreadable", raw: "abc", want: 120},
		{name: "unit suffix is unreadable", raw: "30s", want: 120},
		{name: "zero", raw: "0", want: 120},
		{name: "negative", raw: "-30", want: 120},
		{name: "NaN", raw: "NaN", want: 120},
		{name: "infinity", raw: "Inf", want: 120},

		{name: "whitespace is trimmed, not treated as absent", raw: "  30  ", want: 30},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := SeedAudio.ReserveSeconds(tt.raw); got != tt.want {
				t.Errorf("ReserveSeconds(%q) = %d, want %d", tt.raw, got, tt.want)
			}
		})
	}
}

// No floor is clamped. Seed Audio documents no minimum billable duration, and
// inventing one would hold funds for audio the vendor may never produce. This
// pins the absence so a later "surely at least N seconds" edit has to argue with
// a test rather than slip through.
func TestSeedAudioHasNoMinimumFloor(t *testing.T) {
	if got := SeedAudio.ReserveSeconds("1"); got != 1 {
		t.Fatalf("ReserveSeconds(\"1\") = %d, want 1 — a floor was introduced; Seed Audio documents no minimum billable duration", got)
	}
}

// The registry must resolve to this vendor's rules, not just hold them. A
// registration that never resolves leaves the gate degrading to unreserved
// forwarding while every file in the package looks correct.
func TestSeedAudioIsReachableThroughTheRegistry(t *testing.T) {
	spec, ok := Get(VendorSeedAudio)
	if !ok {
		t.Fatal("Get(VendorSeedAudio) found no rules")
	}
	if spec.MaxOutputSeconds() != SeedAudio.MaxOutputSeconds() {
		t.Error("the registry resolved to a different spec than the exported value")
	}
}
