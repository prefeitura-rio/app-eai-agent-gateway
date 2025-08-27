package middleware

import (
	"context"

	"github.com/gin-gonic/gin"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"

	"github.com/prefeitura-rio/app-eai-agent-gateway/internal/services"
)

// OTelMiddleware provides OpenTelemetry tracing middleware for HTTP requests
func OTelMiddleware(otelService *services.OTelService) gin.HandlerFunc {
	return func(c *gin.Context) {
		// Extract tracing context from headers
		ctx := otel.GetTextMapPropagator().Extract(c.Request.Context(), propagation.HeaderCarrier(c.Request.Header))

		// Get endpoint and method
		method := c.Request.Method
		endpoint := c.FullPath()
		if endpoint == "" {
			endpoint = c.Request.URL.Path
		}

		// Start span
		spanName := method + " " + endpoint
		ctx, span := otelService.StartSpan(ctx, spanName,
			trace.WithAttributes(
				attribute.String("http.method", method),
				attribute.String("http.route", endpoint),
				attribute.String("http.scheme", c.Request.URL.Scheme),
				attribute.String("http.host", c.Request.Host),
				attribute.String("http.target", c.Request.URL.Path),
				attribute.String("http.user_agent", c.Request.UserAgent()),
				attribute.String("http.remote_addr", c.ClientIP()),
				attribute.String("span.kind", "server"),
			))
		defer span.End()

		// Add correlation IDs to span if available
		if correlationID := services.GetCorrelationID(ctx); correlationID != "" {
			span.SetAttributes(attribute.String("correlation.id", correlationID))
		}
		if requestID := services.GetRequestID(ctx); requestID != "" {
			span.SetAttributes(attribute.String("request.id", requestID))
		}
		if userID := services.GetUserID(ctx); userID != "" {
			span.SetAttributes(attribute.String("user.id", userID))
		}

		// Update request context
		c.Request = c.Request.WithContext(ctx)

		// Inject trace context into response headers for downstream services
		otel.GetTextMapPropagator().Inject(ctx, propagation.HeaderCarrier(c.Writer.Header()))

		// Process request
		c.Next()

		// Record response attributes
		statusCode := c.Writer.Status()
		span.SetAttributes(
			attribute.Int("http.status_code", statusCode),
			attribute.Int("http.response_size", c.Writer.Size()),
		)

		// Record errors if present
		if len(c.Errors) > 0 {
			span.SetAttributes(attribute.Bool("error", true))
			for _, err := range c.Errors {
				span.RecordError(err.Err)
			}
		}

		// Set span status based on HTTP status code
		if statusCode >= 400 {
			if statusCode >= 500 {
				span.SetAttributes(attribute.String("error.type", "server_error"))
			} else {
				span.SetAttributes(attribute.String("error.type", "client_error"))
			}
		}
	}
}

// OTelWorkerWrapper provides OpenTelemetry tracing for worker tasks
type OTelWorkerWrapper struct {
	otelService *services.OTelService
}

// NewOTelWorkerWrapper creates a new OpenTelemetry worker wrapper
func NewOTelWorkerWrapper(otelService *services.OTelService) *OTelWorkerWrapper {
	return &OTelWorkerWrapper{
		otelService: otelService,
	}
}

// WrapWorkerTask wraps a worker task with OpenTelemetry tracing
func (w *OTelWorkerWrapper) WrapWorkerTask(ctx context.Context, workerType, taskType string, taskFunc func(context.Context) error) error {
	return w.otelService.TraceWorkerTask(ctx, workerType, taskType, taskFunc)
}

// StartSpan creates a new child span with the given name and attributes
func (w *OTelWorkerWrapper) StartSpan(ctx context.Context, name string, attrs ...attribute.KeyValue) (context.Context, trace.Span) {
	return w.otelService.StartSpan(ctx, name, trace.WithAttributes(attrs...))
}

// OTelQueueWrapper provides OpenTelemetry tracing for queue operations
type OTelQueueWrapper struct {
	otelService *services.OTelService
}

