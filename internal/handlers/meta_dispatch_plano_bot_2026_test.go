// Plano-bot-2026 Fase 0 — tests pra wiring C6 (auth-fail recorder), C4
// (audit logger), I8 (token budget) no MetaDispatchHandler. Arquivo
// separado pra não inflar meta_dispatch_test.go e pra isolar o escopo do plano.
package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	stdio "io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"

	"github.com/prefeitura-rio/app-eai-agent-gateway/internal/models"
	"github.com/prefeitura-rio/app-eai-agent-gateway/internal/services"
)

// ─── Mocks pros novos hooks ──────────────────────────────────────────────

type mockAuthFailRecorder struct {
	mu    sync.Mutex
	calls []string // IPs registrados
}

func (m *mockAuthFailRecorder) RecordAuthFailure(_ context.Context, ip string) (bool, time.Duration, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, ip)
	return false, 0, nil
}

func (m *mockAuthFailRecorder) callCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.calls)
}

type mockAuditLogger struct {
	mu      sync.Mutex
	entries []services.AuditEntry
}

func (m *mockAuditLogger) Log(_ context.Context, entry services.AuditEntry) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.entries = append(m.entries, entry)
}

func (m *mockAuditLogger) last() *services.AuditEntry {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.entries) == 0 {
		return nil
	}
	e := m.entries[len(m.entries)-1]
	return &e
}

type mockTokenBudget struct {
	mu       sync.Mutex
	addCalls []struct {
		e164   string
		tokens int
	}
}

func (m *mockTokenBudget) AddTokensSpent(_ context.Context, e164 string, tokens int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.addCalls = append(m.addCalls, struct {
		e164   string
		tokens int
	}{e164, tokens})
}

func (m *mockTokenBudget) lastCall() (string, int, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.addCalls) == 0 {
		return "", 0, false
	}
	c := m.addCalls[len(m.addCalls)-1]
	return c.e164, c.tokens, true
}

// ─── Helpers ─────────────────────────────────────────────────────────────

func newDispatchHandlerWithHooks(t *testing.T, sender MetaSender, redis RedisServiceInterface,
	authRec AuthFailureRecorder, audit AuditLogger, budget TokenBudgetRecorder,
) *MetaDispatchHandler {
	t.Helper()
	gin.SetMode(gin.TestMode)
	logger := logrus.New()
	logger.SetOutput(stdio.Discard)
	h := NewMetaDispatchHandler(sender, redis, testDispatchSecret, logger)
	if authRec != nil {
		h = h.WithAuthFailureRecorder(authRec)
	}
	if audit != nil {
		h = h.WithAuditLogger(audit)
	}
	if budget != nil {
		h = h.WithTokenBudget(budget)
	}
	return h
}

func postWithSecret(t *testing.T, h *MetaDispatchHandler, body interface{}, secret string) *httptest.ResponseRecorder {
	t.Helper()
	payload, _ := json.Marshal(body)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/meta/dispatch", bytes.NewReader(payload))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Request.Header.Set("X-Meta-Dispatch-Secret", secret)
	c.Request.RemoteAddr = "10.0.0.5:54321"
	h.HandleDispatch(c)
	return w
}

// ─── C6 — Auth failure recorder ──────────────────────────────────────────

func TestDispatch_AuthFailRecorder_TriggeredOnBadSecret(t *testing.T) {
	authRec := &mockAuthFailRecorder{}
	h := newDispatchHandlerWithHooks(t, &mockSender{}, &mockRedisGet{}, authRec, nil, nil)

	w := postWithSecret(t, h, MetaDispatchPayload{MessageID: "x", Status: "completed"}, "wrong-secret")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
	if authRec.callCount() != 1 {
		t.Errorf("expected AuthFailRecorder to fire once, got %d", authRec.callCount())
	}
}

func TestDispatch_AuthFailRecorder_NotTriggeredOnEmptyHeader(t *testing.T) {
	// Header vazio = lib desconfigurada / probe acidental. NÃO punir IP.
	authRec := &mockAuthFailRecorder{}
	h := newDispatchHandlerWithHooks(t, &mockSender{}, &mockRedisGet{}, authRec, nil, nil)

	w := postWithSecret(t, h, MetaDispatchPayload{MessageID: "x", Status: "completed"}, "")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 on empty secret, got %d", w.Code)
	}
	if authRec.callCount() != 0 {
		t.Errorf("expected NO auth fail record on empty header, got %d", authRec.callCount())
	}
}

func TestDispatch_AuthFailRecorder_NilSafe(t *testing.T) {
	// Sem recorder configurado, comportamento unchanged (não panic).
	h := newDispatchHandlerWithHooks(t, &mockSender{}, &mockRedisGet{}, nil, nil, nil)
	w := postWithSecret(t, h, MetaDispatchPayload{MessageID: "x", Status: "completed"}, "wrong")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
}

// ─── C4 — Audit logger emission on send ──────────────────────────────────

