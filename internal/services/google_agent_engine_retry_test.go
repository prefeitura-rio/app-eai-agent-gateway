package services

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/prefeitura-rio/app-eai-agent-gateway/internal/config"
)

// roundTripFunc adapta uma função a http.RoundTripper pra interceptar o Do do
// httpClient sem servidor real (#R1).
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// noopRateLimiter: os retries chamam s.rateLimiter.Wait — no teste é no-op.
type noopRateLimiter struct{}

func (noopRateLimiter) Allow(_ context.Context, _ string) (bool, error) { return true, nil }
func (noopRateLimiter) Wait(_ context.Context, _ string) error          { return nil }

func mkResp(r *http.Request, status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     make(http.Header),
		Request:    r,
	}
}

func newRetryTestService(rt http.RoundTripper) *GoogleAgentEngineService {
	lg := logrus.New()
	lg.SetOutput(io.Discard)
	return &GoogleAgentEngineService{
		config: &config.Config{
			GoogleAgentEngine: config.GoogleAgentEngineConfig{
				Location:     "us-central1",
				ProjectID:    "proj",
				MaxRetries:   3,
				RetryBackoff: time.Millisecond, // backoff ~instantâneo no teste
			},
		},
		logger:      lg,
		rateLimiter: noopRateLimiter{},
		httpClient:  &http.Client{Transport: rt},
	}
}

func TestPostQueryWithRetry_RetriesTransientThenSucceeds(t *testing.T) {
	calls := 0
	svc := newRetryTestService(roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			return mkResp(r, 429, "rate limit"), nil // transitório → retenta
		}
		return mkResp(r, 200, `{"name":"op-1"}`), nil
	}))

	out, err := svc.postQueryWithRetry(context.Background(), "tok", map[string]interface{}{}, "eng")
	if err != nil {
		t.Fatalf("esperava sucesso após retry, veio erro: %v", err)
	}
	if calls != 2 {
		t.Fatalf("esperava 2 chamadas (1 falha + 1 sucesso), veio %d", calls)
	}
	if out["name"] != "op-1" {
		t.Fatalf("resposta inesperada: %v", out)
	}
}

func TestPostQueryWithRetry_DoesNotRetryNonTransient(t *testing.T) {
	calls := 0
	svc := newRetryTestService(roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		return mkResp(r, 400, "bad request"), nil // 4xx não-transitório → sem retry
	}))

	_, err := svc.postQueryWithRetry(context.Background(), "tok", map[string]interface{}{}, "eng")
	if err == nil {
		t.Fatal("esperava erro (400 não é retryable)")
	}
	if calls != 1 {
		t.Fatalf("esperava 1 chamada (sem retry), veio %d", calls)
	}
}

func TestPostQueryWithRetry_DoesNotRetryAmbiguousUnavailable(t *testing.T) {
	// 502/503/504/500 são AMBÍGUOS (a operação pode ter sido criada) → NÃO
	// retenta, pra não rodar thread + tools 2× (async_query sem idempotency key).
	calls := 0
	svc := newRetryTestService(roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		return mkResp(r, 503, "unavailable"), nil
	}))

	_, err := svc.postQueryWithRetry(context.Background(), "tok", map[string]interface{}{}, "eng")
	if err == nil {
		t.Fatal("esperava erro (503 é ambíguo, não retryable)")
	}
	if calls != 1 {
		t.Fatalf("esperava 1 chamada (503 não retenta), veio %d", calls)
	}
}

func TestPostQueryWithRetry_StopsAtMaxRetries(t *testing.T) {
	calls := 0
	svc := newRetryTestService(roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		return mkResp(r, 429, "rate limit"), nil // ratelimited sempre → retenta até o teto
	}))

	_, err := svc.postQueryWithRetry(context.Background(), "tok", map[string]interface{}{}, "eng")
	if err == nil {
		t.Fatal("esperava erro após esgotar os retries")
	}
	if calls != 4 { // 1 inicial + 3 retries (MaxRetries=3)
		t.Fatalf("esperava 4 chamadas (1+3), veio %d", calls)
	}
}
