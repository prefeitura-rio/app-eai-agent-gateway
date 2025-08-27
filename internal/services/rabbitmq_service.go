package services

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/sirupsen/logrus"

	"github.com/prefeitura-rio/app-eai-agent-gateway/internal/config"
)

// RabbitMQService handles RabbitMQ operations with connection management
type RabbitMQService struct {
	config      *config.Config
	logger      *logrus.Logger
	connection  *amqp.Connection
	channel     *amqp.Channel
	mutex       sync.RWMutex
	isConnected bool

	// Connection monitoring
	notifyConnClose chan *amqp.Error
	notifyChanClose chan *amqp.Error
	notifyReconnect chan bool
	isShutdown      bool
}

// MessagePublisher defines the interface for publishing messages
type MessagePublisher interface {
	PublishMessage(ctx context.Context, queueName string, message interface{}) error
	PublishMessageWithDelay(ctx context.Context, queueName string, message interface{}, delay time.Duration) error
	PublishPriorityMessage(ctx context.Context, queueName string, message interface{}, priority uint8) error
}

// MessageConsumer defines the interface for consuming messages
type MessageConsumer interface {
	ConsumeMessages(ctx context.Context, queueName string, handler MessageHandler) error
	StartConsumer(ctx context.Context, queueName string, concurrency int, handler MessageHandler) error
	StopConsumer(queueName string) error
}

// MessageHandler is a function type for handling consumed messages
type MessageHandler func(ctx context.Context, delivery amqp.Delivery) error

// QueueManager defines the interface for queue management operations
type QueueManager interface {
	DeclareQueue(queueName string, durable, autoDelete bool) error
	DeclareExchange(exchangeName, exchangeType string, durable bool) error
	BindQueue(queueName, exchangeName, routingKey string) error
	PurgeQueue(queueName string) (int, error)
	GetQueueInfo(queueName string) (amqp.Queue, error)
}

// NewRabbitMQService creates a new RabbitMQ service with connection management
func NewRabbitMQService(cfg *config.Config, logger *logrus.Logger) (*RabbitMQService, error) {
	service := &RabbitMQService{
		config:          cfg,
		logger:          logger,
		notifyReconnect: make(chan bool),
	}

	if err := service.connect(); err != nil {
		return nil, fmt.Errorf("failed to establish initial RabbitMQ connection: %w", err)
	}

	// Setup auto-reconnection
	go service.handleReconnect()

	logger.WithField("url", cfg.RabbitMQ.URL).Info("RabbitMQ service initialized successfully")
	return service, nil
}

// connect establishes connection to RabbitMQ and sets up exchanges and queues
func (r *RabbitMQService) connect() error {
	r.mutex.Lock()
	defer r.mutex.Unlock()

	// Connect to RabbitMQ
	conn, err := amqp.Dial(r.config.RabbitMQ.URL)
	if err != nil {
		return fmt.Errorf("failed to connect to RabbitMQ: %w", err)
	}

	// Create channel
	ch, err := conn.Channel()
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("failed to open channel: %w", err)
	}

	// Set QoS for fair dispatch
	if err := ch.Qos(1, 0, false); err != nil {
		_ = ch.Close()
		_ = conn.Close()
		return fmt.Errorf("failed to set QoS: %w", err)
	}

	r.connection = conn
	r.channel = ch
	r.isConnected = true

	// Setup connection monitoring
	r.notifyConnClose = make(chan *amqp.Error)
	r.notifyChanClose = make(chan *amqp.Error)
	r.connection.NotifyClose(r.notifyConnClose)
	r.channel.NotifyClose(r.notifyChanClose)

	// Setup exchanges and queues
	if err := r.setupTopology(); err != nil {
		return fmt.Errorf("failed to setup RabbitMQ topology: %w", err)
	}

	r.logger.Info("RabbitMQ connection established successfully")
	return nil
}

