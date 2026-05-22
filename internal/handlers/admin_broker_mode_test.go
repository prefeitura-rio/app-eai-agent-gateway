package handlers

import (
	"encoding/json"
	stdio "io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"

	"github.com/prefeitura-rio/app-eai-agent-gateway/internal/config"
)

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
