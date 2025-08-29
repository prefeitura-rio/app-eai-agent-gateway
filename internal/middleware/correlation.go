package middleware

import (
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/prefeitura-rio/app-eai-agent-gateway/internal/services"
)

// CorrelationHeaders defines the HTTP headers used for correlation tracking
const (
	CorrelationIDHeader = "X-Correlation-ID"
	RequestIDHeader     = "X-Request-ID"
	UserIDHeader        = "X-User-ID"
	TraceIDHeader       = "X-Trace-ID"
	SpanIDHeader        = "X-Span-ID"
)

// CorrelationMiddleware adds correlation IDs to the request context
func CorrelationMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx := c.Request.Context()

		// Get or generate correlation ID
		correlationID := c.GetHeader(CorrelationIDHeader)
		if correlationID == "" {
			correlationID = uuid.New().String()
		}

		// Get or generate request ID
		requestID := c.GetHeader(RequestIDHeader)
		if requestID == "" {
			requestID = uuid.New().String()
		}

		// Get optional user ID
		userID := c.GetHeader(UserIDHeader)

		// Get optional trace and span IDs (for OpenTelemetry integration)
		traceID := c.GetHeader(TraceIDHeader)
		spanID := c.GetHeader(SpanIDHeader)

		// Add correlation IDs to context
		ctx = services.WithCorrelationID(ctx, correlationID)
		ctx = services.WithRequestID(ctx, requestID)

		if userID != "" {
			ctx = services.WithUserID(ctx, userID)
		}

		if traceID != "" {
			ctx = services.WithTraceID(ctx, traceID)
		}

		if spanID != "" {
			ctx = services.WithSpanID(ctx, spanID)
		}

		// Update request context
		c.Request = c.Request.WithContext(ctx)

		// Set response headers
		c.Header(CorrelationIDHeader, correlationID)
		c.Header(RequestIDHeader, requestID)

		// Add to gin context for backward compatibility
		c.Set("correlation_id", correlationID)
		c.Set("request_id", requestID)
		if userID != "" {
			c.Set("user_id", userID)
		}

		c.Next()
	}
}

// ExtractUserIDFromRequest extracts user ID from various sources (headers, JWT, etc.)
func ExtractUserIDFromRequest(c *gin.Context) string {
	// Try header first
	if userID := c.GetHeader(UserIDHeader); userID != "" {
		return userID
	}

	// Try from Authorization header (simplified - would need proper JWT parsing in real implementation)
	if auth := c.GetHeader("Authorization"); auth != "" {
		// This is a placeholder - implement JWT parsing as needed
		// userID := extractUserIDFromJWT(auth)
		// return userID
		_ = auth // TODO: implement JWT parsing
	}

	// Try from query parameter
	if userID := c.Query("user_id"); userID != "" {
		return userID
	}

	return ""
}
