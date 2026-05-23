package services

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
)

// ─── ExtractTokenUsage ──────────────────────────────────────────────────

func TestExtractTokenUsage_FromMessagesList(t *testing.T) {
	data := map[string]interface{}{
		"messages": []interface{}{
			map[string]interface{}{"message_type": "assistant_message", "content": "ok"},
			map[string]interface{}{
				"message_type":  "usage_statistics",
				"prompt_tokens": float64(120),
				"completion_tokens": float64(45),
				"total_tokens":  float64(165),
			},
		},
	}
	got := ExtractTokenUsage(data)
	if got != 165 {
		t.Errorf("expected total_tokens=165, got %d", got)
	}
}

func TestExtractTokenUsage_FallsBackToSum(t *testing.T) {
	// Sem total_tokens explícito — soma prompt + completion + thoughts.
	data := map[string]interface{}{
		"messages": []interface{}{
			map[string]interface{}{
				"message_type":      "usage_statistics",
				"prompt_tokens":     float64(100),
				"completion_tokens": float64(50),
				"thoughts_tokens":   float64(20),
			},
		},
	}
	got := ExtractTokenUsage(data)
	if got != 170 {
		t.Errorf("expected sum=170 when total_tokens missing, got %d", got)
	}
}

func TestExtractTokenUsage_NestedShape(t *testing.T) {
	// Shape alternativo com campo aninhado `usage_statistics`.
	data := map[string]interface{}{
		"messages": []interface{}{
			map[string]interface{}{
				"usage_statistics": map[string]interface{}{
					"total_tokens": float64(500),
				},
			},
		},
	}
	got := ExtractTokenUsage(data)
	if got != 500 {
		t.Errorf("expected nested total_tokens=500, got %d", got)
	}
}

func TestExtractTokenUsage_NoMatch(t *testing.T) {
	data := map[string]interface{}{
		"messages": []interface{}{
			map[string]interface{}{"message_type": "assistant_message", "content": "no usage here"},
		},
	}
	got := ExtractTokenUsage(data)
	if got != 0 {
		t.Errorf("expected 0 when no usage_statistics, got %d", got)
	}
}

func TestExtractTokenUsage_EmptyData(t *testing.T) {
	if got := ExtractTokenUsage(nil); got != 0 {
		t.Errorf("expected 0 for nil data, got %d", got)
	}
	if got := ExtractTokenUsage(map[string]interface{}{}); got != 0 {
		t.Errorf("expected 0 for empty data, got %d", got)
	}
}

// ─── TokenBudgetService ─────────────────────────────────────────────────

// fakeBudgetRedis — in-memory store dedicated to budget tests
// (separa do fakeRedis genérico pra isolar contract).
type fakeBudgetRedis struct {
	store map[string]string
}

func newFakeBudgetRedis() *fakeBudgetRedis {
	return &fakeBudgetRedis{store: map[string]string{}}
}

func (f *fakeBudgetRedis) Get(_ context.Context, key string) (string, error) {
	v, ok := f.store[key]
	if !ok {
		return "", ErrKeyNotFound
	}
	return v, nil
}

func (f *fakeBudgetRedis) SetValue(_ context.Context, key string, value interface{}, _ time.Duration) error {
	switch v := value.(type) {
	case int:
		f.store[key] = strconvItoa(v)
	case string:
		f.store[key] = v
	default:
		f.store[key] = "?"
	}
	return nil
}

func (f *fakeBudgetRedis) Expire(_ context.Context, _ string, _ time.Duration) error {
	return nil
}

// IncrementBy faz INCRBY atomicamente. Mock simples lê o int existente,
// soma o delta, salva, e retorna o novo valor. Suficiente pra unit
// tests; concorrência real é coberta no Redis ao runtime.
func (f *fakeBudgetRedis) IncrementBy(_ context.Context, key string, delta int64) (int64, error) {
	current := int64(0)
	if v, ok := f.store[key]; ok {
		n := int64(0)
		_, _ = fmtSscanf64(v, &n)
		current = n
	}
	newVal := current + delta
	f.store[key] = strconvItoa64(newVal)
	return newVal, nil
}

// strconvItoa64 / fmtSscanf64 — helpers locais sem import strconv pra
// manter o test file alinhado com strconvItoa.
func strconvItoa64(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := false
	if n < 0 {
		neg = true
		n = -n
	}
	var buf [32]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

func fmtSscanf64(s string, n *int64) (int, error) {
	var val int64
	neg := false
	i := 0
	if len(s) > 0 && s[0] == '-' {
		neg = true
		i = 1
	}
	for ; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			break
		}
		val = val*10 + int64(s[i]-'0')
	}
	if neg {
		val = -val
	}
	*n = val
	return 1, nil
}

// strconvItoa local — evita import strconv só pra um teste.
func strconvItoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := false
	if n < 0 {
		neg = true
		n = -n
	}
	var buf [16]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

func captureBudgetLog(buf *bytes.Buffer) *logrus.Logger {
	l := logrus.New()
	l.SetFormatter(&logrus.JSONFormatter{})
	l.SetOutput(buf)
	l.SetLevel(logrus.InfoLevel)
	return l
}

