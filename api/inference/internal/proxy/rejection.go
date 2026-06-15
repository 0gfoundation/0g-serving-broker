package proxy

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/0glabs/0g-serving-broker/common/log"
	"github.com/0glabs/0g-serving-broker/inference/monitor"
)

// rejectionReasonFromContext reads the classified rejection reason that the
// validation gate stashed under monitor.CtxKeyRejectionReason. When the gate
// failed for an unclassified (server-side) reason the key is absent, so we fall
// back to upstream_error rather than mislabeling it as a user-caused rejection.
func rejectionReasonFromContext(ctx *gin.Context) string {
	if v, ok := ctx.Get(monitor.CtxKeyRejectionReason); ok {
		if reason, ok := v.(string); ok && reason != "" {
			return reason
		}
	}
	return monitor.RejectionUpstreamError
}

// rejectionFlushInterval is how often accumulated rejection counts are summarized
// into a single log line per reason. One line per reason per interval bounds log
// volume regardless of request rate — the fix for the per-event warning spam
// that turned a single underfunded client into a 150k-line/day log flood.
const rejectionFlushInterval = 60 * time.Second

// maxRejectionUsersPerReason caps how many distinct user addresses are tracked
// per reason between flushes. A spray of distinct addresses (e.g. an abuser
// rotating wallets) is bounded here: addresses beyond the cap are folded into an
// overflow tally rather than growing the map without limit. The cap is generous
// enough that real top-offender attribution survives for normal traffic.
const maxRejectionUsersPerReason = 256

// reasonBucket accumulates rejections for one reason between flushes.
type reasonBucket struct {
	total    int64
	users    map[string]int64 // truncated address -> count, capped at maxRejectionUsersPerReason
	overflow int64            // count for addresses not tracked because the cap was hit
}

// rejectionAggregator turns per-request rejections into bounded, periodic
// summary logs while leaving the real-time signal to the Prometheus counter
// (incremented separately via monitor.RecordRejection). It exists because the
// previous code logged one warning per rejected request — under a sustained
// rejection flood that is an unbounded, disk-filling log-amplification vector,
// and it also buried the (silent) billing-gate rejections that motivated this
// whole change.
type rejectionAggregator struct {
	logger   log.Logger
	interval time.Duration

	mu      sync.Mutex
	buckets map[string]*reasonBucket

	ticker *time.Ticker
	done   chan struct{}
	wg     sync.WaitGroup
}

func newRejectionAggregator(logger log.Logger, interval time.Duration) *rejectionAggregator {
	a := &rejectionAggregator{
		logger:   logger,
		interval: interval,
		buckets:  make(map[string]*reasonBucket),
		done:     make(chan struct{}),
	}
	a.ticker = time.NewTicker(interval)
	a.wg.Add(1)
	go a.run()
	return a
}

// record increments both the Prometheus counter (real-time, per-reason) and the
// in-memory aggregate used for periodic logging. user is the raw address; it is
// truncated before being stored so full addresses never reach the logs
// (CLAUDE.md logging guidance). An empty user is recorded against the reason
// total only.
func (a *rejectionAggregator) record(reason, user string) {
	monitor.RecordRejection(reason)

	a.mu.Lock()
	defer a.mu.Unlock()

	b := a.buckets[reason]
	if b == nil {
		b = &reasonBucket{users: make(map[string]int64)}
		a.buckets[reason] = b
	}
	b.total++
	if user == "" {
		return
	}
	key := truncateAddr(strings.ToLower(user))
	if _, tracked := b.users[key]; tracked {
		b.users[key]++
		return
	}
	if len(b.users) >= maxRejectionUsersPerReason {
		b.overflow++
		return
	}
	b.users[key] = 1
}

func (a *rejectionAggregator) run() {
	defer a.wg.Done()
	for {
		select {
		case <-a.done:
			a.flush() // final summary so counts since the last tick aren't lost on shutdown
			return
		case <-a.ticker.C:
			a.flush()
		}
	}
}

// flush emits one log line per reason that saw rejections since the last flush,
// then resets the accumulators. Reasons with zero activity produce no output.
func (a *rejectionAggregator) flush() {
	a.mu.Lock()
	buckets := a.buckets
	a.buckets = make(map[string]*reasonBucket)
	a.mu.Unlock()

	for reason, b := range buckets {
		if b.total == 0 {
			continue
		}
		a.logger.Warnf("request rejections [%s]: %d in last %s across %d user(s)%s; top: %s",
			reason, b.total, a.interval, len(b.users), overflowSuffix(b.overflow), topUsers(b.users, 3))
	}
}

func overflowSuffix(overflow int64) string {
	if overflow == 0 {
		return ""
	}
	return fmt.Sprintf(" (+%d from untracked addrs)", overflow)
}

// topUsers returns up to n "addr=count" pairs sorted by count descending, for
// the rejection summary line. Ties are broken by address for stable output.
func topUsers(users map[string]int64, n int) string {
	if len(users) == 0 {
		return "n/a"
	}
	type pair struct {
		addr  string
		count int64
	}
	pairs := make([]pair, 0, len(users))
	for addr, count := range users {
		pairs = append(pairs, pair{addr, count})
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].count != pairs[j].count {
			return pairs[i].count > pairs[j].count
		}
		return pairs[i].addr < pairs[j].addr
	})
	if len(pairs) > n {
		pairs = pairs[:n]
	}
	parts := make([]string, len(pairs))
	for i, p := range pairs {
		parts[i] = fmt.Sprintf("%s=%d", p.addr, p.count)
	}
	return strings.Join(parts, ", ")
}

// stop halts the background flush goroutine after emitting a final summary.
func (a *rejectionAggregator) stop() {
	a.ticker.Stop()
	close(a.done)
	a.wg.Wait()
}
