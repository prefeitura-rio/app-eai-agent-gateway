package workers

import (
	"fmt"
	"strings"
	"testing"

	"github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"
)

// Dados sintéticos (LGPD): nada aqui é de cidadão real.
const agentResponseCitizenText = "meu CPF e 000.000.000-00 e moro na Rua Ficticia 1"

// TestParseAgentEngineContent_PlainTextLogsOnlyMetadata prova que a resposta
// do Agent Engine que não é JSON vira a mensagem de texto do fallback, mas o
// log registra só o tamanho e o tipo do erro, sem o texto (DEV-721).
func TestParseAgentEngineContent_PlainTextLogsOnlyMetadata(t *testing.T) {
	contents := map[string]string{
		"texto puro":    "Resposta do agente: " + agentResponseCitizenText,
		"com BOM":       "\xEF\xBB\xBFResposta do agente: " + agentResponseCitizenText,
		"json truncado": `{"output":{"messages":[{"content":"` + agentResponseCitizenText,
		"texto curto":   "Rua Ficticia",
	}
	wantKind := map[string]string{
		"texto puro":    "syntax",
		"com BOM":       "syntax",
		"json truncado": "unexpected_end",
		"texto curto":   "syntax",
	}

	for name, content := range contents {
		t.Run(name, func(t *testing.T) {
			logger, hook := logtest.NewNullLogger()
			logger.SetLevel(logrus.DebugLevel)

			parsed, plainText := parseAgentEngineContent(logrus.NewEntry(logger), content)
			if !plainText {
				t.Fatalf("conteúdo não JSON deve cair no fallback de texto")
			}

			cleaned := strings.TrimPrefix(content, "\xEF\xBB\xBF")
			messages := parsed["output"].(map[string]interface{})["messages"].([]interface{})
			msg := messages[0].(map[string]interface{})
			if msg["type"] != "ai" || msg["content"] != cleaned {
				t.Fatalf("fallback mudou: %+v", msg)
			}

			found := false
			for _, entry := range hook.AllEntries() {
				texts := []string{entry.Message}
				for key, value := range entry.Data {
					texts = append(texts, key+"="+fmt.Sprint(value))
				}
				for _, text := range texts {
					for _, f := range []string{agentResponseCitizenText, "000.000.000-00", "Rua Ficticia", "Resposta do agente"} {
						if strings.Contains(text, f) {
							t.Fatalf("log %q vazou %q: %s", entry.Message, f, text)
						}
					}
				}
				if entry.Message == "Response is not valid JSON, treating as plain text error message" {
					found = true
					if got := entry.Data["response_length"]; got != len(cleaned) {
						t.Fatalf("response_length = %v, esperado %d", got, len(cleaned))
					}
					if got := entry.Data["error_type"]; got != wantKind[name] {
						t.Fatalf("error_type = %v, esperado %q", got, wantKind[name])
					}
					if _, ok := entry.Data["response_preview"]; ok {
						t.Fatalf("response_preview não deve ser logado")
					}
				}
			}
			if !found {
				t.Fatalf("log de texto puro ausente")
			}
		})
	}
}

// TestParseAgentEngineContent_JSON garante que a resposta JSON (com BOM e
// quebras de linha) continua decodificada sem cair no fallback.
func TestParseAgentEngineContent_JSON(t *testing.T) {
	logger, hook := logtest.NewNullLogger()
	content := "\xEF\xBB\xBF {\"output\":\n{\"messages\":[{\"type\":\"ai\",\"content\":\"ola\"}]}}\r\n"

	parsed, plainText := parseAgentEngineContent(logrus.NewEntry(logger), content)
	if plainText {
		t.Fatalf("JSON válido não deve cair no fallback")
	}
	messages := parsed["output"].(map[string]interface{})["messages"].([]interface{})
	if got := messages[0].(map[string]interface{})["content"]; got != "ola" {
		t.Fatalf("content = %v, esperado ola", got)
	}
	for _, entry := range hook.AllEntries() {
		if entry.Level <= logrus.WarnLevel {
			t.Fatalf("JSON válido não deve gerar aviso: %q", entry.Message)
		}
	}
}
