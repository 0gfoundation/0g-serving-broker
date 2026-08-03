package monitor

import (
	"context"
	"net/http"
	"regexp"
	"time"

	"github.com/0glabs/0g-serving-broker/common/log"
	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
)

var (
	RequestCount    *prometheus.CounterVec
	ErrorCount      *prometheus.CounterVec
	RequestDuration *prometheus.HistogramVec

	// UniqueUsersTotal tracks the number of unique users per day
	UniqueUsersTotal prometheus.Gauge

	// InputTokensTotal tracks cumulative input token count, labeled by service_type.
	InputTokensTotal *prometheus.CounterVec
	// OutputTokensTotal tracks cumulative output token count, labeled by service_type.
	OutputTokensTotal *prometheus.CounterVec
	// AudioSecondsTotal tracks cumulative audio duration (in seconds) for
	// duration-billed services like whisper. Kept separate from token counters
	// so dashboards/alerts don't mix orders of magnitude across units.
	AudioSecondsTotal *prometheus.CounterVec
	// TokensPerSecond records per-request output token generation rate as a histogram, labeled by service_type.
	TokensPerSecond *prometheus.HistogramVec

	// All-time cumulative gauges (queried from database, survive restarts)
	AllTimeRequests     prometheus.Gauge
	AllTimeInputTokens  prometheus.Gauge
	AllTimeOutputTokens prometheus.Gauge
	AllTimeUniqueUsers  prometheus.Gauge

	// Whitelist traffic metrics — track requests and tokens from whitelisted
	// (internal/exempt) users so operators can gauge their share of total traffic
	// and set appropriate RPM/TPM limits for the provider.
	WhitelistRequestsTotal     *prometheus.CounterVec
	WhitelistInputTokensTotal  *prometheus.CounterVec
	WhitelistOutputTokensTotal *prometheus.CounterVec
	// WhitelistAudioSecondsTotal mirrors AudioSecondsTotal for whitelisted users.
	WhitelistAudioSecondsTotal *prometheus.CounterVec

	// RoutingProofSkippedTotal counts responses from a centralized provider that
	// were served WITHOUT a TEE routing proof, by reason. A centralized service
	// advertises its verifiability statically in config, so nothing else notices
	// when proof production stops: the individual per-response log lines are the
	// same volume as ordinary traffic. This counter is the aggregate signal —
	// "sidecar rolled back to an image that doesn't report the upstream
	// certificate" and "every proof has silently vanished for an hour" look
	// identical in logs and obvious here.
	RoutingProofSkippedTotal *prometheus.CounterVec

	// VideoBillingSkippedTotal counts video-generation requests that returned
	// 200 but for which no positive duration could be resolved from either the
	// upstream response or the client request — i.e. the video was served
	// WITHOUT being billed. A non-zero value means an upstream's response shape
	// isn't being parsed (e.g. an async provider that doesn't echo seconds), so
	// it must be alertable rather than a silent skip.
	VideoBillingSkippedTotal prometheus.Counter

	// VideoTableMissTotal counts video-generation requests whose observed
	// (resolution, duration) had no exact per_unit_table row, labeled by whether a
	// bucket still covered it (reason=next_bucket) or nothing did and the table
	// maximum was charged (reason=uncovered).
	//
	// Deliberately NOT folded into VideoBillingSkippedTotal: that one means the video
	// was served WITHOUT being billed, and an operator alerting on it is asking "am I
	// giving away output?". A table miss is billed — just at a fallback price rather
	// than the one /v1/models advertises for the request. Different question,
	// different urgency, different fix (add the row), so it gets its own series.
	VideoTableMissTotal *prometheus.CounterVec

	// VideoPollTimedOutTotal counts video-generation poll jobs (see
	// docs/design/video-generation-async-billing.md) that hit their
	// MaxPollDuration ceiling without the provider ever reaching a terminal
	// state. A non-zero value is a genuine reconciliation gap candidate — the
	// provider may have delivered a video the broker never billed for.
	VideoPollTimedOutTotal prometheus.Counter

	// VideoGenerationFailedTotal counts video-generation requests where the provider
	// itself reported a terminal status=failed (create time or mid-poll) — a clean,
	// expected-shape failure distinct from VideoBillingSkippedTotal (which fires when a
	// 200/"completed" response can't be parsed for a duration). Kept as its own counter
	// rather than folded into VideoBillingSkippedTotal so a spike in provider-side
	// generation failures is independently alertable from a broker-side parsing gap.
	VideoGenerationFailedTotal prometheus.Counter

	// RequestRejectedTotal counts requests rejected before they reach the
	// upstream, labeled by a low-cardinality `reason` (see the Rejection*
	// constants). This is the primary signal for the "high RPS, near-zero
	// revenue" failure mode: a flood of admission/billing-gate rejections that
	// otherwise produce no log line shows up here within scrape interval, e.g.
	// broker_requests_rejected_total{reason="insufficient_balance"} climbing
	// while broker_requests_total stays flat. The reason label is sourced only
	// from the bounded constant set below — never from raw user/error strings —
	// so cardinality stays fixed. A per-user breakdown is deliberately NOT a
	// label here (that would be unbounded); operators get top-N offenders from
	// the periodic aggregated rejection log instead.
	RequestRejectedTotal *prometheus.CounterVec

	// FailureCount is the unified failure-attribution counter. Every request
	// that ends in failure (HTTP status >= 400 — whether the broker rejected it
	// before forwarding or the upstream provider returned a non-2xx we proxied
	// back) increments it exactly once, labeled so a single query answers
	// "whose fault, which model, what code". Coverage is every failure that
	// flows through a synchronous request handler under TrackMetrics; the known
	// gaps are: (1) a handler that panics without writing a status (the engine
	// runs without gin.Recovery(), so a panic-induced 500 surfaces as a
	// process-level crash/log, not here); (2) cross-origin requests rejected by
	// cors with 403 (cors is registered before TrackMetrics so preflight isn't
	// counted as traffic); and (3) failures inside the async background worker
	// (see ctrl/async.go), which runs off the request lifecycle on
	// context.Background() — its upstream non-2xx is recorded to the job's DB
	// status, not to this counter.
	//
	//   - source: "broker" (the broker's own fault — internal 5xx, TEE/signing,
	//     settlement, or an unclassified server-side validation failure), "upstream"
	//     (the provider's fault — it returned a 5xx/429 we proxied back, or the
	//     broker could not reach it: TLS timeout, connection refused, EOF), or
	//     "client" (the request itself was invalid — a client-caused 4xx the broker
	//     flagged with ignoreError, e.g. bad auth, unknown model, malformed body,
	//     insufficient balance, rate limit; or an upstream 4xx the provider rejected
	//     as malformed). The three-way cut lets the upstream/broker alerts fire only
	//     on real faults while client-caused noise lands in its own bucket.
	//   - code:   the bounded classification — a Rejection* reason for broker
	//     rejections; empty for broker/upstream errors that carry no classified
	//     reason (the status label then carries the granularity).
	//   - model:  the bounded metric model (see CtxKeyMetricModel); "" when the
	//     request failed before model resolution (e.g. an early rate-limit gate).
	//   - status: HTTP status text.
	//
	// It supersedes broker_requests_errors_total (which has no source/model and
	// drops client-caused 4xx via ignoreError) and complements
	// broker_requests_rejected_total (broker-only, no status/model); both remain
	// for dashboard back-compat. The provider_address const label is on every
	// series, so provider attribution needs no extra label here.
	FailureCount *prometheus.CounterVec
)

