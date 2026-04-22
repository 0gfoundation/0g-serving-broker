package pricefeed

import (
	"math/big"
	"sync"
	"time"
)

// Cache holds the most recently computed wei prices for the USD-denominated
// service.  Readers on the request-billing hot path call Get and must handle
// the "not yet populated" and "stale" cases.  The PriceUpdateProcessor is the
// sole writer; Set is called once per successful tick.
//
// The rate itself is deliberately not stored — it's a transient value
// internal to each update tick.  Only the wei prices derived from it are
// exposed.  Storing the rate would imply it's authoritative, when in reality
// the authoritative billing unit is always wei.
type Cache struct {
	mu sync.RWMutex

	inputPriceWei  *big.Int
	outputPriceWei *big.Int
	lastUpdate     time.Time
}

// NewCache returns an empty cache.  Get will report populated=false until the
// first successful Set.
func NewCache() *Cache {
	return &Cache{}
}

// Snapshot is a read-only view of the cache at a point in time.  The big.Int
// values are fresh copies so callers may mutate them without affecting the
// cache.  Populated is false iff the cache has never been written.
type Snapshot struct {
	InputPriceWei  *big.Int
	OutputPriceWei *big.Int
	LastUpdate     time.Time
	Populated      bool
}

// Get returns a Snapshot of the cache.  Safe for concurrent use.  Callers
// should check Populated before using the prices and compare LastUpdate
// against any applicable staleness threshold.
func (c *Cache) Get() Snapshot {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.inputPriceWei == nil || c.outputPriceWei == nil {
		return Snapshot{}
	}
	return Snapshot{
		InputPriceWei:  new(big.Int).Set(c.inputPriceWei),
		OutputPriceWei: new(big.Int).Set(c.outputPriceWei),
		LastUpdate:     c.lastUpdate,
		Populated:      true,
	}
}

// Set replaces the cached prices and stamps the update time.  Intended for the
// processor only; fee-computation code must not call this.
func (c *Cache) Set(inputWei, outputWei *big.Int, at time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.inputPriceWei = new(big.Int).Set(inputWei)
	c.outputPriceWei = new(big.Int).Set(outputWei)
	c.lastUpdate = at
}

// IsStale reports whether the last successful refresh is older than threshold.
// An empty cache is considered stale.
func (s Snapshot) IsStale(threshold time.Duration, now time.Time) bool {
	if !s.Populated {
		return true
	}
	return now.Sub(s.LastUpdate) > threshold
}
