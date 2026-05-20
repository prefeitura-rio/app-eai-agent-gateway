package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	stdio "io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"

	"github.com/prefeitura-rio/app-eai-agent-gateway/internal/models"
	"github.com/prefeitura-rio/app-eai-agent-gateway/internal/services"
)

// ─── Mocks ───────────────────────────────────────────────────────────────

type mockSenderCall struct {
	kind      string // "text" | "media" | "location" | "template" | "interactive" | "reaction"
	recipient string
	body      string
	mediaType string
	media     services.MediaInput
	lat, lng  float64
	tplName   string
	subtype   string
	emoji     string
	wamidPrev string
}

type mockSender struct {
	mu      sync.Mutex
	calls   []mockSenderCall
	wamid   string
	failErr error
}

func (m *mockSender) record(call mockSenderCall) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, call)
	if m.failErr != nil {
		return "", m.failErr
	}
	if m.wamid == "" {
		return "wamid.OUT", nil
	}
	return m.wamid, nil
}

func (m *mockSender) SendText(_ context.Context, recipient, body string) (string, error) {
	return m.record(mockSenderCall{kind: "text", recipient: recipient, body: body})
}

func (m *mockSender) SendMedia(_ context.Context, recipient, mediaType string, in services.MediaInput) (string, error) {
	return m.record(mockSenderCall{kind: "media", recipient: recipient, mediaType: mediaType, media: in})
}

func (m *mockSender) SendLocation(_ context.Context, recipient string, lat, lng float64, name, address string) (string, error) {
	return m.record(mockSenderCall{kind: "location", recipient: recipient, lat: lat, lng: lng, body: name + "|" + address})
}

func (m *mockSender) SendTemplate(_ context.Context, recipient, name, langCode string, _ []map[string]interface{}) (string, error) {
	return m.record(mockSenderCall{kind: "template", recipient: recipient, tplName: name, body: langCode})
}

func (m *mockSender) SendInteractive(_ context.Context, recipient, subtype string, _, _, _, _ map[string]interface{}) (string, error) {
	return m.record(mockSenderCall{kind: "interactive", recipient: recipient, subtype: subtype})
}

func (m *mockSender) SendReaction(_ context.Context, recipient, wamid, emoji string) (string, error) {
	return m.record(mockSenderCall{kind: "reaction", recipient: recipient, wamidPrev: wamid, emoji: emoji})
}

func (m *mockSender) UploadMedia(_ context.Context, mimeType string, content []byte, _ string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, mockSenderCall{kind: "upload", mediaType: mimeType, body: string(content)})
	if m.failErr != nil {
		return "", m.failErr
	}
	return "uploaded-media-id-1", nil
}

func (m *mockSender) callCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.calls)
}

func (m *mockSender) lastCall() *mockSenderCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.calls) == 0 {
		return nil
	}
	c := m.calls[len(m.calls)-1]
	return &c
}

type mockRedisGet struct {
	mu    sync.Mutex
	store map[string]string
	err   error
}

func (m *mockRedisGet) put(key, val string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.store == nil {
		m.store = map[string]string{}
	}
	m.store[key] = val
}

func (m *mockRedisGet) Get(_ context.Context, key string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return "", m.err
	}
	if v, ok := m.store[key]; ok {
		return v, nil
	}
	// Match real RedisService.Get semantics — missing key retorna sentinel
	// ErrKeyNotFound (wrappado com %w), permitindo handler discriminar miss
	// esperado (404) de Redis down (5xx).
	return "", services.ErrKeyNotFound
}

// Stubs satisfying RedisServiceInterface — só Get matter pra dispatcher.
func (m *mockRedisGet) SetTaskStatus(_ context.Context, _ string, _ string, _ time.Duration) error {
	return nil
}
func (m *mockRedisGet) GetTaskStatus(_ context.Context, _ string) (string, error) { return "", nil }
func (m *mockRedisGet) GetTaskResult(_ context.Context, _ string, _ interface{}) error {
	return nil
}
func (m *mockRedisGet) Set(_ context.Context, _ string, _ string, _ time.Duration) error {
	return nil
}
func (m *mockRedisGet) StoreCallbackURL(_ context.Context, _, _ string, _ time.Duration) error {
	return nil
}
func (m *mockRedisGet) GetCallbackURL(_ context.Context, _ string) (string, error) {
	return "", nil
}
func (m *mockRedisGet) SetUserLastActivity(_ context.Context, _ string, _ time.Time, _ time.Duration) error {
	return nil
}
func (m *mockRedisGet) GetUserLastActivity(_ context.Context, _ string) (*time.Time, error) {
	return nil, nil
}
func (m *mockRedisGet) GetUserLastActivityTTL(_ context.Context, _ string) (time.Duration, error) {
	return 0, nil
}
func (m *mockRedisGet) Ping(_ context.Context) error { return nil }