// Failure source label values for FailureCount. Bounded to three values so the
// "whose fault" cut never grows cardinality.
const (
	FailureSourceBroker   = "broker"
	FailureSourceUpstream = "upstream"
	// FailureSourceClient marks a failure caused by an invalid client request —
	// a client-caused 4xx the broker flagged with "ignoreError" (bad auth,
	// unknown model, malformed body, insufficient balance, rate limit, oversized
	// body, unsupported endpoint), or an upstream 4xx the provider rejected as
	// malformed. Neither the broker nor the upstream is at fault, so these must
	// not inflate either fault alert.
	FailureSourceClient = "client"
)

// FailureSourceHeader is the response header the broker stamps on every >=400
// response with the fault attribution (broker|upstream|client) — the same value
// recorded to FailureCount. It lets a programmatic caller (the 0G router)
// consume the broker's authoritative "whose fault" verdict instead of
// re-deriving it from the HTTP status. Additive and advisory: existing clients
// ignore it. The broker owns the header exclusively — any value a proxied
// upstream sets is overwritten on a >=400 response and removed on a <400 one,
// so it cannot be forged end-to-end. HTTP header names are case-insensitive.
//
// The name follows the existing ZG-* convention (cf. ZG-Res-Key); no X- prefix
// (RFC 6648 deprecates it). The name and value set are a cross-service contract
// with the router; see docs/design/failure-attribution-contract.md before
// changing them.
const FailureSourceHeader = "ZG-Failure-Source"

// CtxKeyFailureSource lets a handler override the failure source TrackMetrics
// attributes to a >=400 response. When unset, resolveFailureSource derives it:
// a client-caused 4xx (one the handler flagged with "ignoreError", excluding
// the upstream_error fallback) is the client's fault, everything else is the
// broker's. Handlers set it explicitly only when they know better than the
// status — the upstream proxy path sets FailureSourceUpstream for a provider
// non-2xx / unreachable provider, and FailureSourceClient for a proxied
// upstream 4xx.
const CtxKeyFailureSource = "failureSource"

