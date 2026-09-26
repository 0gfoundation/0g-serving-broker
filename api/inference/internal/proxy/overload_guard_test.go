package proxy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/0glabs/0g-serving-broker/inference/config"
	constant "github.com/0glabs/0g-serving-broker/inference/const"
	"github.com/0glabs/0g-serving-broker/inference/internal/ctrl"
	"github.com/0glabs/0g-serving-broker/inference/internal/overload"
	"github.com/0glabs/0g-serving-broker/inference/monitor"
)

// armedGuard returns a guard whose engine reports the given sglang gauges. It
// goes through the real scrape path, so the test covers parse -> verdict ->
// response rather than a stubbed boolean.
func armedGuard(t *testing.T, metrics string) (*overload.Guard, *atomic.Int32) {
	t.Helper()
	hits := &atomic.Int32{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte(metrics))
	}))
	t.Cleanup(srv.Close)
	l := discardLogger{}
	g := overload.New(config.OverloadGuardConfig{
		Enabled:          true,
		MetricsURL:       srv.URL,
		MaxQueueRequests: 5,
		MaxTokenUsage:    0.95,
		PollInterval:     time.Second,
		RetryAfter:       30 * time.Second,
	}, l)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go g.Run(ctx)
	return g, hits
}

type guardResult struct {
	status     int
	retryAfter string
	body       map[string]interface{}
	reason     interface{}
	ignore     interface{}
	source     interface{}
	reached    bool
}

func runThroughGuard(t *testing.T, g *overload.Guard, method string) guardResult {
	return runThroughGuardAt(t, g, method, "/x")
}

func runThroughGuardAt(t *testing.T, g *overload.Guard, method, path string) guardResult {
	t.Helper()
	gin.SetMode(gin.TestMode)
	p := &Proxy{rejections: newTestAggregator(&captureLogger{})}
	p.SetOverloadGuard(g)

	var res guardResult
	r := gin.New()
	// Stands in for TrackMetrics, which reads these keys after the chain unwinds.
	r.Use(func(c *gin.Context) {
		c.Next()
		res.reason, _ = c.Get(monitor.CtxKeyRejectionReason)
		res.ignore, _ = c.Get("ignoreError")
		res.source, _ = c.Get(monitor.CtxKeyFailureSource)
	})
	r.Use(p.overloadGuardMiddleware())
	r.Any(path, func(c *gin.Context) {
		res.reached = true
		c.String(http.StatusOK, "ok")
	})

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(method, path, nil))
	res.status = w.Code
	res.retryAfter = w.Header().Get("Retry-After")
	_ = json.Unmarshal(w.Body.Bytes(), &res.body)
	return res
}

// waitShedding blocks until the guard's first scrape has landed, so the tests
// do not race the poller's initial fetch.
func waitShedding(t *testing.T, g *overload.Guard, want bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if shed, _ := g.Check(); shed == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("guard never reached shed=%v", want)
}

func TestOverloadGuardMiddleware_ShedsPostWhileSaturated(t *testing.T) {
	g, _ := armedGuard(t, "sglang:num_queue_reqs 27\nsglang:token_usage 0.99\n")
	waitShedding(t, g, true)

	res := runThroughGuard(t, g, http.MethodPost)
	if res.reached {
		t.Fatal("a shed request must not reach the handler")
	}
	// 429, not 503: the router trips its breaker on a provider 5xx but treats a
	// 429 as capacity (brief skip, no health penalty) and relays Retry-After.
	if res.status != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", res.status)
	}
	if res.retryAfter != "30" {
		t.Fatalf("Retry-After = %q, want 30", res.retryAfter)
	}
	// The router relays an OpenAI-shaped error body unchanged, which is what gets
	// model_overloaded to the end user instead of a generic provider error.
	errObj, _ := res.body["error"].(map[string]interface{})
	if errObj["code"] != "model_overloaded" || errObj["type"] != "server_error" || errObj["message"] == "" {
		t.Fatalf("body = %v, want an OpenAI error envelope with code model_overloaded", res.body)
	}
	if res.reason != monitor.RejectionBackendOverloaded {
		t.Fatalf("rejection reason = %v, want %q", res.reason, monitor.RejectionBackendOverloaded)
	}
	if res.ignore != true {
		t.Fatal("capacity shedding must be flagged ignoreError, like the global concurrency cap")
	}
	if res.source != monitor.FailureSourceUpstream {
		t.Fatalf("failure source = %v, want upstream: the engine is full, the caller did nothing wrong", res.source)
	}
}

