package videospec

import (
	"strconv"
	"testing"
)

// describedVendors is every vendor that publishes its rules. A vendor added
// here without a Describer fails the type assertion below rather than silently
// shipping a model page with no bounds on it.
var describedVendors = []Vendor{VendorSeedance, VendorMiniMax, VendorDashScope}

func describerFor(t *testing.T, v Vendor) Describer {
	t.Helper()
	spec, ok := Get(v)
	if !ok {
		t.Fatalf("vendor %q is not registered", v)
	}
	d, ok := spec.(Describer)
	if !ok {
		t.Fatalf("vendor %q does not implement Describer", v)
	}
	return d
}

// TestDescriber_Values pins what each vendor publishes. These are the numbers a
// model page prints, so a change here is a change to documentation users act on.
func TestDescriber_Values(t *testing.T) {
	tests := []struct {
		vendor Vendor
		dur    DurationSpec
		res    ResolutionSpec
	}{
		{
			vendor: VendorSeedance,
			dur:    DurationSpec{Min: 4, Max: 30, OutOfRange: OutOfRangeClamp, Unspecified: UnspecifiedVendorDefault, Rounding: RoundingCeil},
			res:    ResolutionSpec{Tiers: []string{"480p", "720p", "1080p"}, Default: "720p", PixelSize: PixelSizeSelectsTier},
		},
		{
			vendor: VendorMiniMax,
			dur:    DurationSpec{Min: 4, Max: 15, OutOfRange: OutOfRangeClamp, Unspecified: UnspecifiedMin, Rounding: RoundingCeil},
			res:    ResolutionSpec{Tiers: []string{"512P", "720P", "768P", "1080P", "2K", "4K"}, Default: "2K", PixelSize: PixelSizeAspectRatioOnly},
		},
		{
			// No floor, no ceiling, no named default — the three absences are the
			// point: read as zeros they would claim a 0s duration range.
			vendor: VendorDashScope,
			dur:    DurationSpec{OutOfRange: OutOfRangePassThrough, Unspecified: UnspecifiedVendorDefault, Rounding: RoundingCeil},
			res:    ResolutionSpec{Tiers: []string{"720P", "1080P"}, PixelSize: PixelSizeSelectsTier},
		},
	}

	for _, tt := range tests {
		t.Run(string(tt.vendor), func(t *testing.T) {
			d := describerFor(t, tt.vendor)
			if got := d.Duration(); got != tt.dur {
				t.Errorf("Duration() = %+v, want %+v", got, tt.dur)
			}
			res := d.Resolution()
			if res.Default != tt.res.Default || res.PixelSize != tt.res.PixelSize {
				t.Errorf("Resolution() = %+v, want %+v", res, tt.res)
			}
			if len(res.Tiers) != len(tt.res.Tiers) {
				t.Fatalf("Resolution().Tiers = %v, want %v", res.Tiers, tt.res.Tiers)
			}
			for i := range res.Tiers {
				if res.Tiers[i] != tt.res.Tiers[i] {
					t.Errorf("Resolution().Tiers = %v, want %v", res.Tiers, tt.res.Tiers)
					break
				}
			}
		})
	}
}

// TestDescriber_AgreesWithNormalizeSeconds is the reason Describer is allowed to
// restate constants the billing path already branches on: if the two ever
// disagree, the model page documents bounds the broker does not enforce, and a
// caller sizing a request against the page is billed for something else.
func TestDescriber_AgreesWithNormalizeSeconds(t *testing.T) {
	for _, v := range describedVendors {
		t.Run(string(v), func(t *testing.T) {
			spec, _ := Get(v)
			d := describerFor(t, v)
			dur := d.Duration()

			switch dur.OutOfRange {
			case OutOfRangeClamp:
				over := strconv.FormatInt(dur.Max+1, 10)
				if got, out := spec.NormalizeSeconds(over); out != SecondsResolved || got != dur.Max {
					t.Errorf("NormalizeSeconds(%s) = (%d, %v), want (%d, resolved) — published max does not clamp", over, got, out, dur.Max)
				}
				if dur.Min > 1 {
					under := strconv.FormatInt(dur.Min-1, 10)
					if got, out := spec.NormalizeSeconds(under); out != SecondsResolved || got != dur.Min {
						t.Errorf("NormalizeSeconds(%s) = (%d, %v), want (%d, resolved) — published min does not clamp", under, got, out, dur.Min)
					}
				}
			case OutOfRangePassThrough:
				if dur.Min != 0 || dur.Max != 0 {
					t.Fatalf("pass_through vendor published bounds %d-%d", dur.Min, dur.Max)
				}
				// Nothing is clamped, so a large value comes back as asked.
				if got, out := spec.NormalizeSeconds("600"); out != SecondsResolved || got != 600 {
					t.Errorf("NormalizeSeconds(600) = (%d, %v), want (600, resolved)", got, out)
				}
			default:
				t.Fatalf("unknown out_of_range %q", dur.OutOfRange)
			}

			// An absent duration: the published enum must name the outcome the
			// vendor actually produces.
			got, out := spec.NormalizeSeconds("")
			switch dur.Unspecified {
			case UnspecifiedVendorDefault:
				if out != SecondsVendorDecides {
					t.Errorf("NormalizeSeconds(\"\") outcome = %v, want SecondsVendorDecides", out)
				}
			case UnspecifiedMin:
				if out != SecondsResolved || got != dur.Min {
					t.Errorf("NormalizeSeconds(\"\") = (%d, %v), want (%d, resolved)", got, out, dur.Min)
				}
			default:
				t.Fatalf("unknown unspecified %q", dur.Unspecified)
			}

			// Every published tier must be one the vendor actually recognises —
			// a tier printed on a page but not forwarded is a priced request the
			// caller cannot make.
			for _, tier := range d.Resolution().Tiers {
				if spec.Tier(tier) == "" {
					t.Errorf("published tier %q resolves to no tier", tier)
				}
			}
		})
	}
}

// TestDescriber_TiersAreCopies: the tier lists back billing lookups. Handing out
// the backing array would let a catalog consumer repoint what gets forwarded.
func TestDescriber_TiersAreCopies(t *testing.T) {
	for _, v := range describedVendors {
		t.Run(string(v), func(t *testing.T) {
			d := describerFor(t, v)
			first := d.Resolution().Tiers
			if len(first) == 0 {
				t.Skip("vendor publishes no tiers")
			}
			original := first[0]
			first[0] = "mutated"
			if again := d.Resolution().Tiers; again[0] != original {
				t.Errorf("Resolution().Tiers[0] = %q after caller mutation, want %q", again[0], original)
			}
		})
	}
}
