// Package handlers — Envelope canônico Meta outbound, extraído da resposta
// do Engine.
//
// Espelha o contrato Mule (`agentMedia` em `webhook-flow.xml`):
//
//	type      "audio"|"image"|"video"|"document"|"sticker"
//	          |"location"|"contacts"|"interactive"|"template"|"reaction"
//	base64    string opcional (mídia inline para upload em /media)
//	url       string opcional (link direto, bypassa upload)
//	mimeType  "audio/ogg", "image/jpeg", ...
//	filename  opcional, só para document
//	caption   opcional, image/video/document
//	voice     bool, só para audio
//	latitude/longitude/name/address  para location
//	interactive  objeto, para interactive (button/list/flow)
//	template     objeto, para template
//	reaction_to_message_id + emoji  para reaction
//
// Engine emite isso via MCP tools:
//
//	send_whatsapp_media       → envelope canonical
//	generate_audio_response   → audio shape compatível
//	send_whatsapp_flow / _buttons / _list → interactive
//
// Tool returns aparecem em `data.messages[]` como `message_type ==
// "tool_return_message"` com `name`/`tool_name` igualando o tool e
// `content` JSON (string ou objeto).
package handlers

import (
	"encoding/json"
)

// AgentMediaEnvelope é o shape canônico que o dispatcher consome para rotear
// outbound. Vazio (`Type==""`) significa "sem media; usar SendText do reply".
type AgentMediaEnvelope struct {
	Type     string `json:"type"`
	Base64   string `json:"base64,omitempty"`
	URL      string `json:"url,omitempty"`
	MimeType string `json:"mime_type,omitempty"`
	Filename string `json:"filename,omitempty"`
	Caption  string `json:"caption,omitempty"`
	Voice    bool   `json:"voice,omitempty"`

	// location
	Latitude  *float64 `json:"latitude,omitempty"`
	Longitude *float64 `json:"longitude,omitempty"`
	Name      string   `json:"name,omitempty"`
	Address   string   `json:"address,omitempty"`

	// interactive (button/list/flow): action obrigatório, demais opcionais
	Interactive *InteractiveEnvelope `json:"interactive,omitempty"`

	// template (proativo fora janela 24h)
	Template *TemplateEnvelope `json:"template,omitempty"`

	// reaction
	ReactionToMessageID string `json:"reaction_to_message_id,omitempty"`
	Emoji               string `json:"emoji,omitempty"`
}

// InteractiveEnvelope encapsula button/list/flow payload do Engine.
type InteractiveEnvelope struct {
	Subtype string                 `json:"subtype"` // "button" | "list" | "flow"
	Header  map[string]interface{} `json:"header,omitempty"`
	Body    map[string]interface{} `json:"body,omitempty"`
	Footer  map[string]interface{} `json:"footer,omitempty"`
	Action  map[string]interface{} `json:"action"`
}

// TemplateEnvelope encapsula template aprovado pelo Meta.
type TemplateEnvelope struct {
	Name       string                   `json:"name"`
	LangCode   string                   `json:"language_code"`
	Components []map[string]interface{} `json:"components,omitempty"`
}

// ExtractAgentMedia procura pelo último tool_return_message no array de
// messages cujo name/tool_name seja "send_whatsapp_media" ou
// "generate_audio_response", e parseia o content. Retorna envelope vazio
// (Type=="") quando não encontra ou parsing falha — caller cai pra SendText.
//
// Tolera content em qualquer um dos formatos:
//   - string JSON: `"{\"type\":\"image\",\"url\":\"...\"}"`
//   - objeto direto: `{"type":"image","url":"..."}`
//   - objeto com text dentro: `{"text":"{\"type\":\"image\"...}"}` (legacy)
//
// Match no Mule `route-langgraph-response-flow:toolReturnByName`.
func ExtractAgentMedia(data map[string]interface{}) AgentMediaEnvelope {
	if data == nil {
		return AgentMediaEnvelope{}
	}
	rawMessages, ok := data["messages"]
	if !ok {
		return AgentMediaEnvelope{}
	}
	list, ok := rawMessages.([]interface{})
	if !ok {
		return AgentMediaEnvelope{}
	}

	// Scan reverso pra pegar o último — Engine pode emitir múltiplos
	// tool_return_message numa só resposta; o último é a intenção atual.
	for i := len(list) - 1; i >= 0; i-- {
		m, ok := list[i].(map[string]interface{})
		if !ok {
			continue
		}
		mtype, _ := m["message_type"].(string)
		if mtype != "tool_return_message" {
			continue
		}
		name, _ := m["name"].(string)
		if name == "" {
			name, _ = m["tool_name"].(string)
		}

		switch name {
		case "send_whatsapp_media", "send_whatsapp_flow", "send_whatsapp_buttons", "send_whatsapp_list":
			if env, ok := parseCanonical(m["content"]); ok {
				return env
			}
		case "generate_audio_response":
			if env, ok := parseAudioResponse(m["content"]); ok {
				return env
			}
		}
	}
	return AgentMediaEnvelope{}
}