// setupTopology declares exchanges, queues, and bindings based on configuration
func (r *RabbitMQService) setupTopology() error {
	// Declare main exchange
	if err := r.channel.ExchangeDeclare(
		r.config.RabbitMQ.Exchange, // name
		"direct",                   // type
		true,                       // durable
		false,                      // auto-deleted
		false,                      // internal
		false,                      // no-wait
		nil,                        // arguments
	); err != nil {
		return fmt.Errorf("failed to declare exchange %s: %w", r.config.RabbitMQ.Exchange, err)
	}

	// Declare dead letter exchange
	if err := r.channel.ExchangeDeclare(
		r.config.RabbitMQ.DLXExchange, // name
		"direct",                      // type
		true,                          // durable
		false,                         // auto-deleted
		false,                         // internal
		false,                         // no-wait
		nil,                           // arguments
	); err != nil {
		return fmt.Errorf("failed to declare DLX exchange %s: %w", r.config.RabbitMQ.DLXExchange, err)
	}

	// Declare user messages queue
	if err := r.declareQueueWithDLX(r.config.RabbitMQ.UserQueue); err != nil {
		return fmt.Errorf("failed to declare user queue: %w", err)
	}

	// Declare user messages queue
	if err := r.declareQueueWithDLX(r.config.RabbitMQ.UserMessagesQueue); err != nil {
		return fmt.Errorf("failed to declare user messages queue: %w", err)
	}

	// Declare dead letter queues
	queues := []string{
		r.config.RabbitMQ.UserQueue,
		r.config.RabbitMQ.UserMessagesQueue,
	}

	for _, queue := range queues {
		if err := r.declareDLQ(queue + "_dlq"); err != nil {
			return fmt.Errorf("failed to declare DLQ for %s: %w", queue, err)
		}
	}

	r.logger.Info("RabbitMQ topology setup completed")
	return nil
}

// declareQueueWithDLX declares a queue with dead letter exchange configuration
func (r *RabbitMQService) declareQueueWithDLX(queueName string) error {
	args := amqp.Table{
		"x-dead-letter-exchange":    r.config.RabbitMQ.DLXExchange,
		"x-dead-letter-routing-key": queueName + "_dlq",
		"x-message-ttl":             300000, // 5 minutes TTL
	}

	_, err := r.channel.QueueDeclare(
		queueName, // name
		true,      // durable
		false,     // delete when unused
		false,     // exclusive
		false,     // no-wait
		args,      // arguments
	)
	if err != nil {
		return err
	}

	// Bind queue to exchange
	return r.channel.QueueBind(
		queueName,                  // queue name
		queueName,                  // routing key (same as queue name)
		r.config.RabbitMQ.Exchange, // exchange
		false,                      // no-wait
		nil,                        // arguments
	)
}

// declareDLQ declares a dead letter queue
func (r *RabbitMQService) declareDLQ(queueName string) error {
	_, err := r.channel.QueueDeclare(
		queueName, // name
		true,      // durable
		false,     // delete when unused
		false,     // exclusive
		false,     // no-wait
		nil,       // arguments
	)
	if err != nil {
		return err
	}

	// Bind DLQ to DLX exchange
	return r.channel.QueueBind(
		queueName,                     // queue name
		queueName,                     // routing key
		r.config.RabbitMQ.DLXExchange, // exchange
		false,                         // no-wait
		nil,                           // arguments
	)
}

// PublishMessage publishes a message to the specified queue
func (r *RabbitMQService) PublishMessage(ctx context.Context, queueName string, message interface{}) error {
	r.mutex.RLock()
	defer r.mutex.RUnlock()

	if !r.isConnected {
		return fmt.Errorf("RabbitMQ connection is not available")
	}

	// Serialize message to JSON
	body, err := json.Marshal(message)
	if err != nil {
		return fmt.Errorf("failed to marshal message: %w", err)
	}

	// Publish message
	err = r.channel.PublishWithContext(
		ctx,
		r.config.RabbitMQ.Exchange, // exchange
		queueName,                  // routing key
		false,                      // mandatory
		false,                      // immediate
		amqp.Publishing{
			ContentType:  "application/json",
			Body:         body,
			DeliveryMode: amqp.Persistent, // Make message persistent
			Timestamp:    time.Now(),
			MessageId:    fmt.Sprintf("%d", time.Now().UnixNano()),
		},
	)

	if err != nil {
		r.logger.WithError(err).WithFields(logrus.Fields{
			"queue":   queueName,
			"message": string(body),
		}).Error("Failed to publish message")
		return fmt.Errorf("failed to publish message: %w", err)
	}

	r.logger.WithFields(logrus.Fields{
		"queue":      queueName,
		"message_id": fmt.Sprintf("%d", time.Now().UnixNano()),
	}).Debug("Message published successfully")

	return nil
}

