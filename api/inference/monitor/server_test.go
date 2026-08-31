package monitor

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/0glabs/0g-serving-broker/common/log"
	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	logrus "github.com/sirupsen/logrus"
)

// testLogger is a no-op logger for testing.
type testLogger struct{}

func (testLogger) Debugf(string, ...interface{})       {}
func (testLogger) Infof(string, ...interface{})        {}
func (testLogger) Printf(string, ...interface{})       {}
func (testLogger) Warnf(string, ...interface{})        {}
func (testLogger) Warningf(string, ...interface{})     {}
func (testLogger) Errorf(string, ...interface{})       {}
func (testLogger) Fatalf(string, ...interface{})       {}
func (testLogger) Panicf(string, ...interface{})       {}
func (testLogger) Debug(...interface{})                {}
func (testLogger) Info(...interface{})                 {}
func (testLogger) Print(...interface{})                {}
func (testLogger) Warn(...interface{})                 {}
func (testLogger) Warning(...interface{})              {}
func (testLogger) Error(...interface{})                {}
func (testLogger) Fatal(...interface{})                {}
func (testLogger) Panic(...interface{})                {}
func (testLogger) Debugln(...interface{})              {}
func (testLogger) Infoln(...interface{})               {}
func (testLogger) Println(...interface{})              {}
func (testLogger) Warnln(...interface{})               {}
func (testLogger) Warningln(...interface{})            {}
func (testLogger) Errorln(...interface{})              {}
func (testLogger) Fatalln(...interface{})              {}
func (testLogger) Panicln(...interface{})              {}
func (testLogger) WithFields(logrus.Fields) log.Logger { return testLogger{} }
func (testLogger) InnerLogger() *logrus.Logger         { return logrus.New() }

// setupTestMetrics creates fresh metrics with a unique server name to avoid
// registration conflicts between tests. Each test gets its own registry.
func setupTestMetrics(t *testing.T) *prometheus.Registry {
	t.Helper()
	registry := prometheus.NewRegistry()

	serverName := t.Name()
	// Mirror production: PrometheusInit stamps both server and
	// provider_address as const labels. Keep the helper in lockstep so tests
	// exercise the same series identity (and so a future label change is
	// caught here, not only at runtime).
	constLabels := prometheus.Labels{"server": serverName, "provider_address": "0x" + "0000000000000000000000000000000000000001"}

	InputTokensTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name:        "broker_input_tokens_total",
			Help:        "Cumulative input token count.",
			ConstLabels: constLabels,
		},
		[]string{"service_type", "model"},
	)

	OutputTokensTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name:        "broker_output_tokens_total",
			Help:        "Cumulative output token count.",
			ConstLabels: constLabels,
		},
		[]string{"service_type", "model"},
	)

	TokensPerSecond = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:        "broker_tokens_per_second",
			Help:        "Per-request output token generation rate.",
			Buckets:     []float64{1, 5, 10, 20, 30, 50, 75, 100, 150, 200, 500},
			ConstLabels: constLabels,
		},
		[]string{"service_type", "model"},
	)

	RequestCount = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name:        "broker_requests_total",
			Help:        "Total number of HTTP requests.",
			ConstLabels: constLabels,
		},
		[]string{"path", "status", "model"},
	)

	ErrorCount = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name:        "broker_requests_errors_total",
			Help:        "Total number of error requests.",
			ConstLabels: constLabels,
		},
		[]string{"path", "status"},
	)

	RequestDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:        "broker_request_duration_seconds",
			Help:        "Histogram of request latencies.",
			Buckets:     prometheus.DefBuckets,
			ConstLabels: constLabels,
		},
		[]string{"path"},
	)

	WhitelistRequestsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name:        "broker_whitelist_requests_total",
			Help:        "Total whitelist requests.",
			ConstLabels: constLabels,
		},
		[]string{"service_type", "model"},
	)

	WhitelistInputTokensTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name:        "broker_whitelist_input_tokens_total",
			Help:        "Whitelist input tokens.",
			ConstLabels: constLabels,
		},
		[]string{"service_type", "model"},
	)

	WhitelistOutputTokensTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name:        "broker_whitelist_output_tokens_total",
			Help:        "Whitelist output tokens.",
			ConstLabels: constLabels,
		},
		[]string{"service_type", "model"},
	)

	AudioSecondsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name:        "broker_audio_seconds_total",
			Help:        "Audio seconds.",
			ConstLabels: constLabels,
		},
		[]string{"service_type", "model"},
	)

	WhitelistAudioSecondsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name:        "broker_whitelist_audio_seconds_total",
			Help:        "Whitelist audio seconds.",
			ConstLabels: constLabels,
		},
		[]string{"service_type", "model"},
	)

	FailureCount = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name:        "broker_request_failures_total",
			Help:        "Total failed requests.",
			ConstLabels: constLabels,
		},
		[]string{"source", "code", "model", "status"},
	)

	registry.MustRegister(InputTokensTotal, OutputTokensTotal, TokensPerSecond,
		RequestCount, ErrorCount, RequestDuration,
		WhitelistRequestsTotal, WhitelistInputTokensTotal, WhitelistOutputTokensTotal,
		AudioSecondsTotal, WhitelistAudioSecondsTotal, FailureCount)

	return registry
}

