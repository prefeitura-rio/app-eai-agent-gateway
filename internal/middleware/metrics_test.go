package middleware

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/sirupsen/logrus"

	"github.com/prefeitura-rio/app-eai-agent-gateway/internal/services"
)

func newMetricsTestRouter(t *testing.T) (*gin.Engine, *services.PrometheusMetricsService) {
	t.Helper()

	previousMode := gin.Mode()
	gin.SetMode(gin.TestMode)
	t.Cleanup(func() { gin.SetMode(previousMode) })

	metricsService, err := services.NewPrometheusMetricsService(services.PrometheusConfig{Namespace: "test"})
	if err != nil {
		t.Fatalf("NewPrometheusMetricsService: %v", err)
	}

	logger := logrus.New()
	logger.SetOutput(io.Discard)

	router := gin.New()
	router.Use(MetricsMiddleware(metricsService))
	router.Use(Recovery(logger))
	router.GET("/items/:id", func(c *gin.Context) { c.String(http.StatusOK, "item") })
	router.GET("/empty", func(c *gin.Context) { c.Status(http.StatusNoContent) })
	router.GET("/panic", func(c *gin.Context) { panic("synthetic panic") })
	return router, metricsService
}

func scrapeMetrics(t *testing.T, metricsService *services.PrometheusMetricsService) string {
	t.Helper()
	recorder := httptest.NewRecorder()
	handler := promhttp.HandlerFor(metricsService.GetPrometheusRegistry(), promhttp.HandlerOpts{})
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	return recorder.Body.String()
}

func TestMetricsMiddlewareLabelsByRouteTemplate(t *testing.T) {
	router, metricsService := newMetricsTestRouter(t)

	for _, path := range []string{"/items/1", "/items/2"} {
		router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, path, nil))
	}

	body := scrapeMetrics(t, metricsService)
	want := `test_http_requests_total{endpoint="/items/:id",method="GET",status_code="200"} 2`
	if !strings.Contains(body, want) {
		t.Errorf("metrics do not contain %q:\n%s", want, body)
	}
	if strings.Contains(body, `endpoint="/items/1"`) {
		t.Error("metrics label a matched request by its raw path")
	}
}

func TestMetricsMiddlewareUnmatchedRouteHasBoundedLabel(t *testing.T) {
	router, metricsService := newMetricsTestRouter(t)

	for _, path := range []string{"/synthetic-a", "/synthetic-b/c?user_number=0"} {
		router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, path, nil))
	}

	body := scrapeMetrics(t, metricsService)
	want := `test_http_requests_total{endpoint="unmatched",method="GET",status_code="404"} 2`
	if !strings.Contains(body, want) {
		t.Errorf("metrics do not contain %q:\n%s", want, body)
	}
	if strings.Contains(body, "synthetic-") {
		t.Error("metrics contain the raw path of an unmatched request")
	}
}

func TestMetricsMiddlewareRecordsRecoveredPanic(t *testing.T) {
	router, metricsService := newMetricsTestRouter(t)

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/panic", nil))
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", recorder.Code)
	}

	body := scrapeMetrics(t, metricsService)
	for _, want := range []string{
		`test_http_requests_total{endpoint="/panic",method="GET",status_code="500"} 1`,
		`test_http_requests_in_flight{endpoint="/panic",method="GET"} 0`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("metrics do not contain %q:\n%s", want, body)
		}
	}
}

func TestMetricsMiddlewareResponseWithoutBodyHasZeroSize(t *testing.T) {
	router, metricsService := newMetricsTestRouter(t)

	router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/empty", nil))

	body := scrapeMetrics(t, metricsService)
	want := `test_http_response_size_bytes_sum{endpoint="/empty",method="GET",status_code="204"} 0`
	if !strings.Contains(body, want) {
		t.Errorf("metrics do not contain %q:\n%s", want, body)
	}
}
