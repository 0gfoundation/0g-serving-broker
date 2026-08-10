package videospec

import "testing"

// The cases below are the vendor behaviours this package claims to describe.
// They are pinned here rather than only in the translator's mapper tests
// because the broker will resolve a fee from these same answers — a change that
// keeps the mappers passing but shifts a bound would silently move money.

func TestMiniMax_NormalizeSeconds(t *testing.T) {
	spec, ok := Get(VendorMiniMax)
	if !ok {
		t.Fatal("no spec for minimax")
	}
	tests := []struct {
		name string
		raw  string
		want int64
	}{
		{"absent renders the vendor floor", "", 4},
		{"in range is honoured", "8", 8},
		{"fractional rounds up to the next whole rendered second", "7.2", 8},
		{"below the floor is raised — the vendor renders 4 either way", "1", 4},
		{"above the ceiling is clamped down", "60", 15},
		// An absurd-but-finite value is CLAMPED by this vendor, not discarded.
		// Converting it to int64 first would be implementation-defined (MinInt64
		// on amd64) and land below the floor — turning the longest possible
		// request into the shortest clip.
		{"non-numeric renders the floor", "abc", 4},
		{"zero renders the floor", "0", 4},
		{"negative renders the floor", "-5", 4},
		// Untrimmed on purpose: the vendor's own reader does not trim, so a
		// padded value is unreadable to it. Trimming here would resolve a
		// duration the vendor will not.
		{"a padded value is unreadable, exactly as the vendor reads it", " 5 ", 4},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, outcome := spec.NormalizeSeconds(tt.raw)
			if outcome != SecondsResolved {
				t.Fatalf("NormalizeSeconds(%q) outcome = %v, want SecondsResolved; this vendor always has a fallback", tt.raw, outcome)
			}
			if got != tt.want {
				t.Errorf("NormalizeSeconds(%q) = %d, want %d", tt.raw, got, tt.want)
			}
		})
	}
}

func TestDashScope_NormalizeSeconds(t *testing.T) {
	spec, ok := Get(VendorDashScope)
	if !ok {
		t.Fatal("no spec for dashscope")
	}
	tests := []struct {
		name        string
		raw         string
		want        int64
		wantOutcome SecondsOutcome
	}{
		{name: "in range is honoured", raw: "5", want: 5, wantOutcome: SecondsResolved},
		{name: "fractional rounds up", raw: "4.1", want: 5, wantOutcome: SecondsResolved},
		{name: "exactly at the representable bound is honoured", raw: "1099511627776", want: 1 << 40, wantOutcome: SecondsResolved},
		// This vendor OMITS an unreadable duration and picks for itself, so the
		// rendered length is not determined by the request. Reporting a number
		// here would be inventing one.
		{name: "absent means the vendor decides", raw: "", wantOutcome: SecondsVendorDecides},
		{name: "non-numeric means the vendor decides", raw: "abc", wantOutcome: SecondsVendorDecides},
		{name: "zero means the vendor decides", raw: "0", wantOutcome: SecondsVendorDecides},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, outcome := spec.NormalizeSeconds(tt.raw)
			if outcome != tt.wantOutcome {
				t.Fatalf("NormalizeSeconds(%q) outcome = %v, want %v (got %d)", tt.raw, outcome, tt.wantOutcome, got)
			}
			if outcome == SecondsResolved && got != tt.want {
				t.Errorf("NormalizeSeconds(%q) = %d, want %d", tt.raw, got, tt.want)
			}
		})
	}
}

