package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"

	"github.com/prefeitura-rio/app-eai-agent-gateway/internal/config"
	"github.com/prefeitura-rio/app-eai-agent-gateway/internal/models"
)

// RedisServiceInterface defines Redis operations needed by MessageHandler
type RedisServiceInterface interface {
	SetTaskStatus(ctx context.Context, taskID string, status string, ttl time.Duration) error
	GetTaskStatus(ctx context.Context, taskID string) (string, error)
	GetTaskResult(ctx context.Context, taskID string, dest interface{}) error
	Get(ctx context.Context, key string) (string, error)
	Set(ctx context.Context, key string, value string, ttl time.Duration) error
	Ping(ctx context.Context) error
}

// RabbitMQServiceInterface defines RabbitMQ operations needed by MessageHandler
type RabbitMQServiceInterface interface {
	PublishMessage(ctx context.Context, queueName string, message interface{}) error
	IsConnected() bool
}

// MessageHandler handles message processing endpoints
type MessageHandler struct {
	logger          *logrus.Logger
	config          *config.Config
	redisService    RedisServiceInterface
	rabbitMQService RabbitMQServiceInterface
}

// NewMessageHandler creates a new message handler
func NewMessageHandler(
	logger *logrus.Logger,
	config *config.Config,
	redisService RedisServiceInterface,
	rabbitMQService RabbitMQServiceInterface,
) *MessageHandler {
	return &MessageHandler{
		logger:          logger,
		config:          config,
		redisService:    redisService,
		rabbitMQService: rabbitMQService,
	}
}

// HandleUserWebhook processes user messages and queues them for processing
//
//	@Summary		Process user message webhook
//	@Description	Accepts a user message and queues it for processing by AI agents
//	@Tags			Messages
//	@Accept			json
//	@Produce		json
//	@Param			request	body		models.UserWebhookRequest	true	"User message request"
//	@Success		202		{object}	models.WebhookResponse		"Message queued successfully"
//	@Failure		400		{object}	map[string]interface{}		"Invalid request"
//	@Failure		500		{object}	map[string]interface{}		"Internal server error"
//	@Router			/api/v1/message/webhook/user [post]
func (h *MessageHandler) HandleUserWebhook(c *gin.Context) {
	var req models.UserWebhookRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		h.logger.WithError(err).Error("Invalid user webhook request")
		c.JSON(http.StatusBadRequest, gin.H{
			"error":   "Invalid request",
			"message": err.Error(),
		})
		return
	}

	// Generate message ID for tracking
	messageID := models.GenerateMessageID()

	// Set default provider if not specified
	provider := "google_agent_engine" // Default provider
	if req.Provider != nil && *req.Provider != "" {
		provider = *req.Provider
	}

	logger := h.logger.WithFields(logrus.Fields{
		"message_id":           messageID,
		"user_number":          req.UserNumber,
		"provider":             provider,
		"has_previous_message": req.PreviousMessage != nil,
		"message_length":       len(req.Message),
	})

	logger.Info("Processing user webhook request")

	// Store initial status to handle immediate polling (like Python API)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := h.redisService.SetTaskStatus(ctx, messageID, string(models.TaskStatusProcessing), h.config.Redis.TaskStatusTTL); err != nil {
		logger.WithError(err).Error("Failed to set initial task status")
		c.JSON(http.StatusInternalServerError, gin.H{
			"error":   "Internal server error",
			"message": "Failed to initialize task tracking",
		})
		return
	}

	// Create queue message with Python API structure
	queueMessage := models.QueueMessage{
		ID:              messageID,
		Type:            "user_message",
		UserNumber:      req.UserNumber,
		Message:         req.Message,
		PreviousMessage: req.PreviousMessage,
		Provider:        provider,
		Timestamp:       time.Now(),
		Metadata:        req.Metadata,
	}

	// Add request metadata
	if queueMessage.Metadata == nil {
		queueMessage.Metadata = make(map[string]interface{})
	}
	queueMessage.Metadata["request_id"] = c.GetString("request_id")
	queueMessage.Metadata["source"] = "webhook"

	// Store metadata in Redis for later retrieval when building the response
	metadataForResponse := map[string]interface{}{
		"user_number": req.UserNumber,
		"provider":    provider,
	}
	if metadataBytes, err := json.Marshal(metadataForResponse); err == nil {
		metadataKey := "task:metadata:" + messageID
		_ = h.redisService.Set(ctx, metadataKey, string(metadataBytes), h.config.Redis.TaskStatusTTL)
	}

	// Queue message for processing
	if err := h.rabbitMQService.PublishMessage(ctx, h.config.RabbitMQ.UserMessagesQueue, queueMessage); err != nil {
		logger.WithError(err).Error("Failed to queue user message")

		// Update task status to failed
		_ = h.redisService.SetTaskStatus(ctx, messageID, string(models.TaskStatusFailed), h.config.Redis.TaskStatusTTL)

		c.JSON(http.StatusInternalServerError, gin.H{
			"error":   "Internal server error",
			"message": "Failed to queue message for processing",
		})
		return
	}

	logger.Info("User message queued successfully")

	// Return response with message ID for polling (Python API format with status 201)
	c.JSON(http.StatusCreated, models.WebhookResponse{
		MessageID:       messageID,
		Status:          string(models.TaskStatusProcessing),
		PollingEndpoint: "/api/v1/message/response?message_id=" + messageID,
	})
}