// Rejection reason label values for RequestRejectedTotal. These are the only
// strings ever passed to RecordRejection, keeping the metric's cardinality
// bounded. Group: admission gates (rate/tpm/ipm/concurrency/model_mismatch),
// billing gates (insufficient_balance/not_acknowledged/account_not_exist), and
// the upstream_error catch-all for validation failures whose specific cause
// isn't classified. Every constant here has a live emit site — a reason is not
// declared until it is actually recorded, so the metric's label set never
// advertises a value that can't appear.
const (
	RejectionRateLimit       = "rate_limit"
	RejectionTPMLimit        = "tpm_limit"
	RejectionIPMLimit        = "ipm_limit"
	RejectionConcurrency     = "concurrency"
	RejectionModelMismatch   = "model_mismatch"
	RejectionInsufficientBal = "insufficient_balance"
	RejectionNotAcknowledged = "not_acknowledged"
	RejectionAccountNotExist = "account_not_exist"
	RejectionUpstreamError   = "upstream_error"
	RejectionModelExpired    = "model_expired"
	// RejectionInvalidRequest: the body carries a field the billing gate cannot
	// price, so the request is refused before it reaches the upstream. Emitted by
	// the video-generation pre-flight reserve for an unpriceable `seconds`. Its own
	// reason rather than the upstream_error catch-all because it is attacker-
	// reachable — an out-of-range duration used to reserve the 1-unit floor while
	// the translator clamped it down and billed in full, so a spike here is
	// something an operator wants to see, not a request that dies unclassified.
	RejectionInvalidRequest = "invalid_request"
	// RejectionPricingUnavailable: the gate could not price the request through no fault of the
	// caller — a stale USD rate snapshot, or a model whose published metadata does not say what
	// an omitted field costs. Broker-attributed in the failure metric; given its own rejection
	// reason because an operator config gap otherwise refuses every conforming create with no
	// classified signal, only a status label.
	RejectionPricingUnavailable = "pricing_unavailable"
)

// CtxKeyRejectionReason is the gin context key under which a request handler
// records WHY a request was rejected, using one of the Rejection* constants.
// The billing/validation gate lives in the ctrl package but its caller (the
// proxy) owns rejection recording, so the reason is passed across that boundary
// via the context rather than by threading a return value through several
// signatures. An unset key means "not a classified rejection".
const CtxKeyRejectionReason = "rejectionReason"

