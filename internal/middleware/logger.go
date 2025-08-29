package middleware

import (
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
)

// RequestIDKey is the key used for request ID in context
const RequestIDKey = "request_id"

// Logger returns a middleware that logs HTTP requests
func Logger(logger *logrus.Logger) gin.HandlerFunc {
	return gin.LoggerWithFormatter(func(param gin.LogFormatterParams) string {
		// Create structured log entry
		fields := logrus.Fields{
			"method":     param.Method,
			"path":       param.Path,
			"status":     param.StatusCode,
			"latency":    param.Latency.String(),
			"client_ip":  param.ClientIP,
			"user_agent": param.Request.UserAgent(),
			"request_id": param.Keys[RequestIDKey],
		}

		if param.ErrorMessage != "" {
			fields["error"] = param.ErrorMessage
		}

		// Determine log level based on status code
		switch {
		case param.StatusCode >= 500:
			logger.WithFields(fields).Error("HTTP request")
		case param.StatusCode >= 400:
			logger.WithFields(fields).Warn("HTTP request")
		default:
			logger.WithFields(fields).Info("HTTP request")
		}

		return "" // Return empty string since we're using structured logging
	})
}

// RequestID adds a unique request ID to each request
func RequestID() gin.HandlerFunc {
	return func(c *gin.Context) {
		requestID := strings.TrimSpace(c.GetHeader("X-Request-ID"))
		if requestID == "" {
			requestID = uuid.New().String()
		}

		c.Set(RequestIDKey, requestID)
		c.Header("X-Request-ID", requestID)
		c.Next()
	}
}

// Recovery returns a middleware that recovers from panics
func Recovery(logger *logrus.Logger) gin.HandlerFunc {
	return gin.RecoveryWithWriter(
		&loggerWriter{logger: logger},
		func(c *gin.Context, recovered interface{}) {
			requestID, _ := c.Get(RequestIDKey)

			logger.WithFields(logrus.Fields{
				"request_id": requestID,
				"panic":      recovered,
				"method":     c.Request.Method,
				"path":       c.Request.URL.Path,
				"client_ip":  c.ClientIP(),
			}).Error("Panic recovered")

			c.AbortWithStatus(500)
		},
	)
}

type loggerWriter struct {
	logger *logrus.Logger
}

func (lw *loggerWriter) Write(p []byte) (n int, err error) {
	lw.logger.Error(string(p))
	return len(p), nil
}
