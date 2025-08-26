package services

import (
	"fmt"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// PrometheusMetricsService provides comprehensive metrics collection for Prometheus
// These metrics can be scraped by SigNoz or any other Prometheus-compatible system
type PrometheusMetricsService struct {
	// Prometheus metrics registry
	promRegistry *prometheus.Registry

	// HTTP API metrics
	httpRequestsTotal    *prometheus.CounterVec
	httpRequestDuration  *prometheus.HistogramVec
	httpRequestsInFlight *prometheus.GaugeVec
	httpResponseSize     *prometheus.HistogramVec

	// Worker metrics
	workerTasksTotal    *prometheus.CounterVec
	workerTaskDuration  *prometheus.HistogramVec
	workerTasksInFlight *prometheus.GaugeVec
	workerRetryAttempts *prometheus.CounterVec

	// Queue metrics
	queueDepth          *prometheus.GaugeVec
	queueProcessingTime *prometheus.HistogramVec
	queueMessages       *prometheus.CounterVec

	// Cache metrics
	cacheOperations *prometheus.CounterVec
	cacheHitRatio   *prometheus.GaugeVec
	cacheDuration   *prometheus.HistogramVec

	// External API metrics
	externalAPICalls    *prometheus.CounterVec
	externalAPIDuration *prometheus.HistogramVec
	externalAPIErrors   *prometheus.CounterVec

	mu sync.RWMutex
}

// PrometheusConfig configures the Prometheus metrics service
type PrometheusConfig struct {
	Namespace string
	Subsystem string
}

// NewPrometheusMetricsService creates a new Prometheus metrics service
func NewPrometheusMetricsService(config PrometheusConfig) (*PrometheusMetricsService, error) {
	ms := &PrometheusMetricsService{
		promRegistry: prometheus.NewRegistry(),
	}

	if err := ms.initPrometheusMetrics(config.Namespace, config.Subsystem); err != nil {
		return nil, fmt.Errorf("failed to initialize Prometheus metrics: %w", err)
	}

	return ms, nil
}

// initPrometheusMetrics initializes all Prometheus metrics
func (ms *PrometheusMetricsService) initPrometheusMetrics(namespace, subsystem string) error {
	factory := promauto.With(ms.promRegistry)

	// HTTP API metrics
	ms.httpRequestsTotal = factory.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: namespace,
			Subsystem: subsystem,
			Name:      "http_requests_total",
			Help:      "Total number of HTTP requests processed",
		},
		[]string{"method", "endpoint", "status_code"},
	)

	ms.httpRequestDuration = factory.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: namespace,
			Subsystem: subsystem,
			Name:      "http_request_duration_seconds",
			Help:      "HTTP request duration in seconds",
			Buckets:   prometheus.DefBuckets,
		},
		[]string{"method", "endpoint", "status_code"},
	)

	ms.httpRequestsInFlight = factory.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: namespace,
			Subsystem: subsystem,
			Name:      "http_requests_in_flight",
			Help:      "Number of HTTP requests currently being processed",
		},
		[]string{"method", "endpoint"},
	)

	ms.httpResponseSize = factory.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: namespace,
			Subsystem: subsystem,
			Name:      "http_response_size_bytes",
			Help:      "HTTP response size in bytes",
			Buckets:   prometheus.ExponentialBuckets(100, 10, 8),
		},
		[]string{"method", "endpoint", "status_code"},
	)

	// Worker metrics
	ms.workerTasksTotal = factory.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: namespace,
			Subsystem: subsystem,
			Name:      "worker_tasks_total",
			Help:      "Total number of worker tasks processed",
		},
		[]string{"worker_type", "task_type", "status"},
	)

	ms.workerTaskDuration = factory.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: namespace,
			Subsystem: subsystem,
			Name:      "worker_task_duration_seconds",
			Help:      "Worker task duration in seconds",
			Buckets:   prometheus.DefBuckets,
		},
		[]string{"worker_type", "task_type", "status"},
	)

	ms.workerTasksInFlight = factory.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: namespace,
			Subsystem: subsystem,
			Name:      "worker_tasks_in_flight",
			Help:      "Number of worker tasks currently being processed",
		},
		[]string{"worker_type", "task_type"},
	)

	ms.workerRetryAttempts = factory.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: namespace,
			Subsystem: subsystem,
			Name:      "worker_retry_attempts_total",
			Help:      "Total number of worker task retry attempts",
		},
		[]string{"worker_type", "task_type", "retry_reason"},
	)

	// Queue metrics
	ms.queueDepth = factory.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: namespace,
			Subsystem: subsystem,
			Name:      "queue_depth",
			Help:      "Current depth of message queues",
		},
		[]string{"queue_name", "queue_type"},
	)

	ms.queueProcessingTime = factory.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: namespace,
			Subsystem: subsystem,
			Name:      "queue_processing_time_seconds",
			Help:      "Message queue processing time in seconds",
			Buckets:   prometheus.DefBuckets,
		},
		[]string{"queue_name", "queue_type", "status"},
	)

	ms.queueMessages = factory.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: namespace,
			Subsystem: subsystem,
			Name:      "queue_messages_total",
			Help:      "Total number of queue messages processed",
		},
		[]string{"queue_name", "queue_type", "status"},
	)

	// Cache metrics
	ms.cacheOperations = factory.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: namespace,
			Subsystem: subsystem,
			Name:      "cache_operations_total",
			Help:      "Total number of cache operations",
		},
		[]string{"operation", "status"},
	)

	ms.cacheHitRatio = factory.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: namespace,
			Subsystem: subsystem,
			Name:      "cache_hit_ratio",
			Help:      "Cache hit ratio (0.0 to 1.0)",
		},
		[]string{"cache_type"},
	)

	ms.cacheDuration = factory.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: namespace,
			Subsystem: subsystem,
			Name:      "cache_operation_duration_seconds",
			Help:      "Cache operation duration in seconds",
			Buckets:   prometheus.DefBuckets,
		},
		[]string{"operation", "status"},
	)

	// External API metrics
	ms.externalAPICalls = factory.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: namespace,
			Subsystem: subsystem,
			Name:      "external_api_calls_total",
			Help:      "Total number of external API calls",
		},
		[]string{"service", "endpoint", "method", "status_code"},
	)

	ms.externalAPIDuration = factory.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: namespace,
			Subsystem: subsystem,
			Name:      "external_api_duration_seconds",
			Help:      "External API call duration in seconds",
			Buckets:   prometheus.DefBuckets,
		},
		[]string{"service", "endpoint", "method", "status_code"},
	)

	ms.externalAPIErrors = factory.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: namespace,
			Subsystem: subsystem,
			Name:      "external_api_errors_total",
			Help:      "Total number of external API errors",
		},
		[]string{"service", "endpoint", "method", "error_type"},
	)

	return nil
}

