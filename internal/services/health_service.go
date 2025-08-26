package services

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/sirupsen/logrus"

	"github.com/prefeitura-rio/app-eai-agent-gateway/internal/models"
)

// HealthStatus represents the health status of a component
type HealthStatus string

const (
	HealthStatusHealthy   HealthStatus = "healthy"
	HealthStatusUnhealthy HealthStatus = "unhealthy"
	HealthStatusDegraded  HealthStatus = "degraded"
	HealthStatusUnknown   HealthStatus = "unknown"
)

// ComponentHealth represents the health of a single component
type ComponentHealth struct {
	Name        string                 `json:"name"`
	Status      HealthStatus           `json:"status"`
	Message     string                 `json:"message,omitempty"`
	LastChecked time.Time              `json:"last_checked"`
	Duration    time.Duration          `json:"duration_ms"`
	Details     map[string]interface{} `json:"details,omitempty"`
	Error       string                 `json:"error,omitempty"`
}

// SystemHealth represents the overall system health
type SystemHealth struct {
	Status     HealthStatus               `json:"status"`
	Components map[string]ComponentHealth `json:"components"`
	Summary    HealthSummary              `json:"summary"`
	Timestamp  time.Time                  `json:"timestamp"`
}

// HealthSummary provides a summary of health check results
type HealthSummary struct {
	Total     int `json:"total"`
	Healthy   int `json:"healthy"`
	Unhealthy int `json:"unhealthy"`
	Degraded  int `json:"degraded"`
	Unknown   int `json:"unknown"`
}

// HealthChecker interface for individual health check implementations
type HealthChecker interface {
	Name() string
	Check(ctx context.Context) ComponentHealth
}

// HealthService manages and coordinates health checks for all system components
type HealthService struct {
	checkers        []HealthChecker
	cache           map[string]ComponentHealth
	cacheTTL        time.Duration
	defaultTimeout  time.Duration
	logger          *logrus.Logger
	mu              sync.RWMutex
	lastFullCheck   time.Time
	checkInProgress bool
}

// HealthConfig configures the health service
type HealthConfig struct {
	CacheTTL       time.Duration
	DefaultTimeout time.Duration
	Logger         *logrus.Logger
}

// NewHealthService creates a new health service
func NewHealthService(config HealthConfig) *HealthService {
	if config.CacheTTL == 0 {
		config.CacheTTL = 30 * time.Second
	}
	if config.DefaultTimeout == 0 {
		config.DefaultTimeout = 10 * time.Second
	}
	if config.Logger == nil {
		config.Logger = logrus.New()
	}

	return &HealthService{
		checkers:       make([]HealthChecker, 0),
		cache:          make(map[string]ComponentHealth),
		cacheTTL:       config.CacheTTL,
		defaultTimeout: config.DefaultTimeout,
		logger:         config.Logger,
	}
}

// RegisterChecker adds a health checker to the service
func (h *HealthService) RegisterChecker(checker HealthChecker) {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.checkers = append(h.checkers, checker)
	h.logger.WithField("checker", checker.Name()).Info("Health checker registered")
}

// CheckHealth performs health checks for all registered components
func (h *HealthService) CheckHealth(ctx context.Context) SystemHealth {
	return h.checkHealthWithOptions(ctx, false)
}

// CheckHealthCached returns cached health results if available and fresh
func (h *HealthService) CheckHealthCached(ctx context.Context) SystemHealth {
	return h.checkHealthWithOptions(ctx, true)
}