func getCounterValue(counter *prometheus.CounterVec, labels ...string) float64 {
	m := &dto.Metric{}
	if err := counter.WithLabelValues(labels...).Write(m); err != nil {
		return 0
	}
	return m.GetCounter().GetValue()
}

func getHistogramCount(hist *prometheus.HistogramVec, labels ...string) uint64 {
	m := &dto.Metric{}
	observer := hist.WithLabelValues(labels...)
	if h, ok := observer.(prometheus.Metric); ok {
		if err := h.Write(m); err != nil {
			return 0
		}
	}
	return m.GetHistogram().GetSampleCount()
}

func getHistogramSum(hist *prometheus.HistogramVec, labels ...string) float64 {
	m := &dto.Metric{}
	observer := hist.WithLabelValues(labels...)
	if h, ok := observer.(prometheus.Metric); ok {
		if err := h.Write(m); err != nil {
			return 0
		}
	}
	return m.GetHistogram().GetSampleSum()
}

// TestPrometheusInitPanicsOnEmptyName verifies that PrometheusInit panics
// when given an empty server name.
func TestPrometheusInitPanicsOnEmptyName(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("PrometheusInit(\"\") did not panic")
		}
	}()
	PrometheusInit("", "0x1234567890abcdef1234567890abcdef12345678")
}

// TestPrometheusInitPanicsOnBadAddress verifies the provider_address format
// guard: an empty or malformed address (or swapped arguments — a ServingURL
// never matches) must panic rather than silently voiding the address label
// on every series.
func TestPrometheusInitPanicsOnBadAddress(t *testing.T) {
	for _, addr := range []string{"", "not-an-address", "https://compute-network-1.example.com", "0x123"} {
		func() {
			defer func() {
				if r := recover(); r == nil {
					t.Errorf("PrometheusInit with address %q did not panic", addr)
				}
			}()
			PrometheusInit(t.Name(), addr)
		}()
	}
}

// TestRecordTokens verifies token counter increments for various inputs.
func TestRecordTokens(t *testing.T) {
	setupTestMetrics(t)

	tests := []struct {
		name                string
		serviceType         string
		inputTokens         int64
		outputTokens        int64
		wantInputIncrement  float64
		wantOutputIncrement float64
	}{
		{
			name:                "both input and output tokens",
			serviceType:         "chatbot",
			inputTokens:         100,
			outputTokens:        50,
			wantInputIncrement:  100,
			wantOutputIncrement: 50,
		},
		{
			name:                "only input tokens",
			serviceType:         "chatbot",
			inputTokens:         200,
			outputTokens:        0,
			wantInputIncrement:  200,
			wantOutputIncrement: 0,
		},
		{
			name:                "only output tokens",
			serviceType:         "chatbot",
			inputTokens:         0,
			outputTokens:        75,
			wantInputIncrement:  0,
			wantOutputIncrement: 75,
		},
		{
			name:                "negative tokens ignored",
			serviceType:         "chatbot",
			inputTokens:         -10,
			outputTokens:        -5,
			wantInputIncrement:  0,
			wantOutputIncrement: 0,
		},
		{
			name:                "speech_to_text service type",
			serviceType:         "speech_to_text",
			inputTokens:         50,
			outputTokens:        30,
			wantInputIncrement:  50,
			wantOutputIncrement: 30,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			beforeInput := getCounterValue(InputTokensTotal, tt.serviceType, "glm-5")
			beforeOutput := getCounterValue(OutputTokensTotal, tt.serviceType, "glm-5")

			RecordTokens(tt.serviceType, "glm-5", tt.inputTokens, tt.outputTokens)

			afterInput := getCounterValue(InputTokensTotal, tt.serviceType, "glm-5")
			afterOutput := getCounterValue(OutputTokensTotal, tt.serviceType, "glm-5")

			inputDelta := afterInput - beforeInput
			outputDelta := afterOutput - beforeOutput

			if inputDelta != tt.wantInputIncrement {
				t.Errorf("input tokens delta = %v, want %v", inputDelta, tt.wantInputIncrement)
			}
			if outputDelta != tt.wantOutputIncrement {
				t.Errorf("output tokens delta = %v, want %v", outputDelta, tt.wantOutputIncrement)
			}
		})
	}
}

