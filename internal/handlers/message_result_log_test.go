package handlers

import (
	"context"
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

// completedTaskRedis answers like Redis for a task the worker marked as
// completed, with result as the stored task result.
type completedTaskRedis struct {
	stubRedis
	result string
}

func (s *completedTaskRedis) GetTaskStatus(context.Context, string) (string, error) {
	return string(models.TaskStatusCompleted), nil
}

func (s *completedTaskRedis) GetTaskResult(_ context.Context, _ string, dest interface{}) error {
	*(dest.(*string)) = s.result
	return nil
}

// TestHandleMessageResponse_UnparsableResultLogsOnlyMetadata prova que, quando
// o resultado da tarefa não é JSON, o log do gateway não carrega o texto do
// resultado (a resposta do agente pode repetir a conversa do cidadão), só o
// tamanho e o tipo do erro (DEV-721). A resposta da API segue a mesma.
func TestHandleMessageResponse_UnparsableResultLogsOnlyMetadata(t *testing.T) {
	gin.SetMode(gin.TestMode)

	results := map[string]string{
		"texto puro":     "Resposta do agente: " + taskErrorCitizenText,
		"json truncado":  `{"messages":[{"content":"` + taskErrorCitizenText,
		"tipo diferente": `["` + taskErrorCitizenText + `"]`,
	}
	wantKind := map[string]string{
		"texto puro":     "syntax",
		"json truncado":  "unexpected_end",
		"tipo diferente": "type_mismatch",
	}

	for name, result := range results {
		t.Run(name, func(t *testing.T) {
			logger, hook := logtest.NewNullLogger()
			logger.SetLevel(logrus.DebugLevel)
			h := NewMessageHandler(logger, &config.Config{}, &completedTaskRedis{result: result}, connectedRabbitMQ{}, nil, nil)

			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/message/response?message_id="+taskErrorMessageID, nil)

			h.HandleMessageResponse(c)

			if w.Code != http.StatusInternalServerError {
				t.Fatalf("esperado 500, veio %d: %s", w.Code, w.Body.String())
			}
			assertNoLeak(t, "resposta HTTP", w.Body.String())

			found := false
			for _, entry := range hook.AllEntries() {
				texts := []string{entry.Message}
				for key, value := range entry.Data {
					texts = append(texts, key+"="+fmt.Sprint(value))
				}
				for _, text := range texts {
					assertNoLeak(t, "log "+entry.Message, text)
					if strings.Contains(text, "Rua Ficticia") || strings.Contains(text, "Resposta do agente") {
						t.Fatalf("log %q vazou o resultado: %s", entry.Message, text)
					}
				}
				if entry.Message == "Failed to parse processed result from worker" {
					found = true
					if got := entry.Data["result_length"]; got != len(result) {
						t.Fatalf("result_length = %v, esperado %d", got, len(result))
					}
					if got := entry.Data["error_type"]; got != wantKind[name] {
						t.Fatalf("error_type = %v, esperado %q", got, wantKind[name])
					}
					if _, ok := entry.Data["raw_result"]; ok {
						t.Fatalf("raw_result não deve ser logado")
					}
				}
			}
			if !found {
				t.Fatalf("log de falha de parse ausente")
			}
		})
	}
}
