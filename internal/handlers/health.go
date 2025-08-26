package handlers

import (
	"context"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"

	"github.com/prefeitura-rio/app-eai-agent-gateway/internal/services"
)

// HealthChecker interface for health check dependencies (legacy interface)
type HealthChecker interface {
	HealthCheck(ctx context.Context) error
}

// HealthHandler handles health and readiness checks
type HealthHandler struct {
	// Legacy fields for backward compatibility
	checkers         map[string]HealthChecker
	healthTimeout    time.Duration
	readinessTimeout time.Duration
	logger           *logrus.Logger

	// New comprehensive health service
	healthService *services.HealthService
}

// NewHealthHandler creates a new health handler
func NewHealthHandler(healthTimeout, readinessTimeout time.Duration, logger *logrus.Logger) *HealthHandler {
	return &HealthHandler{
		checkers:         make(map[string]HealthChecker),
		healthTimeout:    healthTimeout,
		readinessTimeout: readinessTimeout,
		logger:           logger,
	}
}

// NewEnhancedHealthHandler creates a new health handler with comprehensive health service
func NewEnhancedHealthHandler(healthService *services.HealthService, logger *logrus.Logger) *HealthHandler {
	return &HealthHandler{
		checkers:         make(map[string]HealthChecker),
		healthTimeout:    10 * time.Second,
		readinessTimeout: 5 * time.Second,
		logger:           logger,
		healthService:    healthService,
	}
}

// AddChecker adds a health checker (legacy method)
func (h *HealthHandler) AddChecker(name string, checker HealthChecker) {
	h.checkers[name] = checker
}

// SetHealthService sets the comprehensive health service
func (h *HealthHandler) SetHealthService(healthService *services.HealthService) {
	h.healthService = healthService
}

// Health handles the /health endpoint
//
//	@Summary		Health check
//	@Description	Returns the health status of the service and its dependencies
//	@Tags			Health
//	@Accept			json
//	@Produce		json
//	@Success		200	{object}	map[string]interface{}	"Service is healthy"
//	@Success		503	{object}	map[string]interface{}	"Service is unhealthy"
//	@Router			/health [get]
func (h *HealthHandler) Health(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), h.healthTimeout)
	defer cancel()

	// Use comprehensive health service if available
	if h.healthService != nil {
		systemHealth := h.healthService.CheckHealthCached(ctx)

		// Map status to HTTP status code
		httpStatus := http.StatusOK
		switch systemHealth.Status {
		case services.HealthStatusUnhealthy:
			httpStatus = http.StatusServiceUnavailable
		case services.HealthStatusDegraded:
			httpStatus = http.StatusOK // Still healthy but degraded
		case services.HealthStatusUnknown:
			httpStatus = http.StatusServiceUnavailable
		}

		// Convert to gin.H format for consistency
		response := gin.H{
			"status":     string(systemHealth.Status),
			"timestamp":  systemHealth.Timestamp.UTC().Format(time.RFC3339),
			"components": systemHealth.Components,
			"summary":    systemHealth.Summary,
		}

		c.JSON(httpStatus, response)
		return
	}

	// Fallback to legacy health checks
	status := "healthy"
	httpStatus := http.StatusOK
	checks := make(map[string]interface{})

	for name, checker := range h.checkers {
		start := time.Now()
		err := checker.HealthCheck(ctx)
		duration := time.Since(start)

		checkResult := map[string]interface{}{
			"status":   "healthy",
			"duration": duration.String(),
		}

		if err != nil {
			checkResult["status"] = "unhealthy"
			checkResult["error"] = err.Error()
			status = "unhealthy"
			httpStatus = http.StatusServiceUnavailable
			h.logger.WithFields(logrus.Fields{
				"checker":  name,
				"error":    err.Error(),
				"duration": duration,
			}).Error("Health check failed")
		}

		checks[name] = checkResult
	}

	response := gin.H{
		"status":    status,
		"timestamp": time.Now().UTC().Format(time.RFC3339),
		"checks":    checks,
	}

	if len(h.checkers) == 0 {
		response["message"] = "No health checkers configured"
	}

	c.JSON(httpStatus, response)
}