// TestRecordTokensNilMetrics verifies RecordTokens is a no-op when metrics are nil.
func TestRecordTokensNilMetrics(t *testing.T) {
	saved := InputTokensTotal
	InputTokensTotal = nil
	defer func() { InputTokensTotal = saved }()

	// Should not panic
	RecordTokens("chatbot", "glm-5", 100, 50)
}

// TestRecordAudioSeconds verifies the duration counter increments for positive
// values and no-ops for non-positive values. The counter is intentionally
// separate from RecordTokens so dashboards don't mix seconds and tokens on the
// same axis — that's the bug PR #523 review caught.
func TestRecordAudioSeconds(t *testing.T) {
	setupTestMetrics(t)

	tests := []struct {
		name        string
		serviceType string
		seconds     int64
		want        float64
	}{
		{"positive seconds", "speech_to_text", 207, 207},
		{"zero seconds is no-op", "speech_to_text", 0, 0},
		{"negative seconds is no-op", "speech_to_text", -5, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before := getCounterValue(AudioSecondsTotal, tt.serviceType, "whisper-large-v3")
			RecordAudioSeconds(tt.serviceType, "whisper-large-v3", tt.seconds)
			delta := getCounterValue(AudioSecondsTotal, tt.serviceType, "whisper-large-v3") - before
			if delta != tt.want {
				t.Errorf("audio seconds delta = %v, want %v", delta, tt.want)
			}
		})
	}
}

// TestRecordAudioSecondsNilMetrics verifies the helper is a safe no-op when
// the counter hasn't been initialised (e.g. tests that don't call PrometheusInit).
func TestRecordAudioSecondsNilMetrics(t *testing.T) {
	saved := AudioSecondsTotal
	AudioSecondsTotal = nil
	defer func() { AudioSecondsTotal = saved }()

	// Should not panic
	RecordAudioSeconds("speech_to_text", "whisper-large-v3", 100)
}

// TestRecordWhitelistAudioSeconds mirrors TestRecordAudioSeconds for the
// whitelist counter family.
func TestRecordWhitelistAudioSeconds(t *testing.T) {
	setupTestMetrics(t)

	before := getCounterValue(WhitelistAudioSecondsTotal, "speech_to_text", "whisper-large-v3")
	RecordWhitelistAudioSeconds("speech_to_text", "whisper-large-v3", 207)
	if delta := getCounterValue(WhitelistAudioSecondsTotal, "speech_to_text", "whisper-large-v3") - before; delta != 207 {
		t.Errorf("whitelist audio seconds delta = %v, want 207", delta)
	}

	// Zero/negative no-op
	before = getCounterValue(WhitelistAudioSecondsTotal, "speech_to_text", "whisper-large-v3")
	RecordWhitelistAudioSeconds("speech_to_text", "whisper-large-v3", 0)
	RecordWhitelistAudioSeconds("speech_to_text", "whisper-large-v3", -10)
	if delta := getCounterValue(WhitelistAudioSecondsTotal, "speech_to_text", "whisper-large-v3") - before; delta != 0 {
		t.Errorf("whitelist audio seconds should not increment on non-positive, got delta=%v", delta)
	}
}

func TestRecordWhitelistAudioSecondsNilMetrics(t *testing.T) {
	saved := WhitelistAudioSecondsTotal
	WhitelistAudioSecondsTotal = nil
	defer func() { WhitelistAudioSecondsTotal = saved }()

	// Should not panic
	RecordWhitelistAudioSeconds("speech_to_text", "whisper-large-v3", 100)
}

