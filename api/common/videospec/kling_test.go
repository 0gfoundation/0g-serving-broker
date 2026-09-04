package videospec

import "testing"

// TestKlingNormalizeSeconds pins the duration rules the broker's gate and the
// translator now BOTH read, so a change to either has to change this first.
func TestKlingNormalizeSeconds(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		want    int64
		outcome SecondsOutcome
	}{
		{"in range", "8", 8, SecondsResolved},
		{"at the floor", "3", 3, SecondsResolved},
		{"at the ceiling", "15", 15, SecondsResolved},
		// Clamping is safe: billing is on the vendor's echoed usage.duration, so
		// neither clamp can move the bill away from what was rendered.
		{"below the floor renders the floor", "1", 3, SecondsResolved},
		{"above the ceiling renders the ceiling", "16", 15, SecondsResolved},
		{"fractional ceils to the next whole second", "3.1", 4, SecondsResolved},
		{"fractional below the floor still floors", "0.5", 3, SecondsResolved},
		// An unreadable duration is OMITTED from the vendor call, so the vendor
		// picks (5s). Nothing here may invent that number.
		{"absent lets the vendor choose", "", 0, SecondsVendorDecides},
		{"unparsable lets the vendor choose", "abc", 0, SecondsVendorDecides},
		{"zero lets the vendor choose", "0", 0, SecondsVendorDecides},
		{"negative lets the vendor choose", "-3", 0, SecondsVendorDecides},
		// Past this the float->int64 conversion is implementation-defined, and a
		// floor would clamp the result UP. Refused instead.
		{"absurd magnitude is refused, not clamped", "1e30", 0, SecondsRejected},
		{"infinity is refused", "Inf", 0, SecondsRejected},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, outcome := Kling.NormalizeSeconds(tt.raw)
			if got != tt.want || outcome != tt.outcome {
				t.Errorf("NormalizeSeconds(%q) = (%d, %v), want (%d, %v)",
					tt.raw, got, outcome, tt.want, tt.outcome)
			}
		})
	}
}

// TestKlingTier pins the tier rules. Unlike Seedance (whose vendor call must
// always carry an exact token) Kling's mode is optional, so an unresolvable
// size answers "" (vendor default "pro") rather than a hardcoded token.
func TestKlingTier(t *testing.T) {
	tests := []struct {
		name string
		size string
		want string
	}{
		{"a token addresses a tier directly", "std", "std"},
		{"tokens are matched case-insensitively", "PRO", "pro"},
		{"padded tokens are matched too", " std ", "std"},
		{"the documented 720p size snaps to std", "1280x720", "std"},
		{"the documented 1080p size snaps to pro", "1920x1080", "pro"},
		{"portrait is judged by the longer side too", "720x1280", "std"},
		{"an unparsable size lets the vendor decide", "wide", ""},
		{"an empty size lets the vendor decide", "", ""},
		// Exact boundary: 1280 itself must stay "std" (>, not >=).
		{"exact threshold stays std", "1280x1000", "std"},
		{"one past the threshold is pro", "1281x1000", "pro"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Kling.Tier(tt.size); got != tt.want {
				t.Errorf("Tier(%q) = %q, want %q", tt.size, got, tt.want)
			}
		})
	}
}

// TestKlingResolutionToken: the "" answer is the load-bearing one — it is how
// a caller tells a tier token apart from pixel dimensions.
func TestKlingResolutionToken(t *testing.T) {
	for _, tok := range []string{"std", "pro", "STD", " pro "} {
		if Kling.ResolutionToken(tok) == "" {
			t.Errorf("ResolutionToken(%q) = \"\", want a canonical token", tok)
		}
	}
	for _, notTok := range []string{"720p", "1080p", "1280x720", "", "s"} {
		if got := Kling.ResolutionToken(notTok); got != "" {
			t.Errorf("ResolutionToken(%q) = %q, want \"\"", notTok, got)
		}
	}
}

// TestKlingIsRegistered: the rules must be reachable through the registry, or
// the broker's gate falls back to "unknown vendor" and prices nothing.
func TestKlingIsRegistered(t *testing.T) {
	spec, ok := Get(VendorKling)
	if !ok {
		t.Fatal("Get(VendorKling) reported no rules recorded")
	}
	if got := spec.Tier("1920x1080"); got != "pro" {
		t.Errorf("registry-resolved Tier = %q, want pro", got)
	}
}

// TestKlingDoesNotSatisfyTokenEstimator: Kling bills per second, not per
// token, so it must NOT implement the optional TokenEstimator interface — a
// vendor that does would be estimated against a token rate it has none of.
func TestKlingDoesNotSatisfyTokenEstimator(t *testing.T) {
	spec, ok := Get(VendorKling)
	if !ok {
		t.Fatal("Get(VendorKling) reported no rules recorded")
	}
	if _, ok := spec.(TokenEstimator); ok {
		t.Fatal("Kling must not satisfy TokenEstimator: it bills per second (usage.duration), not per token")
	}
}
