package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/prefeitura-rio/app-eai-agent-gateway/internal/config"
	workerhandlers "github.com/prefeitura-rio/app-eai-agent-gateway/internal/handlers/workers"
	"github.com/prefeitura-rio/app-eai-agent-gateway/internal/middleware"
	"github.com/prefeitura-rio/app-eai-agent-gateway/internal/services"
)

// metricsNamespace is the Prometheus namespace of the worker's metrics, the
// same as the gateway's.
const metricsNamespace = "eai_gateway"

// queueDepthInterval is how often the depth of the consumed queue and of its
// dead letter queue is read for the queue_depth series.
const queueDepthInterval = 15 * time.Second

func main() {
	// Load configuration
	cfg, err := config.Load()
	if err != nil {
		logrus.WithError(err).Fatal("Failed to load configuration")
	}

	// Setup logger
	log := logrus.New()

	// Set log level using the existing method
	log.SetLevel(cfg.GetLogLevel())

	// Set log format
	if cfg.Observability.LogFormat == "json" {
		log.SetFormatter(&logrus.JSONFormatter{})
	} else {
		log.SetFormatter(&logrus.TextFormatter{})
	}

	log.WithFields(logrus.Fields{
		"service":     cfg.Observability.OTelServiceName + "-worker",
		"version":     cfg.Observability.OTelServiceVersion,
		"environment": cfg.Observability.OTelEnvironment,
	}).Info("Starting EAí Agent Gateway Worker")

	// Initialize OpenTelemetry service if enabled and collector URL is set
	var otelService *services.OTelService
	var otelWorkerWrapper *middleware.OTelWorkerWrapper
	if cfg.Observability.OTelEnabled && cfg.Observability.OTelCollectorURL != "" {
		log.Info("Initializing OpenTelemetry service for worker")
		otelConfig := services.OTelConfig{
			ServiceName:    cfg.Observability.OTelServiceName + "-worker",
			ServiceVersion: cfg.Observability.OTelServiceVersion,
			Environment:    cfg.Observability.OTelEnvironment,
			OTLPEndpoint:   cfg.Observability.OTelCollectorURL,
			Insecure:       true, // Use insecure connection for local development
			Headers:        make(map[string]string),
		}

		var err error
		otelService, err = services.NewOTelService(context.Background(), otelConfig)
		if err != nil {
			log.WithError(err).Error("Failed to initialize OpenTelemetry service, continuing without tracing")
			otelService = nil
		} else {
			log.WithField("collector_url", cfg.Observability.OTelCollectorURL).Info("OpenTelemetry service initialized successfully for worker")
			otelWorkerWrapper = middleware.NewOTelWorkerWrapper(otelService)
		}
	} else {
		log.Info("OpenTelemetry disabled or collector URL not set for worker, continuing without tracing")
	}

	// Initialize Prometheus metrics if enabled (worker_* and queue_* series,
	// served on the metrics path of the health server)
	var metricsService *services.PrometheusMetricsService
	var workerMetrics *middleware.WorkerMetricsWrapper
	var queueMetrics *middleware.QueueMetricsWrapper
	if cfg.Observability.MetricsEnabled {
		metricsService, err = services.NewPrometheusMetricsService(services.PrometheusConfig{Namespace: metricsNamespace})
		if err != nil {
			log.WithError(err).Fatal("Failed to initialize Prometheus metrics service")
		}
		workerMetrics = middleware.NewWorkerMetricsWrapper(metricsService)
		queueMetrics = middleware.NewQueueMetricsWrapper(metricsService)
		log.WithField("path", cfg.Observability.MetricsPath).Info("Prometheus metrics enabled for worker")
	} else {
		log.Info("Prometheus metrics disabled for worker")
	}

	// Initialize Redis service
	redisService, err := services.NewRedisService(cfg, log)
	if err != nil {
		log.WithError(err).Fatal("Failed to initialize Redis service")
	}

	// Initialize RabbitMQ service
	rabbitMQService, err := services.NewRabbitMQService(cfg, log)
	if err != nil {
		log.WithError(err).Fatal("Failed to initialize RabbitMQ service")
	}

	// Initialize consumer manager for workers
	consumerManager := services.NewConsumerManager(log)

	// Initialize rate limiter service
	rateLimiterService := services.NewRateLimiterService(cfg, log, redisService)

	// Initialize Google Agent Engine service (required)
	googleAgentService, err := services.NewGoogleAgentEngineService(cfg, log, rateLimiterService, redisService)
	if err != nil {
		log.WithError(err).Fatal("Failed to initialize Google Agent Engine service")
	}

	// Initialize transcribe service (optional for development)
	var transcribeService *services.TranscribeService
	transcribeService, err = services.NewTranscribeService(cfg, log, rateLimiterService)
	if err != nil {
		log.WithError(err).Warn("Failed to initialize transcribe service, audio transcription will be disabled")
		transcribeService = nil
	}

	// Initialize message formatter service
	messageFormatterService := services.NewMessageFormatterService(cfg, log)

	// Initialize Data Relay service if enabled
	var dataRelayService *services.DataRelayService
	if cfg.DataRelay.Enabled {
		dataRelayService = services.NewDataRelayService(log, &cfg.DataRelay)
		log.WithFields(logrus.Fields{
			"base_url": cfg.DataRelay.BaseURL,
			"source":   cfg.DataRelay.Source,
			"flowname": cfg.DataRelay.FlowName,
		}).Info("Data Relay error interceptor enabled")
	} else {
		log.Info("Data Relay error interceptor disabled by configuration")
	}

	// Initialize callback service if enabled
	var callbackService *services.CallbackService
	if cfg.Callback.Enabled {
		if otelService != nil {
			callbackService = services.NewCallbackService(log, cfg, otelService.GetTracer(), dataRelayService)
			log.Info("Callback service initialized with tracing")
		} else {
			callbackService = services.NewCallbackService(log, cfg, nil, dataRelayService)
			log.Info("Callback service initialized without tracing")
		}
	} else {
		log.Info("Callback service disabled by configuration")
	}

	// Create transcribe service adapter (always create adapter, but with potentially nil service)
	transcribeAdapter := workerhandlers.NewTranscribeServiceAdapter(transcribeService)

	// Create message handler dependencies
	ctx := context.Background()
	handlerDeps := &workerhandlers.MessageHandlerDependencies{
		Logger:             log,
		Config:             cfg,
		RedisService:       redisService,
		GoogleAgentService: googleAgentService,
		TranscribeService:  transcribeAdapter,
		MessageFormatter:   messageFormatterService,
		CallbackService:    callbackService,   // Optional callback service
		OTelWorkerWrapper:  otelWorkerWrapper, // Optional OTel wrapper
		TracePropagator: func() *middleware.TraceCorrelationPropagator {
			if otelService != nil {
				return middleware.NewTraceCorrelationPropagator(otelService)
			}
			return nil
		}(), // Optional trace propagator for distributed tracing
		WorkerMetrics: workerMetrics, // Optional Prometheus worker metrics
	}

	// Create message handler, counted and timed per delivery when metrics are enabled
	queueName := cfg.RabbitMQ.UserMessagesQueue
	var userMessageHandler services.MessageHandler = workerhandlers.CreateUserMessageHandler(handlerDeps)
	if queueMetrics != nil {
		userMessageHandler = queueMetrics.WrapMessageHandler(queueName, "main", userMessageHandler)
	}

	// Add user message consumer with configurable concurrency
	concurrency := cfg.RabbitMQ.MaxParallel
	if concurrency <= 0 {
		concurrency = 5 // Fallback default
		log.Warn("MAX_PARALLEL not set or invalid, using default concurrency of 5")
	} else if concurrency > 100 {
		log.WithField("requested", concurrency).Warn("MAX_PARALLEL is very high, consider if this is intentional")
	}

	log.WithField("concurrency", concurrency).Info("Setting up user message consumer")
	if err := consumerManager.AddConsumer(ctx, rabbitMQService, queueName, concurrency, userMessageHandler); err != nil {
		log.WithError(err).Fatal("Failed to add user message consumer")
	}

	// Collect the depth of the consumed queue and of its dead letter queue
	metricsCtx, stopMetrics := context.WithCancel(context.Background())
	defer stopMetrics()
	if queueMetrics != nil {
		targets := []middleware.QueueDepthTarget{
			{Name: queueName, Type: "main"},
			{Name: queueName + "_dlq", Type: "dlq"},
		}
		go queueMetrics.CollectQueueDepth(metricsCtx, rabbitMQService, queueDepthInterval, targets, func(target middleware.QueueDepthTarget, err error) {
			log.WithError(err).WithField("queue", target.Name).Debug("Failed to read queue depth")
		})
	}

	log.Info("Worker started successfully - consuming messages from RabbitMQ")

	// Set up health check HTTP server
	healthHandler := workerhandlers.NewWorkerHealthHandler(rabbitMQService, redisService, log)

	// Create HTTP mux for health endpoints and, when enabled, metrics
	mux := workerhandlers.NewHealthMux(healthHandler, metricsService, cfg.Observability.MetricsPath)

	// Get health check port from config or use default
	healthPort := cfg.Server.HealthPort
	if healthPort == 0 {
		healthPort = 8081 // Default health check port for workers
	}

	healthServer := &http.Server{
		Addr:         fmt.Sprintf(":%d", healthPort),
		Handler:      mux,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	// Start health check server in background
	go func() {
		log.WithField("port", healthPort).Info("Starting worker health check server")
		if err := healthServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.WithError(err).Error("Health check server failed")
		}
	}()

	// Wait for interrupt signal to gracefully shutdown
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Info("Shutting down worker...")

	// Stop collecting queue depth
	stopMetrics()

	// Give the worker 30 seconds to gracefully shutdown
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Shutdown health check server
	log.Info("Shutting down health check server")
	if err := healthServer.Shutdown(ctx); err != nil {
		log.WithError(err).Error("Failed to shutdown health check server")
	}

	// Shutdown OpenTelemetry service first if initialized
	if otelService != nil {
		log.Info("Shutting down OpenTelemetry service for worker")
		if err := otelService.Shutdown(ctx); err != nil {
			log.WithError(err).Error("Failed to shutdown OpenTelemetry service in worker")
		}
	}

	// Stop all consumers
	if err := consumerManager.StopAll(); err != nil {
		log.WithError(err).Error("Failed to stop consumers during shutdown")
	}

	// Close Google Agent Engine service
	if err := googleAgentService.Close(); err != nil {
		log.WithError(err).Error("Failed to close Google Agent Engine service during shutdown")
	}

	// Close transcribe service
	if transcribeService != nil {
		if err := transcribeService.Close(); err != nil {
			log.WithError(err).Error("Failed to close transcribe service during shutdown")
		}
	}

	// Close RabbitMQ connection
	if err := rabbitMQService.Close(); err != nil {
		log.WithError(err).Error("Failed to close RabbitMQ connection during shutdown")
	}

	// Close Redis connection
	if err := redisService.Close(); err != nil {
		log.WithError(err).Error("Failed to close Redis connection during shutdown")
	}

	select {
	case <-ctx.Done():
		log.Error("Worker forced to shutdown")
		os.Exit(1)
	default:
		log.Info("Worker shutdown complete")
	}
}
