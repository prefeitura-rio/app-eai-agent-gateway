package services

import (
	"context"
	"fmt"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.21.0"
	"go.opentelemetry.io/otel/trace"
)

// OTelService provides OpenTelemetry integration for SigNoz
type OTelService struct {
	tracer         trace.Tracer
	meter          metric.Meter
	traceProvider  *sdktrace.TracerProvider
	metricProvider *sdkmetric.MeterProvider

	// Metrics instruments
	httpRequestsTotal    metric.Int64Counter
	httpRequestDuration  metric.Float64Histogram
	httpRequestsInFlight metric.Int64UpDownCounter

	workerTasksTotal    metric.Int64Counter
	workerTaskDuration  metric.Float64Histogram
	workerTasksInFlight metric.Int64UpDownCounter

	queueDepth          metric.Int64UpDownCounter
	queueProcessingTime metric.Float64Histogram
	queueMessages       metric.Int64Counter

	cacheOperations metric.Int64Counter
	cacheHitRatio   metric.Float64Gauge

	externalAPICalls    metric.Int64Counter
	externalAPIDuration metric.Float64Histogram
}

// OTelConfig configures OpenTelemetry service
type OTelConfig struct {
	ServiceName    string
	ServiceVersion string
	Environment    string
	OTLPEndpoint   string
	Insecure       bool
	Headers        map[string]string
}

// NewOTelService creates a new OpenTelemetry service
func NewOTelService(ctx context.Context, config OTelConfig) (*OTelService, error) {
	// Create resource
	res, err := resource.New(ctx,
		resource.WithAttributes(
			semconv.ServiceName(config.ServiceName),
			semconv.ServiceVersion(config.ServiceVersion),
			semconv.DeploymentEnvironment(config.Environment),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create resource: %w", err)
	}

	service := &OTelService{}

	// Initialize tracing
	if err := service.initTracing(ctx, res, config); err != nil {
		return nil, fmt.Errorf("failed to initialize tracing: %w", err)
	}

	// Initialize metrics
	if err := service.initMetrics(ctx, res, config); err != nil {
		return nil, fmt.Errorf("failed to initialize metrics: %w", err)
	}

	// Initialize metric instruments
	if err := service.initMetricInstruments(); err != nil {
		return nil, fmt.Errorf("failed to initialize metric instruments: %w", err)
	}

	return service, nil
}

// initTracing initializes OpenTelemetry tracing
func (s *OTelService) initTracing(ctx context.Context, res *resource.Resource, config OTelConfig) error {
	// Create OTLP trace exporter
	traceExporter, err := otlptracegrpc.New(ctx,
		otlptracegrpc.WithEndpoint(config.OTLPEndpoint),
		otlptracegrpc.WithInsecure(),
		otlptracegrpc.WithHeaders(config.Headers),
		otlptracegrpc.WithTimeout(30*time.Second), // Increase timeout for trace exports
	)
	if err != nil {
		return fmt.Errorf("failed to create trace exporter: %w", err)
	}

	// Create trace provider
	s.traceProvider = sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(traceExporter),
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
	)

	// Set global trace provider
	otel.SetTracerProvider(s.traceProvider)

	// Set global propagator
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	// Create tracer
	s.tracer = s.traceProvider.Tracer(
		"eai-agent-gateway",
		trace.WithInstrumentationVersion("1.0.0"),
	)

	return nil
}

// initMetrics initializes OpenTelemetry metrics
func (s *OTelService) initMetrics(ctx context.Context, res *resource.Resource, config OTelConfig) error {
	// Create OTLP metric exporter
	metricExporter, err := otlpmetricgrpc.New(ctx,
		otlpmetricgrpc.WithEndpoint(config.OTLPEndpoint),
		otlpmetricgrpc.WithInsecure(),
		otlpmetricgrpc.WithHeaders(config.Headers),
	)
	if err != nil {
		return fmt.Errorf("failed to create metric exporter: %w", err)
	}

	// Create metric provider
	s.metricProvider = sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExporter,
			sdkmetric.WithInterval(15*time.Second))),
		sdkmetric.WithResource(res),
	)

	// Set global metric provider
	otel.SetMeterProvider(s.metricProvider)

	// Create meter
	s.meter = s.metricProvider.Meter(
		"eai-agent-gateway",
		metric.WithInstrumentationVersion("1.0.0"),
	)

	return nil
}

