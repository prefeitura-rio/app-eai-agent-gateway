package services

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/sirupsen/logrus"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"

	"github.com/prefeitura-rio/app-eai-agent-gateway/internal/config"
	"github.com/prefeitura-rio/app-eai-agent-gateway/internal/models"
)

// GoogleAgentEngineService implements the AgentClientInterface for Google Agent Engine
type GoogleAgentEngineService struct {
	config       *config.Config
	logger       *logrus.Logger
	rateLimiter  RateLimiterInterface
	redisService RedisServiceInterface
	httpClient   *http.Client
	tokenSource  oauth2.TokenSource // Direct token source, no temp files

	// Health check caching
	healthCheckMu           sync.RWMutex
	lastHealthCheckTime     time.Time
	lastHealthCheckResult   error
	lastHealthCheckEngineID string
}

// ReasoningEngineRequest represents the request structure for reasoning engine queries
type ReasoningEngineRequest struct {
	ClassMethod string                 `json:"classMethod"`
	Input       map[string]interface{} `json:"input"`
}

// ReasoningEngineResponse represents the response from reasoning engine queries
type ReasoningEngineResponse struct {
	Name      string      `json:"name,omitempty"`
	Done      bool        `json:"done,omitempty"`
	Response  interface{} `json:"response,omitempty"`
	Error     interface{} `json:"error,omitempty"`
	Operation interface{} `json:"operation,omitempty"`
}

// RateLimiterInterface defines rate limiting operations
type RateLimiterInterface interface {
	Allow(ctx context.Context, key string) (bool, error)
	Wait(ctx context.Context, key string) error
}

// Prometheus metrics for Google Agent Engine health checks
var (
	googleAgentEngineHealthCheckDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: "eai_gateway",
			Subsystem: "google_agent_engine",
			Name:      "health_check_duration_seconds",
			Help:      "Duration of Google Agent Engine health checks in seconds",
			Buckets:   []float64{0.5, 1, 2, 3, 5, 10},
		},
		[]string{"status", "source"}, // status: success/failure, source: redis/memory/fresh
	)

	googleAgentEngineHealthCheckTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "eai_gateway",
			Subsystem: "google_agent_engine",
			Name:      "health_check_total",
			Help:      "Total number of Google Agent Engine health checks performed",
		},
		[]string{"status", "source"},
	)

	googleAgentEngineHealthCheckRetries = promauto.NewCounter(
		prometheus.CounterOpts{
			Namespace: "eai_gateway",
			Subsystem: "google_agent_engine",
			Name:      "health_check_retries_total",
			Help:      "Total number of health check retries performed",
		},
	)

	googleAgentEngineHealthCheckCacheHits = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "eai_gateway",
			Subsystem: "google_agent_engine",
			Name:      "health_check_cache_hits_total",
			Help:      "Total number of health check cache hits",
		},
		[]string{"source"}, // source: redis/memory
	)
)

// RedisServiceInterface is the interface for Redis operations needed by this service
type RedisServiceInterface interface {
	Get(ctx context.Context, key string) (string, error)
	Set(ctx context.Context, key string, value string, ttl time.Duration) error
	SetValue(ctx context.Context, key string, value interface{}, ttl time.Duration) error
	Delete(ctx context.Context, key string) error
}

// createTokenSourceFromCredentials creates a token source directly from credentials JSON
// This avoids writing to temp files which may fail in read-only Kubernetes environments
func createTokenSourceFromCredentials(ctx context.Context, credentialsJSON []byte) (oauth2.TokenSource, error) {
	// Create credentials directly from JSON without temp files
	creds, err := google.CredentialsFromJSON(ctx, credentialsJSON, "https://www.googleapis.com/auth/cloud-platform")
	if err != nil {
		return nil, fmt.Errorf("failed to create credentials from JSON: %w", err)
	}

	return creds.TokenSource, nil
}

