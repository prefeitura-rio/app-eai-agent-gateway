package middleware

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// SecurityHeaders adds security headers to responses
func SecurityHeaders() gin.HandlerFunc {
	return func(c *gin.Context) {
		// Basic security headers
		c.Header("X-Content-Type-Options", "nosniff")
		c.Header("Referrer-Policy", "strict-origin-when-cross-origin")
		// X-XSS-Protection is legacy: the XSS Auditor was removed from Chrome
		// (M78) and Edge, and Firefox never shipped it; on old browsers
		// `1; mode=block` was itself an info-leak/side-channel vector. We set
		// `0` per the OWASP Secure Headers Project to explicitly disable the
		// legacy auditor on every route. Real XSS protection comes from the
		// per-route Content-Security-Policy below (issue #45).
		c.Header("X-XSS-Protection", "0")

		path := c.Request.URL.Path

		// More permissive headers for Swagger UI
		if strings.HasPrefix(path, "/docs/") || path == "/swagger-ui" {
			// Allow Swagger UI resources with necessary permissions including CDN
			c.Header("Content-Security-Policy", "default-src 'self'; script-src 'self' 'unsafe-inline' 'unsafe-eval' https://unpkg.com; style-src 'self' 'unsafe-inline' https://unpkg.com; img-src 'self' data:; font-src 'self' https://unpkg.com")
			c.Header("X-Frame-Options", "SAMEORIGIN") // Allow framing for Swagger UI
		} else if strings.HasPrefix(path, "/auth/govbr/") {
			// Gov.br callback renders first-party HTML templates with inline CSS.
			// Keep fetches locked down while allowing the template stylesheet.
			c.Header("X-Frame-Options", "DENY")
			c.Header("Content-Security-Policy", "default-src 'none'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; font-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
		} else {
			// Strict CSP for API endpoints
			c.Header("X-Frame-Options", "DENY")
			c.Header("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
		}

		c.Next()
	}
}

// RequestSizeLimit limits the size of request bodies
func RequestSizeLimit(maxSize int64) gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request.ContentLength > maxSize {
			c.AbortWithStatusJSON(http.StatusRequestEntityTooLarge, gin.H{
				"error":    "Request body too large",
				"max_size": maxSize,
			})
			return
		}

		// Set a limited reader for the request body
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxSize)
		c.Next()
	}
}
