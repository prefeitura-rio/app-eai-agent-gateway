package services

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sirupsen/logrus"
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

// RedisServiceInterface is the interface for Redis operations needed by this service
type RedisServiceInterface interface {
	Get(ctx context.Context, key string) (string, error)
	Set(ctx context.Context, key string, value string, ttl time.Duration) error
	SetValue(ctx context.Context, key string, value interface{}, ttl time.Duration) error
	Delete(ctx context.Context, key string) error
}

// setGoogleApplicationCredentials sets up credentials for google.DefaultTokenSource
func setGoogleApplicationCredentials(credentialsJSON []byte) error {
	// Create a temporary file for the credentials
	tempDir := os.TempDir()
	tempFile := filepath.Join(tempDir, fmt.Sprintf("gcp-credentials-%d.json", time.Now().UnixNano()))

	if err := os.WriteFile(tempFile, credentialsJSON, 0600); err != nil {
		return fmt.Errorf("failed to write credentials to temp file: %w", err)
	}

	// Set the environment variable for google.DefaultTokenSource
	if err := os.Setenv("GOOGLE_APPLICATION_CREDENTIALS", tempFile); err != nil {
		_ = os.Remove(tempFile) // Clean up on error
		return fmt.Errorf("failed to set GOOGLE_APPLICATION_CREDENTIALS: %w", err)
	}

	return nil
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

	// Set up credentials for DefaultTokenSource to work properly
	if cfg.GoogleAgentEngine.CredentialsJSON != "" {
		// Try to decode as base64 first (for SERVICE_ACCOUNT env var)
		credentialsData := cfg.GoogleAgentEngine.CredentialsJSON
		if decoded, err := base64.StdEncoding.DecodeString(credentialsData); err == nil {
			// Successfully decoded base64, use decoded data
			credentialsData = string(decoded)
		}

		// Set the GOOGLE_APPLICATION_CREDENTIALS environment variable if not set
		// This allows google.DefaultTokenSource to work with our credentials
		if err := setGoogleApplicationCredentials([]byte(credentialsData)); err != nil {
			logger.WithError(err).Warn("Failed to set up credentials for DefaultTokenSource")
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

	// Generate a unique thread ID
	threadID := fmt.Sprintf("thread_%s_%d", userID, time.Now().UnixNano())

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
	userThreadKey := fmt.Sprintf("user_thread:%s", userID)

	if err := s.redisService.SetValue(ctx, threadKey, string(threadData), s.config.Redis.AgentIDCacheTTL); err != nil {
		return "", fmt.Errorf("failed to store thread info: %w", err)
	}

	// Store user -> thread mapping
	if err := s.redisService.SetValue(ctx, userThreadKey, threadID, s.config.Redis.AgentIDCacheTTL); err != nil {
		return "", fmt.Errorf("failed to store user thread mapping: %w", err)
	}

	s.logger.WithFields(logrus.Fields{
		"user_id":   userID,
		"thread_id": threadID,
	}).Info("Thread created successfully")

	return threadID, nil
}

// GetOrCreateThread gets an existing thread for a user or creates a new one
func (s *GoogleAgentEngineService) GetOrCreateThread(ctx context.Context, userID string) (string, error) {
	// Try to get existing thread for user
	userThreadKey := fmt.Sprintf("user_thread:%s", userID)
	existingThreadID, err := s.redisService.Get(ctx, userThreadKey)

	if err == nil && existingThreadID != "" {
		// Verify thread still exists and update last used time
		threadKey := fmt.Sprintf("thread:%s", existingThreadID)
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
					"thread_id": existingThreadID,
				}).Debug("Using existing thread")

				return existingThreadID, nil
			}
		}
	}

	// Create new thread if none exists or existing one is invalid
	return s.CreateThread(ctx, userID)
}