// ─── Helpers ─────────────────────────────────────────────────────────────

const testDispatchSecret = "test-dispatch-secret"

func newDispatchHandler(t *testing.T, sender MetaSender, redis RedisServiceInterface) *MetaDispatchHandler {
	t.Helper()
	gin.SetMode(gin.TestMode)
	logger := logrus.New()
	logger.SetOutput(stdio.Discard)
	return NewMetaDispatchHandler(sender, redis, testDispatchSecret, logger)
}

func postJSON(t *testing.T, h *MetaDispatchHandler, body interface{}) *httptest.ResponseRecorder {
	t.Helper()
	payload, _ := json.Marshal(body)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/meta/dispatch", bytes.NewReader(payload))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Request.Header.Set("X-Meta-Dispatch-Secret", testDispatchSecret)
	h.HandleDispatch(c)
	return w
}

// ─── Tests ───────────────────────────────────────────────────────────────

func TestDispatch_HappyPath(t *testing.T) {
	sender := &mockSender{}
	redis := &mockRedisGet{}
	redis.put("task:metadata:msg-1", `{"user_number":"5521","provider":"google_agent_engine"}`)
	h := newDispatchHandler(t, sender, redis)

	w := postJSON(t, h, MetaDispatchPayload{
		MessageID: "msg-1",
		Status:    string(models.TaskStatusCompleted),
		Data: map[string]interface{}{
			"messages": []interface{}{
				map[string]interface{}{"content": "Olá cidadão!", "role": "ai"},
			},
		},
	})

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	if sender.callCount() != 1 {
		t.Fatalf("expected 1 SendText call, got %d", sender.callCount())
	}
	if sender.calls[0].recipient != "5521" {
		t.Errorf("expected recipient=5521, got %q", sender.calls[0].recipient)
	}
	if sender.calls[0].body != "Olá cidadão!" {
		t.Errorf("expected body=Olá cidadão!, got %q", sender.calls[0].body)
	}
}

func TestDispatch_NoSenderConfigured(t *testing.T) {
	h := newDispatchHandler(t, nil, &mockRedisGet{})
	w := postJSON(t, h, MetaDispatchPayload{MessageID: "x", Status: "completed"})
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 (no sender), got %d", w.Code)
	}
}

func TestDispatch_NonCompletedStatusSkips(t *testing.T) {
	sender := &mockSender{}
	h := newDispatchHandler(t, sender, &mockRedisGet{})
	w := postJSON(t, h, MetaDispatchPayload{MessageID: "x", Status: "failed"})
	if w.Code != http.StatusOK {
		t.Errorf("expected 200 (noop), got %d", w.Code)
	}
	if sender.callCount() != 0 {
		t.Errorf("expected zero sends (non-completed), got %d", sender.callCount())
	}
}

func TestDispatch_UserNumberNotFound(t *testing.T) {
	sender := &mockSender{}
	rs := &mockRedisGet{} // sem put → retorna redis.Nil
	h := newDispatchHandler(t, sender, rs)
	w := postJSON(t, h, MetaDispatchPayload{MessageID: "missing", Status: "completed"})
	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404 (key missing), got %d", w.Code)
	}
	if sender.callCount() != 0 {
		t.Errorf("expected zero sends, got %d", sender.callCount())
	}
}

