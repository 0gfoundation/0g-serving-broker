package proxy

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/0glabs/0g-serving-broker/inference/monitor"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

// captureLogger embeds noopLogger (from proxy_test.go) and records Warnf lines
// so a test can assert on the aggregated rejection summary.
type captureLogger struct {
	noopLogger
	mu    sync.Mutex
	lines []string
}

func (c *captureLogger) Warnf(format string, args ...interface{}) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lines = append(c.lines, fmt.Sprintf(format, args...))
}

func (c *captureLogger) all() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return strings.Join(c.lines, "\n")
}

// newTestAggregator builds an aggregator WITHOUT starting the flush goroutine,
// so tests drive record()/flush() deterministically.
func newTestAggregator(logger *captureLogger) *rejectionAggregator {
	return &rejectionAggregator{
		logger:   logger,
		interval: time.Minute,
		buckets:  make(map[string]*reasonBucket),
	}
}

// A gate that rejects before a user address is resolved records "" — the global
// concurrency cap aborts ahead of ValidateSession. The summary must still report
// the total, and must not claim "across 0 user(s)", which reads as an aggregator
// fault rather than as a gate that cannot attribute.
func TestRejectionAggregator_FlushWithoutUserAttribution(t *testing.T) {
	logger := &captureLogger{}
	a := newTestAggregator(logger)

	for i := 0; i < 7; i++ {
		a.record(nil, monitor.RejectionGlobalConcurrency, "")
	}
	a.flush()

	out := logger.all()
	if len(logger.lines) != 1 {
		t.Fatalf("expected 1 summary line, got %d: %q", len(logger.lines), out)
	}
	if !strings.Contains(out, monitor.RejectionGlobalConcurrency) {
		t.Errorf("summary does not name the reason: %q", out)
	}
	if !strings.Contains(out, "7 in last") {
		t.Errorf("summary lost the total: %q", out)
	}
	if strings.Contains(out, "user(s)") || strings.Contains(out, "top:") {
		t.Errorf("summary still claims per-user attribution it does not have: %q", out)
	}
}

func TestRejectionAggregator_FlushSummarizesPerReason(t *testing.T) {
	logger := &captureLogger{}
	a := newTestAggregator(logger)

	// 5 rejections for one user, 2 reasons.
	for i := 0; i < 5; i++ {
		a.record(nil, monitor.RejectionInsufficientBal, "0x4870000000000000000000000000000000a4E9")
	}
	a.record(nil, monitor.RejectionRateLimit, "0x1111000000000000000000000000000000002222")

	a.flush()

	out := logger.all()
	if !strings.Contains(out, monitor.RejectionInsufficientBal) {
		t.Fatalf("expected insufficient_balance summary, got: %q", out)
	}
	// One aggregated line per reason — never one line per event.
	if got := len(logger.lines); got != 2 {
		t.Fatalf("expected 2 summary lines (one per reason), got %d: %q", got, out)
	}
	if !strings.Contains(out, "5 in last") {
		t.Fatalf("expected total of 5 for insufficient_balance, got: %q", out)
	}

	// After a flush the buckets reset: a second flush with no new activity is silent.
	a.flush()
	if got := len(logger.lines); got != 2 {
		t.Fatalf("flush after reset should emit nothing, got %d lines", got)
	}
}

func TestRejectionAggregator_BoundsTrackedUsers(t *testing.T) {
	logger := &captureLogger{}
	a := newTestAggregator(logger)

	// Spray far more distinct addresses than the per-reason cap. The map must
	// not grow without bound; the excess is folded into the overflow tally.
	total := maxRejectionUsersPerReason + 50
	for i := 0; i < total; i++ {
		a.record(nil, monitor.RejectionRateLimit, addrForIndex(i))
	}

	b := a.buckets[monitor.RejectionRateLimit]
	if b == nil {
		t.Fatal("expected a bucket for rate_limit")
	}
	if len(b.users) != maxRejectionUsersPerReason {
		t.Fatalf("tracked users not capped: got %d, want %d", len(b.users), maxRejectionUsersPerReason)
	}
	if b.overflow != 50 {
		t.Fatalf("overflow miscounted: got %d, want 50", b.overflow)
	}
	if b.total != int64(total) {
		t.Fatalf("total miscounted: got %d, want %d", b.total, total)
	}

	a.flush()
	if !strings.Contains(logger.all(), "from untracked addrs") {
		t.Fatalf("expected overflow note in summary, got: %q", logger.all())
	}
}

func TestTopUsers_OrdersByCountDescending(t *testing.T) {
	users := map[string]int64{
		"0xaaa": 3,
		"0xbbb": 10,
		"0xccc": 1,
		"0xddd": 7,
	}
	got := topUsers(users, 2)
	// Highest two: 0xbbb=10, 0xddd=7.
	if !strings.HasPrefix(got, "0xbbb=10") {
		t.Fatalf("expected highest count first, got: %q", got)
	}
	if strings.Contains(got, "0xccc") {
		t.Fatalf("expected only top 2, got: %q", got)
	}
}

