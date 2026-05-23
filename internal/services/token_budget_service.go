// Package services — Token budget per-E.164 daily counter (plano-bot-2026 Fase 0 I8).
//
// Cap defensivo contra prompt-injection que faz LLM gerar respostas gigantes
// consumindo tokens (DoS variant). Counter Redis daily-resetting:
//
//	Key: "token_budget:<e164_normalized>:<yyyy-mm-dd>"
//	Value: int (acumulado total_tokens daquele dia)
//	TTL: ~26h (cobre carry-over UTC + processing window)
//
// Plumbing:
//
//	worker.callback /meta/dispatch → extrai usage_statistics →
//	BudgetService.AddTokensSpent(e164, total_tokens) →
//	emite WARN log em ≥70% threshold; sem block aqui.
//
//	pre-request middleware no /meta/webhook + /api/v1/message/webhook/user
//	verifica BudgetService.IsBudgetExceeded(e164) — hit 100% → 429 com
//	header X-Budget-Exceeded:true pra signal handoff route.
//
// Source of truth de tokens: Worker callback payload contém um item
// `usage_statistics` no final do array `data.messages` com fields
// `prompt_tokens`, `completion_tokens`, `total_tokens`. Worker
// `calculateUsageStatistics` (workers/message_handlers.go) computa a partir
// de `response_metadata.usage_metadata` do Engine.
package services

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/sirupsen/logrus"
)

// TokenBudgetConfig — runtime params. Vem do config.Config.
type TokenBudgetConfig struct {
	Enabled            bool
	PerUserDaily       int // cap total_tokens/dia/E.164
	WarnThresholdPct   int // ≥X% → log WARN (default 70)
}

// TokenBudgetService — counter Redis per-E.164 com daily reset (TTL 26h).
type TokenBudgetService struct {
	cfg    TokenBudgetConfig
	logger *logrus.Logger
	redis  TokenBudgetRedisOps
}

// TokenBudgetRedisOps — subset Redis. Definido localmente pra evitar
// coupling (mesmo padrão do DoSRateLimiter).
//
// IncrementBy faz INCRBY atomicamente (Redis garante atomicidade mesmo
// sob concorrência multi-pod). Sem isso, read-modify-write (Get + Set)
// permitia perda de increments quando dois callbacks do mesmo E.164
// rodavam concorrentes.
type TokenBudgetRedisOps interface {
	Get(ctx context.Context, key string) (string, error)
	SetValue(ctx context.Context, key string, value interface{}, ttl time.Duration) error
	Expire(ctx context.Context, key string, ttl time.Duration) error
	IncrementBy(ctx context.Context, key string, delta int64) (int64, error)
}

// NewTokenBudgetService constrói o service. Aceita nil redis (modo degraded
// — todas operações no-op).
func NewTokenBudgetService(cfg TokenBudgetConfig, logger *logrus.Logger, redis TokenBudgetRedisOps) *TokenBudgetService {
	return &TokenBudgetService{cfg: cfg, logger: logger, redis: redis}
}

// AddTokensSpent incrementa o counter daily de tokens consumidos por `e164`.
// Chamado pelo dispatch path quando a usage_statistics estiver disponível.
//
// Usa INCRBY atômico (Redis single-cmd) pra não perder increments quando
// callbacks concorrentes do mesmo E.164 rodam paralelos — fix codex P2
// 2026-05-23.
//
// Não-bloqueante: redis err loga e segue. Caller não deve falhar a request
// por isso — esse counter é observability + gate enforcement, não path
// crítico de delivery.
//
// Emite WARN structured quando consumo atinge ≥ WarnThresholdPct cap. Como
// IncrementBy retorna o novo total atomicamente, podemos calcular se
// "passamos" o threshold comparando (newTotal - delta) < threshold <= newTotal.
// Sem race entre check do warn e increment — newTotal é truth source.
func (s *TokenBudgetService) AddTokensSpent(ctx context.Context, e164 string, tokens int) {
	if !s.cfg.Enabled || s.redis == nil || tokens <= 0 || e164 == "" {
		return
	}
	normalized := normalizeBudgetE164(e164)
	if normalized == "" {
		return
	}

	day := time.Now().UTC().Format("2006-01-02")
	key := fmt.Sprintf("token_budget:%s:%s", normalized, day)

	// INCRBY atomicamente. Concurrent callbacks do mesmo E.164 incrementam
	// serializadamente — sem race window.
	newTotal, err := s.redis.IncrementBy(ctx, key, int64(tokens))
	if err != nil {
		s.logger.WithError(err).WithField("event", "token_budget_incr_err").
			Warn("token_budget: redis IncrementBy failed; counter may drift")
		return
	}
	previousTotal := newTotal - int64(tokens) // valor antes do nosso INCR

	// TTL 26h cobre UTC carry-over + processing window. Counter zera
	// naturalmente quando day muda (chave diferente). Set após cada incr
	// é idempotente (Redis EXPIRE refresha o TTL sem warning); operação
	// barata. Evita verificar "key new vs existing" (que abriria race
	// análoga ao read-modify-write antigo).
	if err := s.redis.Expire(ctx, key, 26*time.Hour); err != nil {
		s.logger.WithError(err).WithField("event", "token_budget_expire_err").
			Warn("token_budget: redis Expire failed; counter may not auto-cleanup")
		// Não return: counter incrementou OK; sem TTL apenas vaza chaves
		// até alguém limpar manualmente. Não-fatal pro contrato do service.
	}

	// Warn threshold check. Compute em integer-safe (não float pra evitar
	// drift em borderline). Como INCRBY é atômico, "cruzar" o threshold
	// nesta operação é detectável: previousTotal < threshold <= newTotal.
	if s.cfg.PerUserDaily > 0 && s.cfg.WarnThresholdPct > 0 {
		warnAt := int64(s.cfg.PerUserDaily) * int64(s.cfg.WarnThresholdPct) / 100
		if previousTotal < warnAt && newTotal >= warnAt {
			s.logger.WithFields(logrus.Fields{
				"event":            "token_budget_threshold_warn",
				"e164_prefix":      maskBudgetE164(normalized),
				"total_tokens":     newTotal,
				"threshold":        warnAt,
				"cap":              s.cfg.PerUserDaily,
				"warn_pct":         s.cfg.WarnThresholdPct,
			}).Warn("Token budget: user crossed warn threshold")
		}
		// Hit 100% — log loud (operadores podem alarmar).
		cap := int64(s.cfg.PerUserDaily)
		if previousTotal < cap && newTotal >= cap {
			s.logger.WithFields(logrus.Fields{
				"event":            "token_budget_exceeded",
				"e164_prefix":      maskBudgetE164(normalized),
				"total_tokens":     newTotal,
				"cap":              s.cfg.PerUserDaily,
			}).Error("Token budget: user hit daily cap; next requests will be blocked")
		}
	}
}

