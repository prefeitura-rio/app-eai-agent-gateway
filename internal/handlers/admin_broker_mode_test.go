package handlers

import (
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

	"github.com/prefeitura-rio/app-eai-agent-gateway/internal/config"
)

// adminAuthFailMock — mock pra AuthFailureRecorder usado nos testes do
// admin handler. Reuses padrão do meta_dispatch_plano_bot_2026_test.go
// mas isolado pra evitar shared state cross-test.
type adminAuthFailMock struct {
	mu    sync.Mutex
	calls []string
}

func (m *adminAuthFailMock) RecordAuthFailure(_ context.Context, ip string) (bool, time.Duration, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, ip)
	return false, 0, nil
}
func (m *adminAuthFailMock) callCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.calls)
}

func newAdminHandler(t *testing.T, cfg config.BrokerConfig) *AdminBrokerModeHandler {
	t.Helper()
	gin.SetMode(gin.TestMode)
	logger := logrus.New()
	logger.SetOutput(stdio.Discard)
	return NewAdminBrokerModeHandler(&cfg, logger)
}

func TestAdminBrokerMode_HappyPath(t *testing.T) {
	h := newAdminHandler(t, config.BrokerConfig{
		SalesforceBrokerEnabled: true,
		AdminAPIToken:           "sec",
	})
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/admin/broker-mode", nil)
	c.Request.Header.Set(adminTokenHeader, "sec")
	h.HandleGet(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if resp["salesforce_broker_enabled"] != true {
		t.Errorf("expected true, got %v", resp["salesforce_broker_enabled"])
	}
	if resp["updated_at"] == "" {
		t.Error("expected updated_at populated")
	}
}

func TestAdminBrokerMode_FalseStateReturned(t *testing.T) {
	h := newAdminHandler(t, config.BrokerConfig{
		SalesforceBrokerEnabled: false,
		AdminAPIToken:           "sec",
	})
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/admin/broker-mode", nil)
	c.Request.Header.Set(adminTokenHeader, "sec")
	h.HandleGet(c)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var resp map[string]interface{}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["salesforce_broker_enabled"] != false {
		t.Errorf("expected false, got %v", resp["salesforce_broker_enabled"])
	}
}

func TestAdminBrokerMode_MissingTokenReturns401(t *testing.T) {
	h := newAdminHandler(t, config.BrokerConfig{
		SalesforceBrokerEnabled: true,
		AdminAPIToken:           "sec",
	})
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/admin/broker-mode", nil)
	// sem header
	h.HandleGet(c)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}
}

func TestAdminBrokerMode_WrongTokenReturns401(t *testing.T) {
	h := newAdminHandler(t, config.BrokerConfig{
		SalesforceBrokerEnabled: true,
		AdminAPIToken:           "sec",
	})
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/admin/broker-mode", nil)
	c.Request.Header.Set(adminTokenHeader, "wrong")
	h.HandleGet(c)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}
}

func TestAdminBrokerMode_UnconfiguredTokenReturns503(t *testing.T) {
	// Token vazio → endpoint não-configurado pra esse deploy. 503 (não 401)
	// pra diferenciar "auth falhou" vs "feature off por config".
	h := newAdminHandler(t, config.BrokerConfig{
		SalesforceBrokerEnabled: true,
		AdminAPIToken:           "",
	})
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/admin/broker-mode", nil)
	c.Request.Header.Set(adminTokenHeader, "anything")
	h.HandleGet(c)
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", w.Code)
	}
}

func TestAdminBrokerMode_PlaceholderTokenReturns503(t *testing.T) {
	h := newAdminHandler(t, config.BrokerConfig{
		SalesforceBrokerEnabled: true,
		AdminAPIToken:           "REPLACE_VIA_RUNTIME_MANAGER_PROPERTIES",
	})
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/admin/broker-mode", nil)
	c.Request.Header.Set(adminTokenHeader, "REPLACE_VIA_RUNTIME_MANAGER_PROPERTIES")
	h.HandleGet(c)
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 (placeholder treated as unconfigured), got %d", w.Code)
	}
}

func TestAdminBrokerMode_EmptyHeaderReturns401WithSpecificMessage(t *testing.T) {
	// Constant-time path: header vazio cai no compare, retorna 401 com
	// reason="missing X-Admin-Token" (vs "invalid X-Admin-Token" pra valor
	// errado). Garante diferenciação semântica preservada pós-fix de timing leak.
	h := newAdminHandler(t, config.BrokerConfig{
		SalesforceBrokerEnabled: true,
		AdminAPIToken:           "sec",
	})
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/admin/broker-mode", nil)
	// sem header (provided == "")
	h.HandleGet(c)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
	var resp map[string]interface{}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if got, _ := resp["error"].(string); got != "missing X-Admin-Token" {
		t.Errorf("expected error=missing X-Admin-Token, got %q", got)
	}
}

func TestAdminBrokerMode_WrongTokenReturns401WithSpecificMessage(t *testing.T) {
	h := newAdminHandler(t, config.BrokerConfig{
		SalesforceBrokerEnabled: true,
		AdminAPIToken:           "sec",
	})
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/admin/broker-mode", nil)
	c.Request.Header.Set(adminTokenHeader, "wrong")
	h.HandleGet(c)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
	var resp map[string]interface{}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if got, _ := resp["error"].(string); got != "invalid X-Admin-Token" {
		t.Errorf("expected error=invalid X-Admin-Token, got %q", got)
	}
}

// Plano-bot-2026 Fase 0 C6 — auth-fail recorder integration.
// Garante que tentativas com token errado disparam o limiter.

func TestAdminBrokerMode_AuthFailRecorder_TriggeredOnBadToken(t *testing.T) {
	h := newAdminHandler(t, config.BrokerConfig{
		SalesforceBrokerEnabled: true,
		AdminAPIToken:           "sec",
	})
	rec := &adminAuthFailMock{}
	h = h.WithAuthFailureRecorder(rec)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/admin/broker-mode", nil)
	c.Request.RemoteAddr = "10.0.0.42:12345"
	c.Request.Header.Set(adminTokenHeader, "wrong")
	h.HandleGet(c)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
	if rec.callCount() != 1 {
		t.Errorf("expected auth-fail recorder triggered once; got %d", rec.callCount())
	}
}

func TestAdminBrokerMode_AuthFailRecorder_NotTriggeredOnEmptyHeader(t *testing.T) {
	// Header vazio = client desconfigurado / probe; não punir.
	h := newAdminHandler(t, config.BrokerConfig{
		SalesforceBrokerEnabled: true,
		AdminAPIToken:           "sec",
	})
	rec := &adminAuthFailMock{}
	h = h.WithAuthFailureRecorder(rec)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/admin/broker-mode", nil)
	c.Request.RemoteAddr = "10.0.0.42:12345"
	// sem header
	h.HandleGet(c)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
	if rec.callCount() != 0 {
		t.Errorf("expected NO auth-fail record on empty header; got %d", rec.callCount())
	}
}
