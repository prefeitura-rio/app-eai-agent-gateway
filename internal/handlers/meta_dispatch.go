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
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"

	"github.com/prefeitura-rio/app-eai-agent-gateway/internal/models"
	"github.com/prefeitura-rio/app-eai-agent-gateway/internal/services"
)

// dispatchSentMarkerTTL + dedupValue* em meta_dedup_consts.go (compartilhado
// com meta_webhook.go — ambos os keyspaces usam o mesmo conjunto de sentinelas).

// secureEqual compara duas strings em tempo constante (proteção contra
// timing-attack em validação de secret).
//
// Hash SHA256 ambos antes do compare pra eliminar leak de length:
// `subtle.ConstantTimeCompare` retorna 0 imediatamente em length mismatch,
// permitindo atacante deduzir comprimento do secret esperado via timing.
// Hashing aplaina pra 32 bytes em todos os casos.
func secureEqual(a, b string) bool {
	ah := sha256.Sum256([]byte(a))
	bh := sha256.Sum256([]byte(b))
	return subtle.ConstantTimeCompare(ah[:], bh[:]) == 1
}

// MetaSender — contrato pra dispatcher. MetaGraphService satisfaz nativamente.
// Inclui todos os tipos outbound que o Engine pode pedir via tool returns.
// Mock em testes precisa cobrir só os métodos exercitados.
type MetaSender interface {
	SendText(ctx context.Context, recipient, body string) (string, error)
	SendMedia(ctx context.Context, recipient, mediaType string, in services.MediaInput) (string, error)
	SendLocation(ctx context.Context, recipient string, lat, lng float64, name, address string) (string, error)
	SendTemplate(ctx context.Context, recipient, name, langCode string, components []map[string]interface{}) (string, error)
	SendInteractive(ctx context.Context, recipient, subtype string, header, body, footer, action map[string]interface{}) (string, error)
	SendReaction(ctx context.Context, recipient, wamid, emoji string) (string, error)
	UploadMedia(ctx context.Context, mimeType string, content []byte, filename string) (string, error)
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
	// Compare uniforme em TODOS os paths (incluindo header vazio) via secureEqual.
	// SHA256 dentro de secureEqual aplaina length, e o compare é constant-time.
	// Early-return em provided=="" introduzia timing diff distinguível.
	provided := c.GetHeader(dispatchSecretHeader)
	if !secureEqual(provided, h.secret) {
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
	//   - "done": fallback terminal quando Set(wamid) falha pós-send OK.
	//   - "no_content": payload sem texto nem media — terminal.
	//   - missing: ninguém tentou ainda; claim com SetNX.
	//
	// Sem SETNX (claimer nil), cai pra Get+Set best-effort (vulnerável a race).
	//
	// ORDEM: claim ANTES do metadata lookup é intencional. Inverter (lookup
	// primeiro) abriria janela onde 2 workers concorrentes ambos passam pelo
	// Get OK + ambos fazem SendText → duplicate ao cidadão. Trade-off: se o
	// metadata expirou (404), o sentKey fica "inflight" até o defer liberar
	// (~µs); concurrent retry nesse intervalo vê inflight e retorna 503, é OK
	// porque vai retentar próximo round.
	sentKey := "meta:dispatch:sent:" + payload.MessageID
	// success default-false: defer libera claim em qualquer return early
	// (failure path SendText, panic, etc.). Apenas paths que PRESERVAM
	// a claim (Set wamid bem-sucedido OU no_content — significa "vimos
	// esse message_id e decidimos não enviar; concurrent retry deve ver
	// inflight e ser deduped") setam success=true antes do return.
	// Padrão é mais defensivo a edits futuros que adicionariam novas
	// branches de retorno entre o claim e o Set final.
	success := false
	if h.claimer != nil {
		ok, err := h.claimer.SetNX(ctx, sentKey, dedupValueInflight, dispatchSentMarkerTTL)
		if err != nil {
			h.logger.WithError(err).WithField("message_id", payload.MessageID).
				Error("meta_dispatch: SETNX claim failed (transient)")
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "redis claim failed"})
			return
		}
		if !ok {
			// SETNX false: a chave existia no momento da chamada. Inspecionar
			// estado via Get pra discriminar in-flight / terminal / evict.
			existing, getErr := h.redis.Get(ctx, sentKey)
			if errors.Is(getErr, services.ErrKeyNotFound) {
				// Race: chave evicted (Redis LRU pressure) ou TTL expirou
				// entre SETNX false e Get. Sem claim e sem estado terminal —
				// retornar 503 pra worker retentar; SETNX limpo no próximo
				// round resolve. Sem isso, 200 com wamid vazio mascara o caso
				// e o cidadão não recebe a resposta.
				h.logger.WithField("message_id", payload.MessageID).
					Warn("meta_dispatch: SETNX false but Get not-found (Redis LRU evict/race); will return 503 for retry")
				c.JSON(http.StatusServiceUnavailable, gin.H{"error": "claim state lost; retry"})
				return
			}
			if getErr != nil {
				h.logger.WithError(getErr).WithField("message_id", payload.MessageID).
					Error("meta_dispatch: SETNX false + Get failed (Redis transient); will return 503")
				c.JSON(http.StatusServiceUnavailable, gin.H{"error": "redis lookup failed"})
				return
			}
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
		// Release claim em failure paths via defer com ctx independente
		// (request ctx do gin pode estar cancelado quando o defer roda).
		defer func() {
			if success {
				return
			}
			releaseCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			if err := h.claimer.Delete(releaseCtx, sentKey); err != nil {
				h.logger.WithError(err).WithField("message_id", payload.MessageID).
					Warn("meta_dispatch: failed to release claim on failure path")
			}
		}()
	} else {
		// Fallback non-atomic: Get + Set. Vulnerável a race em concurrent
		// retries — DOIS workers podem ver Get vazio e ambos fazer SendText.
		//
		// IMPORTANT: este branch é dead code em produção. `RedisService`
		// (o único redis injetado em runtime via DI) satisfaz `SendClaimer`
		// (implementa SetNX + Delete), então a type assertion em
		// NewMetaDispatchHandler sempre seta `h.claimer != nil`. O branch só
		// é exercitado por testes que passam `mockRedisGet` pelado.
		// Mantido como rede de segurança defensiva caso alguém substitua o
		// redis service no futuro. Se este código for invocado em
		// produção, indica regressão arquitetural — log Warn loud.
		h.logger.WithField("message_id", payload.MessageID).
			Warn("meta_dispatch: SendClaimer NÃO disponível — operando em modo non-atomic (risco de duplicate send em concurrent retry)")
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

	// Extrai envelope canônico de tool_return_message (send_whatsapp_media,
	// generate_audio_response, send_whatsapp_flow/_buttons/_list). Quando
	// presente, roteia por tipo. Quando ausente, cai pra texto puro do reply.
	envelope := ExtractAgentMedia(payload.Data)

	var (
		wamid    string
		sendKind string
		sendErr  error
	)
	if envelope.Type != "" {
		sendKind = envelope.Type
		wamid, sendErr = h.sendByEnvelope(ctx, userNumber, envelope)
	} else {
		body := extractMessageText(payload.Data)
		if body == "" {
			h.logger.WithField("message_id", payload.MessageID).
				Warn("meta_dispatch: no text content and no media envelope; nothing to send")
			// Sobreescrever marker `inflight` → sentinela terminal ("no_content")
			// pra dedupar concurrent retries do mesmo message_id sem deixá-los
			// presos em 503 até o TTL de 24h (`existing == dedupValueInflight`
			// no branch acima). Concurrent retry vê valor diferente de inflight
			// e cai no path "already_sent" 200. Se o Set falhar (Redis blip), NÃO
			// preservamos a claim — deixar o defer liberar pra retry refazer com
			// estado limpo, evita ficar travado em inflight por 24h. Ctx
			// independente (não o request ctx do gin) pra sobreviver a cliente
			// desconectado pós-decisão — mesma justificativa do defer release.
			markerCtx, markerCancel := context.WithTimeout(context.Background(), 2*time.Second)
			setErr := h.redis.Set(markerCtx, sentKey, dedupValueNoContent, dispatchSentMarkerTTL)
			markerCancel()
			if setErr != nil {
				h.logger.WithError(setErr).WithField("message_id", payload.MessageID).
					Warn("meta_dispatch: failed to write no_content marker; releasing claim via defer")
			} else {
				success = true
			}
			c.JSON(http.StatusOK, gin.H{"status": "no_content"})
			return
		}
		sendKind = "text"
		wamid, sendErr = h.sender.SendText(ctx, userNumber, body)
	}

	if sendErr != nil {
		h.logger.WithError(sendErr).WithFields(logrus.Fields{
			"message_id": payload.MessageID,
			"send_kind":  sendKind,
		}).Error("meta_dispatch: Send failed")
		c.JSON(http.StatusBadGateway, gin.H{"error": "meta send failed"})
		return
	}

	// Marca sent ANTES de devolver 200. Sobreescreve "inflight" → wamid pra
	// concurrent retries verem "done". Ctx independente — cliente pode estar
	// desconectado quando o Set roda (mesmo motivo do defer release).
	//
	// Quando Set falha (Redis blip pós-send OK), NÃO podemos:
	//   1. Deixar `success=true` cego — claim fica "inflight" 24h, retries do
	//      callback_service caem em 503 indefinidamente e viram falso-positivo
	//      DLQ.
	//   2. Deletar a claim — concurrent retry chamaria Meta DE NOVO (mensagem
	//      JÁ foi entregue ao cidadão); duplicate send é o cenário pior.
	// Fallback: tentar Set com sentinel `dedupValueDone` (estado terminal sem
	// o wamid específico). Concurrent retries veem "done" → 200 already_sent
	// em vez de 503 ou re-send. Se AMBOS falharem (Redis fora prolongado),
	// log Error + claim é deletada pelo defer — aceita o risco de duplicate
	// como cenário extremo (operador atua via Redis flush manual).
	sentCtx, sentCancel := context.WithTimeout(context.Background(), 2*time.Second)
	markerErr := h.redis.Set(sentCtx, sentKey, wamid, dispatchSentMarkerTTL)
	sentCancel()
	if markerErr != nil {
		fallbackCtx, fallbackCancel := context.WithTimeout(context.Background(), 2*time.Second)
		fallbackErr := h.redis.Set(fallbackCtx, sentKey, dedupValueDone, dispatchSentMarkerTTL)
		fallbackCancel()
		if fallbackErr != nil {
			h.logger.WithFields(logrus.Fields{
				"message_id":   payload.MessageID,
				"wamid":        wamid,
				"marker_err":   markerErr.Error(),
				"fallback_err": fallbackErr.Error(),
			}).Error("meta_dispatch: BOTH wamid Set AND fallback 'done' Set failed; claim será deletada pelo defer — RISCO de duplicate send se Meta retentar callback")
			// success continua false → defer Delete libera a claim.
		} else {
			h.logger.WithError(markerErr).WithField("message_id", payload.MessageID).
				Warn("meta_dispatch: wamid Set failed; fallback 'done' marker armazenado — concurrent retries verão already_sent")
			success = true
		}
	} else {
		success = true
	}

	h.logger.WithFields(logrus.Fields{
		"event":      "meta_dispatch_sent",
		"message_id": payload.MessageID,
		"wamid":      wamid,
		"send_kind":  sendKind,
	}).Info("Meta dispatch outbound sent")
	c.JSON(http.StatusOK, gin.H{"status": "sent", "wamid": wamid, "kind": sendKind})
}

// sendByEnvelope roteia o envelope canônico pro método apropriado do sender.
// Cada tipo aceita um subset distinto de campos; o helper mapeia + valida
// mínimamente. Erros são propagados puros pra caller decidir HTTP status.
func (h *MetaDispatchHandler) sendByEnvelope(
	ctx context.Context, recipient string, env AgentMediaEnvelope,
) (string, error) {
	switch env.Type {
	case "audio", "image", "video", "document", "sticker":
		// Roteia por canal preferido:
		//   1. URL direto (link público): Meta busca o conteúdo
		//   2. Base64 inline: Gateway faz upload pra /media e usa media_id
		// Sem nenhum dos dois, SendMedia retornaria erro genérico; tratamos
		// explicitamente aqui.
		input := services.MediaInput{
			Caption:  env.Caption,
			Filename: env.Filename,
			Voice:    env.Voice,
		}
		switch {
		case env.URL != "":
			input.Link = env.URL
		case env.Base64 != "":
			content, err := services.DecodeBase64(env.Base64)
			if err != nil {
				return "", fmt.Errorf("decode base64 audio: %w", err)
			}
			mediaID, err := h.sender.UploadMedia(ctx, env.MimeType, content, env.Filename)
			if err != nil {
				return "", fmt.Errorf("upload media to Meta: %w", err)
			}
			input.ID = mediaID
		default:
			return "", errors.New("media envelope has neither url nor base64; nothing to send")
		}
		return h.sender.SendMedia(ctx, recipient, env.Type, input)

	case "location":
		if env.Latitude == nil || env.Longitude == nil {
			return "", errors.New("location envelope missing latitude or longitude")
		}
		return h.sender.SendLocation(ctx, recipient, *env.Latitude, *env.Longitude, env.Name, env.Address)

	case "template":
		if env.Template == nil {
			return "", errors.New("template envelope missing template object")
		}
		return h.sender.SendTemplate(ctx, recipient, env.Template.Name, env.Template.LangCode, env.Template.Components)

	case "interactive":
		if env.Interactive == nil {
			return "", errors.New("interactive envelope missing interactive object")
		}
		return h.sender.SendInteractive(ctx, recipient, env.Interactive.Subtype,
			env.Interactive.Header, env.Interactive.Body, env.Interactive.Footer, env.Interactive.Action)

	case "reaction":
		if env.ReactionToMessageID == "" {
			return "", errors.New("reaction envelope missing reaction_to_message_id")
		}
		return h.sender.SendReaction(ctx, recipient, env.ReactionToMessageID, env.Emoji)

	default:
		return "", errors.New("unsupported envelope type: " + env.Type)
	}
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
				// Defesa em profundidade: aceitar apenas message_type vazio
				// (compat com shape legacy) ou explicitamente "assistant_message".
				// tool_call_message, tool_return_message, reasoning_message
				// carregam JSON cru no `content` — enviar isso pra Meta como
				// texto vazaria internals ao cidadão. ExtractAgentMedia já lida
				// com o caso assistant + envelope; este fallback só roda quando
				// envelope.Type == "" (defensivo).
				if mt, _ := m["message_type"].(string); mt != "" && mt != "assistant_message" {
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
