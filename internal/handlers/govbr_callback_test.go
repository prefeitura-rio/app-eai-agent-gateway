package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/prefeitura-rio/app-eai-agent-gateway/internal/config"
)

var errStub = errors.New("stub redis error")

// stubRedis is a minimal RedisServiceInterface for testing triggerAutoResume.
// Only SetNX carries behaviour; everything else is an inert stub.
type stubRedis struct {
	setNXResult bool
	setNXErr    error
	setNXCalls  int32
}

func (s *stubRedis) SetNX(_ context.Context, _ string, _ string, _ time.Duration) (bool, error) {
	atomic.AddInt32(&s.setNXCalls, 1)
	return s.setNXResult, s.setNXErr
}

func (s *stubRedis) SetTaskStatus(context.Context, string, string, time.Duration) error { return nil }
func (s *stubRedis) GetTaskStatus(context.Context, string) (string, error)              { return "", nil }
func (s *stubRedis) GetTaskResult(context.Context, string, interface{}) error           { return nil }
func (s *stubRedis) Get(context.Context, string) (string, error)                        { return "", nil }
func (s *stubRedis) Set(context.Context, string, string, time.Duration) error           { return nil }
func (s *stubRedis) StoreCallbackURL(context.Context, string, string, time.Duration) error {
	return nil
}
func (s *stubRedis) GetCallbackURL(context.Context, string) (string, error) { return "", nil }
func (s *stubRedis) SetUserLastActivity(context.Context, string, time.Time, time.Duration) error {
	return nil
}
func (s *stubRedis) GetUserLastActivity(context.Context, string) (*time.Time, error) {
	return nil, nil
}
func (s *stubRedis) GetUserLastActivityTTL(context.Context, string) (time.Duration, error) {
	return 0, nil
}
func (s *stubRedis) Ping(context.Context) error { return nil }

// newTestHandler builds a callback handler whose auto-resume POSTs to the given URL.
func newTestHandler(redis RedisServiceInterface, enabled bool, resumeURL string) *GovBrCallbackHandler {
	logger := logrus.New()
	logger.SetOutput(io.Discard)
	cfg := &config.Config{}
	cfg.GovBr.AutoResumeEnabled = enabled
	cfg.GovBr.ResumeWebhookURL = resumeURL
	return &GovBrCallbackHandler{
		logger:            logger,
		config:            cfg,
		redisService:      redis,
		govbrRedisService: redis,
	}
}

func TestTriggerAutoResume_DisabledIsNoop(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		atomic.AddInt32(&hits, 1)
	}))
	defer srv.Close()

	redis := &stubRedis{setNXResult: true}
	h := newTestHandler(redis, false, srv.URL)
	h.triggerAutoResume("5521999999999", "multas")

	if got := atomic.LoadInt32(&hits); got != 0 {
		t.Fatalf("disabled auto-resume should not POST, got %d hits", got)
	}
	if got := atomic.LoadInt32(&redis.setNXCalls); got != 0 {
		t.Fatalf("disabled auto-resume should not touch Redis, got %d SetNX calls", got)
	}
}

func TestTriggerAutoResume_DedupSkipsWhenLockNotAcquired(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		atomic.AddInt32(&hits, 1)
	}))
	defer srv.Close()

	redis := &stubRedis{setNXResult: false} // already resumed
	h := newTestHandler(redis, true, srv.URL)
	h.triggerAutoResume("5521999999999", "multas")

	if got := atomic.LoadInt32(&hits); got != 0 {
		t.Fatalf("dedup should skip POST when lock not acquired, got %d hits", got)
	}
}

func TestTriggerAutoResume_FiresWithExpectedPayload(t *testing.T) {
	type body struct {
		UserNumber string `json:"user_number"`
		Message    string `json:"message"`
	}
	var got body
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &got)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	redis := &stubRedis{setNXResult: true}
	h := newTestHandler(redis, true, srv.URL)
	h.triggerAutoResume("5521999999999", "multas")

	if hits != 1 {
		t.Fatalf("expected exactly 1 POST, got %d", hits)
	}
	if got.UserNumber != "5521999999999" {
		t.Fatalf("expected user_number propagated, got %q", got.UserNumber)
	}
	if got.Message == "" || !strings.Contains(got.Message, "multas") {
		t.Fatalf("expected resume message to mention service context, got %q", got.Message)
	}
}

func TestTriggerAutoResume_SetNXErrorStillFires(t *testing.T) {
	// Best-effort contract: a transient Redis SETNX error must NOT block the
	// resume — it degrades to "may fire" rather than silently dropping it.
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	redis := &stubRedis{setNXResult: false, setNXErr: errStub}
	h := newTestHandler(redis, true, srv.URL)
	h.triggerAutoResume("5521999999999", "multas")

	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Fatalf("SETNX error should not block the resume POST, got %d hits", got)
	}
}
