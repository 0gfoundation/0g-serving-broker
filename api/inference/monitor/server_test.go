package monitor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

// setupTestMetrics creates fresh metrics with a unique server name to avoid
// registration conflicts between tests. Each test gets its own registry.
func setupTestMetrics(t *testing.T) *prometheus.Registry {
	t.Helper()
	registry := prometheus.NewRegistry()

	serverName := t.Name()

	InputTokensTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name:        "broker_input_tokens_total",
			Help:        "Cumulative input token count.",
			ConstLabels: prometheus.Labels{"server": serverName},
		},
		[]string{"service_type"},
	)

	OutputTokensTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name:        "broker_output_tokens_total",
			Help:        "Cumulative output token count.",
			ConstLabels: prometheus.Labels{"server": serverName},
		},
		[]string{"service_type"},
	)

	TokensPerSecond = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:        "broker_tokens_per_second",
			Help:        "Per-request output token generation rate.",
			Buckets:     []float64{1, 5, 10, 20, 30, 50, 75, 100, 150, 200, 500},
			ConstLabels: prometheus.Labels{"server": serverName},
		},
		[]string{"service_type"},
	)

	RequestCount = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name:        "broker_requests_total",
			Help:        "Total number of HTTP requests.",
			ConstLabels: prometheus.Labels{"server": serverName},
		},
		[]string{"path", "status"},
	)

	ErrorCount = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name:        "broker_requests_errors_total",
			Help:        "Total number of error requests.",
			ConstLabels: prometheus.Labels{"server": serverName},
		},
		[]string{"path", "status"},
	)

	RequestDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:        "broker_request_duration_seconds",
			Help:        "Histogram of request latencies.",
			Buckets:     prometheus.DefBuckets,
			ConstLabels: prometheus.Labels{"server": serverName},
		},
		[]string{"path"},
	)

	registry.MustRegister(InputTokensTotal, OutputTokensTotal, TokensPerSecond,
		RequestCount, ErrorCount, RequestDuration)

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
	PrometheusInit("")
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
			beforeInput := getCounterValue(InputTokensTotal, tt.serviceType)
			beforeOutput := getCounterValue(OutputTokensTotal, tt.serviceType)

			RecordTokens(tt.serviceType, tt.inputTokens, tt.outputTokens)

			afterInput := getCounterValue(InputTokensTotal, tt.serviceType)
			afterOutput := getCounterValue(OutputTokensTotal, tt.serviceType)

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
	RecordTokens("chatbot", 100, 50)
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
			beforeCount := getHistogramCount(TokensPerSecond, tt.serviceType)

			RecordTPS(tt.serviceType, tt.tps)

			afterCount := getHistogramCount(TokensPerSecond, tt.serviceType)
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
	RecordTPS("chatbot", 42.5)
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
			beforeCount := getHistogramCount(TokensPerSecond, tt.serviceType)

			ctx := tt.setupCtx()
			RecordTPSFromContext(ctx, tt.serviceType, tt.outputTokens)

			afterCount := getHistogramCount(TokensPerSecond, tt.serviceType)
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

	beforeSum := getHistogramSum(TokensPerSecond, "chatbot")

	RecordTPSFromContext(c, "chatbot", 100)

	afterSum := getHistogramSum(TokensPerSecond, "chatbot")
	observedTPS := afterSum - beforeSum

	// With 100 tokens over ~2 seconds, TPS should be approximately 50
	// Allow tolerance since time.Now() introduces slight variance
	if observedTPS < 40 || observedTPS > 60 {
		t.Errorf("TPS = %v, want approximately 50 (100 tokens / 2 seconds)", observedTPS)
	}
}

// TestRecordUniqueUser verifies non-blocking user recording behavior.
func TestRecordUniqueUser(t *testing.T) {
	// Initialize channel for testing
	uniqueUsersChan = make(chan string, 10)

	t.Run("records non-empty address", func(t *testing.T) {
		RecordUniqueUser("0xabc123")
		select {
		case addr := <-uniqueUsersChan:
			if addr != "0xabc123" {
				t.Errorf("got %q, want %q", addr, "0xabc123")
			}
		default:
			t.Error("expected address in channel, but channel was empty")
		}
	})

	t.Run("skips empty address", func(t *testing.T) {
		RecordUniqueUser("")
		select {
		case addr := <-uniqueUsersChan:
			t.Errorf("expected no address in channel, got %q", addr)
		default:
			// expected
		}
	})

	t.Run("skips when channel is nil", func(t *testing.T) {
		saved := uniqueUsersChan
		uniqueUsersChan = nil
		defer func() { uniqueUsersChan = saved }()

		// Should not panic
		RecordUniqueUser("0xabc123")
	})

	t.Run("non-blocking when channel is full", func(t *testing.T) {
		fullChan := make(chan string, 1)
		fullChan <- "existing"
		uniqueUsersChan = fullChan

		// Should not block
		RecordUniqueUser("0xnew")

		// Channel should still contain only the original item
		addr := <-fullChan
		if addr != "existing" {
			t.Errorf("got %q, want %q", addr, "existing")
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
		beforeCount := getCounterValue(RequestCount, "/api/test", "OK")

		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/test", nil)
		engine.ServeHTTP(w, req)

		afterCount := getCounterValue(RequestCount, "/api/test", "OK")
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
