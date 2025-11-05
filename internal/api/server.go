package api

import (
	"context"
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/sirupsen/logrus"
	swaggerFiles "github.com/swaggo/files"
	ginSwagger "github.com/swaggo/gin-swagger"

	"github.com/prefeitura-rio/app-eai-agent-gateway/internal/config"
	"github.com/prefeitura-rio/app-eai-agent-gateway/internal/handlers"
	"github.com/prefeitura-rio/app-eai-agent-gateway/internal/middleware"
	"github.com/prefeitura-rio/app-eai-agent-gateway/internal/services"
)

// Server represents the HTTP server
type Server struct {
	config          *config.Config
	logger          *logrus.Logger
	router          *gin.Engine
	httpServer      *http.Server
	healthHandler   *handlers.HealthHandler
	messageHandler  *handlers.MessageHandler
	redisService    *services.RedisService
	rabbitMQService *services.RabbitMQService
	otelService     *services.OTelService // Optional OTel service
}

// NewServer creates a new HTTP server
func NewServer(cfg *config.Config, logger *logrus.Logger, otelService *services.OTelService) (*Server, error) {
	// Set Gin mode based on environment
	if cfg.Observability.LogLevel == "debug" {
		gin.SetMode(gin.DebugMode)
	} else {
		gin.SetMode(gin.ReleaseMode)
	}

	// Initialize Redis service
	redisService, err := services.NewRedisService(cfg, logger)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize Redis service: %w", err)
	}

	// Initialize RabbitMQ service
	rabbitMQService, err := services.NewRabbitMQService(cfg, logger)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize RabbitMQ service: %w", err)
	}

	// Initialize Google Agent Engine service (for sync history updates)
	var googleAgentService *services.GoogleAgentEngineService
	rateLimiter := services.NewRateLimiterService(cfg, logger, redisService)
	googleAgentService, err = services.NewGoogleAgentEngineService(cfg, logger, rateLimiter, redisService)
	if err != nil {
		logger.WithError(err).Warn("Failed to initialize Google Agent Engine service, sync history updates will not be available")
		googleAgentService = nil
	}

	server := &Server{
		config:          cfg,
		logger:          logger,
		router:          gin.New(),
		redisService:    redisService,
		rabbitMQService: rabbitMQService,
		otelService:     otelService,
		healthHandler: handlers.NewHealthHandler(
			cfg.Observability.HealthCheckTimeout,
			cfg.Observability.ReadinessCheckTimeout,
			logger,
		),
		messageHandler: handlers.NewMessageHandler(logger, cfg, redisService, rabbitMQService, googleAgentService, func() *middleware.TraceCorrelationPropagator {
			if otelService != nil {
				return middleware.NewTraceCorrelationPropagator(otelService)
			}
			return nil
		}()),
	}

	// Add Google Agent Engine to health checks if available
	if googleAgentService != nil {
		server.healthHandler.AddChecker("google_agent_engine", googleAgentService)
	}

	// Add services to health checks
	server.healthHandler.AddChecker("redis", redisService)
	server.healthHandler.AddChecker("rabbitmq", rabbitMQService)

	server.setupMiddleware()
	server.setupRoutes()
	server.setupHTTPServer()

	return server, nil
}

// setupMiddleware configures middleware
func (s *Server) setupMiddleware() {
	// Request ID middleware (must be first)
	s.router.Use(middleware.RequestID())

	// Recovery middleware
	s.router.Use(middleware.Recovery(s.logger))

	// Logging middleware
	s.router.Use(middleware.Logger(s.logger))

	// OpenTelemetry middleware (if OTel service is available)
	if s.otelService != nil {
		s.logger.Info("Adding OpenTelemetry middleware for HTTP request tracing")
		s.router.Use(middleware.OTelMiddleware(s.otelService))
	}

	// Security headers
	s.router.Use(middleware.SecurityHeaders())

	// CORS middleware
	if s.config.Security.CORSEnabled {
		corsConfig := middleware.CORSConfig{
			AllowedOrigins: s.config.GetCORSOrigins(),
			AllowedMethods: []string{"GET", "POST", "PUT", "DELETE", "OPTIONS", "HEAD"},
			AllowedHeaders: []string{
				"Origin", "Content-Type", "Content-Length", "Accept-Encoding",
				"Authorization", "X-Requested-With", "X-Request-ID",
			},
			AllowCredentials: false,
		}
		s.router.Use(middleware.CORS(corsConfig))
	}

	// Request size limit
	s.router.Use(middleware.RequestSizeLimit(s.config.Security.MaxRequestSize))
}

