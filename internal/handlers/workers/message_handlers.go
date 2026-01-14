package workers

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/sirupsen/logrus"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/prefeitura-rio/app-eai-agent-gateway/internal/config"
	"github.com/prefeitura-rio/app-eai-agent-gateway/internal/middleware"
	"github.com/prefeitura-rio/app-eai-agent-gateway/internal/models"
	"github.com/prefeitura-rio/app-eai-agent-gateway/internal/services"
)

// MessageHandlerDependencies contains dependencies needed for message processing
type MessageHandlerDependencies struct {
	Logger             *logrus.Logger
	Config             *config.Config
	RedisService       *services.RedisService
	GoogleAgentService *services.GoogleAgentEngineService
	TranscribeService  TranscribeServiceInterface
	MessageFormatter   MessageFormatterInterface
	CallbackService    *services.CallbackService              // Optional callback service
	OTelWorkerWrapper  *middleware.OTelWorkerWrapper          // Optional OTel wrapper
	TracePropagator    *middleware.TraceCorrelationPropagator // Optional trace propagator
}

// TranscribeServiceInterface defines audio transcription operations
type TranscribeServiceInterface interface {
	TranscribeAudio(ctx context.Context, audioURL string) (string, error)
	IsAudioURL(url string) bool
	ValidateAudioURL(url string) error
}

// TranscribeServiceAdapter adapts the services.TranscribeService to the handler interface
type TranscribeServiceAdapter struct {
	service *services.TranscribeService
}

// NewTranscribeServiceAdapter creates a new adapter
func NewTranscribeServiceAdapter(service *services.TranscribeService) *TranscribeServiceAdapter {
	return &TranscribeServiceAdapter{service: service}
}

// TranscribeAudio implements the interface by calling TranscribeFromURL
func (a *TranscribeServiceAdapter) TranscribeAudio(ctx context.Context, audioURL string) (string, error) {
	if a.service == nil {
		return "", fmt.Errorf("transcribe service is not available")
	}
	result, err := a.service.TranscribeFromURL(ctx, audioURL)
	if err != nil {
		return "", err
	}
	return result.Text, nil
}

// IsAudioURL checks if the URL appears to be an audio file
func (a *TranscribeServiceAdapter) IsAudioURL(url string) bool {
	// Check for audio file extensions regardless of service availability
	// This allows detection even when transcribe service is not configured
	audioExtensions := []string{".mp3", ".wav", ".m4a", ".aac", ".ogg", ".oga", ".flac", ".wma", ".opus"}
	for _, ext := range audioExtensions {
		if strings.HasSuffix(strings.ToLower(url), ext) {
			return true
		}
	}
	return false
}

// ValidateAudioURL validates the audio URL format
func (a *TranscribeServiceAdapter) ValidateAudioURL(url string) error {
	if a.service == nil {
		return fmt.Errorf("transcribe service is not available")
	}
	if url == "" {
		return fmt.Errorf("audio URL cannot be empty")
	}
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		return fmt.Errorf("audio URL must start with http:// or https://")
	}
	return nil
}

// MessageFormatterInterface defines message formatting operations
type MessageFormatterInterface interface {
	FormatForWhatsApp(ctx context.Context, response *models.AgentResponse) (string, error)
	FormatErrorMessage(ctx context.Context, err error) string
	ValidateMessageContent(content string) error
}