// PrometheusInit registers all broker metrics. serverName (the ServingURL)
// and providerAddress (the provider's on-chain address, as registered in the
// serving contract) are stamped on every series as const labels, so a series
// is identified by (provider_address, server, ...) — the same
// (address, endpoint) identity the router's providers catalog is keyed by,
// immune to URL reuse.
//
// The label is provider_address, NOT provider: deployments already attach a
// `provider` external label carrying the human-readable deployment nickname
// (deploy/phala/*/your-prometheus.yml), and a series-level label with the
// same name would override it and break provider-grouped ops dashboards.
func PrometheusInit(serverName, providerAddress string) {
	if serverName == "" {
		panic("server name must be provided")
	}
	// The address label is the PR's whole point (URL-reuse-proof identity);
	// an empty or malformed value would silently void it on every series.
	// The format check also catches swapped arguments (a ServingURL never
	// matches). Wallet-derived addresses always satisfy this.
	if !providerAddressRe.MatchString(providerAddress) {
		panic("provider address must be a 0x-prefixed 40-hex-char address, got: " + providerAddress)
	}
	constLabels := prometheus.Labels{"server": serverName, "provider_address": providerAddress}

	RequestCount = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name:        "broker_requests_total",
			Help:        "Total number of HTTP requests processed, labeled by path, status and model.",
			ConstLabels: constLabels,
		},
		[]string{"path", "status", "model"},
	)

	ErrorCount = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name:        "broker_requests_errors_total",
			Help:        "Total number of error requests processed by the broker server.",
			ConstLabels: constLabels,
		},
		[]string{"path", "status"},
	)

	RequestDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:        "broker_request_duration_seconds",
			Help:        "Histogram of request latencies.",
			Buckets:     prometheus.DefBuckets, // or customize the buckets according to your needs
			ConstLabels: constLabels,
		},
		[]string{"path"},
	)

	UniqueUsersTotal = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name:        "broker_unique_users_total",
			Help:        "Number of unique users in the last 24 hours (queried from database).",
			ConstLabels: constLabels,
		},
	)

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

	AudioSecondsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name:        "broker_audio_seconds_total",
			Help:        "Cumulative input audio duration in seconds for duration-billed services (e.g. whisper).",
			ConstLabels: constLabels,
		},
		[]string{"service_type", "model"},
	)

	TokensPerSecond = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:        "broker_tokens_per_second",
			Help:        "Per-request output token generation rate (output_tokens / request_duration_seconds).",
			Buckets:     []float64{1, 5, 10, 20, 30, 50, 75, 100, 150, 200, 500},
			ConstLabels: constLabels,
		},
		[]string{"service_type", "model"},
	)

	AllTimeRequests = prometheus.NewGauge(prometheus.GaugeOpts{
		Name:        "broker_alltime_requests_total",
		Help:        "All-time total number of requests (from database).",
		ConstLabels: constLabels,
	})

	AllTimeInputTokens = prometheus.NewGauge(prometheus.GaugeOpts{
		Name:        "broker_alltime_input_tokens_total",
		Help:        "All-time total input token count (from database).",
		ConstLabels: constLabels,
	})

	AllTimeOutputTokens = prometheus.NewGauge(prometheus.GaugeOpts{
		Name:        "broker_alltime_output_tokens_total",
		Help:        "All-time total output token count (from database).",
		ConstLabels: constLabels,
	})

	AllTimeUniqueUsers = prometheus.NewGauge(prometheus.GaugeOpts{
		Name:        "broker_alltime_unique_users_total",
		Help:        "All-time total unique users (from database).",
		ConstLabels: constLabels,
	})

	WhitelistRequestsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name:        "broker_whitelist_requests_total",
			Help:        "Total number of requests from whitelisted (internal) users, labeled by service_type and model.",
			ConstLabels: constLabels,
		},
		[]string{"service_type", "model"},
	)

	WhitelistInputTokensTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name:        "broker_whitelist_input_tokens_total",
			Help:        "Cumulative input token count from whitelisted (internal) users.",
			ConstLabels: constLabels,
		},
		[]string{"service_type", "model"},
	)

	WhitelistOutputTokensTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name:        "broker_whitelist_output_tokens_total",
			Help:        "Cumulative output token count from whitelisted (internal) users.",
			ConstLabels: constLabels,
		},
		[]string{"service_type", "model"},
	)

	WhitelistAudioSecondsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name:        "broker_whitelist_audio_seconds_total",
			Help:        "Cumulative input audio duration in seconds from whitelisted (internal) users.",
			ConstLabels: constLabels,
		},
		[]string{"service_type", "model"},
	)

	prometheus.MustRegister(RequestCount)
	prometheus.MustRegister(ErrorCount)
	prometheus.MustRegister(RequestDuration)
	prometheus.MustRegister(UniqueUsersTotal)
	prometheus.MustRegister(InputTokensTotal)
	prometheus.MustRegister(OutputTokensTotal)
	prometheus.MustRegister(AudioSecondsTotal)
	prometheus.MustRegister(TokensPerSecond)
	prometheus.MustRegister(AllTimeRequests)
	prometheus.MustRegister(AllTimeInputTokens)
	prometheus.MustRegister(AllTimeOutputTokens)
	prometheus.MustRegister(AllTimeUniqueUsers)
	VideoBillingSkippedTotal = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name:        "broker_video_billing_skipped_total",
			Help:        "Video-generation requests served without billing (no positive duration resolvable from response or request).",
			ConstLabels: constLabels,
		},
	)

	VideoPollTimedOutTotal = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name:        "broker_video_poll_timed_out_total",
			Help:        "Video-generation poll jobs that hit MaxPollDuration without the provider reaching a terminal state (potential reconciliation gap).",
			ConstLabels: constLabels,
		},
	)

	VideoTableMissTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name:        "broker_video_table_miss_total",
			Help:        "Video-generation requests whose (resolution, duration) had no exact per_unit_table row, labeled by reason (next_bucket = a longer bucket covered it; uncovered = nothing did, table maximum charged). Non-zero means clients are billed a price GET /v1/models does not advertise for their request — add the missing rows.",
			ConstLabels: constLabels,
		},
		[]string{"reason"},
	)

	VideoReserveShortfallTotal = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name:        "broker_video_reserve_shortfall_total",
			Help:        "Video-generation settlements that billed MORE units than the pre-flight balance reserve had gated — the gate admitted a request it could not cover. The reserve reads the request, settlement reads the response; a moving rate means that model has drifted. Some non-zero value is expected (a vendor rendering a dearer tier than asked for is a documented residual); the RATE is the signal.",
			ConstLabels: constLabels,
		},
	)

	VideoGenerationFailedTotal = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name:        "broker_video_generation_failed_total",
			Help:        "Video-generation requests where the provider reported a terminal status=failed (create time or mid-poll).",
			ConstLabels: constLabels,
		},
	)

	RoutingProofSkippedTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name:        "broker_routing_proof_skipped_total",
			Help:        "Responses from a centralized provider served WITHOUT a TEE routing proof, labeled by reason (no_tls, no_sidecar_report, no_sidecar_host, domain_mismatch, sign_error, no_poll_job). Non-zero means the service is advertising verifiability it is not delivering — alert on any sustained rate.",
			ConstLabels: constLabels,
		},
		[]string{"reason"},
	)

	RequestRejectedTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name:        "broker_requests_rejected_total",
			Help:        "Total number of requests rejected before reaching the upstream, labeled by reason (rate_limit, tpm_limit, ipm_limit, concurrency, model_mismatch, insufficient_balance, not_acknowledged, account_not_exist, upstream_error, model_expired, invalid_request, pricing_unavailable).",
			ConstLabels: constLabels,
		},
		[]string{"reason"},
	)

	FailureCount = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name:        "broker_request_failures_total",
			Help:        "Total failed requests (HTTP status >= 400), labeled by source (broker|upstream|client), code (Rejection* reason or empty), model, and status. Unified failure-attribution counter; see FailureCount doc.",
			ConstLabels: constLabels,
		},
		[]string{"source", "code", "model", "status"},
	)

	prometheus.MustRegister(WhitelistRequestsTotal)
	prometheus.MustRegister(WhitelistInputTokensTotal)
	prometheus.MustRegister(WhitelistOutputTokensTotal)
	prometheus.MustRegister(WhitelistAudioSecondsTotal)
	prometheus.MustRegister(VideoBillingSkippedTotal)
	prometheus.MustRegister(VideoPollTimedOutTotal)
	prometheus.MustRegister(VideoGenerationFailedTotal)
	prometheus.MustRegister(VideoTableMissTotal)
	prometheus.MustRegister(VideoReserveShortfallTotal)
	prometheus.MustRegister(RoutingProofSkippedTotal)
	prometheus.MustRegister(RequestRejectedTotal)
	prometheus.MustRegister(FailureCount)
}

