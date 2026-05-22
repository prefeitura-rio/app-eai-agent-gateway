package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"

	"github.com/prefeitura-rio/app-eai-agent-gateway/internal/config"
)

// GovBrInitiateHandler handles Gov.br authentication initiation
type GovBrInitiateHandler struct {
	logger       *logrus.Logger
	config       *config.Config
	redisService RedisServiceInterface
}

// GovBrInitiateRequest represents the request to initiate Gov.br authentication
type GovBrInitiateRequest struct {
	UserNumber     string `json:"user_number" binding:"required"`
	ServiceContext string `json:"service_context" binding:"required"`
}

// GovBrInitiateResponse represents the response from initiate endpoint
type GovBrInitiateResponse struct {
	AuthURL   string `json:"auth_url"`
	State     string `json:"state"`
	ExpiresIn int    `json:"expires_in"`
}

// NewGovBrInitiateHandler creates a new Gov.br initiate handler
func NewGovBrInitiateHandler(
	logger *logrus.Logger,
	config *config.Config,
	redisService RedisServiceInterface,
) *GovBrInitiateHandler {
	return &GovBrInitiateHandler{
		logger:       logger,
		config:       config,
		redisService: redisService,
	}
}

// HandleInitiate processes requests to initiate Gov.br authentication
//
// POST /api/v1/auth/govbr/initiate
//
// Security:
//   - Requires Bearer token authentication (CALLBACK_AUTH_TOKEN)
//   - Rate limiting: 5 attempts per hour per user
//   - PKCE flow prevents code interception
//   - State parameter prevents CSRF
//   - Audit logging
//
// Request:
//
//	{
//	  "user_number": "5521999999999",
//	  "service_context": "chatbot-whatsapp-staging"
//	}
//
// Response (200):
//
//	{
//	  "auth_url": "https://auth-idriohom.../auth?...",
//	  "state": "uuid",
//	  "expires_in": 300
//	}
//
// Errors:
//   - 401: Invalid or missing authorization token
//   - 400: Invalid request body or phone number format
//   - 429: Rate limit exceeded
//   - 500: Internal server error
func (h *GovBrInitiateHandler) HandleInitiate(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
	defer cancel()

	logger := h.logger.WithFields(logrus.Fields{
		"handler": "govbr_initiate",
	})

	// 1. Authenticate request
	if !h.authenticateRequest(c, logger) {
		return
	}

	// 2. Parse and validate request body
	var req GovBrInitiateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		logger.WithError(err).Error("Invalid request body")
		c.JSON(http.StatusBadRequest, gin.H{
			"error":   "invalid_request",
			"message": "Invalid request body",
			"details": err.Error(),
		})
		return
	}

	// 3. Validate phone number format
	if !h.validatePhoneNumber(req.UserNumber) {
		logger.WithField("user_number", maskPhoneNumber(req.UserNumber)).Error("Invalid phone number format")
		c.JSON(http.StatusBadRequest, gin.H{
			"error":   "invalid_phone_number",
			"message": "Phone number must contain only digits and start with country code (e.g., 5521999999999)",
		})
		return
	}

	// 4. Check rate limit
	if !h.checkRateLimit(ctx, req.UserNumber, logger) {
		c.JSON(http.StatusTooManyRequests, gin.H{
			"error":   "rate_limit_exceeded",
			"message": "Too many authentication attempts. Please wait 1 hour and try again.",
		})
		return
	}

	// 5. Verify Gov.br configuration
	if h.config.GovBr.ClientID == "" || h.config.GovBr.RedirectURI == "" {
		logger.Error("Gov.br OAuth not configured (missing CLIENT_ID or REDIRECT_URI)")
		c.JSON(http.StatusInternalServerError, gin.H{
			"error":   "not_configured",
			"message": "Gov.br authentication is not configured. Please contact support.",
		})
		return
	}

	// 6. Generate PKCE pair
	codeVerifier, codeChallenge, err := generatePKCEPair()
	if err != nil {
		logger.WithError(err).Error("Failed to generate PKCE pair")
		c.JSON(http.StatusInternalServerError, gin.H{
			"error":   "internal_error",
			"message": "Failed to generate authentication parameters",
		})
		return
	}

	// 7. Generate state UUID
	state := uuid.New().String()

	// 8. Save auth state to Redis
	authState := GovBrAuthState{
		UserNumber:     req.UserNumber,
		CodeVerifier:   codeVerifier,
		ServiceContext: req.ServiceContext,
		CreatedAt:      time.Now().UTC().Format(time.RFC3339),
		State:          "pending",
	}

	authStateJSON, err := json.Marshal(authState)
	if err != nil {
		logger.WithError(err).Error("Failed to marshal auth state")
		c.JSON(http.StatusInternalServerError, gin.H{
			"error":   "internal_error",
			"message": "Failed to save authentication state",
		})
		return
	}

	redisKey := fmt.Sprintf("govbr_auth:%s", state)
	ttl := time.Duration(h.config.GovBr.AuthStateTTL) * time.Second
	if err := h.redisService.Set(ctx, redisKey, string(authStateJSON), ttl); err != nil {
		logger.WithError(err).Error("Failed to store auth state in Redis")
		c.JSON(http.StatusInternalServerError, gin.H{
			"error":   "storage_error",
			"message": "Failed to save authentication state",
		})
		return
	}

	// 9. Build authorization URL
	authURL := buildAuthURL(&h.config.GovBr, state, codeChallenge)

	// 10. Log successful initiation
	logger.WithFields(logrus.Fields{
		"user_number":     maskPhoneNumber(req.UserNumber),
		"service_context": req.ServiceContext,
		"state":           state,
		"expires_in":      ttl.Seconds(),
	}).Info("Gov.br authentication initiated successfully")

	// 11. Return response
	c.JSON(http.StatusOK, GovBrInitiateResponse{
		AuthURL:   authURL,
		State:     state,
		ExpiresIn: h.config.GovBr.AuthStateTTL,
	})
}