// ThreadInfo represents thread information stored in Redis
type ThreadInfo struct {
	ThreadID     string    `json:"thread_id"`
	UserID       string    `json:"user_id"`
	CreatedAt    time.Time `json:"created_at"`
	LastUsedAt   time.Time `json:"last_used_at"`
	MessageCount int       `json:"message_count"`
}

// NewGoogleAgentEngineService creates a new Google Agent Engine service
func NewGoogleAgentEngineService(
	cfg *config.Config,
	logger *logrus.Logger,
	rateLimiter RateLimiterInterface,
	redisService RedisServiceInterface,
) (*GoogleAgentEngineService, error) {

	// Validate configuration
	if cfg.GoogleAgentEngine.ProjectID == "" {
		return nil, fmt.Errorf("google Agent Engine project ID is required")
	}
	if cfg.GoogleAgentEngine.Location == "" {
		return nil, fmt.Errorf("google Agent Engine location is required")
	}
	if cfg.GoogleAgentEngine.ReasoningEngineID == "" {
		return nil, fmt.Errorf("google Agent Engine reasoning engine ID is required")
	}

	// Set up credentials using direct token source (no temp files)
	var tokenSource oauth2.TokenSource
	if cfg.GoogleAgentEngine.CredentialsJSON != "" {
		// Try to decode as base64 first (for SERVICE_ACCOUNT env var)
		credentialsData := cfg.GoogleAgentEngine.CredentialsJSON
		if decoded, err := base64.StdEncoding.DecodeString(credentialsData); err == nil {
			// Successfully decoded base64, use decoded data
			credentialsData = string(decoded)
		}

		// Create token source directly from credentials (no temp files)
		var err error
		tokenSource, err = createTokenSourceFromCredentials(context.Background(), []byte(credentialsData))
		if err != nil {
			logger.WithError(err).Warn("Failed to create token source from credentials, falling back to default")
			tokenSource = nil
		} else {
			logger.Info("Successfully created token source from provided credentials")
		}
	}

	// Create HTTP client with configured timeout
	httpClient := &http.Client{
		Timeout: cfg.GoogleAgentEngine.RequestTimeout,
	}

	service := &GoogleAgentEngineService{
		config:       cfg,
		logger:       logger,
		rateLimiter:  rateLimiter,
		redisService: redisService,
		httpClient:   httpClient,
		tokenSource:  tokenSource,
	}

	logger.WithFields(logrus.Fields{
		"project_id":          cfg.GoogleAgentEngine.ProjectID,
		"location":            cfg.GoogleAgentEngine.Location,
		"reasoning_engine_id": cfg.GoogleAgentEngine.ReasoningEngineID,
	}).Info("Google Agent Engine service initialized")

	return service, nil
}

// CreateThread creates a new conversation thread for a user
func (s *GoogleAgentEngineService) CreateThread(ctx context.Context, userID string) (string, error) {
	s.logger.WithField("user_id", userID).Debug("Creating new thread")

	// Apply rate limiting
	if err := s.rateLimiter.Wait(ctx, "google_agent_engine"); err != nil {
		return "", fmt.Errorf("rate limit exceeded: %w", err)
	}

	// Use userID (phone number) directly as thread ID
	threadID := userID

	// Store thread information in Redis
	threadInfo := ThreadInfo{
		ThreadID:     threadID,
		UserID:       userID,
		CreatedAt:    time.Now(),
		LastUsedAt:   time.Now(),
		MessageCount: 0,
	}

	threadData, err := json.Marshal(threadInfo)
	if err != nil {
		return "", fmt.Errorf("failed to marshal thread info: %w", err)
	}

	// Store thread info with TTL
	threadKey := fmt.Sprintf("thread:%s", threadID)

	if err := s.redisService.SetValue(ctx, threadKey, string(threadData), s.config.Redis.AgentIDCacheTTL); err != nil {
		return "", fmt.Errorf("failed to store thread info: %w", err)
	}

	s.logger.WithFields(logrus.Fields{
		"user_id":   userID,
		"thread_id": threadID,
	}).Info("Thread created successfully")

	return threadID, nil
}