// TestRecordTPS verifies TPS histogram recording for various inputs.
func TestRecordTPS(t *testing.T) {
	setupTestMetrics(t)

	tests := []struct {
		name        string
		serviceType string
		tps         float64
		wantRecord  bool
	}{
		{
			name:        "positive TPS recorded",
			serviceType: "chatbot",
			tps:         42.5,
			wantRecord:  true,
		},
		{
			name:        "zero TPS not recorded",
			serviceType: "chatbot",
			tps:         0,
			wantRecord:  false,
		},
		{
			name:        "negative TPS not recorded",
			serviceType: "chatbot",
			tps:         -10,
			wantRecord:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			beforeCount := getHistogramCount(TokensPerSecond, tt.serviceType, "glm-5")

			RecordTPS(tt.serviceType, "glm-5", tt.tps)

			afterCount := getHistogramCount(TokensPerSecond, tt.serviceType, "glm-5")
			recorded := afterCount > beforeCount

			if recorded != tt.wantRecord {
				t.Errorf("recorded = %v, want %v", recorded, tt.wantRecord)
			}
		})
	}
}

// TestRecordTPSNilMetrics verifies RecordTPS is a no-op when metrics are nil.
func TestRecordTPSNilMetrics(t *testing.T) {
	saved := TokensPerSecond
	TokensPerSecond = nil
	defer func() { TokensPerSecond = saved }()

	// Should not panic
	RecordTPS("chatbot", "glm-5", 42.5)
}

// TestRecordTPSFromContext verifies TPS calculation from context start time.
func TestRecordTPSFromContext(t *testing.T) {
	setupTestMetrics(t)

	tests := []struct {
		name         string
		setupCtx     func() context.Context
		serviceType  string
		outputTokens int64
		wantRecord   bool
	}{
		{
			name: "valid context with start time",
			setupCtx: func() context.Context {
				// Use a gin.Context to match real usage (gin stores values in its own map)
				gin.SetMode(gin.TestMode)
				w := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(w)
				c.Set(RequestStartTimeKey, time.Now().Add(-1*time.Second))
				return c
			},
			serviceType:  "chatbot",
			outputTokens: 100,
			wantRecord:   true,
		},
		{
			name: "zero output tokens not recorded",
			setupCtx: func() context.Context {
				gin.SetMode(gin.TestMode)
				w := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(w)
				c.Set(RequestStartTimeKey, time.Now().Add(-1*time.Second))
				return c
			},
			serviceType:  "chatbot",
			outputTokens: 0,
			wantRecord:   false,
		},
		{
			name: "negative output tokens not recorded",
			setupCtx: func() context.Context {
				gin.SetMode(gin.TestMode)
				w := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(w)
				c.Set(RequestStartTimeKey, time.Now().Add(-1*time.Second))
				return c
			},
			serviceType:  "chatbot",
			outputTokens: -5,
			wantRecord:   false,
		},
		{
			name: "missing start time in context",
			setupCtx: func() context.Context {
				return context.Background()
			},
			serviceType:  "chatbot",
			outputTokens: 100,
			wantRecord:   false,
		},
		{
			name: "wrong type for start time in context",
			setupCtx: func() context.Context {
				return context.WithValue(context.Background(), RequestStartTimeKey, "not-a-time")
			},
			serviceType:  "chatbot",
			outputTokens: 100,
			wantRecord:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			beforeCount := getHistogramCount(TokensPerSecond, tt.serviceType, "glm-5")

			ctx := tt.setupCtx()
			RecordTPSFromContext(ctx, tt.serviceType, "glm-5", tt.outputTokens)

			afterCount := getHistogramCount(TokensPerSecond, tt.serviceType, "glm-5")
			recorded := afterCount > beforeCount

			if recorded != tt.wantRecord {
				t.Errorf("recorded = %v, want %v", recorded, tt.wantRecord)
			}
		})
	}
}

