package workers

import (
	"strings"
	"testing"

	"github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"

	"github.com/prefeitura-rio/app-eai-agent-gateway/internal/models"
)

func TestNewProcessingContext_MasksPhoneInLogger(t *testing.T) {
	const phone = "5521900000000" // synthetic
	logger, hook := logtest.NewNullLogger()
	logger.SetLevel(logrus.DebugLevel)

	procCtx := NewProcessingContext(&models.QueueMessage{ID: "msg-1", UserNumber: phone}, logger)

	// The context keeps the real number for Redis/agent calls.
	if procCtx.UserNumber != phone || procCtx.UserID != phone {
		t.Fatalf("processing context must keep the raw number, got %q/%q", procCtx.UserNumber, procCtx.UserID)
	}

	procCtx.Logger.Info("processing")

	entry := hook.LastEntry()
	if entry == nil {
		t.Fatal("expected a log entry")
	}
	if got := entry.Data["user_number"]; got != "5521900***" {
		t.Fatalf("expected masked user_number, got %v", got)
	}
	for k, v := range entry.Data {
		if s, ok := v.(string); ok && strings.Contains(s, phone) {
			t.Errorf("log field %q leaks the phone number", k)
		}
	}
}