// CreateUserMessageHandler creates a handler for user messages
func CreateUserMessageHandler(deps *MessageHandlerDependencies) func(context.Context, amqp.Delivery) error {
	return func(ctx context.Context, delivery amqp.Delivery) error {
		logger := deps.Logger.WithFields(logrus.Fields{
			"handler":      "user_message",
			"delivery_tag": delivery.DeliveryTag,
			"message_id":   delivery.MessageId,
		})

		// Extract trace context from RabbitMQ headers if available
		if deps.TracePropagator != nil && delivery.Headers != nil {
			traceHeaders := make(map[string]string)
			for k, v := range delivery.Headers {
				if str, ok := v.(string); ok {
					traceHeaders[k] = str
				}
			}
			if len(traceHeaders) > 0 {
				ctx = deps.TracePropagator.ExtractTraceContext(ctx, traceHeaders)
				logger.Debug("Extracted distributed trace context from RabbitMQ headers")
			}
		}

		logger.Info("Processing user message")

		// Parse the queue message
		var queueMsg models.QueueMessage
		if err := json.Unmarshal(delivery.Body, &queueMsg); err != nil {
			logger.WithError(err).Error("Failed to unmarshal queue message")
			// Return error for malformed messages (service layer will handle nack)
			return err
		}

		logger = logger.WithFields(logrus.Fields{
			"queue_message_id": queueMsg.ID,
			"user_number":      queueMsg.UserNumber,
			"message_type":     queueMsg.Type,
			"provider":         queueMsg.Provider,
		})

		// Update task status to processing
		if err := deps.RedisService.SetTaskStatus(ctx, queueMsg.ID, string(models.TaskStatusProcessing), deps.Config.Redis.TaskStatusTTL); err != nil {
			logger.WithError(err).Error("Failed to update task status to processing")
		}

		// Process the user message with optional OTel tracing
		var response string
		var err error

		if deps.OTelWorkerWrapper != nil {
			// Wrap with OpenTelemetry tracing
			err = deps.OTelWorkerWrapper.WrapWorkerTask(ctx, "user_message_worker", "process_user_message", func(tracedCtx context.Context) error {
				// Detect message type early for tracing attributes
				isAudio := isAudioURL(queueMsg.Message)

				// Add message type attribute to current span if possible
				if span := trace.SpanFromContext(tracedCtx); span.IsRecording() {
					span.SetAttributes(
						attribute.Bool("message.is_audio", isAudio),
						attribute.String("message.type", func() string {
							if isAudio {
								return "audio"
							}
							return "text"
						}()),
						attribute.Int("message.length", len(queueMsg.Message)),
					)
				}

				response, err = processUserMessage(tracedCtx, &queueMsg, deps)
				return err
			})
		} else {
			// Process without tracing
			response, err = processUserMessage(ctx, &queueMsg, deps)
		}

		if err != nil {
			logger.WithError(err).Error("Failed to process user message")

			// Store error in Redis
			errorKey := "task:error:" + queueMsg.ID
			if redisErr := deps.RedisService.Set(ctx, errorKey, err.Error(), deps.Config.Redis.TaskStatusTTL); redisErr != nil {
				logger.WithError(redisErr).Error("Failed to store error in Redis")
			}

			// Determine if this error should be retried
			shouldRetry := isRetriableError(err)

			if shouldRetry {
				logger.WithFields(logrus.Fields{
					"retriable":     true,
					"error_type":    "retriable",
					"error_message": err.Error(),
				}).Warn("Error is retriable, will be retried by RabbitMQ")
				// Update task status to processing (keep it processing for retry)
				if statusErr := deps.RedisService.SetTaskStatus(ctx, queueMsg.ID, string(models.TaskStatusProcessing), deps.Config.Redis.TaskStatusTTL); statusErr != nil {
					logger.WithError(statusErr).Error("Failed to update task status to processing for retry")
				}
				// Add retriable error attributes to the main span if available
				if deps.OTelWorkerWrapper != nil {
					if span := trace.SpanFromContext(ctx); span.IsRecording() {
						span.SetAttributes(
							attribute.Bool("task.will_retry", true),
							attribute.String("task.error_type", "retriable"),
							attribute.String("task.error", err.Error()),
						)
					}
				}
				// Return error to trigger RabbitMQ retry
				return err
			} else {
				logger.WithFields(logrus.Fields{
					"retriable":     false,
					"error_type":    "permanent",
					"error_message": err.Error(),
				}).Error("Error is permanent, marking task as failed")
				// Update task status to failed for permanent errors
				if statusErr := deps.RedisService.SetTaskStatus(ctx, queueMsg.ID, string(models.TaskStatusFailed), deps.Config.Redis.TaskStatusTTL); statusErr != nil {
					logger.WithError(statusErr).Error("Failed to update task status to failed")
				}
				// Add permanent error attributes to the main span if available
				if deps.OTelWorkerWrapper != nil {
					if span := trace.SpanFromContext(ctx); span.IsRecording() {
						span.SetAttributes(
							attribute.Bool("task.will_retry", false),
							attribute.String("task.error_type", "permanent"),
							attribute.String("task.error", err.Error()),
						)
					}
				}
				// Return nil to prevent retry for permanent failures
				return nil
			}
		}

		// Store the response in Redis
		if err := deps.RedisService.SetTaskResult(ctx, queueMsg.ID, response, deps.Config.Redis.TaskResultTTL); err != nil {
			logger.WithError(err).Error("Failed to store task result")
			// Don't fail the message processing for Redis storage issues
		}

		// Store trace context with result for end-to-end tracing
		if deps.TracePropagator != nil {
			traceHeaders := deps.TracePropagator.InjectTraceContext(ctx)
			if len(traceHeaders) > 0 {
				traceKey := "task:trace:" + queueMsg.ID
				if traceBytes, err := json.Marshal(traceHeaders); err == nil {
					_ = deps.RedisService.Set(ctx, traceKey, string(traceBytes), deps.Config.Redis.TaskResultTTL)
				}
			}
		}

		// Update task status to completed
		if err := deps.RedisService.SetTaskStatus(ctx, queueMsg.ID, string(models.TaskStatusCompleted), deps.Config.Redis.TaskStatusTTL); err != nil {
			logger.WithError(err).Error("Failed to update task status to completed")
		}

		// Add success attributes to the main span if available
		if deps.OTelWorkerWrapper != nil {
			if span := trace.SpanFromContext(ctx); span.IsRecording() {
				span.SetAttributes(
					attribute.Bool("task.will_retry", false),
					attribute.String("task.error_type", "none"),
					attribute.Bool("task.success", true),
					attribute.Int("task.response_length", len(response)),
				)
			}
		}

		// Execute callback if configured
		if deps.CallbackService != nil {
			callbackURL, err := deps.RedisService.GetCallbackURL(ctx, queueMsg.ID)
			if err == nil && callbackURL != "" {
				// Execute callback asynchronously to avoid blocking worker
				go executeCallback(context.Background(), deps, queueMsg.ID, callbackURL, response, logger)
			}
		}

		logger.WithField("response_length", len(response)).Info("User message processed successfully")

		// Return success (service layer will handle acknowledgment)
		return nil
	}
}

// isAudioURL checks if the URL appears to be an audio file (standalone function)
func isAudioURL(url string) bool {
	audioExtensions := []string{".mp3", ".wav", ".m4a", ".aac", ".ogg", ".oga", ".flac", ".wma", ".opus"}
	for _, ext := range audioExtensions {
		if strings.HasSuffix(strings.ToLower(url), ext) {
			return true
		}
	}
	return false
}

