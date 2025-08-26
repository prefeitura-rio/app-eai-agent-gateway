package workers

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/sirupsen/logrus"

	"github.com/prefeitura-rio/app-eai-agent-gateway/internal/models"
)

// BaseWorker provides common functionality for all workers
type BaseWorker struct {
	workerType  models.WorkerType
	queueName   string
	concurrency int
	deps        *WorkerDependencies

	// Runtime state
	isRunning bool
	startedAt *time.Time
	lastSeen  *time.Time
	stopChan  chan struct{}
	wg        sync.WaitGroup
	mutex     sync.RWMutex

	// RabbitMQ integration
	rabbitMQ    RabbitMQServiceInterface
	consumerMgr ConsumerManagerInterface
}

// NewBaseWorker creates a new base worker
func NewBaseWorker(
	workerType models.WorkerType,
	queueName string,
	concurrency int,
	deps *WorkerDependencies,
	rabbitMQ RabbitMQServiceInterface,
	consumerMgr ConsumerManagerInterface,
) *BaseWorker {
	return &BaseWorker{
		workerType:  workerType,
		queueName:   queueName,
		concurrency: concurrency,
		deps:        deps,
		rabbitMQ:    rabbitMQ,
		consumerMgr: consumerMgr,
		stopChan:    make(chan struct{}),
	}
}

// Start begins processing messages from the queue
func (w *BaseWorker) Start(ctx context.Context) error {
	w.mutex.Lock()
	defer w.mutex.Unlock()

	if w.isRunning {
		return fmt.Errorf("worker %s is already running", w.workerType)
	}

	w.deps.Logger.WithFields(logrus.Fields{
		"worker_type": w.workerType,
		"queue_name":  w.queueName,
		"concurrency": w.concurrency,
	}).Info("Starting worker")

	// Start the consumer
	err := w.consumerMgr.AddConsumer(ctx, w.rabbitMQ, w.queueName, w.concurrency, w.handleMessage)
	if err != nil {
		return fmt.Errorf("failed to start consumer for worker %s: %w", w.workerType, err)
	}

	// Update state
	now := time.Now()
	w.isRunning = true
	w.startedAt = &now
	w.lastSeen = &now

	// Record metrics
	w.deps.Metrics.RecordWorkerHealth(w.workerType, true)

	w.deps.Logger.WithField("worker_type", w.workerType).Info("Worker started successfully")
	return nil
}

// Stop gracefully stops the worker
func (w *BaseWorker) Stop(ctx context.Context) error {
	w.mutex.Lock()
	defer w.mutex.Unlock()

	if !w.isRunning {
		return nil
	}

	w.deps.Logger.WithField("worker_type", w.workerType).Info("Stopping worker")

	// Stop the consumer
	err := w.consumerMgr.RemoveConsumer(w.queueName)
	if err != nil {
		w.deps.Logger.WithError(err).WithField("worker_type", w.workerType).Error("Failed to stop consumer")
	}

	// Signal stop
	close(w.stopChan)

	// Wait for goroutines to finish with timeout
	done := make(chan struct{})
	go func() {
		w.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		w.deps.Logger.WithField("worker_type", w.workerType).Info("Worker stopped gracefully")
	case <-ctx.Done():
		w.deps.Logger.WithField("worker_type", w.workerType).Warn("Worker stop timed out")
	}

	// Update state
	w.isRunning = false
	w.startedAt = nil
	w.lastSeen = nil

	// Record metrics
	w.deps.Metrics.RecordWorkerHealth(w.workerType, false)

	return nil
}

