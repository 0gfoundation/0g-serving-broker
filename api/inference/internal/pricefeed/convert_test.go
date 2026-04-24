package pricefeed

import (
	"math/big"
	"testing"
)

func mustRat(t *testing.T, s string) *big.Rat {
	t.Helper()
	r, ok := new(big.Rat).SetString(s)
	if !ok {
		t.Fatalf("SetString(%q) failed", s)
	}
	return r
}

func TestParseUSDPerMillion(t *testing.T) {
	cases := []struct {
		in      string
		want    string // big.Rat FloatString(6) result; empty means error expected
		wantErr bool
	}{
		{"0.50", "0.500000", false},
		{"1.234567", "1.234567", false},
		{"0", "0.000000", false},
		{"10", "10.000000", false},
		{"  0.3  ", "0.300000", false},
		{"", "", true},
		{"not-a-number", "", true},
		{"-1", "", true},
	}
	for _, c := range cases {
		got, err := ParseUSDPerMillion(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("ParseUSDPerMillion(%q) expected error, got %s", c.in, got.FloatString(6))
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseUSDPerMillion(%q) unexpected error: %v", c.in, err)
			continue
		}
		if got.FloatString(6) != c.want {
			t.Errorf("ParseUSDPerMillion(%q) = %s, want %s", c.in, got.FloatString(6), c.want)
		}
	}
}

func TestUSDPerMillionToWeiPerToken(t *testing.T) {
	// Worked example: price $0.50 per 1M tokens, rate $0.003 per 0G.
	//   per-token USD = 0.5 / 1_000_000        = 5e-7
	//   per-token OG  = 5e-7 / 0.003           ≈ 1.6666… e-4
	//   per-token wei = 1.6666… e-4 * 1e18     ≈ 1.6666… e14
	// Exactly: (0.5 * 1e18) / (1_000_000 * 0.003) = 5e17 / 3000 = 166_666_666_666_666.666…
	// Floor to int   → 166_666_666_666_666.
	// Floor to 1e10  → 166_660_000_000_000 (trailing digits below the
	// quantum dropped).
	price := mustRat(t, "0.50")
	rate := mustRat(t, "0.003")
	got, err := USDPerMillionToWeiPerToken(price, rate)
	if err != nil {
		t.Fatalf("USDPerMillionToWeiPerToken: %v", err)
	}
	want, _ := new(big.Int).SetString("166660000000000", 10)
	if got.Cmp(want) != 0 {
		t.Errorf("wei = %s, want %s", got.String(), want.String())
	}
}

func TestUSDPerMillionToWeiPerToken_FloorQuantizes(t *testing.T) {
	// Any wei-per-token output must be an exact multiple of priceQuantumWei
	// (1e10).  Rate chosen deliberately to produce a value whose naive
	// conversion has non-zero digits below the quantum.
	price := mustRat(t, "0.50")
	rate := mustRat(t, "0.003")
	got, err := USDPerMillionToWeiPerToken(price, rate)
	if err != nil {
		t.Fatalf("USDPerMillionToWeiPerToken: %v", err)
	}
	remainder := new(big.Int).Mod(got, priceQuantumWei)
	if remainder.Sign() != 0 {
		t.Errorf("wei=%s not a multiple of %s (remainder=%s)", got.String(), priceQuantumWei.String(), remainder.String())
	}

	// Floor direction: quantised value must never exceed the exact value.
	exact := new(big.Rat).Quo(price, new(big.Rat).SetInt(tokensPerMillion))
	exact.Quo(exact, rate)
	exact.Mul(exact, new(big.Rat).SetInt(weiPerOG))
	gotRat := new(big.Rat).SetInt(got)
	if gotRat.Cmp(exact) > 0 {
		t.Errorf("quantised wei %s exceeds exact %s — floor violated", got.String(), exact.FloatString(6))
	}
	// Drift from exact must be strictly less than one quantum.
	drift := new(big.Rat).Sub(exact, gotRat)
	quantumRat := new(big.Rat).SetInt(priceQuantumWei)
	if drift.Cmp(quantumRat) >= 0 {
		t.Errorf("drift %s >= quantum %s — floor should leave <1 quantum of drift", drift.FloatString(2), quantumRat.FloatString(0))
	}
}

func TestUSDPerMillionToWeiPerToken_RateScales(t *testing.T) {
	price := mustRat(t, "1.0")
	// Rate doubles => wei should halve.
	a, err := USDPerMillionToWeiPerToken(price, mustRat(t, "0.01"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := USDPerMillionToWeiPerToken(price, mustRat(t, "0.02"))
	if err != nil {
		t.Fatal(err)
	}
	// a should be ~2× b (floor division may differ by 1)
	doubled := new(big.Int).Mul(b, big.NewInt(2))
	diff := new(big.Int).Sub(a, doubled)
	if diff.CmpAbs(big.NewInt(1)) > 0 {
		t.Errorf("rate halving: a=%s, 2b=%s (diff=%s)", a.String(), doubled.String(), diff.String())
	}
}

func TestUSDPerMillionToWeiPerToken_InvalidRate(t *testing.T) {
	_, err := USDPerMillionToWeiPerToken(mustRat(t, "1"), mustRat(t, "0"))
	if err == nil {
		t.Error("expected error for zero rate")
	}
	_, err = USDPerMillionToWeiPerToken(mustRat(t, "1"), mustRat(t, "-0.01"))
	if err == nil {
		t.Error("expected error for negative rate")
	}
	if _, err := USDPerMillionToWeiPerToken(nil, mustRat(t, "0.01")); err == nil {
		t.Error("expected error for nil price")
	}
	if _, err := USDPerMillionToWeiPerToken(mustRat(t, "1"), nil); err == nil {
		t.Error("expected error for nil rate")
	}
}

func TestUSDPerMillionStringToPerToken(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"0.50", "0.0000005", false},
		{"1.5", "0.0000015", false},
		{"10", "0.00001", false},
		{"0", "0", false},
		{"1", "0.000001", false},
		{"0.000001", "0.000000000001", false},
		{"", "", true},
		{"-1", "", true},
		{"nope", "", true},
	}
	for _, c := range cases {
		got, err := USDPerMillionStringToPerToken(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("USDPerMillionStringToPerToken(%q) want error, got %q", c.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("USDPerMillionStringToPerToken(%q) unexpected error: %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("USDPerMillionStringToPerToken(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestDriftBps(t *testing.T) {
	cases := []struct {
		current, reference int64
		want               int
	}{
		{100, 100, 0},
		{105, 100, 500},   // +5%
		{95, 100, 500},    // -5%
		{110, 100, 1000},  // +10%
		{0, 100, 10000},   // -100%
		{200, 100, 10000}, // +100%
		{0, 0, 0},
	}
	for _, c := range cases {
		got := DriftBps(big.NewInt(c.current), big.NewInt(c.reference))
		if got != c.want {
			t.Errorf("DriftBps(%d, %d) = %d, want %d", c.current, c.reference, got, c.want)
		}
	}
}

func TestDriftBps_ZeroReferenceNonZeroCurrent(t *testing.T) {
	got := DriftBps(big.NewInt(1), big.NewInt(0))
	if got == 0 {
		t.Error("expected max-int for zero reference with non-zero current")
	}
}