// GetOrCreateThread gets an existing thread for a user or creates a new one
func (s *GoogleAgentEngineService) GetOrCreateThread(ctx context.Context, userID string) (string, error) {
	// Use userID directly as thread ID
	threadID := userID
	threadKey := fmt.Sprintf("thread:%s", threadID)

	// Try to get existing thread
	threadData, err := s.redisService.Get(ctx, threadKey)

	if err == nil && threadData != "" {
		var threadInfo ThreadInfo
		if err := json.Unmarshal([]byte(threadData), &threadInfo); err == nil {
			// Update last used time
			threadInfo.LastUsedAt = time.Now()
			updatedData, _ := json.Marshal(threadInfo)
			_ = s.redisService.SetValue(ctx, threadKey, string(updatedData), s.config.Redis.AgentIDCacheTTL)

			s.logger.WithFields(logrus.Fields{
				"user_id":   userID,
				"thread_id": threadID,
			}).Debug("Using existing thread")

			return threadID, nil
		}
	}

	// Create new thread if none exists or existing one is invalid
	return s.CreateThread(ctx, userID)
}

// SendMessage sends a message to a thread and returns the agent's response
// messageType is optional - if nil, no type parameter is sent; if "history", updates history without response
func (s *GoogleAgentEngineService) SendMessage(ctx context.Context, threadID string, content string, reasoningEngineID *string, messageType *string) (*models.AgentResponse, error) {
	start := time.Now()

	s.logger.WithFields(logrus.Fields{
		"thread_id":        threadID,
		"content_length":   len(content),
		"custom_engine_id": reasoningEngineID != nil && *reasoningEngineID != "",
	}).Debug("Sending message to thread")

	// Apply rate limiting
	if err := s.rateLimiter.Wait(ctx, "google_agent_engine"); err != nil {
		return nil, fmt.Errorf("rate limit exceeded: %w", err)
	}

	// Get thread info and validate
	threadKey := fmt.Sprintf("thread:%s", threadID)
	threadData, err := s.redisService.Get(ctx, threadKey)
	if err != nil {
		return nil, fmt.Errorf("thread not found: %w", err)
	}

	var threadInfo ThreadInfo
	if err := json.Unmarshal([]byte(threadData), &threadInfo); err != nil {
		return nil, fmt.Errorf("failed to parse thread info: %w", err)
	}

	// Call the reasoning engine via HTTP REST API
	responseContent, err := s.queryReasoningEngine(ctx, threadID, content, reasoningEngineID, messageType)
	if err != nil {
		s.logger.WithError(err).WithField("thread_id", threadID).Error("Failed to query reasoning engine")

		// Return graceful error message instead of returning error
		// This prevents raw error propagation to the user
		errorMsg := s.getGracefulErrorMessage(err)
		responseContent = errorMsg

		// Log the actual error but don't fail the request
		s.logger.WithFields(logrus.Fields{
			"thread_id":        threadID,
			"error":            err.Error(),
			"graceful_message": errorMsg,
		}).Warn("Returning graceful error message to user")
	}

	if responseContent == "" {
		responseContent = s.config.GoogleAgentEngine.ErrorMessageDefault
	}

	// Usage metadata is not available from reasoning engine API
	var usage *models.UsageMetadata

	// Update thread info
	threadInfo.LastUsedAt = time.Now()
	threadInfo.MessageCount++
	updatedData, _ := json.Marshal(threadInfo)
	_ = s.redisService.SetValue(ctx, threadKey, string(updatedData), s.config.Redis.AgentIDCacheTTL)

	// Generate response message ID
	messageID := fmt.Sprintf("msg_%s_%d", threadID, time.Now().UnixNano())

	duration := time.Since(start)

	s.logger.WithFields(logrus.Fields{
		"thread_id":       threadID,
		"message_id":      messageID,
		"response_length": len(responseContent),
		"duration_ms":     duration.Milliseconds(),
		"message_count":   threadInfo.MessageCount,
		"usage":           usage,
	}).Info("Message processed successfully")

	return &models.AgentResponse{
		Content:   responseContent,
		ThreadID:  threadID,
		MessageID: messageID,
		Metadata: map[string]interface{}{
			"duration_ms":   duration.Milliseconds(),
			"message_count": threadInfo.MessageCount,
			"user_id":       threadInfo.UserID,
		},
		Usage: usage,
	}, nil
}

