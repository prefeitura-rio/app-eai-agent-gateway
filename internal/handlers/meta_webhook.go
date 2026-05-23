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
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"

	"github.com/prefeitura-rio/app-eai-agent-gateway/internal/config"
	"github.com/prefeitura-rio/app-eai-agent-gateway/internal/models"
	"github.com/prefeitura-rio/app-eai-agent-gateway/internal/services"
)

// MetaDedupChecker — interface mínima que o handler precisa pra idempotência.
//   - `SetNX` faz claim atômico (SET key value NX EX ttl) — retorna true se
//     claim foi adquirida (chave não existia), false se já estava ocupada.
//   - `Get` lê o valor atual da claim. Usado pra discriminar claim in-flight
//     ("inflight") de claim completada ("done").
//   - `Set` upgrade da claim com value novo (de "inflight" pra "done") quando
//     enqueue completa com sucesso. Mesmo TTL que SetNX original.
//   - `Delete` libera a claim quando enqueue falha, pra Meta retry conseguir
//     reprocessar até dedupTTL expirar.
// Implementada pelo RedisService.
type MetaDedupChecker interface {
	SetNX(ctx context.Context, key string, value string, ttl time.Duration) (bool, error)
	Get(ctx context.Context, key string) (string, error)
	Set(ctx context.Context, key string, value string, ttl time.Duration) error
	Delete(ctx context.Context, key string) error
}

// Constantes de dedup (TTL + sentinelas) em meta_dedup_consts.go.

// MessageEnqueuer — contrato mínimo que MetaWebhookHandler precisa pra
// publicar mensagens no MessageHandler. Existe pra permitir mock em testes.
// MessageHandler (concrete) satisfaz essa interface naturalmente.
type MessageEnqueuer interface {
	EnqueueUserMessage(
		ctx context.Context,
		req *models.UserWebhookRequest,
		requestID string,
		traceHeaders map[string]interface{},
		opts EnqueueOptions,
	) (*EnqueueResult, error)
}

// MetaInboundRateLimiter — DoS defense per-E.164 (plano-bot-2026 Fase 0 C6).
// Subset mínimo do contrato services.DoSRateLimiterService usado pelo handler.
// Definido aqui pra evitar import cycle (handlers ↔ services).
type MetaInboundRateLimiter interface {
	AllowMessage(ctx context.Context, e164 string) (allowed bool, retryAfter time.Duration, count int, err error)
}

// TokenBudgetChecker — pre-message gate pra cap diário de tokens
// (plano-bot-2026 Fase 0 I8). Implementado por services.TokenBudgetService.
type TokenBudgetChecker interface {
	IsBudgetExceeded(ctx context.Context, e164 string) (exceeded bool, total int, cap int)
}

// MetaWebhookHandler — recebe inbound webhook Meta direto (substituindo Mule).
type MetaWebhookHandler struct {
	cfg            *config.MetaConfig
	messageHandler MessageEnqueuer
	dedup          MetaDedupChecker
	flowRegistry   *FlowRegistry
	logger         *logrus.Logger
	// rateLimiter aplica cap per-E.164 nas mensagens inbound (DoS defense).
	// Quando nil ou desabilitado, AllowMessage é skipped (backwards-compat).
	rateLimiter MetaInboundRateLimiter
	// tokenBudget checa budget de tokens daily (plano-bot-2026 Fase 0 I8).
	// Quando nil, gate skipped.
	tokenBudget TokenBudgetChecker
}

// NewMetaWebhookHandler constrói o handler injetando deps. messageHandler é
// reutilizado do path /api/v1/message/webhook/user — Meta inbound vira chamada
// equivalente. dedup pode ser nil (modo POC sem Redis); idempotência fica
// desativada nesse caso e Meta retries podem duplicar. messageHandler também
// pode ser nil em testes — handler retorna failed (503) pra qualquer mensagem.
func NewMetaWebhookHandler(
	cfg *config.MetaConfig,
	messageHandler MessageEnqueuer,
	dedup MetaDedupChecker,
	logger *logrus.Logger,
) *MetaWebhookHandler {
	return &MetaWebhookHandler{
		cfg:            cfg,
		messageHandler: messageHandler,
		dedup:          dedup,
		flowRegistry:   NewFlowRegistry(cfg.FlowRegistry, cfg.FlowDefaultService),
		logger:         logger,
	}
}

