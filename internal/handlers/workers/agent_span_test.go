package workers

import (
	"strings"
	"testing"

	"go.opentelemetry.io/otel/attribute"

	"github.com/prefeitura-rio/app-eai-agent-gateway/internal/models"
)

// TestAgentEngineCallSpanAttributes_NoMessageContent garante que o span
// google_agent_engine_call nao exporta o texto enviado ao agente (DEV-446):
// nem a mensagem livre do cidadao nem o envelope [INBOUND_MEDIA], que carrega
// o telefone sem mascara.
func TestAgentEngineCallSpanAttributes_NoMessageContent(t *testing.T) {
	const (
		threadID  = "thread-sintetica-123"
		phone     = "5521999990000"
		userText  = "meu CPF e 000.000.000-00 e moro na Rua Ficticia 1"
		mediaLink = "https://example.invalid/midia/abc.jpg"
	)

	tests := []struct {
		name      string
		message   string
		forbidden []string
	}{
		{
			name:      "texto livre",
			message:   userText,
			forbidden: []string{userText, "000.000.000-00", "Rua Ficticia"},
		},
		{
			name:      "envelope de midia",
			message:   models.EnrichMediaContent("image", phone, map[string]interface{}{"url": mediaLink}, userText),
			forbidden: []string{phone, userText, mediaLink, "[INBOUND_MEDIA]"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			attrs := agentEngineCallSpanAttributes(threadID, tt.message)

			got := map[attribute.Key]attribute.Value{}
			for _, kv := range attrs {
				got[kv.Key] = kv.Value
			}

			if _, ok := got["message.content"]; ok {
				t.Fatalf("span nao pode ter o atributo message.content")
			}
			for key, value := range got {
				for _, f := range tt.forbidden {
					if strings.Contains(value.Emit(), f) {
						t.Fatalf("atributo %q expoe conteudo da mensagem (%q)", key, f)
					}
				}
			}

			if v, ok := got["message.length"]; !ok || v.AsInt64() != int64(len(tt.message)) {
				t.Fatalf("message.length = %v, esperado %d", v.Emit(), len(tt.message))
			}
			if v, ok := got["thread.id"]; !ok || v.AsString() != threadID {
				t.Fatalf("thread.id = %v, esperado %q", v.Emit(), threadID)
			}
		})
	}
}
