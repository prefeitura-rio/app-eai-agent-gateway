package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"

	"github.com/prefeitura-rio/app-eai-agent-gateway/internal/config"
	"github.com/prefeitura-rio/app-eai-agent-gateway/internal/models"
)

// stubGoogleAgent answers like the Google Agent Engine service: threadErr and
// sendErr are returned by GetOrCreateThread and SendHistoryUpdate, resp is the
// map SendHistoryUpdate returns on success.
type stubGoogleAgent struct {
	threadErr error
	sendErr   error
	resp      map[string]interface{}
}

func (s *stubGoogleAgent) GetOrCreateThread(context.Context, string) (string, error) {
	if s.threadErr != nil {
		return "", s.threadErr
	}
	return "thread-sintetico", nil
}

func (s *stubGoogleAgent) SendHistoryUpdate(context.Context, string, []models.HistoryMessage, *string) (map[string]interface{}, error) {
	if s.sendErr != nil {
		return nil, s.sendErr
	}
	return s.resp, nil
}

// agentEngineBodyError is the error SendHistoryUpdate wraps when the Agent
// Engine answers non-2xx: postQuery embeds the remote body, which may repeat
// the citizen's conversation.
func agentEngineBodyError() error {
	body := fmt.Sprintf(`{"error":{"code":400,"message":"Reasoning Engine Execution failed. Error Details: %s","status":"FAILED_PRECONDITION"}}`, taskErrorCitizenText)
	return fmt.Errorf("failed to send history update: failed to post query: %w",
		errors.New("non-2xx response: 400 - "+body))
}

func runHistoryUpdate(t *testing.T, agent *stubGoogleAgent) (*httptest.ResponseRecorder, *logtest.Hook) {
	t.Helper()
	gin.SetMode(gin.TestMode)

	logger, hook := logtest.NewNullLogger()
	logger.SetLevel(logrus.DebugLevel)
	h := NewMessageHandler(logger, &config.Config{}, &stubRedis{}, connectedRabbitMQ{}, agent, nil)

	payload := `{"user_number":"5521900000000","messages":[{"role":"human","content":"` + taskErrorCitizenText + `"}]}`
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/message/webhook/update_history", strings.NewReader(payload))
	c.Request.Header.Set("Content-Type", "application/json")

	h.HandleHistoryUpdateWebhook(c)
	return w, hook
}

// assertLogsNoLeak checks every captured entry, message and fields, for the
// synthetic citizen text and the remote body.
func assertLogsNoLeak(t *testing.T, hook *logtest.Hook) {
	t.Helper()
	for _, entry := range hook.AllEntries() {
		texts := []string{entry.Message}
		for key, value := range entry.Data {
			texts = append(texts, key+"="+fmt.Sprint(value))
		}
		for _, text := range texts {
			assertNoLeak(t, "log "+entry.Message, text)
			if strings.Contains(text, "Reasoning Engine Execution failed") {
				t.Fatalf("log %q vazou o corpo remoto: %s", entry.Message, text)
			}
		}
	}
}

func findEntry(t *testing.T, hook *logtest.Hook, message string) *logrus.Entry {
	t.Helper()
	for _, entry := range hook.AllEntries() {
		if entry.Message == message {
			return entry
		}
	}
	t.Fatalf("log %q ausente", message)
	return nil
}

// TestHandleHistoryUpdateWebhook_ErrorsOmitRemoteBody prova que, quando o
// Agent Engine falha, nem o log nem a resposta 500 carregam o corpo remoto
// (DEV-733): o log leva o resumo de pii.ErrorSummary, com o status HTTP e o
// enum do Google, e a resposta leva uma mensagem fixa.
func TestHandleHistoryUpdateWebhook_ErrorsOmitRemoteBody(t *testing.T) {
	cases := map[string]struct {
		agent      *stubGoogleAgent
		logMessage string
	}{
		"thread": {
			agent:      &stubGoogleAgent{threadErr: agentEngineBodyError()},
			logMessage: "Failed to get or create thread",
		},
		"history update": {
			agent:      &stubGoogleAgent{sendErr: agentEngineBodyError()},
			logMessage: "Failed to send history update",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			w, hook := runHistoryUpdate(t, tc.agent)

			if w.Code != http.StatusInternalServerError {
				t.Fatalf("esperado 500, veio %d: %s", w.Code, w.Body.String())
			}
			body := w.Body.String()
			assertNoLeak(t, "resposta HTTP", body)
			for _, fragment := range []string{"Reasoning Engine Execution failed", "non-2xx response", "FAILED_PRECONDITION"} {
				if strings.Contains(body, fragment) {
					t.Fatalf("resposta HTTP expõe %q: %s", fragment, body)
				}
			}
			var parsed map[string]interface{}
			if err := json.Unmarshal(w.Body.Bytes(), &parsed); err != nil {
				t.Fatalf("resposta não é JSON: %v", err)
			}
			if parsed["error"] == nil || parsed["message"] == nil {
				t.Fatalf("resposta sem error/message: %s", body)
			}

			assertLogsNoLeak(t, hook)
			entry := findEntry(t, hook, tc.logMessage)
			summary, _ := entry.Data[logrus.ErrorKey].(string)
			if !strings.Contains(summary, "non-2xx response: 400") || !strings.Contains(summary, "FAILED_PRECONDITION") {
				t.Fatalf("resumo do erro sem status: %q", summary)
			}
		})
	}
}

// TestHandleHistoryUpdateWebhook_SuccessLogsOnlyMetadata prova que o log Info
// de sucesso grava só a quantidade de campos e o tamanho da resposta do Agent
// Engine, não o conteúdo (DEV-733). A resposta 200 segue sendo o mapa
// devolvido pelo Agent Engine.
func TestHandleHistoryUpdateWebhook_SuccessLogsOnlyMetadata(t *testing.T) {
	resp := map[string]interface{}{
		"status":   "ok",
		"messages": []interface{}{map[string]interface{}{"role": "human", "content": taskErrorCitizenText}},
	}
	w, hook := runHistoryUpdate(t, &stubGoogleAgent{resp: resp})

	if w.Code != http.StatusOK {
		t.Fatalf("esperado 200, veio %d: %s", w.Code, w.Body.String())
	}
	var parsed map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &parsed); err != nil {
		t.Fatalf("resposta não é JSON: %v", err)
	}
	if parsed["status"] != "ok" || parsed["messages"] == nil {
		t.Fatalf("resposta 200 mudou: %s", w.Body.String())
	}

	assertLogsNoLeak(t, hook)
	entry := findEntry(t, hook, "History update processed successfully")
	if _, ok := entry.Data["response"]; ok {
		t.Fatalf("response não deve ser logado")
	}
	if got := entry.Data["response_fields"]; got != len(resp) {
		t.Fatalf("response_fields = %v, esperado %d", got, len(resp))
	}
	encoded, _ := json.Marshal(resp)
	if got := entry.Data["response_size"]; got != len(encoded) {
		t.Fatalf("response_size = %v, esperado %d", got, len(encoded))
	}
}
