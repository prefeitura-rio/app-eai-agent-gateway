package middleware

import (
	"bytes"
	"io"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"

	"github.com/prefeitura-rio/app-eai-agent-gateway/internal/services"
)

// StructuredLoggingMiddleware provides enhanced request/response logging with correlation IDs
func StructuredLoggingMiddleware(structuredLogger *services.StructuredLogger) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()

		// Log request start
		entry := structuredLogger.WithContext(c.Request.Context()).WithFields(logrus.Fields{
			"method":     c.Request.Method,
			"path":       c.Request.URL.Path,
			"query":      c.Request.URL.RawQuery,
			"user_agent": c.Request.UserAgent(),
			"client_ip":  c.ClientIP(),
			"referer":    c.GetHeader("Referer"),
		})

		// Log request body for non-GET requests (with size limit)
		if c.Request.Method != "GET" && c.Request.Method != "HEAD" {
			if c.Request.ContentLength > 0 && c.Request.ContentLength < 1024*10 { // 10KB limit
				bodyBytes, err := io.ReadAll(c.Request.Body)
				if err == nil {
					c.Request.Body = io.NopCloser(bytes.NewReader(bodyBytes))
					entry = entry.WithField("request_body", string(bodyBytes))
				}
			}
			entry = entry.WithField("content_length", c.Request.ContentLength)
			entry = entry.WithField("content_type", c.Request.Header.Get("Content-Type"))
		}

		entry.Info("HTTP request started")

		// Capture response
		responseRecorder := &responseWriter{
			ResponseWriter: c.Writer,
			body:           &bytes.Buffer{},
		}
		c.Writer = responseRecorder

		c.Next()

		// Calculate duration
		duration := time.Since(start)

		// Log response
		responseEntry := structuredLogger.WithDuration(c.Request.Context(), "http_request", start).WithFields(logrus.Fields{
			"method":        c.Request.Method,
			"path":          c.Request.URL.Path,
			"status_code":   responseRecorder.status,
			"response_size": responseRecorder.body.Len(),
			"client_ip":     c.ClientIP(),
		})

		// Add response body for non-binary content (with size limit)
		if responseRecorder.body.Len() > 0 && responseRecorder.body.Len() < 1024*5 { // 5KB limit
			contentType := c.Writer.Header().Get("Content-Type")
			if isTextContent(contentType) {
				responseEntry = responseEntry.WithField("response_body", responseRecorder.body.String())
			}
		}

		// Add error information if present
		if len(c.Errors) > 0 {
			errors := make([]string, len(c.Errors))
			for i, err := range c.Errors {
				errors[i] = err.Error()
			}
			responseEntry = responseEntry.WithField("errors", errors)
		}

		// Determine log level based on status code and duration
		logLevel := determineLogLevel(responseRecorder.status, duration)

		switch logLevel {
		case logrus.ErrorLevel:
			responseEntry.Error("HTTP request completed")
		case logrus.WarnLevel:
			responseEntry.Warn("HTTP request completed")
		default:
			responseEntry.Info("HTTP request completed")
		}
	}
}

// responseWriter captures response data for logging
type responseWriter struct {
	gin.ResponseWriter
	body   *bytes.Buffer
	status int
}

func (rw *responseWriter) Write(data []byte) (int, error) {
	rw.body.Write(data)
	return rw.ResponseWriter.Write(data)
}

func (rw *responseWriter) WriteHeader(statusCode int) {
	rw.status = statusCode
	rw.ResponseWriter.WriteHeader(statusCode)
}

// isTextContent determines if content type is text-based for safe logging
func isTextContent(contentType string) bool {
	textTypes := []string{
		"application/json",
		"application/xml",
		"text/",
		"application/x-www-form-urlencoded",
	}

	for _, textType := range textTypes {
		if len(contentType) >= len(textType) && contentType[:len(textType)] == textType {
			return true
		}
	}
	return false
}

// determineLogLevel determines the appropriate log level based on response status and duration
func determineLogLevel(statusCode int, duration time.Duration) logrus.Level {
	// Error level for server errors
	if statusCode >= 500 {
		return logrus.ErrorLevel
	}

	// Warn level for client errors or slow requests
	if statusCode >= 400 || duration > 5*time.Second {
		return logrus.WarnLevel
	}

	// Info level for successful requests
	return logrus.InfoLevel
}

// ErrorTrackingMiddleware logs errors with detailed context
func ErrorTrackingMiddleware(structuredLogger *services.StructuredLogger) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Next()

		// Log errors if any occurred
		if len(c.Errors) > 0 {
			for _, ginErr := range c.Errors {
				entry := structuredLogger.WithError(c.Request.Context(), ginErr.Err).WithFields(logrus.Fields{
					"error_type":  ginErr.Type,
					"error_meta":  ginErr.Meta,
					"method":      c.Request.Method,
					"path":        c.Request.URL.Path,
					"status_code": c.Writer.Status(),
				})

				switch ginErr.Type {
				case gin.ErrorTypePublic:
					entry.Warn("Public error occurred")
				case gin.ErrorTypeBind:
					entry.Warn("Request binding error")
				case gin.ErrorTypeRender:
					entry.Error("Response rendering error")
				default:
					entry.Error("Internal error occurred")
				}
			}
		}
	}
}

// PanicRecoveryMiddleware provides enhanced panic recovery with structured logging
func PanicRecoveryMiddleware(structuredLogger *services.StructuredLogger) gin.HandlerFunc {
	return gin.CustomRecovery(func(c *gin.Context, recovered interface{}) {
		entry := structuredLogger.WithContext(c.Request.Context()).WithFields(logrus.Fields{
			"panic_value": recovered,
			"method":      c.Request.Method,
			"path":        c.Request.URL.Path,
			"client_ip":   c.ClientIP(),
			"user_agent":  c.Request.UserAgent(),
		})

		entry.Error("Panic recovered in HTTP request")

		// Log security event for potential attacks
		structuredLogger.LogSecurityEvent(
			c.Request.Context(),
			"panic_recovery",
			"Application panic occurred during request processing",
			"high",
		)

		c.AbortWithStatus(500)
	})
}

// PerformanceTrackingMiddleware tracks slow requests
func PerformanceTrackingMiddleware(structuredLogger *services.StructuredLogger, slowThreshold time.Duration) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()

		c.Next()

		duration := time.Since(start)

		// Log slow requests
		if duration > slowThreshold {
			entry := structuredLogger.WithDuration(c.Request.Context(), "slow_request", start).WithFields(logrus.Fields{
				"method":         c.Request.Method,
				"path":           c.Request.URL.Path,
				"status_code":    c.Writer.Status(),
				"slow_threshold": slowThreshold.String(),
			})

			entry.Warn("Slow request detected")
		}
	}
}
