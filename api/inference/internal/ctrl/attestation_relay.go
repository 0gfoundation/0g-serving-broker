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

// The spml's identity card, relayed verbatim.
//
// Why relay at all: confirming "the spml behind this broker is the genuine
// verifier" normally means talking to the spml's tapp-server directly, and
// that port is an admin surface locked to admin IPs. After the single-ingress
// funnelling the outside world cannot reach the spml on any port. So the only
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
// on the spml side must not require a broker release, and re-serialising
// through a Go struct would silently drop anything we did not anticipate.
type spmlAttestationCache struct {
	mu        sync.RWMutex
	body      json.RawMessage
	fetchedAt time.Time
	ttl       time.Duration
	// refresh single-flights the fetch: when the entry expires, every caller
	// that sees it stale would otherwise hit the spml at once. One fetches,
	// the rest wait and reuse its result (a failed fetch lets the next waiter
	// try, so the retry is serial, not parallel).
	refresh sync.Mutex
	// forcedAt rate-limits the ?fresh=1 bypass. Without a floor the bypass
	// would undo the reason the cache exists, since anyone may ask for it.
	forcedAt time.Time
}

// SpmlAttestationForceFloor is the shortest interval between two forced
// re-fetches. The cache is what stops a public endpoint from being an
// amplifier pointed at a host nobody else can reach, so the escape hatch out
// of it needs its own floor — generous enough to confirm a redeploy at once,
// tight enough that a loop gains nothing over the ordinary TTL.
const SpmlAttestationForceFloor = 30 * time.Second

// SpmlAttestation returns the spml's attestation document plus the time we
// fetched it. The caller decides whether that age is acceptable — we do not
// pretend a cached document is fresh.
//
// force skips the TTL. It exists because nothing here can tell that the spml's
// document changed: the obvious signal would be its TLS pin, and that pin is
// KMS-derived precisely so that it SURVIVES a redeploy. So after one, this
// relay serves parameters describing the previous instance until the TTL runs
// out — on the one endpoint whose whole purpose is to let someone verify us.
// A caller who has reason to think that happened can say so; the floor above
// keeps that from becoming the amplifier the cache is here to prevent.
func (c *Ctrl) SpmlAttestation(ctx context.Context, force bool) (json.RawMessage, time.Time, error) {
	if c.spmlVerifierURL == "" {
		return nil, time.Time{}, fmt.Errorf("spml is not configured on this broker")
	}
	cache := c.spmlAttCache // allocated once in New; never lazily (two first callers would race)

	// The ordinary path stays a read lock: this endpoint is public, and making
	// every caller queue behind a writer would hand out a contention point.
	cache.mu.RLock()
	warm := cache.body != nil && time.Since(cache.fetchedAt) < cache.ttl
	body, at := cache.body, cache.fetchedAt
	cache.mu.RUnlock()
	if warm && !force {
		return body, at, nil
	}
	if force && warm {
		// Claim the bypass only when it would actually change the answer, and
		// only once per floor. Claimed AFTER the warm check so an ordinary
		// expiry never spends someone's budget.
		cache.mu.Lock()
		allowed := time.Since(cache.forcedAt) >= SpmlAttestationForceFloor
		if allowed {
			cache.forcedAt = time.Now()
		}
		cache.mu.Unlock()
		if !allowed {
			return body, at, nil // asked too soon: the cached copy, honestly dated
		}
	}

	cache.refresh.Lock()
	defer cache.refresh.Unlock()
	cache.mu.RLock()
	refreshed := cache.body != nil && cache.fetchedAt.After(at)
	body, at = cache.body, cache.fetchedAt
	cache.mu.RUnlock()
	if refreshed {
		return body, at, nil // the fetcher we waited on already answered
	}

	url := c.spmlVerifierURL + constant.SpmlAttestationPath
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, time.Time{}, err
	}
	// The pinned client, not http.DefaultClient — same reason as every other
	// spml call: the verifier's cert is KMS-derived and self-signed, so CA
	// validation can never succeed. No request signature here: unlike the
	// voucher ledger this document is public by design (it is the thing we
	// want auditors to read), and the spml's nginx allowlists it accordingly.
	resp, err := c.spmlDo(req)
	if err != nil {
		// A forced re-fetch that fails must not take the cached document with
		// it: the caller asked for fresher, not for nothing. They can still see
		// it is stale — fetched_at says so.
		if warm {
			return body, at, nil
		}
		return nil, time.Time{}, fmt.Errorf("spml unreachable: %w", err)
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
		return nil, time.Time{}, fmt.Errorf("spml attestation is not valid JSON")
	}

	now := time.Now().UTC()
	cache.mu.Lock()
	cache.body, cache.fetchedAt = raw, now
	cache.mu.Unlock()
	return raw, now, nil
}
