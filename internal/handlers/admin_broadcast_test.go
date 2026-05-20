package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	stdio "io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"
)

const testBroadcastSecret = "test-broadcast-secret"

type mockBroadcastSender struct {
	mu        sync.Mutex
	calls     []struct {
		recipient, name, lang string
		components            []map[string]interface{}
	}
	failPhone string // se phone == failPhone, retorna erro
	failErr   error
}

func (m *mockBroadcastSender) SendTemplate(_ context.Context, recipient, name, langCode string, comps []map[string]interface{}) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, struct {
		recipient, name, lang string
		components            []map[string]interface{}
	}{recipient, name, langCode, comps})
	if m.failPhone != "" && recipient == m.failPhone {
		if m.failErr != nil {
			return "", m.failErr
		}
		return "", errors.New("simulated send failure")
	}
	return "wamid.BROAD." + recipient, nil
}

func newBroadcastHandler(t *testing.T, sender BroadcastSender, secret string) *AdminBroadcastHandler {
	t.Helper()
	gin.SetMode(gin.TestMode)
	logger := logrus.New()
	logger.SetOutput(stdio.Discard)
	return NewAdminBroadcastHandler(sender, secret, logger)
}

func postBroadcast(t *testing.T, h *AdminBroadcastHandler, secret string, body interface{}) *httptest.ResponseRecorder {
	t.Helper()
	payload, _ := json.Marshal(body)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/admin/broadcast", bytes.NewReader(payload))
	c.Request.Header.Set("Content-Type", "application/json")
	if secret != "" {
		c.Request.Header.Set("X-Broadcast-Secret", secret)
	}
	h.HandleBroadcast(c)
	return w
}

// ─── Auth tests ───────────────────────────────────────────────────────

func TestBroadcast_RejectsMissingSecret(t *testing.T) {
	h := newBroadcastHandler(t, &mockBroadcastSender{}, testBroadcastSecret)
	w := postBroadcast(t, h, "", BroadcastRequest{Items: []BroadcastItem{{Phone: "5521", TemplateName: "t", LanguageCode: "pt_BR"}}})
	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 (missing secret), got %d", w.Code)
	}
}

func TestBroadcast_RejectsWrongSecret(t *testing.T) {
	h := newBroadcastHandler(t, &mockBroadcastSender{}, testBroadcastSecret)
	w := postBroadcast(t, h, "wrong", BroadcastRequest{Items: []BroadcastItem{{Phone: "5521", TemplateName: "t", LanguageCode: "pt_BR"}}})
	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 (wrong secret), got %d", w.Code)
	}
}

func TestBroadcast_RejectsEmptyConfiguredSecret(t *testing.T) {
	// Handler with empty secret → fail-closed mesmo se caller passa empty.
	h := newBroadcastHandler(t, &mockBroadcastSender{}, "")
	w := postBroadcast(t, h, "", BroadcastRequest{Items: []BroadcastItem{{Phone: "5521", TemplateName: "t", LanguageCode: "pt_BR"}}})
	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 (fail-closed), got %d", w.Code)
	}
}

func TestBroadcast_RejectsPlaceholderSecret(t *testing.T) {
	h := newBroadcastHandler(t, &mockBroadcastSender{}, "REPLACE_VIA_RUNTIME_MANAGER_PROPERTIES")
	w := postBroadcast(t, h, "REPLACE_VIA_RUNTIME_MANAGER_PROPERTIES", BroadcastRequest{Items: []BroadcastItem{{Phone: "5521", TemplateName: "t", LanguageCode: "pt_BR"}}})
	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 (placeholder secret), got %d", w.Code)
	}
}

func TestBroadcast_NilSenderReturns503(t *testing.T) {
	h := newBroadcastHandler(t, nil, testBroadcastSecret)
	w := postBroadcast(t, h, testBroadcastSecret, BroadcastRequest{Items: []BroadcastItem{{Phone: "5521", TemplateName: "t", LanguageCode: "pt_BR"}}})
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 (no sender), got %d", w.Code)
	}
}

// ─── Validation tests ─────────────────────────────────────────────────

func TestBroadcast_InvalidJSON(t *testing.T) {
	h := newBroadcastHandler(t, &mockBroadcastSender{}, testBroadcastSecret)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/admin/broadcast", bytes.NewReader([]byte("not-json")))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Request.Header.Set("X-Broadcast-Secret", testBroadcastSecret)
	h.HandleBroadcast(c)
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestBroadcast_EmptyItemsRejected(t *testing.T) {
	h := newBroadcastHandler(t, &mockBroadcastSender{}, testBroadcastSecret)
	w := postBroadcast(t, h, testBroadcastSecret, BroadcastRequest{Items: []BroadcastItem{}})
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 (empty items), got %d", w.Code)
	}
}

