// Package middleware — DoS rate-limit middlewares (plano-bot-2026 Fase 0 C6).
//
// Aplica:
//   - DoSMessageRateLimit: limita inbound msgs per E.164 normalizado em
//     /api/v1/message/webhook/user (POST). Para /meta/webhook a extração
//     do E.164 é diferente (vem do body Meta envelope) — usar AllowMessage
//     no handler diretamente.
//   - DoSAuthFailureGate: gate pre-validation que bloqueia IP quando excedeu
//     o cap de auth fails (5/15min). Aplicado em /meta/dispatch e
//     /admin/broker-mode.
//
// Resposta padrão 429 (Too Many Requests) + header `Retry-After` no formato
// Meta-friendly (segundos integer). Body JSON com `error` + `retry_after_seconds`
// pra clients programáticos.
package middleware

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"

	"github.com/prefeitura-rio/app-eai-agent-gateway/internal/services"
)

// DoSRateLimiter — contrato mínimo que o middleware precisa. Implementado
// por services.DoSRateLimiterService.
type DoSRateLimiter interface {
	AllowMessage(ctx context.Context, e164 string) (bool, time.Duration, int, error)
	IsIPBlocked(ctx context.Context, ip string) (bool, time.Duration, error)
}

// TokenBudgetChecker — contrato pra verificar se um E.164 atingiu o cap
// diário (plano-bot-2026 Fase 0 I8). Implementado por
// services.TokenBudgetService.
type TokenBudgetChecker interface {
	IsBudgetExceeded(ctx context.Context, e164 string) (exceeded bool, total int, cap int)
}

// userMsgBodyShape — shape laxo pra extrair user_number do body POST
// /api/v1/message/webhook/user sem consumir o reader (re-buffered).
type userMsgBodyShape struct {
	UserNumber string `json:"user_number"`
}

// DoSMessageRateLimitMiddleware retorna middleware Gin que bloqueia
// requests do tipo "inbound user message" quando o E.164 normalizado
// excede o cap configurado OU quando o budget de tokens daily foi
// atingido. Aplica em /api/v1/message/webhook/user.
//
// Como o body precisa ser lido pra extrair user_number antes do handler,
// a middleware re-buffer o body via io.NopCloser pra que o handler real
// consiga deserializar de novo. Limite de tamanho cobrir pelo
// RequestSizeLimit upstream.
//
// Quando o budget de tokens foi excedido, retorna 429 + header
// X-Budget-Exceeded:true que serve de signal pra escalate route (handoff
// humano). Caller (reverse proxy / Meta / orchestrator) detecta o header
// e roteia pra fila de atendimento.
//
// Fail-open em ambos os limites: erro Redis ou body ilegível → allow
// (handler decide se rejeita por parsing); log Warn. Não bloqueamos
// requests legítimas por blip do limiter — DoS attack ainda esbarra em
// outras camadas (Meta-side rate limit + reverse proxy).
//
// `budget` pode ser nil — então gate de tokens é skipped (compatibilidade
// quando feature flag OFF).
func DoSMessageRateLimitMiddleware(limiter DoSRateLimiter, budget TokenBudgetChecker, logger *logrus.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		// Só intercepta POST. Outras rotas caem fora (sem overhead Redis).
		if c.Request.Method != http.MethodPost {
			c.Next()
			return
		}

		// Re-buffer body — handler downstream também precisa ler.
		bodyBytes, err := io.ReadAll(c.Request.Body)
		if err != nil {
			logger.WithError(err).Warn("dos_rate_limit_msg: body read failed; allowing")
			c.Next()
			return
		}
		c.Request.Body = io.NopCloser(bytes.NewReader(bodyBytes))

		var shape userMsgBodyShape
		if jerr := json.Unmarshal(bodyBytes, &shape); jerr != nil || shape.UserNumber == "" {
			// Body malformado: handler retornará 400 sozinho. Sem identificador
			// pra rate-limit; allow.
			c.Next()
			return
		}

		// Token budget gate (plano-bot-2026 Fase 0 I8) — pré-message
		// rate limit. Excedido = bloqueia ANTES de incrementar o counter
		// per-msg pra não inflacionar o per-user cap durante budget hit.
		if budget != nil {
			exceeded, total, cap := budget.IsBudgetExceeded(c.Request.Context(), shape.UserNumber)
			if exceeded {
				logger.WithFields(logrus.Fields{
					"event":        "token_budget_request_blocked",
					"e164_prefix":  maskE164Prefix(shape.UserNumber),
					"total_tokens": total,
					"cap":          cap,
				}).Warn("Token budget: user request blocked (daily cap exceeded)")
				c.Header("X-Budget-Exceeded", "true")
				c.Header("Retry-After", "86400") // 1 dia até reset UTC
				c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{
					"error":                "token budget exceeded",
					"reason":               "daily token budget reached; escalate to human handoff",
					"total_tokens_spent":   total,
					"daily_cap":            cap,
					"escalate_to_handoff":  true,
				})
				return
			}
		}

		allowed, retryAfter, count, lerr := limiter.AllowMessage(c.Request.Context(), shape.UserNumber)
		if lerr != nil {
			// Limiter failed-open dentro do AllowMessage; já logou erro.
			c.Next()
			return
		}
		if !allowed {
			logger.WithFields(logrus.Fields{
				"event":             "dos_rate_limit_msg_blocked",
				"e164_prefix":       maskE164Prefix(shape.UserNumber),
				"count":             count,
				"retry_after_secs":  int(retryAfter.Seconds()),
			}).Warn("DoS rate limit: message rejected (per-user cap)")

			retryAfterSecs := int(retryAfter.Seconds())
			if retryAfterSecs < 1 {
				retryAfterSecs = 1
			}
			c.Header("Retry-After", strconv.Itoa(retryAfterSecs))
			c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{
				"error":               "rate limit exceeded",
				"reason":              "too many messages per user; backoff and retry",
				"retry_after_seconds": retryAfterSecs,
			})
			return
		}
		c.Next()
	}
}

