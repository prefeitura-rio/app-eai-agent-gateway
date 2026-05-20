// Package handlers — Meta WhatsApp Business Cloud webhook receiver.
//
// Este handler é o entry point quando Gateway substitui o Mule como broker
// entre Meta e Engine (POC `feat/meta-direct-poc`). Cobre:
//
//	GET  /meta/webhook  → Meta verify token handshake (challenge echo).
//	POST /meta/webhook  → recebe Meta inbound events (HMAC validated).
//
// Comparação com o Mule equivalente (`meta-webhook-flow.xml`):
//
//   - GET: idêntico — confere hub.mode/hub.verify_token e responde o challenge.
//   - POST: valida X-Hub-Signature-256 HMAC, parseia minimal payload (text +
//     metadata WABA), enriquece pra UserWebhookRequest, encaminha pro
//     MessageHandler.HandleUserWebhook existente (que cuida do RabbitMQ enqueue
//     + Engine call). POC escopo limitado: TEXT only por enquanto — media/
//     interactive/status callbacks seguem em fases posteriores (ver ADR draft).
//
// HMAC NEVER trust client-side — qualquer payload sem assinatura válida é
// rejeitado HTTP 401 antes de qualquer parsing JSON (defesa contra crafted
// payloads + DoS amplificação).
package handlers

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"

	"github.com/prefeitura-rio/app-eai-agent-gateway/internal/config"
	"github.com/prefeitura-rio/app-eai-agent-gateway/internal/models"
)

// MetaWebhookHandler — recebe inbound webhook Meta direto (substituindo Mule).
type MetaWebhookHandler struct {
	cfg            *config.MetaConfig
	messageHandler *MessageHandler
	logger         *logrus.Logger
}

// NewMetaWebhookHandler constrói o handler injetando deps. messageHandler é
// reutilizado do path /api/v1/message/webhook/user — Meta inbound vira chamada
// equivalente.
func NewMetaWebhookHandler(
	cfg *config.MetaConfig,
	messageHandler *MessageHandler,
	logger *logrus.Logger,
) *MetaWebhookHandler {
	return &MetaWebhookHandler{
		cfg:            cfg,
		messageHandler: messageHandler,
		logger:         logger,
	}
}

// HandleVerify — Meta GET handshake.
//
// Documentação Meta:
//
//	GET https://your-callback-url?hub.mode=subscribe
//	    &hub.verify_token=<your_verify_token>
//	    &hub.challenge=<random_string>
//
// Resposta esperada: HTTP 200 com body = hub.challenge cru (texto, não JSON).
// Mismatch de mode/token retorna 403.
func (h *MetaWebhookHandler) HandleVerify(c *gin.Context) {
	mode := c.Query("hub.mode")
	token := c.Query("hub.verify_token")
	challenge := c.Query("hub.challenge")

	// Fail-closed: se VerifyToken não configurado, REJEITAR mesmo que
	// caller envie token vazio. Caso contrário um config bug abriria
	// verification pra qualquer request (codex P2 round 2).
	if h.cfg.VerifyToken == "" || mode != "subscribe" || token != h.cfg.VerifyToken {
		h.logger.WithFields(logrus.Fields{
			"event":          "meta_verify_rejected",
			"received_mode":  mode,
			"token_match":    token == h.cfg.VerifyToken,
			"verify_present": token != "",
		}).Warn("Meta verify token mismatch")
		c.JSON(http.StatusForbidden, gin.H{"error": "verify token mismatch"})
		return
	}

	h.logger.WithField("event", "meta_verify_ok").Info("Meta webhook verified")
	c.Header("Content-Type", "text/plain")
	c.String(http.StatusOK, challenge)
}

// HandleInbound — Meta POST inbound events.
//
// Sequência:
//  1. Buffer body inteiro (precisa pra HMAC + parse). MaxBodyBytes já vem do
//     middleware RequestSizeLimit.
//  2. Validar `X-Hub-Signature-256: sha256=<hex>` contra HMAC-SHA256(body, app_secret).
//     Se ausente ou inválido → 401.
//  3. Parse minimal envelope Meta (object=whatsapp_business_account, entry[].changes[]).
//  4. Pra cada mensagem TEXT em changes[].value.messages[], delega ao
//     MessageHandler como UserWebhookRequest.
//  5. Status callbacks (delivered/read) por enquanto só são logados — não
//     entram no fluxo de Engine.
func (h *MetaWebhookHandler) HandleInbound(c *gin.Context) {
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		h.logger.WithError(err).Error("meta_inbound: failed to read body")
		c.JSON(http.StatusBadRequest, gin.H{"error": "body read failed"})
		return
	}
	// Restaura body pro caso de futuros middlewares lerem novamente.
	c.Request.Body = io.NopCloser(bytes.NewBuffer(body))

	if !h.validateSignature(c.GetHeader("X-Hub-Signature-256"), body) {
		h.logger.WithField("event", "meta_inbound_hmac_failed").Warn("HMAC signature invalid")
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid signature"})
		return
	}

	var envelope models.MetaWebhookEnvelope
	if err := json.Unmarshal(body, &envelope); err != nil {
		h.logger.WithError(err).Error("meta_inbound: json parse failed")
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid json"})
		return
	}

	// Meta sempre envia object="whatsapp_business_account" pra mensagens WhatsApp.
	if envelope.Object != "whatsapp_business_account" {
		h.logger.WithField("object", envelope.Object).Info("meta_inbound: ignoring non-whatsapp object")
		c.JSON(http.StatusOK, gin.H{"status": "ignored", "reason": "non-whatsapp object"})
		return
	}

	parsed, skipped := h.processEntries(&envelope)

	h.logger.WithFields(logrus.Fields{
		"event":            "meta_inbound_processed",
		"entries":          len(envelope.Entry),
		"messages_parsed":  parsed,
		"messages_skipped": skipped,
	}).Info("Meta inbound processed")

	// POC fail-loud: enquanto enqueue real não estiver wired (task #106),
	// devolver 503 quando há QUALQUER mensagem (parsed=text ou skipped=
	// image/audio/etc). Meta vai retentar — sem isso, mensagens seriam
	// silenciosamente descartadas após Meta receber 200. Apenas status
	// callbacks puros (delivered/read) e envelope vazio retornam 200 normal
	// porque não há trabalho de Engine pendente.
	if parsed > 0 || skipped > 0 {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"status":  "not_ready",
			"reason":  "POC parse-only; enqueue pending task #106",
			"parsed":  parsed,
			"skipped": skipped,
		})
		return
	}

	// Meta espera 200 OK rápido — retries agressivos se demorar.
	c.JSON(http.StatusOK, gin.H{
		"status":          "received",
		"messages_parsed": parsed,
	})
}

