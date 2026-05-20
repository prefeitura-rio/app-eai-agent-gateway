// Package handlers — Meta outbound dispatcher.
//
// `/meta/dispatch` é o callback URL interno apontado por MetaWebhookHandler
// quando inbound chega via /meta/webhook. Quando o worker termina de processar
// a mensagem, ele POSTa o resultado nesse endpoint, e o handler aqui chama
// MetaGraphService.SendText (ou SendMedia) pra entregar a resposta ao cidadão
// via Meta Graph API.
//
// Equivalente ao Mule `process-langgraph-callback` → `route-langgraph-response-flow`
// → `send-via-meta-graph-flow`. Fluxo:
//
//	Engine → callback → /meta/dispatch
//	  body: { message_id, status, data: {messages:[{content,...}], ...}, ... }
//	  ↓
//	  Gateway lookup user_number do Redis (task:metadata:{message_id})
//	  ↓
//	  MetaGraphService.SendText(user_number, content)
package handlers

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"

	"github.com/prefeitura-rio/app-eai-agent-gateway/internal/models"
	"github.com/prefeitura-rio/app-eai-agent-gateway/internal/services"
)

// dispatchSentMarkerTTL — TTL do marker "já enviado por message_id" no
// Redis. Pra duplicates do worker callback (e.g. HTTP timeout no client
// fazendo retry), o handler checa esse marker antes de chamar Meta de novo.
const dispatchSentMarkerTTL = 24 * time.Hour

// secureEqual compara duas strings em tempo constante (proteção contra
// timing-attack em validação de secret).
func secureEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// MetaSender — contrato mínimo pra dispatcher. MetaGraphService satisfaz
// essa interface. Permite mock em testes sem subir HTTP fake completo.
type MetaSender interface {
	SendText(ctx context.Context, recipient, body string) (string, error)
}

// SendClaimer — interface estendida pra dedup atômico do dispatch path.
// Implementada por RedisService (SetNX + Set + Delete).
type SendClaimer interface {
	RedisServiceInterface
	SetNX(ctx context.Context, key string, value string, ttl time.Duration) (bool, error)
	Delete(ctx context.Context, key string) error
}

// MetaDispatchHandler — recebe callbacks do worker pra outbound Meta direto.
type MetaDispatchHandler struct {
	sender MetaSender
	redis  RedisServiceInterface
	// claimer é o subset de Redis com SETNX atômico. Pode ser nil em
	// configurações degraded (handler usa best-effort Get+Set).
	claimer SendClaimer
	// secret é o shared secret esperado em `X-Meta-Dispatch-Secret`. Vazio
	// ou placeholder = fail-closed (endpoint rejeita 401).
	secret string
	logger *logrus.Logger
}

const dispatchSecretHeader = "X-Meta-Dispatch-Secret"

// NewMetaDispatchHandler constrói handler. Se sender é nil, retorna 503
// em runtime (Meta direct outbound desativado). Se secret vazio, endpoint
// é fail-closed (todas chamadas viram 401). Se redis implementa SendClaimer
// (RedisService faz), dispatch usa SETNX atômico pra dedup; senão cai pra
// Get+Set (best-effort, vulnerável a duplicate em concurrent retries).
func NewMetaDispatchHandler(sender MetaSender, redis RedisServiceInterface, secret string, logger *logrus.Logger) *MetaDispatchHandler {
	h := &MetaDispatchHandler{sender: sender, redis: redis, secret: secret, logger: logger}
	if c, ok := redis.(SendClaimer); ok {
		h.claimer = c
	}
	return h
}

// MetaDispatchPayload — shape do POST que o worker envia. Match com
// models.CallbackPayload (sent by callback_service) mas com tipagem
// laxa nos campos data/messages porque o callback é genérico.
type MetaDispatchPayload struct {
	MessageID string                 `json:"message_id"`
	Status    string                 `json:"status"`
	Data      map[string]interface{} `json:"data"`
	Error     *string                `json:"error,omitempty"`
}