// GetPrometheusRegistry returns the Prometheus registry for /metrics endpoint
func (ms *PrometheusMetricsService) GetPrometheusRegistry() *prometheus.Registry {
	return ms.promRegistry
}

// HTTP Metrics Methods

// RecordHTTPRequest records HTTP request metrics
func (ms *PrometheusMetricsService) RecordHTTPRequest(method, endpoint, statusCode string, duration time.Duration, responseSize int64) {
	ms.mu.RLock()
	defer ms.mu.RUnlock()

	durationSeconds := duration.Seconds()

	if ms.httpRequestsTotal != nil {
		ms.httpRequestsTotal.WithLabelValues(method, endpoint, statusCode).Inc()
	}
	if ms.httpRequestDuration != nil {
		ms.httpRequestDuration.WithLabelValues(method, endpoint, statusCode).Observe(durationSeconds)
	}
	if ms.httpResponseSize != nil {
		ms.httpResponseSize.WithLabelValues(method, endpoint, statusCode).Observe(float64(responseSize))
	}
}

// IncrementHTTPInFlight increments in-flight HTTP requests
func (ms *PrometheusMetricsService) IncrementHTTPInFlight(method, endpoint string) {
	ms.mu.RLock()
	defer ms.mu.RUnlock()

	if ms.httpRequestsInFlight != nil {
		ms.httpRequestsInFlight.WithLabelValues(method, endpoint).Inc()
	}
}

// DecrementHTTPInFlight decrements in-flight HTTP requests
func (ms *PrometheusMetricsService) DecrementHTTPInFlight(method, endpoint string) {
	ms.mu.RLock()
	defer ms.mu.RUnlock()

	if ms.httpRequestsInFlight != nil {
		ms.httpRequestsInFlight.WithLabelValues(method, endpoint).Dec()
	}
}

// Worker Metrics Methods

