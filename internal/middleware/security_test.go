package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestSecurityHeadersAllowsGovBrCallbackInlineStyles(t *testing.T) {
	gin.SetMode(gin.TestMode)

	router := gin.New()
	router.Use(SecurityHeaders())
	router.GET("/auth/govbr/callback", func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})

	req := httptest.NewRequest(http.MethodGet, "/auth/govbr/callback", nil)
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	// Pin the full policy: a regression in any directive (dropping
	// default-src 'none', img-src data: for the favicon, the inline-style
	// allowance, or the anti-clickjacking guards) must fail the test.
	const wantCSP = "default-src 'none'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; font-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"
	if got := rec.Header().Get("Content-Security-Policy"); got != wantCSP {
		t.Fatalf("expected gov.br callback CSP %q, got %q", wantCSP, got)
	}
	if got := rec.Header().Get("X-Frame-Options"); got != "DENY" {
		t.Fatalf("expected X-Frame-Options DENY, got %q", got)
	}
}

func TestSecurityHeadersKeepAPIEndpointsStrict(t *testing.T) {
	gin.SetMode(gin.TestMode)

	router := gin.New()
	router.Use(SecurityHeaders())
	router.GET("/api/v1/health", func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if got := rec.Header().Get("Content-Security-Policy"); got != "default-src 'none'; frame-ancestors 'none'" {
		t.Fatalf("expected strict API CSP, got %q", got)
	}
}
