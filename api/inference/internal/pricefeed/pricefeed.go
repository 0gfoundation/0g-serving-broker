// Package pricefeed provides live 0G/USD exchange-rate lookup and derived
// wei-price caching used by USD-denominated provider pricing.
//
// Responsibilities:
//   - Source interface + concrete implementations (CoinGecko, Binance, CoinMarketCap).
//   - Aggregator that combines per-source quotes into a single rate (median +
//     outlier rejection + minimum quorum).
//   - Cache of the most recently computed wei prices, read by the fee-computation
//     path and refreshed by the PriceUpdateProcessor.
//
// The rate itself is never persisted — it lives only as a transient value inside
// each update tick. Only the derived wei prices are cached and, when drift
// exceeds a configured threshold, written to chain via SyncService.
//
// # Units
//
//   - USD prices in config are USD per 1,000,000 tokens (the common LLM
//     pricing convention, matching OpenAI et al.).
//   - Rates returned by Source.FetchRate are USD per 1 0G token.
//   - Derived wei prices stored in Cache are wei per 1 token (the unit
//     expected by the on-chain contract and by all downstream fee math).
package pricefeed

import (
	"context"
	"math/big"
	"time"
)

// Source fetches the current 0G/USD exchange rate from a single provider.
//
// Implementations must be safe for concurrent use. The returned rate is
// expressed as USD per 1 0G token (e.g. 0.003 means 1 0G = $0.003), stored
// as a big.Rat so conversion to wei is exact.  A zero or negative value
// MUST be reported as an error so it cannot corrupt the aggregator's median.
type Source interface {
	// Name identifies the source for logs/metrics/config, e.g. "coingecko".
	Name() string
	// FetchRate returns the current USD-per-0G rate.  The context controls
	// cancellation; implementations must respect ctx.Done().
	FetchRate(ctx context.Context) (*big.Rat, error)
}

// SourceQuote is one source's response packaged with its identity and
// observation time.  Emitted by the aggregator for logging/metrics.
type SourceQuote struct {
	Source    string
	Rate      *big.Rat
	Err       error
	Timestamp time.Time
}
