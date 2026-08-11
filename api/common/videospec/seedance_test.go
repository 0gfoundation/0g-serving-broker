package videospec

import "testing"

// TestSeedanceNormalizeSeconds pins the duration rules the broker's gate and the
// translator now BOTH read, so a change to either has to change this first.
func TestSeedanceNormalizeSeconds(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		want    int64
		outcome SecondsOutcome
	}{
		{"in range", "8", 8, SecondsResolved},
		{"at the floor", "4", 4, SecondsResolved},
		{"at the ceiling", "30", 30, SecondsResolved},
		// Clamping is safe for this vendor in a way it is not for MiniMax:
		// billing is on the vendor's echoed token count, so neither clamp can move
		// the bill away from what was rendered.
		{"below the floor renders the floor", "1", 4, SecondsResolved},
		{"above the ceiling renders the ceiling", "31", 30, SecondsResolved},
		{"fractional ceils to the next whole second", "4.1", 5, SecondsResolved},
		{"fractional below the floor still floors", "0.5", 4, SecondsResolved},
		// An unreadable duration is OMITTED from the vendor call, so the vendor
		// picks (5s). Nothing here may invent that number — a caller must not be
		// able to price this request from these rules.
		{"absent lets the vendor choose", "", 0, SecondsVendorDecides},
		{"unparsable lets the vendor choose", "abc", 0, SecondsVendorDecides},
		{"zero lets the vendor choose", "0", 0, SecondsVendorDecides},
		{"negative lets the vendor choose", "-3", 0, SecondsVendorDecides},
		{"intelligent-duration sentinel is not exposed", "-1", 0, SecondsVendorDecides},
		// Past this the float→int64 conversion is implementation-defined, and a
		// floor would clamp the result UP — turning the most absurd request into
		// the shortest clip. Refused instead: clamping to 30 would hand the caller
		// this model's longest, most expensive clip for a request that asked for
		// no such thing.
		{"absurd magnitude is refused, not clamped", "1e30", 0, SecondsRejected},
		{"infinity is refused", "Inf", 0, SecondsRejected},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, outcome := Seedance.NormalizeSeconds(tt.raw)
			if got != tt.want || outcome != tt.outcome {
				t.Errorf("NormalizeSeconds(%q) = (%d, %v), want (%d, %v)",
					tt.raw, got, outcome, tt.want, tt.outcome)
			}
		})
	}
}

// TestSeedanceNormalizeSeconds_Trims pins this vendor's deliberate deviation from
// Spec's "pass raw UNTRIMMED" rule, which exists so a reading here matches the
// VENDOR-SIDE reader.
//
// Seedance's vendor-side reader is this integration's own translator: it parses
// "seconds" itself and sends the vendor an already-normalized integer, so the
// vendor never sees the client's string. That translator trims, which makes
// trimming here the thing that keeps the two in agreement — a padded value
// resolving to SecondsVendorDecides here while the translator sent 8 would be
// exactly the divergence the shared spec exists to remove.
func TestSeedanceNormalizeSeconds_Trims(t *testing.T) {
	got, outcome := Seedance.NormalizeSeconds("  8  ")
	if got != 8 || outcome != SecondsResolved {
		t.Errorf("NormalizeSeconds(padded) = (%d, %v), want (8, SecondsResolved)", got, outcome)
	}
}

// TestSeedanceTier pins the tier rules. Unlike its two siblings this vendor never
// answers "": the vendor call must name an exact token, so there is no
// "vendor applies its own default" case for resolution.
func TestSeedanceTier(t *testing.T) {
	tests := []struct {
		name string
		size string
		want string
	}{
		{"a token addresses a tier directly", "480p", "480p"},
		{"tokens are matched case-insensitively", "720P", "720p"},
		{"padded tokens are matched too", " 480p ", "480p"},
		// The case a fixed "<=640 is 480p" cutover gets wrong: this codebase's own
		// documented standard 480p size has a longer side of 832, so that reading
		// would bill a client who asked for the cheap tier at the expensive one.
		{"the documented 480p size snaps to 480p", "832x480", "480p"},
		{"portrait is judged by the longer side too", "480x832", "480p"},
		{"the documented 720p size snaps to 720p", "1280x720", "720p"},
		{"a tier this model does not serve snaps down", "1920x1080", "720p"},
		{"an unparsable size falls to the default", "wide", "720p"},
		{"an empty size falls to the default", "", "720p"},
		// 1056 is equidistant from 832 and 1280. First entry wins, so this pins
		// the ORDER of seedanceTierMaxSides: reordering it silently reprices every
		// tied size, which is a decision that should have to edit a test.
		{"an exact tie takes the first entry", "1056x1056", "480p"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Seedance.Tier(tt.size); got != tt.want {
				t.Errorf("Tier(%q) = %q, want %q", tt.size, got, tt.want)
			}
		})
	}
}

// TestSeedanceResolutionToken: the "" answer is the load-bearing one — it is how
// a caller tells a tier token apart from pixel dimensions. 1080p/4k must report
// themselves as NOT tokens: 2.5 is live-confirmed to reject both, and recognising
// them would forward a value the vendor refuses.
func TestSeedanceResolutionToken(t *testing.T) {
	for _, tok := range []string{"480p", "720p", "480P", " 720p "} {
		if Seedance.ResolutionToken(tok) == "" {
			t.Errorf("ResolutionToken(%q) = \"\", want a canonical token", tok)
		}
	}
	for _, notTok := range []string{"1080p", "4k", "2K", "1280x720", "", "p"} {
		if got := Seedance.ResolutionToken(notTok); got != "" {
			t.Errorf("ResolutionToken(%q) = %q, want \"\"", notTok, got)
		}
	}
}

// TestSeedanceIsRegistered: the rules must be reachable through the registry, or
// the broker's gate falls back to "unknown vendor" and prices nothing — the exact
// state recording them was meant to leave.
func TestSeedanceIsRegistered(t *testing.T) {
	spec, ok := Get(VendorSeedance)
	if !ok {
		t.Fatal("Get(VendorSeedance) reported no rules recorded")
	}
	if got := spec.Tier("832x480"); got != "480p" {
		t.Errorf("registry-resolved Tier = %q, want 480p", got)
	}
}
