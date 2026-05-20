package handlers

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	stdio "io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"

	"github.com/prefeitura-rio/app-eai-agent-gateway/internal/config"
)

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
	return NewMetaWebhookHandler(cfg, nil, logger)
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
	h := NewMetaWebhookHandler(&config.MetaConfig{VerifyToken: ""}, nil, logger)
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

func TestInbound_UnsupportedTypeAlsoReturns503(t *testing.T) {
	h := newTestHandler(t)
	// image type não suportado no POC; deve retornar 503 (não 200) pra
	// forçar Meta retry. Sem isso = silent drop (codex P2 round 2).
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
	          "timestamp": "1779225000",
	          "type": "image",
	          "image": {"id": "media-id-xxx", "mime_type": "image/jpeg"}
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
		t.Errorf("expected 503 (unsupported type), got %d", w.Code)
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

func TestInbound_TextMessage_Returns503UntilEnqueueWired(t *testing.T) {
	h := newTestHandler(t)
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

	// POC: text parseado deve retornar 503 (Meta retry) enquanto enqueue
	// real não está wired (task #106). Sem isso, mensagem seria silent-drop.
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 (POC parse-only), got %d body=%s", w.Code, w.Body.String())
	}
	var resp map[string]interface{}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["status"] != "not_ready" {
		t.Errorf("expected status=not_ready, got %v", resp["status"])
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
		nil, logger,
	)
	// Mesmo com header válido, sem AppSecret deve falhar.
	body := []byte("body")
	sig := signBody(t, body, "any")
	if h.validateSignature(sig, body) {
		t.Error("expected false when AppSecret empty (fail-closed)")
	}
}

