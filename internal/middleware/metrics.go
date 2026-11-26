package middleware

import (
	"context"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	amqp "github.com/rabbitmq/amqp091-go"

	"github.com/prefeitura-rio/app-eai-agent-gateway/internal/services"
)

// unmatchedEndpoint is the endpoint label of a request that matched no route.
// The raw path is never used as a label: any client can send arbitrary paths,
// which would create an unbounded number of series.
const unmatchedEndpoint = "unmatched"

// MetricsMiddleware provides HTTP request metrics collection
func MetricsMiddleware(metricsService *services.PrometheusMetricsService) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()

		// Extract endpoint route template and method
		method := c.Request.Method
		endpoint := c.FullPath()
		if endpoint == "" {
			endpoint = unmatchedEndpoint
		}

		// Increment in-flight requests; the decrement is deferred so that it
		// also runs when a later handler panics.
		metricsService.IncrementHTTPInFlight(method, endpoint)
		defer metricsService.DecrementHTTPInFlight(method, endpoint)

		// Process request
		c.Next()

		// Calculate duration and response size (Size is -1 when nothing was written)
		duration := time.Since(start)
		statusCode := strconv.Itoa(c.Writer.Status())
		responseSize := int64(c.Writer.Size())
		if responseSize < 0 {
			responseSize = 0
		}

		// Record metrics
		metricsService.RecordHTTPRequest(method, endpoint, statusCode, duration, responseSize)
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

	// Increment in-flight tasks; the decrement is deferred so that it also
	// runs when the task panics.
	w.metricsService.IncrementWorkerInFlight(workerType, taskType)
	defer w.metricsService.DecrementWorkerInFlight(workerType, taskType)

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

// WrapMessageHandler instruments a RabbitMQ message handler: each delivery it
// handles is counted in queue_messages_total and timed in
// queue_processing_time_seconds. The status follows what the handler returned
// to the consumer: "success" when the delivery is acknowledged, "failure" when
// it goes back to RabbitMQ for a retry or to the dead letter queue.
func (q *QueueMetricsWrapper) WrapMessageHandler(queueName, queueType string, handler services.MessageHandler) services.MessageHandler {
	return func(ctx context.Context, delivery amqp.Delivery) error {
		return q.WrapQueueProcessing(queueName, queueType, func() error {
			return handler(ctx, delivery)
		})
	}
}

// QueueDepthSource reports how many messages are ready in a queue.
type QueueDepthSource interface {
	QueueDepth(queueName string) (int, error)
}

// QueueDepthTarget is a queue whose depth is collected, with the queue_type
// label of its queue_depth series.
type QueueDepthTarget struct {
	Name string
	Type string
}

// CollectQueueDepth sets queue_depth for each target right away and then on
// every interval, until ctx is done. A queue whose depth cannot be read keeps
// its last value, and the error is passed to onError when it is not nil.
func (q *QueueMetricsWrapper) CollectQueueDepth(ctx context.Context, source QueueDepthSource, interval time.Duration, targets []QueueDepthTarget, onError func(target QueueDepthTarget, err error)) {
	collect := func() {
		for _, target := range targets {
			depth, err := source.QueueDepth(target.Name)
			if err != nil {
				if onError != nil {
					onError(target, err)
				}
				continue
			}
			q.UpdateQueueDepth(target.Name, target.Type, int64(depth))
		}
	}

	collect()

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			collect()
		}
	}
}
