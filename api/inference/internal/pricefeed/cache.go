package pricefeed

import (
	"math/big"
	"sync"
	"time"
)

// Cache holds the most recently computed wei prices for the USD-denominated
// service, along with the rate they were derived from.  Readers on the
// request-billing hot path call Get and must handle the "not yet populated"
// and "stale" cases.  The PriceUpdateProcessor is the sole writer; Set is
// called once per successful tick.
//
// The stored rate is informational only — it's surfaced in the /v1/models
// response so SDK clients can display the rate, but the authoritative
// billing unit is always wei.  Fee calculation never reads the rate back
// from the cache.
type Cache struct {
	mu sync.RWMutex

	inputPriceWei  *big.Int
	outputPriceWei *big.Int
	rateUSDPerOG   *big.Rat
	lastUpdate     time.Time
}

// NewCache returns an empty cache.  Get will report populated=false until the
// first successful Set.
func NewCache() *Cache {
	return &Cache{}
}

// Snapshot is a read-only view of the cache at a point in time.  The big.Int
// and big.Rat values are fresh copies so callers may mutate them without
// affecting the cache.  Populated is false iff the cache has never been
// written.
type Snapshot struct {
	InputPriceWei  *big.Int
	OutputPriceWei *big.Int
	RateUSDPerOG   *big.Rat
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
	snap := Snapshot{
		InputPriceWei:  new(big.Int).Set(c.inputPriceWei),
		OutputPriceWei: new(big.Int).Set(c.outputPriceWei),
		LastUpdate:     c.lastUpdate,
		Populated:      true,
	}
	if c.rateUSDPerOG != nil {
		snap.RateUSDPerOG = new(big.Rat).Set(c.rateUSDPerOG)
	}
	return snap
}

// Set replaces the cached prices, rate, and update time.  Intended for the
// processor only; fee-computation code must not call this.  rate may be nil
// for callers that don't track it (e.g. tests); readers must handle that.
func (c *Cache) Set(inputWei, outputWei *big.Int, rate *big.Rat, at time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.inputPriceWei = new(big.Int).Set(inputWei)
	c.outputPriceWei = new(big.Int).Set(outputWei)
	if rate != nil {
		c.rateUSDPerOG = new(big.Rat).Set(rate)
	} else {
		c.rateUSDPerOG = nil
	}
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