// processUserMessage handles the actual user message processing logic (matches Python process_user_message)
func processUserMessage(ctx context.Context, msg *models.QueueMessage, deps *MessageHandlerDependencies) (string, error) {
	logger := deps.Logger.WithField("function", "processUserMessage")

	logger.WithFields(logrus.Fields{
		"user_number":          msg.UserNumber,
		"message_length":       len(msg.Message),
		"has_previous_message": msg.PreviousMessage != nil,
		"provider":             msg.Provider,
	}).Info("Processing user message")

	logger.Info("DEBUG: Starting processUserMessage function execution")

	// Validate provider - currently only support google_agent_engine
	if msg.Provider != "google_agent_engine" {
		logger.WithField("provider", msg.Provider).Error("Unsupported provider")
		return "", fmt.Errorf("unsupported provider: %s (currently only 'google_agent_engine' is supported)", msg.Provider)
	}

	// Check if Google Agent service is available
	if deps.GoogleAgentService == nil {
		logger.Error("Google Agent Engine service not available")
		return "", fmt.Errorf("google Agent Engine service is required but not available")
	}

	// Handle audio transcription if message is an audio URL
	message := msg.Message
	var transcriptText *string

	// Check if message is an audio URL (independent of service availability)
	isAudioURL := isAudioURL(message)

	if isAudioURL {
		// Trace audio transcription step
		var transcribeCtx context.Context
		var transcribeSpan trace.Span
		if deps.OTelWorkerWrapper != nil {
			transcribeCtx, transcribeSpan = deps.OTelWorkerWrapper.StartSpan(ctx, "audio_transcription",
				attribute.String("audio.url", message),
				attribute.Bool("audio.detected", true),
				attribute.String("audio.format", getAudioFormatFromURL(message)))
			defer transcribeSpan.End()
		} else {
			transcribeCtx = ctx
		}

		logger.WithField("audio_url", message).Info("Detected audio URL, attempting transcription")

		if deps.TranscribeService == nil {
			logger.Warn("Transcribe service not available, using fallback")
			message = "Ajuda"
			if deps.OTelWorkerWrapper != nil && transcribeSpan != nil {
				transcribeSpan.SetAttributes(
					attribute.Bool("transcription.success", false),
					attribute.String("transcription.error_type", "service_not_available"),
					attribute.Bool("transcription.fallback_used", true))
			}
		} else {
			transcript, err := deps.TranscribeService.TranscribeAudio(transcribeCtx, message)
			if err != nil {
				logger.WithError(err).Warn("Failed to transcribe audio, using fallback")
				// Fallback to not block the flow (matches Python logic)
				message = "Ajuda"
				if deps.OTelWorkerWrapper != nil && transcribeSpan != nil {
					transcribeSpan.SetAttributes(
						attribute.Bool("transcription.success", false),
						attribute.String("transcription.error_type", classifyTranscriptionError(err)),
						attribute.String("transcription.error", err.Error()),
						attribute.Bool("transcription.fallback_used", true))
				}
			} else if transcript != "" && strings.TrimSpace(transcript) != "" && transcript != "Áudio sem conteúdo reconhecível" {
				transcriptText = &transcript
				message = transcript
				logger.WithField("transcript_length", len(transcript)).Info("Audio transcribed successfully")
				if deps.OTelWorkerWrapper != nil && transcribeSpan != nil {
					transcribeSpan.SetAttributes(
						attribute.Bool("transcription.success", true),
						attribute.Int("transcription.transcript_length", len(transcript)),
						attribute.Bool("transcription.fallback_used", false))
				}
			} else {
				logger.Warn("Transcription returned no useful content, using fallback")
				message = "Ajuda"
				if deps.OTelWorkerWrapper != nil && transcribeSpan != nil {
					transcribeSpan.SetAttributes(
						attribute.Bool("transcription.success", false),
						attribute.String("transcription.error_type", "empty_or_invalid_content"),
						attribute.Bool("transcription.fallback_used", true))
				}
			}
		}
	}

	// Validate message content
	if deps.MessageFormatter != nil {
		if err := deps.MessageFormatter.ValidateMessageContent(message); err != nil {
			logger.WithError(err).Error("Message content validation failed")
			return "", fmt.Errorf("invalid message content: %w", err)
		}
	}

	// Trace thread creation step
	var threadCtx context.Context
	var threadSpan trace.Span
	if deps.OTelWorkerWrapper != nil {
		threadCtx, threadSpan = deps.OTelWorkerWrapper.StartSpan(ctx, "thread_management",
			attribute.String("user.number", msg.UserNumber))
		defer threadSpan.End()
	} else {
		threadCtx = ctx
	}

	// Get or create thread for user (thread ID corresponds to agent ID in Python logic)
	threadID, err := deps.GoogleAgentService.GetOrCreateThread(threadCtx, msg.UserNumber)
	if err != nil {
		logger.WithError(err).Error("Failed to get or create thread")
		if deps.OTelWorkerWrapper != nil && threadSpan != nil {
			threadSpan.SetAttributes(
				attribute.String("thread.result", "error"),
				attribute.String("thread.error", err.Error()))
		}
		return "", fmt.Errorf("failed to get thread: %w", err)
	}

	if deps.OTelWorkerWrapper != nil && threadSpan != nil {
		threadSpan.SetAttributes(
			attribute.String("thread.result", "success"),
			attribute.String("thread.id", threadID))
	}

	logger.WithField("thread_id", threadID).Info("Using thread for conversation")

	// If there's a previous_message, send it as history update first with role="ai"
	if msg.PreviousMessage != nil && *msg.PreviousMessage != "" {
		logger.WithField("previous_message_length", len(*msg.PreviousMessage)).Info("Sending previous message as history update")

		historyMessages := []models.HistoryMessage{
			{
				Content: *msg.PreviousMessage,
				Role:    "ai",
			},
		}

		_, err := deps.GoogleAgentService.SendHistoryUpdate(ctx, threadID, historyMessages, msg.ReasoningEngineID)
		if err != nil {
			logger.WithError(err).Error("Failed to send previous message as history update")
			return "", fmt.Errorf("failed to send previous message as history: %w", err)
		}

		logger.Info("Previous message added to history successfully")
	}

	// Trace Google Agent Engine call
	var agentCtx context.Context
	var agentSpan trace.Span
	if deps.OTelWorkerWrapper != nil {
		agentCtx, agentSpan = deps.OTelWorkerWrapper.StartSpan(ctx, "google_agent_engine_call",
			attribute.String("thread.id", threadID),
			attribute.String("message.content", message),
			attribute.Int("message.length", len(message)))
		defer agentSpan.End()
	} else {
		agentCtx = ctx
	}

	// Send message to Google Agent Engine
	// Pass custom reasoning_engine_id if provided in the request
	// Pass nil for messageType (normal user messages don't need type parameter)
	agentResponse, err := deps.GoogleAgentService.SendMessage(agentCtx, threadID, message, msg.ReasoningEngineID, nil)
	if err != nil {
		logger.WithError(err).Error("Failed to send message to Google Agent Engine")
		if deps.OTelWorkerWrapper != nil && agentSpan != nil {
			agentSpan.SetAttributes(
				attribute.String("agent.result", "error"),
				attribute.String("agent.error", err.Error()))
		}
		return "", fmt.Errorf("failed to get AI response: %w", err)
	}

	if deps.OTelWorkerWrapper != nil && agentSpan != nil {
		agentSpan.SetAttributes(
			attribute.String("agent.result", "success"),
			attribute.Int("agent.response_length", len(agentResponse.Content)))
	}

	// Trace response processing step
	var responseSpan trace.Span
	if deps.OTelWorkerWrapper != nil {
		_, responseSpan = deps.OTelWorkerWrapper.StartSpan(ctx, "response_processing",
			attribute.String("response.raw_length", fmt.Sprintf("%d", len(agentResponse.Content))))
		defer responseSpan.End()
	}

	// Parse Google's raw JSON response immediately after getting it from Google Agent Engine
	logger.WithField("raw_response_length", len(agentResponse.Content)).Debug("Processing Google Agent Engine response")

	// Clean the JSON string first to handle newlines, whitespace, and BOM issues
	cleanedResponse := strings.TrimSpace(agentResponse.Content)
	cleanedBytes := []byte(cleanedResponse)

	// Strip BOM if present (fixes "invalid character 'â'" errors)
	cleanedBytes, hadBOM := stripBOM(cleanedBytes)
	if hadBOM {
		logger.WithField("bom_type", "detected").Debug("Stripped BOM from Google Agent Engine response")
	}

	cleanedResponse = string(cleanedBytes)
	cleanedResponse = strings.ReplaceAll(cleanedResponse, "\n", "")
	cleanedResponse = strings.ReplaceAll(cleanedResponse, "\r", "")

	var parsedResponse map[string]interface{}
	if err := json.Unmarshal([]byte(cleanedResponse), &parsedResponse); err != nil {
		// Log first 100 bytes in hex for debugging encoding issues
		hexDump := ""
		if len(cleanedResponse) > 0 {
			dumpLen := 100
			if len(cleanedResponse) < dumpLen {
				dumpLen = len(cleanedResponse)
			}
			hexDump = fmt.Sprintf("%x", cleanedResponse[:dumpLen])
		}

		logger.WithFields(logrus.Fields{
			"error":       err.Error(),
			"raw_json":    cleanedResponse,
			"json_length": len(cleanedResponse),
			"hex_prefix":  hexDump,
		}).Error("Failed to parse Google Agent Engine JSON response")
		if deps.OTelWorkerWrapper != nil && responseSpan != nil {
			responseSpan.SetAttributes(
				attribute.String("response.result", "json_parse_error"),
				attribute.String("response.error", err.Error()))
		}
		return "", fmt.Errorf("failed to parse AI response JSON: %w", err)
	}

	// Extract the 'output' field which contains the messages
	output, exists := parsedResponse["output"]
	if !exists {
		logger.Error("No 'output' field found in Google Agent Engine response")
		return "", fmt.Errorf("invalid Google Agent Engine response format - missing 'output' field")
	}

	outputMap, ok := output.(map[string]interface{})
	if !ok {
		logger.Error("'output' field is not a map in Google Agent Engine response")
		return "", fmt.Errorf("invalid Google Agent Engine response format - 'output' is not an object")
	}

	// Extract messages array from the output structure
	var transformedMessages []interface{}
	if messagesArray, exists := outputMap["messages"]; exists {
		transformedMessages = transformGoogleAgentMessages(deps.Logger, messagesArray)
	} else {
		// Fallback to empty messages if no messages field
		logger.Warn("No 'messages' field found in output, using empty array")
		transformedMessages = []interface{}{}
	}

	// Generate agent ID based on user number
	agentID := "user_" + msg.UserNumber

	// Set agent_id in the usage statistics message
	if len(transformedMessages) > 0 {
		if lastMsg, ok := transformedMessages[len(transformedMessages)-1].(map[string]interface{}); ok {
			if msgType, exists := lastMsg["message_type"]; exists && msgType == "usage_statistics" {
				lastMsg["agent_id"] = agentID
			}
		}
	}

	// Apply WhatsApp formatting to individual message content
	transformedMessages = applyWhatsAppFormattingToMessages(deps.Logger, deps.MessageFormatter, transformedMessages)

	// Build the final response data to match Python API structure
	processedData := models.ProcessedMessageData{
		Messages:    transformedMessages,
		AgentID:     agentID,
		ProcessedAt: msg.ID, // Use message ID as processed_at identifier
		Status:      "done",
	}

	// Convert the processed data to JSON for storage in Redis
	processedBytes, err := json.Marshal(processedData)
	if err != nil {
		logger.WithError(err).Error("Failed to marshal processed data to JSON")
		return "", fmt.Errorf("failed to marshal processed response: %w", err)
	}

	processedResponse := string(processedBytes)

	// Record successful response processing in tracing
	if deps.OTelWorkerWrapper != nil && responseSpan != nil {
		responseSpan.SetAttributes(
			attribute.String("response.result", "success"),
			attribute.Int("response.messages_count", len(transformedMessages)),
			attribute.Int("response.processed_length", len(processedResponse)))
	}

	// Log successful processing (matches Python log format)
	logger.WithFields(logrus.Fields{
		"thread_id":           threadID,
		"agent_id":            agentID,
		"raw_response_length": len(agentResponse.Content),
		"processed_length":    len(processedResponse),
		"messages_count":      len(transformedMessages),
		"had_transcript":      transcriptText != nil,
	}).Info("Successfully processed user message with full transformation pipeline")

	return processedResponse, nil
}

