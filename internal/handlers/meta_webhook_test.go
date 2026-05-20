package handlers

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
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

	"github.com/prefeitura-rio/app-eai-agent-gateway/internal/config"
	"github.com/prefeitura-rio/app-eai-agent-gateway/internal/models"
)

// ─── Mocks ───────────────────────────────────────────────────────────────

// mockEnqueuer captura chamadas EnqueueUserMessage pra asserir o que foi
// publicado. shouldFail força erro pra testar o path failed.
type mockEnqueuer struct {
	mu          sync.Mutex
	calls       []models.UserWebhookRequest
	shouldFail  bool
	failInvalid bool
}

func (m *mockEnqueuer) EnqueueUserMessage(
	_ context.Context, req *models.UserWebhookRequest,
	_ string, _ map[string]interface{}, _ EnqueueOptions,
) (*EnqueueResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if req != nil {
		m.calls = append(m.calls, *req)
	}
	if m.failInvalid {
		return nil, ErrInvalidPayload{Reason: "test invalid"}
	}
	if m.shouldFail {
		return nil, errors.New("enqueue mock failure")
	}
	return &EnqueueResult{MessageID: "test-msg-id", Status: "processing"}, nil
}

func (m *mockEnqueuer) callCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.calls)
}

func (m *mockEnqueuer) lastCall() *models.UserWebhookRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.calls) == 0 {
		return nil
	}
	r := m.calls[len(m.calls)-1]
	return &r
}

// mockDedup implementa MetaDedupChecker em memória com semântica SETNX +
// Delete. setNXErr força erro no SetNX (pra testar fallback non-atomic).
type mockDedup struct {
	mu       sync.Mutex
	store    map[string]string
	setNXErr error
	delErr   error
}

func newMockDedup() *mockDedup {
	return &mockDedup{store: map[string]string{}}
}

func (m *mockDedup) SetNX(_ context.Context, key string, value string, _ time.Duration) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.setNXErr != nil {
		return false, m.setNXErr
	}
	if _, exists := m.store[key]; exists {
		return false, nil
	}
	m.store[key] = value
	return true, nil
}

func (m *mockDedup) Get(_ context.Context, key string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if v, ok := m.store[key]; ok {
		return v, nil
	}
	return "", nil
}

func (m *mockDedup) Set(_ context.Context, key string, value string, _ time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.store[key] = value
	return nil
}

func (m *mockDedup) Delete(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.delErr != nil {
		return m.delErr
	}
	delete(m.store, key)
	return nil
}

func (m *mockDedup) putValue(key, val string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.store[key] = val
}

func (m *mockDedup) has(key string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.store[key]
	return ok
}

// newTestHandlerWithDeps — helper que aceita mocks de enqueue + dedup.
func newTestHandlerWithDeps(t *testing.T, enq MessageEnqueuer, dedup MetaDedupChecker) *MetaWebhookHandler {
	t.Helper()
	gin.SetMode(gin.TestMode)
	logger := logrus.New()
	logger.SetOutput(stdio.Discard)
	cfg := &config.MetaConfig{
		Enabled:         true,
		VerifyToken:     "test-verify-token",
		AppSecret:       "test-app-secret",
		PhoneNumberID:   "123",
		SystemUserToken: "tok",
		GraphAPIVersion: "v21.0",
	}
	return NewMetaWebhookHandler(cfg, enq, dedup, logger)
}

// newTestHandler — helper pra testes; AppSecret/VerifyToken fixos.
func newTestHandler(t *testing.T) *MetaWebhookHandler {
	t.Helper()
	gin.SetMode(gin.TestMode)
	logger := logrus.New()
	logger.SetOutput(stdio.Discard)
	cfg := &config.MetaConfig{
		Enabled:         true,
		VerifyToken:     "test-verify-token",
		AppSecret:       "test-app-secret",
		PhoneNumberID:   "123",
		SystemUserToken: "tok",
		GraphAPIVersion: "v21.0",
	}
	return NewMetaWebhookHandler(cfg, nil, nil, logger)
}

