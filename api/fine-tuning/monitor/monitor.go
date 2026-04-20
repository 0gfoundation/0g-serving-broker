package monitor

import (
	"context"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
)

var (
	TasksCreatedTotal   prometheus.Counter
	TasksCompletedTotal prometheus.Counter
	TasksFailedTotal    prometheus.Counter

	TasksByState *prometheus.GaugeVec

	TaskPhaseDuration *prometheus.HistogramVec

	StorageUploadTotal      prometheus.Counter
	StorageUploadErrors     prometheus.Counter
	StorageDownloadTotal    prometheus.Counter
	StorageDownloadErrors   prometheus.Counter
	StorageUploadDuration   prometheus.Histogram
	StorageDownloadDuration prometheus.Histogram

	SettlementTotal  prometheus.Counter
	SettlementErrors prometheus.Counter

	RequestCount    *prometheus.CounterVec
	ErrorCount      *prometheus.CounterVec
	RequestDuration *prometheus.HistogramVec

	UniqueUsersTotal prometheus.Gauge

	uniqueUsersChan chan string
)

// Init initializes all Prometheus metrics for the fine-tuning service.
// The context is used for graceful shutdown of background goroutines.
func Init(serverName string, ctx context.Context) {
	if serverName == "" {
		serverName = "fine-tuning"
	}

	labels := prometheus.Labels{"server": serverName}

	TasksCreatedTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Name:        "ft_tasks_created_total",
		Help:        "Total number of fine-tuning tasks created.",
		ConstLabels: labels,
	})

	TasksCompletedTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Name:        "ft_tasks_completed_total",
		Help:        "Total number of fine-tuning tasks completed successfully.",
		ConstLabels: labels,
	})

	TasksFailedTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Name:        "ft_tasks_failed_total",
		Help:        "Total number of fine-tuning tasks that failed.",
		ConstLabels: labels,
	})

	TasksByState = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name:        "ft_tasks_by_state",
		Help:        "Current number of fine-tuning tasks in each state.",
		ConstLabels: labels,
	}, []string{"state"})

	TaskPhaseDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:        "ft_task_phase_duration_seconds",
		Help:        "Duration of each fine-tuning task phase.",
		Buckets:     []float64{10, 30, 60, 120, 300, 600, 1800, 3600, 7200},
		ConstLabels: labels,
	}, []string{"phase"})

	StorageUploadTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Name:        "ft_storage_upload_total",
		Help:        "Total number of 0G Storage upload attempts.",
		ConstLabels: labels,
	})

	StorageUploadErrors = prometheus.NewCounter(prometheus.CounterOpts{
		Name:        "ft_storage_upload_errors_total",
		Help:        "Total number of 0G Storage upload errors.",
		ConstLabels: labels,
	})

	StorageDownloadTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Name:        "ft_storage_download_total",
		Help:        "Total number of 0G Storage download attempts.",
		ConstLabels: labels,
	})

	StorageDownloadErrors = prometheus.NewCounter(prometheus.CounterOpts{
		Name:        "ft_storage_download_errors_total",
		Help:        "Total number of 0G Storage download errors.",
		ConstLabels: labels,
	})

	StorageUploadDuration = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:        "ft_storage_upload_duration_seconds",
		Help:        "Duration of 0G Storage uploads.",
		Buckets:     []float64{5, 15, 30, 60, 120, 300, 600, 1800},
		ConstLabels: labels,
	})

	StorageDownloadDuration = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:        "ft_storage_download_duration_seconds",
		Help:        "Duration of 0G Storage downloads.",
		Buckets:     []float64{5, 15, 30, 60, 120, 300, 600, 1800},
		ConstLabels: labels,
	})

	SettlementTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Name:        "ft_settlement_total",
		Help:        "Total number of fine-tuning settlements processed.",
		ConstLabels: labels,
	})

	SettlementErrors = prometheus.NewCounter(prometheus.CounterOpts{
		Name:        "ft_settlement_errors_total",
		Help:        "Total number of fine-tuning settlement errors.",
		ConstLabels: labels,
	})

	RequestCount = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name:        "ft_requests_total",
		Help:        "Total number of HTTP requests processed.",
		ConstLabels: labels,
	}, []string{"path", "status"})

	ErrorCount = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name:        "ft_requests_errors_total",
		Help:        "Total number of HTTP request errors.",
		ConstLabels: labels,
	}, []string{"path", "status"})

	RequestDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:        "ft_request_duration_seconds",
		Help:        "Histogram of HTTP request latencies.",
		Buckets:     prometheus.DefBuckets,
		ConstLabels: labels,
	}, []string{"path"})

	UniqueUsersTotal = prometheus.NewGauge(prometheus.GaugeOpts{
		Name:        "ft_unique_users_total",
		Help:        "Number of unique fine-tuning users for the current day (resets at UTC midnight).",
		ConstLabels: labels,
	})

	prometheus.MustRegister(
		TasksCreatedTotal, TasksCompletedTotal, TasksFailedTotal,
		TasksByState, TaskPhaseDuration,
		StorageUploadTotal, StorageUploadErrors, StorageDownloadTotal, StorageDownloadErrors,
		StorageUploadDuration, StorageDownloadDuration,
		SettlementTotal, SettlementErrors,
		RequestCount, ErrorCount, RequestDuration,
		UniqueUsersTotal,
	)

	// 10 000 provides ~100 s of burst capacity at 100 tasks/s before drops.
	uniqueUsersChan = make(chan string, 10000)
	go processUniqueUsers(ctx)
}

