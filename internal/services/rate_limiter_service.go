package services

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/prefeitura-rio/app-eai-agent-gateway/internal/config"
)

// RateLimiterService implements rate limiting using Redis sliding window
type RateLimiterService struct {
	config       *config.Config
	logger       *logrus.Logger
	redisService RedisServiceInterface
}

// NewRateLimiterService creates a new rate limiter service
func NewRateLimiterService(
	cfg *config.Config,
	logger *logrus.Logger,
	redisService RedisServiceInterface,
) *RateLimiterService {
	return &RateLimiterService{
		config:       cfg,
		logger:       logger,
		redisService: redisService,
	}
}

// Allow checks if a request is allowed under the rate limit
func (r *RateLimiterService) Allow(ctx context.Context, key string) (bool, error) {
	if !r.config.GoogleCloud.RateLimitEnabled {
		return true, nil
	}

	// Get current count
	count, err := r.getCurrentCount(ctx, key)
	if err != nil {
		r.logger.WithError(err).WithField("key", key).Error("Failed to get current rate limit count")
		return false, err
	}

	allowed := count < r.config.GoogleCloud.MaxRequestsPerMinute

	if allowed {
		// Increment the count
		if err := r.incrementCount(ctx, key); err != nil {
			r.logger.WithError(err).WithField("key", key).Error("Failed to increment rate limit count")
			return false, err
		}
	}

	r.logger.WithFields(logrus.Fields{
		"key":     key,
		"count":   count,
		"limit":   r.config.GoogleCloud.MaxRequestsPerMinute,
		"allowed": allowed,
	}).Debug("Rate limit check")

	return allowed, nil
}

// Wait waits until a request can be made under the rate limit
func (r *RateLimiterService) Wait(ctx context.Context, key string) error {
	if !r.config.GoogleCloud.RateLimitEnabled {
		return nil
	}

	backoffSeconds := float64(r.config.GoogleCloud.MinBackoffSeconds)
	maxRetries := 10 // Prevent infinite loops

	for attempt := 0; attempt < maxRetries; attempt++ {
		allowed, err := r.Allow(ctx, key)
		if err != nil {
			return err
		}

		if allowed {
			return nil
		}

		// Calculate backoff duration
		backoffDuration := time.Duration(backoffSeconds) * time.Second
		if backoffDuration > time.Duration(r.config.GoogleCloud.MaxBackoffSeconds)*time.Second {
			backoffDuration = time.Duration(r.config.GoogleCloud.MaxBackoffSeconds) * time.Second
		}

		r.logger.WithFields(logrus.Fields{
			"key":             key,
			"attempt":         attempt + 1,
			"backoff_seconds": backoffSeconds,
			"max_retries":     maxRetries,
		}).Debug("Rate limit exceeded, backing off")

		// Wait for backoff duration
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoffDuration):
			// Continue to next attempt
		}

		// Exponential backoff
		backoffSeconds = math.Min(
			backoffSeconds*r.config.GoogleCloud.BackoffMultiplier,
			float64(r.config.GoogleCloud.MaxBackoffSeconds),
		)
	}

	return fmt.Errorf("rate limit exceeded after %d attempts for key: %s", maxRetries, key)
}

// getCurrentCount gets the current request count for the key in the current minute window
func (r *RateLimiterService) getCurrentCount(ctx context.Context, key string) (int, error) {
	// Use current minute as the window
	now := time.Now()
	windowKey := fmt.Sprintf("rate_limit:%s:%d", key, now.Unix()/60)

	countStr, err := r.redisService.Get(ctx, windowKey)
	if err != nil {
		// If key doesn't exist, count is 0
		return 0, nil
	}

	count, err := strconv.Atoi(countStr)
	if err != nil {
		r.logger.WithError(err).WithField("window_key", windowKey).Warn("Invalid rate limit count in Redis, resetting to 0")
		return 0, nil
	}

	return count, nil
}

// getCurrentCountForUsage gets the current request count and distinguishes between missing keys and Redis errors
func (r *RateLimiterService) getCurrentCountForUsage(ctx context.Context, key string) (int, error) {
	// Use current minute as the window
	now := time.Now()
	windowKey := fmt.Sprintf("rate_limit:%s:%d", key, now.Unix()/60)

	countStr, err := r.redisService.Get(ctx, windowKey)
	if err != nil {
		// For usage queries, we want to propagate Redis errors but treat missing keys as 0
		if err.Error() == "key not found" {
			return 0, nil
		}
		// Actual Redis connection/communication errors should be propagated
		return 0, err
	}

	count, err := strconv.Atoi(countStr)
	if err != nil {
		r.logger.WithError(err).WithField("window_key", windowKey).Warn("Invalid rate limit count in Redis, resetting to 0")
		return 0, nil
	}

	return count, nil
}

// incrementCount increments the request count for the current minute window
func (r *RateLimiterService) incrementCount(ctx context.Context, key string) error {
	// Use current minute as the window
	now := time.Now()
	windowKey := fmt.Sprintf("rate_limit:%s:%d", key, now.Unix()/60)

	// Get current count
	countStr, err := r.redisService.Get(ctx, windowKey)
	currentCount := 0
	if err == nil {
		if count, parseErr := strconv.Atoi(countStr); parseErr == nil {
			currentCount = count
		}
	}

	// Increment count
	newCount := currentCount + 1

	// Set with 2-minute TTL (to handle edge cases around minute boundaries)
	return r.redisService.SetValue(ctx, windowKey, strconv.Itoa(newCount), 2*time.Minute)
}

// GetCurrentUsage returns the current usage for a key
func (r *RateLimiterService) GetCurrentUsage(ctx context.Context, key string) (int, int, error) {
	if !r.config.GoogleCloud.RateLimitEnabled {
		return 0, r.config.GoogleCloud.MaxRequestsPerMinute, nil
	}

	count, err := r.getCurrentCountForUsage(ctx, key)
	if err != nil {
		return 0, r.config.GoogleCloud.MaxRequestsPerMinute, err
	}

	return count, r.config.GoogleCloud.MaxRequestsPerMinute, nil
}

// ResetLimit resets the rate limit for a specific key (useful for testing)
func (r *RateLimiterService) ResetLimit(ctx context.Context, key string) error {
	now := time.Now()
	windowKey := fmt.Sprintf("rate_limit:%s:%d", key, now.Unix()/60)

	return r.redisService.Delete(ctx, windowKey)
}

// HealthCheck performs a health check on the rate limiter
func (r *RateLimiterService) HealthCheck(ctx context.Context) error {
	// Test basic rate limiter functionality
	testKey := "health_check_test"

	// Reset any existing limit for test key
	if err := r.ResetLimit(ctx, testKey); err != nil {
		return fmt.Errorf("failed to reset test rate limit: %w", err)
	}

	// Test that a request is allowed
	allowed, err := r.Allow(ctx, testKey)
	if err != nil {
		return fmt.Errorf("rate limiter health check failed: %w", err)
	}

	if !allowed {
		return fmt.Errorf("rate limiter health check failed: request should have been allowed")
	}

	// Clean up test key
	if err := r.ResetLimit(ctx, testKey); err != nil {
		r.logger.WithError(err).Warn("Failed to clean up rate limiter health check test key")
	}

	return nil
}
