// Package services — Per-E.164 DoS rate limiter (plano-bot-2026 Fase 0 C6).
//
// Defende o bot contra dois vetores:
//
//  1. **Flooding por cidadão**: um único E.164 disparando centenas de
//     mensagens em segundos esgota worker pool + LLM budget + custo Gemini.
//     Cap: 30 msgs / 15min / E.164 (hard reject 429).
//
//  2. **Auth fail brute-force**: tentativas repetidas de descobrir
//     ADMIN_API_TOKEN ou META_DISPATCH_SECRET. Cap: 5 fails / 15min / IP →
//     bloqueio temporário 15min.
//
// Backing store: Redis (sliding window discreto por bucket de 15min). Não
// usa Lua nem Redlock porque cap é por-bucket — granularidade boa o
// suficiente pra DoS defense; absoluta precisão de "exact-N por janela
// rolante contínua" não justifica o custo. Wraparound entre buckets significa
// que worst-case o atacante pode disparar 2× cap (final do bucket N + começo
// do N+1) — ainda 60 msgs/30min/E.164, longe do que precisa pra DoS efetivo.
//
// Fail-open quando Redis indisponível: registra Warn structured e libera a
// chamada. Trade-off explícito — rate limit não deve ser SPOF que derruba o
// gateway inteiro. Operador monitora `dos_rate_limiter_fail_open` count pra
// detectar Redis flapping.
package services

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/sirupsen/logrus"
)

// DoSRateLimiterConfig — parâmetros runtime do limiter. Vem do config.Config
// (env vars com defaults). Mantido aqui pra desacoplar config global.
type DoSRateLimiterConfig struct {
	Enabled bool

	// Per-user message rate limit (DoS defense).
	MsgPerUserPerWindow int
	WindowDuration      time.Duration

	// Per-IP auth-failure rate limit (brute-force defense).
	AuthFailPerIPPerWindow int
	AuthFailBlockDuration  time.Duration
}

// DoSRedisOps — subset mínimo de operações Redis que o limiter precisa.
// Definida localmente (interface name distinto pra não colidir com outras
// `RedisServiceInterface` espalhadas pelo módulo). RedisService satisfaz
// nativamente (Get/SetValue/Increment/Expire/Delete).
type DoSRedisOps interface {
	Get(ctx context.Context, key string) (string, error)
	SetValue(ctx context.Context, key string, value interface{}, ttl time.Duration) error
	Increment(ctx context.Context, key string) (int64, error)
	Expire(ctx context.Context, key string, ttl time.Duration) error
	Delete(ctx context.Context, key string) error
}

// DoSRateLimiterService — sliding-window counter com Redis backing.
// Stateless além do Redis: instâncias múltiplas do gateway compartilham
// o mesmo counter (chave global por E.164/IP + bucket timestamp).
type DoSRateLimiterService struct {
	cfg    DoSRateLimiterConfig
	logger *logrus.Logger
	redis  DoSRedisOps
}

// NewDoSRateLimiterService constrói o limiter. Aceita `nil` redis (modo
// degraded; AllowMessage sempre retorna true + log Warn).
func NewDoSRateLimiterService(cfg DoSRateLimiterConfig, logger *logrus.Logger, redis DoSRedisOps) *DoSRateLimiterService {
	return &DoSRateLimiterService{cfg: cfg, logger: logger, redis: redis}
}

// AllowMessage verifica se uma mensagem inbound de `e164` está dentro do
// rate limit. Retorna:
//
//   - allowed (bool): true = chamada pode prosseguir
//   - retryAfter (time.Duration): quanto esperar até o próximo window quando bloqueado
//   - currentCount (int): contagem atual no bucket (pra observabilidade)
//   - err (error): erro Redis (caller decide se bloqueia ou fail-open; tipicamente fail-open)
//
// Window é discreto: bucket = floor(now/window). Cada chamada incrementa
// um counter Redis com TTL = 2×window (cobre overlap entre buckets pra
// concurrent calls em torno do roll-over).
func (l *DoSRateLimiterService) AllowMessage(ctx context.Context, e164 string) (bool, time.Duration, int, error) {
	if !l.cfg.Enabled {
		return true, 0, 0, nil
	}
	if e164 == "" {
		// Sem identificador, não dá pra rate-limit. Allow (caller já validou
		// upstream que message tem from); log Warn pra detectar bypass.
		l.logger.Warn("dos_rate_limiter: AllowMessage called with empty e164; allowing")
		return true, 0, 0, nil
	}
	if l.redis == nil {
		l.logger.WithFields(logrus.Fields{
			"event": "dos_rate_limiter_fail_open",
			"e164":  maskDoSE164(e164),
		}).Warn("DoS rate limiter: redis unavailable; failing open")
		return true, 0, 0, nil
	}

	normalized := normalizeDoSE164(e164)
	if normalized == "" {
		l.logger.WithField("raw", e164).Warn("dos_rate_limiter: empty after normalization; allowing")
		return true, 0, 0, nil
	}

	windowDuration := l.cfg.WindowDuration
	if windowDuration <= 0 {
		windowDuration = 15 * time.Minute
	}
	windowSeconds := int64(windowDuration.Seconds())
	now := time.Now().Unix()
	bucket := now / windowSeconds
	key := fmt.Sprintf("ratelimit:msg:%s:%d", normalized, bucket)

	count, err := l.incrementWithTTL(ctx, key, 2*windowDuration)
	if err != nil {
		l.logger.WithError(err).WithFields(logrus.Fields{
			"event": "dos_rate_limiter_redis_err",
			"e164":  maskDoSE164(normalized),
		}).Error("DoS rate limiter: redis op failed; failing open")
		return true, 0, 0, err
	}

	if int(count) > l.cfg.MsgPerUserPerWindow {
		// retryAfter = quanto falta pro próximo bucket começar.
		bucketEnd := (bucket + 1) * windowSeconds
		retryAfter := time.Duration(bucketEnd-now) * time.Second
		if retryAfter < time.Second {
			retryAfter = time.Second
		}
		return false, retryAfter, int(count), nil
	}
	return true, 0, int(count), nil
}