// WithRateLimiter wire opcional do limiter DoS. Idempotente; chamada
// múltipla substitui referência (último wins).
func (h *MetaWebhookHandler) WithRateLimiter(rl MetaInboundRateLimiter) *MetaWebhookHandler {
	h.rateLimiter = rl
	return h
}

// WithTokenBudget wire opcional do gate de tokens. Idempotente.
func (h *MetaWebhookHandler) WithTokenBudget(tb TokenBudgetChecker) *MetaWebhookHandler {
	h.tokenBudget = tb
	return h
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

	enqueued, dup, skipped, failed := h.processEntries(c.Request.Context(), &envelope)

	h.logger.WithFields(logrus.Fields{
		"event":             "meta_inbound_processed",
		"entries":           len(envelope.Entry),
		"messages_enqueued": enqueued,
		"messages_dup":      dup,
		"messages_skipped":  skipped,
		"messages_failed":   failed,
	}).Info("Meta inbound processed")

	// Se TODAS as mensagens foram enqueued ou dup-skipped: 200. Meta para de
	// retentar. Status callbacks puros (delivered/read) também caem aqui.
	//
	// Se houve `failed > 0` (enqueue Redis/RabbitMQ down) ou `skipped > 0`
	// (tipo ainda não suportado): 503 pra Meta retentar. Isso garante:
	//   - Redis/RabbitMQ flapping → retry recupera sem perda
	//   - tipos novos (sticker, document) ficam in-flight até a gente
	//     implementar suporte; só fica realmente perdido se Meta esgotar
	//     retry window (~24h), e nesse caso o log "_skipped" sinaliza.
	if failed > 0 || skipped > 0 {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"status":   "not_ready",
			"enqueued": enqueued,
			"dup":      dup,
			"skipped":  skipped,
			"failed":   failed,
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"status":   "received",
		"enqueued": enqueued,
		"dup":      dup,
	})
}

