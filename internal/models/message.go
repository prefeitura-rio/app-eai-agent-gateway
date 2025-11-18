package models

import (
	"time"

	"github.com/google/uuid"
)

// UserWebhookRequest represents the request payload for user webhook (matches Python API)
type UserWebhookRequest struct {
	UserNumber      string                 `json:"user_number" binding:"required" example:"5521999999999"`
	PreviousMessage *string                `json:"previous_message,omitempty" example:"Previous message context"`
	Message         string                 `json:"message" binding:"required" example:"Hello, how can you help me?"`
	Metadata        map[string]interface{} `json:"metadata,omitempty"`
	Provider        *string                `json:"provider,omitempty" example:"google_agent_engine"`
	CallbackURL     *string                `json:"callback_url,omitempty" binding:"omitempty,url" example:"https://example.com/webhook/callback"`
}

// WebhookResponse represents the response for webhook endpoints (matches Python API)
type WebhookResponse struct {
	MessageID       string `json:"message_id" example:"123e4567-e89b-12d3-a456-426614174000"`
	Status          string `json:"status" example:"processing"`
	PollingEndpoint string `json:"polling_endpoint" example:"/api/v1/message/response?message_id=123e4567-e89b-12d3-a456-426614174000"`
}

// MessageResponseRequest represents the query parameters for message response endpoint
type MessageResponseRequest struct {
	MessageID string `form:"message_id" binding:"required,uuid" example:"123e4567-e89b-12d3-a456-426614174000"`
}

// MessageResponse represents the response structure for message polling (matches Python API)
// @Description Message processing response
type MessageResponse struct {
	Status string      `json:"status" example:"completed"`
	Data   interface{} `json:"data,omitempty" swaggertype:"object"`
	Error  *string     `json:"error,omitempty" example:"Error message if processing failed"`
}

// ProcessedMessageData represents the data structure inside the response (matches Python API)
type ProcessedMessageData struct {
	Messages    interface{}            `json:"messages" swaggertype:"array"`
	AgentID     string                 `json:"agent_id" example:"user_12345"`
	ProcessedAt string                 `json:"processed_at" example:"task-uuid-or-timestamp"`
	Status      string                 `json:"status" example:"done"`
	Metadata    map[string]interface{} `json:"metadata,omitempty"` // Original metadata from webhook request
}

// TaskStatus represents the status of a message processing task
type TaskStatus string

const (
	TaskStatusPending    TaskStatus = "pending"
	TaskStatusProcessing TaskStatus = "processing"
	TaskStatusCompleted  TaskStatus = "completed"
	TaskStatusFailed     TaskStatus = "failed"
)

// TaskDebugInfo represents debug information for a task
type TaskDebugInfo struct {
	MessageID     string                 `json:"message_id"`
	Status        TaskStatus             `json:"status"`
	CreatedAt     time.Time              `json:"created_at"`
	UpdatedAt     time.Time              `json:"updated_at"`
	RetryCount    int                    `json:"retry_count"`
	LastError     *string                `json:"last_error,omitempty"`
	QueueInfo     map[string]interface{} `json:"queue_info,omitempty"`
	ProcessingLog []string               `json:"processing_log,omitempty"`
}

// QueueMessage represents a message in the queue
type QueueMessage struct {
	ID              string                 `json:"id"`
	Type            string                 `json:"type"`
	UserNumber      string                 `json:"user_number,omitempty"`
	AgentID         string                 `json:"agent_id,omitempty"`
	Message         string                 `json:"message"`
	PreviousMessage *string                `json:"previous_message,omitempty"`
	Provider        string                 `json:"provider,omitempty"`
	Timestamp       time.Time              `json:"timestamp"`
	Metadata        map[string]interface{} `json:"metadata,omitempty"`
}

// Note: Agent management models removed - were Letta-specific
// Google Agent Engine handles agent lifecycle automatically via threads

// AgentResponse represents a response from an AI agent
type AgentResponse struct {
	Content   string                 `json:"content"`
	ThreadID  string                 `json:"thread_id"`
	MessageID string                 `json:"message_id"`
	Metadata  map[string]interface{} `json:"metadata"`
	Usage     *UsageMetadata         `json:"usage,omitempty"`
}

// UsageMetadata contains usage statistics from the agent
type UsageMetadata struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
	TotalTokens  int `json:"total_tokens"`
}

// WorkerType represents the type of worker
type WorkerType string

const (
	WorkerTypeUserMessage WorkerType = "user_message"
)

// GenerateMessageID creates a new UUID for message tracking
func GenerateMessageID() string {
	return uuid.New().String()
}

// IsValidUUID checks if a string is a valid UUID
func IsValidUUID(u string) bool {
	_, err := uuid.Parse(u)
	return err == nil
}

// CallbackPayload represents the payload sent to callback URLs
type CallbackPayload struct {
	MessageID   string      `json:"message_id"`
	Status      string      `json:"status"`
	Data        interface{} `json:"data,omitempty"`
	Error       *string     `json:"error,omitempty"`
	Timestamp   string      `json:"timestamp"`
	ProcessedAt string      `json:"processed_at"`
}

// CallbackInfo represents callback metadata stored in Redis
type CallbackInfo struct {
	URL         string    `json:"url"`
	MessageID   string    `json:"message_id"`
	RetryCount  int       `json:"retry_count"`
	LastAttempt time.Time `json:"last_attempt,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
}
