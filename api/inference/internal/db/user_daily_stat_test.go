//go:build integration

package db

import (
	"testing"

	constant "github.com/0glabs/0g-serving-broker/inference/const"
	"github.com/0glabs/0g-serving-broker/inference/model"
)

// migrateUsageTables auto-migrates the tables the per-wallet usage path
// touches onto the shared test DB.
func migrateUsageTables(t *testing.T, d *DB) {
	t.Helper()
	if err := d.db.AutoMigrate(&model.Request{}, &model.UserDailyStat{}, &model.DailyStat{}, &model.HourlyUsageStat{}); err != nil {
		t.Fatalf("auto-migrate usage tables: %v", err)
	}
}

func seedRequest(t *testing.T, d *DB, user, modelName, hash string, in, out int64) {
	t.Helper()
	req := model.Request{
		UserAddress: user,
		Nonce:       hash,
		RequestHash: hash,
		ServiceName: "chatbot",
		ModelName:   modelName,
		InputCount:  in,
		OutputCount: out,
	}
	if err := d.db.Create(&req).Error; err != nil {
		t.Fatalf("seed request %s: %v", hash, err)
	}
}

func fetchUserDailyStat(t *testing.T, d *DB) map[string]model.UserDailyStat {
	t.Helper()
	var rows []model.UserDailyStat
	if err := d.db.Find(&rows).Error; err != nil {
		t.Fatalf("fetch user_daily_stat: %v", err)
	}
	out := make(map[string]model.UserDailyStat, len(rows))
	for _, r := range rows {
		out[r.UserAddress+"|"+r.Model] = r
	}
	return out
}

// TestAccumulatePerWallet verifies the per-wallet upsert aggregates by
// (user, model), deletes the requests, and accumulates across two calls.
func TestAccumulatePerWallet(t *testing.T) {
	d := setupTestDB(t)
	migrateUsageTables(t, d)

	// First settlement batch: two users, alice on two models.
	batch1 := []*model.Request{
		{UserAddress: "0xAlice", ModelName: "deepseek-r1", RequestHash: "h1", InputCount: 10, OutputCount: 20},
		{UserAddress: "0xAlice", ModelName: "deepseek-r1", RequestHash: "h2", InputCount: 5, OutputCount: 7},
		{UserAddress: "0xAlice", ModelName: "qwen3-max", RequestHash: "h3", InputCount: 1, OutputCount: 2},
		{UserAddress: "0xBob", ModelName: "deepseek-r1", RequestHash: "h4", InputCount: 100, OutputCount: 200},
	}
	for _, r := range batch1 {
		seedRequest(t, d, r.UserAddress, r.ModelName, r.RequestHash, r.InputCount, r.OutputCount)
	}
	if err := d.AccumulateAndDeleteRequests(batch1, AccumulateOptions{ServiceType: constant.ServiceTypeChatbot, RecordPerWallet: true, FallbackModel: "fallback-model"}); err != nil {
		t.Fatalf("accumulate batch1: %v", err)
	}

	stats := fetchUserDailyStat(t, d)
	if got := stats["0xAlice|deepseek-r1"]; got.RequestCount != 2 || got.InputTokens != 15 || got.OutputTokens != 27 {
		t.Errorf("alice/deepseek-r1 = %+v, want req=2 in=15 out=27", got)
	}
	if got := stats["0xAlice|qwen3-max"]; got.RequestCount != 1 || got.InputTokens != 1 {
		t.Errorf("alice/qwen3-max = %+v, want req=1 in=1", got)
	}
	if got := stats["0xBob|deepseek-r1"]; got.RequestCount != 1 || got.InputTokens != 100 {
		t.Errorf("bob/deepseek-r1 = %+v, want req=1 in=100", got)
	}

	// Requests must be deleted.
	var remaining int64
	d.db.Model(&model.Request{}).Count(&remaining)
	if remaining != 0 {
		t.Errorf("requests remaining after settlement = %d, want 0", remaining)
	}

	// Second batch on the same day must accumulate onto the existing rows.
	batch2 := []*model.Request{
		{UserAddress: "0xAlice", ModelName: "deepseek-r1", RequestHash: "h5", InputCount: 3, OutputCount: 4},
	}
	seedRequest(t, d, "0xAlice", "deepseek-r1", "h5", 3, 4)
	if err := d.AccumulateAndDeleteRequests(batch2, AccumulateOptions{ServiceType: constant.ServiceTypeChatbot, RecordPerWallet: true, FallbackModel: "fallback-model"}); err != nil {
		t.Fatalf("accumulate batch2: %v", err)
	}
	stats = fetchUserDailyStat(t, d)
	if got := stats["0xAlice|deepseek-r1"]; got.RequestCount != 3 || got.InputTokens != 18 || got.OutputTokens != 31 {
		t.Errorf("after batch2 alice/deepseek-r1 = %+v, want req=3 in=18 out=31", got)
	}
}

// TestAccumulatePerWalletEmptyModelFallback verifies an empty ModelName falls
// back to the provided model id.
func TestAccumulatePerWalletEmptyModelFallback(t *testing.T) {
	d := setupTestDB(t)
	migrateUsageTables(t, d)

	batch := []*model.Request{{UserAddress: "0xAlice", ModelName: "", RequestHash: "e1", InputCount: 9, OutputCount: 1}}
	seedRequest(t, d, "0xAlice", "", "e1", 9, 1)
	if err := d.AccumulateAndDeleteRequests(batch, AccumulateOptions{ServiceType: constant.ServiceTypeChatbot, RecordPerWallet: true, FallbackModel: "single-model"}); err != nil {
		t.Fatalf("accumulate: %v", err)
	}
	stats := fetchUserDailyStat(t, d)
	if got := stats["0xAlice|single-model"]; got.RequestCount != 1 || got.InputTokens != 9 {
		t.Errorf("fallback model row = %+v, want req=1 in=9 under 'single-model'", got)
	}
}

