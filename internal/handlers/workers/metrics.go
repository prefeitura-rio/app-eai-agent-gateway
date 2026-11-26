package workers

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/prefeitura-rio/app-eai-agent-gateway/internal/services"
)

// NewHealthMux returns the routes of the worker's health server: /health,
// /ready and /live and, when metricsService is not nil, the Prometheus
// metrics on metricsPath.
func NewHealthMux(health *WorkerHealthHandler, metricsService *services.PrometheusMetricsService, metricsPath string) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", health.Health)
	mux.HandleFunc("/ready", health.Ready)
	mux.HandleFunc("/live", health.Live)
	if metricsService != nil {
		if metricsPath == "" {
			metricsPath = "/metrics"
		}
		mux.Handle(metricsPath, metricsHandler(metricsService))
	}
	return mux
}

// metricsHandler serves the global Prometheus registry (Go and process
// collectors) together with the registry of the worker's metrics service.
func metricsHandler(metricsService *services.PrometheusMetricsService) http.Handler {
	gatherers := prometheus.Gatherers{prometheus.DefaultGatherer, metricsService.GetPrometheusRegistry()}
	return promhttp.InstrumentMetricHandler(
		prometheus.DefaultRegisterer,
		promhttp.HandlerFor(gatherers, promhttp.HandlerOpts{}),
	)
}
