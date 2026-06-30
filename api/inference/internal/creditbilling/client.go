package creditbilling

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/pkg/errors"
)

// ErrInsufficientBalance is returned by Deduct when the credit service reports
// the user cannot afford the request (HTTP 402 Payment Required). Nothing was
// charged. The caller MUST reject the request (fail-closed) and MUST NOT serve a
// response.
var ErrInsufficientBalance = errors.New("insufficient credit balance")

// Client talks to the centralized credit service over HTTP.
//
// FAIL-CLOSED CONTRACT: callers MUST treat any non-nil error from Balance or
// Deduct — including ErrInsufficientBalance, timeouts, and non-2xx responses —
// as "reject this request". Never serve a response when balance/deduct errored;
// the credit service is the authority on whether the user can pay.
type Client struct {
	endpoint   string
	httpClient *http.Client
}

// NewClient builds a credit-service client. endpoint is the service base URL
// (e.g. https://credit.internal); timeout bounds each call.
func NewClient(endpoint string, timeout time.Duration) *Client {
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	return &Client{
		endpoint: endpoint,
		httpClient: &http.Client{
			Timeout: timeout,
			Transport: &http.Transport{
				MaxIdleConns:        100,
				MaxIdleConnsPerHost: 10,
				IdleConnTimeout:     90 * time.Second,
			},
		},
	}
}

type balanceResponse struct {
	CreditMicroUsd int64 `json:"creditMicroUsd"`
	Sufficient     bool  `json:"sufficient"`
}

// Balance returns the user's remaining credit (micro-USD) and whether it clears
// the service's minimum threshold.
func (c *Client) Balance(ctx context.Context, user, provider string) (int64, bool, error) {
	u := fmt.Sprintf("%s/v1/balance?user=%s&provider=%s",
		c.endpoint, url.QueryEscape(user), url.QueryEscape(provider))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return 0, false, errors.Wrap(err, "build balance request")
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return 0, false, errors.Wrap(err, "call credit service balance")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, false, errors.Errorf("credit service balance returned %d", resp.StatusCode)
	}
	var br balanceResponse
	if err := json.NewDecoder(resp.Body).Decode(&br); err != nil {
		return 0, false, errors.Wrap(err, "decode balance response")
	}
	return br.CreditMicroUsd, br.Sufficient, nil
}

// DeductResult is the credit service's response to a deduct call.
type DeductResult struct {
	ReceiptID       string `json:"receiptId"`
	SettledMicroUsd int64  `json:"settledMicroUsd"`
	BalanceMicroUsd int64  `json:"balanceMicroUsd"`
	Insufficient    bool   `json:"insufficient"`
	Replay          bool   `json:"replay"`
}

type deductBody struct {
	Receipt   Receipt `json:"receipt"`
	Signature string  `json:"signature"`
}

// Deduct submits a signed receipt; the service verifies the signature against
// the on-chain teeSignerAddress and atomically deducts.
func (c *Client) Deduct(ctx context.Context, r Receipt, signatureHex string) (*DeductResult, error) {
	payload, err := json.Marshal(deductBody{Receipt: r, Signature: signatureHex})
	if err != nil {
		return nil, errors.Wrap(err, "marshal deduct body")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint+"/v1/deduct", bytes.NewReader(payload))
	if err != nil {
		return nil, errors.Wrap(err, "build deduct request")
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, errors.Wrap(err, "call credit service deduct")
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusPaymentRequired {
		return nil, ErrInsufficientBalance
	}
	if resp.StatusCode != http.StatusOK {
		return nil, errors.Errorf("credit service deduct returned %d", resp.StatusCode)
	}
	var dr DeductResult
	if err := json.NewDecoder(resp.Body).Decode(&dr); err != nil {
		return nil, errors.Wrap(err, "decode deduct response")
	}
	return &dr, nil
}
