package services

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/sirupsen/logrus"

	"github.com/prefeitura-rio/app-eai-agent-gateway/internal/config"
)

// CacheMetrics tracks cache hit/miss statistics
type CacheMetrics struct {
	mu              sync.RWMutex
	Hits            int64     `json:"hits"`
	Misses          int64     `json:"misses"`
	Sets            int64     `json:"sets"`
	Deletes         int64     `json:"deletes"`
	Errors          int64     `json:"errors"`
	TotalOperations int64     `json:"total_operations"`
	LastResetTime   time.Time `json:"last_reset_time"`
}

// GetHitRatio returns the cache hit ratio as a percentage
func (m *CacheMetrics) GetHitRatio() float64 {
	m.mu.RLock()
	defer m.mu.RUnlock()

	total := m.Hits + m.Misses
	if total == 0 {
		return 0.0
	}
	return float64(m.Hits) / float64(total) * 100.0
}

// GetMissRatio returns the cache miss ratio as a percentage
func (m *CacheMetrics) GetMissRatio() float64 {
	m.mu.RLock()
	defer m.mu.RUnlock()

	total := m.Hits + m.Misses
	if total == 0 {
		return 0.0
	}
	return float64(m.Misses) / float64(total) * 100.0
}

// Reset resets all metrics counters
func (m *CacheMetrics) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.Hits = 0
	m.Misses = 0
	m.Sets = 0
	m.Deletes = 0
	m.Errors = 0
	m.TotalOperations = 0
	m.LastResetTime = time.Now()
}

// GetSnapshot returns a copy of current metrics
func (m *CacheMetrics) GetSnapshot() CacheMetrics {
	m.mu.RLock()
	defer m.mu.RUnlock()

	return CacheMetrics{
		Hits:            m.Hits,
		Misses:          m.Misses,
		Sets:            m.Sets,
		Deletes:         m.Deletes,
		Errors:          m.Errors,
		TotalOperations: m.TotalOperations,
		LastResetTime:   m.LastResetTime,
	}
}

// RedisService handles Redis operations with connection pooling
type RedisService struct {
	client  *redis.Client
	logger  *logrus.Logger
	config  *config.Config
	metrics *CacheMetrics
}

// CacheInterface defines the contract for caching operations
type CacheInterface interface {
	Get(ctx context.Context, key string) (string, error)
	Set(ctx context.Context, key string, value interface{}, ttl time.Duration) error
	Delete(ctx context.Context, key string) error
	Exists(ctx context.Context, key string) (bool, error)
	SetJSON(ctx context.Context, key string, value interface{}, ttl time.Duration) error
	GetJSON(ctx context.Context, key string, dest interface{}) error

	// Task status operations
	SetTaskStatus(ctx context.Context, taskID string, status string, ttl time.Duration) error
	GetTaskStatus(ctx context.Context, taskID string) (string, error)
	SetTaskResult(ctx context.Context, taskID string, result interface{}, ttl time.Duration) error
	GetTaskResult(ctx context.Context, taskID string, dest interface{}) error

	// Agent ID caching
	SetAgentID(ctx context.Context, userID string, agentID string, ttl time.Duration) error
	GetAgentID(ctx context.Context, userID string) (string, error)
	DeleteAgentID(ctx context.Context, userID string) error

	// Callback URL storage
	StoreCallbackURL(ctx context.Context, messageID string, callbackURL string, ttl time.Duration) error
	GetCallbackURL(ctx context.Context, messageID string) (string, error)
	DeleteCallbackURL(ctx context.Context, messageID string) error

	// Health check
	Ping(ctx context.Context) error
	Close() error

	// Metrics
	GetMetrics() CacheMetrics
	ResetMetrics()
}