// getAccessToken gets an access token using the configured token source or default
func (s *GoogleAgentEngineService) getAccessToken(ctx context.Context) (string, error) {
	var ts oauth2.TokenSource

	// Use our stored token source if available, otherwise fall back to default
	if s.tokenSource != nil {
		ts = s.tokenSource
	} else {
		// Fall back to default token source (uses ADC or workload identity)
		var err error
		ts, err = google.DefaultTokenSource(ctx, "https://www.googleapis.com/auth/cloud-platform")
		if err != nil {
			return "", fmt.Errorf("failed to get default token source: %w", err)
		}
	}

	tok, err := ts.Token()
	if err != nil {
		return "", fmt.Errorf("failed to get token: %w", err)
	}

	return tok.AccessToken, nil
}

// postQuery makes a POST request to the reasoning engine query endpoint
func (s *GoogleAgentEngineService) postQuery(ctx context.Context, accessToken string, payload map[string]interface{}, reasoningEngineID string) (map[string]interface{}, error) {
	url := fmt.Sprintf("https://%s-aiplatform.googleapis.com/v1beta1/projects/%s/locations/%s/reasoningEngines/%s:query",
		s.config.GoogleAgentEngine.Location,
		s.config.GoogleAgentEngine.ProjectID,
		s.config.GoogleAgentEngine.Location,
		reasoningEngineID)

	jsonData, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(jsonData))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json; charset=utf-8")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to make request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("non-2xx response: %d - %s", resp.StatusCode, string(bodyBytes))
	}

	var out map[string]interface{}
	if err := json.Unmarshal(bodyBytes, &out); err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w (raw: %s)", err, string(bodyBytes))
	}

	return out, nil
}

// pollOperation polls a long-running operation until completion
func (s *GoogleAgentEngineService) pollOperation(ctx context.Context, accessToken, operationName string, interval, timeout time.Duration) (map[string]interface{}, error) {
	url := fmt.Sprintf("https://%s-aiplatform.googleapis.com/v1beta1/%s", s.config.GoogleAgentEngine.Location, operationName)
	client := s.httpClient
	deadline := time.Now().Add(timeout)

	for {
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("polling timed out after %s", timeout)
		}

		req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
		if err != nil {
			return nil, fmt.Errorf("failed to create poll request: %w", err)
		}
		req.Header.Set("Authorization", "Bearer "+accessToken)

		resp, err := client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("failed to poll operation: %w", err)
		}

		bodyBytes, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("failed to read poll response: %w", err)
		}

		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return nil, fmt.Errorf("poll non-2xx response: %d - %s", resp.StatusCode, string(bodyBytes))
		}

		var op map[string]interface{}
		if err := json.Unmarshal(bodyBytes, &op); err != nil {
			return nil, fmt.Errorf("failed to unmarshal poll response: %w", err)
		}

		if done, _ := op["done"].(bool); done {
			return op, nil
		}

		time.Sleep(interval)
	}
}

// extractOperationName extracts the operation name from a response
func (s *GoogleAgentEngineService) extractOperationName(resp map[string]interface{}) string {
	if n, ok := resp["name"].(string); ok && n != "" {
		return n
	}
	if op, ok := resp["operation"].(map[string]interface{}); ok {
		if n, ok := op["name"].(string); ok && n != "" {
			return n
		}
	}
	return ""
}