// HandleMessageResponse handles polling for message processing results
//
//	@Summary		Get message response
//	@Description	Poll for the processing result of a message by message ID
//	@Tags			Messages
//	@Accept			json
//	@Produce		json
//	@Param			message_id	query		string					true	"Message ID (UUID)"
//	@Success		200			{object}	models.MessageResponse	"Message completed or failed"
//	@Success		202			{object}	models.MessageResponse	"Message still processing"
//	@Failure		400			{object}	map[string]interface{}	"Invalid request or message ID format"
//	@Failure		404			{object}	map[string]interface{}	"Message not found"
//	@Failure		500			{object}	map[string]interface{}	"Internal server error"
//	@Router			/api/v1/message/response [get]
func (h *MessageHandler) HandleMessageResponse(c *gin.Context) {
	var req models.MessageResponseRequest
	if err := c.ShouldBindQuery(&req); err != nil {
		h.logger.WithError(err).Error("Invalid message response request")
		c.JSON(http.StatusBadRequest, gin.H{
			"error":   "Invalid request",
			"message": err.Error(),
		})
		return
	}

	logger := h.logger.WithField("message_id", req.MessageID)
	logger.Debug("Handling message response request")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Get task status from Redis
	status, err := h.redisService.GetTaskStatus(ctx, req.MessageID)
	if err != nil {
		logger.WithError(err).Error("Failed to get task status")
		c.JSON(http.StatusNotFound, gin.H{
			"error":   "Task not found",
			"message": "No task found with the provided message ID",
		})
		return
	}

	response := models.MessageResponse{
		Status: status,
	}

	// If task is completed, get the result
	if status == string(models.TaskStatusCompleted) {
		var result string
		if err := h.redisService.GetTaskResult(ctx, req.MessageID, &result); err != nil {
			logger.WithError(err).Warn("Task completed but no result found")
		} else {
			// The result is already processed by the worker and contains the final ProcessedMessageData
			var processedData models.ProcessedMessageData
			if err := json.Unmarshal([]byte(result), &processedData); err != nil {
				logger.WithFields(logrus.Fields{
					"error":         err.Error(),
					"raw_result":    result,
					"result_length": len(result),
				}).Error("Failed to parse processed result from worker")
				c.JSON(http.StatusInternalServerError, gin.H{
					"error":   "Internal server error",
					"message": "Failed to parse worker response",
				})
				return
			}

			response.Data = processedData
		}
	}

	// If task failed, try to get error information
	if status == string(models.TaskStatusFailed) {
		// Try to get error details from Redis (could be stored by worker)
		errorKey := "task:error:" + req.MessageID
		if errorMsg, err := h.redisService.Get(ctx, errorKey); err == nil {
			response.Error = &errorMsg
		}
	}

	logger.WithField("status", status).Debug("Returning message response")

	// Return appropriate HTTP status code based on task status (matches Python API)
	var httpStatus int
	switch status {
	case string(models.TaskStatusCompleted), string(models.TaskStatusFailed):
		httpStatus = http.StatusOK // 200 for completed/failed
	case string(models.TaskStatusPending), string(models.TaskStatusProcessing):
		httpStatus = http.StatusAccepted // 202 for pending/processing
	default:
		httpStatus = http.StatusOK // Default to 200 for unknown statuses
	}

	c.JSON(httpStatus, response)
}