func TestTokenBudget_UnderThreshold_NoWarn(t *testing.T) {
	redis := newFakeBudgetRedis()
	var buf bytes.Buffer
	cfg := TokenBudgetConfig{
		Enabled:          true,
		PerUserDaily:     100000,
		WarnThresholdPct: 70,
	}
	svc := NewTokenBudgetService(cfg, captureBudgetLog(&buf), redis)
	svc.AddTokensSpent(context.Background(), "5521989091014", 5000)
	if strings.Contains(buf.String(), "token_budget_threshold_warn") {
		t.Errorf("did not expect warn under threshold; output:\n%s", buf.String())
	}
}

func TestTokenBudget_WarnAt70Pct(t *testing.T) {
	redis := newFakeBudgetRedis()
	var buf bytes.Buffer
	cfg := TokenBudgetConfig{
		Enabled:          true,
		PerUserDaily:     100,
		WarnThresholdPct: 70,
	}
	svc := NewTokenBudgetService(cfg, captureBudgetLog(&buf), redis)
	// 65 then 75: cruzar 70 emite warn UMA vez (when current<70 and new>=70).
	svc.AddTokensSpent(context.Background(), "5521989091014", 65)
	if strings.Contains(buf.String(), "token_budget_threshold_warn") {
		t.Errorf("warn fired too early at 65/100")
	}
	svc.AddTokensSpent(context.Background(), "5521989091014", 10) // total 75 >= 70
	if !strings.Contains(buf.String(), "token_budget_threshold_warn") {
		t.Errorf("expected warn at 75/100 (≥70%%); output:\n%s", buf.String())
	}
}

func TestTokenBudget_HitsCap(t *testing.T) {
	redis := newFakeBudgetRedis()
	var buf bytes.Buffer
	cfg := TokenBudgetConfig{
		Enabled:          true,
		PerUserDaily:     100,
		WarnThresholdPct: 70,
	}
	svc := NewTokenBudgetService(cfg, captureBudgetLog(&buf), redis)
	// Acumular até passar 100
	svc.AddTokensSpent(context.Background(), "5521989091014", 80)
	svc.AddTokensSpent(context.Background(), "5521989091014", 30) // 110 > 100

	if !strings.Contains(buf.String(), "token_budget_exceeded") {
		t.Errorf("expected exceeded log; output:\n%s", buf.String())
	}

	// IsBudgetExceeded ergue true
	exceeded, total, cap := svc.IsBudgetExceeded(context.Background(), "5521989091014")
	if !exceeded {
		t.Errorf("expected exceeded=true at total=%d cap=%d", total, cap)
	}
	if total < cap {
		t.Errorf("expected total>=cap; total=%d cap=%d", total, cap)
	}
}

func TestTokenBudget_Disabled(t *testing.T) {
	cfg := TokenBudgetConfig{Enabled: false, PerUserDaily: 1, WarnThresholdPct: 70}
	svc := NewTokenBudgetService(cfg, captureBudgetLog(&bytes.Buffer{}), newFakeBudgetRedis())
	svc.AddTokensSpent(context.Background(), "5521989091014", 999999)
	exceeded, _, _ := svc.IsBudgetExceeded(context.Background(), "5521989091014")
	if exceeded {
		t.Errorf("disabled service should never report exceeded")
	}
}

func TestTokenBudget_DailyReset(t *testing.T) {
	// Daily reset implícito: key inclui yyyy-mm-dd. Não testamos cross-day
	// real (precisa mock clock), mas testamos que e164 normalizado dá key
	// consistente e que IsBudgetExceeded retorna 0 quando key não existe.
	redis := newFakeBudgetRedis()
	cfg := TokenBudgetConfig{
		Enabled:          true,
		PerUserDaily:     100,
		WarnThresholdPct: 70,
	}
	svc := NewTokenBudgetService(cfg, captureBudgetLog(&bytes.Buffer{}), redis)
	exceeded, total, _ := svc.IsBudgetExceeded(context.Background(), "5521989091014")
	if exceeded {
		t.Errorf("expected not exceeded on fresh key")
	}
	if total != 0 {
		t.Errorf("expected total=0 on fresh key, got %d", total)
	}
}

// Fix codex P2 2026-05-23 — atomicidade do counter via INCRBY.
// Garante que sequência de increments produz total correto mesmo quando
// disparados em sequência cerrada (proxy pra teste de concorrência —
// race detector pega bugs em paralelo). Mock IncrementBy simula o
// comportamento atômico do Redis.
func TestTokenBudget_IncrementsAreSummed_NotOverwritten(t *testing.T) {
	redis := newFakeBudgetRedis()
	cfg := TokenBudgetConfig{
		Enabled:          true,
		PerUserDaily:     1000,
		WarnThresholdPct: 70,
	}
	svc := NewTokenBudgetService(cfg, captureBudgetLog(&bytes.Buffer{}), redis)
	// Three increments: 100 + 50 + 75 = 225. Pre-fix (read-modify-write
	// com mock que retornava 0 sempre em Get devido a chamadas ENTRE
	// SetValues) podia produzir last-wins = 75. Com IncrementBy atomicamente
	// somando, total = 225.
	svc.AddTokensSpent(context.Background(), "5521989091014", 100)
	svc.AddTokensSpent(context.Background(), "5521989091014", 50)
	svc.AddTokensSpent(context.Background(), "5521989091014", 75)

	_, total, _ := svc.IsBudgetExceeded(context.Background(), "5521989091014")
	if total != 225 {
		t.Errorf("expected total=225 (atomic INCRBY sum), got %d (last-wins bug?)", total)
	}
}