// parseCanonical: envelope com `type` no top-level. Aceita string-encoded
// JSON ou objeto direto.
func parseCanonical(content interface{}) (AgentMediaEnvelope, bool) {
	obj := normalizeContent(content)
	if obj == nil {
		return AgentMediaEnvelope{}, false
	}
	status, _ := obj["status"].(string)
	if status != "" && status != "ok" {
		return AgentMediaEnvelope{}, false
	}
	t, _ := obj["type"].(string)
	if t == "" {
		return AgentMediaEnvelope{}, false
	}
	env := AgentMediaEnvelope{
		Type:                t,
		Base64:              stringField(obj, "base64"),
		URL:                 stringField(obj, "url"),
		MimeType:            firstNonEmpty(stringField(obj, "mime_type"), stringField(obj, "mimeType")),
		Filename:            stringField(obj, "filename"),
		Caption:             stringField(obj, "caption"),
		Voice:               boolField(obj, "voice"),
		Name:                stringField(obj, "name"),
		Address:             stringField(obj, "address"),
		ReactionToMessageID: stringField(obj, "reaction_to_message_id"),
		Emoji:               stringField(obj, "emoji"),
	}
	if lat, ok := floatField(obj, "latitude"); ok {
		env.Latitude = &lat
	}
	if lng, ok := floatField(obj, "longitude"); ok {
		env.Longitude = &lng
	}
	if interactiveRaw, ok := obj["interactive"].(map[string]interface{}); ok {
		env.Interactive = parseInteractive(interactiveRaw)
	}
	if templateRaw, ok := obj["template"].(map[string]interface{}); ok {
		env.Template = parseTemplate(templateRaw)
	}
	return env, true
}

// parseAudioResponse: shape específico do tool `generate_audio_response`,
// mapeia pra envelope com Type="audio".
func parseAudioResponse(content interface{}) (AgentMediaEnvelope, bool) {
	obj := normalizeContent(content)
	if obj == nil {
		return AgentMediaEnvelope{}, false
	}
	if status, _ := obj["status"].(string); status != "" && status != "ok" {
		return AgentMediaEnvelope{}, false
	}
	b64 := stringField(obj, "audio_base64")
	if b64 == "" {
		return AgentMediaEnvelope{}, false
	}
	mime := stringField(obj, "mime_type")
	if mime == "" {
		mime = "audio/ogg"
	}
	return AgentMediaEnvelope{
		Type:     "audio",
		Base64:   b64,
		MimeType: mime,
	}, true
}

// normalizeContent aceita string (JSON encoded) ou map. Retorna map ou nil.
// Suporta o legacy wrapper `{"text": "<json>"}` que o Engine às vezes emite.
func normalizeContent(content interface{}) map[string]interface{} {
	switch v := content.(type) {
	case nil:
		return nil
	case map[string]interface{}:
		// Se tem `text` string com JSON dentro, tenta parsear (legacy).
		if t, ok := v["text"].(string); ok && t != "" {
			if inner := parseJSONString(t); inner != nil {
				return inner
			}
		}
		return v
	case string:
		return parseJSONString(v)
	}
	return nil
}

func parseJSONString(s string) map[string]interface{} {
	if s == "" {
		return nil
	}
	var m map[string]interface{}
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		return nil
	}
	return m
}

func parseInteractive(raw map[string]interface{}) *InteractiveEnvelope {
	subtype, _ := raw["subtype"].(string)
	if subtype == "" {
		// Tools antigos emitem `type` em vez de `subtype`.
		subtype, _ = raw["type"].(string)
	}
	if subtype == "" {
		return nil
	}
	action, _ := raw["action"].(map[string]interface{})
	if action == nil {
		return nil
	}
	env := &InteractiveEnvelope{Subtype: subtype, Action: action}
	env.Header, _ = raw["header"].(map[string]interface{})
	env.Body, _ = raw["body"].(map[string]interface{})
	env.Footer, _ = raw["footer"].(map[string]interface{})
	return env
}

func parseTemplate(raw map[string]interface{}) *TemplateEnvelope {
	name, _ := raw["name"].(string)
	if name == "" {
		return nil
	}
	langCode, _ := raw["language_code"].(string)
	if langCode == "" {
		// Algumas variantes emitem {"language": {"code": "pt_BR"}} (Meta nativo).
		if lang, ok := raw["language"].(map[string]interface{}); ok {
			langCode, _ = lang["code"].(string)
		}
	}
	if langCode == "" {
		return nil
	}
	env := &TemplateEnvelope{Name: name, LangCode: langCode}
	if compsRaw, ok := raw["components"].([]interface{}); ok {
		for _, c := range compsRaw {
			if m, ok := c.(map[string]interface{}); ok {
				env.Components = append(env.Components, m)
			}
		}
	}
	return env
}

// Helpers tolerantes a tipo.

func stringField(m map[string]interface{}, key string) string {
	v, _ := m[key].(string)
	return v
}

func boolField(m map[string]interface{}, key string) bool {
	v, _ := m[key].(bool)
	return v
}

func floatField(m map[string]interface{}, key string) (float64, bool) {
	switch v := m[key].(type) {
	case float64:
		return v, true
	case float32:
		return float64(v), true
	case int:
		return float64(v), true
	case int64:
		return float64(v), true
	}
	return 0, false
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
