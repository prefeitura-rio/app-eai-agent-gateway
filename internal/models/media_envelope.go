package models

import (
	"encoding/json"
	"fmt"
)

// EnrichMediaContent wraps a worker `message` string com o prefix
// `[INBOUND_MEDIA] type=<mt> user_number=<phone> media=<json> | user_text=<original>`
// pro LLM downstream (Engine LangGraph) detectar via system prompt e chamar a
// tool MCP `register_inbound_media` com os campos parseados.
//
// Mantido em um único helper pra que os dois worker paths
// (`internal/workers/user_message_worker.go` + `internal/handlers/workers/
// message_handlers.go`) usem o mesmo formato. Se o protocolo mudar (adicionar
// mime_type, trocar separador, etc.), basta atualizar aqui.
//
// `media` nil eh serializado como `null`; map vazio serializa como `{}`.
// Em caso de erro de marshal (improvavel pra map[string]interface{}), o helper
// devolve `null` como conteudo do campo media — preserva o prefix valido pra
// caller continuar processando, mesmo que parcialmente incompleto.
func EnrichMediaContent(messageType, userNumber string, media map[string]interface{}, userText string) string {
	mediaJSON := "null"
	if media != nil {
		if jb, err := json.Marshal(media); err == nil {
			mediaJSON = string(jb)
		}
	}
	return fmt.Sprintf("[INBOUND_MEDIA] type=%s user_number=%s media=%s | user_text=%s",
		messageType, userNumber, mediaJSON, userText)
}
