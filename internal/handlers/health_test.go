package handlers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"

	"github.com/prefeitura-rio/app-eai-agent-gateway/internal/services"
)

// Synthetic low-level error carrying an internal address, as the Postgres,
// Redis, amqp and HTTP clients do. It must never reach the HTTP body of an
// unauthenticated probe.
const sensitiveDependencyError = "dial tcp 10.0.0.42:5432: connect: connection refused (postgres.internal.svc)"

type failingChecker struct{ err error }

func (f failingChecker) HealthCheck(context.Context) error { return f.err }

type failingComponent struct{}

func (failingComponent) Name() string { return "postgres" }

func (failingComponent) Check(context.Context) services.ComponentHealth {
	return services.ComponentHealth{
		Name:        "postgres",
		Status:      services.HealthStatusUnhealthy,
		Message:     "connection failed",
		LastChecked: time.Now(),
		Error:       sensitiveDependencyError,
		Details:     map[string]interface{}{"endpoint": "http://postgres.internal.svc:5432"},
	}
}

func assertNoSensitiveData(t *testing.T, body string) {
	t.Helper()
	for _, leaked := range []string{"10.0.0.42", "postgres.internal.svc", "connection refused", `"error"`} {
		if strings.Contains(body, leaked) {
			t.Fatalf("response body leaks %q: %s", leaked, body)
		}
	}
}

func countLoggedErrors(hook *logtest.Hook) int {
	n := 0
	for _, entry := range hook.AllEntries() {
		if entry.Level != logrus.ErrorLevel {
			continue
		}
		if errField, ok := entry.Data[logrus.ErrorKey].(error); ok && strings.Contains(errField.Error(), sensitiveDependencyError) {
			n++
		}
	}
	return n
}

func TestHealthEndpoints_DoNotExposeRawDependencyError(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cases := []struct {
		name            string
		path            string
		componentStatus string
		overallStatus   string
	}{
		{name: "health", path: "/health", componentStatus: "unhealthy", overallStatus: "unhealthy"},
		{name: "ready", path: "/ready", componentStatus: "not_ready", overallStatus: "not_ready"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			logger, hook := logtest.NewNullLogger()
			h := NewHealthHandler(time.Second, time.Second, logger)
			h.AddChecker("postgres", failingChecker{err: errors.New(sensitiveDependencyError)})
			h.AddChecker("redis", failingChecker{err: errors.New(sensitiveDependencyError)})

			r := gin.New()
			r.GET("/health", h.Health)
			r.GET("/ready", h.Ready)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, tc.path, nil))

			body := w.Body.String()
			if w.Code != http.StatusServiceUnavailable {
				t.Fatalf("status = %d, want 503", w.Code)
			}
			assertNoSensitiveData(t, body)
			for _, want := range []string{
				`"status":"` + tc.overallStatus + `"`,
				`"postgres":{`,
				`"redis":{`,
				`"status":"` + tc.componentStatus + `"`,
			} {
				if !strings.Contains(body, want) {
					t.Fatalf("response body misses %s: %s", want, body)
				}
			}
			if got := countLoggedErrors(hook); got != 2 {
				t.Fatalf("logged dependency errors = %d, want 2", got)
			}
		})
	}
}

func TestHealthEndpoints_HealthService_DoNotExposeComponentError(t *testing.T) {
	gin.SetMode(gin.TestMode)

	for _, path := range []string{"/health", "/ready"} {
		t.Run(strings.TrimPrefix(path, "/"), func(t *testing.T) {
			logger, _ := logtest.NewNullLogger()
			svc := services.NewHealthService(services.HealthConfig{Logger: logger})
			svc.RegisterChecker(failingComponent{})
			h := NewEnhancedHealthHandler(svc, logger)

			r := gin.New()
			r.GET("/health", h.Health)
			r.GET("/ready", h.Ready)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))

			body := w.Body.String()
			if w.Code != http.StatusServiceUnavailable {
				t.Fatalf("status = %d, want 503", w.Code)
			}
			assertNoSensitiveData(t, body)
			if !strings.Contains(body, `"postgres":{`) || !strings.Contains(body, `"status":"unhealthy"`) {
				t.Fatalf("response body misses component name or status: %s", body)
			}
		})
	}
}