// checkHealthWithOptions performs health checks with caching options
func (h *HealthService) checkHealthWithOptions(ctx context.Context, useCache bool) SystemHealth {
	h.mu.Lock()

	// Check if full check is already in progress
	if h.checkInProgress {
		h.mu.Unlock()
		// Return cached results if available
		if useCache && time.Since(h.lastFullCheck) < h.cacheTTL {
			return h.buildSystemHealthFromCache()
		}
		// Wait a bit and try again
		time.Sleep(100 * time.Millisecond)
		return h.checkHealthWithOptions(ctx, true)
	}

	// Check if we can use cached results
	if useCache && time.Since(h.lastFullCheck) < h.cacheTTL && len(h.cache) > 0 {
		result := h.buildSystemHealthFromCache()
		h.mu.Unlock()
		return result
	}

	h.checkInProgress = true
	h.mu.Unlock()

	defer func() {
		h.mu.Lock()
		h.checkInProgress = false
		h.lastFullCheck = time.Now()
		h.mu.Unlock()
	}()

	// Perform health checks concurrently
	results := make(chan ComponentHealth, len(h.checkers))
	var wg sync.WaitGroup

	h.mu.RLock()
	checkers := make([]HealthChecker, len(h.checkers))
	copy(checkers, h.checkers)
	h.mu.RUnlock()

	for _, checker := range checkers {
		wg.Add(1)
		go func(c HealthChecker) {
			defer wg.Done()

			// Create timeout context for individual health check
			checkCtx, cancel := context.WithTimeout(ctx, h.defaultTimeout)
			defer cancel()

			start := time.Now()
			health := h.performSingleCheck(checkCtx, c)
			health.Duration = time.Since(start)

			results <- health
		}(checker)
	}

	// Wait for all checks to complete
	go func() {
		wg.Wait()
		close(results)
	}()

	// Collect results
	components := make(map[string]ComponentHealth)
	for health := range results {
		components[health.Name] = health
	}

	// Update cache
	h.mu.Lock()
	h.cache = components
	h.mu.Unlock()

	return h.buildSystemHealth(components)
}

// performSingleCheck executes a single health check with error handling
func (h *HealthService) performSingleCheck(ctx context.Context, checker HealthChecker) ComponentHealth {
	defer func() {
		if r := recover(); r != nil {
			h.logger.WithFields(logrus.Fields{
				"checker": checker.Name(),
				"panic":   r,
			}).Error("Health check panicked")
		}
	}()

	start := time.Now()

	// Run the health check
	health := checker.Check(ctx)

	// Ensure required fields are set
	if health.Name == "" {
		health.Name = checker.Name()
	}
	if health.LastChecked.IsZero() {
		health.LastChecked = start
	}
	if health.Status == "" {
		health.Status = HealthStatusUnknown
	}

	return health
}

// buildSystemHealthFromCache builds system health from cached component results
func (h *HealthService) buildSystemHealthFromCache() SystemHealth {
	components := make(map[string]ComponentHealth)
	for name, health := range h.cache {
		components[name] = health
	}
	return h.buildSystemHealth(components)
}

// buildSystemHealth creates a SystemHealth from component health results
func (h *HealthService) buildSystemHealth(components map[string]ComponentHealth) SystemHealth {
	summary := HealthSummary{
		Total: len(components),
	}

	overallStatus := HealthStatusHealthy

	for _, health := range components {
		switch health.Status {
		case HealthStatusHealthy:
			summary.Healthy++
		case HealthStatusUnhealthy:
			summary.Unhealthy++
			overallStatus = HealthStatusUnhealthy
		case HealthStatusDegraded:
			summary.Degraded++
			if overallStatus == HealthStatusHealthy {
				overallStatus = HealthStatusDegraded
			}
		case HealthStatusUnknown:
			summary.Unknown++
			if overallStatus == HealthStatusHealthy {
				overallStatus = HealthStatusDegraded
			}
		}
	}

	// If we have any unhealthy components, mark as unhealthy
	if summary.Unhealthy > 0 {
		overallStatus = HealthStatusUnhealthy
	} else if summary.Degraded > 0 || summary.Unknown > 0 {
		overallStatus = HealthStatusDegraded
	}

	return SystemHealth{
		Status:     overallStatus,
		Components: components,
		Summary:    summary,
		Timestamp:  time.Now(),
	}
}

// GetComponentHealth returns health for a specific component
func (h *HealthService) GetComponentHealth(ctx context.Context, componentName string) (ComponentHealth, bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()

	// Check cache first
	if health, exists := h.cache[componentName]; exists && time.Since(health.LastChecked) < h.cacheTTL {
		return health, true
	}

	// Find and run specific checker
	for _, checker := range h.checkers {
		if checker.Name() == componentName {
			health := h.performSingleCheck(ctx, checker)
			h.cache[componentName] = health
			return health, true
		}
	}

	return ComponentHealth{}, false
}

// RabbitMQHealthService interface for RabbitMQ health checking
type RabbitMQHealthService interface {
	HealthCheck(ctx context.Context) error
	IsConnected() bool
}

// RabbitMQHealthChecker checks RabbitMQ connectivity
type RabbitMQHealthChecker struct {
	rabbitmqService RabbitMQHealthService
}

