// Package pricefeedtest provides test doubles for the pricefeed package.
// It lives in a separate package so production code cannot accidentally
// import the mocks.
package pricefeedtest

import (
	"context"
	"fmt"
	"math/big"
	"sync"
)

// MockSource is a test double that returns a preconfigured rate or error on
// each FetchRate.  Use SetRate / SetError between calls to simulate live
// feeds.  Safe for concurrent use.
type MockSource struct {
	mu        sync.Mutex
	name      string
	rate      *big.Rat
	err       error
	calls     int
	failFirst int // Return err for the first failFirst calls, then rate.
}

// NewMockSource constructs a mock with the given name and initial rate.  Pass
// nil for rate to start in error state; callers must then SetRate or SetError
// before the first Aggregate call.
func NewMockSource(name string, rate *big.Rat) *MockSource {
	return &MockSource{name: name, rate: rate}
}

func (m *MockSource) Name() string { return m.name }

func (m *MockSource) FetchRate(ctx context.Context) (*big.Rat, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls++
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// Transient-failure mode: return err for the first failFirst calls.
	// Tests use this to exercise retry paths deterministically rather
	// than racing with real sleeps.
	if m.failFirst > 0 {
		m.failFirst--
		if m.err != nil {
			return nil, m.err
		}
		return nil, fmt.Errorf("mock %s: transient failure %d remaining", m.name, m.failFirst)
	}
	if m.err != nil {
		return nil, m.err
	}
	if m.rate == nil {
		return nil, fmt.Errorf("mock %s: no rate set", m.name)
	}
	return new(big.Rat).Set(m.rate), nil
}

// SetRate updates the rate returned by subsequent FetchRate calls and clears
// any pending error.
func (m *MockSource) SetRate(r *big.Rat) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rate = new(big.Rat).Set(r)
	m.err = nil
}

// SetError makes subsequent FetchRate calls return err until SetRate is
// called.
func (m *MockSource) SetError(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.err = err
}

// Calls returns the total number of FetchRate invocations.  Useful for
// assertions in aggregator/processor tests.
func (m *MockSource) Calls() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls
}

// SetFailFirst makes the next n FetchRate calls return an error (using
// whatever SetError configured, or a generic "transient failure" error)
// before reverting to the configured rate.  Used to test retry loops
// deterministically — no timing races, no sleeps in the test.
func (m *MockSource) SetFailFirst(n int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.failFirst = n
}
