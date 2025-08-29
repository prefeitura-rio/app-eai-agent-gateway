package services

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/prefeitura-rio/app-eai-agent-gateway/internal/config"
)

// EAIAgentService implements the EAI Agent Service client for agent configuration and lifecycle
type EAIAgentService struct {
	config     *config.Config
	logger     *logrus.Logger
	httpClient *http.Client
	baseURL    string
	token      string
}

// NewEAIAgentService creates a new EAI Agent Service client
func NewEAIAgentService(cfg *config.Config, logger *logrus.Logger) *EAIAgentService {
	service := &EAIAgentService{
		config:  cfg,
		logger:  logger,
		baseURL: cfg.EAIAgent.URL,
		token:   cfg.EAIAgent.Token,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}

	logger.WithFields(logrus.Fields{
		"base_url": service.baseURL,
		"timeout":  service.httpClient.Timeout,
	}).Info("EAI Agent Service client initialized")

	return service
}

// AgentConfiguration represents the configuration for creating an agent
type AgentConfiguration struct {
	UserNumber           string                 `json:"user_number"`
	AgentType            string                 `json:"agent_type,omitempty"`
	Name                 string                 `json:"name,omitempty"`
	Tags                 []string               `json:"tags,omitempty"`
	System               string                 `json:"system,omitempty"`
	MemoryBlocks         []MemoryBlock          `json:"memory_blocks,omitempty"`
	Tools                []string               `json:"tools,omitempty"`
	Model                string                 `json:"model,omitempty"`
	Embedding            string                 `json:"embedding,omitempty"`
	ContextWindowLimit   int                    `json:"context_window_limit,omitempty"`
	IncludeBaseToolRules bool                   `json:"include_base_tool_rules,omitempty"`
	IncludeBaseTools     bool                   `json:"include_base_tools,omitempty"`
	Timezone             string                 `json:"timezone,omitempty"`
	AdditionalConfig     map[string]interface{} `json:"additional_config,omitempty"`
}

// MemoryBlock represents a memory block for agent configuration
type MemoryBlock struct {
	Type    string                 `json:"type"`
	Content string                 `json:"content"`
	Data    map[string]interface{} `json:"data,omitempty"`
}

// AgentResponse represents the response when creating or retrieving an agent
type AgentResponse struct {
	AgentID    string                 `json:"agent_id"`
	UserNumber string                 `json:"user_number"`
	Status     string                 `json:"status"`
	Config     *AgentConfiguration    `json:"config,omitempty"`
	CreatedAt  time.Time              `json:"created_at"`
	UpdatedAt  time.Time              `json:"updated_at"`
	Metadata   map[string]interface{} `json:"metadata,omitempty"`
}

// SystemPromptResponse represents the response when retrieving system prompts
type SystemPromptResponse struct {
	PromptID  string                 `json:"prompt_id"`
	Content   string                 `json:"content"`
	Type      string                 `json:"type"`
	Version   string                 `json:"version"`
	Tags      []string               `json:"tags,omitempty"`
	Variables map[string]interface{} `json:"variables,omitempty"`
	CreatedAt time.Time              `json:"created_at"`
	UpdatedAt time.Time              `json:"updated_at"`
}

// DeleteAgentRequest represents a request to delete agents
type DeleteAgentRequest struct {
	AgentID         string   `json:"agent_id,omitempty"`
	TagList         []string `json:"tag_list,omitempty"`
	DeleteAllAgents bool     `json:"delete_all_agents,omitempty"`
}

// DeleteAgentResponse represents the response when deleting agents
type DeleteAgentResponse struct {
	Message      string   `json:"message"`
	DeletedCount int      `json:"deleted_count"`
	DeletedIDs   []string `json:"deleted_ids,omitempty"`
}