func TestDispatch_AuditLog_OnTextSend(t *testing.T) {
	sender := &mockSender{}
	redis := &mockSendClaimer{}
	redis.mockRedisGet.put("task:metadata:msg-A", `{"user_number":"5521989091014","provider":"google_agent_engine"}`)
	audit := &mockAuditLogger{}
	h := newDispatchHandlerWithHooks(t, sender, redis, nil, audit, nil)

	w := postWithSecret(t, h, MetaDispatchPayload{
		MessageID: "msg-A",
		Status:    string(models.TaskStatusCompleted),
		Data: map[string]interface{}{
			"messages": []interface{}{
				map[string]interface{}{"content": "Olá!", "role": "ai"},
			},
		},
	}, testDispatchSecret)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	entry := audit.last()
	if entry == nil {
		t.Fatal("expected audit log entry on send success")
	}
	if entry.ActionType != "meta_send_text" {
		t.Errorf("expected ActionType=meta_send_text, got %q", entry.ActionType)
	}
	if entry.Success != true {
		t.Errorf("expected Success=true")
	}
	if entry.RecipientHash == "" {
		t.Errorf("expected RecipientHash non-empty")
	}
	if entry.WAMID == "" {
		t.Errorf("expected WAMID populated from sender")
	}
	if entry.MessageID != "msg-A" {
		t.Errorf("expected MessageID=msg-A, got %q", entry.MessageID)
	}
	if entry.SnowflakeID == "" {
		t.Errorf("expected SnowflakeID populated")
	}
}

func TestDispatch_AuditLog_RecipientHashed_NotRaw(t *testing.T) {
	// Garantia LGPD: número cru não vaza no audit log.
	sender := &mockSender{}
	redis := &mockSendClaimer{}
	redis.mockRedisGet.put("task:metadata:msg-B", `{"user_number":"5521989091014","provider":"google_agent_engine"}`)
	audit := &mockAuditLogger{}
	h := newDispatchHandlerWithHooks(t, sender, redis, nil, audit, nil)

	postWithSecret(t, h, MetaDispatchPayload{
		MessageID: "msg-B",
		Status:    string(models.TaskStatusCompleted),
		Data: map[string]interface{}{
			"messages": []interface{}{map[string]interface{}{"content": "x", "role": "ai"}},
		},
	}, testDispatchSecret)
	entry := audit.last()
	if entry == nil {
		t.Fatal("audit entry missing")
	}
	if entry.RecipientHash == "5521989091014" {
		t.Errorf("RecipientHash leaked raw number")
	}
	// SHA256 hex = 64 chars
	if len(entry.RecipientHash) != 64 {
		t.Errorf("expected RecipientHash to be sha256 hex (64 chars), got %d (%q)", len(entry.RecipientHash), entry.RecipientHash)
	}
}

func TestDispatch_AuditLog_NotEmittedOnFailure(t *testing.T) {
	// Erro do sender → não emite audit success (e ainda não emitimos audit
	// failure pra reduzir log volume; opcional next iteration).
	sender := &mockSender{}
	sender.failErr = errSenderFail{}
	redis := &mockSendClaimer{}
	redis.mockRedisGet.put("task:metadata:msg-C", `{"user_number":"5521","provider":"google_agent_engine"}`)
	audit := &mockAuditLogger{}
	h := newDispatchHandlerWithHooks(t, sender, redis, nil, audit, nil)

	w := postWithSecret(t, h, MetaDispatchPayload{
		MessageID: "msg-C",
		Status:    string(models.TaskStatusCompleted),
		Data: map[string]interface{}{
			"messages": []interface{}{map[string]interface{}{"content": "x", "role": "ai"}},
		},
	}, testDispatchSecret)
	if w.Code != http.StatusBadGateway {
		t.Errorf("expected 502 on sender error, got %d", w.Code)
	}
	if audit.last() != nil {
		t.Errorf("did not expect audit entry on send failure; got %+v", audit.last())
	}
}

// errSenderFail — tipo erro pra triggering failure path do sender mock.
type errSenderFail struct{}

func (e errSenderFail) Error() string { return "simulated meta graph failure" }

// ─── I8 — Token budget update ────────────────────────────────────────────

func TestDispatch_TokenBudget_AccumulatesOnUsageStats(t *testing.T) {
	sender := &mockSender{}
	redis := &mockSendClaimer{}
	redis.mockRedisGet.put("task:metadata:msg-D", `{"user_number":"5521989091014","provider":"google_agent_engine"}`)
	budget := &mockTokenBudget{}
	h := newDispatchHandlerWithHooks(t, sender, redis, nil, nil, budget)

	postWithSecret(t, h, MetaDispatchPayload{
		MessageID: "msg-D",
		Status:    string(models.TaskStatusCompleted),
		Data: map[string]interface{}{
			"messages": []interface{}{
				map[string]interface{}{"content": "Resposta", "role": "ai"},
				map[string]interface{}{
					"message_type":      "usage_statistics",
					"prompt_tokens":     float64(100),
					"completion_tokens": float64(50),
					"total_tokens":      float64(150),
				},
			},
		},
	}, testDispatchSecret)

	e164, tokens, ok := budget.lastCall()
	if !ok {
		t.Fatal("expected token budget AddTokensSpent call")
	}
	if e164 != "5521989091014" {
		t.Errorf("expected e164=5521989091014, got %q", e164)
	}
	if tokens != 150 {
		t.Errorf("expected tokens=150, got %d", tokens)
	}
}

func TestDispatch_TokenBudget_NotCalledWithoutUsage(t *testing.T) {
	sender := &mockSender{}
	redis := &mockSendClaimer{}
	redis.mockRedisGet.put("task:metadata:msg-E", `{"user_number":"5521","provider":"google_agent_engine"}`)
	budget := &mockTokenBudget{}
	h := newDispatchHandlerWithHooks(t, sender, redis, nil, nil, budget)

	postWithSecret(t, h, MetaDispatchPayload{
		MessageID: "msg-E",
		Status:    string(models.TaskStatusCompleted),
		Data: map[string]interface{}{
			"messages": []interface{}{map[string]interface{}{"content": "ok", "role": "ai"}},
		},
	}, testDispatchSecret)

	if _, _, ok := budget.lastCall(); ok {
		t.Errorf("did not expect token budget call without usage_statistics")
	}
}