// NewRabbitMQHealthChecker creates a new RabbitMQ health checker
func NewRabbitMQHealthChecker(rabbitmqService RabbitMQHealthService) *RabbitMQHealthChecker {
	return &RabbitMQHealthChecker{
		rabbitmqService: rabbitmqService,
	}
}

// Name returns the checker name
func (r *RabbitMQHealthChecker) Name() string {
	return "rabbitmq"
}

// Check performs RabbitMQ health check
func (r *RabbitMQHealthChecker) Check(ctx context.Context) ComponentHealth {
	start := time.Now()

	health := ComponentHealth{
		Name:        r.Name(),
		LastChecked: start,
		Details:     make(map[string]interface{}),
	}

	// Check if service is initialized
	if r.rabbitmqService == nil {
		health.Status = HealthStatusUnhealthy
		health.Message = "RabbitMQ service not initialized"
		health.Error = "service is nil"
		return health
	}

	// Perform health check
	err := r.rabbitmqService.HealthCheck(ctx)
	if err != nil {
		health.Status = HealthStatusUnhealthy
		health.Message = "RabbitMQ connection failed"
		health.Error = err.Error()
		return health
	}

	// Get additional details
	health.Details["connection"] = map[string]interface{}{
		"connected": r.rabbitmqService.IsConnected(),
	}

	// Try to get queue info if connected
	if r.rabbitmqService.IsConnected() {
		// Add basic queue information
		health.Details["status"] = "connected"
	} else {
		health.Details["status"] = "disconnected"
	}

	health.Status = HealthStatusHealthy
	health.Message = "RabbitMQ is healthy"

	return health
}

// RedisHealthService interface for Redis health checking
type RedisHealthService interface {
	HealthCheck(ctx context.Context) error
	GetMetrics() CacheMetrics
	GetStats() *redis.PoolStats
}

// RedisHealthChecker checks Redis connectivity
type RedisHealthChecker struct {
	redisService RedisHealthService
}

// NewRedisHealthChecker creates a new Redis health checker
func NewRedisHealthChecker(redisService RedisHealthService) *RedisHealthChecker {
	return &RedisHealthChecker{
		redisService: redisService,
	}
}

// Name returns the checker name
func (r *RedisHealthChecker) Name() string {
	return "redis"
}

// Check performs Redis health check
func (r *RedisHealthChecker) Check(ctx context.Context) ComponentHealth {
	start := time.Now()

	health := ComponentHealth{
		Name:        r.Name(),
		LastChecked: start,
		Details:     make(map[string]interface{}),
	}

	// Check if service is initialized
	if r.redisService == nil {
		health.Status = HealthStatusUnhealthy
		health.Message = "Redis service not initialized"
		health.Error = "service is nil"
		return health
	}

	// Perform health check
	err := r.redisService.HealthCheck(ctx)
	if err != nil {
		health.Status = HealthStatusUnhealthy
		health.Message = "Redis connection failed"
		health.Error = err.Error()
		return health
	}

	// Get cache metrics
	cacheMetrics := r.redisService.GetMetrics()
	health.Details["cache"] = map[string]interface{}{
		"hits":             cacheMetrics.Hits,
		"misses":           cacheMetrics.Misses,
		"hit_ratio":        cacheMetrics.GetHitRatio(),
		"total_operations": cacheMetrics.TotalOperations,
		"errors":           cacheMetrics.Errors,
		"last_reset_time":  cacheMetrics.LastResetTime,
	}

	// Get pool statistics
	poolStats := r.redisService.GetStats()
	health.Details["pool"] = map[string]interface{}{
		"total_connections": poolStats.TotalConns,
		"idle_connections":  poolStats.IdleConns,
		"stale_connections": poolStats.StaleConns,
		"hits":              poolStats.Hits,
		"misses":            poolStats.Misses,
		"timeouts":          poolStats.Timeouts,
	}

	health.Status = HealthStatusHealthy
	health.Message = "Redis is healthy"

	return health
}

// ExternalAPIHealthChecker checks external API connectivity
type ExternalAPIHealthChecker struct {
	name           string
	endpoint       string
	timeout        time.Duration
	httpClient     *http.Client
	expectedStatus int
}

// NewExternalAPIHealthChecker creates a new external API health checker
func NewExternalAPIHealthChecker(name, endpoint string, timeout time.Duration) *ExternalAPIHealthChecker {
	if timeout == 0 {
		timeout = 5 * time.Second
	}

	return &ExternalAPIHealthChecker{
		name:           name,
		endpoint:       endpoint,
		timeout:        timeout,
		expectedStatus: http.StatusOK,
		httpClient: &http.Client{
			Timeout: timeout,
		},
	}
}