// CreateAgent creates a new agent with the specified configuration
func (e *EAIAgentService) CreateAgent(ctx context.Context, config *AgentConfiguration) (*AgentResponse, error) {
	start := time.Now()

	e.logger.WithFields(logrus.Fields{
		"user_number": config.UserNumber,
		"agent_type":  config.AgentType,
		"model":       config.Model,
	}).Debug("Creating agent")

	// Apply default configuration values from service config
	if config.Model == "" {
		config.Model = e.config.EAIAgent.LLMModel
	}
	if config.Embedding == "" {
		config.Embedding = e.config.EAIAgent.EmbeddingModel
	}
	if config.ContextWindowLimit == 0 {
		config.ContextWindowLimit = e.config.EAIAgent.ContextWindowLimit
	}
	if config.AgentType == "" {
		config.AgentType = "memgpt_v2_agent"
	}
	if config.Timezone == "" {
		config.Timezone = "America/Sao_Paulo"
	}
	if config.Tags == nil {
		config.Tags = []string{"agentic_search"}
	}
	if config.Tools == nil {
		config.Tools = []string{"google_search", "web_search_surkai"}
	}

	// Set default system prompt if not provided
	if config.System == "" {
		config.System = "You are an AI assistant designed to help users with information retrieval and task completion. You have access to search tools and can browse the web to provide accurate, up-to-date information."
	}

	// Set default memory blocks if not provided
	if config.MemoryBlocks == nil {
		config.MemoryBlocks = []MemoryBlock{
			{
				Type:    "persona",
				Content: "I am an AI assistant designed to help users with various tasks and information retrieval.",
			},
			{
				Type:    "human",
				Content: fmt.Sprintf("User: %s", config.UserNumber),
			},
		}
	}

	reqBody, err := json.Marshal(config)
	if err != nil {
		e.logger.WithError(err).Error("Failed to marshal agent configuration")
		return nil, fmt.Errorf("failed to marshal agent configuration: %w", err)
	}

	url := fmt.Sprintf("%s/api/v1/agent/create", strings.TrimSuffix(e.baseURL, "/"))
	resp, err := e.makeRequest(ctx, "POST", url, reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to create agent: %w", err)
	}

	var agentResp AgentResponse
	if err := json.Unmarshal(resp, &agentResp); err != nil {
		e.logger.WithError(err).Error("Failed to unmarshal agent response")
		return nil, fmt.Errorf("failed to unmarshal agent response: %w", err)
	}

	duration := time.Since(start)
	e.logger.WithFields(logrus.Fields{
		"agent_id":    agentResp.AgentID,
		"user_number": config.UserNumber,
		"duration_ms": duration.Milliseconds(),
	}).Info("Agent created successfully")

	return &agentResp, nil
}

// GetAgent retrieves an existing agent by ID
func (e *EAIAgentService) GetAgent(ctx context.Context, agentID string) (*AgentResponse, error) {
	e.logger.WithField("agent_id", agentID).Debug("Retrieving agent")

	url := fmt.Sprintf("%s/api/v1/agent/%s", strings.TrimSuffix(e.baseURL, "/"), agentID)
	resp, err := e.makeRequest(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get agent: %w", err)
	}

	var agentResp AgentResponse
	if err := json.Unmarshal(resp, &agentResp); err != nil {
		e.logger.WithError(err).Error("Failed to unmarshal agent response")
		return nil, fmt.Errorf("failed to unmarshal agent response: %w", err)
	}

	e.logger.WithFields(logrus.Fields{
		"agent_id":    agentResp.AgentID,
		"user_number": agentResp.UserNumber,
		"status":      agentResp.Status,
	}).Debug("Agent retrieved successfully")

	return &agentResp, nil
}

