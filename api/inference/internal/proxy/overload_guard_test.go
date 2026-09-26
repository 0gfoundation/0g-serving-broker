package proxy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/0glabs/0g-serving-broker/inference/config"
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
	reached    bool
}

func runThroughGuard(t *testing.T, g *overload.Guard, method string) guardResult {
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
	})
	r.Use(p.overloadGuardMiddleware())
	r.Any("/x", func(c *gin.Context) {
		res.reached = true
		c.String(http.StatusOK, "ok")
	})

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(method, "/x", nil))
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
	if res.status != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", res.status)
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