// initMetricInstruments creates all metric instruments
func (s *OTelService) initMetricInstruments() error {
	var err error

	// HTTP metrics
	s.httpRequestsTotal, err = s.meter.Int64Counter(
		"http_requests_total",
		metric.WithDescription("Total number of HTTP requests processed"),
	)
	if err != nil {
		return fmt.Errorf("failed to create HTTP requests counter: %w", err)
	}

	s.httpRequestDuration, err = s.meter.Float64Histogram(
		"http_request_duration_seconds",
		metric.WithDescription("HTTP request duration in seconds"),
	)
	if err != nil {
		return fmt.Errorf("failed to create HTTP duration histogram: %w", err)
	}

	s.httpRequestsInFlight, err = s.meter.Int64UpDownCounter(
		"http_requests_in_flight",
		metric.WithDescription("Number of HTTP requests currently being processed"),
	)
	if err != nil {
		return fmt.Errorf("failed to create HTTP in-flight counter: %w", err)
	}

	// Worker metrics
	s.workerTasksTotal, err = s.meter.Int64Counter(
		"worker_tasks_total",
		metric.WithDescription("Total number of worker tasks processed"),
	)
	if err != nil {
		return fmt.Errorf("failed to create worker tasks counter: %w", err)
	}

	s.workerTaskDuration, err = s.meter.Float64Histogram(
		"worker_task_duration_seconds",
		metric.WithDescription("Worker task duration in seconds"),
	)
	if err != nil {
		return fmt.Errorf("failed to create worker duration histogram: %w", err)
	}

	s.workerTasksInFlight, err = s.meter.Int64UpDownCounter(
		"worker_tasks_in_flight",
		metric.WithDescription("Number of worker tasks currently being processed"),
	)
	if err != nil {
		return fmt.Errorf("failed to create worker in-flight counter: %w", err)
	}

	// Queue metrics
	s.queueDepth, err = s.meter.Int64UpDownCounter(
		"queue_depth",
		metric.WithDescription("Current depth of message queues"),
	)
	if err != nil {
		return fmt.Errorf("failed to create queue depth gauge: %w", err)
	}

	s.queueProcessingTime, err = s.meter.Float64Histogram(
		"queue_processing_time_seconds",
		metric.WithDescription("Message queue processing time in seconds"),
	)
	if err != nil {
		return fmt.Errorf("failed to create queue processing histogram: %w", err)
	}

	s.queueMessages, err = s.meter.Int64Counter(
		"queue_messages_total",
		metric.WithDescription("Total number of queue messages processed"),
	)
	if err != nil {
		return fmt.Errorf("failed to create queue messages counter: %w", err)
	}

	// Cache metrics
	s.cacheOperations, err = s.meter.Int64Counter(
		"cache_operations_total",
		metric.WithDescription("Total number of cache operations"),
	)
	if err != nil {
		return fmt.Errorf("failed to create cache operations counter: %w", err)
	}

	s.cacheHitRatio, err = s.meter.Float64Gauge(
		"cache_hit_ratio",
		metric.WithDescription("Cache hit ratio (0.0 to 1.0)"),
	)
	if err != nil {
		return fmt.Errorf("failed to create cache hit ratio gauge: %w", err)
	}

	// External API metrics
	s.externalAPICalls, err = s.meter.Int64Counter(
		"external_api_calls_total",
		metric.WithDescription("Total number of external API calls"),
	)
	if err != nil {
		return fmt.Errorf("failed to create external API calls counter: %w", err)
	}

	s.externalAPIDuration, err = s.meter.Float64Histogram(
		"external_api_duration_seconds",
		metric.WithDescription("External API call duration in seconds"),
	)
	if err != nil {
		return fmt.Errorf("failed to create external API duration histogram: %w", err)
	}

	return nil
}