func TestDispatch_AlreadySentSkipsResend(t *testing.T) {
	// Codex P2: worker retry após Meta accept devia retornar 200 sem
	// reenviar (WhatsApp send não é idempotente).
	sender := &mockSender{}
	rs := &mockRedisGet{}
	rs.put("meta:dispatch:sent:msg-1", "wamid.PREVIOUS")
	rs.put("task:metadata:msg-1", `{"user_number":"5521"}`)
	h := newDispatchHandler(t, sender, rs)
	w := postJSON(t, h, MetaDispatchPayload{
		MessageID: "msg-1",
		Status:    "completed",
		Data: map[string]interface{}{
			"messages": []interface{}{map[string]interface{}{"content": "x"}},
		},
	})
	if w.Code != http.StatusOK {
		t.Errorf("expected 200 (already_sent), got %d", w.Code)
	}
	if sender.callCount() != 0 {
		t.Errorf("expected zero re-sends (already_sent), got %d", sender.callCount())
	}
}

func TestDispatch_RedisDownReturns503(t *testing.T) {
	// Codex P2: Redis transient down não deveria virar 404 (non-retriable
	// pelo callback_service). 5xx faz worker retentar.
	sender := &mockSender{}
	rs := &mockRedisGet{err: errors.New("redis: connection refused")}
	h := newDispatchHandler(t, sender, rs)
	w := postJSON(t, h, MetaDispatchPayload{MessageID: "msg-1", Status: "completed"})
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 (redis down → retry), got %d", w.Code)
	}
}

func TestDispatch_EmptyContentSkipsSend(t *testing.T) {
	sender := &mockSender{}
	redis := &mockRedisGet{}
	redis.put("task:metadata:msg-empty", `{"user_number":"5521"}`)
	h := newDispatchHandler(t, sender, redis)
	w := postJSON(t, h, MetaDispatchPayload{
		MessageID: "msg-empty",
		Status:    "completed",
		Data:      map[string]interface{}{"messages": []interface{}{}},
	})
	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
	if sender.callCount() != 0 {
		t.Errorf("expected zero sends (no content), got %d", sender.callCount())
	}
}

func TestDispatch_SendFailureReturns502(t *testing.T) {
	sender := &mockSender{failErr: errors.New("Meta 5xx")}
	redis := &mockRedisGet{}
	redis.put("task:metadata:msg-fail", `{"user_number":"5521"}`)
	h := newDispatchHandler(t, sender, redis)
	w := postJSON(t, h, MetaDispatchPayload{
		MessageID: "msg-fail",
		Status:    "completed",
		Data: map[string]interface{}{
			"messages": []interface{}{
				map[string]interface{}{"content": "hi"},
			},
		},
	})
	if w.Code != http.StatusBadGateway {
		t.Errorf("expected 502, got %d", w.Code)
	}
}

func TestDispatch_RejectsMissingSecret(t *testing.T) {
	// Codex P1: sem auth, qualquer caller com message_id válido podia
	// disparar SendText arbitrário. Verify 401 sem o header.
	sender := &mockSender{}
	redis := &mockRedisGet{}
	redis.put("task:metadata:msg-1", `{"user_number":"5521"}`)
	h := newDispatchHandler(t, sender, redis)

	payload, _ := json.Marshal(MetaDispatchPayload{MessageID: "msg-1", Status: "completed"})
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/meta/dispatch", bytes.NewReader(payload))
	c.Request.Header.Set("Content-Type", "application/json")
	// SEM X-Meta-Dispatch-Secret
	h.HandleDispatch(c)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 (missing secret), got %d", w.Code)
	}
	if sender.callCount() != 0 {
		t.Errorf("expected zero sends (rejected), got %d", sender.callCount())
	}
}

func TestDispatch_RejectsWrongSecret(t *testing.T) {
	sender := &mockSender{}
	redis := &mockRedisGet{}
	redis.put("task:metadata:msg-1", `{"user_number":"5521"}`)
	h := newDispatchHandler(t, sender, redis)

	payload, _ := json.Marshal(MetaDispatchPayload{MessageID: "msg-1", Status: "completed"})
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/meta/dispatch", bytes.NewReader(payload))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Request.Header.Set("X-Meta-Dispatch-Secret", "wrong")
	h.HandleDispatch(c)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 (wrong secret), got %d", w.Code)
	}
}

