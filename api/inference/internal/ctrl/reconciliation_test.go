package ctrl

import (
	"testing"
	"time"

	"github.com/0glabs/0g-serving-broker/inference/internal/db"
)

func ptrI64(v int64) *int64 { return &v }

func dimByName(dims []ReconDimension, name string) (ReconDimension, bool) {
	for _, d := range dims {
		if d.Dimension == name {
			return d, true
		}
	}
	return ReconDimension{}, false
}

// TestBuildReconciliationReport_OnlyPresentFields verifies only the statement-supplied
// dimensions are compared (nil fields are skipped).
func TestBuildReconciliationReport_OnlyPresentFields(t *testing.T) {
	rows := []db.HourlyUsageSum{
		{Model: "MiniMax-M3", Unit: "tokens", ServiceType: "chatbot", RequestCount: 10, InputCount: 1000, OutputCount: 500, CachedInputTokens: 100, CacheWriteInputTokens: 20},
	}
	in := ReconciliationInput{
		Upstream:    "minimax",
		InputTokens: ptrI64(1000), // only these two supplied
		Requests:    ptrI64(10),
	}
	rep := buildReconciliationReport(in, "+08:00", time.Now().UTC(), time.Now().UTC(), rows)
	if len(rep.Dimensions) != 2 {
		t.Fatalf("dimensions = %d, want 2 (only supplied fields)", len(rep.Dimensions))
	}
	if d, ok := dimByName(rep.Dimensions, "input_tokens"); !ok || d.BrokerValue != 1000 || !d.WithinTolerance {
		t.Errorf("input_tokens dim = %+v (ok=%v), want broker=1000 within", d, ok)
	}
	if _, ok := dimByName(rep.Dimensions, "output_tokens"); ok {
		t.Error("output_tokens should be absent (not supplied)")
	}
	if !rep.AllWithinTolerance {
		t.Error("AllWithinTolerance = false, want true")
	}
}

// TestBuildReconciliationReport_TokenUnitFiltering verifies token dimensions sum ONLY
// unit=="tokens" rows, while requests are summed across all units — so a mixed
// chatbot+STT rollup does not fold seconds into the token comparison.
func TestBuildReconciliationReport_TokenUnitFiltering(t *testing.T) {
	rows := []db.HourlyUsageSum{
		{Model: "MiniMax-M3", Unit: "tokens", ServiceType: "chatbot", RequestCount: 3, InputCount: 300, OutputCount: 90},
		{Model: "whisper", Unit: "seconds", ServiceType: "speech-to-text", RequestCount: 2, InputCount: 999, OutputCount: 0},
	}
	in := ReconciliationInput{
		Upstream:     "mixed",
		InputTokens:  ptrI64(300), // must match tokens rows only, not + 999 seconds
		OutputTokens: ptrI64(90),
		Requests:     ptrI64(5), // 3 + 2 across all units
	}
	rep := buildReconciliationReport(in, "+00:00", time.Now().UTC(), time.Now().UTC(), rows)
	inTok, _ := dimByName(rep.Dimensions, "input_tokens")
	if inTok.BrokerValue != 300 || !inTok.WithinTolerance {
		t.Errorf("input_tokens broker = %d, want 300 (seconds excluded)", inTok.BrokerValue)
	}
	req, _ := dimByName(rep.Dimensions, "requests")
	if req.BrokerValue != 5 || !req.WithinTolerance {
		t.Errorf("requests broker = %d, want 5 (all units)", req.BrokerValue)
	}
}

// TestBuildReconciliationReport_OutOfTolerance verifies a mismatch flips both the
// dimension and the report-level flag.
func TestBuildReconciliationReport_OutOfTolerance(t *testing.T) {
	rows := []db.HourlyUsageSum{
		{Model: "m", Unit: "tokens", RequestCount: 1, InputCount: 900},
	}
	in := ReconciliationInput{Upstream: "u", InputTokens: ptrI64(1000)} // 10% variance
	rep := buildReconciliationReport(in, "+00:00", time.Now().UTC(), time.Now().UTC(), rows)
	d, _ := dimByName(rep.Dimensions, "input_tokens")
	if d.WithinTolerance {
		t.Errorf("input_tokens within = true, want false (10%% > 0.5%%)")
	}
	if d.Delta != -100 {
		t.Errorf("delta = %d, want -100", d.Delta)
	}
	if rep.AllWithinTolerance {
		t.Error("AllWithinTolerance = true, want false")
	}
}

// TestBuildReconciliationReport_CacheAndTotal verifies cache sub-categories and the
// total_tokens dimension aggregate correctly, and a custom tolerance is honored.
func TestBuildReconciliationReport_CacheAndTotal(t *testing.T) {
	rows := []db.HourlyUsageSum{
		{Model: "m", Unit: "tokens", RequestCount: 2, InputCount: 1000, OutputCount: 400, CachedInputTokens: 250, CacheWriteInputTokens: 60},
	}
	tol := 2.0
	in := ReconciliationInput{
		Upstream:              "u",
		TotalTokens:           ptrI64(1400),
		CachedInputTokens:     ptrI64(250),
		CacheWriteInputTokens: ptrI64(61), // ~1.6% off, within 2% tolerance
		TolerancePercent:      &tol,
	}
	rep := buildReconciliationReport(in, "+00:00", time.Now().UTC(), time.Now().UTC(), rows)
	if tot, _ := dimByName(rep.Dimensions, "total_tokens"); tot.BrokerValue != 1400 || !tot.WithinTolerance {
		t.Errorf("total_tokens = %+v, want broker=1400 within", tot)
	}
	if cw, _ := dimByName(rep.Dimensions, "cache_write_input_tokens"); !cw.WithinTolerance {
		t.Errorf("cache_write within = false, want true under 2%% tolerance (pct=%v)", cw.PercentVariance)
	}
	if rep.TolerancePercent != 2.0 {
		t.Errorf("tolerance = %v, want 2.0", rep.TolerancePercent)
	}
}

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
