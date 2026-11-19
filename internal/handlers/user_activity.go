package handlers

import (
	"context"
	"crypto/subtle"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"

	"github.com/prefeitura-rio/app-eai-agent-gateway/internal/config"
	"github.com/prefeitura-rio/app-eai-agent-gateway/internal/models"
	"github.com/prefeitura-rio/app-eai-agent-gateway/internal/pii"
	"github.com/prefeitura-rio/app-eai-agent-gateway/internal/services"
)

// UserActivityHandler handles user activity tracking endpoints
type UserActivityHandler struct {
	logger          *logrus.Logger
	config          *config.Config
	redisService    RedisServiceInterface
	postgresService *services.PostgresService
}

// LastActivityTokenHeader carries the dedicated secret that, once
// LAST_ACTIVITY_AUTH_TOKEN is configured, is required to query a user's last
// activity. It is separate from Authorization, which the Istio mesh consumes.
const LastActivityTokenHeader = "X-Last-Activity-Token"

// NewUserActivityHandler creates a new user activity handler
func NewUserActivityHandler(
	logger *logrus.Logger,
	config *config.Config,
	redisService RedisServiceInterface,
	postgresService *services.PostgresService,
) *UserActivityHandler {
	if config != nil && strings.TrimSpace(config.Security.LastActivityAuthToken) == "" {
		logger.Warn("LAST_ACTIVITY_AUTH_TOKEN is not set: GET /api/v1/message/last-activity has no caller authentication")
	}
	return &UserActivityHandler{
		logger:          logger,
		config:          config,
		redisService:    redisService,
		postgresService: postgresService,
	}
}

// HandleLastActivity retrieves the timestamp of user's last message
//
//	@Summary		Get user's last message timestamp
//	@Description	Returns the timestamp when the user last sent a message (cached in Redis, fallback to PostgreSQL)
//	@Tags			User Activity
//	@Produce		json
//	@Param			X-Last-Activity-Token	header		string								false	"Dedicated secret; required when LAST_ACTIVITY_AUTH_TOKEN is configured"
//	@Param			user_number				query		string								true	"User phone number (thread ID)"
//	@Success		200						{object}	models.UserLastActivityResponse		"Last activity timestamp found"
//	@Failure		400						{object}	map[string]interface{}				"Invalid request"
//	@Failure		401						{object}	map[string]interface{}				"Missing or invalid X-Last-Activity-Token"
//	@Failure		404						{object}	map[string]interface{}				"No message history found"
//	@Failure		500						{object}	map[string]interface{}				"Internal server error"
//	@Router			/api/v1/message/last-activity [get]
func (h *UserActivityHandler) HandleLastActivity(c *gin.Context) {
	if !h.callerAuthorized(c) {
		h.logger.WithField("request_id", c.GetString("request_id")).
			Warn("Rejected last activity request: missing or invalid " + LastActivityTokenHeader)
		c.JSON(http.StatusUnauthorized, gin.H{
			"error":   "Unauthorized",
			"message": "missing or invalid " + LastActivityTokenHeader + " header",
		})
		return
	}

	userNumber := c.Query("user_number")
	if userNumber == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"error":   "Missing parameter",
			"message": "user_number query parameter is required",
		})
		return
	}

	logger := h.logger.WithFields(logrus.Fields{
		"user_number": pii.MaskPhone(userNumber),
		"request_id":  c.GetString("request_id"),
	})

	logger.Debug("Handling user last activity request")

	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()

	// Try cache first
	timestamp, err := h.redisService.GetUserLastActivity(ctx, userNumber)
	cached := true

	// On cache miss, query PostgreSQL
	if err != nil || timestamp == nil {
		cached = false
		logger.Debug("Cache miss, querying PostgreSQL for last activity")

		timestamp, err = h.postgresService.GetLastMessageTimestamp(ctx, userNumber)
		if err != nil {
			logger.WithError(err).Warn("Failed to get last message timestamp from PostgreSQL")
			c.JSON(http.StatusNotFound, gin.H{
				"error":   "No message history found",
				"message": "No messages found for the provided user number",
			})
			return
		}

		// Store in cache for future requests
		ttl := h.config.Postgres.UserActivityTTL
		if cacheErr := h.redisService.SetUserLastActivity(ctx, userNumber, *timestamp, ttl); cacheErr != nil {
			logger.WithError(cacheErr).Warn("Failed to cache user last activity timestamp")
			// Don't fail the request for cache errors
		} else {
			logger.WithField("ttl", ttl).Debug("Cached user last activity timestamp")
		}
	} else {
		logger.Debug("Cache hit for user last activity")
	}

	// Default TTL is the configured value (used when the key was just written, or on fallback)
	ttlSeconds := int(h.config.Postgres.UserActivityTTL.Seconds())

	// When the value came from cache, query Redis for the actual remaining TTL so the
	// caller can observe the key expiring in real time instead of always seeing the
	// full configured TTL.
	if cached {
		remainingTTL, ttlErr := h.redisService.GetUserLastActivityTTL(ctx, userNumber)
		if ttlErr != nil {
			logger.WithError(ttlErr).Warn("Failed to get remaining TTL from Redis, falling back to configured TTL")
		} else if remainingTTL >= 0 {
			// remainingTTL == -1 means the key has no expiry; keep the configured value in that case.
			ttlSeconds = int(remainingTTL.Seconds())
		}
	}

	logger.WithFields(logrus.Fields{
		"timestamp":   timestamp,
		"cached":      cached,
		"ttl_seconds": ttlSeconds,
	}).Info("Returning user last activity")

	c.JSON(http.StatusOK, models.UserLastActivityResponse{
		UserNumber:           userNumber,
		LastMessageTimestamp: timestamp.Format(time.RFC3339),
		Cached:               cached,
		TTLSeconds:           ttlSeconds,
	})
}

// callerAuthorized reports whether the request may read a user's last
// activity. With LAST_ACTIVITY_AUTH_TOKEN unset every caller is allowed
// (behaviour before DEV-237); once set, the X-Last-Activity-Token header must
// match it, compared in constant time.
func (h *UserActivityHandler) callerAuthorized(c *gin.Context) bool {
	if h.config == nil {
		return true
	}
	expected := strings.TrimSpace(h.config.Security.LastActivityAuthToken)
	if expected == "" {
		return true
	}
	provided := c.GetHeader(LastActivityTokenHeader)
	if provided == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(provided), []byte(expected)) == 1
}
