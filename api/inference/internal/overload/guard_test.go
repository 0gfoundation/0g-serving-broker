package overload

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/0glabs/0g-serving-broker/inference/config"
	"github.com/0glabs/0g-serving-broker/inference/monitor"
)

// A trimmed sglang /metrics payload with tp-ranked series, as TP=8 engines emit.
const sglangMetrics = `# HELP sglang:num_queue_reqs The number of requests in the waiting queue.
# TYPE sglang:num_queue_reqs gauge
sglang:num_queue_reqs{engine_type="unified",model_name="glm-5.3",tp_rank="0"} 3.0
sglang:num_queue_reqs{engine_type="unified",model_name="glm-5.3",tp_rank="1"} 11.0
# HELP sglang:token_usage The token usage.
# TYPE sglang:token_usage gauge
sglang:token_usage{engine_type="unified",model_name="glm-5.3",tp_rank="0"} 0.42 1790347303000
sglang:num_queue_reqs_total{model_name="glm-5.3"} 999
sglang:num_running_reqs{model_name="glm-5.3"} 2.0
`

func TestParseGauges(t *testing.T) {
	got, err := parseGauges(strings.NewReader(sglangMetrics), metricQueueRequests, metricTokenUsage)
	if err != nil {
		t.Fatal(err)
	}
	// Max across ranks, not the first or last series; the trailing timestamp is
	// ignored; a metric sharing the prefix (…_total) is not confused for it.
	if got[metricQueueRequests] != 11 {
		t.Errorf("queue = %v, want 11 (max across tp ranks)", got[metricQueueRequests])
	}
	if got[metricTokenUsage] != 0.42 {
		t.Errorf("token usage = %v, want 0.42", got[metricTokenUsage])
	}
}

func TestParseGauges_UnlabelledAndQuotedBraces(t *testing.T) {
	in := "sglang:token_usage 0.9\nsglang:num_queue_reqs{note=\"a } b\"} 4\n"
	got, err := parseGauges(strings.NewReader(in), metricQueueRequests, metricTokenUsage)
	if err != nil {
		t.Fatal(err)
	}
	if got[metricTokenUsage] != 0.9 || got[metricQueueRequests] != 4 {
		t.Fatalf("got %v", got)
	}
}

func newTestGuard(cfg config.OverloadGuardConfig) *Guard {
	cfg.Enabled = true
	if cfg.PollInterval == 0 {
		cfg.PollInterval = 5 * time.Second
	}
	if cfg.RetryAfter == 0 {
		cfg.RetryAfter = 30 * time.Second
	}
	l := discardLogger{}
	return New(cfg, l)
}

func TestCheck(t *testing.T) {
	now := time.Date(2026, 9, 24, 19, 0, 0, 0, time.UTC)
	cases := []struct {
		name     string
		cfg      config.OverloadGuardConfig
		s        *sample
		wantShed bool
	}{
		{"no sample yet fails open", config.OverloadGuardConfig{MaxQueueRequests: 5}, nil, false},
		{"stale sample fails open", config.OverloadGuardConfig{MaxQueueRequests: 5},
			&sample{at: now.Add(-16 * time.Second), queueRequests: 27}, false},
		{"fresh enough sample counts", config.OverloadGuardConfig{MaxQueueRequests: 5},
			&sample{at: now.Add(-14 * time.Second), queueRequests: 27}, true},
		{"exactly three intervals old still counts", config.OverloadGuardConfig{MaxQueueRequests: 5},
			&sample{at: now.Add(-15 * time.Second), queueRequests: 27}, true},
		{"queue at the limit sheds", config.OverloadGuardConfig{MaxQueueRequests: 5},
			&sample{at: now, queueRequests: 5}, true},
		{"queue below the limit admits", config.OverloadGuardConfig{MaxQueueRequests: 5},
			&sample{at: now, queueRequests: 4}, false},
		{"kv at the limit sheds", config.OverloadGuardConfig{MaxTokenUsage: 0.95},
			&sample{at: now, tokenUsage: 0.95}, true},
		{"kv below the limit admits", config.OverloadGuardConfig{MaxTokenUsage: 0.95},
			&sample{at: now, tokenUsage: 0.94}, false},
		// The 2026-09-24 shape: few running, KV pinned — queue condition off.
		{"kv alone sheds with queue disabled", config.OverloadGuardConfig{MaxTokenUsage: 0.95},
			&sample{at: now, queueRequests: 27, tokenUsage: 0.99}, true},
		{"disabled condition never sheds", config.OverloadGuardConfig{MaxTokenUsage: 0.95},
			&sample{at: now, queueRequests: 1000, tokenUsage: 0.1}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g := newTestGuard(tc.cfg)
			g.now = func() time.Time { return now }
			if tc.s != nil {
				g.latest.Store(tc.s)
			}
			shed, reason := g.Check()
			if shed != tc.wantShed {
				t.Fatalf("shed = %v (%q), want %v", shed, reason, tc.wantShed)
			}
			if shed && reason == "" {
				t.Fatal("a shed verdict must carry a reason for the log")
			}
		})
	}
}

