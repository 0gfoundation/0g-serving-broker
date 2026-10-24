package event

import (
	"context"
	"errors"
	"testing"
)

// A window wider than the RPC's log limit must shrink instead of pinning the
// cursor: every block gets committed exactly once, in order.
func TestWalkBlockRangeHalvesOversizedWindows(t *testing.T) {
	const limit = 7 // widest window the fake RPC accepts
	var committed []uint64
	scan := func(_ context.Context, from, to uint64) ([]settlementEvent, error) {
		if to-from+1 > limit {
			return nil, errors.New("the result set exceeds the max limit of 10000 logs")
		}
		return nil, nil
	}
	commit := func(_ []settlementEvent, from, to uint64) error {
		for b := from; b <= to; b++ {
			committed = append(committed, b)
		}
		return nil
	}

	if err := walkBlockRange(context.Background(), 101, 200, 50, scan, commit); err != nil {
		t.Fatalf("walk: %v", err)
	}
	if len(committed) != 100 {
		t.Fatalf("committed %d blocks, want 100", len(committed))
	}
	for i, b := range committed {
		if b != uint64(101+i) {
			t.Fatalf("block %d committed out of order: got %d", i, b)
		}
	}
}

// A single block that still fails is a real error, not something to halve.
func TestWalkBlockRangeGivesUpOnSingleBlock(t *testing.T) {
	scan := func(context.Context, uint64, uint64) ([]settlementEvent, error) {
		return nil, errors.New("rpc down")
	}
	commit := func([]settlementEvent, uint64, uint64) error {
		t.Fatal("commit must not run when every scan fails")
		return nil
	}
	if err := walkBlockRange(context.Background(), 1, 100, 50, scan, commit); err == nil {
		t.Fatal("want error")
	}
}
