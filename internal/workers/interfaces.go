package workers

import (
	"context"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/sirupsen/logrus"

	"github.com/prefeitura-rio/app-eai-agent-gateway/internal/config"
	"github.com/prefeitura-rio/app-eai-agent-gateway/internal/models"
)

// Worker defines the interface that all workers must implement
type Worker interface {
	// Start begins processing messages from the assigned queue
	Start(ctx context.Context) error

	// Stop gracefully stops the worker
	Stop(ctx context.Context) error

	// ProcessMessage handles a single message from the queue
	ProcessMessage(ctx context.Context, delivery amqp.Delivery) MessageProcessingResult

	// GetQueueName returns the queue name this worker processes
	GetQueueName() string

	// GetWorkerType returns the type of this worker
	GetWorkerType() models.WorkerType

	// IsRunning returns whether the worker is currently running
	IsRunning() bool
}

// WorkerMetrics defines metrics that workers should collect
type WorkerMetrics interface {
	// RecordMessageProcessed increments the count of processed messages
	RecordMessageProcessed(workerType models.WorkerType, success bool, duration time.Duration)

	// RecordQueueDepth records the current queue depth
	RecordQueueDepth(queueName string, depth int)

	// RecordWorkerHealth records worker health status
	RecordWorkerHealth(workerType models.WorkerType, healthy bool)
}

// MessageProcessingResult represents the result of processing a message
type MessageProcessingResult struct {
	Success    bool
	Content    *string
	Error      error
	Duration   time.Duration
	RetryCount int
}

// WorkerDependencies contains all the dependencies a worker needs
type WorkerDependencies struct {
	Config  *config.Config
	Logger  *logrus.Logger
	Metrics WorkerMetrics

	// External services
	RedisService      RedisServiceInterface
	TranscribeService TranscribeServiceInterface
	AgentClient       AgentClientInterface
	MessageFormatter  MessageFormatterInterface
}

// RedisServiceInterface defines Redis operations needed by workers
type RedisServiceInterface interface {
	SetTaskStatus(ctx context.Context, taskID string, status string, ttl time.Duration) error
	GetTaskStatus(ctx context.Context, taskID string) (string, error)
	SetTaskResult(ctx context.Context, taskID string, result interface{}, ttl time.Duration) error
	GetTaskResult(ctx context.Context, taskID string, dest interface{}) error
	Get(ctx context.Context, key string) (string, error)
	Set(ctx context.Context, key string, value string, ttl time.Duration) error
	SetValue(ctx context.Context, key string, value interface{}, ttl time.Duration) error
	Delete(ctx context.Context, key string) error
}

// TranscribeServiceInterface defines audio transcription operations
type TranscribeServiceInterface interface {
	TranscribeAudio(ctx context.Context, audioURL string) (string, error)
	IsAudioURL(url string) bool
	ValidateAudioURL(url string) error
}

// AgentClientInterface defines Google Agent Engine operations
type AgentClientInterface interface {
	// CreateThread creates a new conversation thread
	CreateThread(ctx context.Context, userID string) (string, error)

	// GetOrCreateThread gets existing thread or creates new one for user
	GetOrCreateThread(ctx context.Context, userID string) (string, error)

	// SendMessage sends a message to a thread and gets response
	SendMessage(ctx context.Context, threadID string, content string) (*models.AgentResponse, error)
}

// MessageFormatterInterface defines message formatting operations
type MessageFormatterInterface interface {
	// FormatForWhatsApp converts agent response to WhatsApp-compatible format
	FormatForWhatsApp(ctx context.Context, response *models.AgentResponse) (string, error)

	// FormatErrorMessage creates a user-friendly error message
	FormatErrorMessage(ctx context.Context, err error) string

	// ValidateMessageContent validates message content before processing
	ValidateMessageContent(content string) error
}

// RabbitMQServiceInterface defines RabbitMQ operations needed by workers
type RabbitMQServiceInterface interface {
	PublishMessage(ctx context.Context, exchange, routingKey string, message interface{}) error
}

// ConsumerManagerInterface defines consumer management operations
type ConsumerManagerInterface interface {
	AddConsumer(ctx context.Context, rabbitmq interface{}, queueName string, concurrency int, handler func(context.Context, amqp.Delivery) error) error
	RemoveConsumer(queueName string) error
}

// WorkerManager manages multiple workers
type WorkerManager interface {
	// StartWorker starts a specific worker type
	StartWorker(ctx context.Context, workerType models.WorkerType, concurrency int) error

	// StopWorker stops a specific worker type
	StopWorker(ctx context.Context, workerType models.WorkerType) error

	// StopAll stops all workers gracefully
	StopAll(ctx context.Context) error

	// GetWorkerStatus returns the status of all workers
	GetWorkerStatus() map[models.WorkerType]WorkerStatus

	// GetMetrics returns worker metrics
	GetMetrics() WorkerMetrics
}

// WorkerStatus represents the current status of a worker
type WorkerStatus struct {
	Type        models.WorkerType `json:"type"`
	Running     bool              `json:"running"`
	Concurrency int               `json:"concurrency"`
	QueueName   string            `json:"queue_name"`
	StartedAt   *time.Time        `json:"started_at,omitempty"`
	LastSeen    *time.Time        `json:"last_seen,omitempty"`
}

// ProcessingContext contains context information for message processing
type ProcessingContext struct {
	MessageID     string
	UserID        string
	AgentID       string
	ThreadID      string
	CorrelationID string
	StartTime     time.Time
	RetryCount    int
	Logger        *logrus.Entry
}

// NewProcessingContext creates a new processing context from a queue message
func NewProcessingContext(queueMessage *models.QueueMessage, logger *logrus.Logger) *ProcessingContext {
	// Extract metadata
	correlationID := ""
	if queueMessage.Metadata != nil {
		if id, ok := queueMessage.Metadata["request_id"].(string); ok {
			correlationID = id
		}
	}

	// Create structured logger
	loggerEntry := logger.WithFields(logrus.Fields{
		"message_id":     queueMessage.ID,
		"message_type":   queueMessage.Type,
		"user_number":    queueMessage.UserNumber,
		"agent_id":       queueMessage.AgentID,
		"correlation_id": correlationID,
	})

	return &ProcessingContext{
		MessageID:     queueMessage.ID,
		UserID:        queueMessage.UserNumber,
		AgentID:       queueMessage.AgentID,
		CorrelationID: correlationID,
		StartTime:     time.Now(),
		Logger:        loggerEntry,
	}
}
