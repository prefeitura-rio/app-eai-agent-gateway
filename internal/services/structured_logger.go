package services

import (
	"context"
	"time"

	"github.com/sirupsen/logrus"
)

// ContextKey is a custom type for context keys to avoid collisions
type ContextKey string

// CorrelationKeys defines the context keys for correlation tracking
var (
	CorrelationIDKey = ContextKey("correlation_id")
	RequestIDKey     = ContextKey("request_id")
	UserIDKey        = ContextKey("user_id")
	TraceIDKey       = ContextKey("trace_id")
	SpanIDKey        = ContextKey("span_id")
)

// StructuredLogger provides enhanced logging with correlation ID support
type StructuredLogger struct {
	logger *logrus.Logger
}

// NewStructuredLogger creates a new structured logger
func NewStructuredLogger(logger *logrus.Logger) *StructuredLogger {
	return &StructuredLogger{
		logger: logger,
	}
}

// WithContext creates a logger entry with correlation fields from context
func (s *StructuredLogger) WithContext(ctx context.Context) *logrus.Entry {
	entry := s.logger.WithFields(logrus.Fields{})

	// Add correlation fields from context
	if correlationID := GetCorrelationID(ctx); correlationID != "" {
		entry = entry.WithField(string(CorrelationIDKey), correlationID)
	}

	if requestID := GetRequestID(ctx); requestID != "" {
		entry = entry.WithField(string(RequestIDKey), requestID)
	}

	if userID := GetUserID(ctx); userID != "" {
		entry = entry.WithField(string(UserIDKey), userID)
	}

	if traceID := GetTraceID(ctx); traceID != "" {
		entry = entry.WithField(string(TraceIDKey), traceID)
	}

	if spanID := GetSpanID(ctx); spanID != "" {
		entry = entry.WithField(string(SpanIDKey), spanID)
	}

	return entry
}

// WithError creates a logger entry with error details and correlation context
func (s *StructuredLogger) WithError(ctx context.Context, err error) *logrus.Entry {
	entry := s.WithContext(ctx)
	if err != nil {
		entry = entry.WithError(err)
	}
	return entry
}

// WithOperation creates a logger entry with operation details
func (s *StructuredLogger) WithOperation(ctx context.Context, operation string) *logrus.Entry {
	return s.WithContext(ctx).WithField("operation", operation)
}

// WithDuration creates a logger entry with duration tracking
func (s *StructuredLogger) WithDuration(ctx context.Context, operation string, start time.Time) *logrus.Entry {
	duration := time.Since(start)
	return s.WithContext(ctx).WithFields(logrus.Fields{
		"operation":   operation,
		"duration":    duration.String(),
		"duration_ms": duration.Milliseconds(),
	})
}

// WithWorker creates a logger entry with worker context
func (s *StructuredLogger) WithWorker(ctx context.Context, workerType, workerID string) *logrus.Entry {
	return s.WithContext(ctx).WithFields(logrus.Fields{
		"worker_type": workerType,
		"worker_id":   workerID,
	})
}

// WithExternalAPI creates a logger entry with external API call context
func (s *StructuredLogger) WithExternalAPI(ctx context.Context, service, endpoint string) *logrus.Entry {
	return s.WithContext(ctx).WithFields(logrus.Fields{
		"external_service":  service,
		"external_endpoint": endpoint,
	})
}

// WithMessage creates a logger entry with message processing context
func (s *StructuredLogger) WithMessage(ctx context.Context, messageID, messageType string) *logrus.Entry {
	return s.WithContext(ctx).WithFields(logrus.Fields{
		"message_id":   messageID,
		"message_type": messageType,
	})
}

// Context helper functions for getting values from context

// GetCorrelationID retrieves correlation ID from context
func GetCorrelationID(ctx context.Context) string {
	if v := ctx.Value(CorrelationIDKey); v != nil {
		if id, ok := v.(string); ok {
			return id
		}
	}
	return ""
}

// GetRequestID retrieves request ID from context
func GetRequestID(ctx context.Context) string {
	if v := ctx.Value(RequestIDKey); v != nil {
		if id, ok := v.(string); ok {
			return id
		}
	}
	return ""
}