// RecordAuthFailure incrementa o counter de auth-fail pro IP e retorna true
// se o IP atingiu o cap (caller deve bloquear). Idempotente — chamar
// múltiplas vezes na mesma request é overcount benigno (escolhemos
// preferir over-bloqueio a under-bloqueio em paths sensíveis).
func (l *DoSRateLimiterService) RecordAuthFailure(ctx context.Context, ip string) (blocked bool, retryAfter time.Duration, err error) {
	if !l.cfg.Enabled || ip == "" || l.redis == nil {
		return false, 0, nil
	}
	blockDuration := l.cfg.AuthFailBlockDuration
	if blockDuration <= 0 {
		blockDuration = 15 * time.Minute
	}
	windowSeconds := int64(blockDuration.Seconds())
	now := time.Now().Unix()
	bucket := now / windowSeconds
	key := fmt.Sprintf("ratelimit:authfail:%s:%d", ip, bucket)

	count, err := l.incrementWithTTL(ctx, key, 2*blockDuration)
	if err != nil {
		l.logger.WithError(err).WithField("event", "dos_rate_limiter_authfail_redis_err").
			Error("DoS rate limiter (auth fail): redis op failed")
		return false, 0, err
	}
	if int(count) >= l.cfg.AuthFailPerIPPerWindow {
		bucketEnd := (bucket + 1) * windowSeconds
		retryAfter = time.Duration(bucketEnd-now) * time.Second
		if retryAfter < time.Second {
			retryAfter = time.Second
		}
		return true, retryAfter, nil
	}
	return false, 0, nil
}

// IsIPBlocked confere se um IP está bloqueado por exceder o auth-fail cap
// (sem incrementar). Usado pre-validação em endpoints sensíveis.
func (l *DoSRateLimiterService) IsIPBlocked(ctx context.Context, ip string) (blocked bool, retryAfter time.Duration, err error) {
	if !l.cfg.Enabled || ip == "" || l.redis == nil {
		return false, 0, nil
	}
	blockDuration := l.cfg.AuthFailBlockDuration
	if blockDuration <= 0 {
		blockDuration = 15 * time.Minute
	}
	windowSeconds := int64(blockDuration.Seconds())
	now := time.Now().Unix()
	bucket := now / windowSeconds
	key := fmt.Sprintf("ratelimit:authfail:%s:%d", ip, bucket)

	countStr, err := l.redis.Get(ctx, key)
	if err != nil {
		if errors.Is(err, ErrKeyNotFound) || err.Error() == "key not found" {
			return false, 0, nil
		}
		return false, 0, err
	}
	count, _ := strconv.Atoi(countStr)
	if count >= l.cfg.AuthFailPerIPPerWindow {
		bucketEnd := (bucket + 1) * windowSeconds
		retryAfter = time.Duration(bucketEnd-now) * time.Second
		if retryAfter < time.Second {
			retryAfter = time.Second
		}
		return true, retryAfter, nil
	}
	return false, 0, nil
}

// incrementWithTTL faz INCR + EXPIRE atomicamente best-effort.
// Redis go-redis client expõe Incr+Expire separados (não há atomic
// MULTI/EXEC aqui), mas o caso degenerado é: INCR ok → Expire falha →
// key vira eterna. Mitigamos com Expire IGNORE-error tolerant + log Warn;
// pior caso a key acumula mas é por-bucket então TTL natural seria
// reciclado no próximo bucket de qualquer forma. Sem Lua pra simplicidade.
func (l *DoSRateLimiterService) incrementWithTTL(ctx context.Context, key string, ttl time.Duration) (int64, error) {
	count, err := l.redis.Increment(ctx, key)
	if err != nil {
		return 0, fmt.Errorf("increment %s: %w", key, err)
	}
	if count == 1 {
		// Primeira vez na key — setar TTL. Em chamadas subsequentes Redis
		// preserva o TTL existente.
		if expErr := l.redis.Expire(ctx, key, ttl); expErr != nil {
			// Não-fatal — key sem TTL ainda funciona pra esse bucket; logamos
			// e seguimos. Próximo bucket reseta naturalmente quando o
			// counter é zerado lá.
			l.logger.WithError(expErr).WithField("key", key).
				Warn("dos_rate_limiter: failed to set TTL on counter (non-fatal)")
		}
	}
	return count, nil
}

// normalizeDoSE164 retorna apenas dígitos (E.164 canônico sem '+').
// "+55 21 9 8909-1014" → "5521989091014".
// String vazia ou tudo-não-dígito → "".
//
// Nome distinto de `normalizeDigits` (meta_webhook.go) e da função interna
// do package handlers — evita coupling cross-package.
func normalizeDoSE164(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		if s[i] >= '0' && s[i] <= '9' {
			out = append(out, s[i])
		}
	}
	return string(out)
}

// maskDoSE164 mascara LGPD pra log: "5521989091014" → "55219890…".
// Espelha `maskPhone` em meta_webhook.go.
func maskDoSE164(phone string) string {
	digits := normalizeDoSE164(phone)
	if len(digits) <= 8 {
		return digits
	}
	return digits[:8] + "…"
}
