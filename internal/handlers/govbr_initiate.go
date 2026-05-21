package handlers

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
)

const govBrInitiateSecretHeader = "X-Govbr-Initiate-Secret"

type govBrInitiateRequest struct {
	UserNumber     string `json:"user_number" binding:"required"`
	ServiceContext string `json:"service_context" binding:"required"`
}

type govBrInitiateResponse struct {
	AuthURL   string `json:"auth_url"`
	State     string `json:"state"`
	ExpiresIn int    `json:"expires_in"`
}

func (h *GovBrCallbackHandler) HandleInitiate(c *gin.Context) {
	// Auth FIRST: rota é trusted-internal (chamada pelo Engine/worker que conhecem
	// qual `user_number` está sendo atendido). Fail-closed se o secret não estiver
	// configurado — evita binding de tokens Gov.br pra `user_number` arbitrário
	// caso o Istio AuthorizationPolicy upstream falhe ou o gateway seja exposto
	// fora do mesh.
	expected := h.config.GovBr.InitiateSecret
	if expected == "" {
		h.logger.Warn("govbr_initiate: secret not configured (fail-closed)")
		c.JSON(http.StatusUnauthorized, gin.H{"error": "initiate secret not configured"})
		return
	}
	provided := c.GetHeader(govBrInitiateSecretHeader)
	if provided == "" || subtle.ConstantTimeCompare([]byte(provided), []byte(expected)) != 1 {
		h.logger.WithField("event", "govbr_initiate_unauthorized").Warn("govbr_initiate: missing/invalid secret header")
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	var req govBrInitiateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_request", "message": err.Error()})
		return
	}

	logger := h.logger.WithFields(logrus.Fields{
		"user":            maskPhoneNumber(req.UserNumber),
		"service_context": req.ServiceContext,
		"handler":         "govbr_initiate",
	})

	verifierBytes := make([]byte, 32)
	if _, err := rand.Read(verifierBytes); err != nil {
		logger.WithError(err).Error("Failed to generate code verifier")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal_error"})
		return
	}
	codeVerifier := base64.RawURLEncoding.EncodeToString(verifierBytes)

	sum := sha256.Sum256([]byte(codeVerifier))
	codeChallenge := base64.RawURLEncoding.EncodeToString(sum[:])

	state := uuid.New().String()

	authState := GovBrAuthState{
		UserNumber:     req.UserNumber,
		CodeVerifier:   codeVerifier,
		ServiceContext: req.ServiceContext,
		CreatedAt:      time.Now().UTC().Format(time.RFC3339),
		State:          "pending",
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
	defer cancel()

	stateJSON, err := json.Marshal(authState)
	if err != nil {
		logger.WithError(err).Error("Failed to marshal auth state")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal_error"})
		return
	}

	// Garante TTL positivo — sem default ou com valor inválido, evita
	// Set(..., 0) que go-redis trata como "sem expiração" (state OAuth
	// persistiria pra sempre, derrotando o expired-session path do callback).
	ttlSeconds := h.config.GovBr.AuthStateTTL
	if ttlSeconds <= 0 {
		ttlSeconds = 600 // 10min default
	}
	ttl := time.Duration(ttlSeconds) * time.Second
	redisKey := fmt.Sprintf("govbr_auth:%s", state)
	if err := h.redisService.Set(ctx, redisKey, string(stateJSON), ttl); err != nil {
		logger.WithError(err).Error("Failed to store auth state in Redis")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal_error"})
		return
	}

	params := url.Values{}
	params.Set("client_id", h.config.GovBr.ClientID)
	params.Set("redirect_uri", h.config.GovBr.RedirectURI)
	params.Set("response_type", "code")
	params.Set("scope", h.config.GovBr.Scope)
	params.Set("state", state)
	params.Set("code_challenge", codeChallenge)
	params.Set("code_challenge_method", "S256")

	authURL := h.config.GovBr.AuthURL + "?" + params.Encode()

	logger.WithField("state", state).Info("Gov.br auth session initiated")

	c.JSON(http.StatusOK, govBrInitiateResponse{
		AuthURL:   authURL,
		State:     state,
		ExpiresIn: ttlSeconds,
	})
}
