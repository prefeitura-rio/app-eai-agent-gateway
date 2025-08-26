package workers

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/sirupsen/logrus"

	"github.com/prefeitura-rio/app-eai-agent-gateway/internal/models"
)

// UserMessageWorker processes user messages and sends them to Google Agent Engine
type UserMessageWorker struct {
	*BaseWorker
}

// NewUserMessageWorker creates a new user message worker
func NewUserMessageWorker(
	concurrency int,
	deps *WorkerDependencies,
	rabbitMQ RabbitMQServiceInterface,
	consumerMgr ConsumerManagerInterface,
) *UserMessageWorker {
	base := NewBaseWorker(
		models.WorkerTypeUserMessage,
		"user_messages", // queue name
		concurrency,
		deps,
		rabbitMQ,
		consumerMgr,
	)

	return &UserMessageWorker{
		BaseWorker: base,
	}
}

// ProcessMessage processes a user message from the queue
func (w *UserMessageWorker) ProcessMessage(ctx context.Context, delivery amqp.Delivery) MessageProcessingResult {
	start := time.Now()

	// Parse the message
	queueMessage, err := w.parseMessage(delivery)
	if err != nil {
		return MessageProcessingResult{
			Success:  false,
			Error:    fmt.Errorf("failed to parse message: %w", err),
			Duration: time.Since(start),
		}
	}

	// Create processing context
	procCtx := NewProcessingContext(queueMessage, w.deps.Logger)

	procCtx.Logger.Info("Processing user message")

	// Parse the user webhook request from the queue message content
	var userRequest models.UserWebhookRequest
	if err := json.Unmarshal([]byte(queueMessage.Message), &userRequest); err != nil {
		procCtx.Logger.WithError(err).Error("Failed to unmarshal user webhook request")
		return MessageProcessingResult{
			Success:  false,
			Error:    fmt.Errorf("failed to unmarshal user webhook request: %w", err),
			Duration: time.Since(start),
		}
	}

	// Validate user request
	if err := w.validateUserRequest(&userRequest); err != nil {
		procCtx.Logger.WithError(err).Error("User request validation failed")
		return MessageProcessingResult{
			Success:  false,
			Error:    fmt.Errorf("user request validation failed: %w", err),
			Duration: time.Since(start),
		}
	}

	// Process the message content (handle audio if needed)
	content, err := w.processMessageContent(ctx, &userRequest, procCtx)
	if err != nil {
		procCtx.Logger.WithError(err).Error("Failed to process message content")
		return MessageProcessingResult{
			Success:  false,
			Error:    fmt.Errorf("failed to process message content: %w", err),
			Duration: time.Since(start),
		}
	}

	// Get or create thread for the user
	threadID, err := w.deps.AgentClient.GetOrCreateThread(ctx, userRequest.UserNumber)
	if err != nil {
		procCtx.Logger.WithError(err).Error("Failed to get or create thread")
		return MessageProcessingResult{
			Success:  false,
			Error:    fmt.Errorf("failed to get or create thread: %w", err),
			Duration: time.Since(start),
		}
	}

	procCtx.ThreadID = threadID
	procCtx.Logger = procCtx.Logger.WithField("thread_id", threadID)

	// Send message to Google Agent Engine
	agentResponse, err := w.deps.AgentClient.SendMessage(ctx, threadID, content)
	if err != nil {
		procCtx.Logger.WithError(err).Error("Failed to send message to agent")
		return MessageProcessingResult{
			Success:  false,
			Error:    fmt.Errorf("failed to send message to agent: %w", err),
			Duration: time.Since(start),
		}
	}

	// Store the agent response in the format expected by the message handler
	// The Google Agent Engine Content field already contains the full response JSON
	// We need to parse it and wrap it in the expected structure

	// Clean the response content first to handle potential JSON issues
	cleanedContent := strings.TrimSpace(agentResponse.Content)

	var agentResponseContent map[string]interface{}
	if err := json.Unmarshal([]byte(cleanedContent), &agentResponseContent); err != nil {
		// If it's not JSON, treat it as a simple text response
		responseData := map[string]interface{}{
			"output": map[string]interface{}{
				"messages": []map[string]interface{}{
					{
						"id":      agentResponse.MessageID,
						"type":    "ai",
						"content": agentResponse.Content,
						"response_metadata": map[string]interface{}{
							"usage_metadata": agentResponse.Usage,
						},
					},
				},
			},
		}

		responseBytes, marshalErr := json.Marshal(responseData)
		if marshalErr != nil {
			procCtx.Logger.WithError(marshalErr).Error("Failed to marshal simple response data")
			return MessageProcessingResult{
				Success:  false,
				Error:    fmt.Errorf("failed to marshal simple response data: %w", marshalErr),
				Duration: time.Since(start),
			}
		}
		responseString := string(responseBytes)

		return MessageProcessingResult{
			Success:  true,
			Content:  &responseString,
			Duration: time.Since(start),
		}
	}

	// If it's already JSON (from Google Agent Engine), wrap it in the expected output structure
	responseData := map[string]interface{}{
		"output": agentResponseContent,
	}

	responseBytes, err := json.Marshal(responseData)
	if err != nil {
		procCtx.Logger.WithError(err).Error("Failed to marshal response data")
		return MessageProcessingResult{
			Success:  false,
			Error:    fmt.Errorf("failed to marshal response data: %w", err),
			Duration: time.Since(start),
		}
	}

	responseString := string(responseBytes)

	// Validate that the JSON we're storing is valid by trying to parse it back
	var validationTest map[string]interface{}
	if err := json.Unmarshal(responseBytes, &validationTest); err != nil {
		procCtx.Logger.WithError(err).Error("Generated invalid JSON response")
		return MessageProcessingResult{
			Success:  false,
			Error:    fmt.Errorf("generated invalid JSON response: %w", err),
			Duration: time.Since(start),
		}
	}

	procCtx.Logger.WithFields(logrus.Fields{
		"agent_message_id": agentResponse.MessageID,
		"content_length":   len(agentResponse.Content),
		"usage":            agentResponse.Usage,
	}).Info("User message processed successfully")

	return MessageProcessingResult{
		Success:  true,
		Content:  &responseString,
		Duration: time.Since(start),
	}
}