// RecordWorkerTask records worker task metrics
func (ms *PrometheusMetricsService) RecordWorkerTask(workerType, taskType, status string, duration time.Duration) {
	ms.mu.RLock()
	defer ms.mu.RUnlock()

	durationSeconds := duration.Seconds()

	if ms.workerTasksTotal != nil {
		ms.workerTasksTotal.WithLabelValues(workerType, taskType, status).Inc()
	}
	if ms.workerTaskDuration != nil {
		ms.workerTaskDuration.WithLabelValues(workerType, taskType, status).Observe(durationSeconds)
	}
}

// IncrementWorkerInFlight increments in-flight worker tasks
func (ms *PrometheusMetricsService) IncrementWorkerInFlight(workerType, taskType string) {
	ms.mu.RLock()
	defer ms.mu.RUnlock()

	if ms.workerTasksInFlight != nil {
		ms.workerTasksInFlight.WithLabelValues(workerType, taskType).Inc()
	}
}

// DecrementWorkerInFlight decrements in-flight worker tasks
func (ms *PrometheusMetricsService) DecrementWorkerInFlight(workerType, taskType string) {
	ms.mu.RLock()
	defer ms.mu.RUnlock()

	if ms.workerTasksInFlight != nil {
		ms.workerTasksInFlight.WithLabelValues(workerType, taskType).Dec()
	}
}

// RecordWorkerRetry records worker retry attempts
func (ms *PrometheusMetricsService) RecordWorkerRetry(workerType, taskType, retryReason string) {
	ms.mu.RLock()
	defer ms.mu.RUnlock()

	if ms.workerRetryAttempts != nil {
		ms.workerRetryAttempts.WithLabelValues(workerType, taskType, retryReason).Inc()
	}
}

// Queue Metrics Methods

// SetQueueDepth sets the current queue depth
func (ms *PrometheusMetricsService) SetQueueDepth(queueName, queueType string, depth int64) {
	ms.mu.RLock()
	defer ms.mu.RUnlock()

	if ms.queueDepth != nil {
		ms.queueDepth.WithLabelValues(queueName, queueType).Set(float64(depth))
	}
}

// RecordQueueProcessing records queue message processing metrics
func (ms *PrometheusMetricsService) RecordQueueProcessing(queueName, queueType, status string, duration time.Duration) {
	ms.mu.RLock()
	defer ms.mu.RUnlock()

	durationSeconds := duration.Seconds()

	if ms.queueMessages != nil {
		ms.queueMessages.WithLabelValues(queueName, queueType, status).Inc()
	}
	if ms.queueProcessingTime != nil {
		ms.queueProcessingTime.WithLabelValues(queueName, queueType, status).Observe(durationSeconds)
	}
}

// Cache Metrics Methods

// RecordCacheOperation records cache operation metrics
func (ms *PrometheusMetricsService) RecordCacheOperation(operation, status string, duration time.Duration) {
	ms.mu.RLock()
	defer ms.mu.RUnlock()

	durationSeconds := duration.Seconds()

	if ms.cacheOperations != nil {
		ms.cacheOperations.WithLabelValues(operation, status).Inc()
	}
	if ms.cacheDuration != nil {
		ms.cacheDuration.WithLabelValues(operation, status).Observe(durationSeconds)
	}
}

// SetCacheHitRatio sets the cache hit ratio
func (ms *PrometheusMetricsService) SetCacheHitRatio(cacheType string, ratio float64) {
	ms.mu.RLock()
	defer ms.mu.RUnlock()

	if ms.cacheHitRatio != nil {
		ms.cacheHitRatio.WithLabelValues(cacheType).Set(ratio)
	}
}

// External API Metrics Methods

// RecordExternalAPICall records external API call metrics
func (ms *PrometheusMetricsService) RecordExternalAPICall(service, endpoint, method, statusCode string, duration time.Duration, err error) {
	ms.mu.RLock()
	defer ms.mu.RUnlock()

	durationSeconds := duration.Seconds()

	if ms.externalAPICalls != nil {
		ms.externalAPICalls.WithLabelValues(service, endpoint, method, statusCode).Inc()
	}
	if ms.externalAPIDuration != nil {
		ms.externalAPIDuration.WithLabelValues(service, endpoint, method, statusCode).Observe(durationSeconds)
	}
	if ms.externalAPIErrors != nil && err != nil {
		errorType := "error"
		ms.externalAPIErrors.WithLabelValues(service, endpoint, method, errorType).Inc()
	}
}
