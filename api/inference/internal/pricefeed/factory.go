package pricefeed

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/0glabs/0g-serving-broker/inference/config"
)

// BuildSources constructs Source implementations from a PriceFeedConfig.
// symbol is the config.priceFeed.symbol value, expected in "<base>-<quote>"
// form (e.g. "0g-usdt"); each source adapts it to its own API convention.
//
// Returns an error if a requested source is unknown or missing required
// credentials (e.g. CoinMarketCap without an API key).
func BuildSources(cfg config.PriceFeedConfig) ([]Source, error) {
	base, quote, err := splitSymbol(cfg.Symbol)
	if err != nil {
		return nil, err
	}

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
		name = strings.ToLower(strings.TrimSpace(name))
		switch name {
		case "coingecko":
			sources = append(sources, NewCoinGeckoSource(httpClient, "", base, quote))
		case "binance":
			// Binance uses combined pair (e.g. "ZGUSDT").  0G's Binance ticker
			// symbol uses the prefix "ZG"; we special-case that mapping here so
			// operators keep the same "0g-usdt" symbol string across sources.
			pair := strings.ToUpper(binancePairBase(base) + quote)
			sources = append(sources, NewBinanceSource(httpClient, "", pair))
		case "coinmarketcap":
			if cfg.CoinMarketCapAPIKey == "" {
				return nil, fmt.Errorf("pricefeed: coinmarketcap source requires priceFeed.coinMarketCapApiKey")
			}
			sources = append(sources, NewCoinMarketCapSource(httpClient, "", cfg.CoinMarketCapAPIKey, strings.ToUpper(base), strings.ToUpper(quote)))
		default:
			return nil, fmt.Errorf("pricefeed: unknown source %q", name)
		}
	}
	return sources, nil
}

// splitSymbol parses a "<base>-<quote>" symbol into its two halves.
func splitSymbol(symbol string) (base, quote string, err error) {
	parts := strings.SplitN(symbol, "-", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("pricefeed: symbol %q must be in '<base>-<quote>' form", symbol)
	}
	return strings.ToLower(parts[0]), strings.ToLower(parts[1]), nil
}

// binancePairBase maps the generic base-symbol used in config to Binance's
// ticker prefix.  0G trades on Binance as "ZG..."; keeping this mapping
// localised lets operators use the canonical "0g-usdt" everywhere.
func binancePairBase(base string) string {
	if base == "0g" {
		return "ZG"
	}
	return strings.ToUpper(base)
}