// TestRecordTPSFromContextCalculation verifies the TPS value is calculated correctly.
func TestRecordTPSFromContextCalculation(t *testing.T) {
	setupTestMetrics(t)

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)

	// Set start time 2 seconds ago
	c.Set(RequestStartTimeKey, time.Now().Add(-2*time.Second))

	beforeSum := getHistogramSum(TokensPerSecond, "chatbot", "glm-5")

	RecordTPSFromContext(c, "chatbot", "glm-5", 100)

	afterSum := getHistogramSum(TokensPerSecond, "chatbot", "glm-5")
	observedTPS := afterSum - beforeSum

	// With 100 tokens over ~2 seconds, TPS should be approximately 50
	// Allow tolerance since time.Now() introduces slight variance
	if observedTPS < 40 || observedTPS > 60 {
		t.Errorf("TPS = %v, want approximately 50 (100 tokens / 2 seconds)", observedTPS)
	}
}

// TestStartDAUUpdater verifies the DB-backed DAU updater sets the gauge correctly.
func TestStartDAUUpdater(t *testing.T) {
	setupTestMetrics(t)

	// Create a fresh gauge for testing
	UniqueUsersTotal = prometheus.NewGauge(prometheus.GaugeOpts{
		Name:        "broker_unique_users_total_test",
		Help:        "Test gauge",
		ConstLabels: prometheus.Labels{"server": t.Name()},
	})

	t.Run("sets gauge from query function on startup", func(t *testing.T) {
		queryFunc := func() (int64, error) {
			return 42, nil
		}

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		StartDAUUpdater(ctx, queryFunc, 1*time.Hour, testLogger{})

		// Give the goroutine time to run the initial query
		time.Sleep(50 * time.Millisecond)

		m := &dto.Metric{}
		if err := UniqueUsersTotal.Write(m); err != nil {
			t.Fatalf("failed to read gauge: %v", err)
		}
		if got := m.GetGauge().GetValue(); got != 42 {
			t.Errorf("UniqueUsersTotal = %v, want 42", got)
		}
	})
}

// TestTrackMetricsSetsStartTime verifies the middleware stores the request start time.
func TestTrackMetricsSetsStartTime(t *testing.T) {
	setupTestMetrics(t)

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, engine := gin.CreateTestContext(w)

	engine.Use(TrackMetrics())
	engine.GET("/test", func(c *gin.Context) {
		// Verify start time is set in context
		val, exists := c.Get(RequestStartTimeKey)
		if !exists {
			t.Error("RequestStartTimeKey not set in context")
			return
		}
		if _, ok := val.(time.Time); !ok {
			t.Errorf("RequestStartTimeKey is %T, want time.Time", val)
		}
		c.Status(http.StatusOK)
	})

	c.Request = httptest.NewRequest(http.MethodGet, "/test", nil)
	engine.ServeHTTP(w, c.Request)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", w.Code, http.StatusOK)
	}
}