// signBody helper — produz "sha256=<hex>".
func signBody(t *testing.T, body []byte, secret string) string {
	t.Helper()
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// ────────────────────────────────────────────────────────────
// HandleVerify
// ────────────────────────────────────────────────────────────

func TestVerify_HappyPath(t *testing.T) {
	h := newTestHandler(t)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet,
		"/meta/webhook?hub.mode=subscribe&hub.verify_token=test-verify-token&hub.challenge=xyz789", nil)

	h.HandleVerify(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if got := w.Body.String(); got != "xyz789" {
		t.Errorf("expected body=challenge, got %q", got)
	}
}

func TestVerify_WrongMode(t *testing.T) {
	h := newTestHandler(t)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet,
		"/meta/webhook?hub.mode=unsubscribe&hub.verify_token=test-verify-token&hub.challenge=x", nil)

	h.HandleVerify(c)

	if w.Code != http.StatusForbidden {
		t.Errorf("expected 403, got %d", w.Code)
	}
}

func TestVerify_FailsClosedWhenTokenUnset(t *testing.T) {
	gin.SetMode(gin.TestMode)
	logger := logrus.New()
	logger.SetOutput(stdio.Discard)
	// VerifyToken empty na config — request sem token também é empty,
	// mas deve falhar (fail-closed, codex P2).
	h := NewMetaWebhookHandler(&config.MetaConfig{VerifyToken: ""}, nil, nil, logger)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet,
		"/meta/webhook?hub.mode=subscribe&hub.challenge=open", nil)

	h.HandleVerify(c)

	if w.Code != http.StatusForbidden {
		t.Errorf("expected 403 fail-closed when token unset, got %d", w.Code)
	}
}

func TestVerify_WrongToken(t *testing.T) {
	h := newTestHandler(t)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet,
		"/meta/webhook?hub.mode=subscribe&hub.verify_token=ATTACKER&hub.challenge=x", nil)

	h.HandleVerify(c)

	if w.Code != http.StatusForbidden {
		t.Errorf("expected 403, got %d", w.Code)
	}
}

// ────────────────────────────────────────────────────────────
// HandleInbound — HMAC
// ────────────────────────────────────────────────────────────

func TestInbound_MissingSignature(t *testing.T) {
	h := newTestHandler(t)
	body := []byte(`{"object":"whatsapp_business_account","entry":[]}`)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/meta/webhook", bytes.NewReader(body))

	h.HandleInbound(c)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 missing signature, got %d", w.Code)
	}
}

func TestInbound_InvalidSignature(t *testing.T) {
	h := newTestHandler(t)
	body := []byte(`{"object":"whatsapp_business_account","entry":[]}`)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/meta/webhook", bytes.NewReader(body))
	c.Request.Header.Set("X-Hub-Signature-256", "sha256=deadbeef")

	h.HandleInbound(c)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 invalid signature, got %d", w.Code)
	}
}

func TestInbound_ValidSignatureEmptyEnvelope(t *testing.T) {
	h := newTestHandler(t)
	body := []byte(`{"object":"whatsapp_business_account","entry":[]}`)
	sig := signBody(t, body, "test-app-secret")
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/meta/webhook", bytes.NewReader(body))
	c.Request.Header.Set("X-Hub-Signature-256", sig)

	h.HandleInbound(c)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestInbound_NonWhatsappObject(t *testing.T) {
	h := newTestHandler(t)
	body := []byte(`{"object":"instagram","entry":[]}`)
	sig := signBody(t, body, "test-app-secret")
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/meta/webhook", bytes.NewReader(body))
	c.Request.Header.Set("X-Hub-Signature-256", sig)

	h.HandleInbound(c)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200 (ignored), got %d", w.Code)
	}
	var resp map[string]interface{}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["status"] != "ignored" {
		t.Errorf("expected status=ignored, got %v", resp)
	}
}

