//go:build integration

package event

import (
	"context"
	"fmt"
	"math/big"
	"os"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/testcontainers/testcontainers-go"
	tcmysql "github.com/testcontainers/testcontainers-go/modules/mysql"

	"github.com/0glabs/0g-serving-broker/inference/config"
	constant "github.com/0glabs/0g-serving-broker/inference/const"
	"github.com/0glabs/0g-serving-broker/inference/internal/db"
	"github.com/0glabs/0g-serving-broker/inference/model"
)

func setupReconcileDB(t *testing.T) *db.DB {
	t.Helper()
	ctx := context.Background()
	image := os.Getenv("TEST_MYSQL_IMAGE") // see db.setupTestDB
	if image == "" {
		image = "mysql:8.0"
	}
	container, err := tcmysql.Run(ctx, image,
		tcmysql.WithDatabase("testdb"), tcmysql.WithUsername("test"), tcmysql.WithPassword("test"))
	if err != nil {
		t.Fatalf("start mysql container: %v", err)
	}
	t.Cleanup(func() { testcontainers.CleanupContainer(t, container) })
	host, _ := container.Host(ctx)
	port, _ := container.MappedPort(ctx, "3306/tcp")
	cfg := &config.Config{}
	cfg.Database.DSN = fmt.Sprintf("test:test@tcp(%s:%s)/testdb?charset=utf8mb4&parseTime=True&loc=Local", host, port.Port())
	d, err := db.NewDB(cfg)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	if err := d.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return d
}

func node0Cumulative(t *testing.T, d *db.DB) string {
	t.Helper()
	states, err := d.ListAssayPayouts()
	if err != nil {
		t.Fatal(err)
	}
	for _, st := range states {
		if st.Node == "node_0" {
			return st.Cumulative
		}
	}
	return "0"
}

// A PARTIAL settlement accrues exactly once (R1-1). h1 fits the settled
// amount, h2 does not. Layer 1 deletes+accrues h1 and releases h2; whether
// it then lives to confirm the row or not, the event process must not fit
// h2 into the same settledAmount and accrue it too.
func TestPartialSettlementAccruesOnce(t *testing.T) {
	d := setupReconcileDB(t)
	opt := &db.AssayPayoutOptions{CutBps: 1000}
	r := &ReconciliationProcessor{db: d, logger: nopLogger{}, assayPayout: opt}
	user := common.HexToAddress("0x1234").Hex()
	tx1, tx2 := common.HexToHash("0x01"), common.HexToHash("0x02")
	now := time.Now()
	for i, fee := range []string{"600", "400"} {
		if err := d.CreateRequest(model.Request{Model: model.Model{CreatedAt: &now}, UserAddress: user, Nonce: fmt.Sprint(i),
			RequestHash: fmt.Sprintf("h%d", i+1), Fee: fee, Node: "node_0", Verdict: constant.AssayVerdictPass, Settling: true}); err != nil {
			t.Fatal(err)
		}
	}
	ps := &model.PendingSettlement{UserAddress: user, TotalFee: "1000", Nonce: "1", RequestHashes: `["h1","h2"]`, Status: "pending", SubmittedBlock: 1, TxHash: tx1.Hex()}
	if err := d.CreatePendingSettlement(ps); err != nil {
		t.Fatal(err)
	}
	evt := settlementEvent{User: common.HexToAddress(user), Status: 1, UnsettledAmount: big.NewInt(400), TxHash: tx1}

	// Layer 1: on-chain PARTIAL with 600 settled -> h1 paid, h2 released.
	if err := d.DeleteSettledRequestsByHashes([]string{"h1"}, opt); err != nil {
		t.Fatal(err)
	}
	if err := d.MarkRequestsSettling([]string{"h2"}, false); err != nil {
		t.Fatal(err)
	}
	if got := node0Cumulative(t, d); got != "540" {
		t.Fatalf("after layer 1 cumulative = %s, want 540", got)
	}

	// Broker died before confirming: Layer 2 sees the row still pending with
	// one of two rows gone. It must not accrue h2.
	r.processEvent(evt)
	if got := node0Cumulative(t, d); got != "540" {
		t.Fatalf("layer 2 re-accrued the remainder: cumulative = %s, want 540", got)
	}
	left, _ := d.GetRequestsByHashes([]string{"h1", "h2"})
	if len(left) != 1 || left[0].RequestHash != "h2" || left[0].Settling {
		t.Fatalf("left = %+v, want h2 released", left)
	}
	if p, _ := d.FindPendingSettlementByTxHash(tx1.Hex(), user); p != nil {
		t.Fatalf("pending row not confirmed: %+v", p)
	}

	// Broker lived: it confirmed the row itself, so the event is a no-op.
	ps2 := &model.PendingSettlement{UserAddress: user, TotalFee: "1000", Nonce: "2", RequestHashes: `["h2","h3"]`, Status: "pending", SubmittedBlock: 1, TxHash: tx2.Hex()}
	if err := d.CreatePendingSettlement(ps2); err != nil {
		t.Fatal(err)
	}
	if err := d.UpdatePendingSettlementStatus(ps2.ID, "confirmed"); err != nil {
		t.Fatal(err)
	}
	evt.TxHash = tx2
	r.processEvent(evt)
	if got := node0Cumulative(t, d); got != "540" {
		t.Fatalf("confirmed row reconciled again: cumulative = %s, want 540", got)
	}
	left, _ = d.GetRequestsByHashes([]string{"h2"})
	if len(left) != 1 {
		t.Fatalf("h2 deleted by a confirmed row's event")
	}
}
