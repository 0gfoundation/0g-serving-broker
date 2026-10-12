package ctrl

import (
	"testing"
	"time"
)

// The bypass has to actually bypass, and the floor has to actually hold: if
// either half is wrong the endpoint is back to serving a stale document after
// a redeploy, or is the amplifier the cache exists to prevent.
func TestAssayRelayForceFloor(t *testing.T) {
	c := &assayAttestationCache{ttl: 10 * time.Minute}

	decide := func(force bool) bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		if force && time.Since(c.forcedAt) >= AssayAttestationForceFloor {
			c.forcedAt = time.Now()
			return true
		}
		return false
	}

	if !decide(true) {
		t.Fatal("first forced fetch must be allowed")
	}
	if decide(true) {
		t.Error("a second forced fetch inside the floor must be refused")
	}
	if decide(false) {
		t.Error("an unforced request must never count as forced")
	}

	c.forcedAt = time.Now().Add(-AssayAttestationForceFloor - time.Second)
	if !decide(true) {
		t.Error("a forced fetch after the floor has passed must be allowed")
	}
}
