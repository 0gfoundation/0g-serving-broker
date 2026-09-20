package ctrl

import (
	"testing"
	"time"
)

func newTestMismatchLimiter() *ModelMismatchLimiter {
	return &ModelMismatchLimiter{
		users:  make(map[mismatchKey]*UserMismatchInfo),
		limit:  5,
		window: 5 * time.Minute,
		block:  1 * time.Hour,
	}
}

// A block must not spread from the model that was spammed to one that works.
//
// This is the 2026-09-20 incident in miniature: the key used to be the address
// alone, so gpt-4o probes from the router's (single, shared) address took
// gpt-5.6-luna down with them for an hour. If the key ever regresses to
// address-only, the luna assertions below fail.
func TestModelMismatchBlockIsScopedToTheRequestedModel(t *testing.T) {
	ml := newTestMismatchLimiter()
	const addr = "0xBB3f5b0b5062CB5B3245222C5917afD1f6e13aF6"

	for i := 1; i <= 5; i++ {
		shouldBlock, _ := ml.RecordModelMismatch(addr, "openai/gpt-4o")
		if want := i >= 5; shouldBlock != want {
			t.Fatalf("attempt %d: shouldBlock = %v, want %v", i, shouldBlock, want)
		}
	}

	if blocked, _ := ml.IsBlocked(addr, "openai/gpt-4o"); !blocked {
		t.Error("the spammed model should be blocked")
	}
	if blocked, _ := ml.IsBlocked(addr, "openai/gpt-5.6-luna"); blocked {
		t.Error("a model this caller never mis-named must stay reachable")
	}
	if blocked, _ := ml.IsBlocked("0xsomeone-else", "openai/gpt-4o"); blocked {
		t.Error("the block must not reach another caller")
	}
}

// Counting is per pair too: four misses on each of two models is eight total
// but must block neither, or the address-wide behaviour survives in the count.
func TestModelMismatchCountIsPerPair(t *testing.T) {
	ml := newTestMismatchLimiter()
	const addr = "0xabc"

	for i := 0; i < 4; i++ {
		if shouldBlock, _ := ml.RecordModelMismatch(addr, "model-a"); shouldBlock {
			t.Fatalf("model-a attempt %d blocked below the limit", i+1)
		}
		if shouldBlock, _ := ml.RecordModelMismatch(addr, "model-b"); shouldBlock {
			t.Fatalf("model-b attempt %d blocked below the limit", i+1)
		}
	}
	for _, m := range []string{"model-a", "model-b"} {
		if blocked, _ := ml.IsBlocked(addr, m); blocked {
			t.Errorf("%s blocked after 4 misses, limit is 5", m)
		}
	}
}