// transformGoogleAgentMessages transforms Google Agent Engine messages to Python API format
func transformGoogleAgentMessages(logger *logrus.Logger, messagesData interface{}) []interface{} {
	var transformedMessages []interface{}

	// Handle both slice and single message cases
	var messagesList []interface{}

	switch v := messagesData.(type) {
	case []interface{}:
		messagesList = v
	case interface{}:
		messagesList = []interface{}{v}
	default:
		logger.Warn("Unexpected messages data type, returning empty array")
		return transformedMessages
	}

	// Track tool_call_id to name mapping for tool return messages
	toolCallToName := make(map[string]string)

	// Keep original messages for usage statistics calculation (avoid double counting)
	var originalMessages []map[string]interface{}

	for _, msgData := range messagesList {
		msgMap, ok := msgData.(map[string]interface{})
		if !ok {
			continue
		}

		// Store original message for usage calculation
		originalMessages = append(originalMessages, msgMap)

		msgType := mapMessageType(msgMap)
		stepID := "step-" + generateStepID()
		msgID := msgMap["id"]

		// Process AI messages with potential reasoning/thinking content
		if msgType == "assistant_message" || msgType == "tool_call_message" {
			aiMessages := processAIMessage(logger, msgMap, stepID, toolCallToName)
			transformedMessages = append(transformedMessages, aiMessages...)
			continue
		}

		// Transform Google Agent Engine message to Python API format
		transformedMsg := map[string]interface{}{
			"id":                      msgID,
			"date":                    nil,
			"session_id":              nil,
			"time_since_last_message": nil,
			"name":                    msgMap["name"],
			"otid":                    msgID,
			"sender_id":               nil,
			"step_id":                 stepID,
			"is_err":                  nil,
			"model_name":              extractModelName(msgMap),
			"finish_reason":           extractFinishReason(msgMap),
			"avg_logprobs":            extractAvgLogprobs(msgMap),
			"usage_metadata":          extractUsageMetadata(msgMap),
			"message_type":            msgType,
			"content":                 msgMap["content"],
		}

		// For tool return messages, extract tool return information
		if msgType == "tool_return_message" {
			if name, exists := msgMap["name"]; exists {
				transformedMsg["tool_return"] = msgMap["content"]
				transformedMsg["status"] = "success"
				transformedMsg["tool_call_id"] = msgMap["tool_call_id"]
				transformedMsg["stdout"] = nil
				transformedMsg["stderr"] = nil
				transformedMsg["name"] = name
			}
		}

		transformedMessages = append(transformedMessages, transformedMsg)
	}

	// Calculate aggregated usage statistics from ORIGINAL messages (avoid double counting)
	usageStats := calculateUsageStatistics(originalMessages, transformedMessages)
	transformedMessages = append(transformedMessages, usageStats)

	return transformedMessages
}