// validateSignature confere HMAC-SHA256 do body vs header.
// Header shape: "sha256=<hex>".
// Compara em constant-time pra evitar timing attacks.
func (h *MetaWebhookHandler) validateSignature(header string, body []byte) bool {
	const prefix = "sha256="
	if len(header) <= len(prefix) || header[:len(prefix)] != prefix {
		return false
	}
	if h.cfg.AppSecret == "" {
		// Sem AppSecret configurado, rejeita por padrão — fail-closed.
		h.logger.Warn("meta_inbound: AppSecret not configured; rejecting all signatures")
		return false
	}
	expected := computeHMAC(body, h.cfg.AppSecret)
	provided, err := hex.DecodeString(header[len(prefix):])
	if err != nil {
		return false
	}
	return hmac.Equal(expected, provided)
}

func computeHMAC(body []byte, secret string) []byte {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(body)
	return mac.Sum(nil)
}

// processEntries percorre envelope. Retorna (parsed_count, skipped_count).
// parsed = mensagens text válidas que SERÃO enfileiradas (task #106).
// skipped = tipos ainda não suportados (image/audio/video/etc).
func (h *MetaWebhookHandler) processEntries(
	envelope *models.MetaWebhookEnvelope,
) (parsed, skipped int) {
	for _, entry := range envelope.Entry {
		for _, change := range entry.Changes {
			if change.Field != "messages" {
				continue
			}
			for i := range change.Value.Messages {
				msg := &change.Value.Messages[i]
				if _, ok := h.routeMessage(&change.Value, msg); ok {
					parsed++
				} else {
					skipped++
				}
			}
			// Status callbacks (delivered/read) só logados no POC.
			for _, st := range change.Value.Statuses {
				h.logger.WithFields(logrus.Fields{
					"event":     "meta_status_callback",
					"wamid":     st.ID,
					"status":    st.Status,
					"recipient": st.RecipientID,
				}).Debug("Meta status callback (logged only in POC)")
			}
		}
	}
	return parsed, skipped
}

// routeMessage converte 1 mensagem Meta → UserWebhookRequest.
//
// POC `feat/meta-direct-poc`: o enqueue real (RabbitMQ + Engine call) NÃO
// está implementado ainda — é fase posterior da migração. Retornar 200 OK
// faria Meta marcar como entregue + parar retries → silent drop. Retornar
// erro força Meta a tentar de novo, mas isso também é ruído se a feature
// flag estiver ativada antes da hora.
//
// Solução pragmática neste POC: handler **NÃO É WIRED** ao MessageHandler
// real até task #106 (full coverage). A função aqui apenas extrai o shape
// (validando parsing) e devolve a `UserWebhookRequest` montado pro caller.
// `HandleInbound` decide o status code: até implementação completa,
// retorna 503 pra qualquer mensagem real (Meta tentará de novo), evitando
// silent drop em produção. Quando o caller `HandleInbound` for atualizado
// pra fazer enqueue real (task #106), trocar pra 200.
func (h *MetaWebhookHandler) routeMessage(
	value *models.MetaChangeValue, msg *models.MetaMessage,
) (*models.UserWebhookRequest, bool) {
	if msg.Type != "text" {
		h.logger.WithFields(logrus.Fields{
			"event": "meta_inbound_unsupported_type",
			"type":  msg.Type,
			"wamid": msg.ID,
		}).Info("Meta inbound: non-text type not yet supported")
		return nil, false
	}
	if msg.Text == nil || msg.Text.Body == "" {
		h.logger.WithField("wamid", msg.ID).Warn("meta_inbound: empty text body")
		return nil, false
	}

	req := &models.UserWebhookRequest{
		UserNumber: msg.From,
		Message:    msg.Text.Body,
		Metadata: map[string]interface{}{
			"source":          "meta_direct",
			"wamid":           msg.ID,
			"meta_timestamp":  msg.Timestamp,
			"meta_phone_id":   value.Metadata.PhoneNumberID,
			"meta_display_id": value.Metadata.DisplayPhoneNumber,
		},
	}

	h.logger.WithFields(logrus.Fields{
		"event":       "meta_inbound_text_parsed",
		"user_number": req.UserNumber,
		"wamid":       msg.ID,
		"text_length": len(msg.Text.Body),
	}).Info("Meta inbound text parsed (enqueue pending task #106)")
	return req, true
}