func processUniqueUsers(ctx context.Context) {
	uniqueUsers := make(map[string]struct{})
	lastResetDay := time.Now().UTC().YearDay()

	for {
		select {
		case <-ctx.Done():
			return
		case userAddress := <-uniqueUsersChan:
			currentDay := time.Now().UTC().YearDay()
			if currentDay != lastResetDay {
				uniqueUsers = make(map[string]struct{})
				lastResetDay = currentDay
				UniqueUsersTotal.Set(0)
			}

			if _, exists := uniqueUsers[userAddress]; !exists {
				uniqueUsers[userAddress] = struct{}{}
				UniqueUsersTotal.Set(float64(len(uniqueUsers)))
			}
		}
	}
}

// TrackMetrics returns a Gin middleware that records HTTP request count, errors, and duration.
func TrackMetrics() gin.HandlerFunc {
	return func(c *gin.Context) {
		startTime := time.Now()

		path := c.Request.URL.Path
		c.Next()

		duration := time.Since(startTime).Seconds()
		RequestDuration.WithLabelValues(path).Observe(duration)

		status := c.Writer.Status()
		RequestCount.WithLabelValues(path, http.StatusText(status)).Inc()
		if status >= 400 {
			ErrorCount.WithLabelValues(path, http.StatusText(status)).Inc()
		}
	}
}

// RecordUniqueUser tracks a unique user address for the current day.
func RecordUniqueUser(userAddress string) {
	if userAddress == "" || uniqueUsersChan == nil {
		return
	}

	select {
	case uniqueUsersChan <- userAddress:
	default:
	}
}

// RecordTaskCreated increments the counter for total fine-tuning tasks created.
func RecordTaskCreated() {
	if TasksCreatedTotal != nil {
		TasksCreatedTotal.Inc()
	}
}

// RecordTaskCompleted increments the counter for successfully completed tasks.
func RecordTaskCompleted() {
	if TasksCompletedTotal != nil {
		TasksCompletedTotal.Inc()
	}
}

// RecordTaskFailed increments the counter for tasks that ended in failure.
func RecordTaskFailed() {
	if TasksFailedTotal != nil {
		TasksFailedTotal.Inc()
	}
}

// RecordPhaseDuration records how long a task spent in a given phase.
func RecordPhaseDuration(phase string, duration time.Duration) {
	if TaskPhaseDuration != nil {
		TaskPhaseDuration.WithLabelValues(phase).Observe(duration.Seconds())
	}
}

// UpdateTaskStateGauge sets the current task count for each state.
func UpdateTaskStateGauge(stateCounts map[string]float64) {
	if TasksByState == nil {
		return
	}
	for state, count := range stateCounts {
		TasksByState.WithLabelValues(state).Set(count)
	}
}

// RecordStorageUpload records an upload attempt, its error status, and duration.
func RecordStorageUpload(err error, duration time.Duration) {
	if StorageUploadTotal != nil {
		StorageUploadTotal.Inc()
	}
	if err != nil && StorageUploadErrors != nil {
		StorageUploadErrors.Inc()
	}
	if StorageUploadDuration != nil {
		StorageUploadDuration.Observe(duration.Seconds())
	}
}

// RecordStorageDownload records a download attempt, its error status, and duration.
func RecordStorageDownload(err error, duration time.Duration) {
	if StorageDownloadTotal != nil {
		StorageDownloadTotal.Inc()
	}
	if err != nil && StorageDownloadErrors != nil {
		StorageDownloadErrors.Inc()
	}
	if StorageDownloadDuration != nil {
		StorageDownloadDuration.Observe(duration.Seconds())
	}
}

// RecordSettlement records a settlement attempt and its error status.
func RecordSettlement(err error) {
	if SettlementTotal != nil {
		SettlementTotal.Inc()
	}
	if err != nil && SettlementErrors != nil {
		SettlementErrors.Inc()
	}
}