// processAIMessage processes AI messages, extracting thinking/reasoning content and tool calls
func processAIMessage(logger *logrus.Logger, msgMap map[string]interface{}, stepID string, toolCallToName map[string]string) []interface{} {
	var messages []interface{}

	msgID := msgMap["id"]
	rawContent := msgMap["content"]
	toolCalls, _ := msgMap["tool_calls"].([]interface{})
	responseMetadata, _ := msgMap["response_metadata"].(map[string]interface{})
	usageMD, _ := responseMetadata["usage_metadata"].(map[string]interface{})

	// Extract reasoning tokens from output_token_details
	var reasoningTokens int
	if outputDetails, ok := usageMD["output_token_details"].(map[string]interface{}); ok {
		if reasoning, exists := outputDetails["reasoning"]; exists {
			reasoningTokens = toInt(reasoning)
		}
	}

	var finalContent string
	var thinkingContent string

	// Process content (can be string or list)
	switch content := rawContent.(type) {
	case []interface{}:
		for _, item := range content {
			if itemMap, ok := item.(map[string]interface{}); ok {
				itemType, _ := itemMap["type"].(string)
				if itemType == "thinking" {
					if thinking, ok := itemMap["thinking"].(string); ok {
						thinkingContent += thinking
					}
				} else if itemType == "text" {
					if text, ok := itemMap["text"].(string); ok {
						finalContent += text
					}
				}
			} else if str, ok := item.(string); ok {
				finalContent += str
			}
		}
	case string:
		finalContent = content
	}

	baseDict := map[string]interface{}{
		"id":                      msgID,
		"date":                    nil,
		"session_id":              nil,
		"time_since_last_message": nil,
		"name":                    msgMap["name"],
		"otid":                    msgID,
		"sender_id":               nil,
		"step_id":                 stepID,
		"is_err":                  nil,
		"model_name":              extractModelName(msgMap),
		"finish_reason":           extractFinishReason(msgMap),
		"avg_logprobs":            extractAvgLogprobs(msgMap),
		"usage_metadata":          extractUsageMetadata(msgMap),
	}

	// Add reasoning message if there's explicit thinking content or reasoning tokens
	if thinkingContent != "" {
		reasoningMsg := copyMap(baseDict)
		reasoningMsg["id"] = fmt.Sprintf("thinking-%v", msgID)
		reasoningMsg["message_type"] = "reasoning_message"
		reasoningMsg["source"] = "reasoner_model"
		reasoningMsg["reasoning"] = thinkingContent
		reasoningMsg["signature"] = nil
		messages = append(messages, reasoningMsg)
	} else if reasoningTokens > 0 {
		// Fallback for implicit reasoning (no textual content exposed)
		reasoningText := "Processando..."
		if len(toolCalls) > 0 {
			if tc, ok := toolCalls[0].(map[string]interface{}); ok {
				toolName, _ := tc["name"].(string)
				if toolName == "" {
					toolName = "unknown"
				}
				reasoningText = fmt.Sprintf("Processando chamada para ferramenta %s", toolName)
			}
		} else if finalContent != "" {
			reasoningText = "Processando resposta para o usuário"
		}

		reasoningMsg := copyMap(baseDict)
		reasoningMsg["id"] = fmt.Sprintf("reasoning-%v", msgID)
		reasoningMsg["message_type"] = "reasoning_message"
		reasoningMsg["source"] = "reasoner_model"
		reasoningMsg["reasoning"] = reasoningText
		reasoningMsg["signature"] = nil
		messages = append(messages, reasoningMsg)
	}

	// Process tool calls
	if len(toolCalls) > 0 {
		for _, tc := range toolCalls {
			if toolCall, ok := tc.(map[string]interface{}); ok {
				toolCallID, _ := toolCall["id"].(string)
				toolName, _ := toolCall["name"].(string)
				if toolCallID != "" {
					toolCallToName[toolCallID] = toolName
				}

				toolMsg := copyMap(baseDict)
				toolMsg["id"] = fmt.Sprintf("tool-%v", toolCallID)
				toolMsg["message_type"] = "tool_call_message"
				toolMsg["content"] = nil
				toolMsg["tool_call"] = map[string]interface{}{
					"name":         toolName,
					"arguments":    toolCall["args"],
					"tool_call_id": toolCallID,
				}
				messages = append(messages, toolMsg)
			}
		}
	}

	// Add assistant message if there's final text content
	if finalContent != "" {
		assistantMsg := copyMap(baseDict)
		assistantMsg["message_type"] = "assistant_message"
		assistantMsg["content"] = finalContent
		messages = append(messages, assistantMsg)
	}

	return messages
}