// dispatchReady — true se toda a chain pra outbound via /meta/dispatch está
// pronta: callback URL setado + dispatch secret válido + Meta credentials.
// Se incompleto, MetaWebhookHandler entra em "polling-only mode" (não wire
// callback) em vez de devolver 200 ao Meta e silenciar a resposta depois.
//
// Placeholder `REPLACE_VIA_RUNTIME_MANAGER_PROPERTIES` é tratado como vazio
// em TODOS os campos sensíveis. Sem isso, operador que esquece de hidratar
// uma das variáveis no Infisical/SecretManager teria a chain "passando" no
// gate mas falhando no runtime quando Meta Graph rejeita Bearer token literal
// "REPLACE_...".
func (h *MetaWebhookHandler) dispatchReady() bool {
	if h.cfg.SelfCallbackURL == "" ||
		h.cfg.SelfCallbackURL == "REPLACE_VIA_RUNTIME_MANAGER_PROPERTIES" {
		return false
	}
	if h.cfg.DispatchSecret == "" ||
		h.cfg.DispatchSecret == "REPLACE_VIA_RUNTIME_MANAGER_PROPERTIES" {
		return false
	}
	if h.cfg.SystemUserToken == "" ||
		h.cfg.SystemUserToken == "REPLACE_VIA_RUNTIME_MANAGER_PROPERTIES" {
		return false
	}
	if h.cfg.PhoneNumberID == "" ||
		h.cfg.PhoneNumberID == "REPLACE_VIA_RUNTIME_MANAGER_PROPERTIES" {
		return false
	}
	return true
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

// processEntries percorre envelope, dedup por wamid via Redis, parse + enqueue
// no MessageHandler.
//
// Retorna contadores discriminados pra resposta + telemetria:
//
//	enqueued — mensagem nova publicada no RabbitMQ com sucesso
//	dup      — wamid já visto em <dedupTTL; skip silencioso (Meta retry esperado)
//	skipped  — tipo ainda não suportado (sticker, document, contacts, etc.)
//	failed   — Redis/RabbitMQ down ou Engine call falhou no enqueue path
func (h *MetaWebhookHandler) processEntries(
	ctx context.Context,
	envelope *models.MetaWebhookEnvelope,
) (enqueued, dup, skipped, failed int) {
	for _, entry := range envelope.Entry {
		for _, change := range entry.Changes {
			if change.Field != "messages" {
				continue
			}
			for i := range change.Value.Messages {
				msg := &change.Value.Messages[i]
				switch h.handleMessage(ctx, &change.Value, msg) {
				case routeOK:
					enqueued++
				case routeDup:
					dup++
				case routeSkipped:
					skipped++
				case routeFailed:
					failed++
				}
			}
			// Status callbacks (delivered/read/failed): emitir como evento
			// estruturado pra observabilidade + persistir failure status no
			// Redis pra worker/Engine consultarem se quiser. Não dispara
			// Engine outbound — apenas signal.
			for _, st := range change.Value.Statuses {
				lvl := logrus.InfoLevel
				if st.Status == "failed" {
					lvl = logrus.WarnLevel
				}
				h.logger.WithFields(logrus.Fields{
					"event":            "meta_status_callback",
					"wamid":            st.ID,
					"status":           st.Status,
					"recipient_prefix": maskPhone(st.RecipientID),
				}).Log(lvl, "Meta status callback")

				// Persistir delivery status no Redis pra Engine/operadores
				// poderem consultar quando handoff humano precisa saber se
				// notice chegou. Best-effort; falha não bloqueia 200 ao Meta —
				// log Warn pra rastreabilidade caso Redis blip degrade o trail.
				if h.dedup != nil && st.ID != "" {
					key := "meta:status:" + st.ID
					if err := h.dedup.Set(ctx, key, st.Status, dedupTTL); err != nil {
						h.logger.WithError(err).WithFields(logrus.Fields{
							"event":  "meta_status_persist_failed",
							"wamid":  st.ID,
							"status": st.Status,
						}).Warn("meta_inbound: failed to persist status callback (best-effort)")
					}
				}
			}
		}
	}
	return enqueued, dup, skipped, failed
}

// routeOutcome distingue o que aconteceu com cada mensagem inbound.
type routeOutcome int

const (
	routeOK routeOutcome = iota
	routeDup
	routeSkipped
	routeFailed
)

// isFromBotItself — anti-loop guard: detecta inbound originário do próprio
// número WABA do bot. Ocorre quando outbound wamid retorna como webhook
// (raro em produção mas observado em sandboxes); processar como mensagem
// cidadã faria o bot se responder em loop.
//
// Comparação possível APENAS via `metadata.DisplayPhoneNumber` (E.164 sem '+',
// mesmo formato de `msg.From`). `metadata.PhoneNumberID` é id interno da WABA
// (numérico, "1112484171945154"), nunca bate com From. Logo é um guard
// best-effort: se Meta entregar webhook com `DisplayPhoneNumber` vazio
// (observado em alguns sandboxes), o guard silencia — registramos Warn pra
// dar visibilidade ao operador.
func (h *MetaWebhookHandler) isFromBotItself(value *models.MetaChangeValue, msg *models.MetaMessage) bool {
	if msg == nil || msg.From == "" {
		return false
	}
	from := normalizeDigits(msg.From)
	if from == "" {
		return false
	}
	displayDigits := normalizeDigits(value.Metadata.DisplayPhoneNumber)
	if displayDigits == "" {
		// Sandbox/edge case: Meta omitiu DisplayPhoneNumber. Sem ele não
		// dá pra comparar E.164 confiável (PhoneNumberID é id interno).
		// Log Warn pra operador notar se isso virar comum em prod.
		h.logger.WithFields(logrus.Fields{
			"event":            "meta_inbound_anti_loop_degraded",
			"wamid":            msg.ID,
			"phone_number_id":  value.Metadata.PhoneNumberID,
			"reason":           "DisplayPhoneNumber vazio, guard desativado pra essa msg",
		}).Warn("meta_inbound: anti-loop guard cannot compare; allowing message through")
		return false
	}
	return displayDigits == from
}

// normalizeDigits remove tudo exceto dígitos. "55 21 9 8909-1014" → "5521989091014".
func normalizeDigits(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		if s[i] >= '0' && s[i] <= '9' {
			out = append(out, s[i])
		}
	}
	return string(out)
}

// maskPhone mascara LGPD: "5521989091014" → "55219890…". Para status
// callbacks logging onde recipient não-PII é suficiente.
func maskPhone(phone string) string {
	digits := normalizeDigits(phone)
	if len(digits) <= 8 {
		return digits
	}
	return digits[:8] + "…"
}

