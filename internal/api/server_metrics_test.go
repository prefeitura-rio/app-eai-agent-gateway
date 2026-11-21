package api

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"

	"github.com/prefeitura-rio/app-eai-agent-gateway/internal/config"
	"github.com/prefeitura-rio/app-eai-agent-gateway/internal/handlers"
)

// newMetricsTestServer builds the router the way NewServer does, without the
// Redis, RabbitMQ and PostgreSQL connections the handlers need.
func newMetricsTestServer(t *testing.T, metricsEnabled bool) *Server {
	t.Helper()

	previousMode := gin.Mode()
	gin.SetMode(gin.TestMode)
	t.Cleanup(func() { gin.SetMode(previousMode) })

	cfg := &config.Config{}
	cfg.Observability.MetricsEnabled = metricsEnabled
	cfg.Observability.MetricsPath = "/metrics"
	cfg.Security.MaxRequestSize = 1 << 20

	logger := logrus.New()
	logger.SetOutput(io.Discard)

	metricsService, err := newMetricsService(cfg)
	if err != nil {
		t.Fatalf("newMetricsService: %v", err)
	}
	if metricsEnabled && metricsService == nil {
		t.Fatal("newMetricsService returned nil with metrics enabled")
	}
	if !metricsEnabled && metricsService != nil {
		t.Fatal("newMetricsService returned a service with metrics disabled")
	}

	server := &Server{
		config:         cfg,
		logger:         logger,
		router:         gin.New(),
		healthHandler:  handlers.NewHealthHandler(time.Second, time.Second, logger),
		metricsService: metricsService,
	}
	server.setupMiddleware()
	server.setupRoutes()
	return server
}

func serve(t *testing.T, router http.Handler, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(method, path, nil))
	return recorder
}

func TestMetricsEndpointExposesHTTPMetrics(t *testing.T) {
	server := newMetricsTestServer(t, true)

	if got := serve(t, server.router, http.MethodGet, "/live").Code; got != http.StatusOK {
		t.Fatalf("GET /live status = %d, want 200", got)
	}
	if got := serve(t, server.router, http.MethodGet, "/synthetic/unknown/path-123").Code; got != http.StatusNotFound {
		t.Fatalf("GET unknown path status = %d, want 404", got)
	}

	response := serve(t, server.router, http.MethodGet, "/metrics")
	if response.Code != http.StatusOK {
		t.Fatalf("GET /metrics status = %d, want 200", response.Code)
	}
	body := response.Body.String()

	wants := []string{
		`eai_gateway_http_requests_total{endpoint="/live",method="GET",status_code="200"} 1`,
		`eai_gateway_http_requests_total{endpoint="unmatched",method="GET",status_code="404"} 1`,
		`eai_gateway_http_request_duration_seconds_count{endpoint="/live",method="GET",status_code="200"} 1`,
		`eai_gateway_http_response_size_bytes_count{endpoint="/live",method="GET",status_code="200"} 1`,
		// The scrape itself is in flight while /metrics is gathered.
		`eai_gateway_http_requests_in_flight{endpoint="/metrics",method="GET"} 1`,
		// The global registry is still served.
		"go_goroutines",
		"process_",
	}
	for _, want := range wants {
		if !strings.Contains(body, want) {
			t.Errorf("/metrics does not contain %q", want)
		}
	}

	// The raw path of an unmatched request never becomes a label.
	if strings.Contains(body, "/synthetic/unknown/path-123") {
		t.Error("/metrics contains the raw path of an unmatched request")
	}

	// A completed request leaves no request in flight.
	if !strings.Contains(body, `eai_gateway_http_requests_in_flight{endpoint="/live",method="GET"} 0`) {
		t.Error("in-flight gauge of /live did not return to 0")
	}
}

func TestMetricsDisabledRegistersNeitherEndpointNorMiddleware(t *testing.T) {
	server := newMetricsTestServer(t, false)

	if server.metricsService != nil {
		t.Fatal("metrics service created with metrics disabled")
	}
	if got := serve(t, server.router, http.MethodGet, "/metrics").Code; got != http.StatusNotFound {
		t.Fatalf("GET /metrics status = %d, want 404 with metrics disabled", got)
	}
}