func TestDispatch_RejectsWhenSecretUnset(t *testing.T) {
	// Fail-closed: handler com secret vazio sempre rejeita 401, mesmo se
	// caller mandar header vazio.
	gin.SetMode(gin.TestMode)
	logger := logrus.New()
	logger.SetOutput(stdio.Discard)
	h := NewMetaDispatchHandler(&mockSender{}, &mockRedisGet{}, "", logger)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/meta/dispatch", bytes.NewReader([]byte("{}")))
	c.Request.Header.Set("X-Meta-Dispatch-Secret", "")
	h.HandleDispatch(c)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 (fail-closed), got %d", w.Code)
	}
}

// ─── Dispatch routing by message_type ────────────────────────────────────

func dispatchPayload(messages ...interface{}) MetaDispatchPayload {
	return MetaDispatchPayload{
		MessageID: "msg-1",
		Status:    "completed",
		Data:      map[string]interface{}{"messages": messages},
	}
}

func TestDispatch_RoutesImageEnvelope(t *testing.T) {
	sender := &mockSender{}
	rs := &mockRedisGet{}
	rs.put("task:metadata:msg-1", `{"user_number":"5521"}`)
	h := newDispatchHandler(t, sender, rs)
	w := postJSON(t, h, dispatchPayload(
		map[string]interface{}{
			"message_type": "tool_return_message",
			"name":         "send_whatsapp_media",
			"content":      `{"status":"ok","type":"image","url":"https://example.com/img.jpg","caption":"foto"}`,
		},
	))
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	last := sender.lastCall()
	if last == nil || last.kind != "media" || last.mediaType != "image" {
		t.Errorf("expected media/image, got %+v", last)
	}
	if last.media.Link != "https://example.com/img.jpg" {
		t.Errorf("expected link, got %q", last.media.Link)
	}
}

func TestDispatch_RoutesAudioFromGenerateAudioResponse(t *testing.T) {
	// audio_base64 → UploadMedia → SendMedia(ID=uploaded-id). Verifica que
	// envelope base64 não falha em "media requires either id or link".
	sender := &mockSender{}
	rs := &mockRedisGet{}
	rs.put("task:metadata:msg-1", `{"user_number":"5521"}`)
	h := newDispatchHandler(t, sender, rs)
	// "AAAA" base64-decoded = 3 bytes nulos
	w := postJSON(t, h, dispatchPayload(
		map[string]interface{}{
			"message_type": "tool_return_message",
			"name":         "generate_audio_response",
			"content":      `{"status":"ok","audio_base64":"SGVsbG8=","mime_type":"audio/ogg"}`,
		},
	))
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	// Deve ter chamado UploadMedia + SendMedia (2 calls)
	if sender.callCount() != 2 {
		t.Fatalf("expected 2 calls (upload+send), got %d", sender.callCount())
	}
	if sender.calls[0].kind != "upload" {
		t.Errorf("expected first call upload, got %+v", sender.calls[0])
	}
	if sender.calls[1].kind != "media" || sender.calls[1].mediaType != "audio" {
		t.Errorf("expected second call media/audio, got %+v", sender.calls[1])
	}
	if sender.calls[1].media.ID != "uploaded-media-id-1" {
		t.Errorf("expected media.ID=uploaded-media-id-1, got %q", sender.calls[1].media.ID)
	}
}

func TestDispatch_RoutesLocationEnvelope(t *testing.T) {
	sender := &mockSender{}
	rs := &mockRedisGet{}
	rs.put("task:metadata:msg-1", `{"user_number":"5521"}`)
	h := newDispatchHandler(t, sender, rs)
	w := postJSON(t, h, dispatchPayload(
		map[string]interface{}{
			"message_type": "tool_return_message",
			"name":         "send_whatsapp_media",
			"content":      `{"status":"ok","type":"location","latitude":-22.9,"longitude":-43.2,"name":"Praça"}`,
		},
	))
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	last := sender.lastCall()
	if last == nil || last.kind != "location" {
		t.Fatalf("expected location, got %+v", last)
	}
	if last.lat != -22.9 || last.lng != -43.2 {
		t.Errorf("expected coords, got lat=%v lng=%v", last.lat, last.lng)
	}
}