// /v1/messages callers (Anthropic SDKs, Claude Code) get Anthropic's native
// overload envelope, which the router also relays unchanged and the SDKs retry.
func TestOverloadGuardMiddleware_AnthropicEnvelopeOnMessages(t *testing.T) {
	g, _ := armedGuard(t, "sglang:num_queue_reqs 27\nsglang:token_usage 0.99\n")
	waitShedding(t, g, true)

	for _, path := range []string{"/v1/proxy/v1/messages", "/v1/proxy/messages/"} {
		res := runThroughGuardAt(t, g, http.MethodPost, path)
		if res.status != http.StatusTooManyRequests {
			t.Fatalf("%s: status = %d, want 429", path, res.status)
		}
		errObj, _ := res.body["error"].(map[string]interface{})
		if res.body["type"] != "error" || errObj["type"] != "overloaded_error" || errObj["message"] == "" {
			t.Fatalf("%s: body = %v, want an Anthropic overloaded_error envelope", path, res.body)
		}
	}
}

func TestOverloadGuardMiddleware_NeverGatesGet(t *testing.T) {
	g, _ := armedGuard(t, "sglang:num_queue_reqs 27\nsglang:token_usage 0.99\n")
	waitShedding(t, g, true)

	// Signature/image retrieval for a response that already completed must keep
	// working while the engine is saturated.
	if res := runThroughGuard(t, g, http.MethodGet); !res.reached || res.status != http.StatusOK {
		t.Fatalf("GET status = %d reached=%v, want it passed through", res.status, res.reached)
	}
}

func TestOverloadGuardMiddleware_AdmitsWhenHealthyOrUnset(t *testing.T) {
	g, hits := armedGuard(t, "sglang:num_queue_reqs 0\nsglang:token_usage 0.5\n")
	// "Admit" is also the pre-scrape state, so asserting it before a sample has
	// landed would prove nothing. The poller stores a sample before it can issue
	// the next scrape, so a second hit means the first sample is in place.
	deadline := time.Now().Add(5 * time.Second)
	for hits.Load() < 2 {
		if time.Now().After(deadline) {
			t.Fatal("guard never completed a scrape")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if res := runThroughGuard(t, g, http.MethodPost); !res.reached {
		t.Fatalf("healthy engine: status = %d, want admitted", res.status)
	}
	if res := runThroughGuard(t, nil, http.MethodPost); !res.reached {
		t.Fatalf("no guard configured: status = %d, want admitted", res.status)
	}
}

type discardLogger struct{}

func (discardLogger) Infof(string, ...interface{}) {}
func (discardLogger) Warnf(string, ...interface{}) {}

var promInitOnce sync.Once

// The guard as production builds it: registered by New() on the service group,
// behind TrackMetrics, in front of a handler. The tests above call the
// middleware directly and so cannot notice it being dropped from New() or moved
// ahead of TrackMetrics — both of which would leave an enabled guard silently
// doing nothing, or its sheds invisible in the failure metrics.
func TestOverloadGuard_RegisteredByNewBehindTrackMetrics(t *testing.T) {
	gin.SetMode(gin.TestMode)
	promInitOnce.Do(func() {
		monitor.PrometheusInit("overload-guard-test", "0x00000000000000000000000000000000000000aa")
	})
	g, _ := armedGuard(t, "sglang:num_queue_reqs 27\nsglang:token_usage 0.99\n")
	waitShedding(t, g, true)

	engine := gin.New()
	p := New(&ctrl.Ctrl{}, engine, nil, true, config.ConcurrencyLimitConfig{}, noopLogger{})
	p.SetOverloadGuard(g)
	reached := false
	p.serviceGroup.POST("/chat/completions", func(c *gin.Context) {
		reached = true
		c.Status(http.StatusOK)
	})

	shed := monitor.RequestRejectedTotal.WithLabelValues(monitor.RejectionBackendOverloaded)
	before := testutil.ToFloat64(shed)

	w := httptest.NewRecorder()
	engine.ServeHTTP(w, httptest.NewRequest(http.MethodPost, constant.ServicePrefix+"/chat/completions", strings.NewReader("{}")))

	if reached {
		t.Fatal("request reached the handler: the guard is not in New()'s chain")
	}
	if w.Code != http.StatusTooManyRequests || w.Header().Get("Retry-After") != "30" {
		t.Fatalf("status=%d Retry-After=%q, want 429 / 30", w.Code, w.Header().Get("Retry-After"))
	}
	// Stamped by TrackMetrics' writer wrapper, so present only if TrackMetrics
	// wraps the guard — this is what tells the router the engine is full.
	if got := w.Header().Get(monitor.FailureSourceHeader); got != monitor.FailureSourceUpstream {
		t.Fatalf("%s = %q, want %q (guard must sit behind TrackMetrics)", monitor.FailureSourceHeader, got, monitor.FailureSourceUpstream)
	}
	if got := testutil.ToFloat64(shed) - before; got != 1 {
		t.Fatalf("broker_requests_rejected_total{reason=backend_overloaded} rose by %v, want 1", got)
	}
}
