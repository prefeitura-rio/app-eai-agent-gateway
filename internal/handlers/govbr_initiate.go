package handlers

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
)

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
	// 1. Authenticate request (Bearer token)
	authHeader := c.GetHeader("Authorization")
	expectedToken := fmt.Sprintf("Bearer %s", h.config.Callback.AuthToken)
	if authHeader == "" || authHeader != expectedToken {
		c.JSON(http.StatusUnauthorized, gin.H{
			"error":   "unauthorized",
			"message": "Missing or invalid Authorization header",
		})
		return
	}

	// 2. Parse request body
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

	// 3. Validate phone number format (E.164: 10-15 digits)
	cleaned := strings.TrimPrefix(req.UserNumber, "+")
	isValid := len(cleaned) >= 10 && len(cleaned) <= 15
	for _, r := range cleaned {
		if r < '0' || r > '9' {
			isValid = false
			break
		}
	}
	if !isValid {
		logger.Error("Invalid phone number format")
		c.JSON(http.StatusBadRequest, gin.H{
			"error":   "invalid_phone_number",
			"message": "Phone number must be in E.164 format (10-15 digits)",
		})
		return
	}

	// 4. Check rate limit (5 attempts per hour per user)
	ctx := c.Request.Context()
	rateKey := fmt.Sprintf("govbr_auth_rate:%s", req.UserNumber)
	countStr, _ := h.redisService.Get(ctx, rateKey)
	var count int
	fmt.Sscanf(countStr, "%d", &count)

	const maxAttempts = 5
	if count >= maxAttempts {
		logger.WithField("attempts", count).Warn("Rate limit exceeded")
		c.JSON(http.StatusTooManyRequests, gin.H{
			"error":   "rate_limit_exceeded",
			"message": "Too many authentication attempts. Please wait 1 hour.",
		})
		return
	}

	// Increment rate limit counter
	newCount := count + 1
	h.redisService.Set(ctx, rateKey, fmt.Sprintf("%d", newCount), 3600*time.Second)

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

	ttl := time.Duration(h.config.GovBr.AuthStateTTL) * time.Second
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
	params.Set("kc_idp_hint", "govbr") // Force use of Gov.br identity provider

	authURL := h.config.GovBr.AuthURL + "?" + params.Encode()

	logger.WithField("state", state).Info("Gov.br auth session initiated")

	c.JSON(http.StatusOK, govBrInitiateResponse{
		AuthURL:   authURL,
		State:     state,
		ExpiresIn: h.config.GovBr.AuthStateTTL,
	})
}
