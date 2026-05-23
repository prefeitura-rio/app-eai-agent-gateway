// Plano-bot-2026 Fase 0 — middleware tests pra rate limit + token budget gate.
package middleware

import (
	"bytes"
	"context"
	"encoding/json"
	stdio "io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"
)

// ─── Mocks ──────────────────────────────────────────────────────────────

type mockLimiter struct {
	allowResult bool
	retryAfter  time.Duration
	count       int
	err         error
	// IP block state
	ipBlocked  bool
	ipRetryAfter time.Duration
	ipErr      error

	mu        sync.Mutex
	allowCalls int
	ipCalls    int
}

func (m *mockLimiter) AllowMessage(_ context.Context, _ string) (bool, time.Duration, int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.allowCalls++
	return m.allowResult, m.retryAfter, m.count, m.err
}

func (m *mockLimiter) IsIPBlocked(_ context.Context, _ string) (bool, time.Duration, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ipCalls++
	return m.ipBlocked, m.ipRetryAfter, m.ipErr
}

type mockBudget struct {
	exceeded bool
	total    int
	cap      int
	mu       sync.Mutex
	calls    int
}

func (m *mockBudget) IsBudgetExceeded(_ context.Context, _ string) (bool, int, int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls++
	return m.exceeded, m.total, m.cap
}

// ─── Helpers ────────────────────────────────────────────────────────────

func testLogger() *logrus.Logger {
	l := logrus.New()
	l.SetOutput(stdio.Discard)
	return l
}

func setupRouter(mw gin.HandlerFunc) (*gin.Engine, *bytes.Buffer) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	calledBuf := &bytes.Buffer{}
	if mw != nil {
		r.Use(mw)
	}
	r.POST("/api/v1/message/webhook/user", func(c *gin.Context) {
		calledBuf.WriteString("HANDLER_CALLED")
		c.JSON(http.StatusAccepted, gin.H{"ok": true})
	})
	return r, calledBuf
}

func doRequest(r *gin.Engine, body interface{}) *httptest.ResponseRecorder {
	payload, _ := json.Marshal(body)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/message/webhook/user", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	return w
}

// ─── Tests: DoSMessageRateLimitMiddleware ───────────────────────────────

func TestDoSMessageRateLimit_AllowsUnderLimit(t *testing.T) {
	limiter := &mockLimiter{allowResult: true}
	r, called := setupRouter(DoSMessageRateLimitMiddleware(limiter, nil, testLogger()))
	w := doRequest(r, map[string]interface{}{"user_number": "5521989091014", "message": "oi"})
	if w.Code != http.StatusAccepted {
		t.Fatalf("expected handler to run (202), got %d", w.Code)
	}
	if !strings.Contains(called.String(), "HANDLER_CALLED") {
		t.Errorf("handler should be reached when under limit")
	}
}

func TestDoSMessageRateLimit_BlocksAtLimit(t *testing.T) {
	limiter := &mockLimiter{allowResult: false, retryAfter: 600 * time.Second, count: 31}
	r, called := setupRouter(DoSMessageRateLimitMiddleware(limiter, nil, testLogger()))
	w := doRequest(r, map[string]interface{}{"user_number": "5521989091014", "message": "oi"})
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d", w.Code)
	}
	if strings.Contains(called.String(), "HANDLER_CALLED") {
		t.Errorf("handler should NOT be reached when over limit")
	}
	if w.Header().Get("Retry-After") == "" {
		t.Errorf("expected Retry-After header")
	}
}

func TestDoSMessageRateLimit_SkipsNonPOST(t *testing.T) {
	limiter := &mockLimiter{}
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(DoSMessageRateLimitMiddleware(limiter, nil, testLogger()))
	r.GET("/test", func(c *gin.Context) { c.String(200, "ok") })
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/test", nil))
	if w.Code != 200 {
		t.Errorf("expected 200 GET passthrough, got %d", w.Code)
	}
	if limiter.allowCalls != 0 {
		t.Errorf("expected no limiter calls on GET, got %d", limiter.allowCalls)
	}
}

func TestDoSMessageRateLimit_AllowsOnBadJSON(t *testing.T) {
	// Body malformado: handler downstream retorna 400. Middleware NÃO
	// rejeita por isso (sem identificador pra rate-limit).
	limiter := &mockLimiter{}
	r, called := setupRouter(DoSMessageRateLimitMiddleware(limiter, nil, testLogger()))
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/message/webhook/user", strings.NewReader("not json"))
	r.ServeHTTP(w, req)
	if !strings.Contains(called.String(), "HANDLER_CALLED") {
		t.Errorf("handler should be reached on malformed body (will 400 itself); got body=%q", w.Body.String())
	}
}

// ─── Tests: Token budget enforcement gate ──────────────────────────────

