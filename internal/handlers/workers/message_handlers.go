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

			// Extract retry count from RabbitMQ headers
			retryCount := int64(0)
			if delivery.Headers != nil {
				if count, ok := delivery.Headers["x-retry-count"].(int64); ok {
					retryCount = count
				}
			}
			maxRetries := int64(deps.Config.RabbitMQ.MaxRetries)

			// Determine if this error should be retried
			shouldRetry := isRetriableError(err)

			if shouldRetry {
				// Check if max retries reached
				if retryCount >= maxRetries {
					logger.WithFields(logrus.Fields{
						"retriable":     true,
						"error_type":    "retriable_max_retries_exceeded",
						"error_message": err.Error(),
						"retry_count":   retryCount,
						"max_retries":   maxRetries,
					}).Error("Retriable error but max retries reached, marking as failed")

					// Update task status to failed
					if statusErr := deps.RedisService.SetTaskStatus(ctx, queueMsg.ID, string(models.TaskStatusFailed), deps.Config.Redis.TaskStatusTTL); statusErr != nil {
						logger.WithError(statusErr).Error("Failed to update task status to failed")
					}

					// Execute error callback if configured
					if deps.CallbackService != nil {
						callbackURL, getErr := deps.RedisService.GetCallbackURL(ctx, queueMsg.ID)
						if getErr == nil && callbackURL != "" {
							// Execute error callback asynchronously
							go executeCallbackOnError(context.Background(), deps, queueMsg.ID, callbackURL, err, logger)
						}
					}

					// Add retriable error exceeded attributes to the main span if available
					if deps.OTelWorkerWrapper != nil {
						if span := trace.SpanFromContext(ctx); span.IsRecording() {
							span.SetAttributes(
								attribute.Bool("task.will_retry", false),
								attribute.String("task.error_type", "retriable_max_retries_exceeded"),
								attribute.String("task.error", err.Error()),
								attribute.Int64("task.retry_count", retryCount),
								attribute.Int64("task.max_retries", maxRetries),
							)
						}
					}

					// Return nil to prevent further retries
					return nil
				}

				logger.WithFields(logrus.Fields{
					"retriable":     true,
					"error_type":    "retriable",
					"error_message": err.Error(),
					"retry_count":   retryCount,
					"max_retries":   maxRetries,
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
							attribute.Int64("task.retry_count", retryCount),
							attribute.Int64("task.max_retries", maxRetries),
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
				// Execute error callback if configured
				if deps.CallbackService != nil {
					callbackURL, getErr := deps.RedisService.GetCallbackURL(ctx, queueMsg.ID)
					if getErr == nil && callbackURL != "" {
						// Execute error callback asynchronously
						go executeCallbackOnError(context.Background(), deps, queueMsg.ID, callbackURL, err, logger)
					}
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
	// The Google Agent Engine automatically handles previous message context via thread ID
	agentResponse, err := deps.GoogleAgentService.SendMessage(agentCtx, threadID, message)
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

	// Clean the JSON string first to handle newlines and other whitespace issues
	cleanedResponse := strings.ReplaceAll(agentResponse.Content, "\n", "")
	cleanedResponse = strings.ReplaceAll(cleanedResponse, "\r", "")
	cleanedResponse = strings.TrimSpace(cleanedResponse)

	var parsedResponse map[string]interface{}
	if err := json.Unmarshal([]byte(cleanedResponse), &parsedResponse); err != nil {
		logger.WithFields(logrus.Fields{
			"error":       err.Error(),
			"raw_json":    cleanedResponse,
			"json_length": len(cleanedResponse),
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
		// Fallback: if no 'messages' field, wrap the entire output as structured data
		logger.Warn("No 'messages' field found in output, wrapping output as structured data message")
		structuredMessage := map[string]interface{}{
			"message_type": "structured_data",
			"content":      outputMap, // Include all output fields
			"timestamp":    time.Now().Format(time.RFC3339),
		}
		transformedMessages = []interface{}{structuredMessage}
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

	for _, msgData := range messagesList {
		msgMap, ok := msgData.(map[string]interface{})
		if !ok {
			continue
		}

		// Transform Google Agent Engine message to Python API format
		transformedMsg := map[string]interface{}{
			"id":                      msgMap["id"],
			"date":                    nil,
			"session_id":              nil,
			"time_since_last_message": nil,
			"name":                    msgMap["name"],
			"otid":                    msgMap["id"], // Use same ID as otid
			"sender_id":               nil,
			"step_id":                 "step-" + generateStepID(), // Generate step ID
			"is_err":                  nil,
			"model_name":              extractModelName(msgMap),
			"finish_reason":           extractFinishReason(msgMap),
			"avg_logprobs":            extractAvgLogprobs(msgMap),
			"usage_metadata":          extractUsageMetadata(msgMap),
			"message_type":            mapMessageType(msgMap),
			"content":                 msgMap["content"],
		}

		// Add type-specific fields
		if msgType := mapMessageType(msgMap); msgType == "tool_call_message" {
			if toolCalls, exists := msgMap["tool_calls"].([]interface{}); exists && len(toolCalls) > 0 {
				if toolCall, ok := toolCalls[0].(map[string]interface{}); ok {
					transformedMsg["tool_call"] = map[string]interface{}{
						"name":         toolCall["name"],
						"arguments":    toolCall["args"],
						"tool_call_id": toolCall["id"],
					}
				}
			}
		} else if msgType == "tool_return_message" {
			// For tool messages, extract tool return information
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

	// Add usage statistics message at the end (matching Python API)
	usageStats := map[string]interface{}{
		"message_type":      "usage_statistics",
		"completion_tokens": 0,
		"prompt_tokens":     0,
		"total_tokens":      0,
		"step_count":        len(transformedMessages),
		"steps_messages":    nil,
		"run_ids":           nil,
		"agent_id":          "", // Will be filled by calling function
		"processed_at":      time.Now().Format(time.RFC3339),
		"status":            "done",
		"model_names":       []string{},
	}
	transformedMessages = append(transformedMessages, usageStats)

	return transformedMessages
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
			// Transform to Python API format
			if usageMap, ok := usageMetadata.(map[string]interface{}); ok {
				result := map[string]interface{}{
					"prompt_token_count":     usageMap["input_tokens"],
					"candidates_token_count": usageMap["output_tokens"],
					"total_token_count":      usageMap["total_tokens"],
				}

				// Safely extract nested fields with proper type conversion
				if outputDetails, exists := usageMap["output_token_details"].(map[string]interface{}); exists {
					if reasoning, exists := outputDetails["reasoning"]; exists {
						if reasoningInt, ok := reasoning.(int); ok {
							result["thoughts_token_count"] = float64(reasoningInt)
						}
					}
				}

				if inputDetails, exists := usageMap["input_token_details"].(map[string]interface{}); exists {
					if cacheRead, exists := inputDetails["cache_read"]; exists {
						if cacheReadInt, ok := cacheRead.(int); ok {
							result["cached_content_token_count"] = float64(cacheReadInt)
						}
					}
				}

				// Convert other fields to float64 to match Python API
				if inputTokens, exists := usageMap["input_tokens"]; exists {
					if inputInt, ok := inputTokens.(int); ok {
						result["prompt_token_count"] = float64(inputInt)
					}
				}
				if outputTokens, exists := usageMap["output_tokens"]; exists {
					if outputInt, ok := outputTokens.(int); ok {
						result["candidates_token_count"] = float64(outputInt)
					}
				}
				if totalTokens, exists := usageMap["total_tokens"]; exists {
					if totalInt, ok := totalTokens.(int); ok {
						result["total_token_count"] = float64(totalInt)
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

// executeCallbackOnError handles the callback execution for failed tasks
func executeCallbackOnError(ctx context.Context, deps *MessageHandlerDependencies, messageID string, callbackURL string, processErr error, logger *logrus.Entry) {
	callbackLogger := logger.WithFields(logrus.Fields{
		"callback_url": callbackURL,
		"message_id":   messageID,
		"error_type":   "failure",
	})

	callbackLogger.Info("Executing error callback for failed task")

	// Create error message
	errorMessage := processErr.Error()

	// Create callback payload with error information
	payload := models.CallbackPayload{
		MessageID: messageID,
		Status:    "failed", // Status indicates failure
		Data: models.ProcessedMessageData{
			Messages:    []string{errorMessage}, // Error message in messages array
			AgentID:     "",                     // No agent_id for errors
			ProcessedAt: messageID,
			Status:      "error",
		},
		Error:       &errorMessage, // Error description
		Timestamp:   time.Now().UTC().Format(time.RFC3339),
		ProcessedAt: messageID,
	}

	// Execute the callback with retry logic
	if err := deps.CallbackService.ExecuteCallback(ctx, callbackURL, payload); err != nil {
		callbackLogger.WithError(err).Error("Failed to execute error callback after all retries")
		// Store callback failure for debugging
		errorKey := "callback:error:" + messageID
		errorMsg := fmt.Sprintf("Failed to execute error callback: %v", err)
		_ = deps.RedisService.Set(ctx, errorKey, errorMsg, deps.Config.Redis.TaskStatusTTL)
	} else {
		callbackLogger.Info("Error callback executed successfully")
		// Clean up callback URL from Redis
		_ = deps.RedisService.DeleteCallbackURL(ctx, messageID)
	}
}