// queryReasoningEngine makes a request to the reasoning engine with proper async handling
// messageType is optional - if nil, no type parameter is sent; if "history", updates history without response
func (s *GoogleAgentEngineService) queryReasoningEngine(ctx context.Context, threadID, message string, reasoningEngineID *string, messageType *string) (string, error) {
	accessToken, err := s.getAccessToken(ctx)
	if err != nil {
		return "", fmt.Errorf("failed to get access token: %w", err)
	}

	// Use custom reasoning engine ID if provided, otherwise use config default
	engineID := s.config.GoogleAgentEngine.ReasoningEngineID
	if reasoningEngineID != nil && *reasoningEngineID != "" {
		engineID = *reasoningEngineID
		s.logger.WithFields(logrus.Fields{
			"custom_engine_id":  engineID,
			"default_engine_id": s.config.GoogleAgentEngine.ReasoningEngineID,
		}).Info("Using custom reasoning engine ID from request")
	}

	// Build messages array with the current message
	messages := []map[string]interface{}{
		{
			"role":    "human",
			"content": message,
		},
	}

	// Build payload matching the sandbox pattern
	inputPayload := map[string]interface{}{
		"input": map[string]interface{}{
			"messages": messages,
		},
		"config": map[string]interface{}{
			"configurable": map[string]interface{}{
				"thread_id": threadID,
			},
		},
	}

	// Add type parameter only if specified (e.g., "history" for history updates)
	if messageType != nil && *messageType != "" {
		inputPayload["type"] = *messageType
	}

	payload := map[string]interface{}{
		"classMethod": "async_query",
		"input":       inputPayload,
	}

	s.logger.WithFields(logrus.Fields{
		"thread_id":            threadID,
		"message_length":       len(message),
		"total_messages_count": len(messages),
		"reasoning_engine_id":  engineID,
	}).Debug("Making async_query call to reasoning engine")

	resp, err := s.postQuery(ctx, accessToken, payload, engineID)
	if err != nil {
		return "", fmt.Errorf("failed to post query: %w", err)
	}

	opName := s.extractOperationName(resp)
	if opName == "" {
		// Direct response, no polling needed
		if response, ok := resp["response"]; ok {
			return s.extractContentFromResponse(response)
		}
		// Log the response for debugging
		respBytes, _ := json.Marshal(resp)
		s.logger.WithField("response", string(respBytes)).Debug("No operation returned, checking for direct response")
		return s.extractContentFromResponse(resp)
	}

	s.logger.WithField("operation_name", opName).Debug("Polling operation until completion")

	// Poll with reasonable intervals using configured timeout
	op, err := s.pollOperation(ctx, accessToken, opName, 2*time.Second, s.config.GoogleAgentEngine.RequestTimeout)
	if err != nil {
		return "", fmt.Errorf("failed to poll operation: %w", err)
	}

	if responseObj, ok := op["response"]; ok {
		return s.extractContentFromResponse(responseObj)
	} else if errObj, ok := op["error"]; ok {
		errBytes, _ := json.Marshal(errObj)
		return "", fmt.Errorf("operation finished with error: %s", string(errBytes))
	}

	return "", fmt.Errorf("operation finished but no 'response' or 'error' field found")
}

// extractContentFromResponse extracts the content string from a response object
func (s *GoogleAgentEngineService) extractContentFromResponse(response interface{}) (string, error) {
	// Try to extract content from various possible response structures
	responseMap, ok := response.(map[string]interface{})
	if !ok {
		// If response is a string, return it directly
		if str, ok := response.(string); ok {
			return str, nil
		}
		// Convert to JSON as fallback
		responseBytes, err := json.Marshal(response)
		if err != nil {
			return "", fmt.Errorf("failed to marshal response: %w", err)
		}
		return string(responseBytes), nil
	}

	// Look for common content fields
	contentFields := []string{"content", "text", "message", "output", "result"}
	for _, field := range contentFields {
		if content, ok := responseMap[field]; ok {
			if str, ok := content.(string); ok {
				return str, nil
			}
		}
	}

	// Try to extract from messages array (common in chat responses)
	if messages, ok := responseMap["messages"]; ok {
		if messagesArray, ok := messages.([]interface{}); ok {
			for _, msg := range messagesArray {
				if msgMap, ok := msg.(map[string]interface{}); ok {
					if content, ok := msgMap["content"].(string); ok {
						return content, nil
					}
				}
			}
		}
	}

	// Fallback to JSON marshaling
	responseBytes, err := json.Marshal(response)
	if err != nil {
		return "", fmt.Errorf("failed to marshal response: %w", err)
	}
	return string(responseBytes), nil
}

