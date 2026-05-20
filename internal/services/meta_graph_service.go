// Package services — Meta Graph API client for outbound WhatsApp Business
// messages. POC scope (`feat/meta-direct-poc`): text-only send.
//
// Equivalente ao Mule `send-via-meta-graph-flow` em `webhook-flow.xml`. Quando
// Gateway absorver completamente o Mule, este client cobrirá também media
// upload + send (image/audio/video/document/sticker), location, template,
// interactive — fase posterior do migration plan.
//
// Endpoint Meta:
//
//	POST https://graph.facebook.com/{api_version}/{phone_number_id}/messages
//	Authorization: Bearer {system_user_token}
//	Content-Type: application/json
//
// Retries: o caller (worker) já tem retry/backoff; este client só faz 1
// tentativa por chamada. Erros 5xx do Meta retornam erro pro caller decidir.
package services

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/prefeitura-rio/app-eai-agent-gateway/internal/config"
)

// MetaGraphService — client HTTP para Meta WhatsApp Business Cloud API.
type MetaGraphService struct {
	cfg        *config.MetaConfig
	httpClient *http.Client
	logger     *logrus.Logger
}

// NewMetaGraphService constrói o client. Timeout 15s alinhado com Mule
// `send-via-meta-graph-flow` (config: 15s connect + 15s response).
func NewMetaGraphService(cfg *config.MetaConfig, logger *logrus.Logger) *MetaGraphService {
	return &MetaGraphService{
		cfg: cfg,
		httpClient: &http.Client{
			Timeout: 15 * time.Second,
		},
		logger: logger,
	}
}

// metaSendTextRequest — payload mínimo Meta /messages pra texto.
type metaSendTextRequest struct {
	MessagingProduct string          `json:"messaging_product"`
	RecipientType    string          `json:"recipient_type"`
	To               string          `json:"to"`
	Type             string          `json:"type"`
	Text             metaTextPayload `json:"text"`
}

type metaTextPayload struct {
	PreviewURL bool   `json:"preview_url"`
	Body       string `json:"body"`
}

// MetaSendResponse — shape do success response do Meta /messages.
type MetaSendResponse struct {
	MessagingProduct string                  `json:"messaging_product"`
	Contacts         []MetaSendContactRef    `json:"contacts"`
	Messages         []MetaSendMessageResult `json:"messages"`
}

// MetaSendContactRef — eco do destinatário (Meta normaliza).
type MetaSendContactRef struct {
	Input string `json:"input"`
	WAID  string `json:"wa_id"`
}

// MetaSendMessageResult — id retornado por mensagem enviada (wamid).
type MetaSendMessageResult struct {
	ID string `json:"id"`
}

// SendText envia mensagem texto pro Meta Graph API.
//
// Retorna wamid em sucesso ou erro descritivo. Não faz retry — caller decide.
// PII (texto) NUNCA aparece em log; só nome + len + status code.
func (s *MetaGraphService) SendText(ctx context.Context, recipient, body string) (string, error) {
	if !s.cfg.Enabled {
		return "", fmt.Errorf("meta direct integration disabled (META_DIRECT_ENABLED=false)")
	}
	if s.cfg.SystemUserToken == "" || s.cfg.PhoneNumberID == "" {
		return "", fmt.Errorf("meta credentials missing (SystemUserToken or PhoneNumberID empty)")
	}
	if recipient == "" || body == "" {
		return "", fmt.Errorf("recipient and body must be non-empty")
	}

	reqBody := metaSendTextRequest{
		MessagingProduct: "whatsapp",
		RecipientType:    "individual",
		To:               recipient,
		Type:             "text",
		Text:             metaTextPayload{PreviewURL: false, Body: body},
	}

	payload, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("marshal payload: %w", err)
	}

	url := fmt.Sprintf(
		"https://graph.facebook.com/%s/%s/messages",
		s.cfg.GraphAPIVersion, s.cfg.PhoneNumberID,
	)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+s.cfg.SystemUserToken)
	req.Header.Set("Content-Type", "application/json")

	start := time.Now()
	resp, err := s.httpClient.Do(req)
	if err != nil {
		s.logger.WithFields(logrus.Fields{
			"event":    "meta_graph_send_error",
			"recipient_prefix": recipientPrefix(recipient),
			"duration_ms":      time.Since(start).Milliseconds(),
			"error":            err.Error(),
		}).Error("Meta Graph send failed")
		return "", fmt.Errorf("http send: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)

	logFields := logrus.Fields{
		"event":            "meta_graph_send",
		"recipient_prefix": recipientPrefix(recipient),
		"status_code":      resp.StatusCode,
		"duration_ms":      time.Since(start).Milliseconds(),
		"body_length":      len(body),
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// Snippet do erro pra debug — Meta retorna error.message útil mas
		// pode conter PII; truncar pra primeiros 200 chars.
		errSnippet := string(respBody)
		if len(errSnippet) > 200 {
			errSnippet = errSnippet[:200] + "...(truncated)"
		}
		logFields["error_body"] = errSnippet
		s.logger.WithFields(logFields).Error("Meta Graph send non-2xx")
		return "", fmt.Errorf("meta returned status %d: %s", resp.StatusCode, errSnippet)
	}

	var sendResp MetaSendResponse
	if err := json.Unmarshal(respBody, &sendResp); err != nil {
		s.logger.WithFields(logFields).WithError(err).Error("Meta Graph response parse failed")
		return "", fmt.Errorf("parse response: %w", err)
	}

	if len(sendResp.Messages) == 0 || sendResp.Messages[0].ID == "" {
		s.logger.WithFields(logFields).Warn("Meta Graph success response without wamid")
		return "", fmt.Errorf("no wamid in response")
	}

	wamid := sendResp.Messages[0].ID
	logFields["wamid"] = wamid
	s.logger.WithFields(logFields).Info("Meta Graph send success")
	return wamid, nil
}

// recipientPrefix mascara telefone pra log (LGPD/PII).
// "5521965850470" → "55219658…" (8 chars + suffix).
func recipientPrefix(phone string) string {
	if len(phone) <= 8 {
		return phone
	}
	return phone[:8] + "…"
}
