package main

import (
	"context"
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
		OTelWorkerWrapper:  otelWorkerWrapper, // Optional OTel wrapper
	}

	// Create message handler
	userMessageHandler := workerhandlers.CreateUserMessageHandler(handlerDeps)

	// Add user message consumer with configurable concurrency
	concurrency := cfg.RabbitMQ.MaxParallel
	if concurrency <= 0 {
		concurrency = 5 // Fallback default
		log.Warn("MAX_PARALLEL not set or invalid, using default concurrency of 5")
	} else if concurrency > 100 {
		log.WithField("requested", concurrency).Warn("MAX_PARALLEL is very high, consider if this is intentional")
	}
	
	log.WithField("concurrency", concurrency).Info("Setting up user message consumer")
	if err := consumerManager.AddConsumer(ctx, rabbitMQService, cfg.RabbitMQ.UserMessagesQueue, concurrency, userMessageHandler); err != nil {
		log.WithError(err).Fatal("Failed to add user message consumer")
	}

	log.Info("Worker started successfully - consuming messages from RabbitMQ")

	// Wait for interrupt signal to gracefully shutdown
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Info("Shutting down worker...")

	// Give the worker 30 seconds to gracefully shutdown
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

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
