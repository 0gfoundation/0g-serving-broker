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
// from RefreshDerived, where a nil rate is a no-op.
//
// Set no longer "gets away with" nil the way it once did. The overlay in
// GetCachedService used to read the stored wei pair, so a rate-less cache was
// still billable; it now derives from the rate like every other path, so a nil
// rate here produces a cache that passes Populated and IsStale but fails
// closed on the first request with ErrPricingUnavailable. That is the correct
// outcome — we cannot state a price — but it makes the nil affordance purely a
// test convenience. No production caller passes nil: Bootstrap errors rather
// than return a nil rate, and every tick path has one in hand before it gets
// here.
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

// RefreshDerived stores the freshly derived pair and rate WITHOUT marking them
// published. For the tick whose chain write failed.
//
// It deliberately breaks the old cache.wei == on-chain invariant and replaces
// it with a sharper one: the cached pair is always the current derivation, and
// LastChainSync says when the chain last agreed.
//
// Since the service-level overlay in ctrl.GetCachedService moved to deriving
// from the rate, no production billing path reads InputPriceWei/OutputPriceWei
// any more — every path converts from RateUSDPerOG. The pair is kept because
// Set records the published values into it and LastChainSync/ChainSyncLag are
// defined against that publish; storing the fresh derivation here rather than
// the stale published one keeps the field truthful for anything that inspects
// it (status output, future metrics), but nothing is billed from it. If that
// ever changes, the rate is the value to trust, not this pair.
//
// A nil rate or nil pair is a no-op, not a refresh: an incomplete tick must not
// advance LastUpdate, or it would pass the staleness gate with nothing behind
// it and turn a clear PRICING_UNAVAILABLE into a per-request failure deeper in.
//
// It also refuses to populate a cache that has never been published. Populated
// means "this service has a registered price", which main.go guarantees before
// serving by panicking if the bootstrap sync fails — so this state is
// unreachable in production, and keeping the meaning intact is free. Without
// the guard a never-published service could start billing while ChainSyncLag
// read 0 (its zero LastChainSync is indistinguishable from "in sync"), hiding
// exactly the condition the field exists to show.
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
