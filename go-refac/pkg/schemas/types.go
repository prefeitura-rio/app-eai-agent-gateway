package schemas

import (
	"time"
)

// Payloads de entrada da API
type AgentWebhookRequest struct {
    AgentID string `json:"agent_id" binding:"required"`
    Message string `json:"message" binding:"required"`
}

type UserWebhookRequest struct {
    UserNumber      string  `json:"user_number" binding:"required"`
    Message         string  `json:"message" binding:"required"`
    PreviousMessage *string `json:"previous_message"`
    Provider        string  `json:"provider"`
}

// Mensagens publicadas no RabbitMQ
type UserMessage struct {
    MessageID       string     `json:"message_id"`
    Provider        string     `json:"provider"`
    UserNumber      string     `json:"user_number"`
    Message         string     `json:"message"`
    PreviousMessage *string    `json:"previous_message"`
    Attempt         int        `json:"attempt"`
    MaxRetries      int        `json:"max_retries"`
    CreatedAt       time.Time  `json:"created_at"`
}

type AgentMessage struct {
    MessageID  string    `json:"message_id"`
    Provider   string    `json:"provider"`
    AgentID    string    `json:"agent_id"`
    Message    string    `json:"message"`
    Attempt    int       `json:"attempt"`
    MaxRetries int       `json:"max_retries"`
    CreatedAt  time.Time `json:"created_at"`
}

// Estrutura de resposta armazenada no Redis (compatível com Python)
type TaskResult struct {
    Status     string       `json:"status"` // processing | retry | error | done
    Message    string       `json:"message,omitempty"`
    Error      string       `json:"error,omitempty"`
    RetryCount int          `json:"retry_count,omitempty"`
    MaxRetries int          `json:"max_retries,omitempty"`
    Data       *ResultData  `json:"data,omitempty"`
}

type ResultData struct {
    Messages   []Message `json:"messages"`
    Usage      Usage     `json:"usage"`
    AgentID    string              `json:"agent_id"`
    ProcessedAt string             `json:"processed_at"`
}

// Tipos padronizados de mensagem e usage entre providers
type Message struct {
    Role    string `json:"role"`
    Content string `json:"content"`
}

type Usage struct {
    InputTokens  int `json:"input_tokens"`
    OutputTokens int `json:"output_tokens"`
    TotalTokens  int `json:"total_tokens"`
}