// Shutdown gracefully shuts down the OpenTelemetry service
func (s *OTelService) Shutdown(ctx context.Context) error {
	var errs []error

	if s.traceProvider != nil {
		if err := s.traceProvider.Shutdown(ctx); err != nil {
			errs = append(errs, fmt.Errorf("failed to shutdown trace provider: %w", err))
		}
	}

	if s.metricProvider != nil {
		if err := s.metricProvider.Shutdown(ctx); err != nil {
			errs = append(errs, fmt.Errorf("failed to shutdown metric provider: %w", err))
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("shutdown errors: %v", errs)
	}

	return nil
}

// GetTracer returns the OpenTelemetry tracer
func (s *OTelService) GetTracer() trace.Tracer {
	return s.tracer
}

// StartSpan starts a new trace span
func (s *OTelService) StartSpan(ctx context.Context, name string, opts ...trace.SpanStartOption) (context.Context, trace.Span) {
	return s.tracer.Start(ctx, name, opts...)
}

// HTTP Tracing Methods

// TraceHTTPRequest traces an HTTP request with automatic span creation
func (s *OTelService) TraceHTTPRequest(ctx context.Context, method, endpoint string, handler func(context.Context) (int, error)) error {
	ctx, span := s.StartSpan(ctx, fmt.Sprintf("HTTP %s %s", method, endpoint),
		trace.WithAttributes(
			attribute.String("http.method", method),
			attribute.String("http.route", endpoint),
			attribute.String("span.kind", "server"),
		))
	defer span.End()

	start := time.Now()

	// Increment in-flight requests
	s.httpRequestsInFlight.Add(ctx, 1, metric.WithAttributes(
		attribute.String("method", method),
		attribute.String("endpoint", endpoint),
	))
	defer s.httpRequestsInFlight.Add(ctx, -1, metric.WithAttributes(
		attribute.String("method", method),
		attribute.String("endpoint", endpoint),
	))

	// Execute handler
	statusCode, err := handler(ctx)
	duration := time.Since(start)

	// Record metrics
	s.httpRequestsTotal.Add(ctx, 1, metric.WithAttributes(
		attribute.String("method", method),
		attribute.String("endpoint", endpoint),
		attribute.Int("status_code", statusCode),
	))

	s.httpRequestDuration.Record(ctx, duration.Seconds(), metric.WithAttributes(
		attribute.String("method", method),
		attribute.String("endpoint", endpoint),
		attribute.Int("status_code", statusCode),
	))

	// Add span attributes
	span.SetAttributes(
		attribute.Int("http.status_code", statusCode),
		attribute.Float64("http.duration_ms", float64(duration.Milliseconds())),
	)

	if err != nil {
		span.RecordError(err)
		span.SetAttributes(attribute.String("error", "true"))
	}

	return err
}

// Worker Tracing Methods

// TraceWorkerTask traces a worker task execution
func (s *OTelService) TraceWorkerTask(ctx context.Context, workerType, taskType string, handler func(context.Context) error) error {
	ctx, span := s.StartSpan(ctx, fmt.Sprintf("Worker %s %s", workerType, taskType),
		trace.WithAttributes(
			attribute.String("worker.type", workerType),
			attribute.String("task.type", taskType),
			attribute.String("span.kind", "internal"),
		))
	defer span.End()

	start := time.Now()

	// Increment in-flight tasks
	s.workerTasksInFlight.Add(ctx, 1, metric.WithAttributes(
		attribute.String("worker_type", workerType),
		attribute.String("task_type", taskType),
	))
	defer s.workerTasksInFlight.Add(ctx, -1, metric.WithAttributes(
		attribute.String("worker_type", workerType),
		attribute.String("task_type", taskType),
	))

	// Execute handler
	err := handler(ctx)
	duration := time.Since(start)

	// Determine status
	status := "success"
	if err != nil {
		status = "failure"
		span.RecordError(err)
		span.SetAttributes(attribute.String("error", "true"))
	}

	// Record metrics
	s.workerTasksTotal.Add(ctx, 1, metric.WithAttributes(
		attribute.String("worker_type", workerType),
		attribute.String("task_type", taskType),
		attribute.String("status", status),
	))

	s.workerTaskDuration.Record(ctx, duration.Seconds(), metric.WithAttributes(
		attribute.String("worker_type", workerType),
		attribute.String("task_type", taskType),
		attribute.String("status", status),
	))

	// Add span attributes
	span.SetAttributes(
		attribute.String("task.status", status),
		attribute.Float64("task.duration_ms", float64(duration.Milliseconds())),
	)

	return err
}

// Queue Tracing Methods

// TraceQueueProcessing traces queue message processing
func (s *OTelService) TraceQueueProcessing(ctx context.Context, queueName, queueType string, handler func(context.Context) error) error {
	ctx, span := s.StartSpan(ctx, fmt.Sprintf("Queue %s", queueName),
		trace.WithAttributes(
			attribute.String("queue.name", queueName),
			attribute.String("queue.type", queueType),
			attribute.String("span.kind", "consumer"),
		))
	defer span.End()

	start := time.Now()

	// Execute handler
	err := handler(ctx)
	duration := time.Since(start)

	// Determine status
	status := "success"
	if err != nil {
		status = "failure"
		span.RecordError(err)
		span.SetAttributes(attribute.String("error", "true"))
	}

	// Record metrics
	s.queueMessages.Add(ctx, 1, metric.WithAttributes(
		attribute.String("queue_name", queueName),
		attribute.String("queue_type", queueType),
		attribute.String("status", status),
	))

	s.queueProcessingTime.Record(ctx, duration.Seconds(), metric.WithAttributes(
		attribute.String("queue_name", queueName),
		attribute.String("queue_type", queueType),
		attribute.String("status", status),
	))

	// Add span attributes
	span.SetAttributes(
		attribute.String("message.status", status),
		attribute.Float64("processing.duration_ms", float64(duration.Milliseconds())),
	)

	return err
}

// Cache Tracing Methods

// TraceCacheOperation traces cache operations
func (s *OTelService) TraceCacheOperation(ctx context.Context, operation string, handler func(context.Context) error) error {
	ctx, span := s.StartSpan(ctx, fmt.Sprintf("Cache %s", operation),
		trace.WithAttributes(
			attribute.String("cache.operation", operation),
			attribute.String("span.kind", "client"),
		))
	defer span.End()

	start := time.Now()

	// Execute handler
	err := handler(ctx)
	duration := time.Since(start)

	// Determine status
	status := "success"
	if err != nil {
		status = "error"
		span.RecordError(err)
		span.SetAttributes(attribute.String("error", "true"))
	}

	// Record metrics
	s.cacheOperations.Add(ctx, 1, metric.WithAttributes(
		attribute.String("operation", operation),
		attribute.String("status", status),
	))

	// Add span attributes
	span.SetAttributes(
		attribute.String("cache.status", status),
		attribute.Float64("cache.duration_ms", float64(duration.Milliseconds())),
	)

	return err
}

// External API Tracing Methods

// TraceExternalAPICall traces external API calls
func (s *OTelService) TraceExternalAPICall(ctx context.Context, service, endpoint, method string, handler func(context.Context) (int, error)) error {
	ctx, span := s.StartSpan(ctx, fmt.Sprintf("External %s %s %s", method, service, endpoint),
		trace.WithAttributes(
			attribute.String("http.method", method),
			attribute.String("http.url", endpoint),
			attribute.String("service.name", service),
			attribute.String("span.kind", "client"),
		))
	defer span.End()

	start := time.Now()

	// Execute handler
	statusCode, err := handler(ctx)
	duration := time.Since(start)

	// Record metrics
	s.externalAPICalls.Add(ctx, 1, metric.WithAttributes(
		attribute.String("service", service),
		attribute.String("endpoint", endpoint),
		attribute.String("method", method),
		attribute.Int("status_code", statusCode),
	))

	s.externalAPIDuration.Record(ctx, duration.Seconds(), metric.WithAttributes(
		attribute.String("service", service),
		attribute.String("endpoint", endpoint),
		attribute.String("method", method),
		attribute.Int("status_code", statusCode),
	))

	// Add span attributes
	span.SetAttributes(
		attribute.Int("http.status_code", statusCode),
		attribute.Float64("http.duration_ms", float64(duration.Milliseconds())),
	)

	if err != nil {
		span.RecordError(err)
		span.SetAttributes(attribute.String("error", "true"))
	}

	return err
}

// UpdateQueueDepth updates queue depth metrics
func (s *OTelService) UpdateQueueDepth(ctx context.Context, queueName, queueType string, depth int64) {
	s.queueDepth.Add(ctx, depth, metric.WithAttributes(
		attribute.String("queue_name", queueName),
		attribute.String("queue_type", queueType),
	))
}

// UpdateCacheHitRatio updates cache hit ratio metrics
func (s *OTelService) UpdateCacheHitRatio(ctx context.Context, cacheType string, ratio float64) {
	s.cacheHitRatio.Record(ctx, ratio, metric.WithAttributes(
		attribute.String("cache_type", cacheType),
	))
}
