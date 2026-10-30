//go:build integration

package ctrl

import (
	"context"
	"testing"

	"github.com/0glabs/0g-serving-broker/common/config"
	"github.com/0glabs/0g-serving-broker/common/log"
)

type countingHooks struct {
	noHooks
	blocked bool
	after   int
}

func (h *countingHooks) settlementBlocked() (bool, string) { return h.blocked, "test" }
func (h *countingHooks) afterSettlement(context.Context)   { h.after++ }

// For spml afterSettlement is the invoice retry. It has to run on a round that
// settled nothing — a failed invoice must not wait for new traffic — and must
// not run on a blocked round, which is meant to leave everything untouched.
func TestSettlementRoundRunsAfterSettlementEvenWhenEmpty(t *testing.T) {
	c := setupReconcileCtrl(t)
	if err := c.db.Migrate(); err != nil { // the real migration set: request, user, video tables, assay_payout
		t.Fatal(err)
	}
	logger, err := log.GetLogger(&config.LoggerConfig{Format: log.TextLogFormat, Level: "error"})
	if err != nil {
		t.Fatal(err)
	}
	c.logger = logger
	c.videoJobOwnerDB = c.db
	hooks := &countingHooks{}
	c.hooks = hooks

	if err := c.SettleFeesWithTEE(context.Background()); err != nil {
		t.Fatalf("empty round: %v", err)
	}
	if hooks.after != 1 {
		t.Fatalf("afterSettlement ran %d times on an empty round, want 1", hooks.after)
	}

	hooks.blocked = true
	if err := c.SettleFeesWithTEE(context.Background()); err != nil {
		t.Fatalf("blocked round: %v", err)
	}
	if hooks.after != 1 {
		t.Fatalf("afterSettlement ran on a blocked round (%d total)", hooks.after)
	}
}
