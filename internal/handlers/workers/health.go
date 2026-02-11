package workers

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/sirupsen/logrus"
)

// HealthChecker interface for worker health check dependencies
type HealthChecker interface {
	HealthCheck(ctx context.Context) error
}

// WorkerHealthHandler handles worker health and readiness checks
type WorkerHealthHandler struct {
	rabbitMQ      HealthChecker
	redis         HealthChecker
	logger        *logrus.Logger
	healthTimeout time.Duration
}

// NewWorkerHealthHandler creates a new worker health handler
func NewWorkerHealthHandler(rabbitMQ, redis HealthChecker, logger *logrus.Logger) *WorkerHealthHandler {
	return &WorkerHealthHandler{
		rabbitMQ:      rabbitMQ,
		redis:         redis,
		logger:        logger,
		healthTimeout: 5 * time.Second,
	}
}

// Health handles the /health endpoint
// Returns comprehensive health status of all worker dependencies
func (h *WorkerHealthHandler) Health(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), h.healthTimeout)
	defer cancel()

	status := "healthy"
	httpStatus := http.StatusOK
	checks := make(map[string]interface{})

	// Check RabbitMQ
	if h.rabbitMQ != nil {
		start := time.Now()
		err := h.rabbitMQ.HealthCheck(ctx)
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
				"checker":  "rabbitmq",
				"error":    err.Error(),
				"duration": duration,
			}).Error("Worker health check failed: RabbitMQ")
		}

		checks["rabbitmq"] = checkResult
	}

	// Check Redis
	if h.redis != nil {
		start := time.Now()
		err := h.redis.HealthCheck(ctx)
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
				"checker":  "redis",
				"error":    err.Error(),
				"duration": duration,
			}).Error("Worker health check failed: Redis")
		}

		checks["redis"] = checkResult
	}

	response := map[string]interface{}{
		"status":    status,
		"timestamp": time.Now().UTC().Format(time.RFC3339),
		"checks":    checks,
		"service":   "eai-gateway-worker",
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(httpStatus)
	if err := json.NewEncoder(w).Encode(response); err != nil {
		h.logger.WithError(err).Error("Failed to encode health response")
	}
}

// Ready handles the /ready endpoint (readiness probe)
// Returns whether the worker is ready to consume messages
func (h *WorkerHealthHandler) Ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), h.healthTimeout)
	defer cancel()

	status := "ready"
	httpStatus := http.StatusOK
	checks := make(map[string]interface{})

	// For readiness, check only critical dependencies needed to process messages
	// RabbitMQ and Redis must both be available

	if h.rabbitMQ != nil {
		start := time.Now()
		err := h.rabbitMQ.HealthCheck(ctx)
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

		checks["rabbitmq"] = checkResult
	}

	if h.redis != nil {
		start := time.Now()
		err := h.redis.HealthCheck(ctx)
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

		checks["redis"] = checkResult
	}

	response := map[string]interface{}{
		"status":    status,
		"timestamp": time.Now().UTC().Format(time.RFC3339),
		"checks":    checks,
		"service":   "eai-gateway-worker",
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(httpStatus)
	if err := json.NewEncoder(w).Encode(response); err != nil {
		h.logger.WithError(err).Error("Failed to encode ready response")
	}
}

// Live handles the /live endpoint (liveness probe)
// Returns whether the worker process is alive
// This is a simple check that always returns 200 unless the process is completely dead
func (h *WorkerHealthHandler) Live(w http.ResponseWriter, r *http.Request) {
	response := map[string]interface{}{
		"status":    "alive",
		"timestamp": time.Now().UTC().Format(time.RFC3339),
		"service":   "eai-gateway-worker",
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if err := json.NewEncoder(w).Encode(response); err != nil {
		h.logger.WithError(err).Error("Failed to encode live response")
	}
}