func TestNilGuardNeverSheds(t *testing.T) {
	var g *Guard
	if shed, _ := g.Check(); shed {
		t.Fatal("nil guard shed")
	}
	if New(config.OverloadGuardConfig{Enabled: false}, nil) != nil {
		t.Fatal("disabled config must produce a nil guard")
	}
	g.Run(context.Background()) // must return immediately, not block
}

func TestScrape(t *testing.T) {
	body, status := sglangMetrics, http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	g := newTestGuard(config.OverloadGuardConfig{MetricsURL: srv.URL, MaxQueueRequests: 5})
	g.poll(context.Background())
	if shed, _ := g.Check(); !shed {
		t.Fatal("queue of 11 against a limit of 5 must shed after a scrape")
	}

	// A failed scrape fails open at once: the last verdict is not acted on
	// while the engine cannot confirm it.
	status = http.StatusInternalServerError
	g.poll(context.Background())
	if shed, _ := g.Check(); shed {
		t.Fatal("a failed scrape must stop shedding immediately")
	}

	// And a recovered endpoint re-arms it.
	status = http.StatusOK
	g.poll(context.Background())
	if shed, _ := g.Check(); !shed {
		t.Fatal("a successful scrape after a failure must re-arm the guard")
	}

	// An endpoint missing one gauge is refused rather than read as 0.
	status, body = http.StatusOK, "sglang:num_queue_reqs 0\n"
	if _, err := g.scrape(context.Background()); err == nil {
		t.Fatal("scrape must fail when sglang:token_usage is absent")
	}
}

type discardLogger struct{}

func (discardLogger) Infof(string, ...interface{}) {}
func (discardLogger) Warnf(string, ...interface{}) {}

func TestParseGauges_SkipsNaN(t *testing.T) {
	// A NaN first would otherwise stick as the maximum (every comparison with
	// NaN is false) and disarm the condition for good.
	got, err := parseGauges(strings.NewReader("sglang:token_usage NaN\nsglang:token_usage 0.97\n"), metricTokenUsage)
	if err != nil {
		t.Fatal(err)
	}
	if got[metricTokenUsage] != 0.97 {
		t.Fatalf("token usage = %v, want 0.97 with the NaN series skipped", got[metricTokenUsage])
	}
}

func saturatedServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("sglang:num_queue_reqs 27\nsglang:token_usage 0.99\n"))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// Start is main's whole wiring. A long poll interval means only Run's
// immediate first scrape can arm it within the deadline.
func TestStart(t *testing.T) {
	if Start(context.Background(), config.OverloadGuardConfig{}, discardLogger{}) != nil {
		t.Fatal("disabled config must start nothing")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	g := Start(ctx, config.OverloadGuardConfig{
		Enabled: true, MetricsURL: saturatedServer(t).URL, MaxQueueRequests: 5,
		PollInterval: time.Hour, RetryAfter: 30 * time.Second,
	}, discardLogger{})
	deadline := time.Now().Add(2 * time.Second)
	for {
		if shed, _ := g.Check(); shed {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("guard not armed by the first scrape: Run must poll immediately, not after one interval")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A metrics endpoint that accepts the connection and never answers must not
// wedge the poller: the scrape is bounded by the poll interval.
func TestPoll_HungEndpointIsBounded(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(release)

	g := newTestGuard(config.OverloadGuardConfig{MetricsURL: srv.URL, MaxQueueRequests: 5, PollInterval: time.Second})
	done := make(chan struct{})
	go func() { g.poll(context.Background()); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("poll against a hung endpoint did not return: the scrape has no timeout")
	}
	if shed, _ := g.Check(); shed {
		t.Fatal("a timed-out scrape must fail open")
	}
}

type recLogger struct{ lines []string }

func (l *recLogger) Infof(f string, a ...interface{}) {
	l.lines = append(l.lines, "INFO "+fmt.Sprintf(f, a...))
}
func (l *recLogger) Warnf(f string, a ...interface{}) {
	l.lines = append(l.lines, "WARN "+fmt.Sprintf(f, a...))
}

// Load at a threshold flips the verdict every poll; the log must stay at about
// one line a minute and still say that it was oscillating.
func TestTransitionLogging(t *testing.T) {
	l := &recLogger{}
	g := newTestGuard(config.OverloadGuardConfig{MaxQueueRequests: 5})
	g.logger = l
	t0 := time.Date(2026, 9, 24, 19, 0, 0, 0, time.UTC)
	at := func(d time.Duration, state string) {
		g.now = func() time.Time { return t0.Add(d) }
		g.transition(state, state)
	}
	at(0, "ok")             // healthy start: not logged
	at(5*time.Second, "ok") // no change
	at(10*time.Second, "shedding")
	at(15*time.Second, "ok")       // within the gap: suppressed
	at(20*time.Second, "shedding") // suppressed
	at(25*time.Second, "ok")       // suppressed
	at(80*time.Second, "scrape-failed")

	want := []string{"WARN shedding", "WARN scrape-failed (3 earlier state changes not logged)"}
	if strings.Join(l.lines, "|") != strings.Join(want, "|") {
		t.Fatalf("log lines = %q, want %q", l.lines, want)
	}
}

// gaugeValue reads a gauge from the default registry — the one /metrics serves
// — so the test sees what an operator's alert would.
func gaugeValue(t *testing.T, name string) float64 {
	t.Helper()
	mfs, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, mf := range mfs {
		if mf.GetName() == name && len(mf.GetMetric()) == 1 {
			return mf.GetMetric()[0].GetGauge().GetValue()
		}
	}
	t.Fatalf("gauge %s not registered", name)
	return 0
}

// broker_overload_guard_up is how an operator learns that an enabled guard is
// not protecting anything (sglang without --enable-metrics, a renamed gauge);
// _shedding is what dashboards plot. Both must follow each scrape.
func TestPoll_UpdatesGauges(t *testing.T) {
	status, body := http.StatusOK, "sglang:num_queue_reqs 0\nsglang:token_usage 0.5\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()
	g := newTestGuard(config.OverloadGuardConfig{MetricsURL: srv.URL, MaxQueueRequests: 5})

	// Healthy first, so a gauge that merely mirrored "scrape succeeded" fails.
	g.poll(context.Background())
	if up, shed := gaugeValue(t, "broker_overload_guard_up"), gaugeValue(t, "broker_overload_guard_shedding"); up != 1 || shed != 0 {
		t.Fatalf("after a healthy scrape up=%v shedding=%v, want 1/0", up, shed)
	}
	body = "sglang:num_queue_reqs 27\nsglang:token_usage 0.99\n"
	g.poll(context.Background())
	if up, shed := gaugeValue(t, "broker_overload_guard_up"), gaugeValue(t, "broker_overload_guard_shedding"); up != 1 || shed != 1 {
		t.Fatalf("after a saturated scrape up=%v shedding=%v, want 1/1", up, shed)
	}
	status = http.StatusNotFound
	g.poll(context.Background())
	if up, shed := gaugeValue(t, "broker_overload_guard_up"), gaugeValue(t, "broker_overload_guard_shedding"); up != 0 || shed != 0 {
		t.Fatalf("after a failed scrape up=%v shedding=%v, want 0/0", up, shed)
	}
}

// TestMain initialises the default registry and the guard gauges once, before
// any test starts a poller: PrometheusInit may run only once per process
// (-count=N would re-register), and registering the gauges while a poller from
// an earlier test is still writing them is a data race.
func TestMain(m *testing.M) {
	monitor.PrometheusInit("overload-guard-test", "0x00000000000000000000000000000000000000bb")
	monitor.EnableOverloadGuardMetrics()
	os.Exit(m.Run())
}