// handleMessage: claim atômico (SETNX) → parse → enqueue → release on failure.
//
// Ordem corrige duas condições de corrida apontadas pelo codex P1/P2:
//
//   1. Get+Set race — duas Meta retries simultâneas observavam ambos "no key"
//      e enqueueavam duplicado. Substituído por SETNX atômico.
//   2. Claim-before-enqueue trap — se enqueue falhava após o set, Meta retry
//      hitava dup e devolvia 200 (silent loss). Agora: claim primeiro, mas
//      se enqueue falhar/skipped, dedup.Delete libera o slot pra Meta
//      retentar até TTL.
//
// Trade-off: dedup ledger fica eventualmente consistente em failure paths
// (best-effort delete). Pior caso: Delete falha → Meta retry vê dup falso
// negativo até dedupTTL expirar (24h). Operadores podem flush via Redis
// quando necessário.
func (h *MetaWebhookHandler) handleMessage(
	ctx context.Context,
	value *models.MetaChangeValue,
	msg *models.MetaMessage,
) routeOutcome {
	// Anti-loop guard: dropar mensagens originadas do próprio bot. Evita
	// cascade bot→bot caso outbound vire inbound por erro de config Meta.
	if h.isFromBotItself(value, msg) {
		h.logger.WithFields(logrus.Fields{
			"event":     "meta_inbound_self_loop_drop",
			"wamid":     msg.ID,
			"from":      msg.From,
			"waba_display": value.Metadata.DisplayPhoneNumber,
		}).Warn("Meta inbound: message from own WABA number; drop to break loop")
		return routeDup // 200 OK semantics — não retentar essa
	}

	var (
		claimed     bool
		dedupKey    string
		hasDedupKey = h.dedup != nil && msg.ID != ""
	)
	if hasDedupKey {
		dedupKey = "meta:wamid:" + msg.ID
		ok, err := h.dedup.SetNX(ctx, dedupKey, dedupValueInflight, dedupTTL)
		if err != nil {
			h.logger.WithError(err).WithField("wamid", msg.ID).
				Warn("meta_inbound: SETNX failed; continuing without dedup guarantee")
		} else if !ok {
			// Discriminar entre in-flight (primeira request ainda processando)
			// e done (primeira request já completou). Se in-flight, Meta deve
			// retentar; se done, primeira request deu 200 ao Meta e dup é
			// silent skip.
			existing, getErr := h.dedup.Get(ctx, dedupKey)
			if errors.Is(getErr, services.ErrKeyNotFound) {
				// Race: SETNX falhou mas o Get retornou not-found. Significa
				// que a chave foi evicted (Redis LRU pressure) ou TTL expirou
				// entre as duas chamadas. Comportamento semanticamente correto
				// é assumir "sem claim" — retornar routeFailed faria Meta
				// retentar desnecessariamente. Mas como NÃO temos a claim
				// (SETNX deu false), seguir o fluxo de enqueue criaria
				// inconsistência. Trate como in-flight; o Meta retry resolve
				// no próximo round com SETNX limpo.
				h.logger.WithField("wamid", msg.ID).
					Warn("meta_inbound: SETNX false but Get not-found (Redis LRU evict/race); will return 503 for Meta retry")
				return routeFailed
			}
			if getErr != nil {
				h.logger.WithError(getErr).WithField("wamid", msg.ID).
					Warn("meta_inbound: dedup Get failed; treating as in-flight (will return 503)")
				return routeFailed
			}
			switch existing {
			case dedupValueDone:
				h.logger.WithFields(logrus.Fields{
					"event": "meta_inbound_dup",
					"wamid": msg.ID,
				}).Info("Meta inbound: wamid already completed, skip")
				return routeDup
			default: // inflight, ou outro valor → trate como in-flight
				h.logger.WithFields(logrus.Fields{
					"event": "meta_inbound_inflight",
					"wamid": msg.ID,
				}).Info("Meta inbound: wamid in-flight, return 503 pra Meta retentar")
				return routeFailed
			}
		} else {
			claimed = true
		}
	}

	// Sentinela: na saída, claim recebe:
	//   - routeFailed/routeSkipped → Delete (libera, Meta retry consegue replay)
	//   - routeOK → Set("done") com mesmo TTL (upgrade in-flight → done)
	//
	// Nota: NÃO usamos o pattern `success := false; defer if !success {...}` do
	// meta_dispatch.go porque aqui temos 3 ações terminais (don't-touch /
	// Delete / Set-done), não 2 (release / keep). `outcome routeOutcome` carrega
	// essa cardinalidade direta; reduzir a binário perderia clareza. Ctx
	// independente no defer pelo mesmo motivo do dispatch — cliente pode estar
	// desconectado quando o release roda.
	outcome := routeFailed
	defer func() {
		if !claimed {
			return
		}
		releaseCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		switch outcome {
		case routeFailed, routeSkipped:
			if err := h.dedup.Delete(releaseCtx, dedupKey); err != nil {
				h.logger.WithError(err).WithField("wamid", msg.ID).
					Warn("meta_inbound: dedup release failed; retries blocked até TTL")
			}
		case routeOK:
			if err := h.dedup.Set(releaseCtx, dedupKey, dedupValueDone, dedupTTL); err != nil {
				h.logger.WithError(err).WithField("wamid", msg.ID).
					Warn("meta_inbound: dedup upgrade to done failed; duplicates can still be in-flight")
			}
		}
	}()

	// Parse: extrai req do tipo apropriado.
	req, parseOutcome := h.parseMessage(value, msg)
	if parseOutcome != routeOK {
		outcome = parseOutcome
		return outcome
	}

	// Token budget gate (plano-bot-2026 Fase 0 I8) — bloqueia ANTES do
	// rate limit per-msg pra não inflacionar o per-user cap durante hit
	// de budget. Match comportamento do middleware /api/v1/message/webhook/user.
	//
	// Como o webhook Meta entrega o envelope completo (não 1-by-1), usar
	// routeOK ack pro Meta com log Warn — escalate de handoff deve vir
	// por canal alternativo (operador/SOC alertado pelo log structured).
	if h.tokenBudget != nil {
		exceeded, total, cap := h.tokenBudget.IsBudgetExceeded(ctx, req.UserNumber)
		if exceeded {
			h.logger.WithFields(logrus.Fields{
				"event":        "meta_inbound_token_budget_exceeded",
				"wamid":        msg.ID,
				"e164_prefix":  maskPhone(req.UserNumber),
				"total_tokens": total,
				"cap":          cap,
			}).Warn("Meta inbound: user hit token budget cap; dropping message (handoff route via separate channel)")
			outcome = routeOK // ack to Meta — não retentar
			return outcome
		}
	}

	// DoS rate limit per E.164 (plano-bot-2026 Fase 0 C6).
	// Aplicado APÓS parse + dedup pra que retries do Meta (mesmo wamid)
	// não consumam slot — a primeira chamada já passou pelo limiter.
	// Bloqueio: log Warn + drop msg (routeSkipped). Meta NÃO retentará
	// porque returnamos 200 ao envelope completo pelo agregador
	// (DoS é uma decisão "stop sending", não "transient failure").
	//
	// Trade-off: usar routeSkipped → response 503. Optamos por routeOK pra
	// não causar Meta retry storm em flooding legítimo (atacante quer DoS
	// e adicionar retries é counter-productive). LOG sinal pra alarmar
	// operador, mas response = 200 (msg silenciosamente dropada). Cidadão
	// continua mandando msgs até o window expirar; nenhuma é enqueued.
	if h.rateLimiter != nil {
		allowed, retryAfter, count, lerr := h.rateLimiter.AllowMessage(ctx, req.UserNumber)
		if lerr != nil {
			h.logger.WithError(lerr).WithField("wamid", msg.ID).
				Warn("meta_inbound: rate limiter error; failing open (allowing)")
		} else if !allowed {
			h.logger.WithFields(logrus.Fields{
				"event":            "meta_inbound_rate_limit_blocked",
				"wamid":            msg.ID,
				"e164_prefix":      maskPhone(req.UserNumber),
				"count":            count,
				"retry_after_secs": int(retryAfter.Seconds()),
			}).Warn("Meta inbound: per-user rate limit hit; dropping message silently")
			outcome = routeOK // ack to Meta — não retentar (DoS defense)
			return outcome
		}
	}

	// Wire callback apenas quando TODA a chain dispatch está pronta: URL +
	// secret + Meta credentials. Sem isso a mensagem entra na fila sem callback,
	// worker armazena resposta em Redis, e nunca volta pro cidadão (órfã).
	// O webhook só é registrado quando META_DIRECT_ENABLED=true → qualquer
	// chain incompleta aqui é misconfiguration; rejeita pra forçar Meta retry.
	if !h.dispatchReady() {
		h.logger.WithFields(logrus.Fields{
			"event":                 "meta_inbound_dispatch_not_ready",
			"wamid":                 msg.ID,
			"self_callback_url_set": h.cfg.SelfCallbackURL != "",
			"has_dispatch_secret":   h.cfg.DispatchSecret != "",
			"has_meta_creds":        h.cfg.SystemUserToken != "" && h.cfg.PhoneNumberID != "",
		}).Error("Meta inbound: dispatch chain incomplete; rejecting to force Meta retry")
		outcome = routeFailed
		return outcome
	}
	url := h.cfg.SelfCallbackURL
	req.CallbackURL = &url

	// Enqueue real via MessageHandler (mesmo path do /api/v1/message/webhook/user).
	if h.messageHandler == nil {
		h.logger.WithField("wamid", msg.ID).Error("meta_inbound: messageHandler nil; cannot enqueue")
		outcome = routeFailed
		return outcome
	}

	enqueueCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	if _, err := h.messageHandler.EnqueueUserMessage(enqueueCtx, req, "meta_direct:"+msg.ID, nil, EnqueueOptions{TrustedInternal: true}); err != nil {
		var inv ErrInvalidPayload
		if errors.As(err, &inv) {
			h.logger.WithFields(logrus.Fields{
				"event":  "meta_inbound_invalid_payload",
				"wamid":  msg.ID,
				"reason": inv.Reason,
			}).Warn("Meta inbound: payload inválido pós-parse; skip")
			outcome = routeSkipped
			return outcome
		}
		h.logger.WithError(err).WithField("wamid", msg.ID).Error("meta_inbound: enqueue failed")
		outcome = routeFailed
		return outcome
	}

	h.logger.WithFields(logrus.Fields{
		"event":       "meta_inbound_enqueued",
		"wamid":       msg.ID,
		"user_number": req.UserNumber,
		"type":        msg.Type,
	}).Info("Meta inbound enqueued")
	outcome = routeOK
	return outcome
}