// calculateUsageStatistics aggregates usage statistics from original messages
// originalMessages: raw messages from Google Agent Engine (for token counting - avoids double counting)
// transformedMessages: processed messages (for step_id counting)
func calculateUsageStatistics(originalMessages []map[string]interface{}, transformedMessages []interface{}) map[string]interface{} {
	var inputTokens, outputTokens, totalTokens, thoughtsTokens int
	modelNames := make(map[string]bool)
	stepIDs := make(map[string]bool)

	// Calculate tokens from ORIGINAL messages (avoid double counting from derived messages)
	for _, msgMap := range originalMessages {
		// Extract response_metadata.usage_metadata from original message
		responseMetadata, _ := msgMap["response_metadata"].(map[string]interface{})
		usageMD, _ := responseMetadata["usage_metadata"].(map[string]interface{})

		if usageMD != nil {
			// Input tokens
			inputTokens += toInt(usageMD["input_tokens"])

			// Output tokens
			outputTokens += toInt(usageMD["output_tokens"])

			// Thoughts tokens from output_token_details.reasoning
			if outputDetails, ok := usageMD["output_token_details"].(map[string]interface{}); ok {
				thoughtsTokens += toInt(outputDetails["reasoning"])
			}

			// Total tokens
			msgTotal := toInt(usageMD["total_tokens"])
			if msgTotal == 0 {
				msgTotal = toInt(usageMD["input_tokens"]) +
					toInt(usageMD["output_tokens"])
				if outputDetails, ok := usageMD["output_token_details"].(map[string]interface{}); ok {
					msgTotal += toInt(outputDetails["reasoning"])
				}
			}
			totalTokens += msgTotal
		}

		// Collect model names from original messages
		if modelName, ok := responseMetadata["model_name"].(string); ok && modelName != "" {
			modelNames[modelName] = true
		}
	}

	// Count unique step_ids from transformed messages
	for _, msgInterface := range transformedMessages {
		if msgMap, ok := msgInterface.(map[string]interface{}); ok {
			if stepID, ok := msgMap["step_id"].(string); ok && stepID != "" {
				stepIDs[stepID] = true
			}
		}
	}

	// Total output includes thoughts (Gemini charges thoughts as output)
	totalOutputTokens := outputTokens + thoughtsTokens

	// Convert model names map to slice
	modelNamesList := make([]string, 0, len(modelNames))
	for name := range modelNames {
		modelNamesList = append(modelNamesList, name)
	}

	return map[string]interface{}{
		"message_type":      "usage_statistics",
		"completion_tokens": totalOutputTokens,
		"thoughts_tokens":   thoughtsTokens,
		"prompt_tokens":     inputTokens,
		"total_tokens":      totalTokens,
		"step_count":        len(stepIDs),
		"steps_messages":    nil,
		"run_ids":           nil,
		"agent_id":          "",
		"processed_at":      time.Now().Format(time.RFC3339),
		"status":            "done",
		"model_names":       modelNamesList,
	}
}

// stripBOM removes Byte Order Mark (BOM) from the beginning of a byte slice
// Returns the cleaned bytes and a boolean indicating if BOM was found
func stripBOM(data []byte) ([]byte, bool) {
	// UTF-8 BOM (0xEF 0xBB 0xBF)
	// This is the most common cause of "invalid character 'â'" errors
	if len(data) >= 3 && data[0] == 0xEF && data[1] == 0xBB && data[2] == 0xBF {
		return data[3:], true
	}
	// UTF-32 BE BOM (0x00 0x00 0xFE 0xFF) - check before UTF-16
	if len(data) >= 4 && data[0] == 0x00 && data[1] == 0x00 && data[2] == 0xFE && data[3] == 0xFF {
		return data[4:], true
	}
	// UTF-32 LE BOM (0xFF 0xFE 0x00 0x00) - check before UTF-16
	if len(data) >= 4 && data[0] == 0xFF && data[1] == 0xFE && data[2] == 0x00 && data[3] == 0x00 {
		return data[4:], true
	}
	// UTF-16 BE BOM (0xFE 0xFF)
	if len(data) >= 2 && data[0] == 0xFE && data[1] == 0xFF {
		return data[2:], true
	}
	// UTF-16 LE BOM (0xFF 0xFE)
	if len(data) >= 2 && data[0] == 0xFF && data[1] == 0xFE {
		return data[2:], true
	}
	return data, false
}

