// Package handlers — Admin broadcast endpoint (POC pra ADR-028 opção (a)).
//
// `POST /admin/broadcast` aceita um JSON envelope:
//
//	{
//	  "items": [
//	    {"phone": "5521...", "template_name": "saudacao",
//	     "language_code": "pt_BR", "components": [...] }
//	  ],
//	  "rate_per_second": 50
//	}
//
// e dispara `MetaGraphService.SendTemplate` pra cada um. Pensado pra
// substituir Marketing Cloud Journeys de broadcast simples (alertas,
// saudações, notice em massa) quando Prefeitura cortar SF/MC.
//
// **POC sync limit:** batches devem completar em <25s (HTTP write_timeout
// default 30s). Estimate factor: len(items) * MAX(1/rate, 1s avg). Pra
// campanhas de massa, async dispatch necessária (out-of-scope POC).
//
// Out-of-scope deste POC:
//   - Segmentação avançada (Data Extension queries) → caller envia lista resolvida
//   - Journey builder visual → caller usa script/UI próprio
//   - A/B testing → não suportado
//   - Opt-out tracking → caller checa antes de enviar
//
// Quando Prefeitura escolher MC alternative (a), este endpoint vira o
// substituto. Quando escolher (c) manter MC, este endpoint fica dormente.
package handlers

import (
	"context"
	"crypto/subtle"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"
)

// BroadcastSender — contrato mínimo (subset de MetaSender pra SendTemplate).
type BroadcastSender interface {
	SendTemplate(ctx context.Context, recipient, name, langCode string, components []map[string]interface{}) (string, error)
}

const (
	broadcastSecretHeader = "X-Broadcast-Secret"
	// Meta WhatsApp Cloud API tem rate limit ~80 messages/sec per WABA na
	// tier business; default conservative pra evitar throttle.
	broadcastDefaultRateLimit = 50
	// Limite hard de items por request, calculado a partir do per-send
	// timeout REAL pra evitar HTTP write_timeout breach.
	//
	// Constraints:
	//   - SendTemplate context timeout (worst case Meta): 15s
	//   - HTTP write_timeout default: 30s
	//   - Buffer pra c.JSON serialização + network: 5s
	//
	// Max items = (30 - 5) / 15 = ~1 item no worst case.
	// Sync POC = 1 item por request. Pra broadcast em massa, async é
	// obrigatório (out-of-scope POC). Estimate check abaixo (com avg=1s)
	// previne batches médios estourarem; este cap previne worst case.
	broadcastMaxItems = 1
)

// BroadcastItem é uma entrada da lista de envio. `Phone` em E.164 sem '+'.
// `Components` segue shape Meta — typically `[{"type":"body","parameters":[{"type":"text","text":"..."}]}]`.
type BroadcastItem struct {
	Phone         string                   `json:"phone" binding:"required"`
	TemplateName  string                   `json:"template_name" binding:"required"`
	LanguageCode  string                   `json:"language_code" binding:"required"`
	Components    []map[string]interface{} `json:"components,omitempty"`
}

// BroadcastRequest é o payload completo. `RatePerSecond` opcional limita
// taxa (default broadcastDefaultRateLimit).
type BroadcastRequest struct {
	Items         []BroadcastItem `json:"items" binding:"required,min=1"`
	RatePerSecond int             `json:"rate_per_second,omitempty"`
}

// BroadcastResult é o sumário retornado após processar tudo.
type BroadcastResult struct {
	Total     int                    `json:"total"`
	Succeeded int                    `json:"succeeded"`
	Failed    int                    `json:"failed"`
	Items     []BroadcastItemResult  `json:"items"`
}

// BroadcastItemResult é o status por item. `Wamid` populado em sucesso,
// `Error` em falha. Phone presente em ambos pra audit. PII mascara em logs.
type BroadcastItemResult struct {
	Phone string `json:"phone"`
	Wamid string `json:"wamid,omitempty"`
	Error string `json:"error,omitempty"`
}