// TestNormalizeSeconds_OutOfRangeIsRejectedForEveryVendor pins the one rule that
// is OURS rather than any vendor's: past what an int64 can represent, no
// duration can be resolved, so the request is refused.
//
// Refusing is the point. Each vendor's own fallback would bill the caller for a
// clip nothing in their request asked for — MiniMax would clamp to its LONGEST
// (most expensive) one, DashScope would hand the choice to the vendor. Both
// render something and charge for it. This used to be carried as a per-vendor
// field, as though the vendors disagreed about it; they do not, and it was never
// a vendor rule to begin with — it guards a Go conversion.
func TestNormalizeSeconds_OutOfRangeIsRejectedForEveryVendor(t *testing.T) {
	for _, v := range []Vendor{VendorMiniMax, VendorDashScope} {
		spec, _ := Get(v)
		for _, raw := range []string{"1e20", "1e30", "1e50", "Inf", "1099511627777"} {
			if _, outcome := spec.NormalizeSeconds(raw); outcome != SecondsRejected {
				t.Errorf("%s: NormalizeSeconds(%q) outcome = %v, want SecondsRejected", v, raw, outcome)
			}
		}
	}
}

func TestTier(t *testing.T) {
	minimax, _ := Get(VendorMiniMax)
	dashscope, _ := Get(VendorDashScope)

	tests := []struct {
		name              string
		spec              Spec
		size              string
		deploymentDefault string
		want              string
	}{
		// MiniMax reads pixel dimensions as an ASPECT RATIO only; its tier comes
		// from deployment configuration. This is the case that makes a tier
		// unknowable to anyone who only looks at the request.
		{"minimax: pixel dimensions are not a tier", minimax, "1280x720", "2K", "2K"},
		{"minimax: portrait pixels are not a tier either", minimax, "720x1280", "2K", "2K"},
		{"minimax: absent size falls to the deployment tier", minimax, "", "2K", "2K"},
		{"minimax: its own token is honoured", minimax, "1080P", "2K", "1080P"},
		{"minimax: token matching is case/space insensitive", minimax, " 4k ", "2K", "4K"},
		{"minimax: an unknown token is not a tier", minimax, "8K", "2K", "2K"},

		// DashScope DOES snap pixel dimensions onto its two-tier enum, so the
		// deployment default is irrelevant to it.
		{"dashscope: its own token passes through", dashscope, "720p", "ignored", "720P"},
		{"dashscope: pixels at the threshold snap down", dashscope, "1280x720", "ignored", "720P"},
		{"dashscope: pixels above the threshold snap up", dashscope, "1792x1024", "ignored", "1080P"},
		{"dashscope: portrait above the threshold snaps up too", dashscope, "1024x1792", "ignored", "1080P"},
		// Nothing recognisable: the vendor applies its own default, which the
		// request does not determine.
		{"dashscope: unparsable size leaves the tier undetermined", dashscope, "garbage", "ignored", ""},
		{"dashscope: absent size leaves the tier undetermined", dashscope, "", "ignored", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.spec.Tier(tt.size, tt.deploymentDefault); got != tt.want {
				t.Errorf("Tier(%q, %q) = %q, want %q", tt.size, tt.deploymentDefault, got, tt.want)
			}
		})
	}
}

// TestGet_UnknownVendor: a vendor nobody has recorded rules for must report
// itself as unknown. Returning a zero Spec silently would make every caller
// treat it as "no floor, no ceiling, tier undetermined" — a plausible-looking
// answer that describes no real vendor.
func TestGet_UnknownVendor(t *testing.T) {
	if _, ok := Get("seedance"); ok {
		t.Error("Get reported rules for a vendor that has none recorded")
	}
	if _, ok := Get(""); ok {
		t.Error("Get reported rules for an empty vendor")
	}
	// Recorded vendors are matched case/space insensitively, since the value
	// reaching here comes from deployment configuration.
	if _, ok := Get(" MiniMax "); !ok {
		t.Error("Get should match a recorded vendor case/space insensitively")
	}
}

func TestParsePixelSize(t *testing.T) {
	tests := []struct {
		size string
		w, h int
		ok   bool
	}{
		{"1280x720", 1280, 720, true},
		{"1280X720", 1280, 720, true},
		{" 1280 x 720 ", 1280, 720, true},
		{"720P", 0, 0, false},
		{"", 0, 0, false},
		{"1280", 0, 0, false},
		{"0x720", 0, 0, false},
		{"-1x720", 0, 0, false},
	}
	for _, tt := range tests {
		w, h, ok := ParsePixelSize(tt.size)
		if ok != tt.ok || (ok && (w != tt.w || h != tt.h)) {
			t.Errorf("ParsePixelSize(%q) = (%d, %d, %v), want (%d, %d, %v)", tt.size, w, h, ok, tt.w, tt.h, tt.ok)
		}
	}
}