func TestBroadcast_TooManyItemsRejected(t *testing.T) {
	h := newBroadcastHandler(t, &mockBroadcastSender{}, testBroadcastSecret)
	items := make([]BroadcastItem, broadcastMaxItems+1)
	for i := range items {
		items[i] = BroadcastItem{Phone: "5521", TemplateName: "t", LanguageCode: "pt_BR"}
	}
	w := postBroadcast(t, h, testBroadcastSecret, BroadcastRequest{Items: items, RatePerSecond: 100})
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 (too many items), got %d", w.Code)
	}
}

func TestBroadcast_BatchTooLargeForTimeout(t *testing.T) {
	// 100 items / rate=1 = 100s estimated, write_timeout 30s default →
	// rejeitar pra evitar timeout silencioso + retry duplicado.
	h := newBroadcastHandler(t, &mockBroadcastSender{}, testBroadcastSecret)
	items := make([]BroadcastItem, 100)
	for i := range items {
		items[i] = BroadcastItem{Phone: "5521", TemplateName: "t", LanguageCode: "pt_BR"}
	}
	w := postBroadcast(t, h, testBroadcastSecret, BroadcastRequest{Items: items, RatePerSecond: 1})
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 (estimated > timeout), got %d body=%s", w.Code, w.Body.String())
	}
}

// ─── Dispatch tests ───────────────────────────────────────────────────

func TestBroadcast_HappyPath_SingleItem(t *testing.T) {
	// Sync POC: 1 item por request (max). Operador faz N requests pra
	// broadcast em massa; async dispatch é out-of-scope.
	sender := &mockBroadcastSender{}
	h := newBroadcastHandler(t, sender, testBroadcastSecret)
	w := postBroadcast(t, h, testBroadcastSecret, BroadcastRequest{
		Items: []BroadcastItem{
			{Phone: "5521001", TemplateName: "saudacao", LanguageCode: "pt_BR"},
		},
		RatePerSecond: 100,
	})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	var result BroadcastResult
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatalf("parse result: %v", err)
	}
	if result.Total != 1 || result.Succeeded != 1 || result.Failed != 0 {
		t.Errorf("expected 1/1/0, got %+v", result)
	}
}

func TestBroadcast_SingleFailure(t *testing.T) {
	sender := &mockBroadcastSender{failPhone: "5521002"}
	h := newBroadcastHandler(t, sender, testBroadcastSecret)
	w := postBroadcast(t, h, testBroadcastSecret, BroadcastRequest{
		Items: []BroadcastItem{
			{Phone: "5521002", TemplateName: "t", LanguageCode: "pt_BR"},
		},
		RatePerSecond: 100,
	})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var result BroadcastResult
	_ = json.Unmarshal(w.Body.Bytes(), &result)
	if result.Failed != 1 || result.Succeeded != 0 {
		t.Errorf("expected 0/1, got %+v", result)
	}
}

func TestBroadcast_PerItemValidationWholeRequest400(t *testing.T) {
	// Item com template_name vazio. Gin ShouldBindJSON respeita binding
	// tag "required" no struct field — rejeita o request inteiro 400.
	// Esse é o comportamento defensivo correto: sender NÃO deve receber
	// chamada pra item parcialmente inválido (evita parcial broadcast
	// inesperado).
	sender := &mockBroadcastSender{}
	h := newBroadcastHandler(t, sender, testBroadcastSecret)
	w := postBroadcast(t, h, testBroadcastSecret, BroadcastRequest{
		Items: []BroadcastItem{
			{Phone: "5521001", TemplateName: "valid", LanguageCode: "pt_BR"},
			{Phone: "5521002", TemplateName: "", LanguageCode: "pt_BR"},
		},
		RatePerSecond: 100,
	})
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 (item required field violation), got %d", w.Code)
	}
	// Crítico: sender NÃO deve ter sido chamado se request rejeitado
	if len(sender.calls) != 0 {
		t.Errorf("expected zero sender calls on validation failure, got %d", len(sender.calls))
	}
}

// ─── Rate limit tests ─────────────────────────────────────────────────

func TestBroadcast_RateLimitDefault(t *testing.T) {
	// Quando rate=0, deve usar default. Não testa exatamente o timing,
	// apenas confirma que não loops infinitos (test framework com timeout).
	sender := &mockBroadcastSender{}
	h := newBroadcastHandler(t, sender, testBroadcastSecret)
	w := postBroadcast(t, h, testBroadcastSecret, BroadcastRequest{
		Items: []BroadcastItem{{Phone: "5521", TemplateName: "t", LanguageCode: "pt_BR"}},
	})
	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
}

// ─── Helpers ──────────────────────────────────────────────────────────

func TestMaskPhonePublic(t *testing.T) {
	cases := []struct{ in, want string }{
		{"5521989091014", "55219890…"},
		{"5500", "5500"},
		{"", ""},
	}
	for _, tc := range cases {
		if got := maskPhonePublic(tc.in); got != tc.want {
			t.Errorf("maskPhonePublic(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