// parseMessage converte 1 mensagem Meta → UserWebhookRequest por tipo.
//
// Tipos suportados:
//
//	text                       → Message = body
//	image / audio / video      → MessageType + Media{meta_media_id, mime, ...}
//	location                   → MessageType=location + Media{latitude, longitude, name?, address?}
//	interactive (button_reply / list_reply / nfm_reply) → downcast pra text com extra_metadata
//
// Tipos não suportados retornam (nil, routeSkipped):
//
//	contacts, sticker, document, reaction
func (h *MetaWebhookHandler) parseMessage(
	value *models.MetaChangeValue, msg *models.MetaMessage,
) (*models.UserWebhookRequest, routeOutcome) {
	baseMeta := map[string]interface{}{
		"source":          "meta_direct",
		"wamid":           msg.ID,
		"meta_timestamp":  msg.Timestamp,
		"meta_phone_id":   value.Metadata.PhoneNumberID,
		"meta_display_id": value.Metadata.DisplayPhoneNumber,
	}

	switch msg.Type {
	case "text":
		if msg.Text == nil || msg.Text.Body == "" {
			return nil, routeSkipped
		}
		return &models.UserWebhookRequest{
			UserNumber: msg.From,
			Message:    msg.Text.Body,
			Metadata:   baseMeta,
		}, routeOK

	case "image", "audio", "video":
		// Media tipos: extrai meta_media_id + mime pra Engine fazer download.
		media := mediaFromMeta(msg)
		if media == nil {
			h.logger.WithField("wamid", msg.ID).Warn("meta_inbound: media envelope missing id")
			return nil, routeSkipped
		}
		mt := msg.Type
		caption := ""
		if msg.Image != nil && msg.Image.Caption != "" {
			caption = msg.Image.Caption
		} else if msg.Video != nil && msg.Video.Caption != "" {
			caption = msg.Video.Caption
		}
		return &models.UserWebhookRequest{
			UserNumber:  msg.From,
			Message:     caption,
			MessageType: &mt,
			Media:       media,
			Metadata:    baseMeta,
		}, routeOK

	case "location":
		if msg.Location == nil {
			return nil, routeSkipped
		}
		mt := "location"
		media := map[string]interface{}{
			"latitude":  msg.Location.Latitude,
			"longitude": msg.Location.Longitude,
		}
		if msg.Location.Name != "" {
			media["name"] = msg.Location.Name
		}
		if msg.Location.Address != "" {
			media["address"] = msg.Location.Address
		}
		return &models.UserWebhookRequest{
			UserNumber:  msg.From,
			MessageType: &mt,
			Media:       media,
			Metadata:    baseMeta,
		}, routeOK

	case "interactive":
		// button_reply / list_reply / nfm_reply (WhatsApp Flow) — downcast pra
		// text preservando id em extra_metadata pra Engine roteiar pelo
		// Flow registry ou pelo botão clicado.
		body, extra := h.flattenInteractive(msg.Interactive)
		if body == "" {
			return nil, routeSkipped
		}
		for k, v := range extra {
			baseMeta[k] = v
		}
		return &models.UserWebhookRequest{
			UserNumber: msg.From,
			Message:    body,
			Metadata:   baseMeta,
		}, routeOK

	default:
		h.logger.WithFields(logrus.Fields{
			"event": "meta_inbound_unsupported_type",
			"type":  msg.Type,
			"wamid": msg.ID,
		}).Info("Meta inbound: type not yet supported")
		return nil, routeSkipped
	}
}

