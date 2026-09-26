package overload

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/0glabs/0g-serving-broker/inference/config"
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

	// A failed scrape keeps the old sample; it must not be replaced by zeros
	// (which would admit) or kept forever (covered by the stale case in TestCheck).
	status = http.StatusInternalServerError
	g.poll(context.Background())
	if shed, _ := g.Check(); !shed {
		t.Fatal("a failed scrape must not overwrite the last good sample")
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
