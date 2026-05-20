package handlers

import (
	"testing"
)

func TestExtractAgentMedia_StringJSONContent(t *testing.T) {
	data := map[string]interface{}{
		"messages": []interface{}{
			map[string]interface{}{
				"message_type": "tool_return_message",
				"name":         "send_whatsapp_media",
				"content":      `{"status":"ok","type":"image","url":"https://e/x.jpg","caption":"foto"}`,
			},
		},
	}
	env := ExtractAgentMedia(data)
	if env.Type != "image" {
		t.Errorf("expected type=image, got %q", env.Type)
	}
	if env.URL != "https://e/x.jpg" {
		t.Errorf("expected url, got %q", env.URL)
	}
	if env.Caption != "foto" {
		t.Errorf("expected caption, got %q", env.Caption)
	}
}

func TestExtractAgentMedia_ObjectContent(t *testing.T) {
	data := map[string]interface{}{
		"messages": []interface{}{
			map[string]interface{}{
				"message_type": "tool_return_message",
				"tool_name":    "send_whatsapp_media",
				"content": map[string]interface{}{
					"status": "ok", "type": "audio", "url": "https://e/a.ogg",
				},
			},
		},
	}
	env := ExtractAgentMedia(data)
	if env.Type != "audio" || env.URL != "https://e/a.ogg" {
		t.Errorf("got %+v", env)
	}
}

func TestExtractAgentMedia_GenerateAudioResponse(t *testing.T) {
	data := map[string]interface{}{
		"messages": []interface{}{
			map[string]interface{}{
				"message_type": "tool_return_message",
				"name":         "generate_audio_response",
				"content":      `{"status":"ok","audio_base64":"AAAA","mime_type":"audio/ogg"}`,
			},
		},
	}
	env := ExtractAgentMedia(data)
	if env.Type != "audio" || env.Base64 != "AAAA" || env.MimeType != "audio/ogg" {
		t.Errorf("got %+v", env)
	}
}

func TestExtractAgentMedia_LastToolReturnWins(t *testing.T) {
	// Múltiplos tool returns — o último é a intenção corrente.
	data := map[string]interface{}{
		"messages": []interface{}{
			map[string]interface{}{
				"message_type": "tool_return_message", "name": "send_whatsapp_media",
				"content": `{"type":"image","url":"first"}`,
			},
			map[string]interface{}{
				"message_type": "tool_return_message", "name": "send_whatsapp_media",
				"content": `{"type":"image","url":"last"}`,
			},
		},
	}
	env := ExtractAgentMedia(data)
	if env.URL != "last" {
		t.Errorf("expected last URL, got %q", env.URL)
	}
}

func TestExtractAgentMedia_StatusErrorSkipped(t *testing.T) {
	// status != ok faz envelope ficar inválido — caller cai pra texto.
	data := map[string]interface{}{
		"messages": []interface{}{
			map[string]interface{}{
				"message_type": "tool_return_message", "name": "send_whatsapp_media",
				"content": `{"status":"error","type":"image"}`,
			},
		},
	}
	env := ExtractAgentMedia(data)
	if env.Type != "" {
		t.Errorf("expected empty envelope, got %+v", env)
	}
}

func TestExtractAgentMedia_NoToolReturn(t *testing.T) {
	data := map[string]interface{}{
		"messages": []interface{}{
			map[string]interface{}{"content": "Olá!", "role": "ai"},
		},
	}
	env := ExtractAgentMedia(data)
	if env.Type != "" {
		t.Errorf("expected empty envelope, got %+v", env)
	}
}

func TestExtractAgentMedia_NilSafe(t *testing.T) {
	if env := ExtractAgentMedia(nil); env.Type != "" {
		t.Errorf("expected empty on nil, got %+v", env)
	}
}

func TestExtractAgentMedia_Location(t *testing.T) {
	data := map[string]interface{}{
		"messages": []interface{}{
			map[string]interface{}{
				"message_type": "tool_return_message", "name": "send_whatsapp_media",
				"content": `{"status":"ok","type":"location","latitude":-22.9,"longitude":-43.2,"name":"Praça"}`,
			},
		},
	}
	env := ExtractAgentMedia(data)
	if env.Type != "location" {
		t.Fatalf("expected location, got %q", env.Type)
	}
	if env.Latitude == nil || *env.Latitude != -22.9 {
		t.Errorf("expected lat=-22.9, got %v", env.Latitude)
	}
}

func TestExtractAgentMedia_Interactive(t *testing.T) {
	data := map[string]interface{}{
		"messages": []interface{}{
			map[string]interface{}{
				"message_type": "tool_return_message", "name": "send_whatsapp_buttons",
				"content": map[string]interface{}{
					"status": "ok", "type": "interactive",
					"interactive": map[string]interface{}{
						"subtype": "button",
						"action":  map[string]interface{}{"buttons": []interface{}{}},
					},
				},
			},
		},
	}
	env := ExtractAgentMedia(data)
	if env.Type != "interactive" || env.Interactive == nil || env.Interactive.Subtype != "button" {
		t.Errorf("got %+v", env)
	}
}

// ─── FlowRegistry tests ────────────────────────────────────────────────────

func TestFlowRegistry_SubstringMatch(t *testing.T) {
	r := NewFlowRegistry("luminaria:reparo_luminaria;saude:agenda_saude", "default")
	cases := []struct {
		flow string
		want string
	}{
		// Match é case-insensitive + substring + diacritic-insensitive.
		{"Luminária Quebrada", "reparo_luminaria"},
		{"LUMINARIA Quebrada", "reparo_luminaria"},
		{"luminaria", "reparo_luminaria"},
		{"Saúde Agendamento", "agenda_saude"},
		{"saude agendamento", "agenda_saude"},
		{"unknown", "default"},
		{"", "default"},
	}
	for _, tc := range cases {
		if got := r.Resolve(tc.flow); got != tc.want {
			t.Errorf("Resolve(%q): got %q, want %q", tc.flow, got, tc.want)
		}
	}
}

func TestFlowRegistry_EmptyConfig(t *testing.T) {
	r := NewFlowRegistry("", "default")
	if got := r.Resolve("anything"); got != "default" {
		t.Errorf("expected default, got %q", got)
	}
}

func TestFlowRegistry_MalformedEntriesIgnored(t *testing.T) {
	r := NewFlowRegistry("malformed;ok:service;:noflow;flow:", "")
	if got := r.Resolve("ok"); got != "service" {
		t.Errorf("expected service, got %q", got)
	}
	if got := r.Resolve("noflow"); got != "" {
		t.Errorf("expected empty default, got %q", got)
	}
}

func TestParseFlowResponse(t *testing.T) {
	parsed := ParseFlowResponse(`{"endereco":"x","numero":42}`)
	if parsed["endereco"] != "x" {
		t.Errorf("got %v", parsed)
	}
	if ParseFlowResponse("") != nil {
		t.Error("expected nil on empty")
	}
	if ParseFlowResponse("not json") != nil {
		t.Error("expected nil on invalid")
	}
}