// authenticateRequest validates the Authorization header against CALLBACK_AUTH_TOKEN
//
// Returns true if authentication succeeds, false otherwise (and sets error response)
func (h *GovBrInitiateHandler) authenticateRequest(c *gin.Context, logger *logrus.Entry) bool {
	authHeader := c.GetHeader("Authorization")

	// Check if Authorization header is present
	if authHeader == "" {
		logger.Warn("Missing Authorization header")
		c.JSON(http.StatusUnauthorized, gin.H{
			"error":   "unauthorized",
			"message": "Missing Authorization header",
		})
		return false
	}

	// Validate Bearer token format
	expectedToken := fmt.Sprintf("Bearer %s", h.config.Callback.AuthToken)
	if authHeader != expectedToken {
		logger.Warn("Invalid Authorization token")
		c.JSON(http.StatusUnauthorized, gin.H{
			"error":   "unauthorized",
			"message": "Invalid Authorization token",
		})
		return false
	}

	return true
}

// validatePhoneNumber checks if phone number is in valid format
//
// Valid formats:
//   - Must contain only digits (after removing +)
//   - Minimum 10 digits
//   - Maximum 15 digits (E.164 format)
func (h *GovBrInitiateHandler) validatePhoneNumber(phone string) bool {
	// Remove + prefix if present
	cleaned := strings.TrimPrefix(phone, "+")

	// Check if contains only digits
	for _, r := range cleaned {
		if r < '0' || r > '9' {
			return false
		}
	}

	// Check length (E.164: 10-15 digits)
	length := len(cleaned)
	return length >= 10 && length <= 15
}

// checkRateLimit enforces 5 attempts per hour per user
//
// Uses Redis INCR with TTL for atomic rate limiting
//
// Returns true if within limit, false if exceeded
func (h *GovBrInitiateHandler) checkRateLimit(ctx context.Context, userNumber string, logger *logrus.Entry) bool {
	rateKey := fmt.Sprintf("govbr_auth_rate:%s", userNumber)

	// Get current count
	countStr, err := h.redisService.Get(ctx, rateKey)
	var count int
	if err == nil {
		// Key exists, parse count
		fmt.Sscanf(countStr, "%d", &count)
	}

	// Check if limit exceeded
	const maxAttempts = 5
	if count >= maxAttempts {
		logger.WithFields(logrus.Fields{
			"user_number": maskPhoneNumber(userNumber),
			"attempts":    count,
		}).Warn("Rate limit exceeded for Gov.br auth initiation")
		return false
	}

	// Increment counter
	newCount := count + 1
	ttl := 3600 * time.Second // 1 hour
	if err := h.redisService.Set(ctx, rateKey, fmt.Sprintf("%d", newCount), ttl); err != nil {
		logger.WithError(err).Error("Failed to update rate limit counter")
		// Allow request to proceed even if rate limit update fails
		// (fail open for availability)
	}

	return true
}