// mediaFromMeta extrai meta_media_id + mime_type dum MetaMessage cujo
// `Type` é "image", "audio" ou "video". Retorna nil se nenhum envelope
// tem id (payload corrompido).
func mediaFromMeta(msg *models.MetaMessage) map[string]interface{} {
	switch msg.Type {
	case "image":
		if msg.Image == nil || msg.Image.ID == "" {
			return nil
		}
		return map[string]interface{}{
			"meta_media_id": msg.Image.ID,
			"mime_type":     msg.Image.MimeType,
			"sha256":        msg.Image.SHA256,
		}
	case "audio":
		if msg.Audio == nil || msg.Audio.ID == "" {
			return nil
		}
		return map[string]interface{}{
			"meta_media_id": msg.Audio.ID,
			"mime_type":     msg.Audio.MimeType,
			"sha256":        msg.Audio.SHA256,
			"voice":         msg.Audio.Voice,
		}
	case "video":
		if msg.Video == nil || msg.Video.ID == "" {
			return nil
		}
		return map[string]interface{}{
			"meta_media_id": msg.Video.ID,
			"mime_type":     msg.Video.MimeType,
			"sha256":        msg.Video.SHA256,
		}
	}
	return nil
}

// flattenInteractive converte interactive payload (button_reply / list_reply
// / nfm_reply) numa string + metadata extra. Retorna ("", nil) se shape
// desconhecido. Pra nfm_reply, usa flowRegistry pra resolver service_name
// e parsear response_json em metadata.form_submission estruturado (ADR-024).
func (h *MetaWebhookHandler) flattenInteractive(intr *models.MetaInteractive) (string, map[string]interface{}) {
	if intr == nil {
		return "", nil
	}
	switch intr.Type {
	case "button_reply":
		if intr.ButtonReply == nil {
			return "", nil
		}
		return intr.ButtonReply.Title, map[string]interface{}{
			"interactive_kind": "button_reply",
			"interactive_id":   intr.ButtonReply.ID,
		}
	case "list_reply":
		if intr.ListReply == nil {
			return "", nil
		}
		return intr.ListReply.Title, map[string]interface{}{
			"interactive_kind":        "list_reply",
			"interactive_id":          intr.ListReply.ID,
			"interactive_description": intr.ListReply.Description,
		}
	case "nfm_reply":
		// WhatsApp Flow submission. Resolve service_name via registry e
		// inclui form_submission estruturado (parse response_json). Engine
		// dispatch pra MCP correto sem precisar inspecionar raw JSON.
		if intr.NFMReply == nil {
			return "", nil
		}
		body := "[whatsapp_flow_submission]"
		if intr.NFMReply.Body != "" {
			body = intr.NFMReply.Body
		}
		extra := map[string]interface{}{
			"interactive_kind":  "nfm_reply",
			"flow_name":         intr.NFMReply.Name,
			"flow_response_raw": intr.NFMReply.ResponseJSON,
		}
		if intr.NFMReply.FlowToken != "" {
			extra["flow_token"] = intr.NFMReply.FlowToken
		}
		if service := h.flowRegistry.Resolve(intr.NFMReply.Name); service != "" {
			extra["service_name"] = service
		}
		if parsed := ParseFlowResponse(intr.NFMReply.ResponseJSON); parsed != nil {
			extra["form_submission"] = parsed
		}
		return body, extra
	}
	return "", nil
}