// TestRegister_RejectsDuplicate: two files claiming one vendor is a merge
// accident, and letting the last registration win would make the outcome depend
// on file order — with the losing set of rules silently pricing every request
// for that vendor. Failing at init is the only outcome an operator can act on.
func TestRegister_RejectsDuplicate(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("registering a vendor twice must panic, not silently replace the first set of rules")
		}
	}()
	register(VendorMiniMax, miniMax{})
}

func TestRegister_RejectsUnnamedVendor(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("registering a spec with no vendor name must panic; Get would never find it")
		}
	}()
	register("", miniMax{})
}

// TestVendorsShareNoStructure is the property this package is organised around,
// asserted rather than left to a comment: each vendor implements the contract in
// its own file with its own logic, so the two recorded ones disagree on every
// axis without either constraining the other.
//
// If someone later collapses them into one shared shape filled with per-vendor
// data, one of these will have to be bent to fit — and the next vendor
// (ByteDance Seedance bills in tokens, not seconds) will not fit at all.
func TestVendorsShareNoStructure(t *testing.T) {
	minimax, _ := Get(VendorMiniMax)
	dashscope, _ := Get(VendorDashScope)

	// Unreadable duration: one has a fallback, the other hands the choice over.
	if _, out := minimax.NormalizeSeconds(""); out != SecondsResolved {
		t.Errorf("minimax unreadable outcome = %v, want SecondsResolved", out)
	}
	if _, out := dashscope.NormalizeSeconds(""); out != SecondsVendorDecides {
		t.Errorf("dashscope unreadable outcome = %v, want SecondsVendorDecides", out)
	}
	// Below any floor: one raises, the other passes through.
	if got, _ := minimax.NormalizeSeconds("1"); got != 4 {
		t.Errorf("minimax NormalizeSeconds(1) = %d, want 4 (its floor)", got)
	}
	if got, _ := dashscope.NormalizeSeconds("1"); got != 1 {
		t.Errorf("dashscope NormalizeSeconds(1) = %d, want 1 (it has no floor)", got)
	}
	// Pixel dimensions: one reads them as a tier, the other as an aspect ratio.
	if got := minimax.Tier("1792x1024", "2K"); got != "2K" {
		t.Errorf("minimax Tier(pixels) = %q, want the deployment tier", got)
	}
	if got := dashscope.Tier("1792x1024", "2K"); got != "1080P" {
		t.Errorf("dashscope Tier(pixels) = %q, want 1080P (derived, deployment tier ignored)", got)
	}
}

// TestResolutionToken_TheEmptyAnswerIsTheLoadBearingOne pins what this function
// is FOR: telling a tier apart from pixel dimensions. Everything downstream —
// which code path a size takes, which price row a request is billed on — hangs
// on that distinction, and only a recorded vocabulary can make it.
//
// The canonical spelling it also returns is a much smaller benefit, and the
// vocabulary is a plain list precisely because that is all there is to it: the
// spelling IS the element.
func TestResolutionToken_TheEmptyAnswerIsTheLoadBearingOne(t *testing.T) {
	minimax := MiniMax
	tests := []struct {
		size string
		want string
		why  string
	}{
		{"2K", "2K", "a tier, recognised"},
		{"2k", "2K", "same tier, canonicalised for the value sent upstream"},
		{" 1080P ", "1080P", "padding is the client's, not a different tier"},
		{"1280x720", "", "pixel dimensions are NOT a tier — this is the answer that routes it elsewhere"},
		{"", "", "absent is not a tier"},
		{"8K", "", "a tier this vendor has no vocabulary for is not one either"},
		{"garbage", "", "nor is anything else"},
	}
	for _, tt := range tests {
		if got := minimax.ResolutionToken(tt.size); got != tt.want {
			t.Errorf("ResolutionToken(%q) = %q, want %q (%s)", tt.size, got, tt.want, tt.why)
		}
	}
}
