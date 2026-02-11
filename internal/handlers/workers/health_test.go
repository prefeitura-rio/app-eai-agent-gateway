package workers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
)

// MockHealthChecker is a mock implementation of HealthChecker for testing
type MockHealthChecker struct {
	shouldFail bool
	err        error
}

func (m *MockHealthChecker) HealthCheck(ctx context.Context) error {
	if m.shouldFail {
		if m.err != nil {
			return m.err
		}
		return errors.New("mock health check failed")
	}
	return nil
}

func TestWorkerHealthHandler_Live(t *testing.T) {
	logger := logrus.New()
	logger.SetOutput(logrus.StandardLogger().Out)

	handler := NewWorkerHealthHandler(nil, nil, logger)

	req := httptest.NewRequest("GET", "/live", nil)
	w := httptest.NewRecorder()

	handler.Live(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var response map[string]interface{}
	err := json.NewDecoder(w.Body).Decode(&response)
	assert.NoError(t, err)

	assert.Equal(t, "alive", response["status"])
	assert.Equal(t, "eai-gateway-worker", response["service"])
	assert.NotEmpty(t, response["timestamp"])
}

func TestWorkerHealthHandler_Health_AllHealthy(t *testing.T) {
	logger := logrus.New()
	logger.SetOutput(logrus.StandardLogger().Out)

	rabbitMQ := &MockHealthChecker{shouldFail: false}
	redis := &MockHealthChecker{shouldFail: false}

	handler := NewWorkerHealthHandler(rabbitMQ, redis, logger)

	req := httptest.NewRequest("GET", "/health", nil)
	w := httptest.NewRecorder()

	handler.Health(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var response map[string]interface{}
	err := json.NewDecoder(w.Body).Decode(&response)
	assert.NoError(t, err)

	assert.Equal(t, "healthy", response["status"])
	assert.Equal(t, "eai-gateway-worker", response["service"])

	checks := response["checks"].(map[string]interface{})
	assert.NotNil(t, checks["rabbitmq"])
	assert.NotNil(t, checks["redis"])

	rabbitMQCheck := checks["rabbitmq"].(map[string]interface{})
	assert.Equal(t, "healthy", rabbitMQCheck["status"])

	redisCheck := checks["redis"].(map[string]interface{})
	assert.Equal(t, "healthy", redisCheck["status"])
}

func TestWorkerHealthHandler_Health_RabbitMQUnhealthy(t *testing.T) {
	logger := logrus.New()
	logger.SetOutput(logrus.StandardLogger().Out)

	rabbitMQ := &MockHealthChecker{shouldFail: true, err: errors.New("connection refused")}
	redis := &MockHealthChecker{shouldFail: false}

	handler := NewWorkerHealthHandler(rabbitMQ, redis, logger)

	req := httptest.NewRequest("GET", "/health", nil)
	w := httptest.NewRecorder()

	handler.Health(w, req)

	assert.Equal(t, http.StatusServiceUnavailable, w.Code)

	var response map[string]interface{}
	err := json.NewDecoder(w.Body).Decode(&response)
	assert.NoError(t, err)

	assert.Equal(t, "unhealthy", response["status"])

	checks := response["checks"].(map[string]interface{})
	rabbitMQCheck := checks["rabbitmq"].(map[string]interface{})
	assert.Equal(t, "unhealthy", rabbitMQCheck["status"])
	assert.Equal(t, "connection refused", rabbitMQCheck["error"])

	// Redis should still be healthy
	redisCheck := checks["redis"].(map[string]interface{})
	assert.Equal(t, "healthy", redisCheck["status"])
}

func TestWorkerHealthHandler_Health_RedisUnhealthy(t *testing.T) {
	logger := logrus.New()
	logger.SetOutput(logrus.StandardLogger().Out)

	rabbitMQ := &MockHealthChecker{shouldFail: false}
	redis := &MockHealthChecker{shouldFail: true, err: errors.New("timeout")}

	handler := NewWorkerHealthHandler(rabbitMQ, redis, logger)

	req := httptest.NewRequest("GET", "/health", nil)
	w := httptest.NewRecorder()

	handler.Health(w, req)

	assert.Equal(t, http.StatusServiceUnavailable, w.Code)

	var response map[string]interface{}
	err := json.NewDecoder(w.Body).Decode(&response)
	assert.NoError(t, err)

	assert.Equal(t, "unhealthy", response["status"])

	checks := response["checks"].(map[string]interface{})
	redisCheck := checks["redis"].(map[string]interface{})
	assert.Equal(t, "unhealthy", redisCheck["status"])
	assert.Equal(t, "timeout", redisCheck["error"])

	// RabbitMQ should still be healthy
	rabbitMQCheck := checks["rabbitmq"].(map[string]interface{})
	assert.Equal(t, "healthy", rabbitMQCheck["status"])
}

func TestWorkerHealthHandler_Ready_AllReady(t *testing.T) {
	logger := logrus.New()
	logger.SetOutput(logrus.StandardLogger().Out)

	rabbitMQ := &MockHealthChecker{shouldFail: false}
	redis := &MockHealthChecker{shouldFail: false}

	handler := NewWorkerHealthHandler(rabbitMQ, redis, logger)

	req := httptest.NewRequest("GET", "/ready", nil)
	w := httptest.NewRecorder()

	handler.Ready(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var response map[string]interface{}
	err := json.NewDecoder(w.Body).Decode(&response)
	assert.NoError(t, err)

	assert.Equal(t, "ready", response["status"])
	assert.Equal(t, "eai-gateway-worker", response["service"])
}

func TestWorkerHealthHandler_Ready_NotReady(t *testing.T) {
	logger := logrus.New()
	logger.SetOutput(logrus.StandardLogger().Out)

	rabbitMQ := &MockHealthChecker{shouldFail: true}
	redis := &MockHealthChecker{shouldFail: false}

	handler := NewWorkerHealthHandler(rabbitMQ, redis, logger)

	req := httptest.NewRequest("GET", "/ready", nil)
	w := httptest.NewRecorder()

	handler.Ready(w, req)

	assert.Equal(t, http.StatusServiceUnavailable, w.Code)

	var response map[string]interface{}
	err := json.NewDecoder(w.Body).Decode(&response)
	assert.NoError(t, err)

	assert.Equal(t, "not_ready", response["status"])
}

func TestWorkerHealthHandler_NilDependencies(t *testing.T) {
	logger := logrus.New()
	logger.SetOutput(logrus.StandardLogger().Out)

	// Test with nil dependencies (should not panic)
	handler := NewWorkerHealthHandler(nil, nil, logger)

	t.Run("Health with nil dependencies", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/health", nil)
		w := httptest.NewRecorder()

		handler.Health(w, req)

		assert.Equal(t, http.StatusOK, w.Code)

		var response map[string]interface{}
		err := json.NewDecoder(w.Body).Decode(&response)
		assert.NoError(t, err)

		assert.Equal(t, "healthy", response["status"])
	})

	t.Run("Ready with nil dependencies", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/ready", nil)
		w := httptest.NewRecorder()

		handler.Ready(w, req)

		assert.Equal(t, http.StatusOK, w.Code)

		var response map[string]interface{}
		err := json.NewDecoder(w.Body).Decode(&response)
		assert.NoError(t, err)

		assert.Equal(t, "ready", response["status"])
	})
}
