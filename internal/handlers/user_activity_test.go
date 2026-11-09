package handlers

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"

	"github.com/prefeitura-rio/app-eai-agent-gateway/internal/config"
)

// lastActivityRedis answers GetUserLastActivity from cache so the handler
// never reaches PostgreSQL.
type lastActivityRedis struct {
	stubRedis
	ts time.Time
}

func (s *lastActivityRedis) GetUserLastActivity(context.Context, string) (*time.Time, error) {
	return &s.ts, nil
}

func (s *lastActivityRedis) GetUserLastActivityTTL(context.Context, string) (time.Duration, error) {
	return time.Minute, nil
}

// Synthetic number (LGPD: never a real one).
const testPhone = "5521900000000"

// assertNoPhoneInLogs fails if any captured entry carries the full phone
// number, in a field value or in the message.
func assertNoPhoneInLogs(t *testing.T, hook *logtest.Hook, phone string) {
	t.Helper()
	entries := hook.AllEntries()
	if len(entries) == 0 {
		t.Fatal("expected log entries to be captured")
	}
	for _, e := range entries {
		if strings.Contains(e.Message, phone) {
			t.Errorf("log message leaks the phone number: %q", e.Message)
		}
		for k, v := range e.Data {
			if s, ok := v.(string); ok && strings.Contains(s, phone) {
				t.Errorf("log field %q leaks the phone number in %q", k, e.Message)
			}
		}
	}
}

func TestHandleLastActivity_DoesNotLogPhoneNumber(t *testing.T) {
	gin.SetMode(gin.TestMode)
	logger, hook := logtest.NewNullLogger()
	logger.SetLevel(logrus.DebugLevel)
	logger.SetOutput(io.Discard)

	cfg := &config.Config{}
	cfg.Postgres.UserActivityTTL = time.Hour
	h := NewUserActivityHandler(logger, cfg, &lastActivityRedis{ts: time.Now()}, nil)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/message/last-activity?user_number="+testPhone, nil)

	h.HandleLastActivity(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	// The API response itself still carries the number (contract unchanged).
	if !strings.Contains(w.Body.String(), testPhone) {
		t.Fatalf("response should keep user_number, got %s", w.Body.String())
	}
	assertNoPhoneInLogs(t, hook, testPhone)

	last := hook.LastEntry()
	if got := last.Data["user_number"]; got != "5521900***" {
		t.Fatalf("expected masked user_number in log, got %v", got)
	}
}
