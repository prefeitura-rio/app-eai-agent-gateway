package services

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/sirupsen/logrus"
)

// ─── HashRecipientE164 ──────────────────────────────────────────────────

func TestHashRecipientE164_Deterministic(t *testing.T) {
	a := HashRecipientE164("5521989091014")
	b := HashRecipientE164("5521989091014")
	if a == "" || a != b {
		t.Errorf("expected deterministic hash, got %q vs %q", a, b)
	}
}

func TestHashRecipientE164_StripsNonDigits(t *testing.T) {
	a := HashRecipientE164("+55 21 9 8909-1014")
	b := HashRecipientE164("5521989091014")
	if a != b {
		t.Errorf("expected hash to be format-invariant; got %q vs %q", a, b)
	}
}

func TestHashRecipientE164_DifferentInputsDifferentHashes(t *testing.T) {
	a := HashRecipientE164("5521989091014")
	b := HashRecipientE164("5521989091015")
	if a == b {
		t.Errorf("expected different hashes for different last-8 digits")
	}
}

func TestHashRecipientE164_TooShort(t *testing.T) {
	if HashRecipientE164("123") != "" {
		t.Errorf("expected empty hash for input with <8 digits")
	}
}

func TestHashRecipientE164_SameLast8(t *testing.T) {
	// Truncamos pra últimos 8 dígitos — design intencional (collision).
	a := HashRecipientE164("5521989091014")
	b := HashRecipientE164("9891014")
	// "9891014" tem só 7 digits, falha. Vamos comparar dois com últimos 8 iguais:
	c := HashRecipientE164("5521989091014")
	d := HashRecipientE164("1189091014")
	// últimos 8 de "5521989091014" = "89091014"
	// últimos 8 de "1189091014" = "89091014"
	_ = a
	_ = b
	if c != d {
		t.Errorf("expected same hash when last-8 digits match (collision is design); got %q vs %q", c, d)
	}
}

// ─── NewSnowflakeID ─────────────────────────────────────────────────────

func TestNewSnowflakeID_Uniqueness(t *testing.T) {
	seen := make(map[string]bool, 100)
	for i := 0; i < 100; i++ {
		id := NewSnowflakeID()
		if id == "" {
			t.Fatalf("empty snowflake ID")
		}
		if seen[id] {
			t.Fatalf("duplicate snowflake ID: %s", id)
		}
		seen[id] = true
	}
}

func TestNewSnowflakeID_Format(t *testing.T) {
	id := NewSnowflakeID()
	// Format: "20060102T150405.000000000Z-XXXXXXXX"
	parts := strings.Split(id, "-")
	if len(parts) != 2 {
		t.Fatalf("expected ID with single '-' separator, got %q", id)
	}
	if !strings.HasSuffix(parts[0], "Z") {
		t.Errorf("expected ISO UTC timestamp before '-', got %q", parts[0])
	}
	if len(parts[1]) != 8 {
		t.Errorf("expected 8 hex chars after '-' (4 bytes), got %d (%q)", len(parts[1]), parts[1])
	}
}

// ─── LogrusAuditLogger ──────────────────────────────────────────────────

// captureLog cria um logrus instance que escreve em buf, JSON formatter.
// Espelha o setup esperado em produção (LogFormat="json").
func captureLog(buf io.Writer) *logrus.Logger {
	l := logrus.New()
	l.SetFormatter(&logrus.JSONFormatter{})
	l.SetOutput(buf)
	l.SetLevel(logrus.InfoLevel)
	return l
}

func TestLogrusAuditLogger_EmitsStructured(t *testing.T) {
	var buf bytes.Buffer
	logger := captureLog(&buf)
	al := NewLogrusAuditLogger(logger)

	entry := AuditEntry{
		SnowflakeID:   "snowflake-001",
		TimestampUTC:  "2026-05-23T12:00:00Z",
		ActionType:    "meta_send_text",
		RecipientHash: "abc123",
		WAMID:         "wamid.HBgM5521",
		MessageID:     "msg-uuid",
		TraceID:       "trace-hex",
		Success:       true,
	}
	al.Log(context.Background(), entry)

	if buf.Len() == 0 {
		t.Fatal("audit logger emitted nothing")
	}
	var parsed map[string]interface{}
	if err := json.Unmarshal(buf.Bytes(), &parsed); err != nil {
		t.Fatalf("audit log output not JSON: %v\n%s", err, buf.String())
	}
	if parsed["event"] != "audit" {
		t.Errorf("expected event=audit, got %v", parsed["event"])
	}
	if parsed["action_type"] != "meta_send_text" {
		t.Errorf("expected action_type=meta_send_text, got %v", parsed["action_type"])
	}
	if parsed["snowflake_id"] != "snowflake-001" {
		t.Errorf("expected snowflake_id=snowflake-001, got %v", parsed["snowflake_id"])
	}
	if parsed["recipient_hash"] != "abc123" {
		t.Errorf("expected recipient_hash=abc123, got %v", parsed["recipient_hash"])
	}
	if parsed["wamid"] != "wamid.HBgM5521" {
		t.Errorf("expected wamid set, got %v", parsed["wamid"])
	}
	if parsed["success"] != true {
		t.Errorf("expected success=true, got %v", parsed["success"])
	}
}

func TestLogrusAuditLogger_FailurePath(t *testing.T) {
	var buf bytes.Buffer
	logger := captureLog(&buf)
	al := NewLogrusAuditLogger(logger)

	entry := AuditEntry{
		SnowflakeID:  "snowflake-002",
		TimestampUTC: "2026-05-23T12:00:00Z",
		ActionType:   "meta_send_media",
		Success:      false,
		ErrorClass:   "meta_4xx",
	}
	al.Log(context.Background(), entry)

	var parsed map[string]interface{}
	if err := json.Unmarshal(buf.Bytes(), &parsed); err != nil {
		t.Fatalf("audit log output not JSON: %v\n%s", err, buf.String())
	}
	if parsed["success"] != false {
		t.Errorf("expected success=false in failure path, got %v", parsed["success"])
	}
	if parsed["error_class"] != "meta_4xx" {
		t.Errorf("expected error_class=meta_4xx, got %v", parsed["error_class"])
	}
}

func TestLogrusAuditLogger_DoesNotLeakBodyContent(t *testing.T) {
	// Garante que NÃO logamos campos sensíveis (body text, captions).
	// AuditEntry shape não tem esses campos por design — esse teste verifica
	// que ninguém adicionou acidentalmente.
	entry := AuditEntry{
		SnowflakeID:   "snowflake-003",
		RecipientHash: "hash",
		ActionType:    "meta_send_text",
	}
	var buf bytes.Buffer
	logger := captureLog(&buf)
	al := NewLogrusAuditLogger(logger)
	al.Log(context.Background(), entry)

	out := buf.String()
	if strings.Contains(out, "body") || strings.Contains(out, "caption") || strings.Contains(out, "media_url") {
		t.Errorf("audit log unexpectedly contains body/caption/media_url field; output:\n%s", out)
	}
}