// StartDAUUpdater starts a background goroutine that periodically queries the database
// to count unique users in the last 24 hours and updates the Prometheus gauge.
// This ensures the DAU metric survives process restarts.
// The goroutine exits when ctx is cancelled.
func StartDAUUpdater(ctx context.Context, queryFunc func() (int64, error), interval time.Duration, logger log.Logger) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		// Run immediately on startup
		if count, err := queryFunc(); err != nil {
			logger.Errorf("DAU updater: failed to query unique users: %v", err)
		} else {
			UniqueUsersTotal.Set(float64(count))
		}

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if count, err := queryFunc(); err != nil {
					logger.Errorf("DAU updater: failed to query unique users: %v", err)
				} else {
					UniqueUsersTotal.Set(float64(count))
				}
			}
		}
	}()
}

// TotalStatsResult holds the combined stats for the all-time gauges.
type TotalStatsResult struct {
	TotalRequests     int64
	TotalInputTokens  int64
	TotalOutputTokens int64
	TotalUniqueUsers  int64
}

// StartAllTimeStatsUpdater starts a background goroutine that periodically queries
// the database for all-time cumulative stats and updates the Prometheus gauges.
// The goroutine exits when ctx is cancelled.
func StartAllTimeStatsUpdater(ctx context.Context, queryFunc func() (TotalStatsResult, error), interval time.Duration, logger log.Logger) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		update := func() {
			stats, err := queryFunc()
			if err != nil {
				logger.Errorf("All-time stats updater: failed to query: %v", err)
				return
			}
			AllTimeRequests.Set(float64(stats.TotalRequests))
			AllTimeInputTokens.Set(float64(stats.TotalInputTokens))
			AllTimeOutputTokens.Set(float64(stats.TotalOutputTokens))
			AllTimeUniqueUsers.Set(float64(stats.TotalUniqueUsers))
		}

		update()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				update()
			}
		}
	}()
}

// providerAddressRe validates the provider_address const label at init: a
// 0x-prefixed 20-byte hex address (checksummed or not).
var providerAddressRe = regexp.MustCompile(`^0x[0-9a-fA-F]{40}$`)

// RequestStartTimeKey is the gin context key for the request start time.
const RequestStartTimeKey = "requestStartTime"

// CtxKeyResolvedModel is the gin context key under which the request path
// stores the VALIDATED per-request model id (multi-model allowlist hit or
// single-model configured rewrite). It lives in this package so the setter
// (ctrl) and the billing reader share one definition without an import
// cycle; ctrl re-exports it. NOTE: "validated" is NOT "bounded" — on a
// wildcard (serve-all) deployment the allowlist admits arbitrary user
// strings, so this key must NEVER be used as a metric label value directly;
// metrics read CtxKeyMetricModel instead.
const CtxKeyResolvedModel = "resolvedModel"

// CtxKeyMetricModel carries the BOUNDED metric label value for the request:
// ctrl computes it (enumerated pricing id / configured model / "*" wildcard
// sentinel — see ctrl.metricModel) and stores it in PrepareHTTPRequest, so
// the monitor package can label without access to the pricing config and
// raw user strings can never mint series.
const CtxKeyMetricModel = "metricModel"