func TestInbound_UnsupportedTypeReturns503(t *testing.T) {
	// Sticker não é suportado no parser — handler retorna routeSkipped que
	// vira 503 pra Meta retentar. Sem isso = silent drop (codex P2 round 2).
	enq := &mockEnqueuer{}
	h := newTestHandlerWithDeps(t, enq, newMockDedup())
	payload := `{
	  "object": "whatsapp_business_account",
	  "entry": [{
	    "id": "WABA_ID",
	    "changes": [{
	      "field": "messages",
	      "value": {
	        "messaging_product": "whatsapp",
	        "metadata": {"display_phone_number": "21", "phone_number_id": "1"},
	        "messages": [{
	          "from": "5500",
	          "id": "wamid.STK",
	          "timestamp": "1779225000",
	          "type": "sticker",
	          "sticker": {"id": "stk-1", "mime_type": "image/webp"}
	        }]
	      }
	    }]
	  }]
	}`
	body := []byte(payload)
	sig := signBody(t, body, "test-app-secret")
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/meta/webhook", bytes.NewReader(body))
	c.Request.Header.Set("X-Hub-Signature-256", sig)
	h.HandleInbound(c)
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 (unsupported sticker), got %d", w.Code)
	}
	if enq.callCount() != 0 {
		t.Errorf("expected zero enqueue calls (unsupported), got %d", enq.callCount())
	}
}

func TestInbound_StatusCallbackReturns200(t *testing.T) {
	h := newTestHandler(t)
	// Status callbacks só (sem mensagens) — não há trabalho pendente,
	// 200 OK é correto (Meta não precisa retentar).
	payload := `{
	  "object": "whatsapp_business_account",
	  "entry": [{
	    "id": "WABA_ID",
	    "changes": [{
	      "field": "messages",
	      "value": {
	        "messaging_product": "whatsapp",
	        "metadata": {"display_phone_number": "21", "phone_number_id": "1"},
	        "statuses": [{
	          "id": "wamid.X",
	          "status": "delivered",
	          "timestamp": "1779225000",
	          "recipient_id": "5500"
	        }]
	      }
	    }]
	  }]
	}`
	body := []byte(payload)
	sig := signBody(t, body, "test-app-secret")
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/meta/webhook", bytes.NewReader(body))
	c.Request.Header.Set("X-Hub-Signature-256", sig)

	h.HandleInbound(c)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200 (status-only), got %d body=%s", w.Code, w.Body.String())
	}
}

func TestInbound_TextMessage_EnqueuesAndReturns200(t *testing.T) {
	enq := &mockEnqueuer{}
	h := newTestHandlerWithDeps(t, enq, newMockDedup())
	payload := `{
	  "object": "whatsapp_business_account",
	  "entry": [{
	    "id": "WABA_ID",
	    "changes": [{
	      "field": "messages",
	      "value": {
	        "messaging_product": "whatsapp",
	        "metadata": {"display_phone_number": "21989091014", "phone_number_id": "1121997777663061"},
	        "contacts": [{"wa_id": "5500000000000", "profile": {"name": "Test"}}],
	        "messages": [{
	          "from": "5500000000000",
	          "id": "wamid.TEST",
	          "timestamp": "1779225000",
	          "type": "text",
	          "text": {"body": "Olá bot"}
	        }]
	      }
	    }]
	  }]
	}`
	body := []byte(payload)
	sig := signBody(t, body, "test-app-secret")
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/meta/webhook", bytes.NewReader(body))
	c.Request.Header.Set("X-Hub-Signature-256", sig)
	h.HandleInbound(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 (enqueued), got %d body=%s", w.Code, w.Body.String())
	}
	if enq.callCount() != 1 {
		t.Fatalf("expected 1 enqueue call, got %d", enq.callCount())
	}
	req := enq.lastCall()
	if req.UserNumber != "5500000000000" {
		t.Errorf("expected UserNumber=5500000000000, got %q", req.UserNumber)
	}
	if req.Message != "Olá bot" {
		t.Errorf("expected Message=Olá bot, got %q", req.Message)
	}
	if req.Metadata["wamid"] != "wamid.TEST" {
		t.Errorf("expected metadata.wamid=wamid.TEST, got %v", req.Metadata["wamid"])
	}
}