// NewOTelQueueWrapper creates a new OpenTelemetry queue wrapper
func NewOTelQueueWrapper(otelService *services.OTelService) *OTelQueueWrapper {
	return &OTelQueueWrapper{
		otelService: otelService,
	}
}

// WrapQueueProcessing wraps queue message processing with OpenTelemetry tracing
func (q *OTelQueueWrapper) WrapQueueProcessing(ctx context.Context, queueName, queueType string, processFunc func(context.Context) error) error {
	return q.otelService.TraceQueueProcessing(ctx, queueName, queueType, processFunc)
}

// UpdateQueueDepth updates queue depth metrics
func (q *OTelQueueWrapper) UpdateQueueDepth(ctx context.Context, queueName, queueType string, depth int64) {
	q.otelService.UpdateQueueDepth(ctx, queueName, queueType, depth)
}

// OTelCacheWrapper provides OpenTelemetry tracing for cache operations
type OTelCacheWrapper struct {
	otelService *services.OTelService
}

// NewOTelCacheWrapper creates a new OpenTelemetry cache wrapper
func NewOTelCacheWrapper(otelService *services.OTelService) *OTelCacheWrapper {
	return &OTelCacheWrapper{
		otelService: otelService,
	}
}

// WrapCacheOperation wraps cache operations with OpenTelemetry tracing
func (c *OTelCacheWrapper) WrapCacheOperation(ctx context.Context, operation string, operationFunc func(context.Context) error) error {
	return c.otelService.TraceCacheOperation(ctx, operation, operationFunc)
}

// UpdateCacheHitRatio updates cache hit ratio metrics
func (c *OTelCacheWrapper) UpdateCacheHitRatio(ctx context.Context, cacheType string, ratio float64) {
	c.otelService.UpdateCacheHitRatio(ctx, cacheType, ratio)
}

// OTelExternalAPIWrapper provides OpenTelemetry tracing for external API calls
type OTelExternalAPIWrapper struct {
	otelService *services.OTelService
}

// NewOTelExternalAPIWrapper creates a new OpenTelemetry external API wrapper
func NewOTelExternalAPIWrapper(otelService *services.OTelService) *OTelExternalAPIWrapper {
	return &OTelExternalAPIWrapper{
		otelService: otelService,
	}
}

// WrapAPICall wraps external API calls with OpenTelemetry tracing
func (e *OTelExternalAPIWrapper) WrapAPICall(ctx context.Context, service, endpoint, method string, apiFunc func(context.Context) (int, error)) error {
	return e.otelService.TraceExternalAPICall(ctx, service, endpoint, method, apiFunc)
}

// TraceCorrelationPropagator helps propagate trace context across async boundaries
type TraceCorrelationPropagator struct {
	otelService *services.OTelService
}

// NewTraceCorrelationPropagator creates a new trace correlation propagator
func NewTraceCorrelationPropagator(otelService *services.OTelService) *TraceCorrelationPropagator {
	return &TraceCorrelationPropagator{
		otelService: otelService,
	}
}

// InjectTraceContext injects trace context into a map for async processing
func (p *TraceCorrelationPropagator) InjectTraceContext(ctx context.Context) map[string]string {
	headers := make(map[string]string)
	otel.GetTextMapPropagator().Inject(ctx, propagation.MapCarrier(headers))
	return headers
}

// ExtractTraceContext extracts trace context from a map
func (p *TraceCorrelationPropagator) ExtractTraceContext(ctx context.Context, headers map[string]string) context.Context {
	return otel.GetTextMapPropagator().Extract(ctx, propagation.MapCarrier(headers))
}

// CreateChildSpan creates a child span for async operations
func (p *TraceCorrelationPropagator) CreateChildSpan(ctx context.Context, name string, attributes ...attribute.KeyValue) (context.Context, trace.Span) {
	opts := []trace.SpanStartOption{
		trace.WithAttributes(attributes...),
		trace.WithSpanKind(trace.SpanKindInternal),
	}
	return p.otelService.StartSpan(ctx, name, opts...)
}
