package middleware

import (
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/prefeitura-rio/app-eai-agent-gateway/internal/services"
)

// MetricsMiddleware provides HTTP request metrics collection
func MetricsMiddleware(metricsService *services.PrometheusMetricsService) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()

		// Extract endpoint path and method
		method := c.Request.Method
		endpoint := c.FullPath()
		if endpoint == "" {
			endpoint = c.Request.URL.Path
		}

		// Increment in-flight requests
		metricsService.IncrementHTTPInFlight(method, endpoint)

		// Process request
		c.Next()

		// Calculate duration and response size
		duration := time.Since(start)
		statusCode := strconv.Itoa(c.Writer.Status())
		responseSize := int64(c.Writer.Size())

		// Record metrics
		metricsService.RecordHTTPRequest(method, endpoint, statusCode, duration, responseSize)

		// Decrement in-flight requests
		metricsService.DecrementHTTPInFlight(method, endpoint)
	}
}

// WorkerMetricsWrapper provides a wrapper for worker task metrics
type WorkerMetricsWrapper struct {
	metricsService *services.PrometheusMetricsService
}

// NewWorkerMetricsWrapper creates a new worker metrics wrapper
func NewWorkerMetricsWrapper(metricsService *services.PrometheusMetricsService) *WorkerMetricsWrapper {
	return &WorkerMetricsWrapper{
		metricsService: metricsService,
	}
}

// WrapWorkerTask wraps a worker task execution with metrics collection
func (w *WorkerMetricsWrapper) WrapWorkerTask(workerType, taskType string, taskFunc func() error) error {
	start := time.Now()

	// Increment in-flight tasks
	w.metricsService.IncrementWorkerInFlight(workerType, taskType)

	// Execute task
	err := taskFunc()

	// Calculate duration and status
	duration := time.Since(start)
	status := "success"
	if err != nil {
		status = "failure"
	}

	// Record metrics
	w.metricsService.RecordWorkerTask(workerType, taskType, status, duration)

	// Decrement in-flight tasks
	w.metricsService.DecrementWorkerInFlight(workerType, taskType)

	return err
}

// RecordWorkerRetry records a worker retry attempt
func (w *WorkerMetricsWrapper) RecordWorkerRetry(workerType, taskType, retryReason string) {
	w.metricsService.RecordWorkerRetry(workerType, taskType, retryReason)
}

// QueueMetricsWrapper provides a wrapper for queue metrics
type QueueMetricsWrapper struct {
	metricsService *services.PrometheusMetricsService
}

// NewQueueMetricsWrapper creates a new queue metrics wrapper
func NewQueueMetricsWrapper(metricsService *services.PrometheusMetricsService) *QueueMetricsWrapper {
	return &QueueMetricsWrapper{
		metricsService: metricsService,
	}
}

// UpdateQueueDepth updates the queue depth metric
func (q *QueueMetricsWrapper) UpdateQueueDepth(queueName, queueType string, depth int64) {
	q.metricsService.SetQueueDepth(queueName, queueType, depth)
}

// WrapQueueProcessing wraps queue message processing with metrics
func (q *QueueMetricsWrapper) WrapQueueProcessing(queueName, queueType string, processFunc func() error) error {
	start := time.Now()

	err := processFunc()

	duration := time.Since(start)
	status := "success"
	if err != nil {
		status = "failure"
	}

	q.metricsService.RecordQueueProcessing(queueName, queueType, status, duration)

	return err
}

// CacheMetricsWrapper provides a wrapper for cache operation metrics
type CacheMetricsWrapper struct {
	metricsService *services.PrometheusMetricsService
}

// NewCacheMetricsWrapper creates a new cache metrics wrapper
func NewCacheMetricsWrapper(metricsService *services.PrometheusMetricsService) *CacheMetricsWrapper {
	return &CacheMetricsWrapper{
		metricsService: metricsService,
	}
}

// WrapCacheOperation wraps a cache operation with metrics
func (c *CacheMetricsWrapper) WrapCacheOperation(operation string, operationFunc func() error) error {
	start := time.Now()

	err := operationFunc()

	duration := time.Since(start)
	status := "success"
	if err != nil {
		status = "error"
	}

	c.metricsService.RecordCacheOperation(operation, status, duration)

	return err
}

// UpdateCacheHitRatio updates the cache hit ratio metric
func (c *CacheMetricsWrapper) UpdateCacheHitRatio(cacheType string, ratio float64) {
	c.metricsService.SetCacheHitRatio(cacheType, ratio)
}

// ExternalAPIMetricsWrapper provides a wrapper for external API call metrics
type ExternalAPIMetricsWrapper struct {
	metricsService *services.PrometheusMetricsService
}

// NewExternalAPIMetricsWrapper creates a new external API metrics wrapper
func NewExternalAPIMetricsWrapper(metricsService *services.PrometheusMetricsService) *ExternalAPIMetricsWrapper {
	return &ExternalAPIMetricsWrapper{
		metricsService: metricsService,
	}
}

// WrapAPICall wraps an external API call with metrics collection
func (e *ExternalAPIMetricsWrapper) WrapAPICall(service, endpoint, method string, apiFunc func() (int, error)) error {
	start := time.Now()

	statusCode, err := apiFunc()

	duration := time.Since(start)
	statusCodeStr := strconv.Itoa(statusCode)

	e.metricsService.RecordExternalAPICall(service, endpoint, method, statusCodeStr, duration, err)

	return err
}
