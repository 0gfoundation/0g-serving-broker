//go:build integration

package ctrl

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	tcmysql "github.com/testcontainers/testcontainers-go/modules/mysql"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/schema"

	"github.com/0glabs/0g-serving-broker/inference/internal/db"
	"github.com/0glabs/0g-serving-broker/inference/model"
)

// setupReconcileCtrl spins up a real MySQL, migrates the hourly rollup table, and returns a
// Ctrl wired to it. This exercises the full Reconcile seam — the real GORM Scan in
// SumHourlyUsageByModel populating db.HourlyUsageSum, which buildReconciliationReport then
// consumes — that the split unit tests (hand-built structs on each side) cannot catch.
func setupReconcileCtrl(t *testing.T) *Ctrl {
	t.Helper()
	ctx := context.Background()

	container, err := tcmysql.Run(ctx, "mysql:8.0",
		tcmysql.WithDatabase("testdb"),
		tcmysql.WithUsername("test"),
		tcmysql.WithPassword("test"),
	)
	if err != nil {
		t.Fatalf("start mysql container: %v", err)
	}
	t.Cleanup(func() { testcontainers.CleanupContainer(t, container) })

	host, err := container.Host(ctx)
	if err != nil {
		t.Fatalf("get container host: %v", err)
	}
	port, err := container.MappedPort(ctx, "3306/tcp")
	if err != nil {
		t.Fatalf("get container port: %v", err)
	}

	dsn := fmt.Sprintf("test:test@tcp(%s:%s)/testdb?charset=utf8mb4&parseTime=True&loc=Local", host, port.Port())
	gdb, err := gorm.Open(mysql.Open(dsn), &gorm.Config{
		NamingStrategy: schema.NamingStrategy{SingularTable: true},
	})
	if err != nil {
		t.Fatalf("connect to mysql: %v", err)
	}
	if err := gdb.AutoMigrate(&model.HourlyUsageStat{}); err != nil {
		t.Fatalf("auto-migrate: %v", err)
	}
	return &Ctrl{db: db.NewTestDB(gdb)}
}

// TestReconcile_Integration seeds the hourly rollup and runs the full Reconcile() against a
// real DB: window resolution → filtered/grouped SQL query with real Scan → report
// aggregation. A drift in the db.HourlyUsageSum field mapping (rename/reorder/tag) would
// silently pass both split unit tests but fail here.
func TestReconcile_Integration(t *testing.T) {
	c := setupReconcileCtrl(t)

	seed := func(r model.HourlyUsageStat) {
		if err := c.db.AccumulateHourlyUsage(r); err != nil {
			t.Fatalf("seed hourly row: %v", err)
		}
	}
	// UTC+8 "2026-06-29" == UTC [2026-06-28T16:00Z, 2026-06-29T16:00Z).
	seed(model.HourlyUsageStat{Hour: time.Date(2026, 6, 28, 16, 0, 0, 0, time.UTC), Upstream: "minimax", Model: "MiniMax-M3", Unit: "tokens", ServiceType: "chatbot", RequestCount: 3, InputCount: 300, OutputCount: 90, CachedInputTokens: 30, CacheWriteInputTokens: 6})
	seed(model.HourlyUsageStat{Hour: time.Date(2026, 6, 29, 15, 0, 0, 0, time.UTC), Upstream: "minimax", Model: "glm-5", Unit: "tokens", ServiceType: "chatbot", RequestCount: 2, InputCount: 200, OutputCount: 40})
	// Out of window (first hour of the next UTC+8 day) — must be excluded.
	seed(model.HourlyUsageStat{Hour: time.Date(2026, 6, 29, 16, 0, 0, 0, time.UTC), Upstream: "minimax", Model: "MiniMax-M3", Unit: "tokens", ServiceType: "chatbot", RequestCount: 99, InputCount: 9999})
	// Different upstream, in window — must be excluded by the upstream filter.
	seed(model.HourlyUsageStat{Hour: time.Date(2026, 6, 29, 1, 0, 0, 0, time.UTC), Upstream: "aliyun", Model: "qwen", Unit: "tokens", ServiceType: "chatbot", RequestCount: 7, InputCount: 700})

	rep, err := c.Reconcile("minimax", "2026-06-29", "2026-06-29", "+08:00")
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	if rep.WindowStartUTC != "2026-06-28T16:00:00Z" || rep.WindowEndUTC != "2026-06-29T16:00:00Z" {
		t.Errorf("window = [%s, %s), want the UTC+8 day", rep.WindowStartUTC, rep.WindowEndUTC)
	}
	if len(rep.Upstreams) != 1 || rep.Upstreams[0].Upstream != "minimax" {
		t.Fatalf("upstreams = %+v, want exactly [minimax] (aliyun filtered out)", rep.Upstreams)
	}
	up := rep.Upstreams[0]
	tok := up.TotalsByUnit["tokens"]
	if tok == nil || tok.RequestCount != 5 || tok.InputCount != 500 || tok.OutputCount != 130 || tok.CachedInputTokens != 30 || tok.CacheWriteInputTokens != 6 {
		t.Errorf("tokens totals = %+v, want req=5 in=500 out=130 cached=30 write=6 (16:00Z row excluded)", tok)
	}
	if up.TotalRequests != 5 {
		t.Errorf("TotalRequests = %d, want 5", up.TotalRequests)
	}
	if len(up.PerModel) != 2 {
		t.Errorf("PerModel = %d rows, want 2 (MiniMax-M3, glm-5)", len(up.PerModel))
	}
	// Every per-model row must carry the fields the report/Scan mapping depends on.
	for _, m := range up.PerModel {
		if m.Upstream != "minimax" || m.Unit != "tokens" || m.ServiceType != "chatbot" || m.Model == "" {
			t.Errorf("per-model row not fully populated by Scan: %+v", m)
		}
	}
}

