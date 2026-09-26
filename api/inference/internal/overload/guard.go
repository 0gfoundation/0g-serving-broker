// Package overload sheds inference requests while the model engine behind the
// broker is saturated, so a caller gets an immediate 429 with Retry-After
// instead of waiting for a first token that is minutes away.
//
// The broker's own admission gates count requests. That is the wrong unit when
// a few very long contexts are what fills the engine: on 2026-09-24 a
// self-hosted glm-5.3 ran only 2-5 requests while its KV cache sat at 90-100%
// for about four hours, ~27 requests queued behind them, and every new request
// waited out the router's 5-minute header timeout before failing with a generic
// 502. The engine's own gauges said so the whole time; this reads them.
package overload

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/0glabs/0g-serving-broker/inference/config"
)

// The two sglang gauges the guard reads. Engines with tensor parallelism can
// publish one series per rank; the guard takes the maximum across series, so
// any one rank reporting saturation counts.
const (
	metricQueueRequests = "sglang:num_queue_reqs"
	metricTokenUsage    = "sglang:token_usage"
)

// maxMetricsBytes bounds how much of a metrics response is read. sglang's
// endpoint is tens of KB; the bound only matters if the URL points somewhere
// unexpected.
const maxMetricsBytes = 4 << 20

// Logger is the subset of the broker logger the guard uses.
type Logger interface {
	Infof(format string, args ...interface{})
	Warnf(format string, args ...interface{})
}

type sample struct {
	at            time.Time
	queueRequests float64
	tokenUsage    float64
}

// Guard polls the engine's metrics endpoint and answers whether it is
// saturated. A nil *Guard is valid and never reports overload.
type Guard struct {
	cfg    config.OverloadGuardConfig
	client *http.Client
	logger Logger
	now    func() time.Time

	latest        atomic.Pointer[sample]
	overloaded    atomic.Bool // last reported state, for transition logging only
	scrapeFailing atomic.Bool // same, for the metrics endpoint itself
}

// New returns nil when the guard is disabled, so callers can hold the result
// unconditionally.
func New(cfg config.OverloadGuardConfig, logger Logger) *Guard {
	if !cfg.Enabled {
		return nil
	}
	return &Guard{
		cfg: cfg,
		// Bounded by the poll interval: a scrape slower than that is stale by
		// the time it lands, and must not stack up behind the next tick.
		client: &http.Client{Timeout: cfg.PollInterval},
		logger: logger,
		now:    time.Now,
	}
}

// Run polls until ctx is cancelled. It scrapes once immediately so the guard is
// armed from the first request rather than one interval later.
func (g *Guard) Run(ctx context.Context) {
	if g == nil {
		return
	}
	g.poll(ctx)
	ticker := time.NewTicker(g.cfg.PollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			g.poll(ctx)
		}
	}
}

func (g *Guard) poll(ctx context.Context) {
	s, err := g.scrape(ctx)
	if err != nil {
		// Drop the previous sample rather than keep acting on it: a verdict the
		// engine can no longer confirm is not evidence of saturation, so a broken
		// endpoint admits from the next request on. Logged once per outage.
		g.latest.Store(nil)
		if !g.scrapeFailing.Swap(true) {
			g.logger.Warnf("overload guard: metrics scrape failed, admitting all requests until it recovers: %v", err)
		}
		return
	}
	if g.scrapeFailing.Swap(false) {
		g.logger.Infof("overload guard: metrics scrape recovered")
	}
	g.latest.Store(s)
}

func (g *Guard) scrape(ctx context.Context) (*sample, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, g.cfg.MetricsURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := g.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("metrics endpoint returned %d", resp.StatusCode)
	}
	values, err := parseGauges(io.LimitReader(resp.Body, maxMetricsBytes), metricQueueRequests, metricTokenUsage)
	if err != nil {
		return nil, err
	}
	queue, okQ := values[metricQueueRequests]
	usage, okU := values[metricTokenUsage]
	// Both or nothing. An endpoint missing one gauge is not the engine this was
	// configured for (or a renamed metric after an upgrade); treating the absent
	// one as 0 would quietly disable half the guard.
	if !okQ || !okU {
		return nil, fmt.Errorf("metrics endpoint is missing %s or %s", metricQueueRequests, metricTokenUsage)
	}
	return &sample{at: g.now(), queueRequests: queue, tokenUsage: usage}, nil
}

// Check reports whether new requests should be shed, and why. It never sheds
// without a current sample: a failed scrape clears it (see poll), and a sample
// older than three poll intervals — a poller that stopped, or a scrape still
// hanging — is ignored. The guard must not become a way for the box to reject
// traffic it could have served.
func (g *Guard) Check() (overloaded bool, reason string) {
	if g == nil {
		return false, ""
	}
	s := g.latest.Load()
	if s == nil || g.now().Sub(s.at) > 3*g.cfg.PollInterval {
		g.noteTransition(false, "")
		return false, ""
	}
	switch {
	case g.cfg.MaxQueueRequests > 0 && s.queueRequests >= float64(g.cfg.MaxQueueRequests):
		reason = fmt.Sprintf("%.0f requests queued (limit %d)", s.queueRequests, g.cfg.MaxQueueRequests)
	case g.cfg.MaxTokenUsage > 0 && s.tokenUsage >= g.cfg.MaxTokenUsage:
		reason = fmt.Sprintf("KV cache %.0f%% used (limit %.0f%%)", s.tokenUsage*100, g.cfg.MaxTokenUsage*100)
	}
	g.noteTransition(reason != "", reason)
	return reason != "", reason
}

// RetryAfterSeconds is the Retry-After value to send with a shed request.
func (g *Guard) RetryAfterSeconds() int {
	if g == nil {
		return 0
	}
	return int(g.cfg.RetryAfter / time.Second)
}

// noteTransition logs only when the verdict flips, so a saturated box emits
// two lines per episode instead of one per shed request.
func (g *Guard) noteTransition(now bool, reason string) {
	if g.overloaded.Swap(now) == now {
		return
	}
	if now {
		g.logger.Warnf("overload guard: engine saturated (%s), shedding new inference requests with 503", reason)
	} else {
		g.logger.Infof("overload guard: engine no longer saturated, admitting requests")
	}
}

// parseGauges reads Prometheus text exposition and returns, for each wanted
// metric name, the maximum value across its series. Names that never appear
// are absent from the result.
func parseGauges(r io.Reader, names ...string) (map[string]float64, error) {
	want := make(map[string]bool, len(names))
	for _, n := range names {
		want[n] = true
	}
	out := make(map[string]float64, len(names))
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if line == "" || line[0] == '#' {
			continue
		}
		name, rest := line, ""
		if i := strings.IndexAny(line, "{ "); i >= 0 {
			name, rest = line[:i], line[i:]
		}
		if !want[name] {
			continue
		}
		if strings.HasPrefix(rest, "{") {
			// Label values may contain spaces or braces inside quotes; the value
			// starts after the LAST closing brace.
			j := strings.LastIndex(rest, "}")
			if j < 0 {
				continue
			}
			rest = rest[j+1:]
		}
		fields := strings.Fields(rest)
		if len(fields) == 0 {
			continue
		}
		v, err := strconv.ParseFloat(fields[0], 64)
		if err != nil {
			continue
		}
		if cur, ok := out[name]; !ok || v > cur {
			out[name] = v
		}
	}
	return out, sc.Err()
}