// PublishMessageWithHeaders publishes a message with custom headers (for trace context)
func (r *RabbitMQService) PublishMessageWithHeaders(ctx context.Context, queueName string, message interface{}, headers map[string]interface{}) error {
	r.mutex.RLock()
	defer r.mutex.RUnlock()

	if !r.isConnected {
		return fmt.Errorf("RabbitMQ connection is not available")
	}

	// Serialize message to JSON
	body, err := json.Marshal(message)
	if err != nil {
		return fmt.Errorf("failed to marshal message: %w", err)
	}

	// Convert headers to AMQP Table
	amqpHeaders := amqp.Table{}
	for k, v := range headers {
		amqpHeaders[k] = v
	}

	// Publish message with headers
	err = r.channel.PublishWithContext(
		ctx,
		r.config.RabbitMQ.Exchange, // exchange
		queueName,                  // routing key
		false,                      // mandatory
		false,                      // immediate
		amqp.Publishing{
			ContentType:  "application/json",
			Body:         body,
			DeliveryMode: amqp.Persistent, // Make message persistent
			Timestamp:    time.Now(),
			MessageId:    fmt.Sprintf("%d", time.Now().UnixNano()),
			Headers:      amqpHeaders, // Include trace headers
		},
	)

	if err != nil {
		r.logger.WithError(err).WithFields(logrus.Fields{
			"queue":   queueName,
			"message": string(body),
			"headers": headers,
		}).Error("Failed to publish message with headers")
		return fmt.Errorf("failed to publish message with headers: %w", err)
	}

	r.logger.WithFields(logrus.Fields{
		"queue":      queueName,
		"message_id": fmt.Sprintf("%d", time.Now().UnixNano()),
		"headers":    headers,
	}).Debug("Message with headers published successfully")

	return nil
}

// PublishMessageWithDelay publishes a message with a delay using RabbitMQ delayed message plugin
func (r *RabbitMQService) PublishMessageWithDelay(ctx context.Context, queueName string, message interface{}, delay time.Duration) error {
	r.mutex.RLock()
	defer r.mutex.RUnlock()

	if !r.isConnected {
		return fmt.Errorf("RabbitMQ connection is not available")
	}

	// Serialize message to JSON
	body, err := json.Marshal(message)
	if err != nil {
		return fmt.Errorf("failed to marshal message: %w", err)
	}

	// Use headers for delay information
	headers := amqp.Table{
		"x-delay": int64(delay.Milliseconds()),
	}

	// Publish delayed message
	err = r.channel.PublishWithContext(
		ctx,
		r.config.RabbitMQ.Exchange, // exchange
		queueName,                  // routing key
		false,                      // mandatory
		false,                      // immediate
		amqp.Publishing{
			ContentType:  "application/json",
			Body:         body,
			DeliveryMode: amqp.Persistent,
			Timestamp:    time.Now(),
			MessageId:    fmt.Sprintf("%d", time.Now().UnixNano()),
			Headers:      headers,
		},
	)

	if err != nil {
		return fmt.Errorf("failed to publish delayed message: %w", err)
	}

	return nil
}

// PublishPriorityMessage publishes a message with priority
func (r *RabbitMQService) PublishPriorityMessage(ctx context.Context, queueName string, message interface{}, priority uint8) error {
	r.mutex.RLock()
	defer r.mutex.RUnlock()

	if !r.isConnected {
		return fmt.Errorf("RabbitMQ connection is not available")
	}

	// Serialize message to JSON
	body, err := json.Marshal(message)
	if err != nil {
		return fmt.Errorf("failed to marshal message: %w", err)
	}

	// Publish message with priority
	err = r.channel.PublishWithContext(
		ctx,
		r.config.RabbitMQ.Exchange, // exchange
		queueName,                  // routing key
		false,                      // mandatory
		false,                      // immediate
		amqp.Publishing{
			ContentType:  "application/json",
			Body:         body,
			DeliveryMode: amqp.Persistent,
			Priority:     priority,
			Timestamp:    time.Now(),
			MessageId:    fmt.Sprintf("%d", time.Now().UnixNano()),
		},
	)

	if err != nil {
		return fmt.Errorf("failed to publish priority message: %w", err)
	}

	return nil
}