// AdminBroadcastHandler — endpoint POST /admin/broadcast.
type AdminBroadcastHandler struct {
	sender BroadcastSender
	secret string
	logger *logrus.Logger
}

// NewAdminBroadcastHandler constrói o handler. Se sender é nil OU secret
// vazio, todas as requests retornam 503 / 401 respectivamente.
func NewAdminBroadcastHandler(sender BroadcastSender, secret string, logger *logrus.Logger) *AdminBroadcastHandler {
	return &AdminBroadcastHandler{sender: sender, secret: secret, logger: logger}
}

// HandleBroadcast — entrypoint. Auth via X-Broadcast-Secret (fail-closed
// se secret vazio ou placeholder). Dispatch sequencial com rate limit via
// ticker (não goroutines paralelas — caller fica esperando até concluir).
func (h *AdminBroadcastHandler) HandleBroadcast(c *gin.Context) {
	// Auth FIRST. Sem secret = sempre 401.
	if h.secret == "" || h.secret == "REPLACE_VIA_RUNTIME_MANAGER_PROPERTIES" {
		h.logger.Warn("admin_broadcast: secret not configured (fail-closed)")
		c.JSON(http.StatusUnauthorized, gin.H{"error": "broadcast secret not configured"})
		return
	}
	provided := c.GetHeader(broadcastSecretHeader)
	if provided == "" || !secureEqualBroadcast(provided, h.secret) {
		h.logger.WithField("event", "admin_broadcast_unauthorized").Warn("Admin broadcast: invalid or missing secret")
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	if h.sender == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "broadcast sender not configured"})
		return
	}

	var req BroadcastRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid json", "detail": err.Error()})
		return
	}
	if len(req.Items) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "items required"})
		return
	}
	if len(req.Items) > broadcastMaxItems {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "too many items",
			"detail": "batch limit exceeded; quebrar em batches menores",
			"max": broadcastMaxItems,
		})
		return
	}

	rate := req.RatePerSecond
	if rate <= 0 {
		rate = broadcastDefaultRateLimit
	}
	if rate > 100 {
		rate = 100 // hard cap pra evitar Meta tier throttle
	}

	// Estimated dispatch time considera DOIS fatores:
	//   1. Ticker pacing: len(items) / rate segundos
	//   2. Meta latency: ~1s avg por SendTemplate (conservador, real é
	//      ~200-500ms mas pode picar a 15s timeout em rede ruim)
	// Tempo total ≈ len(items) * MAX(1/rate, 1s)
	//
	// Servidor HTTP tem write_timeout (default 30s); se batch demora
	// mais, c.JSON é perdido mas msgs FORAM enviadas — caller retentaria
	// causando duplicate. Cap conservador rejeitando batches que estouram.
	//
	// POC sync limit: 20 items por batch (assumindo pior caso 1s/item =
	// 20s, dentro de 25s buffer pra 30s timeout). Production scale exige
	// async dispatch (out of scope deste POC).
	const (
		broadcastAvgSecondsPerSend = 1   // conservador
		broadcastMaxDuration       = 25  // seconds, buffer pra 30s timeout
	)
	pacingSeconds := len(req.Items) / rate
	latencySeconds := len(req.Items) * broadcastAvgSecondsPerSend
	estimatedSeconds := pacingSeconds
	if latencySeconds > estimatedSeconds {
		estimatedSeconds = latencySeconds
	}
	if estimatedSeconds > broadcastMaxDuration {
		c.JSON(http.StatusBadRequest, gin.H{
			"error":               "batch too large for synchronous dispatch",
			"estimated_seconds":   estimatedSeconds,
			"max_duration_secs":   broadcastMaxDuration,
			"items":               len(req.Items),
			"rate_per_second":     rate,
			"avg_seconds_per_send": broadcastAvgSecondsPerSend,
			"hint":                "Quebrar em batches menores. Calculo: len(items) * MAX(1/rate, avg_seconds_per_send) <= 25s. Pra campanhas grandes, considerar fila async (out of scope POC; ver ADR-028).",
		})
		return
	}

	// Validação upfront atomica: SE qualquer item tem field obrigatório
	// vazio, rejeita REQUEST INTEIRO antes de enviar qualquer mensagem.
	// Sem isso, dispatch loop sequencial enviaria os primeiros válidos e
	// só marcaria fail no item bad — gerando broadcast parcial inesperado.
	for i, item := range req.Items {
		if item.Phone == "" || item.TemplateName == "" || item.LanguageCode == "" {
			c.JSON(http.StatusBadRequest, gin.H{
				"error":      "invalid item: missing required field (phone, template_name, language_code)",
				"item_index": i,
				"phone":      item.Phone,
			})
			return
		}
	}

	h.logger.WithFields(logrus.Fields{
		"event":           "admin_broadcast_start",
		"items":           len(req.Items),
		"rate_per_second": rate,
	}).Info("Admin broadcast dispatch")

	result := h.dispatch(c.Request.Context(), req.Items, rate)

	h.logger.WithFields(logrus.Fields{
		"event":     "admin_broadcast_complete",
		"total":     result.Total,
		"succeeded": result.Succeeded,
		"failed":    result.Failed,
	}).Info("Admin broadcast complete")

	c.JSON(http.StatusOK, result)
}

