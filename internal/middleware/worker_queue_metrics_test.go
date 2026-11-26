package middleware

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"

	"github.com/prefeitura-rio/app-eai-agent-gateway/internal/services"
)

func newTestMetricsService(t *testing.T) *services.PrometheusMetricsService {
	t.Helper()
	metricsService, err := services.NewPrometheusMetricsService(services.PrometheusConfig{Namespace: "test"})
	if err != nil {
		t.Fatalf("NewPrometheusMetricsService: %v", err)
	}
	return metricsService
}

func assertMetricsContain(t *testing.T, body string, wants ...string) {
	t.Helper()
	for _, want := range wants {
		if !strings.Contains(body, want) {
			t.Errorf("metrics do not contain %q:\n%s", want, body)
		}
	}
}

func TestWrapWorkerTaskDecrementsInFlightOnPanic(t *testing.T) {
	metricsService := newTestMetricsService(t)
	wrapper := NewWorkerMetricsWrapper(metricsService)

	func() {
		defer func() { _ = recover() }()
		_ = wrapper.WrapWorkerTask("w", "t", func() error { panic("synthetic panic") })
	}()

	assertMetricsContain(t, scrapeMetrics(t, metricsService),
		`test_worker_tasks_in_flight{task_type="t",worker_type="w"} 0`)
}

func TestWrapMessageHandlerRecordsStatusByHandlerResult(t *testing.T) {
	metricsService := newTestMetricsService(t)
	wrapper := NewQueueMetricsWrapper(metricsService)

	failure := errors.New("synthetic failure")
	var seen []uint64
	handler := wrapper.WrapMessageHandler("user_messages", "main", func(ctx context.Context, delivery amqp.Delivery) error {
		seen = append(seen, delivery.DeliveryTag)
		if delivery.DeliveryTag == 2 {
			return failure
		}
		return nil
	})

	if err := handler(context.Background(), amqp.Delivery{DeliveryTag: 1}); err != nil {
		t.Fatalf("handler returned %v for a successful delivery", err)
	}
	if err := handler(context.Background(), amqp.Delivery{DeliveryTag: 2}); !errors.Is(err, failure) {
		t.Fatalf("handler returned %v, want the handler error", err)
	}
	if len(seen) != 2 || seen[0] != 1 || seen[1] != 2 {
		t.Fatalf("wrapped handler saw deliveries %v, want [1 2]", seen)
	}

	assertMetricsContain(t, scrapeMetrics(t, metricsService),
		`test_queue_messages_total{queue_name="user_messages",queue_type="main",status="success"} 1`,
		`test_queue_messages_total{queue_name="user_messages",queue_type="main",status="failure"} 1`,
		`test_queue_processing_time_seconds_count{queue_name="user_messages",queue_type="main",status="failure"} 1`,
	)
}

type fakeQueueDepthSource struct {
	mu     sync.Mutex
	depths map[string]int
	calls  int
}

func (f *fakeQueueDepthSource) QueueDepth(queueName string) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	depth, ok := f.depths[queueName]
	if !ok {
		return 0, errors.New("synthetic missing queue")
	}
	return depth, nil
}

func TestCollectQueueDepthSetsDepthAndReportsErrors(t *testing.T) {
	metricsService := newTestMetricsService(t)
	wrapper := NewQueueMetricsWrapper(metricsService)
	source := &fakeQueueDepthSource{depths: map[string]int{"user_messages": 7, "user_messages_dlq": 2}}
	targets := []QueueDepthTarget{
		{Name: "user_messages", Type: "main"},
		{Name: "user_messages_dlq", Type: "dlq"},
		{Name: "missing", Type: "main"},
	}

	ctx, cancel := context.WithCancel(context.Background())
	var failed []string
	var failedMu sync.Mutex
	done := make(chan struct{})
	go func() {
		defer close(done)
		wrapper.CollectQueueDepth(ctx, source, time.Hour, targets, func(target QueueDepthTarget, err error) {
			failedMu.Lock()
			defer failedMu.Unlock()
			failed = append(failed, target.Name)
		})
	}()

	// The first collection runs right away, before the first tick.
	deadline := time.Now().Add(5 * time.Second)
	for {
		source.mu.Lock()
		calls := source.calls
		source.mu.Unlock()
		if calls >= len(targets) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("queue depth was not collected right away")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("CollectQueueDepth did not return after the context was cancelled")
	}

	body := scrapeMetrics(t, metricsService)
	assertMetricsContain(t, body,
		`test_queue_depth{queue_name="user_messages",queue_type="main"} 7`,
		`test_queue_depth{queue_name="user_messages_dlq",queue_type="dlq"} 2`,
	)
	if strings.Contains(body, `queue_name="missing"`) {
		t.Error("a queue whose depth could not be read has a queue_depth series")
	}
	failedMu.Lock()
	defer failedMu.Unlock()
	if len(failed) != 1 || failed[0] != "missing" {
		t.Errorf("onError called for %v, want [missing]", failed)
	}
}