// TestAccumulatePerWalletSTTSkipsTokens verifies speech-to-text records
// request_count but writes 0 to the token columns (input_count carries
// seconds for whisper, not tokens).
func TestAccumulatePerWalletSTTSkipsTokens(t *testing.T) {
	d := setupTestDB(t)
	migrateUsageTables(t, d)

	batch := []*model.Request{
		{UserAddress: "0xAlice", ModelName: "whisper-large-v3", RequestHash: "s1", InputCount: 120, OutputCount: 0},
		{UserAddress: "0xAlice", ModelName: "whisper-large-v3", RequestHash: "s2", InputCount: 90, OutputCount: 0},
	}
	for _, r := range batch {
		seedRequest(t, d, r.UserAddress, r.ModelName, r.RequestHash, r.InputCount, r.OutputCount)
	}
	if err := d.AccumulateAndDeleteRequests(batch, AccumulateOptions{ServiceType: constant.ServiceTypeSpeechToText, RecordPerWallet: true, FallbackModel: "whisper-large-v3"}); err != nil {
		t.Fatalf("accumulate stt: %v", err)
	}
	stats := fetchUserDailyStat(t, d)
	got := stats["0xAlice|whisper-large-v3"]
	if got.RequestCount != 2 {
		t.Errorf("stt request_count = %d, want 2", got.RequestCount)
	}
	if got.InputTokens != 0 || got.OutputTokens != 0 {
		t.Errorf("stt tokens = (%d,%d), want (0,0) — seconds must not be recorded as tokens", got.InputTokens, got.OutputTokens)
	}
}

// TestAccumulatePerWalletDisabled verifies that with recordPerWallet=false no
// user_daily_stat rows are written (requests are still deleted).
func TestAccumulatePerWalletDisabled(t *testing.T) {
	d := setupTestDB(t)
	migrateUsageTables(t, d)

	batch := []*model.Request{{UserAddress: "0xAlice", ModelName: "deepseek-r1", RequestHash: "d1", InputCount: 10, OutputCount: 20}}
	seedRequest(t, d, "0xAlice", "deepseek-r1", "d1", 10, 20)
	if err := d.AccumulateAndDeleteRequests(batch, AccumulateOptions{ServiceType: constant.ServiceTypeChatbot, RecordPerWallet: false, FallbackModel: "fallback"}); err != nil {
		t.Fatalf("accumulate: %v", err)
	}
	var count int64
	d.db.Model(&model.UserDailyStat{}).Count(&count)
	if count != 0 {
		t.Errorf("user_daily_stat rows with feature disabled = %d, want 0", count)
	}
}

// TestListAndPruneUserDailyStat verifies ordering, total, pagination, and TTL.
func TestListAndPruneUserDailyStat(t *testing.T) {
	d := setupTestDB(t)
	migrateUsageTables(t, d)

	// Seed three rows for one date directly, out of order.
	rows := []model.UserDailyStat{
		{Date: "2026-06-22", UserAddress: "0xBob", Model: "m1", RequestCount: 1, InputTokens: 1},
		{Date: "2026-06-22", UserAddress: "0xAlice", Model: "m2", RequestCount: 1, InputTokens: 1},
		{Date: "2026-06-22", UserAddress: "0xAlice", Model: "m1", RequestCount: 1, InputTokens: 1},
		// A stale row far in the past for the prune test.
		{Date: "2020-01-01", UserAddress: "0xAlice", Model: "m1", RequestCount: 1, InputTokens: 1},
	}
	if err := d.db.Create(&rows).Error; err != nil {
		t.Fatalf("seed rows: %v", err)
	}

	page, total, err := d.ListUserDailyStat("2026-06-22", 2, 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if total != 3 {
		t.Errorf("total = %d, want 3", total)
	}
	// Ordered by (user_address, model): alice/m1, alice/m2, then bob/m1.
	if len(page) != 2 || page[0].UserAddress != "0xAlice" || page[0].Model != "m1" || page[1].Model != "m2" {
		t.Errorf("page1 = %+v, want [alice/m1, alice/m2]", page)
	}
	page2, _, err := d.ListUserDailyStat("2026-06-22", 2, 2)
	if err != nil {
		t.Fatalf("list page2: %v", err)
	}
	if len(page2) != 1 || page2[0].UserAddress != "0xBob" {
		t.Errorf("page2 = %+v, want [bob/m1]", page2)
	}

	// Prune with retention=0 must be a no-op (the documented "keep forever"
	// contract), then a 365-day retention must drop the ancient 2020 row while
	// leaving the recent rows untouched — an assertion that holds regardless of
	// the test clock.
	if removed, err := d.PruneUserDailyStat(0); err != nil || removed != 0 {
		t.Errorf("prune(0) = (%d, %v), want (0, nil)", removed, err)
	}
	// 2020-01-01 is comfortably older than 365 days from any plausible test
	// clock, while the 2026 rows are not (this test's data date).
	removed, err := d.PruneUserDailyStat(365)
	if err != nil {
		t.Fatalf("prune(365): %v", err)
	}
	if removed < 1 {
		t.Errorf("prune(365) removed = %d, want >= 1 (the 2020 row)", removed)
	}
	var remaining int64
	d.db.Model(&model.UserDailyStat{}).Where("date = ?", "2020-01-01").Count(&remaining)
	if remaining != 0 {
		t.Errorf("ancient rows remaining = %d, want 0", remaining)
	}
}