// dispatch é o loop sequencial com pacing. Cancela tudo se ctx Done.
func (h *AdminBroadcastHandler) dispatch(ctx context.Context, items []BroadcastItem, ratePerSecond int) BroadcastResult {
	result := BroadcastResult{
		Total: len(items),
		Items: make([]BroadcastItemResult, 0, len(items)),
	}
	interval := time.Second / time.Duration(ratePerSecond)
	if interval < time.Millisecond {
		interval = time.Millisecond
	}

	var mu sync.Mutex
	appendResult := func(ir BroadcastItemResult) {
		mu.Lock()
		result.Items = append(result.Items, ir)
		mu.Unlock()
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for i := range items {
		select {
		case <-ctx.Done():
			appendResult(BroadcastItemResult{
				Phone: items[i].Phone,
				Error: "context cancelled before send",
			})
			result.Failed++
			continue
		case <-ticker.C:
		}

		item := items[i]
		// Validação básica per-item — Meta rejeitaria mesmo, mas falhar
		// fast economiza chamada HTTP.
		if item.Phone == "" || item.TemplateName == "" || item.LanguageCode == "" {
			appendResult(BroadcastItemResult{
				Phone: item.Phone,
				Error: "missing required field (phone, template_name, language_code)",
			})
			result.Failed++
			continue
		}

		sendCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		wamid, err := h.sender.SendTemplate(sendCtx, item.Phone, item.TemplateName, item.LanguageCode, item.Components)
		cancel()

		if err != nil {
			h.logger.WithError(err).WithFields(logrus.Fields{
				"phone_prefix": maskPhonePublic(item.Phone),
				"template":     item.TemplateName,
			}).Warn("admin_broadcast: SendTemplate failed")
			appendResult(BroadcastItemResult{Phone: item.Phone, Error: err.Error()})
			result.Failed++
			continue
		}

		appendResult(BroadcastItemResult{Phone: item.Phone, Wamid: wamid})
		result.Succeeded++
	}
	return result
}

// secureEqualBroadcast — constant-time compare pra evitar timing attacks.
func secureEqualBroadcast(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// maskPhonePublic — mascara phone E.164 pra logs (LGPD).
// "5521989091014" → "55219890…"
func maskPhonePublic(phone string) string {
	if len(phone) <= 8 {
		return phone
	}
	return phone[:8] + "…"
}

// ErrBroadcastInvalid é exposto pra callers programáticos (futuros) que
// queiram discriminar errors.
var ErrBroadcastInvalid = errors.New("invalid broadcast payload")
