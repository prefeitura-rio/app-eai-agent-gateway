package services

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/google/uuid"
	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/sirupsen/logrus"
)

// Consumer represents a message consumer for a specific queue
type Consumer struct {
	rabbitMQ    *RabbitMQService
	logger      *logrus.Logger
	queueName   string
	concurrency int
	handler     MessageHandler

	// Consumer lifecycle
	isRunning bool
	stopChan  chan struct{}
	wg        sync.WaitGroup
	mutex     sync.RWMutex
}

// ConsumerManager manages multiple consumers
type ConsumerManager struct {
	consumers map[string]*Consumer
	mutex     sync.RWMutex
	logger    *logrus.Logger
}

// NewConsumerManager creates a new consumer manager
func NewConsumerManager(logger *logrus.Logger) *ConsumerManager {
	return &ConsumerManager{
		consumers: make(map[string]*Consumer),
		logger:    logger,
	}
}

// ConsumeMessages implements MessageConsumer interface
func (r *RabbitMQService) ConsumeMessages(ctx context.Context, queueName string, handler MessageHandler) error {
	r.mutex.RLock()
	defer r.mutex.RUnlock()

	if !r.isConnected {
		return fmt.Errorf("RabbitMQ connection is not available")
	}

	// Start consuming
	msgs, err := r.channel.Consume(
		queueName, // queue
		"",        // consumer tag (empty = auto-generated)
		false,     // auto-ack (we'll manually ack)
		false,     // exclusive
		false,     // no-local
		false,     // no-wait
		nil,       // arguments
	)
	if err != nil {
		return fmt.Errorf("failed to register consumer for queue %s: %w", queueName, err)
	}

	r.logger.WithField("queue", queueName).Info("Started consuming messages")

	// Process messages
	for {
		select {
		case <-ctx.Done():
			r.logger.WithField("queue", queueName).Info("Consumer context cancelled")
			return ctx.Err()
		case msg, ok := <-msgs:
			if !ok {
				r.logger.WithField("queue", queueName).Info("Message channel closed")
				return nil
			}
			r.processMessage(ctx, msg, handler)
		}
	}
}

// StartConsumer implements MessageConsumer interface with concurrency support
func (r *RabbitMQService) StartConsumer(ctx context.Context, queueName string, concurrency int, handler MessageHandler) error {
	r.mutex.RLock()
	defer r.mutex.RUnlock()

	if !r.isConnected {
		return fmt.Errorf("RabbitMQ connection is not available")
	}

	consumer := &Consumer{
		rabbitMQ:    r,
		logger:      r.logger,
		queueName:   queueName,
		concurrency: concurrency,
		handler:     handler,
		stopChan:    make(chan struct{}),
	}

	// Start multiple goroutines for concurrent processing
	for i := 0; i < concurrency; i++ {
		consumer.wg.Add(1)
		go consumer.workerLoop(ctx, i)
	}

	consumer.mutex.Lock()
	consumer.isRunning = true
	consumer.mutex.Unlock()

	r.logger.WithFields(logrus.Fields{
		"queue":       queueName,
		"concurrency": concurrency,
	}).Info("Started consumer with concurrency")

	return nil
}

// StopConsumer stops a consumer (placeholder for interface compatibility)
func (r *RabbitMQService) StopConsumer(queueName string) error {
	// This would be implemented by the ConsumerManager
	return fmt.Errorf("use ConsumerManager for stopping consumers")
}

// workerLoop runs a single worker goroutine
func (c *Consumer) workerLoop(ctx context.Context, workerID int) {
	defer c.wg.Done()

	// Generate unique consumer tag to avoid conflicts between containers
	hostname, _ := os.Hostname()
	uniqueID := uuid.New().String()[:8] // Use first 8 chars of UUID
	consumerTag := fmt.Sprintf("worker_%s_%s_%d", hostname, uniqueID, workerID)

	logger := c.logger.WithFields(logrus.Fields{
		"queue":        c.queueName,
		"worker_id":    workerID,
		"consumer_tag": consumerTag,
	})

	// Start consuming for this worker
	msgs, err := c.rabbitMQ.channel.Consume(
		c.queueName, // queue
		consumerTag, // consumer tag (unique across containers)
		false,       // auto-ack
		false,       // exclusive
		false,       // no-local
		false,       // no-wait
		nil,         // arguments
	)
	if err != nil {
		logger.WithError(err).Error("Failed to start consuming messages")
		return
	}

	logger.Info("Worker started consuming messages")

	for {
		select {
		case <-ctx.Done():
			logger.Info("Worker context cancelled")
			return
		case <-c.stopChan:
			logger.Info("Worker received stop signal")
			return
		case msg, ok := <-msgs:
			if !ok {
				logger.Info("Message channel closed for worker")
				return
			}
			c.processMessageWithRetry(ctx, msg, logger)
		}
	}
}