// Name returns the checker name
func (e *ExternalAPIHealthChecker) Name() string {
	return e.name
}

// Check performs external API health check
func (e *ExternalAPIHealthChecker) Check(ctx context.Context) ComponentHealth {
	start := time.Now()

	health := ComponentHealth{
		Name:        e.Name(),
		LastChecked: start,
		Details:     make(map[string]interface{}),
	}

	// Create request with context
	req, err := http.NewRequestWithContext(ctx, "GET", e.endpoint, nil)
	if err != nil {
		health.Status = HealthStatusUnhealthy
		health.Message = "Failed to create request"
		health.Error = err.Error()
		return health
	}

	// Add headers
	req.Header.Set("User-Agent", "EAI-Gateway-HealthCheck/1.0")
	req.Header.Set("Accept", "application/json")

	// Make request
	resp, err := e.httpClient.Do(req)
	if err != nil {
		health.Status = HealthStatusUnhealthy
		health.Message = "Failed to reach endpoint"
		health.Error = err.Error()
		health.Details["endpoint"] = e.endpoint
		return health
	}
	defer func() { _ = resp.Body.Close() }()

	// Check response
	health.Details["endpoint"] = e.endpoint
	health.Details["status_code"] = resp.StatusCode
	health.Details["response_time_ms"] = time.Since(start).Milliseconds()

	if resp.StatusCode != e.expectedStatus {
		health.Status = HealthStatusDegraded
		health.Message = fmt.Sprintf("Unexpected status code: %d", resp.StatusCode)
		return health
	}

	health.Status = HealthStatusHealthy
	health.Message = "External API is reachable"

	return health
}

// WorkerHealthService interface for worker health checking
type WorkerHealthService interface {
	GetMetricsSummary() map[string]interface{}
	GetTotalMessageCount() (int64, int64, int64)
	GetAllWorkerHealth() map[models.WorkerType]bool
	GetOverallSuccessRate() float64
}

// WorkerHealthChecker checks worker health
type WorkerHealthChecker struct {
	workerMetrics WorkerHealthService
}

// NewWorkerHealthChecker creates a new worker health checker
func NewWorkerHealthChecker(workerMetrics WorkerHealthService) *WorkerHealthChecker {
	return &WorkerHealthChecker{
		workerMetrics: workerMetrics,
	}
}

// Name returns the checker name
func (w *WorkerHealthChecker) Name() string {
	return "workers"
}

// Check performs worker health check
func (w *WorkerHealthChecker) Check(ctx context.Context) ComponentHealth {
	start := time.Now()

	health := ComponentHealth{
		Name:        w.Name(),
		LastChecked: start,
		Details:     make(map[string]interface{}),
	}

	// Check if service is initialized
	if w.workerMetrics == nil {
		health.Status = HealthStatusUnhealthy
		health.Message = "Worker metrics service not initialized"
		health.Error = "service is nil"
		return health
	}

	// Get comprehensive metrics
	summary := w.workerMetrics.GetMetricsSummary()

	health.Details["metrics"] = summary

	// Get total message counts
	total, successful, failed := w.workerMetrics.GetTotalMessageCount()
	health.Details["message_counts"] = map[string]interface{}{
		"total":      total,
		"successful": successful,
		"failed":     failed,
	}

	// Get worker health status
	workerHealth := w.workerMetrics.GetAllWorkerHealth()
	health.Details["worker_health"] = workerHealth

	// Determine health based on success rates and recent activity
	if total == 0 {
		health.Status = HealthStatusUnknown
		health.Message = "No worker activity detected"
		return health
	}

	successRate := w.workerMetrics.GetOverallSuccessRate()
	if successRate < 0.8 { // Less than 80% success rate
		health.Status = HealthStatusDegraded
		health.Message = fmt.Sprintf("Low success rate: %.2f%%", successRate*100)
		return health
	}

	if successRate < 0.5 { // Less than 50% success rate
		health.Status = HealthStatusUnhealthy
		health.Message = fmt.Sprintf("Very low success rate: %.2f%%", successRate*100)
		return health
	}

	health.Status = HealthStatusHealthy
	health.Message = fmt.Sprintf("Workers healthy (%.2f%% success rate)", successRate*100)

	return health
}
