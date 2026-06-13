package proxy

import (
	"testing"

	"github.com/0glabs/0g-serving-broker/inference/config"
)

// TestBuildPerUserOverrides verifies address validation, casing normalization,
// TPM-burst flooring, and that invalid / empty / no-op entries are dropped
// rather than silently stored under a key no real user can match.
func TestBuildPerUserOverrides(t *testing.T) {
	const ctxLen = 1000
	const partnerLower = "0xabc0000000000000000000000000000000000001"

	overrides := []config.PerUserLimitOverride{
		{UserAddress: "0xABC0000000000000000000000000000000000001", MaxConcurrent: 40, TPM: 900000, TPMBurst: 500},
		{UserAddress: "not-an-address", MaxConcurrent: 99},          // invalid -> skipped
		{UserAddress: "   ", MaxConcurrent: 99},                     // empty -> skipped
		{UserAddress: "0xDEF0000000000000000000000000000000000002"}, // no positive limits -> no-op, skipped
	}

	conc, rpm, tpm, ipm := buildPerUserOverrides(overrides, ctxLen, noopLogger{})

	// Valid entry stored under the lowercase key.
	if conc[partnerLower] != 40 {
		t.Fatalf("conc[%s] = %d, want 40", partnerLower, conc[partnerLower])
	}
	// TPM burst floored up to context length (500 -> 1000); rate preserved.
	if got := tpm[partnerLower]; got.Burst != ctxLen || got.Rate != 900000 {
		t.Fatalf("tpm override = %+v, want {Rate:900000 Burst:1000}", got)
	}
	// Only the one valid entry survives; invalid/empty/no-op are absent.
	if len(conc) != 1 {
		t.Fatalf("conc map size = %d, want 1 (invalid/empty/no-op must be skipped)", len(conc))
	}
	if _, ok := conc["0xdef0000000000000000000000000000000000002"]; ok {
		t.Fatal("no-op entry (no positive limits) should not be stored")
	}
	// No RPM/IPM fields were set on the valid entry, so those maps stay empty.
	if len(rpm) != 0 || len(ipm) != 0 {
		t.Fatalf("rpm=%d ipm=%d, want both 0", len(rpm), len(ipm))
	}
}

// TestBuildPerUserOverrides_Empty verifies the no-overrides path returns nil
// maps (the "no overrides" sentinel the limiters expect).
func TestBuildPerUserOverrides_Empty(t *testing.T) {
	conc, rpm, tpm, ipm := buildPerUserOverrides(nil, 1000, noopLogger{})
	if conc != nil || rpm != nil || tpm != nil || ipm != nil {
		t.Fatalf("expected all nil maps for empty input, got conc=%v rpm=%v tpm=%v ipm=%v", conc, rpm, tpm, ipm)
	}
}
