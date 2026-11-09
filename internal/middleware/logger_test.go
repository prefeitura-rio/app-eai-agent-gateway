package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	logtest "github.com/sirupsen/logrus/hooks/test"
)

func TestLogger_MasksPhoneInQuery(t *testing.T) {
	const phone = "5521900000000" // synthetic
	gin.SetMode(gin.TestMode)
	logger, hook := logtest.NewNullLogger()

	r := gin.New()
	r.Use(Logger(logger))
	r.GET("/api/v1/message/last-activity", func(c *gin.Context) { c.Status(http.StatusOK) })

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/message/last-activity?user_number="+phone+"&foo=bar", nil))

	entry := hook.LastEntry()
	if entry == nil {
		t.Fatal("expected an access log entry")
	}
	path, _ := entry.Data["path"].(string)
	if strings.Contains(path, phone) {
		t.Fatalf("access log leaks the phone number: %q", path)
	}
	if !strings.Contains(path, "user_number=5521900%2A%2A%2A") || !strings.Contains(path, "foo=bar") {
		t.Fatalf("expected masked user_number and other params kept, got %q", path)
	}
}

func TestRedactPathQuery(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "no query", in: "/health", want: "/health"},
		{name: "unrelated query kept as is", in: "/api/v1/message/response?message_id=abc", want: "/api/v1/message/response?message_id=abc"},
		{name: "phone masked", in: "/x?user_number=5521900000000", want: "/x?user_number=5521900%2A%2A%2A"},
		{name: "malformed query dropped", in: "/x?user_number=%zz5521900000000", want: "/x?[query omitted]"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := redactPathQuery(tt.in); got != tt.want {
				t.Fatalf("redactPathQuery(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