// UpdateAgent updates an existing agent configuration
func (e *EAIAgentService) UpdateAgent(ctx context.Context, agentID string, config *AgentConfiguration) (*AgentResponse, error) {
	e.logger.WithFields(logrus.Fields{
		"agent_id":    agentID,
		"user_number": config.UserNumber,
	}).Debug("Updating agent")

	reqBody, err := json.Marshal(config)
	if err != nil {
		e.logger.WithError(err).Error("Failed to marshal agent configuration")
		return nil, fmt.Errorf("failed to marshal agent configuration: %w", err)
	}

	url := fmt.Sprintf("%s/api/v1/agent/%s", strings.TrimSuffix(e.baseURL, "/"), agentID)
	resp, err := e.makeRequest(ctx, "PUT", url, reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to update agent: %w", err)
	}

	var agentResp AgentResponse
	if err := json.Unmarshal(resp, &agentResp); err != nil {
		e.logger.WithError(err).Error("Failed to unmarshal agent response")
		return nil, fmt.Errorf("failed to unmarshal agent response: %w", err)
	}

	e.logger.WithField("agent_id", agentID).Info("Agent updated successfully")
	return &agentResp, nil
}

// DeleteAgent deletes agents based on the provided criteria
func (e *EAIAgentService) DeleteAgent(ctx context.Context, req *DeleteAgentRequest) (*DeleteAgentResponse, error) {
	e.logger.WithFields(logrus.Fields{
		"agent_id":          req.AgentID,
		"tag_list":          req.TagList,
		"delete_all_agents": req.DeleteAllAgents,
	}).Debug("Deleting agent(s)")

	// Validate that exactly one deletion criterion is provided
	criteriaCount := 0
	if req.AgentID != "" {
		criteriaCount++
	}
	if len(req.TagList) > 0 {
		criteriaCount++
	}
	if req.DeleteAllAgents {
		criteriaCount++
	}

	if criteriaCount != 1 {
		return nil, fmt.Errorf("exactly one deletion criterion must be provided")
	}

	reqBody, err := json.Marshal(req)
	if err != nil {
		e.logger.WithError(err).Error("Failed to marshal delete request")
		return nil, fmt.Errorf("failed to marshal delete request: %w", err)
	}

	url := fmt.Sprintf("%s/api/v1/agent/", strings.TrimSuffix(e.baseURL, "/"))
	resp, err := e.makeRequest(ctx, "DELETE", url, reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to delete agent: %w", err)
	}

	var deleteResp DeleteAgentResponse
	if err := json.Unmarshal(resp, &deleteResp); err != nil {
		e.logger.WithError(err).Error("Failed to unmarshal delete response")
		return nil, fmt.Errorf("failed to unmarshal delete response: %w", err)
	}

	e.logger.WithFields(logrus.Fields{
		"deleted_count": deleteResp.DeletedCount,
		"message":       deleteResp.Message,
	}).Info("Agent(s) deleted successfully")

	return &deleteResp, nil
}

// GetSystemPrompt retrieves a system prompt by ID or type
func (e *EAIAgentService) GetSystemPrompt(ctx context.Context, promptID string) (*SystemPromptResponse, error) {
	e.logger.WithField("prompt_id", promptID).Debug("Retrieving system prompt")

	url := fmt.Sprintf("%s/api/v1/system-prompt/%s", strings.TrimSuffix(e.baseURL, "/"), promptID)
	resp, err := e.makeRequest(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get system prompt: %w", err)
	}

	var promptResp SystemPromptResponse
	if err := json.Unmarshal(resp, &promptResp); err != nil {
		e.logger.WithError(err).Error("Failed to unmarshal system prompt response")
		return nil, fmt.Errorf("failed to unmarshal system prompt response: %w", err)
	}

	e.logger.WithFields(logrus.Fields{
		"prompt_id": promptResp.PromptID,
		"type":      promptResp.Type,
		"version":   promptResp.Version,
	}).Debug("System prompt retrieved successfully")

	return &promptResp, nil
}

