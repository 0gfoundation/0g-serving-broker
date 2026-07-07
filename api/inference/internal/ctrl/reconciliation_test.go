package ctrl

import (
	"testing"
	"time"
)

func TestParseFixedZone(t *testing.T) {
	tests := []struct {
		in         string
		wantLabel  string
		wantOffset int // seconds
		wantErr    bool
	}{
		{"", "+00:00", 0, false},
		{"Z", "+00:00", 0, false},
		{"utc", "+00:00", 0, false},
		{"+08:00", "+08:00", 8 * 3600, false},
		{"+0800", "+08:00", 8 * 3600, false},
		{"-05:00", "-05:00", -5 * 3600, false},
		{"+05:30", "+05:30", 5*3600 + 30*60, false},
		{"08:00", "", 0, true},  // missing sign
		{"+8:00", "", 0, true},  // not zero-padded → 3 chars after strip
		{"+15:00", "", 0, true}, // hours out of range
		{"+08:99", "", 0, true}, // minutes out of range
		{"garbage", "", 0, true},
	}
	for _, tt := range tests {
		loc, label, err := parseFixedZone(tt.in)
		if tt.wantErr {
			if err == nil {
				t.Errorf("parseFixedZone(%q) = nil err, want error", tt.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseFixedZone(%q) unexpected err: %v", tt.in, err)
			continue
		}
		if label != tt.wantLabel {
			t.Errorf("parseFixedZone(%q) label = %q, want %q", tt.in, label, tt.wantLabel)
		}
		_, off := time.Now().In(loc).Zone()
		if off != tt.wantOffset {
			t.Errorf("parseFixedZone(%q) offset = %d, want %d", tt.in, off, tt.wantOffset)
		}
	}
}

func TestReconciliationWindowUTC_UTC8(t *testing.T) {
	loc, _, err := parseFixedZone("+08:00")
	if err != nil {
		t.Fatalf("parseFixedZone: %v", err)
	}
	// MiniMax's UTC+8 "2026-06-29" is the UTC range [2026-06-28T16:00Z, 2026-06-29T16:00Z).
	start, end, err := reconciliationWindowUTC("2026-06-29", "2026-06-29", loc)
	if err != nil {
		t.Fatalf("reconciliationWindowUTC: %v", err)
	}
	wantStart := time.Date(2026, 6, 28, 16, 0, 0, 0, time.UTC)
	wantEnd := time.Date(2026, 6, 29, 16, 0, 0, 0, time.UTC)
	if !start.Equal(wantStart) {
		t.Errorf("start = %s, want %s", start.Format(time.RFC3339), wantStart.Format(time.RFC3339))
	}
	if !end.Equal(wantEnd) {
		t.Errorf("end = %s, want %s", end.Format(time.RFC3339), wantEnd.Format(time.RFC3339))
	}
}

func TestReconciliationWindowUTC_MultiDayUTC(t *testing.T) {
	start, end, err := reconciliationWindowUTC("2026-06-01", "2026-06-30", time.UTC)
	if err != nil {
		t.Fatalf("reconciliationWindowUTC: %v", err)
	}
	if !start.Equal(time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("start = %s", start.Format(time.RFC3339))
	}
	// periodEnd inclusive → window ends at start of July 1.
	if !end.Equal(time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("end = %s", end.Format(time.RFC3339))
	}
}

func TestReconciliationWindowUTC_EndBeforeStart(t *testing.T) {
	if _, _, err := reconciliationWindowUTC("2026-06-30", "2026-06-01", time.UTC); err == nil {
		t.Error("expected error when periodEnd is before periodStart")
	}
}

func TestMakeDimension(t *testing.T) {
	tests := []struct {
		name       string
		broker     int64
		provider   int64
		tol        float64
		wantDelta  int64
		wantWithin bool
	}{
		{"exact match", 1000, 1000, 0.5, 0, true},
		{"within tolerance", 1004, 1000, 0.5, 4, true},   // 0.4%
		{"out of tolerance", 1010, 1000, 0.5, 10, false}, // 1.0%
		{"both zero", 0, 0, 0.5, 0, true},
		{"provider zero broker nonzero", 5, 0, 0.5, 5, false},
		{"broker short", 990, 1000, 0.5, -10, false},
	}
	for _, tt := range tests {
		d := makeDimension(tt.name, tt.broker, tt.provider, tt.tol)
		if d.Delta != tt.wantDelta {
			t.Errorf("%s: delta = %d, want %d", tt.name, d.Delta, tt.wantDelta)
		}
		if d.WithinTolerance != tt.wantWithin {
			t.Errorf("%s: within = %v (pct=%v), want %v", tt.name, d.WithinTolerance, d.PercentVariance, tt.wantWithin)
		}
	}
}