// IsBudgetExceeded retorna true se o user já atingiu/passou o cap daily.
// Não-bloqueante a Redis err — fail-open (return false).
func (s *TokenBudgetService) IsBudgetExceeded(ctx context.Context, e164 string) (exceeded bool, total int, cap int) {
	if !s.cfg.Enabled || s.redis == nil || e164 == "" {
		return false, 0, s.cfg.PerUserDaily
	}
	normalized := normalizeBudgetE164(e164)
	if normalized == "" {
		return false, 0, s.cfg.PerUserDaily
	}
	day := time.Now().UTC().Format("2006-01-02")
	key := fmt.Sprintf("token_budget:%s:%s", normalized, day)

	v, err := s.redis.Get(ctx, key)
	if err != nil {
		if !errors.Is(err, ErrKeyNotFound) && err.Error() != "key not found" {
			s.logger.WithError(err).WithField("event", "token_budget_check_err").
				Warn("token_budget: redis Get failed in check; failing open")
		}
		return false, 0, s.cfg.PerUserDaily
	}
	count, _ := strconv.Atoi(v)
	return count >= s.cfg.PerUserDaily, count, s.cfg.PerUserDaily
}

// ExtractTokenUsage extrai o total_tokens de um worker callback payload.
// Retorna 0 quando não encontra (callback sem usage_statistics, shape
// diferente). Caller deve tratar 0 como "skip budget update" — não vale
// emitir warn pra ausência.
//
// Shape esperado (do worker, ver internal/handlers/workers/message_handlers.go
// `calculateUsageStatistics`):
//
//	data.messages[-1] = {
//	  message_type: "usage_statistics",
//	  prompt_tokens: int,
//	  completion_tokens: int,
//	  thoughts_tokens: int,
//	  total_tokens: int,
//	  ...
//	}
//
// Preferimos `total_tokens` direto; quando ausente caímos pra soma de
// prompt + completion + thoughts (espelhando o agregador downstream).
func ExtractTokenUsage(data map[string]interface{}) int {
	if data == nil {
		return 0
	}
	rawList, ok := data["messages"]
	if !ok {
		return 0
	}
	list, ok := rawList.([]interface{})
	if !ok {
		return 0
	}
	for i := len(list) - 1; i >= 0; i-- {
		m, ok := list[i].(map[string]interface{})
		if !ok {
			continue
		}
		// Identificar pelo discriminador `message_type` ou `type`.
		mt, _ := m["message_type"].(string)
		t, _ := m["type"].(string)
		_, isStats := m["usage_statistics"]
		if mt != "usage_statistics" && t != "usage_statistics" && !isStats {
			continue
		}
		// Caso 1: campo aninhado `usage_statistics` (shape alternativo)
		if isStats {
			if inner, ok := m["usage_statistics"].(map[string]interface{}); ok {
				if total := toInt(inner["total_tokens"]); total > 0 {
					return total
				}
				return toInt(inner["prompt_tokens"]) + toInt(inner["completion_tokens"]) + toInt(inner["thoughts_tokens"])
			}
		}
		// Caso 2: campos top-level (shape canônico do worker)
		if total := toInt(m["total_tokens"]); total > 0 {
			return total
		}
		return toInt(m["prompt_tokens"]) + toInt(m["completion_tokens"]) + toInt(m["thoughts_tokens"])
	}
	return 0
}

// toInt — coerção numerica defensiva (JSON unmarshal produz float64).
func toInt(v interface{}) int {
	switch n := v.(type) {
	case int:
		return n
	case int32:
		return int(n)
	case int64:
		return int(n)
	case float64:
		return int(n)
	case float32:
		return int(n)
	default:
		return 0
	}
}

// normalizeBudgetE164 / maskBudgetE164 — locais pra evitar import cycle do
// dos_rate_limiter (mesmo lógica). Distintos por nome pra explicit scope.
func normalizeBudgetE164(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		if s[i] >= '0' && s[i] <= '9' {
			out = append(out, s[i])
		}
	}
	return string(out)
}

func maskBudgetE164(phone string) string {
	digits := normalizeBudgetE164(phone)
	if len(digits) <= 8 {
		return digits
	}
	return digits[:8] + "…"
}