// ListSystemPrompts retrieves all available system prompts
func (e *EAIAgentService) ListSystemPrompts(ctx context.Context, tags []string) ([]SystemPromptResponse, error) {
	e.logger.WithField("tags", tags).Debug("Listing system prompts")

	url := fmt.Sprintf("%s/api/v1/system-prompt", strings.TrimSuffix(e.baseURL, "/"))
	if len(tags) > 0 {
		url += "?tags=" + strings.Join(tags, ",")
	}

	resp, err := e.makeRequest(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to list system prompts: %w", err)
	}

	var prompts []SystemPromptResponse
	if err := json.Unmarshal(resp, &prompts); err != nil {
		e.logger.WithError(err).Error("Failed to unmarshal system prompts response")
		return nil, fmt.Errorf("failed to unmarshal system prompts response: %w", err)
	}

	e.logger.WithField("count", len(prompts)).Debug("System prompts listed successfully")
	return prompts, nil
}

// HealthCheck checks if the EAI Agent Service is healthy
func (e *EAIAgentService) HealthCheck(ctx context.Context) error {
	url := fmt.Sprintf("%s/health", strings.TrimSuffix(e.baseURL, "/"))

	_, err := e.makeRequest(ctx, "GET", url, nil)
	if err != nil {
		e.logger.WithError(err).Error("EAI Agent Service health check failed")
		return fmt.Errorf("EAI Agent Service health check failed: %w", err)
	}

	e.logger.Debug("EAI Agent Service health check passed")
	return nil
}

// makeRequest makes an HTTP request to the EAI Agent Service
func (e *EAIAgentService) makeRequest(ctx context.Context, method, url string, body []byte) ([]byte, error) {
	var reqBody io.Reader
	if body != nil {
		reqBody = bytes.NewReader(body)
	}

	req, err := http.NewRequestWithContext(ctx, method, url, reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	// Set headers
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "EAI-Gateway-Go/1.0")

	if e.token != "" {
		req.Header.Set("Authorization", "Bearer "+e.token)
	}

	e.logger.WithFields(logrus.Fields{
		"method":   method,
		"url":      url,
		"has_body": body != nil,
	}).Debug("Making HTTP request to EAI Agent Service")

	resp, err := e.httpClient.Do(req)
	if err != nil {
		e.logger.WithError(err).WithFields(logrus.Fields{
			"method": method,
			"url":    url,
		}).Error("HTTP request failed")
		return nil, fmt.Errorf("HTTP request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		e.logger.WithError(err).Error("Failed to read response body")
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		e.logger.WithFields(logrus.Fields{
			"status_code": resp.StatusCode,
			"method":      method,
			"url":         url,
			"response":    string(respBody),
		}).Error("HTTP request returned error status")
		return nil, fmt.Errorf("HTTP request failed with status %d: %s", resp.StatusCode, string(respBody))
	}

	e.logger.WithFields(logrus.Fields{
		"status_code":   resp.StatusCode,
		"response_size": len(respBody),
	}).Debug("HTTP request completed successfully")

	return respBody, nil
}

// GetDefaultAgentConfiguration returns a default agent configuration with service defaults
func (e *EAIAgentService) GetDefaultAgentConfiguration(userNumber string) *AgentConfiguration {
	return &AgentConfiguration{
		UserNumber: userNumber,
		AgentType:  "memgpt_v2_agent",
		Name:       "",
		Tags:       []string{"agentic_search"},
		System:     "You are an AI assistant designed to help users with information retrieval and task completion. You have access to search tools and can browse the web to provide accurate, up-to-date information.",
		MemoryBlocks: []MemoryBlock{
			{
				Type:    "persona",
				Content: "I am an AI assistant designed to help users with various tasks and information retrieval.",
			},
			{
				Type:    "human",
				Content: fmt.Sprintf("User: %s", userNumber),
			},
		},
		Tools:                []string{"google_search", "web_search_surkai"},
		Model:                e.config.EAIAgent.LLMModel,
		Embedding:            e.config.EAIAgent.EmbeddingModel,
		ContextWindowLimit:   e.config.EAIAgent.ContextWindowLimit,
		IncludeBaseToolRules: true,
		IncludeBaseTools:     true,
		Timezone:             "America/Sao_Paulo",
	}
}
