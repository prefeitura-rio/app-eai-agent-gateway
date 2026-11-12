package workers

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Synthetic low-level error carrying an internal address, as the Redis/amqp
// drivers do. It must never reach the HTTP body of an unauthenticated probe.
const sensitiveDependencyError = "dial tcp 10.0.0.42:6379: connect: connection refused (redis-master.internal.svc)"

func TestWorkerHealthEndpoints_DoNotExposeRawDependencyError(t *testing.T) {
	cases := []struct {
		name            string
		call            func(h *WorkerHealthHandler, w http.ResponseWriter, r *http.Request)
		path            string
		componentStatus string
		overallStatus   string
	}{
		{name: "health", call: (*WorkerHealthHandler).Health, path: "/health", componentStatus: "unhealthy", overallStatus: "unhealthy"},
		{name: "ready", call: (*WorkerHealthHandler).Ready, path: "/ready", componentStatus: "not_ready", overallStatus: "not_ready"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			logger, hook := logtest.NewNullLogger()
			rabbitMQ := &MockHealthChecker{shouldFail: true, err: errors.New(sensitiveDependencyError)}
			redis := &MockHealthChecker{shouldFail: true, err: errors.New(sensitiveDependencyError)}
			handler := NewWorkerHealthHandler(rabbitMQ, redis, logger)

			w := httptest.NewRecorder()
			tc.call(handler, w, httptest.NewRequest(http.MethodGet, tc.path, nil))

			body := w.Body.String()
			assert.Equal(t, http.StatusServiceUnavailable, w.Code)
			assert.NotContains(t, body, "10.0.0.42")
			assert.NotContains(t, body, "redis-master.internal.svc")
			assert.NotContains(t, body, "connection refused")
			assert.NotContains(t, body, `"error"`)
			assert.Contains(t, body, `"status":"`+tc.overallStatus+`"`)
			assert.Contains(t, body, `"rabbitmq":{`)
			assert.Contains(t, body, `"redis":{`)
			assert.Contains(t, body, `"status":"`+tc.componentStatus+`"`)

			// The detail stays available to operators, only in the log.
			logged := 0
			for _, entry := range hook.AllEntries() {
				if entry.Level != logrus.ErrorLevel {
					continue
				}
				if errField, ok := entry.Data[logrus.ErrorKey].(error); ok && strings.Contains(errField.Error(), sensitiveDependencyError) {
					logged++
				}
			}
			require.Equal(t, 2, logged, "each failed dependency must be logged with its error")
		})
	}
}