// Ready handles the /ready endpoint (readiness probe)
//
//	@Summary		Readiness check
//	@Description	Returns whether the service is ready to handle requests
//	@Tags			Health
//	@Accept			json
//	@Produce		json
//	@Success		200	{object}	map[string]interface{}	"Service is ready"
//	@Success		503	{object}	map[string]interface{}	"Service is not ready"
//	@Router			/ready [get]
func (h *HealthHandler) Ready(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), h.readinessTimeout)
	defer cancel()

	// Use comprehensive health service if available
	if h.healthService != nil {
		systemHealth := h.healthService.CheckHealthCached(ctx)

		// For readiness, we're more strict - only healthy is ready
		status := "ready"
		httpStatus := http.StatusOK

		if systemHealth.Status != services.HealthStatusHealthy {
			status = "not_ready"
			httpStatus = http.StatusServiceUnavailable
		}

		// Convert to readiness format
		response := gin.H{
			"status":     status,
			"timestamp":  systemHealth.Timestamp.UTC().Format(time.RFC3339),
			"components": systemHealth.Components,
			"summary":    systemHealth.Summary,
		}

		c.JSON(httpStatus, response)
		return
	}

	// Fallback to legacy readiness checks
	status := "ready"
	httpStatus := http.StatusOK
	checks := make(map[string]interface{})

	for name, checker := range h.checkers {
		start := time.Now()
		err := checker.HealthCheck(ctx)
		duration := time.Since(start)

		checkResult := map[string]interface{}{
			"status":   "ready",
			"duration": duration.String(),
		}

		if err != nil {
			checkResult["status"] = "not_ready"
			checkResult["error"] = err.Error()
			status = "not_ready"
			httpStatus = http.StatusServiceUnavailable
		}

		checks[name] = checkResult
	}

	response := gin.H{
		"status":    status,
		"timestamp": time.Now().UTC().Format(time.RFC3339),
		"checks":    checks,
	}

	c.JSON(httpStatus, response)
}

// Live handles the /live endpoint (liveness probe)
//
//	@Summary		Liveness check
//	@Description	Returns whether the service is alive (for Kubernetes liveness probe)
//	@Tags			Health
//	@Accept			json
//	@Produce		json
//	@Success		200	{object}	map[string]interface{}	"Service is alive"
//	@Router			/live [get]
func (h *HealthHandler) Live(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"status":    "alive",
		"timestamp": time.Now().UTC().Format(time.RFC3339),
		"service":   "eai-gateway",
	})
}

// DetailedHealth handles the /health/detailed endpoint for comprehensive health information
func (h *HealthHandler) DetailedHealth(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), h.healthTimeout)
	defer cancel()

	// Use comprehensive health service if available
	if h.healthService != nil {
		// Force fresh check (not cached) for detailed endpoint
		systemHealth := h.healthService.CheckHealth(ctx)

		// Map status to HTTP status code
		httpStatus := http.StatusOK
		switch systemHealth.Status {
		case services.HealthStatusUnhealthy:
			httpStatus = http.StatusServiceUnavailable
		case services.HealthStatusDegraded:
			httpStatus = http.StatusOK // Still healthy but degraded
		case services.HealthStatusUnknown:
			httpStatus = http.StatusServiceUnavailable
		}

		c.JSON(httpStatus, systemHealth)
		return
	}

	// Fallback response if no health service is configured
	c.JSON(http.StatusServiceUnavailable, gin.H{
		"error":     "Comprehensive health service not configured",
		"timestamp": time.Now().UTC().Format(time.RFC3339),
	})
}

// ComponentHealth handles the /health/component/{name} endpoint for individual component health
func (h *HealthHandler) ComponentHealth(c *gin.Context) {
	componentName := c.Param("name")
	if componentName == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"error":     "Component name is required",
			"timestamp": time.Now().UTC().Format(time.RFC3339),
		})
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), h.healthTimeout)
	defer cancel()

	// Use comprehensive health service if available
	if h.healthService != nil {
		health, found := h.healthService.GetComponentHealth(ctx, componentName)
		if !found {
			c.JSON(http.StatusNotFound, gin.H{
				"error":     "Component not found",
				"component": componentName,
				"timestamp": time.Now().UTC().Format(time.RFC3339),
			})
			return
		}

		// Map status to HTTP status code
		httpStatus := http.StatusOK
		switch health.Status {
		case services.HealthStatusUnhealthy:
			httpStatus = http.StatusServiceUnavailable
		case services.HealthStatusDegraded:
			httpStatus = http.StatusOK
		case services.HealthStatusUnknown:
			httpStatus = http.StatusServiceUnavailable
		}

		c.JSON(httpStatus, health)
		return
	}

	// Fallback response if no health service is configured
	c.JSON(http.StatusServiceUnavailable, gin.H{
		"error":     "Comprehensive health service not configured",
		"timestamp": time.Now().UTC().Format(time.RFC3339),
	})
}