// handleReconnect monitors connection and handles automatic reconnection
func (r *RabbitMQService) handleReconnect() {
	for {
		select {
		case err := <-r.notifyConnClose:
			if r.isShutdown {
				return
			}
			r.logger.WithError(err).Error("RabbitMQ connection lost, attempting to reconnect")
			r.isConnected = false
			r.reconnect()

		case err := <-r.notifyChanClose:
			if r.isShutdown {
				return
			}
			r.logger.WithError(err).Error("RabbitMQ channel lost, attempting to reconnect")
			r.isConnected = false
			r.reconnect()

		case <-r.notifyReconnect:
			if r.isShutdown {
				return
			}
			r.logger.Info("Manual reconnection requested")
			r.reconnect()
		}
	}
}

// reconnect attempts to reconnect to RabbitMQ with exponential backoff
func (r *RabbitMQService) reconnect() {
	retryCount := 0
	maxRetries := r.config.RabbitMQ.MaxRetries

	for retryCount < maxRetries {
		if r.isShutdown {
			return
		}

		// Exponential backoff
		delay := time.Duration(retryCount*retryCount+1) * time.Second
		r.logger.WithFields(logrus.Fields{
			"retry_count": retryCount + 1,
			"max_retries": maxRetries,
			"delay":       delay,
		}).Info("Attempting to reconnect to RabbitMQ")

		time.Sleep(delay)

		if err := r.connect(); err != nil {
			r.logger.WithError(err).WithField("retry_count", retryCount+1).Error("Failed to reconnect to RabbitMQ")
			retryCount++
			continue
		}

		r.logger.Info("Successfully reconnected to RabbitMQ")
		return
	}

	r.logger.WithField("max_retries", maxRetries).Error("Failed to reconnect to RabbitMQ after maximum retries")
}

// HealthCheck implements the HealthChecker interface
func (r *RabbitMQService) HealthCheck(ctx context.Context) error {
	r.mutex.RLock()
	defer r.mutex.RUnlock()

	if !r.isConnected || r.connection == nil || r.connection.IsClosed() {
		return fmt.Errorf("RabbitMQ connection is not available")
	}

	// Try to declare a temporary queue to test connection
	tempQueue := fmt.Sprintf("health_check_%d", time.Now().UnixNano())
	_, err := r.channel.QueueDeclare(
		tempQueue, // name
		false,     // durable
		true,      // delete when unused
		true,      // exclusive
		false,     // no-wait
		nil,       // arguments
	)

	if err != nil {
		return fmt.Errorf("RabbitMQ health check failed: %w", err)
	}

	// Clean up the temp queue
	_, err = r.channel.QueueDelete(tempQueue, false, false, false)
	if err != nil {
		r.logger.WithError(err).Warn("Failed to clean up health check queue")
	}

	return nil
}

// GetQueueInfo returns information about a specific queue
func (r *RabbitMQService) GetQueueInfo(queueName string) (amqp.Queue, error) {
	r.mutex.RLock()
	defer r.mutex.RUnlock()

	if !r.isConnected {
		return amqp.Queue{}, fmt.Errorf("RabbitMQ connection is not available")
	}

	return r.channel.QueueDeclarePassive(queueName, true, false, false, false, nil)
}

// Close gracefully closes the RabbitMQ connection
func (r *RabbitMQService) Close() error {
	r.mutex.Lock()
	defer r.mutex.Unlock()

	r.isShutdown = true
	r.isConnected = false

	if r.channel != nil {
		if err := r.channel.Close(); err != nil {
			r.logger.WithError(err).Error("Failed to close RabbitMQ channel")
		}
	}

	if r.connection != nil {
		if err := r.connection.Close(); err != nil {
			r.logger.WithError(err).Error("Failed to close RabbitMQ connection")
			return fmt.Errorf("failed to close RabbitMQ connection: %w", err)
		}
	}

	r.logger.Info("RabbitMQ connection closed")
	return nil
}

// IsConnected returns the current connection status
func (r *RabbitMQService) IsConnected() bool {
	r.mutex.RLock()
	defer r.mutex.RUnlock()
	return r.isConnected
}

// TriggerReconnect manually triggers a reconnection attempt
func (r *RabbitMQService) TriggerReconnect() {
	select {
	case r.notifyReconnect <- true:
	default:
		// Channel is full, reconnection already in progress
	}
}
