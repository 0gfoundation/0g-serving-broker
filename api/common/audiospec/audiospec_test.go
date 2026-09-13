package audiospec

import (
	"math"
	"strconv"
	"testing"
)

func TestParseMaxDuration(t *testing.T) {
	tests := []struct {
		name  string
		raw   string
		want  float64
		wantO bool
	}{
		{name: "plain integer", raw: "30", want: 30, wantO: true},
		{name: "fractional", raw: "12.5", want: 12.5, wantO: true},
		{name: "leading plus", raw: "+30", want: 30, wantO: true},
		{name: "exponent", raw: "1e2", want: 100, wantO: true},
		// Trimmed, unlike videospec.ParseSeconds. The translator parses this field
		// itself and sends the vendor a normalized integer, so the reader this must
		// agree with is the translator's, and it trims.
		{name: "surrounding whitespace is trimmed", raw: "  45  ", want: 45, wantO: true},

		// Every one of these collapses to ok=false, which the vendor rules answer
		// with the ceiling. The point of the collapse is that none of them has a
		// different right answer.
		{name: "empty", raw: "", wantO: false},
		{name: "not a number", raw: "abc", wantO: false},
		{name: "unit suffix", raw: "30s", wantO: false},
		{name: "zero", raw: "0", wantO: false},
		{name: "negative", raw: "-5", wantO: false},
		{name: "NaN", raw: "NaN", wantO: false},
		{name: "positive infinity", raw: "Inf", wantO: false},
		{name: "negative infinity", raw: "-Inf", wantO: false},
		{name: "beyond the representable bound", raw: strconv.FormatFloat(maxRepresentableSeconds*2, 'f', -1, 64), wantO: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := ParseMaxDuration(tt.raw)
			if ok != tt.wantO {
				t.Fatalf("ParseMaxDuration(%q) ok = %v, want %v", tt.raw, ok, tt.wantO)
			}
			if ok && got != tt.want {
				t.Errorf("ParseMaxDuration(%q) = %v, want %v", tt.raw, got, tt.want)
			}
			if !ok && got != 0 {
				t.Errorf("ParseMaxDuration(%q) returned %v alongside ok=false; a non-zero value there invites a caller to use it", tt.raw, got)
			}
		})
	}
}

// The representable bound guards the float->int64 conversion every implementation
// performs. Past it the conversion is implementation-defined, and a vendor's clamp
// would then move the garbage UP to its ceiling — the right answer by accident, via
// a path nothing tests. This pins that it is rejected here instead.
func TestParseMaxDurationRejectsBeyondRepresentable(t *testing.T) {
	if _, ok := ParseMaxDuration(strconv.FormatFloat(maxRepresentableSeconds+1, 'f', -1, 64)); ok {
		t.Fatal("a duration past maxRepresentableSeconds was accepted")
	}
	if _, ok := ParseMaxDuration(strconv.FormatFloat(maxRepresentableSeconds, 'f', -1, 64)); !ok {
		t.Fatal("the representable bound itself should still parse")
	}
}

func TestGetIsCaseAndWhitespaceInsensitive(t *testing.T) {
	for _, name := range []string{"seedaudio", "SeedAudio", "  SEEDAUDIO  "} {
		if _, ok := Get(Vendor(name)); !ok {
			t.Errorf("Get(%q) found no rules; lookup must not depend on how an operator spelled the vendor", name)
		}
	}
}

// A vendor nobody has recorded rules for must report ok=false, never a zero Spec.
// A caller that cannot look up a ceiling has to degrade explicitly (forward
// unreserved and count it); silently handing back a usable-looking value would
// size every reservation for that vendor at whatever the zero value implies.
func TestGetUnknownVendor(t *testing.T) {
	if _, ok := Get("no-such-vendor"); ok {
		t.Fatal("Get returned rules for an unregistered vendor")
	}
	if _, ok := Get(""); ok {
		t.Fatal("Get returned rules for the empty vendor")
	}
}

func TestRegisterRejectsEmptyVendor(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("register accepted an empty vendor name")
		}
	}()
	register("", SeedAudio)
}

// Two files claiming one vendor is a merge accident, and letting the last
// registration win would make the outcome depend on file order — with the losing
// rules silently sizing every reservation.
func TestRegisterRejectsDuplicate(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("register accepted a duplicate vendor registration")
		}
	}()
	register(VendorSeedAudio, SeedAudio)
}

// Every registered vendor must return a bound in (0, MaxOutputSeconds] for any
// input at all. This is the contract the reservation gate relies on: a zero or
// negative reserve disables the gate, and one above the ceiling over-holds. Run
// across the registry so a vendor added later cannot skip it.
func TestEveryVendorAlwaysReturnsAUsableBound(t *testing.T) {
	raws := []string{"", "0", "-1", "abc", "NaN", "Inf", "1", "0.1", "119.4", "120", "121", "1e300"}
	for vendor, spec := range specs {
		max := spec.MaxOutputSeconds()
		if max <= 0 {
			t.Errorf("%s: MaxOutputSeconds() = %d, must be positive", vendor, max)
			continue
		}
		for _, raw := range raws {
			got := spec.ReserveSeconds(raw)
			if got <= 0 || got > max {
				t.Errorf("%s: ReserveSeconds(%q) = %d, want a bound in (0, %d]", vendor, raw, got, max)
			}
		}
	}
}

// The reserve must never come in below the whole-second fee the same request can
// be billed. Checked across the registry against the ceiling itself, since that
// is the value every unusable input resolves to.
func TestReserveNeverBelowCeiledRequest(t *testing.T) {
	for vendor, spec := range specs {
		max := spec.MaxOutputSeconds()
		for _, f := range []float64{0.1, 1.0, 1.4, 59.9, 60, 119.4} {
			if f > float64(max) {
				continue
			}
			raw := strconv.FormatFloat(f, 'f', -1, 64)
			want := int64(math.Ceil(f))
			if got := spec.ReserveSeconds(raw); got < want {
				t.Errorf("%s: ReserveSeconds(%q) = %d, below the ceil'd request %d — a bound must never round down", vendor, raw, got, want)
			}
		}
	}
}