// TestReconcile_Integration_RateClass runs the full Reconcile() against a real DB with rows
// that differ only by rate_class, asserting the cost dimension survives the query→Scan→report
// seam: the same (model, unit) split into one per-model row per tier, while the unit total sums
// across tiers.
func TestReconcile_Integration_RateClass(t *testing.T) {
	c := setupReconcileCtrl(t)
	h := time.Date(2026, 6, 29, 3, 0, 0, 0, time.UTC)
	seed := func(rateClass string, req, in int64) {
		row := model.HourlyUsageStat{Hour: h, Upstream: "minimax", Model: "MiniMax-M3", Unit: "tokens", RateClass: rateClass, ServiceType: "chatbot", RequestCount: req, InputCount: in}
		if err := c.db.AccumulateHourlyUsage(row); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	seed("tier:<=32000", 4, 400)
	seed("tier:unbounded", 1, 90000)

	rep, err := c.Reconcile("minimax", "2026-06-29", "2026-06-29", "Z")
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(rep.Upstreams) != 1 {
		t.Fatalf("upstreams = %d, want 1", len(rep.Upstreams))
	}
	up := rep.Upstreams[0]
	if tok := up.TotalsByUnit["tokens"]; tok == nil || tok.RequestCount != 5 || tok.InputCount != 90400 {
		t.Errorf("tokens totals = %+v, want req=5 in=90400 (summed across tiers)", up.TotalsByUnit["tokens"])
	}
	if len(up.PerModel) != 2 {
		t.Fatalf("PerModel = %d, want 2 (one per rate_class)", len(up.PerModel))
	}
	byClass := map[string]int64{}
	for _, m := range up.PerModel {
		byClass[m.RateClass] = m.InputCount
	}
	if byClass["tier:<=32000"] != 400 || byClass["tier:unbounded"] != 90000 {
		t.Errorf("per-tier input = %v, want tier:<=32000=400 tier:unbounded=90000", byClass)
	}
}

// TestReconcile_Integration_AllUpstreams verifies the no-upstream (fleet) query groups by
// upstream against a real DB.
func TestReconcile_Integration_AllUpstreams(t *testing.T) {
	c := setupReconcileCtrl(t)
	h := time.Date(2026, 6, 29, 3, 0, 0, 0, time.UTC) // inside UTC "2026-06-29"
	if err := c.db.AccumulateHourlyUsage(model.HourlyUsageStat{Hour: h, Upstream: "minimax", Model: "MiniMax-M3", Unit: "tokens", ServiceType: "chatbot", RequestCount: 1, InputCount: 100}); err != nil {
		t.Fatal(err)
	}
	if err := c.db.AccumulateHourlyUsage(model.HourlyUsageStat{Hour: h, Upstream: "aliyun", Model: "qwen", Unit: "tokens", ServiceType: "chatbot", RequestCount: 2, InputCount: 200}); err != nil {
		t.Fatal(err)
	}

	rep, err := c.Reconcile("", "2026-06-29", "2026-06-29", "Z")
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(rep.Upstreams) != 2 {
		t.Fatalf("upstreams = %d, want 2 (all)", len(rep.Upstreams))
	}
	// Sorted by name: aliyun then minimax.
	if rep.Upstreams[0].Upstream != "aliyun" || rep.Upstreams[1].Upstream != "minimax" {
		t.Errorf("order = [%s, %s], want [aliyun, minimax]", rep.Upstreams[0].Upstream, rep.Upstreams[1].Upstream)
	}
}
