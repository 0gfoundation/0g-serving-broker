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

func upstreamByName(rep *ReconciliationReport, name string) (UpstreamReport, bool) {
	for _, u := range rep.Upstreams {
		if u.Upstream == name {
			return u, true
		}
	}
	return UpstreamReport{}, false
}

// TestBuildReconciliationReport_TotalsAndPerModel verifies per-unit totals sum across
// models and requests span all units, while each unit's counts stay separate (a mixed
// chatbot+STT rollup must not fold seconds into the token totals).
func TestBuildReconciliationReport_TotalsAndPerModel(t *testing.T) {
	rows := []db.HourlyUsageSum{
		{Upstream: "minimax", Model: "MiniMax-M3", Unit: "tokens", ServiceType: "chatbot", RequestCount: 3, InputCount: 300, OutputCount: 90, CachedInputTokens: 30, CacheWriteInputTokens: 6},
		{Upstream: "minimax", Model: "glm-5", Unit: "tokens", ServiceType: "chatbot", RequestCount: 2, InputCount: 200, OutputCount: 40, CachedInputTokens: 10},
		{Upstream: "minimax", Model: "whisper", Unit: "seconds", ServiceType: "speech-to-text", RequestCount: 5, InputCount: 999},
	}
	start := time.Date(2026, 6, 28, 16, 0, 0, 0, time.UTC)
	end := time.Date(2026, 6, 29, 16, 0, 0, 0, time.UTC)
	rep := buildReconciliationReport("2026-06-29", "2026-06-29", "+08:00", start, end, rows)

	if len(rep.Upstreams) != 1 {
		t.Fatalf("upstreams = %d, want 1", len(rep.Upstreams))
	}
	mm, ok := upstreamByName(rep, "minimax")
	if !ok {
		t.Fatal("missing minimax upstream")
	}
	if mm.TotalRequests != 10 {
		t.Errorf("TotalRequests = %d, want 10 (all units)", mm.TotalRequests)
	}
	tok := mm.TotalsByUnit["tokens"]
	if tok == nil || tok.InputCount != 500 || tok.OutputCount != 130 || tok.RequestCount != 5 || tok.CachedInputTokens != 40 || tok.CacheWriteInputTokens != 6 {
		t.Errorf("tokens totals = %+v, want in=500 out=130 req=5 cached=40 write=6", tok)
	}
	sec := mm.TotalsByUnit["seconds"]
	if sec == nil || sec.InputCount != 999 || sec.RequestCount != 5 {
		t.Errorf("seconds totals = %+v, want in=999 req=5 (not folded into tokens)", sec)
	}
	if len(mm.PerModel) != 3 {
		t.Errorf("PerModel len = %d, want 3", len(mm.PerModel))
	}
	if rep.WindowStartUTC != "2026-06-28T16:00:00Z" || rep.WindowEndUTC != "2026-06-29T16:00:00Z" {
		t.Errorf("window = [%s, %s), want the UTC+8 day", rep.WindowStartUTC, rep.WindowEndUTC)
	}
}

// TestBuildReconciliationReport_MultiUpstream verifies rows are grouped per upstream and
// the upstreams are sorted by name (deterministic response), for the all-upstreams query.
func TestBuildReconciliationReport_MultiUpstream(t *testing.T) {
	rows := []db.HourlyUsageSum{
		{Upstream: "minimax", Model: "MiniMax-M3", Unit: "tokens", RequestCount: 1, InputCount: 100},
		{Upstream: "aliyun", Model: "glm-5", Unit: "tokens", RequestCount: 2, InputCount: 200},
		{Upstream: "aliyun", Model: "qwen", Unit: "tokens", RequestCount: 3, InputCount: 300},
	}
	rep := buildReconciliationReport("2026-06-29", "2026-06-29", "+00:00",
		time.Now().UTC(), time.Now().UTC(), rows)
	if len(rep.Upstreams) != 2 {
		t.Fatalf("upstreams = %d, want 2", len(rep.Upstreams))
	}
	// Sorted by name: aliyun first.
	if rep.Upstreams[0].Upstream != "aliyun" || rep.Upstreams[1].Upstream != "minimax" {
		t.Errorf("order = [%s, %s], want [aliyun, minimax]", rep.Upstreams[0].Upstream, rep.Upstreams[1].Upstream)
	}
	ali, _ := upstreamByName(rep, "aliyun")
	if ali.TotalRequests != 5 || ali.TotalsByUnit["tokens"].InputCount != 500 {
		t.Errorf("aliyun = %+v, want req=5 in=500 (grouped across its models)", ali)
	}
	mm, _ := upstreamByName(rep, "minimax")
	if mm.TotalRequests != 1 || mm.TotalsByUnit["tokens"].InputCount != 100 {
		t.Errorf("minimax = %+v, want req=1 in=100", mm)
	}
}

// TestBuildReconciliationReport_RateClass verifies the cost dimension: two rows for the same
// (model, unit) but different rate_class stay as separate per-model rows (so each tier can be
// compared against a vendor's tiered statement line), while the unit total still sums across
// tiers.
func TestBuildReconciliationReport_RateClass(t *testing.T) {
	rows := []db.HourlyUsageSum{
		{Upstream: "minimax", Model: "MiniMax-M3", Unit: "tokens", RateClass: "tier:<=32000", ServiceType: "chatbot", RequestCount: 4, InputCount: 400, OutputCount: 80},
		{Upstream: "minimax", Model: "MiniMax-M3", Unit: "tokens", RateClass: "tier:unbounded", ServiceType: "chatbot", RequestCount: 1, InputCount: 90000, OutputCount: 200},
	}
	rep := buildReconciliationReport("2026-06-29", "2026-06-29", "+08:00",
		time.Now().UTC(), time.Now().UTC(), rows)

	mm, ok := upstreamByName(rep, "minimax")
	if !ok {
		t.Fatal("missing minimax upstream")
	}
	// Unit total sums across both tiers.
	tok := mm.TotalsByUnit["tokens"]
	if tok == nil || tok.RequestCount != 5 || tok.InputCount != 90400 || tok.OutputCount != 280 {
		t.Errorf("tokens totals = %+v, want req=5 in=90400 out=280 (summed across tiers)", tok)
	}
	// But the tiers remain separately visible for per-tier comparison.
	if len(mm.PerModel) != 2 {
		t.Fatalf("PerModel len = %d, want 2 (one row per rate_class)", len(mm.PerModel))
	}
	seen := map[string]int64{}
	for _, m := range mm.PerModel {
		seen[m.RateClass] = m.InputCount
	}
	if seen["tier:<=32000"] != 400 || seen["tier:unbounded"] != 90000 {
		t.Errorf("per-tier input counts = %v, want tier:<=32000=400 tier:unbounded=90000", seen)
	}
}

// TestBuildReconciliationReport_Empty verifies an empty rollup yields an empty (non-nil)
// upstreams slice.
func TestBuildReconciliationReport_Empty(t *testing.T) {
	rep := buildReconciliationReport("2026-06-29", "2026-06-29", "+08:00",
		time.Now().UTC(), time.Now().UTC(), nil)
	if rep.Upstreams == nil {
		t.Error("Upstreams is nil, want empty non-nil slice for clean JSON")
	}
	if len(rep.Upstreams) != 0 {
		t.Errorf("Upstreams = %v, want empty", rep.Upstreams)
	}
}