func TestInbound_EnqueueFailureReturns503(t *testing.T) {
	enq := &mockEnqueuer{shouldFail: true}
	h := newTestHandlerWithDeps(t, enq, newMockDedup())
	payload := `{
	  "object": "whatsapp_business_account",
	  "entry": [{
	    "id": "WABA_ID",
	    "changes": [{
	      "field": "messages",
	      "value": {
	        "messaging_product": "whatsapp",
	        "metadata": {"display_phone_number": "21", "phone_number_id": "1"},
	        "messages": [{
	          "from": "5500",
	          "id": "wamid.FAIL",
	          "timestamp": "1",
	          "type": "text",
	          "text": {"body": "x"}
	        }]
	      }
	    }]
	  }]
	}`
	body := []byte(payload)
	sig := signBody(t, body, "test-app-secret")
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/meta/webhook", bytes.NewReader(body))
	c.Request.Header.Set("X-Hub-Signature-256", sig)
	h.HandleInbound(c)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 (enqueue failure), got %d", w.Code)
	}
}

func TestInbound_DedupSkipsSecondCall(t *testing.T) {
	enq := &mockEnqueuer{}
	dedup := newMockDedup()
	h := newTestHandlerWithDeps(t, enq, dedup)
	payload := `{
	  "object": "whatsapp_business_account",
	  "entry": [{
	    "id": "WABA_ID",
	    "changes": [{
	      "field": "messages",
	      "value": {
	        "messaging_product": "whatsapp",
	        "metadata": {"display_phone_number": "21", "phone_number_id": "1"},
	        "messages": [{
	          "from": "5500",
	          "id": "wamid.DUP",
	          "timestamp": "1",
	          "type": "text",
	          "text": {"body": "dup test"}
	        }]
	      }
	    }]
	  }]
	}`
	body := []byte(payload)
	sig := signBody(t, body, "test-app-secret")

	// Primeira chamada — enqueue + dedup register
	{
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, "/meta/webhook", bytes.NewReader(body))
		c.Request.Header.Set("X-Hub-Signature-256", sig)
		h.HandleInbound(c)
		if w.Code != http.StatusOK {
			t.Fatalf("first call expected 200, got %d", w.Code)
		}
	}
	// Segunda — dedup hit, sem nova enqueue
	{
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, "/meta/webhook", bytes.NewReader(body))
		c.Request.Header.Set("X-Hub-Signature-256", sig)
		h.HandleInbound(c)
		if w.Code != http.StatusOK {
			t.Fatalf("second (dup) call expected 200, got %d", w.Code)
		}
	}
	if enq.callCount() != 1 {
		t.Errorf("expected 1 enqueue (dup skipped), got %d", enq.callCount())
	}
}