// processMessage handles a single message
func (r *RabbitMQService) processMessage(ctx context.Context, msg amqp.Delivery, handler MessageHandler) {
	logger := r.logger.WithFields(logrus.Fields{
		"message_id":   msg.MessageId,
		"routing_key":  msg.RoutingKey,
		"delivery_tag": msg.DeliveryTag,
		"redelivered":  msg.Redelivered,
	})

	logger.Debug("Processing message")

	// Create a context with timeout for message processing
	msgCtx, cancel := context.WithTimeout(ctx, r.config.RabbitMQ.MessageTimeout)
	defer cancel()

	// Process the message
	err := handler(msgCtx, msg)
	if err != nil {
		logger.WithError(err).Error("Message processing failed")

		// Check if this is a retry (redelivered message)
		if msg.Redelivered {
			logger.Error("Message failed after retry, sending to DLQ")
			// Reject without requeue (sends to DLQ)
			if err := msg.Reject(false); err != nil {
				logger.WithError(err).Error("Failed to reject message")
			}
		} else {
			logger.Info("Message failed, requeueing for retry")
			// Reject with requeue for retry
			if err := msg.Reject(true); err != nil {
				logger.WithError(err).Error("Failed to requeue message")
			}
		}
		return
	}

	// Acknowledge successful processing
	if err := msg.Ack(false); err != nil {
		logger.WithError(err).Error("Failed to acknowledge message")
	} else {
		logger.Debug("Message processed successfully")
	}
}

// processMessageWithRetry handles message processing with retry logic
func (c *Consumer) processMessageWithRetry(ctx context.Context, msg amqp.Delivery, logger *logrus.Entry) {
	logger = logger.WithFields(logrus.Fields{
		"message_id":   msg.MessageId,
		"routing_key":  msg.RoutingKey,
		"delivery_tag": msg.DeliveryTag,
		"redelivered":  msg.Redelivered,
	})

	logger.Debug("Processing message with retry logic")

	// Extract retry count from headers
	retryCount := int64(0)
	if msg.Headers != nil {
		if count, ok := msg.Headers["x-retry-count"].(int64); ok {
			retryCount = count
		}
	}

	maxRetries := int64(c.rabbitMQ.config.RabbitMQ.MaxRetries)

	// Create a context with timeout for message processing
	msgCtx, cancel := context.WithTimeout(ctx, c.rabbitMQ.config.RabbitMQ.MessageTimeout)
	defer cancel()

	// Process the message
	err := c.handler(msgCtx, msg)
	if err != nil {
		logger.WithError(err).WithField("retry_count", retryCount).Error("Message processing failed")

		if retryCount >= maxRetries {
			logger.WithField("max_retries", maxRetries).Error("Message exceeded maximum retries, sending to DLQ")
			// Send to DLQ
			if err := msg.Reject(false); err != nil {
				logger.WithError(err).Error("Failed to reject message to DLQ")
			}
			return
		}

		// Retry the message with exponential backoff
		retryDelay := time.Duration((retryCount+1)*(retryCount+1)) * time.Second
		logger.WithFields(logrus.Fields{
			"retry_count": retryCount + 1,
			"retry_delay": retryDelay,
		}).Info("Retrying message with delay")

		// Publish retry message with increased retry count
		c.publishRetryMessage(msg, retryCount+1, retryDelay, logger)

		// Acknowledge original message
		if err := msg.Ack(false); err != nil {
			logger.WithError(err).Error("Failed to acknowledge message for retry")
		}
		return
	}

	// Acknowledge successful processing
	if err := msg.Ack(false); err != nil {
		logger.WithError(err).Error("Failed to acknowledge message")
	} else {
		logger.Debug("Message processed successfully")
	}
}