// TestTrackMetricsRecordsRequestMetrics verifies the middleware records request count and duration.
func TestTrackMetricsRecordsRequestMetrics(t *testing.T) {
	setupTestMetrics(t)

	gin.SetMode(gin.TestMode)

	engine := gin.New()
	engine.Use(TrackMetrics())
	engine.GET("/api/test", func(c *gin.Context) {
		c.Status(http.StatusOK)
	})
	engine.GET("/api/error", func(c *gin.Context) {
		c.Status(http.StatusInternalServerError)
	})

	t.Run("successful request increments counter", func(t *testing.T) {
		beforeCount := getCounterValue(RequestCount, "/api/test", "OK", "")

		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/test", nil)
		engine.ServeHTTP(w, req)

		afterCount := getCounterValue(RequestCount, "/api/test", "OK", "")
		if afterCount-beforeCount != 1 {
			t.Errorf("request count delta = %v, want 1", afterCount-beforeCount)
		}
	})

	t.Run("error request increments error counter", func(t *testing.T) {
		beforeCount := getCounterValue(ErrorCount, "/api/error", "Internal Server Error")

		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/error", nil)
		engine.ServeHTTP(w, req)

		afterCount := getCounterValue(ErrorCount, "/api/error", "Internal Server Error")
		if afterCount-beforeCount != 1 {
			t.Errorf("error count delta = %v, want 1", afterCount-beforeCount)
		}
	})

	t.Run("request counter carries the bounded metric model", func(t *testing.T) {
		engine.GET("/api/model", func(c *gin.Context) {
			c.Set(CtxKeyMetricModel, "glm-5")
			c.Status(http.StatusOK)
		})

		before := getCounterValue(RequestCount, "/api/model", "OK", "glm-5")

		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/model", nil)
		engine.ServeHTTP(w, req)

		if delta := getCounterValue(RequestCount, "/api/model", "OK", "glm-5") - before; delta != 1 {
			t.Errorf("request count delta for bounded model = %v, want 1", delta)
		}
	})

	t.Run("raw resolvedModel never reaches the request counter label", func(t *testing.T) {
		// On wildcard deployments CtxKeyResolvedModel carries RAW user
		// strings; TrackMetrics must read only the bounded CtxKeyMetricModel
		// (here deliberately unset -> empty label), never the raw key.
		engine.GET("/api/raw", func(c *gin.Context) {
			c.Set(CtxKeyResolvedModel, "attacker/minted-model-string")
			c.Status(http.StatusOK)
		})

		beforeRaw := getCounterValue(RequestCount, "/api/raw", "OK", "attacker/minted-model-string")
		beforeEmpty := getCounterValue(RequestCount, "/api/raw", "OK", "")

		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/raw", nil)
		engine.ServeHTTP(w, req)

		if delta := getCounterValue(RequestCount, "/api/raw", "OK", "attacker/minted-model-string") - beforeRaw; delta != 0 {
			t.Errorf("raw resolvedModel leaked into the request counter label (delta=%v)", delta)
		}
		if delta := getCounterValue(RequestCount, "/api/raw", "OK", "") - beforeEmpty; delta != 1 {
			t.Errorf("expected the empty bounded label, delta = %v, want 1", delta)
		}
	})

	t.Run("records request duration", func(t *testing.T) {
		beforeCount := getHistogramCount(RequestDuration, "/api/test")

		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/test", nil)
		engine.ServeHTTP(w, req)

		afterCount := getHistogramCount(RequestDuration, "/api/test")
		if afterCount <= beforeCount {
			t.Error("request duration histogram not updated")
		}
	})
}

// TestTrackMetricsIgnoreError verifies that errors are not counted when ignoreError is set.
func TestTrackMetricsIgnoreError(t *testing.T) {
	setupTestMetrics(t)

	gin.SetMode(gin.TestMode)

	engine := gin.New()
	engine.Use(TrackMetrics())
	engine.GET("/api/ignored", func(c *gin.Context) {
		c.Set("ignoreError", true)
		c.Status(http.StatusBadRequest)
	})

	beforeCount := getCounterValue(ErrorCount, "/api/ignored", "Bad Request")

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/ignored", nil)
	engine.ServeHTTP(w, req)

	afterCount := getCounterValue(ErrorCount, "/api/ignored", "Bad Request")
	if afterCount != beforeCount {
		t.Errorf("error count delta = %v, want 0 (ignoreError was set)", afterCount-beforeCount)
	}
}