func TestInbound_ImageMessage_ExtractsMedia(t *testing.T) {
	enq := &mockEnqueuer{}
	h := newTestHandlerWithDeps(t, enq, newMockDedup())
	payload := `{
	  "object": "whatsapp_business_account",
	  "entry": [{
	    "id": "WABA_ID",
	    "changes": [{
	      "field": "messages",
	      "value": {
	        "messaging_product": "whatsapp",
	        "metadata": {"display_phone_number": "21", "phone_number_id": "1"},
	        "messages": [{
	          "from": "5500",
	          "id": "wamid.IMG",
	          "timestamp": "1",
	          "type": "image",
	          "image": {"id": "meta-media-123", "mime_type": "image/jpeg", "sha256": "abc"}
	        }]
	      }
	    }]
	  }]
	}`
	body := []byte(payload)
	sig := signBody(t, body, "test-app-secret")
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/meta/webhook", bytes.NewReader(body))
	c.Request.Header.Set("X-Hub-Signature-256", sig)
	h.HandleInbound(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	req := enq.lastCall()
	if req == nil {
		t.Fatal("expected enqueue call, got none")
	}
	if req.MessageType == nil || *req.MessageType != "image" {
		t.Errorf("expected MessageType=image, got %v", req.MessageType)
	}
	if req.Media["meta_media_id"] != "meta-media-123" {
		t.Errorf("expected meta_media_id=meta-media-123, got %v", req.Media["meta_media_id"])
	}
}

func TestInbound_LocationMessage_ExtractsCoords(t *testing.T) {
	enq := &mockEnqueuer{}
	h := newTestHandlerWithDeps(t, enq, newMockDedup())
	payload := `{
	  "object": "whatsapp_business_account",
	  "entry": [{
	    "id": "WABA_ID",
	    "changes": [{
	      "field": "messages",
	      "value": {
	        "messaging_product": "whatsapp",
	        "metadata": {"display_phone_number": "21", "phone_number_id": "1"},
	        "messages": [{
	          "from": "5500",
	          "id": "wamid.LOC",
	          "timestamp": "1",
	          "type": "location",
	          "location": {"latitude": -22.9, "longitude": -43.2, "name": "Centro", "address": "RJ"}
	        }]
	      }
	    }]
	  }]
	}`
	body := []byte(payload)
	sig := signBody(t, body, "test-app-secret")
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/meta/webhook", bytes.NewReader(body))
	c.Request.Header.Set("X-Hub-Signature-256", sig)
	h.HandleInbound(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	req := enq.lastCall()
	if req == nil {
		t.Fatal("expected enqueue call")
	}
	if req.MessageType == nil || *req.MessageType != "location" {
		t.Errorf("expected MessageType=location, got %v", req.MessageType)
	}
	if got := req.Media["latitude"]; got != float64(-22.9) {
		t.Errorf("expected latitude=-22.9, got %v", got)
	}
}

func TestInbound_InteractiveButtonReply_DowncastsToText(t *testing.T) {
	enq := &mockEnqueuer{}
	h := newTestHandlerWithDeps(t, enq, newMockDedup())
	payload := `{
	  "object": "whatsapp_business_account",
	  "entry": [{
	    "id": "WABA_ID",
	    "changes": [{
	      "field": "messages",
	      "value": {
	        "messaging_product": "whatsapp",
	        "metadata": {"display_phone_number": "21", "phone_number_id": "1"},
	        "messages": [{
	          "from": "5500",
	          "id": "wamid.BTN",
	          "timestamp": "1",
	          "type": "interactive",
	          "interactive": {
	            "type": "button_reply",
	            "button_reply": {"id": "btn-1", "title": "Sim"}
	          }
	        }]
	      }
	    }]
	  }]
	}`
	body := []byte(payload)
	sig := signBody(t, body, "test-app-secret")
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/meta/webhook", bytes.NewReader(body))
	c.Request.Header.Set("X-Hub-Signature-256", sig)
	h.HandleInbound(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	req := enq.lastCall()
	if req == nil || req.Message != "Sim" {
		t.Errorf("expected Message=Sim, got %v", req)
	}
	if req.Metadata["interactive_kind"] != "button_reply" {
		t.Errorf("expected interactive_kind=button_reply, got %v", req.Metadata["interactive_kind"])
	}
	if req.Metadata["interactive_id"] != "btn-1" {
		t.Errorf("expected interactive_id=btn-1, got %v", req.Metadata["interactive_id"])
	}
}

func TestInbound_InFlightClaimReturns503(t *testing.T) {
	// Codex P2: claim adquirida mas ainda não-done. Dup deve retornar 503
	// pra Meta retentar (vs 200 que silenciaria a entrega).
	enq := &mockEnqueuer{}
	dedup := newMockDedup()
	dedup.putValue("meta:wamid:wamid.INFLIGHT", "inflight") // simula primeira req in-progress
	h := newTestHandlerWithDeps(t, enq, dedup)
	payload := `{
	  "object": "whatsapp_business_account",
	  "entry": [{"id": "WABA_ID", "changes": [{"field": "messages",
	    "value": {"messaging_product":"whatsapp",
	      "metadata":{"display_phone_number":"21","phone_number_id":"1"},
	      "messages":[{"from":"5500","id":"wamid.INFLIGHT","timestamp":"1","type":"text","text":{"body":"x"}}]
	    }}]}]}`
	body := []byte(payload)
	sig := signBody(t, body, "test-app-secret")
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/meta/webhook", bytes.NewReader(body))
	c.Request.Header.Set("X-Hub-Signature-256", sig)
	h.HandleInbound(c)
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 (in-flight → retry), got %d", w.Code)
	}
}

func TestInbound_CompletedDupReturns200(t *testing.T) {
	// Mesma claim mas valor "done" → 200 (silent skip, primeira request
	// já completou e devolveu 200 ao Meta).
	enq := &mockEnqueuer{}
	dedup := newMockDedup()
	dedup.putValue("meta:wamid:wamid.DONE", "done")
	h := newTestHandlerWithDeps(t, enq, dedup)
	payload := `{
	  "object": "whatsapp_business_account",
	  "entry": [{"id": "WABA_ID", "changes": [{"field": "messages",
	    "value": {"messaging_product":"whatsapp",
	      "metadata":{"display_phone_number":"21","phone_number_id":"1"},
	      "messages":[{"from":"5500","id":"wamid.DONE","timestamp":"1","type":"text","text":{"body":"x"}}]
	    }}]}]}`
	body := []byte(payload)
	sig := signBody(t, body, "test-app-secret")
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/meta/webhook", bytes.NewReader(body))
	c.Request.Header.Set("X-Hub-Signature-256", sig)
	h.HandleInbound(c)
	if w.Code != http.StatusOK {
		t.Errorf("expected 200 (done → silent dup), got %d", w.Code)
	}
	if enq.callCount() != 0 {
		t.Errorf("expected zero enqueue (already done), got %d", enq.callCount())
	}
}

func TestInbound_EnqueueFailureReleasesDedupClaim(t *testing.T) {
	// Codex P1: claim antes do enqueue cria silent loss se enqueue falha.
	// Fix: liberar a claim quando outcome=failed/skipped, pra Meta retry
	// conseguir reprocessar.
	enq := &mockEnqueuer{shouldFail: true}
	dedup := newMockDedup()
	h := newTestHandlerWithDeps(t, enq, dedup)
	payload := `{
	  "object": "whatsapp_business_account",
	  "entry": [{
	    "id": "WABA_ID",
	    "changes": [{
	      "field": "messages",
	      "value": {
	        "messaging_product": "whatsapp",
	        "metadata": {"display_phone_number": "21", "phone_number_id": "1"},
	        "messages": [{"from": "5500", "id": "wamid.RELEASE", "timestamp": "1", "type": "text", "text": {"body": "x"}}]
	      }
	    }]
	  }]
	}`
	body := []byte(payload)
	sig := signBody(t, body, "test-app-secret")
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/meta/webhook", bytes.NewReader(body))
	c.Request.Header.Set("X-Hub-Signature-256", sig)
	h.HandleInbound(c)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 (enqueue failed), got %d", w.Code)
	}
	// Claim deve ter sido liberada — Meta retry verá ledger limpo.
	if dedup.has("meta:wamid:wamid.RELEASE") {
		t.Error("expected dedup claim to be released after enqueue failure; Meta retry would be silently skipped")
	}
}

func TestInbound_DedupSetNXErrorContinuesWithoutBlock(t *testing.T) {
	// SETNX retornando erro não deve bloquear o handler — só logamos e
	// continuamos sem garantia de dedup. Resilience >	atomicidade quando
	// Redis flapping.
	enq := &mockEnqueuer{}
	dedup := newMockDedup()
	dedup.setNXErr = errors.New("redis down")
	h := newTestHandlerWithDeps(t, enq, dedup)
	payload := `{
	  "object": "whatsapp_business_account",
	  "entry": [{
	    "id": "WABA_ID",
	    "changes": [{
	      "field": "messages",
	      "value": {
	        "messaging_product": "whatsapp",
	        "metadata": {"display_phone_number": "21", "phone_number_id": "1"},
	        "messages": [{"from": "5500", "id": "wamid.SETNXERR", "timestamp": "1", "type": "text", "text": {"body": "x"}}]
	      }
	    }]
	  }]
	}`
	body := []byte(payload)
	sig := signBody(t, body, "test-app-secret")
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/meta/webhook", bytes.NewReader(body))
	c.Request.Header.Set("X-Hub-Signature-256", sig)
	h.HandleInbound(c)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200 (degraded dedup), got %d", w.Code)
	}
	if enq.callCount() != 1 {
		t.Errorf("expected 1 enqueue (without dedup), got %d", enq.callCount())
	}
}