// DoSAuthFailureGateMiddleware bloqueia requests de IPs que excederam o cap
// de auth-failures. Pre-check (não incrementa) — handlers continuam a
// chamar RecordAuthFailure quando rejeitam por auth invalida.
func DoSAuthFailureGateMiddleware(limiter DoSRateLimiter, logger *logrus.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		ip := c.ClientIP()
		if ip == "" {
			c.Next()
			return
		}
		blocked, retryAfter, err := limiter.IsIPBlocked(c.Request.Context(), ip)
		if err != nil {
			if !errors.Is(err, services.ErrKeyNotFound) {
				logger.WithError(err).WithField("event", "dos_authfail_gate_err").
					Warn("DoS auth-fail gate: redis check failed; allowing")
			}
			c.Next()
			return
		}
		if !blocked {
			c.Next()
			return
		}
		logger.WithFields(logrus.Fields{
			"event":            "dos_rate_limit_ip_blocked",
			"ip":               ip,
			"retry_after_secs": int(retryAfter.Seconds()),
			"path":             c.Request.URL.Path,
		}).Warn("DoS rate limit: IP blocked (auth-fail cap exceeded)")
		retryAfterSecs := int(retryAfter.Seconds())
		if retryAfterSecs < 1 {
			retryAfterSecs = 1
		}
		c.Header("Retry-After", strconv.Itoa(retryAfterSecs))
		c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{
			"error":               "ip temporarily blocked",
			"reason":              "too many authentication failures from this ip; backoff and retry",
			"retry_after_seconds": retryAfterSecs,
		})
	}
}

// maskE164Prefix mascara LGPD pra log estruturado: "5521989091014" → "55219890…".
func maskE164Prefix(phone string) string {
	digits := make([]byte, 0, len(phone))
	for i := 0; i < len(phone); i++ {
		if phone[i] >= '0' && phone[i] <= '9' {
			digits = append(digits, phone[i])
		}
	}
	if len(digits) <= 8 {
		return string(digits)
	}
	return string(digits[:8]) + "…"
}
