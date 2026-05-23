// Package services — Audit logger pra ações sensíveis (plano-bot-2026 Fase 0 C4).
//
// Toda chamada bem-sucedida de outbound Meta direto (text, media, location,
// interactive, template, reaction) emite um audit log JSON estruturado
// distinto do logger principal. Sink default: stdout (segregado por marker
// `event=audit` pra collector Loki/CloudWatch agarrar com filtro próprio).
//
// Por que estrutura separada (e não só Info no logger normal):
//
//  1. **Retention obrigatória ≥5 anos** (CC 2002 prescrição civil + LGPD
//     Art.15). Logs operacionais em alguns ambientes giram em 30-90 dias;
//     audit log precisa ir pra cold storage segregado.
//  2. **PII redaction enforced**: recipient sempre hash SHA256 dos últimos
//     8 dígitos (não E.164 cru). Tooling de redação evita acidente.
//  3. **Filtro estrutural**: queries como "todas as actions deste
//     trace_id" são one-line via filtro `event=audit`.
//
// Compliance check: este logger NÃO armazena conteúdo da mensagem (body
// text, media URLs, captions). Apenas metadados — quem, o quê (tipo),
// quando, onde (wamid), com qual correlation. Conteúdo pode ser
// reconstruído via trace_id correlation com OTel se necessário, sob
// processo administrativo.
package services

import (
	"context"
	crand "crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"time"

	"github.com/sirupsen/logrus"
)

// cryptoRandRead — indirection pra crypto/rand.Reader. Variable pra que
// testes possam injetar um source determinístico se precisar.
var cryptoRandRead = func(b []byte) (int, error) {
	return io.ReadFull(crand.Reader, b)
}

// AuditEntry — shape estruturado pra eventos sensíveis. Campos vão direto
// pro JSON output do logrus.
type AuditEntry struct {
	// SnowflakeID — ID time-ordered único pra cada audit row. Gerado no
	// momento da emissão. Usado pra correlation cross-storage (Loki ↔ BQ).
	SnowflakeID string `json:"snowflake_id"`
	// TimestampUTC — RFC3339 strict, sempre UTC pra evitar TZ drift.
	TimestampUTC string `json:"timestamp_utc"`
	// ActionType — discriminador. Exemplos: "meta_send_text",
	// "meta_send_media", "meta_send_location", "meta_send_interactive",
	// "meta_send_template", "meta_send_reaction", "admin_broker_mode_read",
	// "broker_mode_change".
	ActionType string `json:"action_type"`
	// RecipientHash — SHA256(últimos 8 dígitos do E.164). 64 hex chars.
	// NUNCA armazenar o número cru. Permite "este número?" lookup via
	// hash compare, mas não permite re-identificação reversa.
	RecipientHash string `json:"recipient_hash,omitempty"`
	// WAMID — Meta WhatsApp Message ID retornado pela Graph API. Vazio se
	// a ação não gerou (ex: broker_mode_read).
	WAMID string `json:"wamid,omitempty"`
	// MessageID — UUID interno gerado pelo Gateway pra correlation.
	MessageID string `json:"message_id,omitempty"`
	// TraceID — OTel trace ID hexadecimal pra debug profundo.
	TraceID string `json:"trace_id,omitempty"`
	// Success — true = ação completou (Meta aceitou); false = erro
	// downstream. Recorded mesmo em failure pra auditar tentativas.
	Success bool `json:"success"`
	// ErrorClass — quando success=false, tipo de erro (ex: "meta_4xx",
	// "redis_down", "timeout"). Não inclui mensagem cru (PII risk).
	ErrorClass string `json:"error_class,omitempty"`
}

// AuditLogger — sink genérico pra audit entries. Implementação default:
// LogrusAuditLogger (stdout JSON). Mock implementations em testes.
type AuditLogger interface {
	Log(ctx context.Context, entry AuditEntry)
}

// LogrusAuditLogger — implementação que emite logs estruturados via logrus
// com nível Info e marker `event=audit`. Logger interno separado pra que
// configuração futura (formatter custom, sink dedicado, level fixo) não
// afete logger geral.
type LogrusAuditLogger struct {
	logger *logrus.Logger
}

