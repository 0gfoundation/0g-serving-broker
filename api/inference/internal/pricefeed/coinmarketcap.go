package pricefeed

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"strings"
)

// CoinMarketCapSource queries CoinMarketCap's v2 /cryptocurrency/quotes/latest
// endpoint.  Requires an API key; auth is via the X-CMC_PRO_API_KEY header.
type CoinMarketCapSource struct {
	httpClient *http.Client
	baseURL    string
	apiKey     string
	symbol     string // e.g. "0G"
	quote      string // e.g. "USD"
	userAgent  string
}

// NewCoinMarketCapSource constructs a source.  apiKey must be non-empty.
func NewCoinMarketCapSource(client *http.Client, baseURL, apiKey, symbol, quote, userAgent string) *CoinMarketCapSource {
	if baseURL == "" {
		baseURL = "https://pro-api.coinmarketcap.com"
	}
	if quote == "" {
		quote = "USD"
	}
	return &CoinMarketCapSource{
		httpClient: client,
		baseURL:    baseURL,
		apiKey:     apiKey,
		symbol:     strings.ToUpper(symbol),
		quote:      strings.ToUpper(quote),
		userAgent:  userAgent,
	}
}

func (s *CoinMarketCapSource) Name() string { return "coinmarketcap" }

func (s *CoinMarketCapSource) FetchRate(ctx context.Context) (*big.Rat, error) {
	if s.apiKey == "" {
		return nil, fmt.Errorf("coinmarketcap: api key not configured")
	}
	q := url.Values{}
	q.Set("symbol", s.symbol)
	q.Set("convert", s.quote)
	reqURL := fmt.Sprintf("%s/v2/cryptocurrency/quotes/latest?%s", s.baseURL, q.Encode())

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("coinmarketcap: build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-CMC_PRO_API_KEY", s.apiKey)
	if s.userAgent != "" {
		req.Header.Set("User-Agent", s.userAgent)
	}

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("coinmarketcap: http: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("coinmarketcap: unexpected status %d", resp.StatusCode)
	}

	// Response shape (abridged):
	// {
	//   "data": {
	//     "0G": [ { "quote": { "USD": { "price": 0.00301 } } } ]
	//   }
	// }
	var body struct {
		Data map[string][]struct {
			Quote map[string]struct {
				Price json.Number `json:"price"`
			} `json:"quote"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxResponseBytes)).Decode(&body); err != nil {
		return nil, fmt.Errorf("coinmarketcap: decode: %w", err)
	}
	entries, ok := body.Data[s.symbol]
	if !ok || len(entries) == 0 {
		return nil, fmt.Errorf("coinmarketcap: missing symbol %q in response", s.symbol)
	}
	quote, ok := entries[0].Quote[s.quote]
	if !ok {
		return nil, fmt.Errorf("coinmarketcap: missing quote %q in response", s.quote)
	}
	rate, ok := new(big.Rat).SetString(quote.Price.String())
	if !ok {
		return nil, fmt.Errorf("coinmarketcap: price %q not parseable", quote.Price.String())
	}
	if rate.Sign() <= 0 {
		return nil, fmt.Errorf("coinmarketcap: non-positive rate %s", quote.Price.String())
	}
	return rate, nil
}
