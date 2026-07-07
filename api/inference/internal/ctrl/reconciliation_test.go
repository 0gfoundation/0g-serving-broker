package ctrl

import (
	"testing"
	"time"

	"github.com/0glabs/0g-serving-broker/inference/internal/db"
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

// TestBuildReconciliationReport_TotalsAndPerModel verifies per-unit totals sum across
// models and requests span all units, while each unit's counts stay separate (a mixed
// chatbot+STT rollup must not fold seconds into the token totals).
func TestBuildReconciliationReport_TotalsAndPerModel(t *testing.T) {
	rows := []db.HourlyUsageSum{
		{Model: "MiniMax-M3", Unit: "tokens", ServiceType: "chatbot", RequestCount: 3, InputCount: 300, OutputCount: 90, CachedInputTokens: 30, CacheWriteInputTokens: 6},
		{Model: "glm-5", Unit: "tokens", ServiceType: "chatbot", RequestCount: 2, InputCount: 200, OutputCount: 40, CachedInputTokens: 10},
		{Model: "whisper", Unit: "seconds", ServiceType: "speech-to-text", RequestCount: 5, InputCount: 999},
	}
	start := time.Date(2026, 6, 28, 16, 0, 0, 0, time.UTC)
	end := time.Date(2026, 6, 29, 16, 0, 0, 0, time.UTC)
	rep := buildReconciliationReport("minimax", "2026-06-29", "2026-06-29", "+08:00", start, end, rows)

	if rep.TotalRequests != 10 {
		t.Errorf("TotalRequests = %d, want 10 (all units)", rep.TotalRequests)
	}
	tok := rep.TotalsByUnit["tokens"]
	if tok == nil || tok.InputCount != 500 || tok.OutputCount != 130 || tok.RequestCount != 5 || tok.CachedInputTokens != 40 || tok.CacheWriteInputTokens != 6 {
		t.Errorf("tokens totals = %+v, want in=500 out=130 req=5 cached=40 write=6", tok)
	}
	sec := rep.TotalsByUnit["seconds"]
	if sec == nil || sec.InputCount != 999 || sec.RequestCount != 5 {
		t.Errorf("seconds totals = %+v, want in=999 req=5 (not folded into tokens)", sec)
	}
	if len(rep.PerModel) != 3 {
		t.Errorf("PerModel len = %d, want 3", len(rep.PerModel))
	}
	if rep.WindowStartUTC != "2026-06-28T16:00:00Z" || rep.WindowEndUTC != "2026-06-29T16:00:00Z" {
		t.Errorf("window = [%s, %s), want the UTC+8 day", rep.WindowStartUTC, rep.WindowEndUTC)
	}
}

// TestBuildReconciliationReport_Empty verifies an empty rollup yields zero totals and a
// non-nil (JSON-friendly) PerModel slice.
func TestBuildReconciliationReport_Empty(t *testing.T) {
	rep := buildReconciliationReport("minimax", "2026-06-29", "2026-06-29", "+08:00",
		time.Now().UTC(), time.Now().UTC(), nil)
	if rep.TotalRequests != 0 {
		t.Errorf("TotalRequests = %d, want 0", rep.TotalRequests)
	}
	if len(rep.TotalsByUnit) != 0 {
		t.Errorf("TotalsByUnit = %v, want empty", rep.TotalsByUnit)
	}
	if rep.PerModel == nil {
		t.Error("PerModel is nil, want empty non-nil slice for clean JSON")
	}
}