// NewRedisService creates a new Redis service with connection pooling
func NewRedisService(cfg *config.Config, logger *logrus.Logger) (*RedisService, error) {
	// Parse Redis DSN for main connection
	opts, err := redis.ParseURL(cfg.Redis.DSN)
	if err != nil {
		return nil, fmt.Errorf("failed to parse Redis DSN: %w", err)
	}

	// Configure connection pool
	opts.PoolSize = cfg.Redis.PoolSize
	opts.MinIdleConns = cfg.Redis.MinIdleConnections
	opts.MaxIdleConns = cfg.Redis.MaxIdleConnections
	opts.ConnMaxIdleTime = time.Duration(cfg.Redis.ConnectionMaxIdleTime) * time.Second
	opts.ConnMaxLifetime = time.Duration(cfg.Redis.ConnectionMaxLifetime) * time.Second
	opts.DialTimeout = time.Duration(cfg.Redis.DialTimeout) * time.Second
	opts.ReadTimeout = time.Duration(cfg.Redis.ReadTimeout) * time.Second
	opts.WriteTimeout = time.Duration(cfg.Redis.WriteTimeout) * time.Second

	client := redis.NewClient(opts)

	// Test connection
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := client.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("failed to connect to Redis: %w", err)
	}

	logger.WithFields(logrus.Fields{
		"dsn":       cfg.Redis.DSN,
		"pool_size": cfg.Redis.PoolSize,
		"min_idle":  cfg.Redis.MinIdleConnections,
		"max_idle":  cfg.Redis.MaxIdleConnections,
	}).Info("Redis service initialized successfully")

	return &RedisService{
		client: client,
		logger: logger,
		config: cfg,
		metrics: &CacheMetrics{
			LastResetTime: time.Now(),
		},
	}, nil
}

// Get retrieves a value by key
func (r *RedisService) Get(ctx context.Context, key string) (string, error) {
	r.recordOperation()

	result := r.client.Get(ctx, key)
	if err := result.Err(); err != nil {
		if err == redis.Nil {
			r.recordMiss()
			return "", fmt.Errorf("key not found: %s", key)
		}
		r.recordError()
		r.logger.WithError(err).WithField("key", key).Error("Failed to get value from Redis")
		return "", fmt.Errorf("redis get error: %w", err)
	}

	r.recordHit()
	return result.Val(), nil
}

// Set stores a value with TTL (generic version)
func (r *RedisService) SetValue(ctx context.Context, key string, value interface{}, ttl time.Duration) error {
	r.recordOperation()

	if err := r.client.Set(ctx, key, value, ttl).Err(); err != nil {
		r.recordError()
		r.logger.WithError(err).WithFields(logrus.Fields{
			"key": key,
			"ttl": ttl,
		}).Error("Failed to set value in Redis")
		return fmt.Errorf("redis set error: %w", err)
	}

	r.recordSet()
	return nil
}

// Set stores a string value with TTL (implements interface)
func (r *RedisService) Set(ctx context.Context, key string, value string, ttl time.Duration) error {
	return r.SetValue(ctx, key, value, ttl)
}

// Delete removes a key
func (r *RedisService) Delete(ctx context.Context, key string) error {
	r.recordOperation()

	if err := r.client.Del(ctx, key).Err(); err != nil {
		r.recordError()
		r.logger.WithError(err).WithField("key", key).Error("Failed to delete key from Redis")
		return fmt.Errorf("redis delete error: %w", err)
	}

	r.recordDelete()
	return nil
}

// Exists checks if a key exists
func (r *RedisService) Exists(ctx context.Context, key string) (bool, error) {
	result := r.client.Exists(ctx, key)
	if err := result.Err(); err != nil {
		r.logger.WithError(err).WithField("key", key).Error("Failed to check key existence in Redis")
		return false, fmt.Errorf("redis exists error: %w", err)
	}
	return result.Val() > 0, nil
}

// SetJSON stores a JSON-encoded value
func (r *RedisService) SetJSON(ctx context.Context, key string, value interface{}, ttl time.Duration) error {
	jsonData, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("failed to marshal JSON: %w", err)
	}
	return r.SetValue(ctx, key, jsonData, ttl)
}

// GetJSON retrieves and unmarshals a JSON value
func (r *RedisService) GetJSON(ctx context.Context, key string, dest interface{}) error {
	jsonStr, err := r.Get(ctx, key)
	if err != nil {
		return err
	}

	if err := json.Unmarshal([]byte(jsonStr), dest); err != nil {
		r.logger.WithError(err).WithField("key", key).Error("Failed to unmarshal JSON from Redis")
		return fmt.Errorf("failed to unmarshal JSON: %w", err)
	}
	return nil
}

// SetTaskStatus stores task status with configured TTL
func (r *RedisService) SetTaskStatus(ctx context.Context, taskID string, status string, ttl time.Duration) error {
	key := fmt.Sprintf("task:status:%s", taskID)
	return r.SetValue(ctx, key, status, ttl)
}

// GetTaskStatus retrieves task status
func (r *RedisService) GetTaskStatus(ctx context.Context, taskID string) (string, error) {
	key := fmt.Sprintf("task:status:%s", taskID)
	return r.Get(ctx, key)
}