// modelFromGinContext returns the bounded metric model recorded under
// CtxKeyMetricModel, or "" when the request path didn't set one (paths that
// never reach PrepareHTTPRequest). An empty label value is dropped by
// Prometheus, letting a deployment-level external_labels `model` (the legacy
// single-model convention) backfill it.
func modelFromGinContext(c *gin.Context) string {
	if v, exists := c.Get(CtxKeyMetricModel); exists {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// resolveFailureSource decides the fault attribution for a >=400 response.
//
// An explicit override stamped under CtxKeyFailureSource always wins — the
// upstream proxy path uses it to mark a provider non-2xx / unreachable provider
// as "upstream" (and a proxied upstream 4xx as "client"), because there the
// status alone can't tell whose fault it is.
//
// With no override the failure was produced by the broker itself, and the
// attribution turns on clientCaused — the handler's "ignoreError" flag, the
// same marker ErrorCount uses to drop user-caused failures. The broker sets it
// whenever it rejects a request for a client-side reason (bad auth, unknown
// model, malformed body, insufficient balance, rate limit, oversized body,
// unsupported endpoint). A 4xx carrying that flag is the client's fault →
// "client".
//
// Everything else stays "broker" — crucially including an un-flagged 4xx:
// errors.Response defaults any unclassified internal error to 400, so a DB /
// TEE / billing / request-prep failure surfaces as a 400 with no ignoreError.
// Deriving from status alone would hide those genuine broker faults in the
// "client" bucket; gating on the flag keeps them attributed to the broker so
// the broker alert still fires. The status<500 guard keeps a client-flagged
// 5xx out of "client", and the upstream_error fallback (proxy/rejection.go) —
// the broker's own unclassified-validation reason — is pinned to "broker" too.
func resolveFailureSource(c *gin.Context, status int, code string, clientCaused bool) string {
	if v, exists := c.Get(CtxKeyFailureSource); exists {
		if s, ok := v.(string); ok && s != "" {
			return s
		}
	}
	if clientCaused && status >= 400 && status < 500 && code != RejectionUpstreamError {
		return FailureSourceClient
	}
	return FailureSourceBroker
}

// failureCodeFromGinContext returns the bounded classification stamped under
// CtxKeyRejectionReason (one of the Rejection* constants), or "" when the
// failure carried no classified reason — the status label then distinguishes it.
func failureCodeFromGinContext(c *gin.Context) string {
	if v, exists := c.Get(CtxKeyRejectionReason); exists {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// failureSourceWriter wraps the gin ResponseWriter to stamp FailureSourceHeader
// on the first write of a response, deriving the value exactly as the failure
// metric does. Wrapping (rather than setting the header in each handler) means
// every error response — however and wherever it is written — carries the
// attribution once, set into the header map before it flushes. TrackMetrics'
// own post-c.Next() code cannot do this: by then the handler has already
// flushed the headers.
type failureSourceWriter struct {
	gin.ResponseWriter
	ctx     *gin.Context
	stamped bool
}

// stamp sets (>=400) or clears (<400) FailureSourceHeader exactly once, before
// the headers flush. Clearing on success denies a proxied upstream the chance
// to forge the attribution on a 2xx we pass through.
func (w *failureSourceWriter) stamp() {
	if w.stamped {
		return
	}
	w.stamped = true
	status := w.ResponseWriter.Status()
	if status >= 400 {
		source := resolveFailureSource(w.ctx, status, failureCodeFromGinContext(w.ctx), w.ctx.GetBool("ignoreError"))
		w.ResponseWriter.Header().Set(FailureSourceHeader, source)
		return
	}
	w.ResponseWriter.Header().Del(FailureSourceHeader)
}

func (w *failureSourceWriter) WriteHeader(code int) {
	w.ResponseWriter.WriteHeader(code)
	w.stamp()
}

func (w *failureSourceWriter) WriteHeaderNow() {
	w.stamp()
	w.ResponseWriter.WriteHeaderNow()
}

func (w *failureSourceWriter) Write(b []byte) (int, error) {
	w.stamp()
	return w.ResponseWriter.Write(b)
}

func (w *failureSourceWriter) WriteString(s string) (int, error) {
	w.stamp()
	return w.ResponseWriter.WriteString(s)
}

// TrackMetrics is a Gin middleware that tracks request metrics.
func TrackMetrics() gin.HandlerFunc {
	return func(c *gin.Context) {
		startTime := time.Now()
		c.Set(RequestStartTimeKey, startTime)

		// Wrap the writer so every >=400 response carries the ZG-Failure-Source
		// header. It must be set before the handler flushes its headers, which the
		// post-c.Next() code below is too late to do; the wrapper stamps it at the
		// first write instead.
		c.Writer = &failureSourceWriter{ResponseWriter: c.Writer, ctx: c}

		path := c.Request.URL.Path
		c.Next() // Process the request

		ignoreError := c.GetBool("ignoreError")

		// Track request duration
		duration := time.Since(startTime).Seconds()
		RequestDuration.WithLabelValues(path).Observe(duration)

		// Track request count and errors. The model label is read AFTER c.Next(),
		// so it carries whatever the request path resolved ("" for non-inference
		// paths and unresolved requests).
		status := c.Writer.Status()
		statusText := http.StatusText(status)
		model := modelFromGinContext(c)
		RequestCount.WithLabelValues(path, statusText, model).Inc()
		if !ignoreError && status >= 400 {
			ErrorCount.WithLabelValues(path, statusText).Inc()
		}
		// Unified failure attribution: count EVERY >=400 (including client-caused
		// rejections that ErrorCount drops via ignoreError) exactly once, split by
		// source/code/model so "broker vs upstream vs client, which model, what
		// reason" is a single query. resolveFailureSource uses an explicit handler
		// override when present, else derives the source from the ignoreError flag
		// and status.
		if status >= 400 {
			code := failureCodeFromGinContext(c)
			RecordFailure(resolveFailureSource(c, status, code, ignoreError), code, model, statusText)
		}
	}
}

// RecordTokens increments the cumulative input and output token counters.
// model is the per-request BOUNDED model label ("" lets a deployment-level
// external label backfill — see CtxKeyMetricModel).
func RecordTokens(serviceType, model string, inputTokens, outputTokens int64) {
	if InputTokensTotal == nil || OutputTokensTotal == nil {
		return
	}
	if inputTokens > 0 {
		InputTokensTotal.WithLabelValues(serviceType, model).Add(float64(inputTokens))
	}
	if outputTokens > 0 {
		OutputTokensTotal.WithLabelValues(serviceType, model).Add(float64(outputTokens))
	}
}

// RecordAudioSeconds increments the cumulative audio-seconds counter for
// duration-billed services. This is intentionally separate from RecordTokens —
// accumulating seconds into broker_input_tokens_total would skew the metric
// by orders of magnitude versus token-billed services on the same dashboard.
func RecordAudioSeconds(serviceType, model string, seconds int64) {
	if AudioSecondsTotal == nil || seconds <= 0 {
		return
	}
	AudioSecondsTotal.WithLabelValues(serviceType, model).Add(float64(seconds))
}

// RecordWhitelistAudioSeconds mirrors RecordAudioSeconds for whitelisted users.
func RecordWhitelistAudioSeconds(serviceType, model string, seconds int64) {
	if WhitelistAudioSecondsTotal == nil || seconds <= 0 {
		return
	}
	WhitelistAudioSecondsTotal.WithLabelValues(serviceType, model).Add(float64(seconds))
}

// RecordVideoBillingSkipped increments the counter of video requests served
// without billing because no duration could be resolved (alertable signal that
// an upstream response shape is unparsed).
func RecordVideoBillingSkipped() {
	if VideoBillingSkippedTotal == nil {
		return
	}
	VideoBillingSkippedTotal.Inc()
}

// Reasons for RecordRoutingProofSkipped.
const (
	// RoutingProofSkipNoTLS: a direct centralized response arrived with no TLS
	// connection state to bind.
	RoutingProofSkipNoTLS = "no_tls"
	// RoutingProofSkipNoSidecarReport: targetTLSProxy is set but the in-enclave
	// shim reported no usable certificate fingerprint.
	RoutingProofSkipNoSidecarReport = "no_sidecar_report"
	// RoutingProofSkipSignError: evidence was present but signing itself failed.
	RoutingProofSkipSignError = "sign_error"
	// RoutingProofSkipDomainMismatch: the in-enclave shim reported dialing a host
	// other than service.upstreamDomain. The proof would be signed over one host's
	// certificate while serving_domain points verifiers at another, so every
	// verification would fail — with no other signal, since the fingerprint itself
	// was well-formed. Almost always broker config and shim env having drifted
	// apart (e.g. MINIMAX_BASE_URL changed for a domestic-site account).
	RoutingProofSkipDomainMismatch = "domain_mismatch"
	// RoutingProofSkipNoSidecarHost: the shim reported a certificate but no SNI, so
	// the broker cannot confirm it dialed the host it publishes as serving_domain.
	// Separate from domain_mismatch because the fix is different: this one is a shim
	// image predating the header, or a *_BASE_URL that is an IP literal or plaintext
	// URL — not two config files disagreeing.
	RoutingProofSkipNoSidecarHost = "no_sidecar_host"
	// RoutingProofSkipNoPollJob: an async video job never reached the poll
	// scheduler (no provider job id, scheduler disabled, or the job row could not
	// be written), so nothing will sign the final body the client was promised a
	// proof over. Distinct from sign_error because the fix is the scheduler
	// config or the shim's create response, not the TEE signer.
	RoutingProofSkipNoPollJob = "no_poll_job"
)

// RecordRoutingProofSkipped increments the counter of centralized-provider
// responses served without a TEE routing proof. Every call site is a place where
// the service continues to advertise verifiability it did not deliver for that
// response, so this must be alertable rather than log-only.
func RecordRoutingProofSkipped(reason string) {
	if RoutingProofSkippedTotal == nil {
		return
	}
	RoutingProofSkippedTotal.WithLabelValues(reason).Inc()
}

// Reasons for RecordVideoTableMiss.
const (
	// VideoTableMissNextBucket: no exact row, but a longer bucket covered the
	// observation and was charged.
	VideoTableMissNextBucket = "next_bucket"
	// VideoTableMissUncovered: nothing at or above the observation exists for that
	// resolution, so the table maximum across every resolution was charged.
	VideoTableMissUncovered = "uncovered"
)

var (
	// VideoReserveShortfallTotal counts video settlements that billed MORE units than the pre-flight
	// reserve had gated, i.e. the gate admitted a request it could not cover.
	//
	// This is the one signal that catches the whole class rather than an instance of it. The reserve
	// reads what the REQUEST asked for; settlement reads what the RESPONSE delivered; every
	// under-reserve this path has ever had was those two disagreeing, and each fix for one instance
	// opened the mirror of it — three times over. A counter on the delta is what makes the next one
	// visible on a dashboard instead of in a review.
	//
	// Non-zero is not automatically a bug: an upstream that renders a dearer tier than the client
	// asked for is a known, documented residual. But a RATE that moves means the reserve's model of
	// the upstream has drifted, which is exactly when someone should look.
	VideoReserveShortfallTotal prometheus.Counter
)

// RecordVideoTableMiss increments the per_unit_table miss counter.
// RecordVideoReserveShortfall reports that settlement billed more units than the reserve gated.
func RecordVideoReserveShortfall() {
	if VideoReserveShortfallTotal != nil {
		VideoReserveShortfallTotal.Inc()
	}
}

func RecordVideoTableMiss(reason string) {
	if VideoTableMissTotal == nil {
		return
	}
	VideoTableMissTotal.WithLabelValues(reason).Inc()
}

// RecordVideoPollTimedOut increments the counter of video poll jobs that hit
// MaxPollDuration without the provider ever reaching a terminal state.
func RecordVideoPollTimedOut() {
	if VideoPollTimedOutTotal == nil {
		return
	}
	VideoPollTimedOutTotal.Inc()
}

// RecordVideoGenerationFailed increments the counter of video-generation requests where the
// provider reported a terminal status=failed, at create time or mid-poll.
func RecordVideoGenerationFailed() {
	if VideoGenerationFailedTotal == nil {
		return
	}
	VideoGenerationFailedTotal.Inc()
}

// RecordRejection increments the rejected-request counter for the given reason.
// reason must be one of the Rejection* constants so the metric stays bounded.
// Safe to call before PrometheusInit (no-op when monitoring is disabled).
func RecordRejection(reason string) {
	if RequestRejectedTotal == nil {
		return
	}
	RequestRejectedTotal.WithLabelValues(reason).Inc()
}

// RecordFailure increments the unified failure counter. source must be a
// FailureSource* constant and code a Rejection* constant or "" so the metric
// stays bounded. Safe to call before PrometheusInit (no-op when monitoring is
// disabled). Normally called only from TrackMetrics' single emit site.
func RecordFailure(source, code, model, status string) {
	if FailureCount == nil {
		return
	}
	FailureCount.WithLabelValues(source, code, model, status).Inc()
}

// RecordWhitelistRequest increments the whitelist request counter for the given
// service type and model.
func RecordWhitelistRequest(serviceType, model string) {
	if WhitelistRequestsTotal == nil {
		return
	}
	WhitelistRequestsTotal.WithLabelValues(serviceType, model).Inc()
}

// RecordWhitelistTokens increments the whitelist input and output token counters.
func RecordWhitelistTokens(serviceType, model string, inputTokens, outputTokens int64) {
	if WhitelistInputTokensTotal == nil || WhitelistOutputTokensTotal == nil {
		return
	}
	if inputTokens > 0 {
		WhitelistInputTokensTotal.WithLabelValues(serviceType, model).Add(float64(inputTokens))
	}
	if outputTokens > 0 {
		WhitelistOutputTokensTotal.WithLabelValues(serviceType, model).Add(float64(outputTokens))
	}
}

// RecordTPS records the per-request output tokens per second as a histogram observation.
func RecordTPS(serviceType, model string, tps float64) {
	if TokensPerSecond == nil || tps <= 0 {
		return
	}
	TokensPerSecond.WithLabelValues(serviceType, model).Observe(tps)
}

// RecordTPSFromContext calculates TPS from the request start time stored in context
// and records it. This is a convenience wrapper combining start-time extraction,
// duration calculation, and TPS recording.
func RecordTPSFromContext(ctx context.Context, serviceType, model string, outputTokens int64) {
	if outputTokens <= 0 {
		return
	}
	startTime, ok := ctx.Value(RequestStartTimeKey).(time.Time)
	if !ok {
		return
	}
	duration := time.Since(startTime).Seconds()
	if duration > 0 {
		RecordTPS(serviceType, model, float64(outputTokens)/duration)
	}
}
