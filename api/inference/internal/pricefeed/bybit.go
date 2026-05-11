package pricefeed

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strings"
)

// BybitSource queries Bybit's public spot ticker endpoint.
// symbol is the Bybit instrument id (e.g. "0GUSDT"), using the same
// concatenated convention as Binance — distinct from OKX's "0G-USDT"
// or CoinGecko's coin-id "zero-gravity".
//
// Bybit's public market endpoints don't require an API key and are
// generally available globally, making this a useful complement to
// Binance (which returns 451 in sanctioned regions).
type BybitSource struct {
	httpClient *http.Client
	baseURL    string
	symbol     string
	userAgent  string
}

// NewBybitSource constructs a source.  symbol is the Bybit spot
// instrument id (e.g. "0GUSDT"); callers should uppercase it to match
// Bybit's API.
func NewBybitSource(client *http.Client, baseURL, symbol, userAgent string) *BybitSource {
	if baseURL == "" {
		baseURL = "https://api.bybit.com"
	}
	return &BybitSource{
		httpClient: client,
		baseURL:    baseURL,
		symbol:     strings.ToUpper(symbol),
		userAgent:  userAgent,
	}
}

func (s *BybitSource) Name() string { return "bybit" }

func (s *BybitSource) FetchRate(ctx context.Context) (*big.Rat, error) {
	reqURL := fmt.Sprintf("%s/v5/market/tickers?category=spot&symbol=%s", s.baseURL, s.symbol)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("bybit: build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	if s.userAgent != "" {
		req.Header.Set("User-Agent", s.userAgent)
	}

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("bybit: http: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("bybit: unexpected status %d", resp.StatusCode)
	}

	// Response shape:
	//   { "retCode": 0, "retMsg": "OK",
	//     "result": { "category": "spot",
	//                 "list": [ { "symbol": "0GUSDT", "lastPrice": "0.5658", ... } ] } }
	// Bybit returns HTTP 200 with retCode != 0 for application-level
	// errors (e.g. unknown symbol), so the body must be inspected.
	var body struct {
		RetCode int    `json:"retCode"`
		RetMsg  string `json:"retMsg"`
		Result  struct {
			List []struct {
				Symbol    string `json:"symbol"`
				LastPrice string `json:"lastPrice"`
			} `json:"list"`
		} `json:"result"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxResponseBytes)).Decode(&body); err != nil {
		return nil, fmt.Errorf("bybit: decode: %w", err)
	}
	if body.RetCode != 0 {
		return nil, fmt.Errorf("bybit: api error retCode=%d retMsg=%q", body.RetCode, body.RetMsg)
	}
	if len(body.Result.List) == 0 {
		return nil, fmt.Errorf("bybit: empty list for symbol %q", s.symbol)
	}
	if body.Result.List[0].LastPrice == "" {
		return nil, fmt.Errorf("bybit: empty lastPrice in response")
	}
	rate, ok := new(big.Rat).SetString(body.Result.List[0].LastPrice)
	if !ok {
		return nil, fmt.Errorf("bybit: price %q not parseable", body.Result.List[0].LastPrice)
	}
	if rate.Sign() <= 0 {
		return nil, fmt.Errorf("bybit: non-positive rate %s", body.Result.List[0].LastPrice)
	}
	return rate, nil
}