// Close closes the Google Agent Engine client
func (s *GoogleAgentEngineService) Close() error {
	// HTTP client doesn't need explicit closing
	return nil
}

// HealthCheck performs a health check on the Google Agent Engine service
func (s *GoogleAgentEngineService) HealthCheck(ctx context.Context) error {
	start := time.Now()
	currentEngineID := s.config.GoogleAgentEngine.ReasoningEngineID
	cacheTTL := s.config.GoogleAgentEngine.HealthCheckCacheTTL

	// FIRST: Try Redis shared cache (best case - avoid duplicate checks across pods)
	// If Redis is down, fall back to in-memory cache
	redisCacheKey := fmt.Sprintf("health_check:google_agent_engine:%s", currentEngineID)
	if s.redisService != nil {
		cachedStatus, err := s.redisService.Get(ctx, redisCacheKey)
		if err == nil && cachedStatus != "" {
			s.logger.WithField("source", "redis").Debug("Using Redis cached health check result")

			// Record cache hit
			googleAgentEngineHealthCheckCacheHits.WithLabelValues("redis").Inc()

			status := "success"
			if cachedStatus != "healthy" {
				status = "failure"
			}
			googleAgentEngineHealthCheckDuration.WithLabelValues(status, "redis").Observe(time.Since(start).Seconds())
			googleAgentEngineHealthCheckTotal.WithLabelValues(status, "redis").Inc()

			if cachedStatus == "healthy" {
				return nil
			}
			return fmt.Errorf("cached unhealthy status from Redis")
		}
		// Redis error or miss - fall through to in-memory cache
		if err != nil {
			s.logger.WithError(err).Debug("Redis cache miss or error, falling back to in-memory cache")
		}
	}

	// SECOND: Check in-memory cache (fallback when Redis unavailable)
	s.healthCheckMu.RLock()
	cacheValid := time.Since(s.lastHealthCheckTime) < cacheTTL &&
		s.lastHealthCheckEngineID == currentEngineID
	cachedResult := s.lastHealthCheckResult
	s.healthCheckMu.RUnlock()

	if cacheValid {
		s.logger.WithFields(logrus.Fields{
			"cached_result": cachedResult == nil,
			"age":           time.Since(s.lastHealthCheckTime).String(),
			"source":        "memory",
		}).Debug("Using in-memory cached health check result")

		// Record cache hit
		googleAgentEngineHealthCheckCacheHits.WithLabelValues("memory").Inc()

		status := "success"
		if cachedResult != nil {
			status = "failure"
		}
		googleAgentEngineHealthCheckDuration.WithLabelValues(status, "memory").Observe(time.Since(start).Seconds())
		googleAgentEngineHealthCheckTotal.WithLabelValues(status, "memory").Inc()

		return cachedResult
	}

	// Apply rate limiting for health check
	if allowed, err := s.rateLimiter.Allow(ctx, "google_agent_engine_health"); err != nil {
		return fmt.Errorf("rate limiter error during health check: %w", err)
	} else if !allowed {
		// If rate limited, return last cached result if available
		s.healthCheckMu.RLock()
		lastResult := s.lastHealthCheckResult
		s.healthCheckMu.RUnlock()
		if lastResult == nil {
			return nil // Last check was successful
		}
		return fmt.Errorf("rate limit exceeded for health check")
	}

	// Perform health check with retries
	maxRetries := s.config.GoogleAgentEngine.HealthCheckMaxRetries
	retryDelay := s.config.GoogleAgentEngine.HealthCheckRetryDelay
	var lastErr error

	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			s.logger.WithFields(logrus.Fields{
				"attempt":     attempt,
				"max_retries": maxRetries,
			}).Debug("Retrying Google Agent Engine health check")
			googleAgentEngineHealthCheckRetries.Inc()
			time.Sleep(retryDelay)
		}

		err := s.performHealthCheck(ctx)
		if err == nil {
			// Success - cache the result in BOTH locations
			s.healthCheckMu.Lock()
			s.lastHealthCheckTime = time.Now()
			s.lastHealthCheckResult = nil
			s.lastHealthCheckEngineID = currentEngineID
			s.healthCheckMu.Unlock()

			// Also cache in Redis (best-effort, don't fail if Redis is down)
			if s.redisService != nil {
				_ = s.redisService.Set(ctx, redisCacheKey, "healthy", cacheTTL)
			}

			// Record successful fresh health check
			googleAgentEngineHealthCheckDuration.WithLabelValues("success", "fresh").Observe(time.Since(start).Seconds())
			googleAgentEngineHealthCheckTotal.WithLabelValues("success", "fresh").Inc()

			return nil
		}

		lastErr = err

		// Don't retry on auth errors or non-timeout errors
		if !strings.Contains(err.Error(), "timeout") &&
			!strings.Contains(err.Error(), "deadline") &&
			!strings.Contains(err.Error(), "connection refused") {
			break
		}
	}

	// Cache the failure result in BOTH locations
	s.healthCheckMu.Lock()
	s.lastHealthCheckTime = time.Now()
	s.lastHealthCheckResult = lastErr
	s.lastHealthCheckEngineID = currentEngineID
	s.healthCheckMu.Unlock()

	// Also cache failure in Redis (best-effort)
	if s.redisService != nil {
		_ = s.redisService.Set(ctx, redisCacheKey, "unhealthy", cacheTTL)
	}

	// Record failed fresh health check
	googleAgentEngineHealthCheckDuration.WithLabelValues("failure", "fresh").Observe(time.Since(start).Seconds())
	googleAgentEngineHealthCheckTotal.WithLabelValues("failure", "fresh").Inc()

	return lastErr
}

