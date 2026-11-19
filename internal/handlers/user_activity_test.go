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

// countingLastActivityRedis records whether the handler reached the cache, so
// a rejected request can be shown to never look the user number up.
type countingLastActivityRedis struct {
	lastActivityRedis
	lookups int
}

func (s *countingLastActivityRedis) GetUserLastActivity(ctx context.Context, n string) (*time.Time, error) {
	s.lookups++
	return s.lastActivityRedis.GetUserLastActivity(ctx, n)
}

// Synthetic secret for tests only.
const testLastActivityToken = "test-last-activity-token-0123456789"

func runLastActivity(t *testing.T, configured string, header *string) (*httptest.ResponseRecorder, *countingLastActivityRedis, *logtest.Hook) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	logger, hook := logtest.NewNullLogger()
	logger.SetLevel(logrus.DebugLevel)
	logger.SetOutput(io.Discard)

	cfg := &config.Config{}
	cfg.Postgres.UserActivityTTL = time.Hour
	cfg.Security.LastActivityAuthToken = configured
	redis := &countingLastActivityRedis{lastActivityRedis: lastActivityRedis{ts: time.Now()}}
	h := NewUserActivityHandler(logger, cfg, redis, nil)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/message/last-activity?user_number="+testPhone, nil)
	if header != nil {
		c.Request.Header.Set(LastActivityTokenHeader, *header)
	}

	h.HandleLastActivity(c)
	return w, redis, hook
}

func strPtr(s string) *string { return &s }

func TestHandleLastActivity_TokenConfigured_RejectsWithoutValidToken(t *testing.T) {
	cases := map[string]*string{
		"missing header":  nil,
		"empty header":    strPtr(""),
		"wrong token":     strPtr("not-the-token"),
		"token prefix":    strPtr(testLastActivityToken[:10]),
		"token plus more": strPtr(testLastActivityToken + "x"),
	}
	for name, header := range cases {
		t.Run(name, func(t *testing.T) {
			w, redis, hook := runLastActivity(t, testLastActivityToken, header)
			if w.Code != http.StatusUnauthorized {
				t.Fatalf("expected 401, got %d: %s", w.Code, w.Body.String())
			}
			if redis.lookups != 0 {
				t.Fatalf("rejected request must not look up the user, got %d lookups", redis.lookups)
			}
			body := w.Body.String()
			if strings.Contains(body, testPhone) || strings.Contains(body, "last_message_timestamp") {
				t.Fatalf("401 body leaks activity data: %s", body)
			}
			for _, e := range hook.AllEntries() {
				if strings.Contains(e.Message, testLastActivityToken) {
					t.Fatalf("log message leaks the token: %q", e.Message)
				}
				for k, v := range e.Data {
					if s, ok := v.(string); ok && strings.Contains(s, testLastActivityToken) {
						t.Fatalf("log field %q leaks the token", k)
					}
				}
			}
		})
	}
}

func TestHandleLastActivity_TokenConfigured_AcceptsValidToken(t *testing.T) {
	w, redis, hook := runLastActivity(t, testLastActivityToken, strPtr(testLastActivityToken))
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if redis.lookups != 1 {
		t.Fatalf("expected one cache lookup, got %d", redis.lookups)
	}
	if !strings.Contains(w.Body.String(), testPhone) {
		t.Fatalf("response should keep user_number, got %s", w.Body.String())
	}
	assertNoPhoneInLogs(t, hook, testPhone)
}

func TestHandleLastActivity_TokenConfiguredWithWhitespace_IsTrimmed(t *testing.T) {
	w, _, _ := runLastActivity(t, "  "+testLastActivityToken+"\n", strPtr(testLastActivityToken))
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestHandleLastActivity_TokenNotConfigured_KeepsPreviousBehaviour(t *testing.T) {
	for name, configured := range map[string]string{"empty": "", "blank": "   "} {
		t.Run(name, func(t *testing.T) {
			w, redis, hook := runLastActivity(t, configured, nil)
			if w.Code != http.StatusOK {
				t.Fatalf("expected 200 without configured token, got %d: %s", w.Code, w.Body.String())
			}
			if redis.lookups != 1 {
				t.Fatalf("expected one cache lookup, got %d", redis.lookups)
			}
			warned := false
			for _, e := range hook.AllEntries() {
				if e.Level == logrus.WarnLevel && strings.Contains(e.Message, "LAST_ACTIVITY_AUTH_TOKEN is not set") {
					warned = true
				}
			}
			if !warned {
				t.Fatal("expected a warning that the route has no caller authentication")
			}
		})
	}
}
