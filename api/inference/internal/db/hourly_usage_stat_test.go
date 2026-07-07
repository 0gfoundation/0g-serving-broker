//go:build integration

package db

import (
	"testing"
	"time"

	constant "github.com/0glabs/0g-serving-broker/inference/const"
	"github.com/0glabs/0g-serving-broker/inference/model"
)

func fetchHourly(t *testing.T, d *DB) map[string]model.HourlyUsageStat {
	t.Helper()
	var rows []model.HourlyUsageStat
	if err := d.db.Find(&rows).Error; err != nil {
		t.Fatalf("fetch hourly_usage_stat: %v", err)
	}
	out := make(map[string]model.HourlyUsageStat, len(rows))
	for _, r := range rows {
		key := r.Hour.UTC().Format(time.RFC3339) + "|" + r.Upstream + "|" + r.Model + "|" + r.Unit
		out[key] = r
	}
	return out
}

// TestHourlyBucketingByCreatedAt verifies the settlement rollup buckets requests by
// their created_at hour (UTC), not the settlement day, and keeps raw counts + cache
// sub-categories keyed by (hour, upstream, model, unit).
func TestHourlyBucketingByCreatedAt(t *testing.T) {
	d := setupTestDB(t)
	migrateUsageTables(t, d)

	h10 := time.Date(2026, 6, 29, 10, 30, 0, 0, time.UTC) // buckets to 10:00
	h10b := time.Date(2026, 6, 29, 10, 5, 0, 0, time.UTC) // same 10:00 bucket
	h11 := time.Date(2026, 6, 29, 11, 1, 0, 0, time.UTC)  // 11:00 bucket
	// Cross UTC midnight: 23:50 on the 29th vs 00:10 on the 30th land in different days.
	hLate := time.Date(2026, 6, 29, 23, 50, 0, 0, time.UTC)
	hNext := time.Date(2026, 6, 30, 0, 10, 0, 0, time.UTC)

	batch := []*model.Request{
		{Model: model.Model{CreatedAt: &h10}, RequestHash: "a1", Upstream: "minimax", ModelName: "MiniMax-M3", Unit: "tokens", InputCount: 100, OutputCount: 50, CachedInputTokens: 20, CacheWriteInputTokens: 5},
		{Model: model.Model{CreatedAt: &h10b}, RequestHash: "a2", Upstream: "minimax", ModelName: "MiniMax-M3", Unit: "tokens", InputCount: 10, OutputCount: 5, CachedInputTokens: 1},
		{Model: model.Model{CreatedAt: &h11}, RequestHash: "a3", Upstream: "minimax", ModelName: "MiniMax-M3", Unit: "tokens", InputCount: 7, OutputCount: 3},
		{Model: model.Model{CreatedAt: &hLate}, RequestHash: "a4", Upstream: "minimax", ModelName: "MiniMax-M3", Unit: "tokens", InputCount: 1, OutputCount: 1},
		{Model: model.Model{CreatedAt: &hNext}, RequestHash: "a5", Upstream: "minimax", ModelName: "MiniMax-M3", Unit: "tokens", InputCount: 2, OutputCount: 2},
	}
	if err := d.db.Create(&batch).Error; err != nil {
		t.Fatalf("seed requests: %v", err)
	}
	if err := d.AccumulateAndDeleteRequests(batch, AccumulateOptions{ServiceType: constant.ServiceTypeChatbot}); err != nil {
		t.Fatalf("accumulate: %v", err)
	}

	hourly := fetchHourly(t, d)
	// 10:00 bucket = a1 + a2 merged.
	got := hourly["2026-06-29T10:00:00Z|minimax|MiniMax-M3|tokens"]
	if got.RequestCount != 2 || got.InputCount != 110 || got.OutputCount != 55 || got.CachedInputTokens != 21 || got.CacheWriteInputTokens != 5 {
		t.Errorf("10:00 bucket = %+v, want req=2 in=110 out=55 cached=21 write=5", got)
	}
	if got.ServiceType != constant.ServiceTypeChatbot {
		t.Errorf("10:00 bucket service_type = %q, want chatbot", got.ServiceType)
	}
	// Distinct hour buckets exist for 11:00, 23:00 (late), and 00:00 next day.
	if got := hourly["2026-06-29T11:00:00Z|minimax|MiniMax-M3|tokens"]; got.RequestCount != 1 || got.InputCount != 7 {
		t.Errorf("11:00 bucket = %+v, want req=1 in=7", got)
	}
	if _, ok := hourly["2026-06-29T23:00:00Z|minimax|MiniMax-M3|tokens"]; !ok {
		t.Error("missing 23:00 bucket (cross-midnight day 29)")
	}
	if _, ok := hourly["2026-06-30T00:00:00Z|minimax|MiniMax-M3|tokens"]; !ok {
		t.Error("missing 00:00 bucket (cross-midnight day 30)")
	}
	if len(hourly) != 4 {
		t.Errorf("distinct hourly buckets = %d, want 4", len(hourly))
	}
}

