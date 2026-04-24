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
			sources = append(sources, NewCoinGeckoSource(httpClient, "", base, quote, cfg.CoinGeckoAPIKey, cfg.UserAgent))
		case "binance":
			// Binance pairs are the base + quote uppercased and concatenated
			// (e.g. "0GUSDT").  splitSymbol guarantees base is already the
			// correct token identifier for the exchange.
			pair := strings.ToUpper(base + quote)
			sources = append(sources, NewBinanceSource(httpClient, "", pair, cfg.UserAgent))
		case "coinmarketcap":
			if cfg.CoinMarketCapAPIKey == "" {
				return nil, fmt.Errorf("pricefeed: coinmarketcap source requires priceFeed.coinMarketCapApiKey")
			}
			sources = append(sources, NewCoinMarketCapSource(httpClient, "", cfg.CoinMarketCapAPIKey, strings.ToUpper(base), strings.ToUpper(quote), cfg.UserAgent))
		default:
			return nil, fmt.Errorf("pricefeed: unknown source %q", name)
		}
	}
	return sources, nil
}

// splitSymbol parses a "<base>-<quote>" symbol into its two halves, splitting
// on the LAST hyphen.  CoinGecko coin IDs commonly contain hyphens
// (e.g. "zero-gravity", "bitcoin-cash"), so splitting on the first hyphen
// would mangle the base.  Assumes the quote currency never contains a hyphen
// — true for every fiat/stablecoin ticker we care about (usd, usdt, usdc,
// eur, btc, eth, ...).
func splitSymbol(symbol string) (base, quote string, err error) {
	idx := strings.LastIndex(symbol, "-")
	if idx <= 0 || idx == len(symbol)-1 {
		return "", "", fmt.Errorf("pricefeed: symbol %q must be in '<base>-<quote>' form", symbol)
	}
	return strings.ToLower(symbol[:idx]), strings.ToLower(symbol[idx+1:]), nil
}