func TestTopUsers_EmptyIsNA(t *testing.T) {
	if got := topUsers(nil, 3); got != "n/a" {
		t.Fatalf("expected n/a for empty, got %q", got)
	}
}

func TestRejectionAggregator_NilReceiverRecordDoesNotPanic(t *testing.T) {
	// A Proxy built outside New() (e.g. the test helper) has a nil aggregator.
	// record must still fire the metric without panicking.
	var a *rejectionAggregator
	a.record(nil, monitor.RejectionRateLimit, "0x4870000000000000000000000000000000a4E9")
}

func TestRejectionAggregator_StopIsIdempotent(t *testing.T) {
	a := newRejectionAggregator(&captureLogger{}, time.Hour)
	a.stop()
	a.stop() // second call must not panic on a closed channel
}

func TestRejectionAggregator_DistinctTruncationCollisionsCountedSeparately(t *testing.T) {
	logger := &captureLogger{}
	a := newTestAggregator(logger)
	// Two addresses that share the 6+4 truncation prefix but differ in the
	// middle must be counted as two distinct users, not merged.
	a.record(nil, monitor.RejectionRateLimit, "0x4870aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa4E9")
	a.record(nil, monitor.RejectionRateLimit, "0x4870bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb4E9")
	b := a.buckets[monitor.RejectionRateLimit]
	if got := len(b.users); got != 2 {
		t.Fatalf("expected 2 distinct users despite shared truncation prefix, got %d", got)
	}
}

// addrForIndex produces a distinct, well-formed 40-hex-char address per index.
func addrForIndex(i int) string {
	const hexDigits = "0123456789abcdef"
	var sb strings.Builder
	sb.WriteString("0x")
	// Encode i across the last few nibbles; rest zero-padded. 40 hex chars total.
	body := make([]byte, 40)
	for j := range body {
		body[j] = '0'
	}
	n := i
	pos := 39
	for n > 0 && pos >= 0 {
		body[pos] = hexDigits[n&0xf]
		n >>= 4
		pos--
	}
	sb.Write(body)
	return sb.String()
}

// record() must emit broker_requests_rejected_total, not merely log. Deleting
// the RecordRejection call left every test in the repo green, because the metric
// the admission gates exist to feed was asserted nowhere — this pins the call
// site, which the monitor-package test cannot: that one proves RecordRejection
// works, not that anyone invokes it.
func TestRejectionAggregatorEmitsTheRejectionMetric(t *testing.T) {
	saved := monitor.RequestRejectedTotal
	monitor.RequestRejectedTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{Name: "test_requests_rejected_total"},
		[]string{"reason"},
	)
	defer func() { monitor.RequestRejectedTotal = saved }()

	a := newTestAggregator(&captureLogger{})
	a.record(nil, monitor.RejectionGlobalConcurrency, "")
	a.record(nil, monitor.RejectionGlobalConcurrency, "")
	a.record(nil, monitor.RejectionRateLimit, "0x1111000000000000000000000000000000002222")

	if got := rejectionCount(t, monitor.RejectionGlobalConcurrency); got != 2 {
		t.Errorf("reason=%q counter = %v, want 2", monitor.RejectionGlobalConcurrency, got)
	}
	if got := rejectionCount(t, monitor.RejectionRateLimit); got != 1 {
		t.Errorf("reason=%q counter = %v, want 1", monitor.RejectionRateLimit, got)
	}
}

// A nil receiver must still emit the metric: record() fires the counter and the
// context stamp before its own nil guard, so monitoring survives a Proxy built
// without an aggregator.
func TestRejectionAggregatorNilReceiverStillEmitsTheMetric(t *testing.T) {
	saved := monitor.RequestRejectedTotal
	monitor.RequestRejectedTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{Name: "test_requests_rejected_total_nilrecv"},
		[]string{"reason"},
	)
	defer func() { monitor.RequestRejectedTotal = saved }()

	var a *rejectionAggregator
	a.record(nil, monitor.RejectionGlobalConcurrency, "")

	if got := rejectionCount(t, monitor.RejectionGlobalConcurrency); got != 1 {
		t.Errorf("counter = %v, want 1", got)
	}
}

func rejectionCount(t *testing.T, reason string) float64 {
	t.Helper()
	m := &dto.Metric{}
	c, err := monitor.RequestRejectedTotal.GetMetricWithLabelValues(reason)
	if err != nil {
		t.Fatalf("GetMetricWithLabelValues(%q): %v", reason, err)
	}
	if err := c.Write(m); err != nil {
		t.Fatalf("writing metric: %v", err)
	}
	return m.GetCounter().GetValue()
}