// HandleDebugTaskStatus provides debug information about task processing
//
//	@Summary		Get task debug status
//	@Description	Get detailed debug information about message processing task status
//	@Tags			Debug
//	@Accept			json
//	@Produce		json
//	@Param			message_id	query		string					true	"Message ID (UUID)"
//	@Success		200			{object}	models.TaskDebugInfo	"Task debug information"
//	@Failure		400			{object}	map[string]interface{}	"Invalid request or message ID format"
//	@Failure		404			{object}	map[string]interface{}	"Task not found"
//	@Failure		500			{object}	map[string]interface{}	"Internal server error"
//	@Router			/api/v1/message/debug/task-status [get]
func (h *MessageHandler) HandleDebugTaskStatus(c *gin.Context) {
	messageID := c.Query("message_id")
	if messageID == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"error":   "Missing parameter",
			"message": "message_id query parameter is required",
		})
		return
	}

	if !models.IsValidUUID(messageID) {
		c.JSON(http.StatusBadRequest, gin.H{
			"error":   "Invalid parameter",
			"message": "message_id must be a valid UUID",
		})
		return
	}

	logger := h.logger.WithField("message_id", messageID)
	logger.Debug("Handling debug task status request")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Get task status
	status, err := h.redisService.GetTaskStatus(ctx, messageID)
	if err != nil {
		logger.WithError(err).Error("Failed to get task status")
		c.JSON(http.StatusNotFound, gin.H{
			"error":   "Task not found",
			"message": "No task found with the provided message ID",
		})
		return
	}

	// Create debug info
	debugInfo := models.TaskDebugInfo{
		MessageID: messageID,
		Status:    models.TaskStatus(status),
		UpdatedAt: time.Now(),
	}

	// Try to get additional debug information from Redis
	// These keys would be set by the message processing workers
	if retryCount, err := h.redisService.Get(ctx, "task:retry:"+messageID); err == nil {
		if count := parseRetryCount(retryCount); count >= 0 {
			debugInfo.RetryCount = count
		}
	}

	if errorMsg, err := h.redisService.Get(ctx, "task:error:"+messageID); err == nil {
		debugInfo.LastError = &errorMsg
	}

	if createdAt, err := h.redisService.Get(ctx, "task:created:"+messageID); err == nil {
		if timestamp, err := time.Parse(time.RFC3339, createdAt); err == nil {
			debugInfo.CreatedAt = timestamp
		}
	}

	// Get queue information (simplified)
	debugInfo.QueueInfo = map[string]interface{}{
		"rabbitmq_connected": h.rabbitMQService.IsConnected(),
		"redis_connected":    h.redisService.Ping(ctx) == nil,
	}

	logger.Debug("Returning debug task status")

	c.JSON(http.StatusOK, debugInfo)
}

// parseRetryCount safely parses retry count from string
func parseRetryCount(s string) int {
	// Simple implementation - in production you might want more robust parsing
	if s == "0" {
		return 0
	}
	if s == "1" {
		return 1
	}
	if s == "2" {
		return 2
	}
	if s == "3" {
		return 3
	}
	return -1 // Invalid
}
