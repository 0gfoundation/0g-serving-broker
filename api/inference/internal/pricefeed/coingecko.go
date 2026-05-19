package pricefeed

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
)

// maxResponseBytes caps each source's HTTP response to 1 MiB.  The actual
// payloads are <1 KiB; the limit is a defence-in-depth guard against a
// compromised or misbehaving upstream returning an unbounded stream.
const maxResponseBytes = 1 << 20

// CoinGeckoSource queries the CoinGecko /simple/price endpoint.
// Symbol is the CoinGecko coin id (e.g. "0g"), quote is the fiat / stablecoin
// id (e.g. "usd"); these differ from the exchange-pair style used by Binance.
//
// CoinGecko has two independent key tiers — Demo (free, served from
// api.coingecko.com via x-cg-demo-api-key) and Pro (paid, served from
// pro-api.coingecko.com via x-cg-pro-api-key) — and a key issued for one
// tier is rejected by the other.  keyType selects between them; see
// NewCoinGeckoSource.
type CoinGeckoSource struct {
	httpClient *http.Client
	baseURL    string // overridable for tests
	coinID     string
	quoteID    string
	apiKey     string
	keyHeader  string // request header to send apiKey in; empty when apiKey is empty
	userAgent  string
}

// NewCoinGeckoSource constructs a source.
//
// keyType must be "demo" or "pro" when apiKey is non-empty; it picks both the
// default endpoint (api.coingecko.com vs pro-api.coingecko.com) and the
// request header (x-cg-demo-api-key vs x-cg-pro-api-key).  When apiKey is
// empty, keyType is ignored and the public anonymous endpoint is used.
//
// baseURL may be empty to use the tier-appropriate default; tests inject a
// stub server's URL while still exercising the header selection logic.
func NewCoinGeckoSource(client *http.Client, baseURL, coinID, quoteID, apiKey, keyType, userAgent string) *CoinGeckoSource {
	keyHeader := ""
	if apiKey != "" {
		if keyType == "pro" {
			keyHeader = "x-cg-pro-api-key"
		} else {
			keyHeader = "x-cg-demo-api-key"
		}
	}
	if baseURL == "" {
		if keyType == "pro" {
			baseURL = "https://pro-api.coingecko.com/api/v3"
		} else {
			baseURL = "https://api.coingecko.com/api/v3"
		}
	}
	return &CoinGeckoSource{
		httpClient: client,
		baseURL:    baseURL,
		coinID:     coinID,
		quoteID:    quoteID,
		apiKey:     apiKey,
		keyHeader:  keyHeader,
		userAgent:  userAgent,
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
	if s.userAgent != "" {
		req.Header.Set("User-Agent", s.userAgent)
	}
	if s.apiKey != "" && s.keyHeader != "" {
		req.Header.Set(s.keyHeader, s.apiKey)
	}

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
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxResponseBytes)).Decode(&body); err != nil {
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
