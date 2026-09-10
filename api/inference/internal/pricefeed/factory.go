package pricefeed

import (
	"fmt"
	"net/http"
	"time"

	"github.com/0glabs/0g-serving-broker/inference/config"
)

// 0G-specific symbol constants for each supported source.  These are facts
// about where 0G trades, not operator choices — there's no sensible alternate
// value for this broker (it prices 0G for on-chain settlement).  If 0G is
// ever re-slugged on CoinGecko or relisted under a different pair on an
// exchange, update these in place.
const (
	coinGeckoCoinID = "zero-gravity"
	coinGeckoQuote  = "usd"
	binanceSymbol   = "0GUSDT"
	bybitSymbol     = "0GUSDT"
)

// BuildSources constructs Source implementations from a PriceFeedConfig.
// Each supported source has its 0G symbol baked in; see the constants above.
//
// Returns an error if a requested source name is unknown.
func BuildSources(cfg config.PriceFeedConfig) ([]Source, error) {
	httpClient := &http.Client{
		Timeout: cfg.HTTPTimeout,
		Transport: &http.Transport{
			MaxIdleConns:        10,
			MaxIdleConnsPerHost: 2,
			IdleConnTimeout:     30 * time.Second,
		},
	}

	sources := make([]Source, 0, len(cfg.Sources))
	for _, name := range cfg.Sources {
		switch name {
		case "coingecko":
			sources = append(sources, NewCoinGeckoSource(httpClient, "", coinGeckoCoinID, coinGeckoQuote, cfg.CoinGeckoAPIKey, cfg.CoinGeckoKeyType, cfg.UserAgent))
		case "binance":
			sources = append(sources, NewBinanceSource(httpClient, "", binanceSymbol, cfg.UserAgent))
		case "bybit":
			sources = append(sources, NewBybitSource(httpClient, "", bybitSymbol, cfg.UserAgent))
		default:
			return nil, fmt.Errorf("pricefeed: unknown source %q", name)
		}
	}
	return sources, nil
}