// TestTrackMetricsFailureSource verifies the three-way fault attribution of the
// unified failure counter: a handler's explicit CtxKeyFailureSource override
// wins, and an un-overridden failure is derived from the ignoreError flag and
// status — a client-flagged 4xx is "client" (except the upstream_error
// fallback), while an un-flagged failure (including a broker-fault 4xx that
// errors.Response defaulted to 400) stays "broker".
func TestTrackMetricsFailureSource(t *testing.T) {
	setupTestMetrics(t)

	gin.SetMode(gin.TestMode)

	cases := []struct {
		name        string
		status      int
		setSource   string // explicit CtxKeyFailureSource override, "" for none
		code        string // CtxKeyRejectionReason, "" for none
		ignoreError bool   // handler flagged the failure client-caused
		wantSource  string
		wantStatus  string
	}{
		// No override: derived from ignoreError + status.
		{"broker 5xx -> broker", http.StatusInternalServerError, "", "", false, FailureSourceBroker, "Internal Server Error"},
		// A broker-fault 4xx (errors.Response default-400, no ignoreError) must
		// stay broker — it must not be hidden in the client bucket.
		{"broker 4xx (unflagged) -> broker", http.StatusBadRequest, "", "", false, FailureSourceBroker, "Bad Request"},
		// A client-caused 4xx the handler flagged with ignoreError -> client.
		{"client 4xx (flagged) -> client", http.StatusUnauthorized, "", "", true, FailureSourceClient, "Unauthorized"},
		{"client rejection 4xx (flagged) -> client", http.StatusTooManyRequests, "", RejectionRateLimit, true, FailureSourceClient, "Too Many Requests"},
		// concurrency is a 503: 5xx never derives to client even if flagged.
		{"concurrency 503 -> broker", http.StatusServiceUnavailable, "", RejectionConcurrency, false, FailureSourceBroker, "Service Unavailable"},
		// The global cap's actual production shape, which the case above does
		// NOT cover: it sets ignoreError so the 503 is not counted as a service
		// error, and the "5xx never derives to client even if flagged" claim is
		// only asserted here. Regressing the status guard in
		// resolveFailureSource would silently move every capacity rejection into
		// the client bucket, hiding broker saturation from the broker-fault alert.
		{"global concurrency 503 (flagged) -> broker", http.StatusServiceUnavailable, "", RejectionGlobalConcurrency, true, FailureSourceBroker, "Service Unavailable"},
		// upstream_error fallback stays broker even on a flagged 4xx.
		{"upstream_error 4xx -> broker", http.StatusBadRequest, "", RejectionUpstreamError, true, FailureSourceBroker, "Bad Request"},
		// Explicit overrides always win, regardless of ignoreError/status.
		{"upstream 5xx override", http.StatusBadGateway, FailureSourceUpstream, "", false, FailureSourceUpstream, "Bad Gateway"},
		{"upstream 429 override", http.StatusTooManyRequests, FailureSourceUpstream, "", false, FailureSourceUpstream, "Too Many Requests"},
		{"client 4xx override (upstream rejected)", http.StatusBadRequest, FailureSourceClient, "", true, FailureSourceClient, "Bad Request"},
	}

	engine := gin.New()
	engine.Use(TrackMetrics())
	for i, tc := range cases {
		tc := tc
		engine.GET(fmt.Sprintf("/fs/%d", i), func(c *gin.Context) {
			if tc.ignoreError {
				c.Set("ignoreError", true)
			}
			if tc.setSource != "" {
				c.Set(CtxKeyFailureSource, tc.setSource)
			}
			if tc.code != "" {
				c.Set(CtxKeyRejectionReason, tc.code)
			}
			c.Status(tc.status)
		})
	}

	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := getCounterValue(FailureCount, tc.wantSource, tc.code, "", tc.wantStatus)

			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/fs/%d", i), nil)
			engine.ServeHTTP(w, req)

			if delta := getCounterValue(FailureCount, tc.wantSource, tc.code, "", tc.wantStatus) - before; delta != 1 {
				t.Errorf("failure[source=%s code=%q status=%s] delta = %v, want 1",
					tc.wantSource, tc.code, tc.wantStatus, delta)
			}
		})
	}
}

// TestFailureSourceHeader verifies the ZG-Failure-Source response header: it
// carries the same attribution as the metric on every >=400 response, and is
// absent on a 2xx (including when a proxied upstream tried to forge it).
func TestFailureSourceHeader(t *testing.T) {
	setupTestMetrics(t)
	gin.SetMode(gin.TestMode)

	engine := gin.New()
	engine.Use(TrackMetrics())
	engine.GET("/broker5xx", func(c *gin.Context) { c.JSON(http.StatusInternalServerError, gin.H{"error": "x"}) })
	engine.GET("/broker4xx", func(c *gin.Context) { c.JSON(http.StatusBadRequest, gin.H{"error": "x"}) }) // unflagged -> broker
	engine.GET("/client4xx", func(c *gin.Context) {
		c.Set("ignoreError", true)
		c.JSON(http.StatusUnauthorized, gin.H{"error": "x"})
	})
	engine.GET("/upstream", func(c *gin.Context) {
		c.Set(CtxKeyFailureSource, FailureSourceUpstream)
		c.JSON(http.StatusBadGateway, gin.H{"error": "x"})
	})
	engine.GET("/ok", func(c *gin.Context) {
		// Simulate a forwarded upstream attempt to forge the header on a 2xx.
		c.Header(FailureSourceHeader, "upstream")
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	cases := []struct {
		path string
		want string // expected header value, "" means must be absent
	}{
		{"/broker5xx", FailureSourceBroker},
		{"/broker4xx", FailureSourceBroker},
		{"/client4xx", FailureSourceClient},
		{"/upstream", FailureSourceUpstream},
		{"/ok", ""},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			engine.ServeHTTP(w, req)
			if got := w.Header().Get(FailureSourceHeader); got != tc.want {
				t.Errorf("%s: header = %q, want %q", tc.path, got, tc.want)
			}
		})
	}
}

