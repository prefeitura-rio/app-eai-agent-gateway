// Package main provides the entry point for the EAí Agent Gateway
//
//	@title						EAí Agent Gateway API
//	@version					0.1.0
//	@description				Gateway service for AI agent interactions with WhatsApp integration
//	@termsOfService				http://swagger.io/terms/
//
//	@contact.name				EAí Team
//	@contact.url				https://github.com/prefeitura-rio/app-eai-agent-gateway
//	@contact.email				example@example.com
//
//	@license.name				MIT
//	@license.url				https://opensource.org/licenses/MIT
//
//	@host						localhost:8000
//	@BasePath					/
//
//	@schemes					http https
//	@produce					json
//	@consumes					json
//
//	@tag.name					Health
//	@tag.description			Health check endpoints for monitoring
//
//	@tag.name					Messages
//	@tag.description			Message processing endpoints for webhooks and responses
//
//	@tag.name					Debug
//	@tag.description			Debug and troubleshooting endpoints
//
//	@securityDefinitions.basic	BasicAuth
//
//	@x-extension-openapi		{"example": "value on a json format"}
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/sirupsen/logrus"

	_ "github.com/prefeitura-rio/app-eai-agent-gateway/docs" // Import swagger docs
	"github.com/prefeitura-rio/app-eai-agent-gateway/internal/api"
	"github.com/prefeitura-rio/app-eai-agent-gateway/internal/config"
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
		"service":     cfg.Observability.OTelServiceName,
		"version":     cfg.Observability.OTelServiceVersion,
		"environment": cfg.Observability.OTelEnvironment,
	}).Info("Starting EAí Agent Gateway")

	// Initialize OpenTelemetry service if enabled and collector URL is set
	var otelService *services.OTelService
	if cfg.Observability.OTelEnabled && cfg.Observability.OTelCollectorURL != "" {
		log.Info("Initializing OpenTelemetry service")
		otelConfig := services.OTelConfig{
			ServiceName:    cfg.Observability.OTelServiceName,
			ServiceVersion: cfg.Observability.OTelServiceVersion,
			Environment:    cfg.Observability.OTelEnvironment,
			OTLPEndpoint:   cfg.Observability.OTelCollectorURL,
			Insecure:       true, // Use insecure connection for local development
			Headers:        make(map[string]string),
		}

		otelService, err = services.NewOTelService(context.Background(), otelConfig)
		if err != nil {
			log.WithError(err).Error("Failed to initialize OpenTelemetry service, continuing without tracing")
			otelService = nil
		} else {
			log.WithField("collector_url", cfg.Observability.OTelCollectorURL).Info("OpenTelemetry service initialized successfully")
		}
	} else {
		log.Info("OpenTelemetry disabled or collector URL not set, continuing without tracing")
	}

	// Create HTTP server with Redis service and optional OTel service
	server, err := api.NewServer(cfg, log, otelService)
	if err != nil {
		log.WithError(err).Fatal("Failed to create server")
	}

	// Start server in a goroutine
	go func() {
		if err := server.Start(); err != nil {
			log.WithError(err).Fatal("Failed to start server")
		}
	}()

	log.WithField("address", fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port)).Info("Server started successfully")

	// Wait for interrupt signal to gracefully shutdown
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Info("Shutting down server...")

	// Give the server 30 seconds to gracefully shutdown
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Shutdown OpenTelemetry service first if initialized
	if otelService != nil {
		log.Info("Shutting down OpenTelemetry service")
		if err := otelService.Shutdown(ctx); err != nil {
			log.WithError(err).Error("Failed to shutdown OpenTelemetry service")
		}
	}

	if err := server.Stop(ctx); err != nil {
		log.WithError(err).Error("Server forced to shutdown")
		os.Exit(1)
	}

	log.Info("Server shutdown complete")
}