// HandleDispatch — entrypoint do callback. Resolve user_number via Redis
// (worker armazena em `task:metadata:{message_id}`) e chama Meta Graph.
//
// Auth FIRST: rejeita 401 se header `X-Meta-Dispatch-Secret` não bate com
// o secret configurado (ou se secret está vazio/placeholder). Sem isso,
// qualquer caller com um message_id válido (que vem da resposta do webhook
// inbound) poderia disparar SendText arbitrário via Meta credentials.
func (h *MetaDispatchHandler) HandleDispatch(c *gin.Context) {
	if h.secret == "" || h.secret == "REPLACE_VIA_RUNTIME_MANAGER_PROPERTIES" {
		h.logger.Warn("meta_dispatch: secret not configured (fail-closed)")
		c.JSON(http.StatusUnauthorized, gin.H{"error": "dispatch secret not configured"})
		return
	}
	provided := c.GetHeader(dispatchSecretHeader)
	if provided == "" || !secureEqual(provided, h.secret) {
		h.logger.WithField("event", "meta_dispatch_unauthorized").Warn("Meta dispatch: invalid or missing secret header")
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	if h.sender == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "meta sender not configured"})
		return
	}

	var payload MetaDispatchPayload
	if err := c.ShouldBindJSON(&payload); err != nil {
		h.logger.WithError(err).Warn("meta_dispatch: invalid JSON")
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid json"})
		return
	}

	if payload.MessageID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "message_id required"})
		return
	}

	// Status diferente de completed: nada a enviar. Worker pode chamar com
	// status=failed; logamos pra observabilidade mas não tentamos outbound.
	if payload.Status != string(models.TaskStatusCompleted) {
		h.logger.WithFields(logrus.Fields{
			"event":      "meta_dispatch_non_completed",
			"message_id": payload.MessageID,
			"status":     payload.Status,
		}).Info("Meta dispatch: non-completed status, skip outbound")
		c.JSON(http.StatusOK, gin.H{"status": "noop"})
		return
	}

	// Lookup user_number salvo pelo MessageHandler.EnqueueUserMessage em
	// task:metadata:{id} (JSON: {user_number, provider}).
	ctx := c.Request.Context()

	// Dedup send-once: claim atômico no Redis evita duplicar SendText quando
	// o worker retenta o callback (e.g. HTTP timeout no client após Meta
	// aceitar). WhatsApp send não é idempotente.
	//
	// Estados:
	//   - "inflight": SendText em progresso (set por nós antes da chamada).
	//     Concurrent retries veem isso e retornam 503 (worker retenta após).
	//   - "<wamid>": SendText completou. Concurrent retries retornam 200 sem
	//     reenviar (idempotência efetiva).
	//   - missing: ninguém tentou ainda; claim com SetNX.
	//
	// Sem SETNX (claimer nil), cai pra Get+Set best-effort (vulnerável a race).
	sentKey := "meta:dispatch:sent:" + payload.MessageID
	claimed := false
	if h.claimer != nil {
		ok, err := h.claimer.SetNX(ctx, sentKey, dedupValueInflight, dispatchSentMarkerTTL)
		if err != nil {
			h.logger.WithError(err).WithField("message_id", payload.MessageID).
				Error("meta_dispatch: SETNX claim failed (transient)")
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "redis claim failed"})
			return
		}
		if !ok {
			existing, _ := h.redis.Get(ctx, sentKey)
			if existing == dedupValueInflight {
				h.logger.WithField("message_id", payload.MessageID).
					Info("meta_dispatch: send in-flight; concurrent request, worker should retry")
				c.JSON(http.StatusServiceUnavailable, gin.H{"error": "in_flight"})
				return
			}
			h.logger.WithField("message_id", payload.MessageID).
				Info("meta_dispatch: already sent; skipping duplicate")
			c.JSON(http.StatusOK, gin.H{"status": "already_sent", "wamid": existing})
			return
		}
		claimed = true
		// Release claim em caso de falha no SendText abaixo. Sucesso reescreve
		// o value pro wamid (pra concurrent retries verem "done").
		defer func() {
			if !claimed {
				return
			}
			// claimed=true significa que NÃO chegamos no Set final (SendText
			// falhou ou algo entre claim e Set). Liberar pra Meta retentar.
			if err := h.claimer.Delete(ctx, sentKey); err != nil {
				h.logger.WithError(err).WithField("message_id", payload.MessageID).
					Warn("meta_dispatch: failed to release claim on failure path")
			}
		}()
	} else {
		// Fallback non-atomic: Get + Set. Vulnerável a race em concurrent
		// retries, mas funciona em deployments sem RedisService.SetNX.
		if existing, err := h.redis.Get(ctx, sentKey); err == nil && existing != "" {
			h.logger.WithField("message_id", payload.MessageID).
				Info("meta_dispatch: send marker exists (non-atomic); skipping duplicate")
			c.JSON(http.StatusOK, gin.H{"status": "already_sent"})
			return
		} else if err != nil && !errors.Is(err, services.ErrKeyNotFound) {
			h.logger.WithError(err).WithField("message_id", payload.MessageID).
				Error("meta_dispatch: sent marker lookup failed; treat as transient")
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "redis lookup failed"})
			return
		}
	}

	metaKey := "task:metadata:" + payload.MessageID
	metaJSON, err := h.redis.Get(ctx, metaKey)
	if err != nil {
		// Distingue "chave inexistente" (404 — não retentar) de "Redis down"
		// (503 — caller deve retentar). CallbackService trata 4xx (exceto 429)
		// como non-retriable, então classificar erro transitório como 404
		// dropava a resposta permanentemente.
		if errors.Is(err, services.ErrKeyNotFound) {
			h.logger.WithField("message_id", payload.MessageID).
				Warn("meta_dispatch: metadata key missing (likely TTL expired)")
			c.JSON(http.StatusNotFound, gin.H{"error": "user_number not found"})
			return
		}
		h.logger.WithError(err).WithField("message_id", payload.MessageID).
			Error("meta_dispatch: redis lookup failed (transient); caller should retry")
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "redis lookup failed"})
		return
	}
	if metaJSON == "" {
		h.logger.WithField("message_id", payload.MessageID).
			Warn("meta_dispatch: metadata empty")
		c.JSON(http.StatusNotFound, gin.H{"error": "user_number not found"})
		return
	}
	var meta map[string]interface{}
	if err := json.Unmarshal([]byte(metaJSON), &meta); err != nil {
		h.logger.WithError(err).Error("meta_dispatch: failed to parse stored metadata")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "metadata parse"})
		return
	}
	userNumber, _ := meta["user_number"].(string)
	if userNumber == "" {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "user_number empty"})
		return
	}

	// Extrai texto da resposta. Worker storage shape:
	//   data.messages = [{ "content": "...", "role": "ai", ... }, ...]
	// Pega o último (ou apenas o primeiro item se só tem um).
	body := extractMessageText(payload.Data)
	if body == "" {
		h.logger.WithField("message_id", payload.MessageID).
			Warn("meta_dispatch: no text content in callback data; nothing to send")
		c.JSON(http.StatusOK, gin.H{"status": "no_content"})
		return
	}

	wamid, err := h.sender.SendText(ctx, userNumber, body)
	if err != nil {
		h.logger.WithError(err).WithField("message_id", payload.MessageID).
			Error("meta_dispatch: SendText failed")
		c.JSON(http.StatusBadGateway, gin.H{"error": "meta send failed"})
		return
	}

	// Marca sent ANTES de devolver 200. Sobreescreve "inflight" → wamid pra
	// concurrent retries verem "done". Best-effort: erro no Set não bloqueia
	// retorno (retentar 5xx faria worker retry causando o duplicate que
	// estamos tentando evitar). Cancela o defer de release (claim agora é
	// "done" sentinela, não in-flight).
	if setErr := h.redis.Set(ctx, sentKey, wamid, dispatchSentMarkerTTL); setErr != nil {
		h.logger.WithError(setErr).WithField("message_id", payload.MessageID).
			Warn("meta_dispatch: failed to write sent marker; duplicates possible on worker retry")
	}
	// Set sucesso/fail — qualquer caso, NÃO releasar a claim (queremos manter
	// pra dedup de concurrent retries). Flag claimed=false desabilita o defer.
	claimed = false

	h.logger.WithFields(logrus.Fields{
		"event":      "meta_dispatch_sent",
		"message_id": payload.MessageID,
		"wamid":      wamid,
	}).Info("Meta dispatch outbound sent")
	c.JSON(http.StatusOK, gin.H{"status": "sent", "wamid": wamid})
}

// extractMessageText puxa a string de conteúdo do payload do worker.
//
// Worker `transformGoogleAgentMessages` sempre anexa um item
// `usage_statistics` no final do array, então ler simplesmente `[last]`
// inspeciona stats e ignora a resposta real. Scan reverso pulando entries
// sem `content` (ou tipo telemetria) resolve.
//
// Fallback: data.content como string direta (shape mais simples).
func extractMessageText(data map[string]interface{}) string {
	if data == nil {
		return ""
	}
	if rawList, ok := data["messages"]; ok {
		if list, ok := rawList.([]interface{}); ok {
			for i := len(list) - 1; i >= 0; i-- {
				m, ok := list[i].(map[string]interface{})
				if !ok {
					continue
				}
				// Pular usage_statistics e variantes (não são reply real)
				if t, _ := m["type"].(string); t == "usage_statistics" {
					continue
				}
				if _, isStats := m["usage_statistics"]; isStats {
					continue
				}
				if c, ok := m["content"].(string); ok && c != "" {
					return c
				}
			}
		}
	}
	if c, ok := data["content"].(string); ok && c != "" {
		return c
	}
	return ""
}