// copyMap creates a shallow copy of a map
func copyMap(original map[string]interface{}) map[string]interface{} {
	copy := make(map[string]interface{})
	for k, v := range original {
		copy[k] = v
	}
	return copy
}

// toInt safely converts an interface to int
func toInt(v interface{}) int {
	switch val := v.(type) {
	case int:
		return val
	case int64:
		return int(val)
	case float64:
		return int(val)
	case float32:
		return int(val)
	default:
		return 0
	}
}

// Helper functions for message transformation
func extractModelName(msgMap map[string]interface{}) interface{} {
	if responseMetadata, exists := msgMap["response_metadata"].(map[string]interface{}); exists {
		if modelName, exists := responseMetadata["model_name"]; exists {
			return modelName
		}
	}
	return nil
}

func extractFinishReason(msgMap map[string]interface{}) interface{} {
	if responseMetadata, exists := msgMap["response_metadata"].(map[string]interface{}); exists {
		if finishReason, exists := responseMetadata["finish_reason"]; exists {
			return finishReason
		}
	}
	return nil
}

func extractAvgLogprobs(msgMap map[string]interface{}) interface{} {
	if responseMetadata, exists := msgMap["response_metadata"].(map[string]interface{}); exists {
		if avgLogprobs, exists := responseMetadata["avg_logprobs"]; exists {
			return avgLogprobs
		}
	}
	return nil
}

func extractUsageMetadata(msgMap map[string]interface{}) interface{} {
	if responseMetadata, exists := msgMap["response_metadata"].(map[string]interface{}); exists {
		if usageMetadata, exists := responseMetadata["usage_metadata"]; exists {
			if usageMap, ok := usageMetadata.(map[string]interface{}); ok {
				result := map[string]interface{}{
					"prompt_token_count":     toInt(usageMap["input_tokens"]),
					"candidates_token_count": toInt(usageMap["output_tokens"]),
					"total_token_count":      toInt(usageMap["total_tokens"]),
				}

				// Extract thoughts_token_count from output_token_details.reasoning
				if outputDetails, exists := usageMap["output_token_details"].(map[string]interface{}); exists {
					if reasoning, exists := outputDetails["reasoning"]; exists {
						result["thoughts_token_count"] = toInt(reasoning)
					}
				}

				// Extract cached_content_token_count from input_token_details.cache_read
				if inputDetails, exists := usageMap["input_token_details"].(map[string]interface{}); exists {
					if cacheRead, exists := inputDetails["cache_read"]; exists {
						result["cached_content_token_count"] = toInt(cacheRead)
					}
				}

				return result
			}
		}
	}
	return nil
}

func mapMessageType(msgMap map[string]interface{}) string {
	msgType, exists := msgMap["type"].(string)
	if !exists {
		return "user_message" // Default fallback
	}

	switch msgType {
	case "human":
		return "user_message"
	case "ai":
		// Check if it has tool calls
		if toolCalls, exists := msgMap["tool_calls"].([]interface{}); exists && len(toolCalls) > 0 {
			return "tool_call_message"
		}
		return "assistant_message"
	case "tool":
		return "tool_return_message"
	default:
		return "user_message"
	}
}

// generateStepID generates a random step ID in the format expected by Python API
func generateStepID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b) // Ignore error as rand.Read from crypto/rand always returns len(b), nil
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

// isRetriableError determines if an error should be retried by RabbitMQ
func isRetriableError(err error) bool {
	if err == nil {
		return false
	}

	errorStr := strings.ToLower(err.Error())

	// Timeout errors - should be retried
	if strings.Contains(errorStr, "context deadline exceeded") ||
		strings.Contains(errorStr, "timeout") ||
		strings.Contains(errorStr, "timed out") {
		return true
	}

	// Network errors - should be retried
	if strings.Contains(errorStr, "connection refused") ||
		strings.Contains(errorStr, "network unreachable") ||
		strings.Contains(errorStr, "no route to host") ||
		strings.Contains(errorStr, "connection reset") ||
		strings.Contains(errorStr, "connection was closed") ||
		strings.Contains(errorStr, "connection closed") ||
		strings.Contains(errorStr, "broken pipe") ||
		strings.Contains(errorStr, "connection lost") ||
		strings.Contains(errorStr, "connection interrupted") {
		return true
	}

	// HTTP 5xx errors - should be retried
	if strings.Contains(errorStr, "500") ||
		strings.Contains(errorStr, "502") ||
		strings.Contains(errorStr, "503") ||
		strings.Contains(errorStr, "504") ||
		strings.Contains(errorStr, "internal server error") ||
		strings.Contains(errorStr, "bad gateway") ||
		strings.Contains(errorStr, "service unavailable") ||
		strings.Contains(errorStr, "gateway timeout") {
		return true
	}

	// Rate limiting - should be retried
	if strings.Contains(errorStr, "rate limit") ||
		strings.Contains(errorStr, "too many requests") ||
		strings.Contains(errorStr, "429") {
		return true
	}

	// DNS errors - should be retried
	if strings.Contains(errorStr, "no such host") ||
		strings.Contains(errorStr, "dns") {
		return true
	}

	// Google Reasoning Engine specific errors - check inner error details
	if strings.Contains(errorStr, "reasoning engine execution failed") {
		// Look for connection issues in the nested error details
		if strings.Contains(errorStr, "connection was closed") ||
			strings.Contains(errorStr, "connection lost") ||
			strings.Contains(errorStr, "connection interrupted") ||
			strings.Contains(errorStr, "connection reset") ||
			strings.Contains(errorStr, "timeout") ||
			strings.Contains(errorStr, "network") {
			return true
		}

		// Check for transient execution failures
		if strings.Contains(errorStr, "agent engine error") ||
			strings.Contains(errorStr, "an error occurred during invocation") ||
			strings.Contains(errorStr, "failed_precondition") ||
			strings.Contains(errorStr, "execution failed") {
			return true
		}
	}

	// Check for network.Error interface
	if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
		return true
	}

	// Permanent failures - should NOT be retried:
	// - Authentication errors (401, 403, invalid credentials)
	// - Client errors (400, 404, malformed requests) WITHOUT underlying connection issues
	// - Application logic errors (invalid input, etc.)

	return false
}

