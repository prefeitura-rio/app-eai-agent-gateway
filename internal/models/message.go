package models

import (
	"strings"
	"time"

	"github.com/google/uuid"
)

// KnownMessageTypesList mirrors inbound-capable media types from
// prefeitura-rio/app-mcp-server/media-types.yaml plus Gateway sentinel values.
// Gateway intentionally remains pass-through for media payload shape; semantic
// validation belongs to MCP tools.
var KnownMessageTypesList = []string{
	"text",
	"audio",
	"image",
	"video",
	"location",
	"interactive",
	"unsupported",
	"unknown",
}

var knownMessageTypesSet = func() map[string]bool {
	set := make(map[string]bool, len(KnownMessageTypesList))
	for _, messageType := range KnownMessageTypesList {
		set[messageType] = true
	}
	return set
}()

// IsKnownMessageType reports whether message_type is supported by the Gateway
// input contract. Empty is allowed because absent message_type preserves legacy
// text behavior.
func IsKnownMessageType(messageType string) bool {
	if messageType == "" {
		return true
	}
	return knownMessageTypesSet[messageType]
}

// AllowsEmptyMedia reports whether message_type can carry only its sentinel
// classification without a media metadata object.
func AllowsEmptyMedia(messageType string) bool {
	return messageType == "unsupported" || messageType == "unknown"
}

// HistoryMessage represents a single message in the history update
type HistoryMessage struct {
	Content string `json:"content" binding:"required" example:"Hello, World!"`
	Role    string `json:"role" binding:"required" example:"ai"`
}

// HistoryUpdateWebhookRequest represents the request payload for history update webhook
type HistoryUpdateWebhookRequest struct {
	UserNumber        string                 `json:"user_number" binding:"required" example:"5521999999999"`
	Messages          []HistoryMessage       `json:"messages" binding:"required,min=1"`
	Metadata          map[string]interface{} `json:"metadata,omitempty"`
	ReasoningEngineID *string                `json:"reasoning_engine_id,omitempty" example:"12345678"`
}

// UserWebhookRequest represents the request payload for user webhook (matches Python API)
type UserWebhookRequest struct {
	UserNumber      string  `json:"user_number" binding:"required" example:"5521999999999"`
	PreviousMessage *string `json:"previous_message,omitempty" example:"Previous message context"`
	// Message is optional when MessageType != "text" and Media is provided
	// (media-only payloads, e.g. raw image without caption). Handler enforces
	// "either Message non-empty OR (MessageType non-text + Media)" semantic
	// after JSON bind. Pre-existing callers (with always-populated text) keep
	// working unchanged.
	Message           string                 `json:"message" example:"Hello, how can you help me?"`
	Metadata          map[string]interface{} `json:"metadata,omitempty"`
	Provider          *string                `json:"provider,omitempty" example:"google_agent_engine"`
	CallbackURL       *string                `json:"callback_url,omitempty" binding:"omitempty,url" example:"https://example.com/webhook/callback"`
	ReasoningEngineID *string                `json:"reasoning_engine_id,omitempty" example:"12345678"`
	// MessageType discriminates inbound media kinds when caller (Mule, etc.) sends
	// non-text payloads. Inbound-capable values follow the canonical registry in
	// prefeitura-rio/app-mcp-server/media-types.yaml: "text", "audio", "image",
	// "video", "location", "interactive". Gateway also accepts sentinel values
	// "unsupported" and "unknown". Worker uses it to enrich `Message` with an
	// [INBOUND_MEDIA] prefix so the downstream LLM can call MCP tools. Optional;
	// absent or "text" preserves legacy behavior.
	MessageType *string `json:"message_type,omitempty" example:"image"`
	// Media carries the media metadata when MessageType is non-text.
	// Pass-through `map[string]interface{}` to avoid coupling to any specific
	// upstream schema; serialized as-is into the enriched Message body for the
	// LLM to extract. Source upstream: Salesforce Apex (study-sf-whatsapp-poc1)
	// → Mule sc-inbound-flow.
	//
	// Chaves esperadas/conhecidas (mantidas em sync com o tool MCP
	// `register_inbound_media` em prefeitura-rio/app-mcp-server e ADR-012 em
	// study-sf-whatsapp-poc1). Typos passam silenciosos por design — gateway
	// é pass-through; validação semântica fica no MCP tool. Atualizar este
	// comentário sempre que o tool aceitar/exigir nova chave.
	//
	// Pra image/audio/video (BSP entrega via ContentVersion auto-attachado):
	//   - content_version_id   (string, SF Id 18-char)
	//   - content_document_id  (string, SF Id 18-char)
	//   - file_extension       (string, lowercase: "jpg"|"png"|"oga"|"ogg"|"mp4"|...)
	//   - file_size_bytes      (number)
	//   - download_path        (string, REST path: "/services/data/v62.0/sobjects/ContentVersion/{Id}/VersionData")
	//
	// Pra location (atualmente não entregue pelo BSP; placeholder pra BYOC futuro):
	//   - latitude             (number, -90..90)
	//   - longitude            (number, -180..180)
	//   - address              (string, opcional)
	//   - name                 (string, opcional — point-of-interest)
	//
	// Pra interactive: Media carrega sub-shapes de WhatsApp interactive inbound
	// (button_reply, list_reply, nfm_reply/Flow submission) sem validação local.
	//
	// Pra unsupported/unknown: Media pode ser nil ou {} — tipo já carrega o
	// signal suficiente, sem metadata útil pra MCP processar.
	Media map[string]interface{} `json:"media,omitempty"`
}

func KnownMessageTypesDescription() string {
	return strings.Join(KnownMessageTypesList, ", ")
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
	Messages    interface{} `json:"messages" swaggertype:"array"`
	AgentID     string      `json:"agent_id" example:"user_12345"`
	ProcessedAt string      `json:"processed_at" example:"task-uuid-or-timestamp"`
	Status      string      `json:"status" example:"done"`
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
	ID                string                 `json:"id"`
	Type              string                 `json:"type"` // "user_message" or "history_update"
	UserNumber        string                 `json:"user_number,omitempty"`
	AgentID           string                 `json:"agent_id,omitempty"`
	Message           string                 `json:"message,omitempty"`  // For user_message type
	Messages          []HistoryMessage       `json:"messages,omitempty"` // For history_update type
	PreviousMessage   *string                `json:"previous_message,omitempty"`
	Provider          string                 `json:"provider,omitempty"`
	Timestamp         time.Time              `json:"timestamp"`
	Metadata          map[string]interface{} `json:"metadata,omitempty"`
	ReasoningEngineID *string                `json:"reasoning_engine_id,omitempty"`
	// MessageType + Media propagated from UserWebhookRequest. Worker reads these
	// before invoking the agent provider and enriches Message accordingly.
	MessageType *string                `json:"message_type,omitempty"`
	Media       map[string]interface{} `json:"media,omitempty"`
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
	UserNumber  string    `json:"user_number"` // WhatsApp number for error tracking
	RetryCount  int       `json:"retry_count"`
	LastAttempt time.Time `json:"last_attempt,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
}

// UserLastActivityResponse represents the response for user last activity endpoint
type UserLastActivityResponse struct {
	UserNumber           string `json:"user_number" example:"5521999999999"`
	LastMessageTimestamp string `json:"last_message_timestamp" example:"2026-03-24T14:30:00Z"`
	Cached               bool   `json:"cached" example:"true"`
	TTLSeconds           int    `json:"ttl_seconds" example:"86400"`
}