// SendMessage sends a message to a thread and returns the agent's response
func (s *GoogleAgentEngineService) SendMessage(ctx context.Context, threadID string, content string) (*models.AgentResponse, error) {
	start := time.Now()

	s.logger.WithFields(logrus.Fields{
		"thread_id":      threadID,
		"content_length": len(content),
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
	responseContent, err := s.queryReasoningEngine(ctx, threadID, content)
	if err != nil {
		s.logger.WithError(err).WithField("thread_id", threadID).Error("Failed to query reasoning engine")
		return nil, fmt.Errorf("failed to get AI response: %w", err)
	}

	if responseContent == "" {
		responseContent = "I apologize, but I couldn't generate a response. Please try again."
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

// SendDirectMessage sends a message directly to an agent (legacy support)
func (s *GoogleAgentEngineService) SendDirectMessage(ctx context.Context, agentID string, content string) (*models.AgentResponse, error) {
	start := time.Now()

	s.logger.WithFields(logrus.Fields{
		"agent_id":       agentID,
		"content_length": len(content),
	}).Debug("Sending direct message to agent")

	// Apply rate limiting
	if err := s.rateLimiter.Wait(ctx, "google_agent_engine"); err != nil {
		return nil, fmt.Errorf("rate limit exceeded: %w", err)
	}

	// For direct messages, we create a temporary thread-like context
	// This maintains compatibility with the legacy API while using Google Agent Engine
	tempThreadID := fmt.Sprintf("direct_%s_%d", agentID, time.Now().UnixNano())

	// Call the reasoning engine via HTTP REST API for direct messages
	responseContent, err := s.queryReasoningEngine(ctx, tempThreadID, content)
	if err != nil {
		s.logger.WithError(err).WithField("agent_id", agentID).Error("Failed to query reasoning engine for direct message")
		return nil, fmt.Errorf("failed to get AI response: %w", err)
	}

	if responseContent == "" {
		responseContent = "I apologize, but I couldn't generate a response. Please try again."
	}

	// Usage metadata is not available from reasoning engine API
	var usage *models.UsageMetadata

	// Generate response message ID
	messageID := fmt.Sprintf("msg_%s_%d", agentID, time.Now().UnixNano())

	duration := time.Since(start)

	s.logger.WithFields(logrus.Fields{
		"agent_id":        agentID,
		"message_id":      messageID,
		"response_length": len(responseContent),
		"duration_ms":     duration.Milliseconds(),
		"usage":           usage,
	}).Info("Direct message processed successfully")

	return &models.AgentResponse{
		Content:   responseContent,
		ThreadID:  tempThreadID,
		MessageID: messageID,
		Metadata: map[string]interface{}{
			"duration_ms": duration.Milliseconds(),
			"agent_id":    agentID,
			"direct_mode": true,
		},
		Usage: usage,
	}, nil
}

// getAccessToken gets an access token using the default token source
func (s *GoogleAgentEngineService) getAccessToken(ctx context.Context) (string, error) {
	ts, err := google.DefaultTokenSource(ctx, "https://www.googleapis.com/auth/cloud-platform")
	if err != nil {
		return "", fmt.Errorf("failed to get token source: %w", err)
	}

	tok, err := ts.Token()
	if err != nil {
		return "", fmt.Errorf("failed to get token: %w", err)
	}

	return tok.AccessToken, nil
}

// postQuery makes a POST request to the reasoning engine query endpoint
func (s *GoogleAgentEngineService) postQuery(ctx context.Context, accessToken string, payload map[string]interface{}) (map[string]interface{}, error) {
	url := fmt.Sprintf("https://%s-aiplatform.googleapis.com/v1beta1/projects/%s/locations/%s/reasoningEngines/%s:query",
		s.config.GoogleAgentEngine.Location,
		s.config.GoogleAgentEngine.ProjectID,
		s.config.GoogleAgentEngine.Location,
		s.config.GoogleAgentEngine.ReasoningEngineID)

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
func (s *GoogleAgentEngineService) queryReasoningEngine(ctx context.Context, threadID, message string) (string, error) {
	accessToken, err := s.getAccessToken(ctx)
	if err != nil {
		return "", fmt.Errorf("failed to get access token: %w", err)
	}

	// Build payload matching the sandbox pattern
	payload := map[string]interface{}{
		"classMethod": "async_query",
		"input": map[string]interface{}{
			"input": map[string]interface{}{
				"messages": []map[string]interface{}{
					{"role": "human", "content": message},
				},
			},
			"config": map[string]interface{}{
				"configurable": map[string]interface{}{
					"thread_id": threadID,
				},
			},
		},
	}

	s.logger.WithFields(logrus.Fields{
		"thread_id":      threadID,
		"message_length": len(message),
	}).Debug("Making async_query call to reasoning engine")

	resp, err := s.postQuery(ctx, accessToken, payload)
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

	// Poll with reasonable intervals and timeout
	op, err := s.pollOperation(ctx, accessToken, opName, 2*time.Second, 120*time.Second)
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
	// Apply rate limiting for health check
	if allowed, err := s.rateLimiter.Allow(ctx, "google_agent_engine_health"); err != nil {
		return fmt.Errorf("rate limiter error during health check: %w", err)
	} else if !allowed {
		return fmt.Errorf("rate limit exceeded for health check")
	}

	// Simple test query to verify the service is accessible
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	// Test with a simple health check query to the reasoning engine
	_, err := s.queryReasoningEngine(ctx, "health-check", "Health check - please respond with 'OK'")
	if err != nil {
		if strings.Contains(err.Error(), "context deadline exceeded") {
			return fmt.Errorf("google Agent Engine health check timeout")
		}
		return fmt.Errorf("google Agent Engine health check failed: %w", err)
	}

	return nil
}
