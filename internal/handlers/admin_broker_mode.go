// Package handlers — admin endpoint /admin/broker-mode.
//
// Source of truth do master switch arquitetural Salesforce-broker vs Meta-direct:
// env var SALESFORCE_BROKER_ENABLED (Infisical). Outras camadas (Mule) consultam
// este endpoint a cada 30s pra propagação <1min sem redeploy.
//
// Auth via header X-Admin-Token comparado em constant-time com ADMIN_API_TOKEN.
// Fail-closed: token vazio/placeholder → 401 sempre.
package handlers

import (
	"crypto/subtle"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"

	"github.com/prefeitura-rio/app-eai-agent-gateway/internal/config"
)

const adminTokenHeader = "X-Admin-Token"

// AdminBrokerModeHandler — expõe GET /admin/broker-mode pra Mule pollar.
type AdminBrokerModeHandler struct {
	cfg    *config.BrokerConfig
	logger *logrus.Logger
	// startedAt fixa o tempo de boot do processo; updated_at no response
	// é tempo de boot, pois flag é estática-por-pod (mudou no Infisical →
	// pod restarta via Infisical agent → startedAt avança).
	startedAt time.Time
}

// NewAdminBrokerModeHandler constrói o handler com config + logger.
func NewAdminBrokerModeHandler(cfg *config.BrokerConfig, logger *logrus.Logger) *AdminBrokerModeHandler {
	return &AdminBrokerModeHandler{
		cfg:       cfg,
		logger:    logger,
		startedAt: time.Now().UTC(),
	}
}

// HandleGet responde JSON com o estado atual da flag.
//
//	GET /admin/broker-mode
//	Headers: X-Admin-Token: <ADMIN_API_TOKEN>
//	200 → {"salesforce_broker_enabled": true, "updated_at": "2026-05-22T..."}
//	401 → token ausente/inválido (fail-closed)
//	503 → endpoint sem token configurado (não habilitado neste deploy)
func (h *AdminBrokerModeHandler) HandleGet(c *gin.Context) {
	if h.cfg.AdminAPIToken == "" || h.cfg.AdminAPIToken == "REPLACE_VIA_RUNTIME_MANAGER_PROPERTIES" {
		h.logger.Warn("admin_broker_mode: ADMIN_API_TOKEN not configured; endpoint disabled")
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "admin endpoint not configured"})
		return
	}
	provided := c.GetHeader(adminTokenHeader)
	if provided == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "missing X-Admin-Token"})
		return
	}
	if subtle.ConstantTimeCompare([]byte(provided), []byte(h.cfg.AdminAPIToken)) != 1 {
		h.logger.WithField("event", "admin_broker_mode_unauthorized").
			Warn("admin_broker_mode: token mismatch")
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid X-Admin-Token"})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"salesforce_broker_enabled": h.cfg.SalesforceBrokerEnabled,
		"updated_at":                h.startedAt.Format(time.RFC3339),
	})
}