// publishRetryMessage publishes a message for retry with delay
func (c *Consumer) publishRetryMessage(originalMsg amqp.Delivery, retryCount int64, delay time.Duration, logger *logrus.Entry) {
	// Prepare headers with retry information
	headers := amqp.Table{
		"x-retry-count": retryCount,
		"x-delay":       int64(delay.Milliseconds()),
	}

	// Copy original headers and add retry info
	if originalMsg.Headers != nil {
		for k, v := range originalMsg.Headers {
			if k != "x-retry-count" && k != "x-delay" {
				headers[k] = v
			}
		}
	}

	// Publish retry message
	err := c.rabbitMQ.channel.Publish(
		c.rabbitMQ.config.RabbitMQ.Exchange, // exchange
		c.queueName,                         // routing key
		false,                               // mandatory
		false,                               // immediate
		amqp.Publishing{
			ContentType:  originalMsg.ContentType,
			Body:         originalMsg.Body,
			DeliveryMode: amqp.Persistent,
			Headers:      headers,
			Timestamp:    time.Now(),
			MessageId:    originalMsg.MessageId + "_retry_" + fmt.Sprintf("%d", retryCount),
		},
	)

	if err != nil {
		logger.WithError(err).Error("Failed to publish retry message")
	}
}

// AddConsumer adds a new consumer to the manager
func (cm *ConsumerManager) AddConsumer(ctx context.Context, rabbitMQ *RabbitMQService, queueName string, concurrency int, handler MessageHandler) error {
	cm.mutex.Lock()
	defer cm.mutex.Unlock()

	if _, exists := cm.consumers[queueName]; exists {
		return fmt.Errorf("consumer for queue %s already exists", queueName)
	}

	consumer := &Consumer{
		rabbitMQ:    rabbitMQ,
		logger:      cm.logger,
		queueName:   queueName,
		concurrency: concurrency,
		handler:     handler,
		stopChan:    make(chan struct{}),
	}

	// Start the consumer
	for i := 0; i < concurrency; i++ {
		consumer.wg.Add(1)
		go consumer.workerLoop(ctx, i)
	}

	consumer.mutex.Lock()
	consumer.isRunning = true
	consumer.mutex.Unlock()

	cm.consumers[queueName] = consumer

	cm.logger.WithFields(logrus.Fields{
		"queue":       queueName,
		"concurrency": concurrency,
	}).Info("Added consumer to manager")

	return nil
}

// RemoveConsumer removes and stops a consumer
func (cm *ConsumerManager) RemoveConsumer(queueName string) error {
	cm.mutex.Lock()
	defer cm.mutex.Unlock()

	consumer, exists := cm.consumers[queueName]
	if !exists {
		return fmt.Errorf("consumer for queue %s does not exist", queueName)
	}

	// Stop the consumer
	consumer.mutex.Lock()
	if consumer.isRunning {
		close(consumer.stopChan)
		consumer.isRunning = false
	}
	consumer.mutex.Unlock()

	// Wait for all workers to finish
	consumer.wg.Wait()

	// Remove from manager
	delete(cm.consumers, queueName)

	cm.logger.WithField("queue", queueName).Info("Removed consumer from manager")
	return nil
}

// StopAll stops all consumers
func (cm *ConsumerManager) StopAll() error {
	cm.mutex.Lock()
	defer cm.mutex.Unlock()

	for queueName, consumer := range cm.consumers {
		consumer.mutex.Lock()
		if consumer.isRunning {
			close(consumer.stopChan)
			consumer.isRunning = false
		}
		consumer.mutex.Unlock()

		// Wait for workers to finish
		consumer.wg.Wait()

		cm.logger.WithField("queue", queueName).Info("Stopped consumer")
	}

	// Clear all consumers
	cm.consumers = make(map[string]*Consumer)
	cm.logger.Info("Stopped all consumers")
	return nil
}

// GetConsumerStats returns statistics for all consumers
func (cm *ConsumerManager) GetConsumerStats() map[string]interface{} {
	cm.mutex.RLock()
	defer cm.mutex.RUnlock()

	stats := make(map[string]interface{})
	for queueName, consumer := range cm.consumers {
		consumer.mutex.RLock()
		stats[queueName] = map[string]interface{}{
			"queue":       queueName,
			"concurrency": consumer.concurrency,
			"is_running":  consumer.isRunning,
		}
		consumer.mutex.RUnlock()
	}

	return stats
}