// TestHourlySTTKeepsSeconds verifies STT rows keep raw seconds in input_count (unit
// "seconds"), unlike daily_stat which zeroes the token columns for STT.
func TestHourlySTTKeepsSeconds(t *testing.T) {
	d := setupTestDB(t)
	migrateUsageTables(t, d)

	h := time.Date(2026, 6, 29, 9, 0, 0, 0, time.UTC)
	batch := []*model.Request{
		{Model: model.Model{CreatedAt: &h}, RequestHash: "s1", Upstream: "self", ModelName: "whisper-large-v3", Unit: "seconds", InputCount: 120, OutputCount: 0},
	}
	if err := d.db.Create(&batch).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := d.AccumulateAndDeleteRequests(batch, AccumulateOptions{ServiceType: constant.ServiceTypeSpeechToText}); err != nil {
		t.Fatalf("accumulate: %v", err)
	}
	got := fetchHourly(t, d)["2026-06-29T09:00:00Z|self|whisper-large-v3|seconds"]
	if got.InputCount != 120 || got.Unit != "seconds" {
		t.Errorf("STT hourly row = %+v, want in=120 unit=seconds (seconds preserved, not zeroed)", got)
	}
}

// TestSumHourlyUsageByModel_UTC8Window verifies the reconciliation query sums the exact
// hours inside a UTC+8 day and excludes hours outside it.
func TestSumHourlyUsageByModel_UTC8Window(t *testing.T) {
	d := setupTestDB(t)
	migrateUsageTables(t, d)

	// UTC+8 "2026-06-29" == UTC [2026-06-28T16:00Z, 2026-06-29T16:00Z).
	inside1 := time.Date(2026, 6, 28, 16, 0, 0, 0, time.UTC) // first hour inside
	inside2 := time.Date(2026, 6, 29, 15, 0, 0, 0, time.UTC) // last hour inside
	before := time.Date(2026, 6, 28, 15, 0, 0, 0, time.UTC)  // just before window
	after := time.Date(2026, 6, 29, 16, 0, 0, 0, time.UTC)   // first hour of next UTC+8 day
	rows := []model.HourlyUsageStat{
		{Hour: inside1, Upstream: "minimax", Model: "MiniMax-M3", Unit: "tokens", ServiceType: "chatbot", RequestCount: 1, InputCount: 100, OutputCount: 40},
		{Hour: inside2, Upstream: "minimax", Model: "MiniMax-M3", Unit: "tokens", ServiceType: "chatbot", RequestCount: 1, InputCount: 200, OutputCount: 60},
		{Hour: before, Upstream: "minimax", Model: "MiniMax-M3", Unit: "tokens", ServiceType: "chatbot", RequestCount: 1, InputCount: 999, OutputCount: 999},
		{Hour: after, Upstream: "minimax", Model: "MiniMax-M3", Unit: "tokens", ServiceType: "chatbot", RequestCount: 1, InputCount: 777, OutputCount: 777},
	}
	if err := d.db.Create(&rows).Error; err != nil {
		t.Fatalf("seed hourly: %v", err)
	}

	start := time.Date(2026, 6, 28, 16, 0, 0, 0, time.UTC)
	end := time.Date(2026, 6, 29, 16, 0, 0, 0, time.UTC)
	sums, err := d.SumHourlyUsageByModel("minimax", start, end)
	if err != nil {
		t.Fatalf("sum: %v", err)
	}
	if len(sums) != 1 {
		t.Fatalf("groups = %d, want 1", len(sums))
	}
	got := sums[0]
	if got.InputCount != 300 || got.OutputCount != 100 || got.RequestCount != 2 {
		t.Errorf("sum = %+v, want in=300 out=100 req=2 (excludes before/after window)", got)
	}
}

// TestPruneHourlyUsageStat verifies retention: 0 is a no-op, and a bounded retention
// drops ancient rows while keeping recent ones.
func TestPruneHourlyUsageStat(t *testing.T) {
	d := setupTestDB(t)
	migrateUsageTables(t, d)

	old := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	recent := time.Now().UTC().Truncate(time.Hour)
	rows := []model.HourlyUsageStat{
		{Hour: old, Upstream: "minimax", Model: "m", Unit: "tokens", RequestCount: 1},
		{Hour: recent, Upstream: "minimax", Model: "m", Unit: "tokens", RequestCount: 1},
	}
	if err := d.db.Create(&rows).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}
	if removed, err := d.PruneHourlyUsageStat(0); err != nil || removed != 0 {
		t.Errorf("prune(0) = (%d, %v), want (0, nil)", removed, err)
	}
	removed, err := d.PruneHourlyUsageStat(90)
	if err != nil {
		t.Fatalf("prune(90): %v", err)
	}
	if removed != 1 {
		t.Errorf("prune(90) removed = %d, want 1 (the 2020 row)", removed)
	}
	var remaining int64
	d.db.Model(&model.HourlyUsageStat{}).Count(&remaining)
	if remaining != 1 {
		t.Errorf("remaining = %d, want 1", remaining)
	}
}
