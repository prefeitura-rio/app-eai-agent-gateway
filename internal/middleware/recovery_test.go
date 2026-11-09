package middleware

import (
	"bytes"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"syscall"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"
)

// Synthetic values only: never a real citizen's number or token.
const (
	syntheticPhone  = "5521999990000"
	syntheticCookie = "session=synthetic-cookie-value"
	syntheticHeader = "synthetic-forwarded-value"
)

func newRecoveryTestRouter(t *testing.T, mode string, panicValue any) (*gin.Engine, *bytes.Buffer) {
	t.Helper()

	previousMode := gin.Mode()
	gin.SetMode(mode)
	t.Cleanup(func() { gin.SetMode(previousMode) })

	var buf bytes.Buffer
	logger := logrus.New()
	logger.SetOutput(&buf)
	logger.SetFormatter(&logrus.JSONFormatter{})
	logger.SetLevel(logrus.DebugLevel)

	router := gin.New()
	router.Use(RequestID())
	router.Use(Recovery(logger))
	router.GET("/api/v1/message/last-activity", func(c *gin.Context) {
		panic(panicValue)
	})

	return router, &buf
}

func newLastActivityRequest() *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/message/last-activity?user_number="+syntheticPhone, nil)
	req.Header.Set("X-Request-ID", "req-dev-447")
	req.Header.Set("Cookie", syntheticCookie)
	req.Header.Set("X-Forwarded-For", syntheticHeader)
	req.Header.Set("Authorization", "Bearer synthetic-token")
	return req
}

func assertNoRequestDump(t *testing.T, logs string) {
	t.Helper()

	for _, leaked := range []string{
		syntheticPhone,
		"user_number",
		syntheticCookie,
		syntheticHeader,
		"synthetic-token",
		"HTTP/1.1",
	} {
		if strings.Contains(logs, leaked) {
			t.Fatalf("recovery log leaked %q from the raw request:\n%s", leaked, logs)
		}
	}
}

func TestRecoveryDoesNotLogRawRequestInDebugMode(t *testing.T) {
	router, buf := newRecoveryTestRouter(t, gin.DebugMode, "boom")

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, newLastActivityRequest())

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected status 500, got %d", rec.Code)
	}

	logs := buf.String()
	assertNoRequestDump(t, logs)

	for _, want := range []string{
		`"msg":"Panic recovered"`,
		`"method":"GET"`,
		`"path":"/api/v1/message/last-activity"`,
		`"request_id":"req-dev-447"`,
		`"panic":"boom"`,
		`"stack":`,
	} {
		if !strings.Contains(logs, want) {
			t.Fatalf("expected recovery log to contain %s, got:\n%s", want, logs)
		}
	}
}

func TestRecoveryDoesNotLogRawRequestInReleaseMode(t *testing.T) {
	router, buf := newRecoveryTestRouter(t, gin.ReleaseMode, "boom")

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, newLastActivityRequest())

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected status 500, got %d", rec.Code)
	}
	assertNoRequestDump(t, buf.String())
	if !strings.Contains(buf.String(), `"path":"/api/v1/message/last-activity"`) {
		t.Fatalf("expected path without query in recovery log, got:\n%s", buf.String())
	}
}

func TestRecoveryDoesNotLogRawRequestOnBrokenPipe(t *testing.T) {
	brokenPipe := &net.OpError{
		Op:  "write",
		Net: "tcp",
		Err: os.NewSyscallError("write", syscall.EPIPE),
	}
	router, buf := newRecoveryTestRouter(t, gin.ReleaseMode, brokenPipe)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, newLastActivityRequest())

	logs := buf.String()
	assertNoRequestDump(t, logs)

	for _, want := range []string{
		`"broken_pipe":true`,
		`"path":"/api/v1/message/last-activity"`,
		`"request_id":"req-dev-447"`,
	} {
		if !strings.Contains(logs, want) {
			t.Fatalf("expected broken-pipe log to contain %s, got:\n%s", want, logs)
		}
	}
	if strings.Contains(logs, `"stack":`) {
		t.Fatalf("broken pipe is not a server fault and should not log a stack trace:\n%s", logs)
	}
}

func TestIsBrokenPipe(t *testing.T) {
	tests := []struct {
		name  string
		value any
		want  bool
	}{
		{"broken pipe", &net.OpError{Op: "write", Err: os.NewSyscallError("write", syscall.EPIPE)}, true},
		{"connection reset", &net.OpError{Op: "read", Err: os.NewSyscallError("read", syscall.ECONNRESET)}, true},
		{"other net error", &net.OpError{Op: "dial", Err: os.NewSyscallError("connect", syscall.ECONNREFUSED)}, false},
		{"plain error", errors.New("broken pipe"), false},
		{"string", "broken pipe", false},
		{"nil", nil, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isBrokenPipe(tt.value); got != tt.want {
				t.Fatalf("isBrokenPipe(%v) = %v, want %v", tt.value, got, tt.want)
			}
		})
	}
}

func TestRecoveryRepanicsErrAbortHandler(t *testing.T) {
	router, buf := newRecoveryTestRouter(t, gin.ReleaseMode, http.ErrAbortHandler)

	defer func() {
		if got := recover(); got != http.ErrAbortHandler {
			t.Fatalf("expected http.ErrAbortHandler to propagate, got %v", got)
		}
		if buf.Len() != 0 {
			t.Fatalf("expected no log for a deliberate abort, got:\n%s", buf.String())
		}
	}()

	router.ServeHTTP(httptest.NewRecorder(), newLastActivityRequest())
	t.Fatal("expected panic to propagate")
}