// applyWhatsAppFormattingToMessages applies WhatsApp formatting to individual message content
func applyWhatsAppFormattingToMessages(logger *logrus.Logger, messageFormatter MessageFormatterInterface, messages []interface{}) []interface{} {
	if messageFormatter == nil {
		logger.Warn("MessageFormatter is nil, skipping WhatsApp formatting")
		return messages
	}

	for i, msgInterface := range messages {
		if msgMap, ok := msgInterface.(map[string]interface{}); ok {
			// Only format message content, not metadata
			if content, exists := msgMap["content"].(string); exists && content != "" {
				// Create a temporary AgentResponse to use with the FormatForWhatsApp service
				tempResponse := &models.AgentResponse{
					Content:   content,
					MessageID: "temp", // Not used by the formatter
					ThreadID:  "temp", // Not used by the formatter
				}

				// Apply WhatsApp formatting using the proper service
				formattedContent, err := messageFormatter.FormatForWhatsApp(context.Background(), tempResponse)
				if err != nil {
					logger.WithError(err).Warn("Failed to format message content for WhatsApp, using original content")
					formattedContent = content // Fallback to original content
				}

				msgMap["content"] = formattedContent
				messages[i] = msgMap
			}
		}
	}
	return messages
}

// getAudioFormatFromURL extracts the audio format from URL extension
func getAudioFormatFromURL(url string) string {
	// Extract extension from URL
	parts := strings.Split(url, ".")
	if len(parts) > 1 {
		ext := strings.ToLower(parts[len(parts)-1])
		// Handle URLs with query parameters
		if strings.Contains(ext, "?") {
			ext = strings.Split(ext, "?")[0]
		}
		return ext
	}
	return "unknown"
}

// classifyTranscriptionError categorizes transcription errors for better observability
func classifyTranscriptionError(err error) string {
	if err == nil {
		return "none"
	}

	errorStr := strings.ToLower(err.Error())

	// Network/connectivity errors
	if strings.Contains(errorStr, "connection") ||
		strings.Contains(errorStr, "network") ||
		strings.Contains(errorStr, "timeout") ||
		strings.Contains(errorStr, "deadline exceeded") {
		return "network_error"
	}

	// Authentication errors
	if strings.Contains(errorStr, "unauthorized") ||
		strings.Contains(errorStr, "permission") ||
		strings.Contains(errorStr, "credentials") ||
		strings.Contains(errorStr, "authentication") {
		return "auth_error"
	}

	// Google API specific errors
	if strings.Contains(errorStr, "invalid argument") ||
		strings.Contains(errorStr, "recognitionaudio empty") {
		return "api_validation_error"
	}

	// Rate limiting
	if strings.Contains(errorStr, "rate limit") ||
		strings.Contains(errorStr, "quota") ||
		strings.Contains(errorStr, "too many requests") {
		return "rate_limit_error"
	}

	// File/format errors
	if strings.Contains(errorStr, "unsupported") ||
		strings.Contains(errorStr, "format") ||
		strings.Contains(errorStr, "invalid file") {
		return "format_error"
	}

	// Download errors
	if strings.Contains(errorStr, "download") ||
		strings.Contains(errorStr, "failed to get") ||
		strings.Contains(errorStr, "http") {
		return "download_error"
	}

	// Service unavailable
	if strings.Contains(errorStr, "service") ||
		strings.Contains(errorStr, "unavailable") {
		return "service_error"
	}

	return "unknown_error"
}

// executeCallback handles the callback execution asynchronously
func executeCallback(ctx context.Context, deps *MessageHandlerDependencies, messageID string, callbackURL string, response string, logger *logrus.Entry) {
	callbackLogger := logger.WithFields(logrus.Fields{
		"callback_url": callbackURL,
		"message_id":   messageID,
	})

	callbackLogger.Info("Executing callback for completed task")

	// Parse the response to extract the ProcessedMessageData
	var processedData models.ProcessedMessageData
	if err := json.Unmarshal([]byte(response), &processedData); err != nil {
		callbackLogger.WithError(err).Error("Failed to parse response for callback")
		return
	}

	// Create callback payload
	payload := models.CallbackPayload{
		MessageID:   messageID,
		Status:      "completed",
		Data:        processedData,
		Error:       nil,
		Timestamp:   time.Now().UTC().Format(time.RFC3339),
		ProcessedAt: processedData.ProcessedAt,
	}

	// Execute the callback with retry logic
	if err := deps.CallbackService.ExecuteCallback(ctx, callbackURL, payload); err != nil {
		callbackLogger.WithError(err).Error("Failed to execute callback after all retries")
		// Store callback failure for debugging
		errorKey := "callback:error:" + messageID
		errorMsg := fmt.Sprintf("Failed to execute callback: %v", err)
		_ = deps.RedisService.Set(ctx, errorKey, errorMsg, deps.Config.Redis.TaskStatusTTL)
	} else {
		callbackLogger.Info("Callback executed successfully")
		// Clean up callback URL from Redis
		_ = deps.RedisService.DeleteCallbackURL(ctx, messageID)
	}
}
