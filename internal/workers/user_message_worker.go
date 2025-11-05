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

	// Check if this is a history update message
	if queueMessage.Type == "history_update" {
		return w.processHistoryUpdate(ctx, queueMessage, procCtx, start)
	}

	procCtx.Logger.Info("Processing user message")

	// Process the message content (handle audio if needed)
	content := queueMessage.Message
	if w.deps.TranscribeService != nil && w.deps.TranscribeService.IsAudioURL(content) {
		procCtx.Logger.WithField("audio_url", content).Debug("Detected audio message, transcribing")

		transcribedText, err := w.deps.TranscribeService.TranscribeAudio(ctx, content)
		if err != nil {
			procCtx.Logger.WithError(err).Error("Failed to transcribe audio")
			return MessageProcessingResult{
				Success:  false,
				Error:    fmt.Errorf("failed to transcribe audio: %w", err),
				Duration: time.Since(start),
			}
		}

		procCtx.Logger.WithFields(logrus.Fields{
			"original_url":     content,
			"transcribed_text": transcribedText,
			"text_length":      len(transcribedText),
		}).Info("Audio transcribed successfully")

		content = transcribedText
	}

	// Get or create thread for the user
	threadID, err := w.deps.AgentClient.GetOrCreateThread(ctx, queueMessage.UserNumber)
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

	// If there's a previous_message, send it as history update first with role="ai"
	if queueMessage.PreviousMessage != nil && *queueMessage.PreviousMessage != "" {
		procCtx.Logger.WithField("previous_message_length", len(*queueMessage.PreviousMessage)).Debug("Sending previous message as history update")

		historyMessages := []models.HistoryMessage{
			{
				Content: *queueMessage.PreviousMessage,
				Role:    "ai",
			},
		}

		_, err := w.deps.AgentClient.SendHistoryUpdate(ctx, threadID, historyMessages, queueMessage.ReasoningEngineID)
		if err != nil {
			procCtx.Logger.WithError(err).Error("Failed to send previous message as history update")
			return MessageProcessingResult{
				Success:  false,
				Error:    fmt.Errorf("failed to send previous message as history: %w", err),
				Duration: time.Since(start),
			}
		}

		procCtx.Logger.Info("Previous message added to history successfully")
	}

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

// processHistoryUpdate handles history update messages
func (w *UserMessageWorker) processHistoryUpdate(ctx context.Context, queueMessage *models.QueueMessage, procCtx *ProcessingContext, start time.Time) MessageProcessingResult {
	procCtx.Logger.Info("Processing history update message")

	// Validate that we have messages
	if len(queueMessage.Messages) == 0 {
		procCtx.Logger.Error("History update message has no messages")
		return MessageProcessingResult{
			Success:  false,
			Error:    fmt.Errorf("history update message has no messages"),
			Duration: time.Since(start),
		}
	}

	// Process audio URLs in messages if needed
	processedMessages := make([]models.HistoryMessage, len(queueMessage.Messages))
	for i, msg := range queueMessage.Messages {
		content := msg.Content

		// Check if content is an audio URL and transcribe if needed
		if w.deps.TranscribeService != nil && w.deps.TranscribeService.IsAudioURL(content) {
			procCtx.Logger.WithFields(logrus.Fields{
				"message_index": i,
				"audio_url":     content,
			}).Debug("Detected audio URL in history message, transcribing")

			transcribedText, err := w.deps.TranscribeService.TranscribeAudio(ctx, content)
			if err != nil {
				procCtx.Logger.WithError(err).WithField("message_index", i).Error("Failed to transcribe audio in history message")
				return MessageProcessingResult{
					Success:  false,
					Error:    fmt.Errorf("failed to transcribe audio in message %d: %w", i, err),
					Duration: time.Since(start),
				}
			}

			procCtx.Logger.WithFields(logrus.Fields{
				"message_index":    i,
				"original_url":     content,
				"transcribed_text": transcribedText,
				"text_length":      len(transcribedText),
			}).Info("Audio in history message transcribed successfully")

			content = transcribedText
		}

		processedMessages[i] = models.HistoryMessage{
			Content: content,
			Role:    msg.Role,
		}
	}

	// Get or create thread for the user
	threadID, err := w.deps.AgentClient.GetOrCreateThread(ctx, queueMessage.UserNumber)
	if err != nil {
		procCtx.Logger.WithError(err).Error("Failed to get or create thread for history update")
		return MessageProcessingResult{
			Success:  false,
			Error:    fmt.Errorf("failed to get or create thread: %w", err),
			Duration: time.Since(start),
		}
	}

	procCtx.ThreadID = threadID
	procCtx.Logger = procCtx.Logger.WithField("thread_id", threadID)

	// Send history update to Google Agent Engine
	resp, err := w.deps.AgentClient.SendHistoryUpdate(ctx, threadID, processedMessages, queueMessage.ReasoningEngineID)
	if err != nil {
		procCtx.Logger.WithError(err).Error("Failed to send history update to agent")
		return MessageProcessingResult{
			Success:  false,
			Error:    fmt.Errorf("failed to send history update to agent: %w", err),
			Duration: time.Since(start),
		}
	}

	// Marshal the response to JSON
	responseBytes, err := json.Marshal(resp)
	if err != nil {
		procCtx.Logger.WithError(err).Error("Failed to marshal history update response")
		return MessageProcessingResult{
			Success:  false,
			Error:    fmt.Errorf("failed to marshal response: %w", err),
			Duration: time.Since(start),
		}
	}

	responseString := string(responseBytes)

	procCtx.Logger.WithFields(logrus.Fields{
		"thread_id":      threadID,
		"messages_count": len(processedMessages),
		"duration_ms":    time.Since(start).Milliseconds(),
		"response":       responseString,
	}).Info("History update processed successfully")

	return MessageProcessingResult{
		Success:  true,
		Content:  &responseString,
		Duration: time.Since(start),
	}
}
