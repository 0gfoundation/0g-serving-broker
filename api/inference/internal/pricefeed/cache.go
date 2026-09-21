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
// The rate is NOT informational. Multi-model providers bill through
// ctrl.modelUSDPricesToWei, which converts each model's configured USD price
// at RateUSDPerOG — so the rate is the authoritative billing input for them,
// and the wei pair is the single max() across models that gets published on
// chain for discovery. (This comment used to say fee calculation never reads
// the rate back; that stopped being true when multi-model pricing landed.)
type Cache struct {
	mu sync.RWMutex

	inputPriceWei  *big.Int
	outputPriceWei *big.Int
	rateUSDPerOG   *big.Rat
	lastUpdate     time.Time
	lastChainSync  time.Time
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
	// LastChainSync is when the wei pair was last successfully PUBLISHED on
	// chain. LastUpdate, by contrast, is when the pair was last derived — the
	// two diverge whenever a chain write fails while the rate feed keeps
	// working, which is the state that used to stop the provider serving.
	//
	// The pair itself always reflects the latest rate, so billing reads it
	// without consulting this field. Readers must NOT gate requests on it: a
	// lagging chain means the advertised number is behind, not that we have
	// lost track of the price. It is here so the lag is alertable.
	LastChainSync time.Time
	Populated     bool
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
		LastChainSync:  c.lastChainSync,
		Populated:      true,
	}
	if c.rateUSDPerOG != nil {
		snap.RateUSDPerOG = new(big.Rat).Set(c.rateUSDPerOG)
	}
	return snap
}

// Set replaces the cached prices and rate and stamps BOTH clocks — LastUpdate
// and LastChainSync. That second stamp is the whole difference from
// RefreshDerived, so call Set only when the pair is known to match the chain.
//
// Both of the syncer's success paths qualify. A push writes the derived pair,
// and a drift-skip returns the prior on-chain pair for the caller to adopt; in
// either case what lands here is what is registered, so LastChainSync is
// honest and ChainSyncLag correctly reads zero.
//
// Intended for the processor only; fee-computation code must not call this.
//
// rate may be nil for callers that don't track it (e.g. tests), which wipes
// the cached rate while still marking everything fresh — note this differs
// from RefreshDerived, where a nil rate is a no-op. Set can afford it because
// it always leaves a usable wei pair behind; RefreshDerived cannot, because
// the rate is its entire contribution. No production caller passes nil:
// Bootstrap errors rather than return a nil rate, and every tick path has one
// in hand before it gets here.
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
	c.lastChainSync = at
}

// RefreshDerived stores the freshly derived prices WITHOUT marking them
// published. For the tick whose chain write failed.
//
// It deliberately breaks the old cache.wei == on-chain invariant, and replaces
// it with a sharper one: the cached pair is always what we would charge, and
// LastChainSync says when the chain last agreed. Holding the previously
// published pair instead — the first version of this change — looked safer but
// silently mis-bills: GetBillingPrices falls back to GetCachedService for
// single-model services AND for any multi-model request whose model does not
// resolve, and that path bills straight off the cached wei. Freezing it while
// LastUpdate kept advancing would have charged a stale price indefinitely
// rather than failing closed, which is worse than the outage this set out to
// fix — a wrong bill is not a degraded service.
//
// Nothing needs the cached pair to equal the chain. The contract never reads
// the price at settlement; ProcessSettlement uses it for a batching threshold;
// and the drift check in SyncServiceWithPrices compares against the value it
// reads from the contract, not against this cache.
//
// A nil rate is a no-op, not a refresh: the rate is what makes the derived
// pair meaningful, so accepting nil would advance LastUpdate with nothing
// behind it and turn a clear PRICING_UNAVAILABLE into a per-request failure
// deeper in conversion.
//
// It also refuses to populate a cache that has never been published. Populated
// means "this service has a registered price", which main.go guarantees before
// serving by panicking if the bootstrap sync fails — so this state is
// unreachable in production, and keeping the meaning intact is free. Without
// the guard a never-published service could start billing off a derived pair
// while ChainSyncLag read 0 (its zero LastChainSync is indistinguishable from
// "in sync"), hiding exactly the condition the field exists to show.
func (c *Cache) RefreshDerived(inputWei, outputWei *big.Int, rate *big.Rat, at time.Time) {
	if rate == nil || inputWei == nil || outputWei == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.inputPriceWei == nil || c.outputPriceWei == nil {
		return
	}
	c.inputPriceWei = new(big.Int).Set(inputWei)
	c.outputPriceWei = new(big.Int).Set(outputWei)
	c.rateUSDPerOG = new(big.Rat).Set(rate)
	c.lastUpdate = at
}

// ChainSyncLag reports how far the published on-chain pair trails the derived
// one. Zero
// when they were last written together (or when nothing is populated yet).
func (s Snapshot) ChainSyncLag() time.Duration {
	if !s.Populated || s.LastChainSync.IsZero() || !s.LastUpdate.After(s.LastChainSync) {
		return 0
	}
	return s.LastUpdate.Sub(s.LastChainSync)
}

// IsStale reports whether the last successful refresh is older than threshold.
// An empty cache is considered stale.
func (s Snapshot) IsStale(threshold time.Duration, now time.Time) bool {
	if !s.Populated {
		return true
	}
	return now.Sub(s.LastUpdate) > threshold
}
