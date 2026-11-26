package workers

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/sirupsen/logrus"

	"github.com/prefeitura-rio/app-eai-agent-gateway/internal/middleware"
	"github.com/prefeitura-rio/app-eai-agent-gateway/internal/services"
)

func newWorkerMetricsTestMux(t *testing.T) (*http.ServeMux, *services.PrometheusMetricsService) {
	t.Helper()

	metricsService, err := services.NewPrometheusMetricsService(services.PrometheusConfig{Namespace: "eai_gateway"})
	if err != nil {
		t.Fatalf("NewPrometheusMetricsService: %v", err)
	}

	logger := logrus.New()
	logger.SetOutput(io.Discard)
	health := NewWorkerHealthHandler(nil, nil, logger)
	return NewHealthMux(health, metricsService, "/metrics"), metricsService
}

func get(t *testing.T, mux http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
	return recorder
}

func TestWorkerMetricsEndpointExposesWorkerAndQueueSeries(t *testing.T) {
	mux, metricsService := newWorkerMetricsTestMux(t)
	workerMetrics := middleware.NewWorkerMetricsWrapper(metricsService)
	queueMetrics := middleware.NewQueueMetricsWrapper(metricsService)

	// One task that succeeds, one that fails permanently, one retriable failure
	// that the consumer publishes again, and one that already used its retries.
	if err := runUserMessageTask(workerMetrics, func() error { return nil }); err != nil {
		t.Fatalf("successful task returned %v", err)
	}
	failure := errors.New("synthetic failure")
	if err := runUserMessageTask(workerMetrics, func() error { return failure }); !errors.Is(err, failure) {
		t.Fatalf("failed task returned %v, want the task error", err)
	}
	recordUserMessageRetry(workerMetrics, amqp.Delivery{}, 3)
	recordUserMessageRetry(workerMetrics, amqp.Delivery{Headers: amqp.Table{"x-retry-count": int64(3)}}, 3)
	queueMetrics.UpdateQueueDepth("user_messages", "main", 4)

	response := get(t, mux, "/metrics")
	if response.Code != http.StatusOK {
		t.Fatalf("GET /metrics status = %d, want 200", response.Code)
	}
	body := response.Body.String()

	for _, want := range []string{
		`eai_gateway_worker_tasks_total{status="success",task_type="process_user_message",worker_type="user_message_worker"} 1`,
		`eai_gateway_worker_tasks_total{status="failure",task_type="process_user_message",worker_type="user_message_worker"} 1`,
		`eai_gateway_worker_task_duration_seconds_count{status="failure",task_type="process_user_message",worker_type="user_message_worker"} 1`,
		`eai_gateway_worker_tasks_in_flight{task_type="process_user_message",worker_type="user_message_worker"} 0`,
		`eai_gateway_worker_retry_attempts_total{retry_reason="retriable_error",task_type="process_user_message",worker_type="user_message_worker"} 1`,
		`eai_gateway_queue_depth{queue_name="user_messages",queue_type="main"} 4`,
		// The global registry is served too.
		"go_goroutines",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("/metrics does not contain %q", want)
		}
	}
	for _, removed := range []string{"eai_gateway_cache_", "eai_gateway_external_api_"} {
		if strings.Contains(body, removed) {
			t.Errorf("/metrics contains removed series %q", removed)
		}
	}

	// The health routes stay on the same server.
	if got := get(t, mux, "/live").Code; got != http.StatusOK {
		t.Errorf("GET /live status = %d, want 200", got)
	}
}

func TestWorkerHealthMuxWithoutMetricsHasNoMetricsRoute(t *testing.T) {
	logger := logrus.New()
	logger.SetOutput(io.Discard)
	mux := NewHealthMux(NewWorkerHealthHandler(nil, nil, logger), nil, "/metrics")

	if got := get(t, mux, "/metrics").Code; got != http.StatusNotFound {
		t.Errorf("GET /metrics status = %d, want 404 with metrics disabled", got)
	}
	if got := get(t, mux, "/live").Code; got != http.StatusOK {
		t.Errorf("GET /live status = %d, want 200", got)
	}
}

func TestRunUserMessageTaskWithoutMetricsRunsTheTask(t *testing.T) {
	ran := false
	failure := errors.New("synthetic failure")
	err := runUserMessageTask(nil, func() error {
		ran = true
		return failure
	})
	if !ran {
		t.Fatal("task did not run")
	}
	if !errors.Is(err, failure) {
		t.Fatalf("runUserMessageTask returned %v, want the task error", err)
	}
	// Without metrics a retry is not recorded and does not panic.
	recordUserMessageRetry(nil, amqp.Delivery{}, 3)
}

func TestWillBeRetriedMirrorsConsumerRetryCount(t *testing.T) {
	for _, tc := range []struct {
		name    string
		headers amqp.Table
		want    bool
	}{
		{"first delivery", nil, true},
		{"below the limit", amqp.Table{"x-retry-count": int64(2)}, true},
		{"at the limit", amqp.Table{"x-retry-count": int64(3)}, false},
		{"above the limit", amqp.Table{"x-retry-count": int64(5)}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := willBeRetried(amqp.Delivery{Headers: tc.headers}, 3); got != tc.want {
				t.Errorf("willBeRetried = %v, want %v", got, tc.want)
			}
		})
	}
}
