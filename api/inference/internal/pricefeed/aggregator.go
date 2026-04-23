package pricefeed

import (
	"context"
	"fmt"
	"math/big"
	"sort"
	"sync"
	"time"

	"github.com/0glabs/0g-serving-broker/common/log"
)

// Aggregator queries multiple Sources in parallel and reduces their responses
// to a single rate using median + outlier rejection + minimum quorum.
//
// The aggregator is stateless across calls; all timing/cache concerns live in
// the PriceUpdateProcessor that owns it.
type Aggregator struct {
	sources        []Source
	minQuorum      int
	maxDeviationBp int
	httpTimeout    time.Duration
	logger         log.Logger
}

// NewAggregator constructs an aggregator.  minQuorum is the minimum number of
// healthy sources required for Aggregate to return a rate; below this the
// call returns an error and callers should keep their last good cache.
// maxDeviationBp drops any source that deviates from the working median by
// more than this many basis points (1/10000).
func NewAggregator(sources []Source, minQuorum, maxDeviationBp int, httpTimeout time.Duration, logger log.Logger) *Aggregator {
	return &Aggregator{
		sources:        sources,
		minQuorum:      minQuorum,
		maxDeviationBp: maxDeviationBp,
		httpTimeout:    httpTimeout,
		logger:         logger,
	}
}

// Aggregate fans out to every source, collects responses, rejects outliers,
// and returns the median rate together with all per-source quotes (including
// errors and outliers, for logging/metrics consumers).
//
// Algorithm:
//  1. Fan out concurrently, per-source timeout = httpTimeout.
//  2. Drop responses with errors or non-positive rates.
//  3. Compute a working median of the survivors.
//  4. Drop survivors that deviate > maxDeviationBp from the working median.
//  5. Require the remaining set size >= minQuorum.
//  6. Return the median of the remaining set.
func (a *Aggregator) Aggregate(ctx context.Context) (*big.Rat, []SourceQuote, error) {
	quotes := a.fanOut(ctx)

	// Single pass to collect healthy quotes together with their source
	// identities — we need the identity for outlier logging below.
	type healthyQuote struct {
		source string
		rate   *big.Rat
	}
	healthy := make([]healthyQuote, 0, len(quotes))
	rates := make([]*big.Rat, 0, len(quotes))
	for i := range quotes {
		q := quotes[i]
		if q.Err != nil || q.Rate == nil || q.Rate.Sign() <= 0 {
			continue
		}
		healthy = append(healthy, healthyQuote{source: q.Source, rate: q.Rate})
		rates = append(rates, q.Rate)
	}
	if len(healthy) == 0 {
		return nil, quotes, fmt.Errorf("no healthy price-feed sources (queried %d)", len(quotes))
	}

	// Working median, used only to identify outliers.  We rebuild the
	// final median from the post-outlier set.
	working := medianRat(rates)

	kept := make([]*big.Rat, 0, len(healthy))
	for _, hq := range healthy {
		dev := deviationBps(hq.rate, working)
		if dev > a.maxDeviationBp {
			if a.logger != nil {
				a.logger.Warnf("pricefeed: dropping outlier source=%s rate=%s median=%s deviationBps=%d threshold=%d",
					hq.source, hq.rate.FloatString(8), working.FloatString(8),
					dev, a.maxDeviationBp)
			}
			continue
		}
		kept = append(kept, hq.rate)
	}
	if len(kept) < a.minQuorum {
		return nil, quotes, fmt.Errorf("pricefeed quorum not met: %d healthy sources after outlier rejection, need %d", len(kept), a.minQuorum)
	}

	return medianRat(kept), quotes, nil
}

func (a *Aggregator) fanOut(ctx context.Context) []SourceQuote {
	quotes := make([]SourceQuote, len(a.sources))
	var wg sync.WaitGroup
	wg.Add(len(a.sources))
	for i, src := range a.sources {
		go func(i int, src Source) {
			defer wg.Done()
			fetchCtx, cancel := context.WithTimeout(ctx, a.httpTimeout)
			defer cancel()
			rate, err := src.FetchRate(fetchCtx)
			quotes[i] = SourceQuote{
				Source:    src.Name(),
				Rate:      rate,
				Err:       err,
				Timestamp: time.Now(),
			}
			if err != nil && a.logger != nil {
				a.logger.Warnf("pricefeed: source=%s fetch error: %v", src.Name(), err)
			}
		}(i, src)
	}
	wg.Wait()
	return quotes
}

// medianRat returns the median of xs.  xs must be non-empty; for even-length
// lists the mean of the two middle elements is returned.  Input order is not
// preserved — a defensive copy is made before sorting.
func medianRat(xs []*big.Rat) *big.Rat {
	cp := make([]*big.Rat, len(xs))
	copy(cp, xs)
	sort.Slice(cp, func(i, j int) bool { return cp[i].Cmp(cp[j]) < 0 })
	n := len(cp)
	if n%2 == 1 {
		return new(big.Rat).Set(cp[n/2])
	}
	lo := cp[n/2-1]
	hi := cp[n/2]
	sum := new(big.Rat).Add(lo, hi)
	return sum.Quo(sum, new(big.Rat).SetInt64(2))
}

// deviationBps returns |x - ref| / ref in basis points.  Returns 0 if ref is
// zero (caller has already filtered non-positive rates).
func deviationBps(x, ref *big.Rat) int {
	if ref.Sign() <= 0 {
		return 0
	}
	diff := new(big.Rat).Sub(x, ref)
	if diff.Sign() < 0 {
		diff.Neg(diff)
	}
	diff.Quo(diff, ref)
	diff.Mul(diff, new(big.Rat).SetInt64(10000))

	// Floor to int; we accept integer precision for a threshold check.
	out := new(big.Int).Quo(diff.Num(), diff.Denom())
	if !out.IsInt64() {
		return int(^uint(0) >> 1) // math.MaxInt
	}
	v := out.Int64()
	if v > int64(int(^uint(0)>>1)) {
		return int(^uint(0) >> 1)
	}
	return int(v)
}