// SetTaskResult stores task result with configured TTL
func (r *RedisService) SetTaskResult(ctx context.Context, taskID string, result interface{}, ttl time.Duration) error {
	key := fmt.Sprintf("task:result:%s", taskID)
	return r.SetJSON(ctx, key, result, ttl)
}

// GetTaskResult retrieves task result
func (r *RedisService) GetTaskResult(ctx context.Context, taskID string, dest interface{}) error {
	key := fmt.Sprintf("task:result:%s", taskID)
	return r.GetJSON(ctx, key, dest)
}

// SetAgentID caches agent ID for a user with configured TTL
func (r *RedisService) SetAgentID(ctx context.Context, userID string, agentID string, ttl time.Duration) error {
	key := fmt.Sprintf("agent:id:%s", userID)
	return r.SetValue(ctx, key, agentID, ttl)
}

// GetAgentID retrieves cached agent ID for a user
func (r *RedisService) GetAgentID(ctx context.Context, userID string) (string, error) {
	key := fmt.Sprintf("agent:id:%s", userID)
	return r.Get(ctx, key)
}

// DeleteAgentID removes cached agent ID for a user
func (r *RedisService) DeleteAgentID(ctx context.Context, userID string) error {
	key := fmt.Sprintf("agent:id:%s", userID)
	return r.Delete(ctx, key)
}

// StoreCallbackURL stores callback URL for a message with configured TTL
func (r *RedisService) StoreCallbackURL(ctx context.Context, messageID string, callbackURL string, ttl time.Duration) error {
	key := fmt.Sprintf("callback:url:%s", messageID)
	return r.SetValue(ctx, key, callbackURL, ttl)
}

// GetCallbackURL retrieves callback URL for a message
func (r *RedisService) GetCallbackURL(ctx context.Context, messageID string) (string, error) {
	key := fmt.Sprintf("callback:url:%s", messageID)
	return r.Get(ctx, key)
}

// DeleteCallbackURL removes callback URL for a message
func (r *RedisService) DeleteCallbackURL(ctx context.Context, messageID string) error {
	key := fmt.Sprintf("callback:url:%s", messageID)
	return r.Delete(ctx, key)
}

// Ping tests the Redis connection
func (r *RedisService) Ping(ctx context.Context) error {
	if err := r.client.Ping(ctx).Err(); err != nil {
		r.logger.WithError(err).Error("Redis ping failed")
		return fmt.Errorf("redis ping error: %w", err)
	}
	return nil
}

// Close closes the Redis connection
func (r *RedisService) Close() error {
	if err := r.client.Close(); err != nil {
		r.logger.WithError(err).Error("Failed to close Redis connection")
		return fmt.Errorf("redis close error: %w", err)
	}
	r.logger.Info("Redis connection closed")
	return nil
}

// Metrics recording methods
func (r *RedisService) recordOperation() {
	r.metrics.mu.Lock()
	defer r.metrics.mu.Unlock()
	r.metrics.TotalOperations++
}

func (r *RedisService) recordHit() {
	r.metrics.mu.Lock()
	defer r.metrics.mu.Unlock()
	r.metrics.Hits++
}

func (r *RedisService) recordMiss() {
	r.metrics.mu.Lock()
	defer r.metrics.mu.Unlock()
	r.metrics.Misses++
}

func (r *RedisService) recordSet() {
	r.metrics.mu.Lock()
	defer r.metrics.mu.Unlock()
	r.metrics.Sets++
}

func (r *RedisService) recordDelete() {
	r.metrics.mu.Lock()
	defer r.metrics.mu.Unlock()
	r.metrics.Deletes++
}

func (r *RedisService) recordError() {
	r.metrics.mu.Lock()
	defer r.metrics.mu.Unlock()
	r.metrics.Errors++
}

// GetMetrics returns a snapshot of current cache metrics
func (r *RedisService) GetMetrics() CacheMetrics {
	return r.metrics.GetSnapshot()
}

// ResetMetrics resets all cache metrics
func (r *RedisService) ResetMetrics() {
	r.metrics.Reset()
	r.logger.Info("Cache metrics reset")
}

// GetStats returns Redis connection pool statistics
func (r *RedisService) GetStats() *redis.PoolStats {
	return r.client.PoolStats()
}

// HealthCheck implements the HealthChecker interface
func (r *RedisService) HealthCheck(ctx context.Context) error {
	return r.Ping(ctx)
}
