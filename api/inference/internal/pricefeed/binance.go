package pricefeed

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"strings"
)

// BinanceSource queries Binance's public spot ticker endpoint.
// Symbol is the exchange pair (e.g. "ZGUSDT") — differs from CoinGecko's
// coin-id convention.
//
// Note: Binance's public API returns HTTP 451 in sanctioned regions.  If
// Binance is the only viable source for an operator's region, either drop
// it from priceFeed.sources or point at a Binance-compatible mirror.
type BinanceSource struct {
	httpClient *http.Client
	baseURL    string
	pair       string
	userAgent  string
}

// NewBinanceSource constructs a source.  pair is the exchange symbol
// (e.g. "ZGUSDT"); callers should uppercase it to match Binance's API.
func NewBinanceSource(client *http.Client, baseURL, pair, userAgent string) *BinanceSource {
	if baseURL == "" {
		baseURL = "https://api.binance.com"
	}
	return &BinanceSource{
		httpClient: client,
		baseURL:    baseURL,
		pair:       strings.ToUpper(pair),
		userAgent:  userAgent,
	}
}

func (s *BinanceSource) Name() string { return "binance" }

func (s *BinanceSource) FetchRate(ctx context.Context) (*big.Rat, error) {
	reqURL := fmt.Sprintf("%s/api/v3/ticker/price?symbol=%s", s.baseURL, s.pair)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("binance: build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	if s.userAgent != "" {
		req.Header.Set("User-Agent", s.userAgent)
	}

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("binance: http: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("binance: unexpected status %d", resp.StatusCode)
	}

	// Response shape: { "symbol": "ZGUSDT", "price": "0.00301" }
	var body struct {
		Symbol string `json:"symbol"`
		Price  string `json:"price"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("binance: decode: %w", err)
	}
	if body.Price == "" {
		return nil, fmt.Errorf("binance: empty price in response")
	}
	rate, ok := new(big.Rat).SetString(body.Price)
	if !ok {
		return nil, fmt.Errorf("binance: price %q not parseable", body.Price)
	}
	if rate.Sign() <= 0 {
		return nil, fmt.Errorf("binance: non-positive rate %s", body.Price)
	}
	return rate, nil
}