// setupRoutes configures all routes
func (s *Server) setupRoutes() {
	// Health check endpoints
	s.router.GET("/health", s.healthHandler.Health)
	s.router.GET("/ready", s.healthHandler.Ready)
	s.router.GET("/live", s.healthHandler.Live)

	// Metrics endpoint (if enabled)
	if s.config.Observability.MetricsEnabled {
		s.router.GET(s.config.Observability.MetricsPath, gin.WrapH(promhttp.Handler()))
	}

	// Swagger documentation endpoint
	s.router.GET("/docs/*any", ginSwagger.WrapHandler(swaggerFiles.Handler))

	// Debug endpoint to serve swagger.json directly
	s.router.Static("/swagger", "./docs")

	// Manual Swagger UI endpoint as fallback
	s.router.GET("/swagger-ui", func(c *gin.Context) {
		html := `<!DOCTYPE html>
<html>
<head>
    <title>API Documentation</title>
    <link rel="stylesheet" type="text/css" href="https://unpkg.com/swagger-ui-dist@3.25.0/swagger-ui.css" />
</head>
<body>
    <div id="swagger-ui"></div>
    <script src="https://unpkg.com/swagger-ui-dist@3.25.0/swagger-ui-bundle.js"></script>
    <script>
    SwaggerUIBundle({
        url: '/swagger/swagger.json',
        dom_id: '#swagger-ui',
        presets: [
            SwaggerUIBundle.presets.apis,
            SwaggerUIBundle.presets.standalone
        ]
    });
    </script>
</body>
</html>`
		c.Header("Content-Type", "text/html")
		c.String(200, html)
	})

	// API routes group
	api := s.router.Group("/api")
	{
		v1 := api.Group("/v1")
		{
			// Message endpoints
			message := v1.Group("/message")
			{
				message.POST("/webhook/user", s.messageHandler.HandleUserWebhook)
				message.POST("/webhook/update_history", s.messageHandler.HandleHistoryUpdateWebhook)
				message.GET("/response", s.messageHandler.HandleMessageResponse)
				message.GET("/debug/task-status", s.messageHandler.HandleDebugTaskStatus)
			}

			// Note: Agent management endpoints removed - were Letta-specific
			// Google Agent Engine handles agent lifecycle automatically
		}
	}

	// Catch-all for undefined routes
	s.router.NoRoute(func(c *gin.Context) {
		c.JSON(http.StatusNotFound, gin.H{
			"error":   "Not Found",
			"message": "The requested endpoint does not exist",
			"path":    c.Request.URL.Path,
		})
	})
}

// setupHTTPServer configures the HTTP server
func (s *Server) setupHTTPServer() {
	s.httpServer = &http.Server{
		Addr:         fmt.Sprintf("%s:%d", s.config.Server.Host, s.config.Server.Port),
		Handler:      s.router,
		ReadTimeout:  s.config.Server.ReadTimeout,
		WriteTimeout: s.config.Server.WriteTimeout,
		IdleTimeout:  s.config.Server.IdleTimeout,
	}
}

// AddHealthChecker adds a health checker to the health handler
func (s *Server) AddHealthChecker(name string, checker handlers.HealthChecker) {
	s.healthHandler.AddChecker(name, checker)
}

// Start starts the HTTP server
func (s *Server) Start() error {
	s.logger.WithFields(logrus.Fields{
		"address":       s.httpServer.Addr,
		"read_timeout":  s.config.Server.ReadTimeout,
		"write_timeout": s.config.Server.WriteTimeout,
		"idle_timeout":  s.config.Server.IdleTimeout,
	}).Info("Starting HTTP server")

	if err := s.httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("failed to start server: %w", err)
	}

	return nil
}

// Stop gracefully stops the HTTP server
func (s *Server) Stop(ctx context.Context) error {
	s.logger.Info("Shutting down HTTP server")

	// Close RabbitMQ connection
	if s.rabbitMQService != nil {
		if err := s.rabbitMQService.Close(); err != nil {
			s.logger.WithError(err).Error("Failed to close RabbitMQ connection during shutdown")
		}
	}

	// Close Redis connection
	if s.redisService != nil {
		if err := s.redisService.Close(); err != nil {
			s.logger.WithError(err).Error("Failed to close Redis connection during shutdown")
		}
	}

	return s.httpServer.Shutdown(ctx)
}