// validateUserRequest validates the user webhook request
func (w *UserMessageWorker) validateUserRequest(req *models.UserWebhookRequest) error {
	if req.UserNumber == "" {
		return fmt.Errorf("user_id is required")
	}

	if req.Message == "" {
		return fmt.Errorf("content is required")
	}

	// Validate message content
	if err := w.deps.MessageFormatter.ValidateMessageContent(req.Message); err != nil {
		return fmt.Errorf("invalid message content: %w", err)
	}

	return nil
}

// processMessageContent processes the message content, handling audio transcription if needed
func (w *UserMessageWorker) processMessageContent(ctx context.Context, req *models.UserWebhookRequest, procCtx *ProcessingContext) (string, error) {
	content := req.Message

	// Check if content is an audio URL
	if w.deps.TranscribeService.IsAudioURL(content) {
		procCtx.Logger.WithField("audio_url", content).Info("Processing audio message")

		// Validate audio URL
		if err := w.deps.TranscribeService.ValidateAudioURL(content); err != nil {
			return "", fmt.Errorf("invalid audio URL: %w", err)
		}

		// Transcribe audio
		transcribedText, err := w.deps.TranscribeService.TranscribeAudio(ctx, content)
		if err != nil {
			return "", fmt.Errorf("failed to transcribe audio: %w", err)
		}

		procCtx.Logger.WithFields(logrus.Fields{
			"original_url":     content,
			"transcribed_text": transcribedText,
			"text_length":      len(transcribedText),
		}).Info("Audio transcribed successfully")

		content = transcribedText
	}

	return content, nil
}
