package ctrl

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	constant "github.com/0glabs/0g-serving-broker/inference/const"
)

// The assay's identity card, relayed verbatim.
//
// Why relay at all: confirming "the assay behind this broker is the genuine
// verifier" normally means talking to the assay's tapp-server directly, and
// that port is an admin surface locked to admin IPs. After the single-ingress
// funnelling the outside world cannot reach the assay on any port. So the only
// way a client gets to look is through us.
//
// Why relaying is safe even though we are not trusted: everything in the
// response is either self-authenticating or checkable against a source we do
// not control. The signer address reconciles against TappRegistry on chain,
// which the client reads itself; the liveness check is an ecrecover over a
// ZG-Verdict-Sig the client already holds. We can refuse to answer, or answer
// staleley — we cannot forge an identity the chain will agree with. A relay
// that cannot lie does not need to be trusted, only available.
//
// Deliberately NOT parsed into a struct: this is a passthrough. Adding fields
// on the assay side must not require a broker release, and re-serialising
// through a Go struct would silently drop anything we did not anticipate.
type assayAttestationCache struct {
	mu        sync.RWMutex
	body      json.RawMessage
	fetchedAt time.Time
	ttl       time.Duration
}

// AssayAttestation returns the assay's attestation document plus the time we
// fetched it. The caller decides whether that age is acceptable — we do not
// pretend a cached document is fresh.
func (c *Ctrl) AssayAttestation(ctx context.Context) (json.RawMessage, time.Time, error) {
	if c.assayVerifierURL == "" {
		return nil, time.Time{}, fmt.Errorf("assay is not configured on this broker")
	}
	if c.assayAttCache == nil {
		c.assayAttCache = &assayAttestationCache{ttl: constant.AssayAttestationRelayTTL}
	}
	cache := c.assayAttCache

	cache.mu.RLock()
	if cache.body != nil && time.Since(cache.fetchedAt) < cache.ttl {
		body, at := cache.body, cache.fetchedAt
		cache.mu.RUnlock()
		return body, at, nil
	}
	cache.mu.RUnlock()

	url := c.assayVerifierURL + constant.AssayAttestationPath
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, time.Time{}, err
	}
	// The pinned client, not http.DefaultClient — same reason as every other
	// assay call: the verifier's cert is KMS-derived and self-signed, so CA
	// validation can never succeed. No request signature here: unlike the
	// voucher ledger this document is public by design (it is the thing we
	// want auditors to read), and the assay's nginx allowlists it accordingly.
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, time.Time{}, fmt.Errorf("assay unreachable: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, time.Time{}, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, time.Time{}, fmt.Errorf("%s -> %d: %s", url, resp.StatusCode, raw)
	}
	if !json.Valid(raw) {
		return nil, time.Time{}, fmt.Errorf("assay attestation is not valid JSON")
	}

	now := time.Now().UTC()
	cache.mu.Lock()
	cache.body, cache.fetchedAt = raw, now
	cache.mu.Unlock()
	return raw, now, nil
}
