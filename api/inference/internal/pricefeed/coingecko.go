package pricefeed

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/url"
)

// CoinGeckoSource queries the free CoinGecko /simple/price endpoint.
// Symbol is the CoinGecko coin id (e.g. "0g"), quote is the fiat / stablecoin
// id (e.g. "usd"); these differ from the exchange-pair style used by Binance.
type CoinGeckoSource struct {
	httpClient *http.Client
	baseURL    string // overridable for tests
	coinID     string
	quoteID    string
}

// NewCoinGeckoSource constructs a source.  baseURL may be empty to use the
// public endpoint; tests inject a stub server's URL.
func NewCoinGeckoSource(client *http.Client, baseURL, coinID, quoteID string) *CoinGeckoSource {
	if baseURL == "" {
		baseURL = "https://api.coingecko.com/api/v3"
	}
	return &CoinGeckoSource{
		httpClient: client,
		baseURL:    baseURL,
		coinID:     coinID,
		quoteID:    quoteID,
	}
}

func (s *CoinGeckoSource) Name() string { return "coingecko" }

func (s *CoinGeckoSource) FetchRate(ctx context.Context) (*big.Rat, error) {
	q := url.Values{}
	q.Set("ids", s.coinID)
	q.Set("vs_currencies", s.quoteID)
	reqURL := fmt.Sprintf("%s/simple/price?%s", s.baseURL, q.Encode())

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("coingecko: build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("coingecko: http: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("coingecko: unexpected status %d", resp.StatusCode)
	}

	// Response shape: { "<coinID>": { "<quoteID>": <number> } }
	var body map[string]map[string]json.Number
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("coingecko: decode: %w", err)
	}
	inner, ok := body[s.coinID]
	if !ok {
		return nil, fmt.Errorf("coingecko: missing coin %q in response", s.coinID)
	}
	raw, ok := inner[s.quoteID]
	if !ok {
		return nil, fmt.Errorf("coingecko: missing quote %q in response", s.quoteID)
	}
	rate, ok := new(big.Rat).SetString(raw.String())
	if !ok {
		return nil, fmt.Errorf("coingecko: rate %q not parseable", raw.String())
	}
	if rate.Sign() <= 0 {
		return nil, fmt.Errorf("coingecko: non-positive rate %s", raw.String())
	}
	return rate, nil
}