func TestInbound_AntiLoopGuardDropsSelfMessages(t *testing.T) {
	// Mensagem em que `from` bate com `display_phone_number` da WABA = bot
	// se respondendo a si mesmo. Drop pra evitar cascade infinita.
	enq := &mockEnqueuer{}
	h := newTestHandlerWithDeps(t, enq, newMockDedup())
	payload := `{
	  "object": "whatsapp_business_account",
	  "entry": [{"id": "WABA_ID", "changes": [{"field": "messages",
	    "value": {"messaging_product":"whatsapp",
	      "metadata":{"display_phone_number":"5521989091014","phone_number_id":"1"},
	      "messages":[{"from":"5521989091014","id":"wamid.LOOP","timestamp":"1","type":"text","text":{"body":"loop"}}]
	    }}]}]}`
	body := []byte(payload)
	sig := signBody(t, body, "test-app-secret")
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/meta/webhook", bytes.NewReader(body))
	c.Request.Header.Set("X-Hub-Signature-256", sig)
	h.HandleInbound(c)
	if w.Code != http.StatusOK {
		t.Errorf("expected 200 (dropped self-loop), got %d", w.Code)
	}
	if enq.callCount() != 0 {
		t.Errorf("expected zero enqueues (self-loop drop), got %d", enq.callCount())
	}
}