// performHealthCheck executes a single health check attempt
func (s *GoogleAgentEngineService) performHealthCheck(ctx context.Context) error {
	// Use configured health check timeout
	timeout := s.config.GoogleAgentEngine.HealthCheckTimeout
	if timeout == 0 {
		timeout = 3 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Use lightweight GET request to check reasoning engine existence
	reasoningEngineID := s.config.GoogleAgentEngine.ReasoningEngineID
	url := fmt.Sprintf(
		"https://%s-aiplatform.googleapis.com/v1/projects/%s/locations/%s/reasoningEngines/%s",
		s.config.GoogleAgentEngine.Location,
		s.config.GoogleAgentEngine.ProjectID,
		s.config.GoogleAgentEngine.Location,
		reasoningEngineID,
	)

	// Get OAuth token
	token, err := s.tokenSource.Token()
	if err != nil {
		return fmt.Errorf("failed to get OAuth token: %w", err)
	}

	// Create GET request
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return fmt.Errorf("failed to create health check request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+token.AccessToken)
	req.Header.Set("Content-Type", "application/json")

	// Create dedicated HTTP client for health checks with shorter timeout
	healthClient := &http.Client{
		Timeout: timeout,
	}

	// Make request
	resp, err := healthClient.Do(req)
	if err != nil {
		if strings.Contains(err.Error(), "context deadline exceeded") || strings.Contains(err.Error(), "context canceled") {
			return fmt.Errorf("google Agent Engine health check timeout")
		}
		return fmt.Errorf("google Agent Engine health check failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	// Check response status
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("google Agent Engine health check returned status %d: %s", resp.StatusCode, string(body))
	}

	return nil
}

// getGracefulErrorMessage returns a user-friendly error message based on the error type
func (s *GoogleAgentEngineService) getGracefulErrorMessage(err error) string {
	errStr := err.Error()

	// Check for timeout errors
	if strings.Contains(errStr, "timeout") ||
		strings.Contains(errStr, "deadline exceeded") ||
		strings.Contains(errStr, "context canceled") {
		return s.config.GoogleAgentEngine.ErrorMessageTimeout
	}

	// Check for availability errors
	if strings.Contains(errStr, "connection refused") ||
		strings.Contains(errStr, "unavailable") ||
		strings.Contains(errStr, "503") ||
		strings.Contains(errStr, "502") ||
		strings.Contains(errStr, "504") {
		return s.config.GoogleAgentEngine.ErrorMessageTemporarilyUnavailable
	}

	// Default error message for unknown errors
	return s.config.GoogleAgentEngine.ErrorMessageDefault
}

// SendHistoryUpdate sends multiple messages to update conversation history
// Returns a simple status response without waiting for agent reply
func (s *GoogleAgentEngineService) SendHistoryUpdate(ctx context.Context, threadID string, messages []models.HistoryMessage, reasoningEngineID *string) (map[string]interface{}, error) {
	start := time.Now()

	s.logger.WithFields(logrus.Fields{
		"thread_id":        threadID,
		"messages_count":   len(messages),
		"custom_engine_id": reasoningEngineID != nil && *reasoningEngineID != "",
	}).Debug("Sending history update to thread")

	// Apply rate limiting
	if err := s.rateLimiter.Wait(ctx, "google_agent_engine"); err != nil {
		return nil, fmt.Errorf("rate limit exceeded: %w", err)
	}

	// Get thread info and validate
	threadKey := fmt.Sprintf("thread:%s", threadID)
	threadData, err := s.redisService.Get(ctx, threadKey)
	if err != nil {
		return nil, fmt.Errorf("thread not found: %w", err)
	}

	var threadInfo ThreadInfo
	if err := json.Unmarshal([]byte(threadData), &threadInfo); err != nil {
		return nil, fmt.Errorf("failed to parse thread info: %w", err)
	}

	// Get access token
	accessToken, err := s.getAccessToken(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get access token: %w", err)
	}

	// Use custom reasoning engine ID if provided
	engineID := s.config.GoogleAgentEngine.ReasoningEngineID
	if reasoningEngineID != nil && *reasoningEngineID != "" {
		engineID = *reasoningEngineID
		s.logger.WithFields(logrus.Fields{
			"custom_engine_id":  engineID,
			"default_engine_id": s.config.GoogleAgentEngine.ReasoningEngineID,
		}).Info("Using custom reasoning engine ID for history update")
	}

	// Build messages array from the input messages
	formattedMessages := make([]map[string]interface{}, len(messages))
	for i, msg := range messages {
		formattedMessages[i] = map[string]interface{}{
			"role":    msg.Role,
			"content": msg.Content,
		}
	}

	// Build payload for history update with type="history"
	historyType := "history"
	inputPayload := map[string]interface{}{
		"input": map[string]interface{}{
			"messages": formattedMessages,
		},
		"config": map[string]interface{}{
			"configurable": map[string]interface{}{
				"thread_id": threadID,
			},
		},
		"type": historyType,
	}

	payload := map[string]interface{}{
		"classMethod": "async_query",
		"input":       inputPayload,
	}

	s.logger.WithFields(logrus.Fields{
		"thread_id":           threadID,
		"messages_count":      len(formattedMessages),
		"reasoning_engine_id": engineID,
	}).Debug("Making async_query call with type=history to reasoning engine")

	resp, err := s.postQuery(ctx, accessToken, payload, engineID)
	if err != nil {
		return nil, fmt.Errorf("failed to post query: %w", err)
	}

	// Update thread info
	threadInfo.LastUsedAt = time.Now()
	threadInfo.MessageCount += len(messages)
	updatedData, _ := json.Marshal(threadInfo)
	_ = s.redisService.SetValue(ctx, threadKey, string(updatedData), s.config.Redis.AgentIDCacheTTL)

	duration := time.Since(start)

	s.logger.WithFields(logrus.Fields{
		"thread_id":     threadID,
		"duration_ms":   duration.Milliseconds(),
		"message_count": threadInfo.MessageCount,
	}).Info("History update processed successfully")

	return resp, nil
}