func TestDispatch_RoutesInteractiveEnvelope(t *testing.T) {
	sender := &mockSender{}
	rs := &mockRedisGet{}
	rs.put("task:metadata:msg-1", `{"user_number":"5521"}`)
	h := newDispatchHandler(t, sender, rs)
	w := postJSON(t, h, dispatchPayload(
		map[string]interface{}{
			"message_type": "tool_return_message",
			"name":         "send_whatsapp_buttons",
			"content": map[string]interface{}{
				"status": "ok",
				"type":   "interactive",
				"interactive": map[string]interface{}{
					"subtype": "button",
					"body":    map[string]interface{}{"text": "Confirma?"},
					"action": map[string]interface{}{
						"buttons": []map[string]interface{}{
							{"type": "reply", "reply": map[string]string{"id": "yes", "title": "Sim"}},
						},
					},
				},
			},
		},
	))
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	last := sender.lastCall()
	if last == nil || last.kind != "interactive" || last.subtype != "button" {
		t.Errorf("expected interactive/button, got %+v", last)
	}
}

func TestDispatch_RoutesReactionEnvelope(t *testing.T) {
	sender := &mockSender{}
	rs := &mockRedisGet{}
	rs.put("task:metadata:msg-1", `{"user_number":"5521"}`)
	h := newDispatchHandler(t, sender, rs)
	w := postJSON(t, h, dispatchPayload(
		map[string]interface{}{
			"message_type": "tool_return_message",
			"name":         "send_whatsapp_media",
			"content":      `{"status":"ok","type":"reaction","reaction_to_message_id":"wamid.PREV","emoji":"❤️"}`,
		},
	))
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	last := sender.lastCall()
	if last == nil || last.kind != "reaction" || last.emoji != "❤️" || last.wamidPrev != "wamid.PREV" {
		t.Errorf("expected reaction, got %+v", last)
	}
}

func TestDispatch_FallsBackToTextWhenNoEnvelope(t *testing.T) {
	sender := &mockSender{}
	rs := &mockRedisGet{}
	rs.put("task:metadata:msg-1", `{"user_number":"5521"}`)
	h := newDispatchHandler(t, sender, rs)
	w := postJSON(t, h, dispatchPayload(
		map[string]interface{}{"content": "Olá!", "role": "ai"},
	))
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	last := sender.lastCall()
	if last == nil || last.kind != "text" || last.body != "Olá!" {
		t.Errorf("expected text Olá!, got %+v", last)
	}
}

func TestDispatch_LocationEnvelopeMissingCoordsReturns502(t *testing.T) {
	sender := &mockSender{}
	rs := &mockRedisGet{}
	rs.put("task:metadata:msg-1", `{"user_number":"5521"}`)
	h := newDispatchHandler(t, sender, rs)
	w := postJSON(t, h, dispatchPayload(
		map[string]interface{}{
			"message_type": "tool_return_message",
			"name":         "send_whatsapp_media",
			"content":      `{"status":"ok","type":"location","name":"Sem coords"}`,
		},
	))
	if w.Code != http.StatusBadGateway {
		t.Errorf("expected 502, got %d", w.Code)
	}
	if sender.callCount() != 0 {
		t.Errorf("expected zero sends, got %d", sender.callCount())
	}
}

func TestExtractMessageText(t *testing.T) {
	cases := []struct {
		name string
		in   map[string]interface{}
		want string
	}{
		{"nil", nil, ""},
		{"empty", map[string]interface{}{}, ""},
		{"messages last", map[string]interface{}{
			"messages": []interface{}{
				map[string]interface{}{"content": "a"},
				map[string]interface{}{"content": "b"},
			},
		}, "b"},
		{"skip trailing usage_statistics by type", map[string]interface{}{
			"messages": []interface{}{
				map[string]interface{}{"content": "real reply", "role": "ai"},
				map[string]interface{}{"type": "usage_statistics", "input_tokens": 10},
			},
		}, "real reply"},
		{"skip trailing usage_statistics by key presence", map[string]interface{}{
			"messages": []interface{}{
				map[string]interface{}{"content": "another reply"},
				map[string]interface{}{"usage_statistics": map[string]interface{}{"x": 1}},
			},
		}, "another reply"},
		{"data.content fallback", map[string]interface{}{
			"content": "direct",
		}, "direct"},
		{"messages with non-string", map[string]interface{}{
			"messages": []interface{}{
				map[string]interface{}{"content": 123},
			},
		}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := extractMessageText(tc.in); got != tc.want {
				t.Errorf("got %q want %q", got, tc.want)
			}
		})
	}
}