// handleMessage is the entry point for all message processing
func (w *BaseWorker) handleMessage(ctx context.Context, delivery amqp.Delivery) error {
	// Update last seen
	w.mutex.Lock()
	now := time.Now()
	w.lastSeen = &now
	w.mutex.Unlock()

	start := time.Now()
	var success bool
	defer func() {
		duration := time.Since(start)
		w.deps.Metrics.RecordMessageProcessed(w.workerType, success, duration)
	}()

	// Parse the message
	queueMessage, err := w.parseMessage(delivery)
	if err != nil {
		w.deps.Logger.WithError(err).Error("Failed to parse queue message")
		return err
	}

	// Create processing context
	procCtx := NewProcessingContext(queueMessage, w.deps.Logger)
	procCtx.Logger.Info("Processing message")

	// Set processing status in Redis
	err = w.deps.RedisService.SetTaskStatus(ctx, procCtx.MessageID, string(models.TaskStatusProcessing), w.deps.Config.Redis.TaskStatusTTL)
	if err != nil {
		procCtx.Logger.WithError(err).Error("Failed to set processing status")
		// Continue processing anyway
	}

	// Process the message (implemented by concrete workers)
	result := w.ProcessMessage(ctx, delivery)

	// Update final status based on result
	if result.Success {
		success = true
		procCtx.Logger.Info("Message processed successfully")

		// Store result in Redis
		if result.Content != nil {
			err = w.deps.RedisService.SetTaskResult(ctx, procCtx.MessageID, *result.Content, w.deps.Config.Redis.TaskResultTTL)
			if err != nil {
				procCtx.Logger.WithError(err).Error("Failed to store task result")
			}
		}

		// Set completed status
		err = w.deps.RedisService.SetTaskStatus(ctx, procCtx.MessageID, string(models.TaskStatusCompleted), w.deps.Config.Redis.TaskStatusTTL)
		if err != nil {
			procCtx.Logger.WithError(err).Error("Failed to set completed status")
		}

	} else {
		procCtx.Logger.WithError(result.Error).Error("Message processing failed")

		// Store error information using a fresh context to avoid timeout issues
		redisCtx, redisCancel := context.WithTimeout(context.Background(), 5*time.Second)
		errorKey := "task:error:" + procCtx.MessageID
		errorMsg := w.deps.MessageFormatter.FormatErrorMessage(redisCtx, result.Error)
		err = w.deps.RedisService.Set(redisCtx, errorKey, errorMsg, w.deps.Config.Redis.TaskStatusTTL)
		if err != nil {
			procCtx.Logger.WithError(err).Error("Failed to store error in Redis")
		}

		// Set failed status
		err = w.deps.RedisService.SetTaskStatus(redisCtx, procCtx.MessageID, string(models.TaskStatusFailed), w.deps.Config.Redis.TaskStatusTTL)
		if err != nil {
			procCtx.Logger.WithError(err).Error("Failed to update task status to failed")
		}
		redisCancel()

		return result.Error
	}

	return nil
}

// parseMessage parses an AMQP delivery into a QueueMessage
func (w *BaseWorker) parseMessage(delivery amqp.Delivery) (*models.QueueMessage, error) {
	var queueMessage models.QueueMessage
	err := json.Unmarshal(delivery.Body, &queueMessage)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal queue message: %w", err)
	}

	// Validate required fields
	if queueMessage.ID == "" {
		return nil, fmt.Errorf("message ID is required")
	}

	if queueMessage.Type == "" {
		return nil, fmt.Errorf("message type is required")
	}

	return &queueMessage, nil
}

// ProcessMessage is implemented by concrete workers
func (w *BaseWorker) ProcessMessage(ctx context.Context, delivery amqp.Delivery) MessageProcessingResult {
	return MessageProcessingResult{
		Success: false,
		Error:   fmt.Errorf("ProcessMessage not implemented for worker type %s", w.workerType),
	}
}

// GetQueueName returns the queue name this worker processes
func (w *BaseWorker) GetQueueName() string {
	return w.queueName
}

// GetWorkerType returns the type of this worker
func (w *BaseWorker) GetWorkerType() models.WorkerType {
	return w.workerType
}

// IsRunning returns whether the worker is currently running
func (w *BaseWorker) IsRunning() bool {
	w.mutex.RLock()
	defer w.mutex.RUnlock()
	return w.isRunning
}

// GetStatus returns the current status of the worker
func (w *BaseWorker) GetStatus() WorkerStatus {
	w.mutex.RLock()
	defer w.mutex.RUnlock()

	return WorkerStatus{
		Type:        w.workerType,
		Running:     w.isRunning,
		Concurrency: w.concurrency,
		QueueName:   w.queueName,
		StartedAt:   w.startedAt,
		LastSeen:    w.lastSeen,
	}
}