// GetUserID retrieves user ID from context
func GetUserID(ctx context.Context) string {
	if v := ctx.Value(UserIDKey); v != nil {
		if id, ok := v.(string); ok {
			return id
		}
	}
	return ""
}

// GetTraceID retrieves trace ID from context
func GetTraceID(ctx context.Context) string {
	if v := ctx.Value(TraceIDKey); v != nil {
		if id, ok := v.(string); ok {
			return id
		}
	}
	return ""
}

// GetSpanID retrieves span ID from context
func GetSpanID(ctx context.Context) string {
	if v := ctx.Value(SpanIDKey); v != nil {
		if id, ok := v.(string); ok {
			return id
		}
	}
	return ""
}

// Context helper functions for setting values in context

// WithCorrelationID adds correlation ID to context
func WithCorrelationID(ctx context.Context, correlationID string) context.Context {
	return context.WithValue(ctx, CorrelationIDKey, correlationID)
}

// WithRequestID adds request ID to context
func WithRequestID(ctx context.Context, requestID string) context.Context {
	return context.WithValue(ctx, RequestIDKey, requestID)
}

// WithUserID adds user ID to context
func WithUserID(ctx context.Context, userID string) context.Context {
	return context.WithValue(ctx, UserIDKey, userID)
}

// WithTraceID adds trace ID to context
func WithTraceID(ctx context.Context, traceID string) context.Context {
	return context.WithValue(ctx, TraceIDKey, traceID)
}

// WithSpanID adds span ID to context
func WithSpanID(ctx context.Context, spanID string) context.Context {
	return context.WithValue(ctx, SpanIDKey, spanID)
}

// LogMetrics logs operation metrics with structured fields
func (s *StructuredLogger) LogMetrics(ctx context.Context, operation string, metrics map[string]interface{}) {
	entry := s.WithOperation(ctx, operation)
	for key, value := range metrics {
		entry = entry.WithField(key, value)
	}
	entry.Info("Operation metrics")
}

// LogAPICall logs external API call details
func (s *StructuredLogger) LogAPICall(ctx context.Context, service, method, endpoint string, statusCode int, duration time.Duration, err error) {
	entry := s.WithExternalAPI(ctx, service, endpoint).WithFields(logrus.Fields{
		"http_method": method,
		"status_code": statusCode,
		"duration":    duration.String(),
		"duration_ms": duration.Milliseconds(),
	})

	if err != nil {
		entry = entry.WithError(err)
		entry.Error("External API call failed")
	} else {
		entry.Info("External API call completed")
	}
}

// LogTaskExecution logs task execution details
func (s *StructuredLogger) LogTaskExecution(ctx context.Context, taskType, taskID string, duration time.Duration, success bool, err error) {
	entry := s.WithContext(ctx).WithFields(logrus.Fields{
		"task_type":   taskType,
		"task_id":     taskID,
		"duration":    duration.String(),
		"duration_ms": duration.Milliseconds(),
		"success":     success,
	})

	if err != nil {
		entry = entry.WithError(err)
		entry.Error("Task execution failed")
	} else {
		entry.Info("Task execution completed")
	}
}

// LogWorkerEvent logs worker lifecycle events
func (s *StructuredLogger) LogWorkerEvent(ctx context.Context, workerType, workerID, event string, details map[string]interface{}) {
	entry := s.WithWorker(ctx, workerType, workerID).WithField("event", event)

	for key, value := range details {
		entry = entry.WithField(key, value)
	}

	entry.Info("Worker event")
}

// LogSecurityEvent logs security-related events
func (s *StructuredLogger) LogSecurityEvent(ctx context.Context, eventType, description string, severity string) {
	entry := s.WithContext(ctx).WithFields(logrus.Fields{
		"security_event_type": eventType,
		"description":         description,
		"severity":            severity,
	})

	switch severity {
	case "critical", "high":
		entry.Error("Security event")
	case "medium":
		entry.Warn("Security event")
	default:
		entry.Info("Security event")
	}
}