// NewLogrusAuditLogger constrói o sink. Logger compartilhado deve estar
// configurado em JSON formatter pra que campos JSON sejam preservados.
// Configuração via config.Observability.LogFormat="json" (default).
func NewLogrusAuditLogger(logger *logrus.Logger) *LogrusAuditLogger {
	return &LogrusAuditLogger{logger: logger}
}

// Log emite a entrada como evento estruturado. Campos vão pra `data` do
// JSON. Marker `event=audit` permite filtros downstream:
//
//	loki> {app="gateway"} | json | event="audit"
//	bq:  WHERE jsonPayload.event = 'audit'
func (l *LogrusAuditLogger) Log(_ context.Context, entry AuditEntry) {
	l.logger.WithFields(logrus.Fields{
		"event":          "audit",
		"snowflake_id":   entry.SnowflakeID,
		"timestamp_utc":  entry.TimestampUTC,
		"action_type":    entry.ActionType,
		"recipient_hash": entry.RecipientHash,
		"wamid":          entry.WAMID,
		"message_id":     entry.MessageID,
		"trace_id":       entry.TraceID,
		"success":        entry.Success,
		"error_class":    entry.ErrorClass,
	}).Info("audit log entry")
}

// HashRecipientE164 retorna SHA256 hex dos últimos 8 dígitos do E.164.
// Vazio se input não tem 8+ dígitos.
//
// Por que truncar antes do hash:
//   - SHA256 colide em ~2^128 trials; pra E.164 com domínio finito (~10^11
//     números brasileiros), full-number hash permite rainbow table lookup
//     barato. Truncar pra 8 dígitos cria 10^8 (~100M) classes de
//     equivalência — ainda determinístico pra "este número?" lookup mas
//     ofusca prefixos.
//   - LGPD Art.5 §III: "tratamento" inclui pseudonimização; truncar
//     evidencia intenção de minimização.
//
// NÃO usar pra anti-fraud que precise distinguir números diferentes
// que compartilham os últimos 8 dígitos — collision é design.
func HashRecipientE164(e164 string) string {
	digits := make([]byte, 0, len(e164))
	for i := 0; i < len(e164); i++ {
		if e164[i] >= '0' && e164[i] <= '9' {
			digits = append(digits, e164[i])
		}
	}
	if len(digits) < 8 {
		return ""
	}
	last8 := digits[len(digits)-8:]
	h := sha256.Sum256(last8)
	return hex.EncodeToString(h[:])
}

// NewSnowflakeID gera um ID time-ordered curto e único pro audit log.
// Combina nano-timestamp UTC + 4 bytes random hex. NÃO é Twitter
// snowflake oficial (não exigimos cluster-wide unique cross-process),
// mas é monotonic per-process e único na prática.
//
// Trade-off: usar nanos puros vazaria info de wall-clock skew se vários
// pods emitirem audit no mesmo instante (TZ diff, NTP slew). Random
// suffix isola pods.
func NewSnowflakeID() string {
	// Implementação trivial; ID quase-único determinístico.
	// Para audit log volume previsto (1-10 events/sec/pod), nanos +
	// 4-byte rand é over-engineered nem prematuro.
	return time.Now().UTC().Format("20060102T150405.000000000Z") + "-" +
		hex.EncodeToString(randomBytes(4))
}

// randomBytes — wrap minimal sobre crypto/rand. Em ambiente onde a source
// falha (tipicamente test sandbox sem /dev/urandom), fallback determinístico
// via nano-clock mantém uniqueness suficiente pro audit timestamp+random
// pattern (não-criptográfico, mas pra ID monotonic isso é o desejado).
func randomBytes(n int) []byte {
	b := make([]byte, n)
	if _, err := cryptoRandRead(b); err == nil {
		return b
	}
	nanos := time.Now().UnixNano()
	for i := 0; i < n; i++ {
		b[i] = byte(nanos >> (i * 8))
	}
	return b
}
