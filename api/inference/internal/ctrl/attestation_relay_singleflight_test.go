package ctrl

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// When the cached document expires, every caller that notices must not each
// fetch it: one fetches, the rest reuse its answer. Without the single-flight
// the relay is an amplifier pointed at the assay on exactly the moment the
// cache stops protecting it.
func TestAssayRelaySingleFlight(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		time.Sleep(50 * time.Millisecond) // long enough for every caller to pile up
		_, _ = w.Write([]byte(`{"doc":1}`))
	}))
	defer srv.Close()

	c := &Ctrl{
		assayVerifierURL: srv.URL,
		httpClient:       srv.Client(),
		assayAttCache:    &assayAttestationCache{ttl: time.Minute},
	}
	burst := func() {
		var wg sync.WaitGroup
		for i := 0; i < 20; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				body, _, err := c.AssayAttestation(context.Background(), false)
				if err != nil || string(body) != `{"doc":1}` {
					t.Errorf("body=%s err=%v", body, err)
				}
			}()
		}
		wg.Wait()
	}

	burst()
	if n := hits.Load(); n != 1 {
		t.Fatalf("cold cache: %d fetches for 20 concurrent callers, want 1", n)
	}

	// Expire it and go again: exactly one more fetch, not one per caller.
	c.assayAttCache.mu.Lock()
	c.assayAttCache.fetchedAt = time.Now().Add(-2 * time.Minute)
	c.assayAttCache.mu.Unlock()
	burst()
	if n := hits.Load(); n != 2 {
		t.Fatalf("expired cache: %d fetches total, want 2", n)
	}
}