// TestRecordWhitelistRequest verifies the whitelist request counter.
func TestRecordWhitelistRequest(t *testing.T) {
	setupTestMetrics(t)

	tests := []struct {
		name        string
		serviceType string
		calls       int
		wantDelta   float64
	}{
		{
			name:        "single chatbot request",
			serviceType: "chatbot",
			calls:       1,
			wantDelta:   1,
		},
		{
			name:        "multiple text-to-image requests",
			serviceType: "text-to-image",
			calls:       3,
			wantDelta:   3,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before := getCounterValue(WhitelistRequestsTotal, tt.serviceType, "glm-5")
			for i := 0; i < tt.calls; i++ {
				RecordWhitelistRequest(tt.serviceType, "glm-5")
			}
			after := getCounterValue(WhitelistRequestsTotal, tt.serviceType, "glm-5")
			if delta := after - before; delta != tt.wantDelta {
				t.Errorf("whitelist requests delta = %v, want %v", delta, tt.wantDelta)
			}
		})
	}
}

// TestRecordWhitelistRequestNilMetrics verifies no panic when metrics are nil.
func TestRecordWhitelistRequestNilMetrics(t *testing.T) {
	saved := WhitelistRequestsTotal
	WhitelistRequestsTotal = nil
	defer func() { WhitelistRequestsTotal = saved }()

	RecordWhitelistRequest("chatbot", "glm-5") // should not panic
}

// TestRecordWhitelistTokens verifies the whitelist token counters.
func TestRecordWhitelistTokens(t *testing.T) {
	setupTestMetrics(t)

	tests := []struct {
		name                string
		serviceType         string
		inputTokens         int64
		outputTokens        int64
		wantInputIncrement  float64
		wantOutputIncrement float64
	}{
		{
			name:                "both input and output tokens",
			serviceType:         "chatbot",
			inputTokens:         100,
			outputTokens:        50,
			wantInputIncrement:  100,
			wantOutputIncrement: 50,
		},
		{
			name:                "only input tokens",
			serviceType:         "chatbot",
			inputTokens:         200,
			outputTokens:        0,
			wantInputIncrement:  200,
			wantOutputIncrement: 0,
		},
		{
			name:                "only output tokens",
			serviceType:         "text-to-image",
			inputTokens:         0,
			outputTokens:        75,
			wantInputIncrement:  0,
			wantOutputIncrement: 75,
		},
		{
			name:                "negative tokens ignored",
			serviceType:         "chatbot",
			inputTokens:         -10,
			outputTokens:        -5,
			wantInputIncrement:  0,
			wantOutputIncrement: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			beforeInput := getCounterValue(WhitelistInputTokensTotal, tt.serviceType, "glm-5")
			beforeOutput := getCounterValue(WhitelistOutputTokensTotal, tt.serviceType, "glm-5")

			RecordWhitelistTokens(tt.serviceType, "glm-5", tt.inputTokens, tt.outputTokens)

			afterInput := getCounterValue(WhitelistInputTokensTotal, tt.serviceType, "glm-5")
			afterOutput := getCounterValue(WhitelistOutputTokensTotal, tt.serviceType, "glm-5")

			if delta := afterInput - beforeInput; delta != tt.wantInputIncrement {
				t.Errorf("whitelist input tokens delta = %v, want %v", delta, tt.wantInputIncrement)
			}
			if delta := afterOutput - beforeOutput; delta != tt.wantOutputIncrement {
				t.Errorf("whitelist output tokens delta = %v, want %v", delta, tt.wantOutputIncrement)
			}
		})
	}
}

// TestRecordWhitelistTokensNilMetrics verifies no panic when metrics are nil.
func TestRecordWhitelistTokensNilMetrics(t *testing.T) {
	saved := WhitelistInputTokensTotal
	WhitelistInputTokensTotal = nil
	defer func() { WhitelistInputTokensTotal = saved }()

	RecordWhitelistTokens("chatbot", "glm-5", 100, 50) // should not panic
}