func TestTokenBudget_BlocksWhenExceeded(t *testing.T) {
	limiter := &mockLimiter{allowResult: true}
	budget := &mockBudget{exceeded: true, total: 100000, cap: 100000}
	r, called := setupRouter(DoSMessageRateLimitMiddleware(limiter, budget, testLogger()))
	w := doRequest(r, map[string]interface{}{"user_number": "5521989091014", "message": "oi"})
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429 when budget exceeded, got %d", w.Code)
	}
	if strings.Contains(called.String(), "HANDLER_CALLED") {
		t.Errorf("handler should NOT be reached when budget exceeded")
	}
	if w.Header().Get("X-Budget-Exceeded") != "true" {
		t.Errorf("expected X-Budget-Exceeded:true header; got %q", w.Header().Get("X-Budget-Exceeded"))
	}
	if w.Header().Get("Retry-After") != "86400" {
		t.Errorf("expected Retry-After=86400 (1 day) for daily budget reset; got %q", w.Header().Get("Retry-After"))
	}
	// Body contains escalate signal
	var body map[string]interface{}
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if body["escalate_to_handoff"] != true {
		t.Errorf("expected escalate_to_handoff=true in body, got %v", body["escalate_to_handoff"])
	}
}

func TestTokenBudget_AllowsWhenNotExceeded(t *testing.T) {
	limiter := &mockLimiter{allowResult: true}
	budget := &mockBudget{exceeded: false, total: 50000, cap: 100000}
	r, called := setupRouter(DoSMessageRateLimitMiddleware(limiter, budget, testLogger()))
	w := doRequest(r, map[string]interface{}{"user_number": "5521989091014", "message": "oi"})
	if w.Code != http.StatusAccepted {
		t.Fatalf("expected 202 when budget not exceeded, got %d", w.Code)
	}
	if !strings.Contains(called.String(), "HANDLER_CALLED") {
		t.Errorf("handler should be reached when budget not exceeded")
	}
}

func TestTokenBudget_NilCheckerSkipsGate(t *testing.T) {
	// budget=nil → gate completamente skipped (feature flag OFF compat).
	limiter := &mockLimiter{allowResult: true}
	r, called := setupRouter(DoSMessageRateLimitMiddleware(limiter, nil, testLogger()))
	w := doRequest(r, map[string]interface{}{"user_number": "5521989091014", "message": "oi"})
	if w.Code != http.StatusAccepted {
		t.Fatalf("expected handler to run with nil budget, got %d", w.Code)
	}
	if !strings.Contains(called.String(), "HANDLER_CALLED") {
		t.Errorf("handler should be reached when no budget configured")
	}
}

func TestTokenBudget_GateRunsBeforeRateLimit(t *testing.T) {
	// Quando budget excedido, NÃO chamamos AllowMessage (não inflar
	// per-user counter sem motivo).
	limiter := &mockLimiter{allowResult: true}
	budget := &mockBudget{exceeded: true}
	r, _ := setupRouter(DoSMessageRateLimitMiddleware(limiter, budget, testLogger()))
	_ = doRequest(r, map[string]interface{}{"user_number": "5521989091014", "message": "oi"})
	if limiter.allowCalls != 0 {
		t.Errorf("limiter.AllowMessage should NOT be called when budget exceeded; got %d calls", limiter.allowCalls)
	}
	if budget.calls != 1 {
		t.Errorf("budget check should fire once; got %d", budget.calls)
	}
}

// ─── Tests: DoSAuthFailureGateMiddleware ────────────────────────────────

func TestDoSAuthFailureGate_BlocksWhenIPBlocked(t *testing.T) {
	limiter := &mockLimiter{ipBlocked: true, ipRetryAfter: 600 * time.Second}
	gin.SetMode(gin.TestMode)
	r := gin.New()
	called := false
	r.Use(DoSAuthFailureGateMiddleware(limiter, testLogger()))
	r.POST("/admin/anything", func(c *gin.Context) {
		called = true
		c.String(200, "ok")
	})
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/admin/anything", nil)
	req.RemoteAddr = "10.0.0.1:54321"
	r.ServeHTTP(w, req)
	if w.Code != http.StatusTooManyRequests {
		t.Errorf("expected 429 when IP blocked, got %d", w.Code)
	}
	if called {
		t.Errorf("handler should NOT be reached when IP blocked")
	}
}

func TestDoSAuthFailureGate_AllowsWhenIPNotBlocked(t *testing.T) {
	limiter := &mockLimiter{ipBlocked: false}
	gin.SetMode(gin.TestMode)
	r := gin.New()
	called := false
	r.Use(DoSAuthFailureGateMiddleware(limiter, testLogger()))
	r.POST("/admin/anything", func(c *gin.Context) {
		called = true
		c.String(200, "ok")
	})
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/admin/anything", nil)
	req.RemoteAddr = "10.0.0.1:54321"
	r.ServeHTTP(w, req)
	if !called {
		t.Errorf("handler should run when IP not blocked")
	}
}
