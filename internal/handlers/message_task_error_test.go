package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"

	"github.com/prefeitura-rio/app-eai-agent-gateway/internal/config"
	"github.com/prefeitura-rio/app-eai-agent-gateway/internal/models"
)

// Dados sintéticos (LGPD): nada aqui é de cidadão real.
const (
	taskErrorMessageID   = "123e4567-e89b-12d3-a456-426614174000"
	taskErrorCitizenText = "meu CPF e 000.000.000-00 e moro na Rua Ficticia 1"
	taskErrorAudioURL    = "https://cdn.example.invalid/whatsapp/5521900000000/audio-abc.ogg?sig=ASSINATURA123"
)

// failedTaskRedis answers like Redis for a task the worker marked as failed,
// with taskError as the stored task:error value.
type failedTaskRedis struct {
	stubRedis
	taskError string
}

func (s *failedTaskRedis) GetTaskStatus(context.Context, string) (string, error) {
	return string(models.TaskStatusFailed), nil
}

func (s *failedTaskRedis) Get(_ context.Context, key string) (string, error) {
	if key == "task:error:"+taskErrorMessageID {
		return s.taskError, nil
	}
	return "", errors.New("redis: nil")
}

type connectedRabbitMQ struct{}

func (connectedRabbitMQ) PublishMessage(context.Context, string, interface{}) error { return nil }
func (connectedRabbitMQ) PublishMessageWithHeaders(context.Context, string, interface{}, map[string]interface{}) error {
	return nil
}
func (connectedRabbitMQ) IsConnected() bool { return true }

func newTaskErrorHandler(taskError string) *MessageHandler {
	logger := logrus.New()
	logger.SetOutput(io.Discard)
	return NewMessageHandler(logger, &config.Config{}, &failedTaskRedis{taskError: taskError}, connectedRabbitMQ{}, nil, nil)
}

// rawTaskErrors are task:error values as the worker wrote them before DEV-453
// (err.Error()): they may still be in Redis until the TTL expires.
func rawTaskErrors() map[string]string {
	body := fmt.Sprintf(`{"error":{"code":400,"message":"Reasoning Engine Execution failed. Error Details: %s","status":"FAILED_PRECONDITION"}}`, taskErrorCitizenText)
	return map[string]string{
		"agent engine body": "failed to get AI response: failed to post query: non-2xx response: 400 - " + body,
		"agent engine raw":  "failed to parse response: invalid character 'x' (raw: " + taskErrorCitizenText + ")",
		"media url":         fmt.Sprintf("audio transcription failed: failed to download file: Get %q: dial tcp: connection refused", taskErrorAudioURL),
	}
}

var leakedFragments = []string{
	taskErrorCitizenText, "000.000.000-00", "Error Details", "Rua Ficticia",
	taskErrorAudioURL, "5521900000000", "audio-abc", "ASSINATURA123", "/whatsapp/",
}

func assertNoLeak(t *testing.T, where, text string) {
	t.Helper()
	for _, f := range leakedFragments {
		if strings.Contains(text, f) {
			t.Fatalf("%s expõe %q: %s", where, f, text)
		}
	}
}

func TestHandleMessageResponse_FailedTaskErrorHasNoRemoteBodyOrMediaURL(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for name, raw := range rawTaskErrors() {
		t.Run(name, func(t *testing.T) {
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/message/response?message_id="+taskErrorMessageID, nil)

			newTaskErrorHandler(raw).HandleMessageResponse(c)

			if w.Code != http.StatusOK {
				t.Fatalf("esperado 200, veio %d: %s", w.Code, w.Body.String())
			}
			assertNoLeak(t, "GET /message/response", w.Body.String())

			var resp models.MessageResponse
			if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
				t.Fatalf("resposta não é JSON: %v", err)
			}
			if resp.Status != string(models.TaskStatusFailed) || resp.Error == nil || *resp.Error == "" {
				t.Fatalf("a tarefa falhou e o campo error (string) deve continuar presente: %+v", resp)
			}
		})
	}
}

func TestHandleDebugTaskStatus_LastErrorHasNoRemoteBodyOrMediaURL(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for name, raw := range rawTaskErrors() {
		t.Run(name, func(t *testing.T) {
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/message/debug/task-status?message_id="+taskErrorMessageID, nil)

			newTaskErrorHandler(raw).HandleDebugTaskStatus(c)

			if w.Code != http.StatusOK {
				t.Fatalf("esperado 200, veio %d: %s", w.Code, w.Body.String())
			}
			assertNoLeak(t, "GET /message/debug/task-status", w.Body.String())

			var info models.TaskDebugInfo
			if err := json.Unmarshal(w.Body.Bytes(), &info); err != nil {
				t.Fatalf("resposta não é JSON: %v", err)
			}
			if info.LastError == nil || *info.LastError == "" {
				t.Fatalf("last_error deve continuar presente: %s", w.Body.String())
			}
		})
	}
}

// TestHandleMessageResponse_TaskErrorKeepsClassification mostra o que o
// chamador ainda recebe: o status HTTP, o enum do Google e o host da mídia.
func TestHandleMessageResponse_TaskErrorKeepsClassification(t *testing.T) {
	gin.SetMode(gin.TestMode)
	raws := rawTaskErrors()
	cases := map[string]string{
		"agent engine body": "failed to get AI response: failed to post query: non-2xx response: 400 (FAILED_PRECONDITION)",
		"agent engine raw":  "failed to parse response: invalid character 'x'",
		"media url":         `audio transcription failed: failed to download file: Get "https://cdn.example.invalid/[omitido]": dial tcp: connection refused`,
	}
	for name, want := range cases {
		t.Run(name, func(t *testing.T) {
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/message/response?message_id="+taskErrorMessageID, nil)

			newTaskErrorHandler(raws[name]).HandleMessageResponse(c)

			var resp models.MessageResponse
			if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
				t.Fatalf("resposta não é JSON: %v", err)
			}
			if resp.Error == nil || *resp.Error != want {
				t.Fatalf("error = %v, esperado %q", resp.Error, want)
			}
		})
	}
}

// TestHandleMessageResponse_FriendlyTaskErrorUnchanged garante que a mensagem
// amigável que o worker legado grava (MessageFormatter) chega igual.
func TestHandleMessageResponse_FriendlyTaskErrorUnchanged(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const friendly = "I couldn't process your audio message. Please try recording it again or send a text message."
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/message/response?message_id="+taskErrorMessageID, nil)

	newTaskErrorHandler(friendly).HandleMessageResponse(c)

	var resp models.MessageResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("resposta não é JSON: %v", err)
	}
	if resp.Error == nil || *resp.Error != friendly {
		t.Fatalf("error = %v, esperado %q", resp.Error, friendly)
	}
}