func TestInbound_NfmReplyAppliesFlowRegistry(t *testing.T) {
	// Submissão de Flow "Luminária" deve adicionar service_name pra Engine
	// rotear pelo MCP correto.
	enq := &mockEnqueuer{}
	dedup := newMockDedup()
	gin.SetMode(gin.TestMode)
	logger := logrus.New()
	logger.SetOutput(stdio.Discard)
	cfg := &config.MetaConfig{
		Enabled: true, VerifyToken: "t", AppSecret: "test-app-secret",
		PhoneNumberID: "1", SystemUserToken: "tok", GraphAPIVersion: "v21.0",
		FlowRegistry:       "luminaria:reparo_luminaria;saude:agenda_saude",
		FlowDefaultService: "default_service",
	}
	h := NewMetaWebhookHandler(cfg, enq, dedup, logger)

	payload := `{
	  "object": "whatsapp_business_account",
	  "entry": [{"id": "WABA_ID", "changes": [{"field": "messages",
	    "value": {"messaging_product":"whatsapp",
	      "metadata":{"display_phone_number":"21","phone_number_id":"1"},
	      "messages":[{"from":"5500","id":"wamid.FLOW","timestamp":"1","type":"interactive",
	        "interactive":{
	          "type":"nfm_reply",
	          "nfm_reply":{
	            "name":"Luminária Quebrada",
	            "response_json":"{\"endereco\":\"Rua X, 100\",\"defeito\":\"piscando\"}"
	          }
	        }}]
	    }}]}]}`
	body := []byte(payload)
	sig := signBody(t, body, "test-app-secret")
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/meta/webhook", bytes.NewReader(body))
	c.Request.Header.Set("X-Hub-Signature-256", sig)
	h.HandleInbound(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	req := enq.lastCall()
	if req == nil {
		t.Fatal("expected enqueue call")
	}
	if req.Metadata["service_name"] != "reparo_luminaria" {
		t.Errorf("expected service_name=reparo_luminaria, got %v", req.Metadata["service_name"])
	}
	form, ok := req.Metadata["form_submission"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected form_submission map, got %T", req.Metadata["form_submission"])
	}
	if form["endereco"] != "Rua X, 100" {
		t.Errorf("expected form_submission.endereco=Rua X, 100, got %v", form["endereco"])
	}
}

func TestInbound_StatusCallbackPersistsToDedup(t *testing.T) {
	// Status callbacks devem ser persistidos no Redis (key meta:status:<wamid>)
	// pra Engine/operadores consultarem delivery state.
	dedup := newMockDedup()
	h := newTestHandlerWithDeps(t, &mockEnqueuer{}, dedup)
	payload := `{
	  "object": "whatsapp_business_account",
	  "entry": [{"id": "WABA_ID", "changes": [{"field": "messages",
	    "value": {"messaging_product":"whatsapp",
	      "metadata":{"display_phone_number":"21","phone_number_id":"1"},
	      "statuses":[{"id":"wamid.SENT","status":"delivered","timestamp":"1","recipient_id":"5500"}]
	    }}]}]}`
	body := []byte(payload)
	sig := signBody(t, body, "test-app-secret")
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/meta/webhook", bytes.NewReader(body))
	c.Request.Header.Set("X-Hub-Signature-256", sig)
	h.HandleInbound(c)
	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
	if !dedup.has("meta:status:wamid.SENT") {
		t.Error("expected status callback persisted to dedup store")
	}
}

func TestInbound_NoMessageHandlerReturns503(t *testing.T) {
	// Sem MessageEnqueuer wired, text também cai em routeFailed → 503.
	gin.SetMode(gin.TestMode)
	logger := logrus.New()
	logger.SetOutput(stdio.Discard)
	cfg := &config.MetaConfig{
		Enabled: true, VerifyToken: "t", AppSecret: "test-app-secret",
		PhoneNumberID: "1", SystemUserToken: "tok", GraphAPIVersion: "v21.0",
	}
	h := NewMetaWebhookHandler(cfg, nil, newMockDedup(), logger)
	payload := `{
	  "object": "whatsapp_business_account",
	  "entry": [{
	    "id": "WABA_ID",
	    "changes": [{
	      "field": "messages",
	      "value": {
	        "messaging_product": "whatsapp",
	        "metadata": {"display_phone_number": "21", "phone_number_id": "1"},
	        "messages": [{"from": "5500", "id": "wamid.NIL", "timestamp": "1", "type": "text", "text": {"body": "x"}}]
	      }
	    }]
	  }]
	}`
	body := []byte(payload)
	sig := signBody(t, body, "test-app-secret")
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/meta/webhook", bytes.NewReader(body))
	c.Request.Header.Set("X-Hub-Signature-256", sig)
	h.HandleInbound(c)
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 (no enqueuer), got %d", w.Code)
	}
}

// ────────────────────────────────────────────────────────────
// validateSignature — edge cases
// ────────────────────────────────────────────────────────────

func TestValidateSignature_NoPrefix(t *testing.T) {
	h := newTestHandler(t)
	if h.validateSignature("nothex", []byte("body")) {
		t.Error("expected false for header without sha256= prefix")
	}
}

func TestValidateSignature_EmptySecretFailsClosed(t *testing.T) {
	gin.SetMode(gin.TestMode)
	logger := logrus.New()
	logger.SetOutput(stdio.Discard)
	h := NewMetaWebhookHandler(
		&config.MetaConfig{AppSecret: ""},
		nil, nil, logger,
	)
	// Mesmo com header válido, sem AppSecret deve falhar.
	body := []byte("body")
	sig := signBody(t, body, "any")
	if h.validateSignature(sig, body) {
		t.Error("expected false when AppSecret empty (fail-closed)")
	}
}

