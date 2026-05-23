package services

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
)

// ─── Fake Redis (in-memory) ──────────────────────────────────────────────

type fakeRedis struct {
	mu        sync.Mutex
	store     map[string]string
	ttls      map[string]time.Time
	incrErr   error
	expireErr error
	getErr    error
	now       func() time.Time
}

func newFakeRedis() *fakeRedis {
	return &fakeRedis{
		store: map[string]string{},
		ttls:  map[string]time.Time{},
		now:   time.Now,
	}
}

func (f *fakeRedis) Get(_ context.Context, key string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.getErr != nil {
		return "", f.getErr
	}
	v, ok := f.store[key]
	if !ok {
		return "", fmt.Errorf("%w: %s", ErrKeyNotFound, key)
	}
	// Honor TTL — evicted se expirou
	if t, hasTTL := f.ttls[key]; hasTTL && f.now().After(t) {
		delete(f.store, key)
		delete(f.ttls, key)
		return "", fmt.Errorf("%w: %s", ErrKeyNotFound, key)
	}
	return v, nil
}

func (f *fakeRedis) SetValue(_ context.Context, key string, value interface{}, ttl time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.store[key] = fmt.Sprintf("%v", value)
	if ttl > 0 {
		f.ttls[key] = f.now().Add(ttl)
	}
	return nil
}

func (f *fakeRedis) Increment(_ context.Context, key string) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.incrErr != nil {
		return 0, f.incrErr
	}
	current := 0
	if v, ok := f.store[key]; ok {
		_, _ = fmt.Sscanf(v, "%d", &current)
	}
	current++
	f.store[key] = fmt.Sprintf("%d", current)
	return int64(current), nil
}

func (f *fakeRedis) Expire(_ context.Context, key string, ttl time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.expireErr != nil {
		return f.expireErr
	}
	if _, ok := f.store[key]; ok {
		f.ttls[key] = f.now().Add(ttl)
	}
	return nil
}

func (f *fakeRedis) Delete(_ context.Context, key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.store, key)
	delete(f.ttls, key)
	return nil
}

// ─── AllowMessage tests ─────────────────────────────────────────────────

func testLogger() *logrus.Logger {
	l := logrus.New()
	l.SetOutput(io.Discard)
	return l
}

func TestDoSRateLimiter_AllowMessage_UnderLimit(t *testing.T) {
	redis := newFakeRedis()
	cfg := DoSRateLimiterConfig{
		Enabled:                true,
		MsgPerUserPerWindow:    5,
		WindowDuration:         15 * time.Minute,
		AuthFailPerIPPerWindow: 5,
		AuthFailBlockDuration:  15 * time.Minute,
	}
	limiter := NewDoSRateLimiterService(cfg, testLogger(), redis)

	for i := 0; i < 5; i++ {
		allowed, retryAfter, count, err := limiter.AllowMessage(context.Background(), "5521989091014")
		if err != nil {
			t.Fatalf("unexpected error iter %d: %v", i, err)
		}
		if !allowed {
			t.Fatalf("iter %d: expected allowed=true, got false (count=%d, retryAfter=%s)", i, count, retryAfter)
		}
		if count != i+1 {
			t.Fatalf("iter %d: expected count=%d, got %d", i, i+1, count)
		}
		if retryAfter != 0 {
			t.Fatalf("iter %d: expected retryAfter=0, got %s", i, retryAfter)
		}
	}
}

func TestDoSRateLimiter_AllowMessage_AtLimit(t *testing.T) {
	// Cap=5; 5ª chamada ainda allowed (count == cap), 6ª = block.
	redis := newFakeRedis()
	cfg := DoSRateLimiterConfig{
		Enabled:             true,
		MsgPerUserPerWindow: 5,
		WindowDuration:      15 * time.Minute,
	}
	limiter := NewDoSRateLimiterService(cfg, testLogger(), redis)

	var lastCount int
	for i := 0; i < 5; i++ {
		allowed, _, count, err := limiter.AllowMessage(context.Background(), "5521989091014")
		if err != nil || !allowed {
			t.Fatalf("iter %d: expected ok, got allowed=%v err=%v", i, allowed, err)
		}
		lastCount = count
	}
	if lastCount != 5 {
		t.Fatalf("expected final count=5, got %d", lastCount)
	}
}

func TestDoSRateLimiter_AllowMessage_OverLimit(t *testing.T) {
	redis := newFakeRedis()
	cfg := DoSRateLimiterConfig{
		Enabled:             true,
		MsgPerUserPerWindow: 3,
		WindowDuration:      15 * time.Minute,
	}
	limiter := NewDoSRateLimiterService(cfg, testLogger(), redis)

	// 3 allowed
	for i := 0; i < 3; i++ {
		allowed, _, _, _ := limiter.AllowMessage(context.Background(), "5521989091014")
		if !allowed {
			t.Fatalf("iter %d: expected allowed under cap", i)
		}
	}
	// 4ª deve bloquear
	allowed, retryAfter, count, err := limiter.AllowMessage(context.Background(), "5521989091014")
	if err != nil {
		t.Fatalf("unexpected error on over-limit: %v", err)
	}
	if allowed {
		t.Fatalf("expected blocked at count=%d", count)
	}
	if count != 4 {
		t.Fatalf("expected count=4 on first block, got %d", count)
	}
	if retryAfter < time.Second {
		t.Fatalf("expected retryAfter >= 1s, got %s", retryAfter)
	}
}

func TestDoSRateLimiter_AllowMessage_WindowReset(t *testing.T) {
	// Bucket id = floor(time.Now().Unix() / window_seconds). Window-reset
	// real precisa do tempo wall-clock avançar — em teste rapido usamos uma
	// janela curta (1s) e dormimos 1.1s pra forçar o roll.
	if testing.Short() {
		t.Skip("skipping window reset test in -short mode (requires 1s sleep)")
	}
	redis := newFakeRedis()
	cfg := DoSRateLimiterConfig{
		Enabled:             true,
		MsgPerUserPerWindow: 2,
		WindowDuration:      1 * time.Second,
	}
	limiter := NewDoSRateLimiterService(cfg, testLogger(), redis)

	// Esgotar bucket atual: 2 allow + 1 block
	_, _, _, _ = limiter.AllowMessage(context.Background(), "5521989091014")
	_, _, _, _ = limiter.AllowMessage(context.Background(), "5521989091014")
	allowed, _, _, _ := limiter.AllowMessage(context.Background(), "5521989091014")
	if allowed {
		t.Fatalf("expected block within bucket")
	}

	// Aguardar o bucket rolar. Floor(unix/1) muda quando o segundo wall
	// avança — dormimos +1.5s pra garantir, dado que start pode estar
	// próximo do roll-over.
	time.Sleep(1500 * time.Millisecond)

	// Nova chamada: bucket diferente, count reseta — allow.
	allowed, _, count, err := limiter.AllowMessage(context.Background(), "5521989091014")
	if err != nil {
		t.Fatalf("unexpected error after window reset: %v", err)
	}
	if !allowed {
		t.Fatalf("expected allow after window reset, got blocked (count=%d)", count)
	}
	if count != 1 {
		t.Fatalf("expected count=1 in new bucket, got %d", count)
	}
}

func TestDoSRateLimiter_AllowMessage_Disabled(t *testing.T) {
	cfg := DoSRateLimiterConfig{Enabled: false, MsgPerUserPerWindow: 1}
	limiter := NewDoSRateLimiterService(cfg, testLogger(), nil)
	for i := 0; i < 100; i++ {
		allowed, _, _, err := limiter.AllowMessage(context.Background(), "5521989091014")
		if err != nil || !allowed {
			t.Fatalf("disabled limiter should always allow")
		}
	}
}

func TestDoSRateLimiter_AllowMessage_NilRedis_FailsOpen(t *testing.T) {
	cfg := DoSRateLimiterConfig{
		Enabled:             true,
		MsgPerUserPerWindow: 1,
		WindowDuration:      15 * time.Minute,
	}
	limiter := NewDoSRateLimiterService(cfg, testLogger(), nil)
	allowed, _, _, err := limiter.AllowMessage(context.Background(), "5521989091014")
	if err != nil {
		t.Fatalf("nil redis should fail-open without error: %v", err)
	}
	if !allowed {
		t.Fatalf("nil redis should fail-open allow")
	}
}

func TestDoSRateLimiter_AllowMessage_RedisErr_FailsOpen(t *testing.T) {
	redis := newFakeRedis()
	redis.incrErr = errors.New("simulated redis down")
	cfg := DoSRateLimiterConfig{
		Enabled:             true,
		MsgPerUserPerWindow: 1,
		WindowDuration:      15 * time.Minute,
	}
	limiter := NewDoSRateLimiterService(cfg, testLogger(), redis)
	allowed, _, _, err := limiter.AllowMessage(context.Background(), "5521989091014")
	if err == nil {
		t.Fatalf("expected propagated error from redis incr")
	}
	if !allowed {
		t.Fatalf("redis error path should fail-open allow=true")
	}
}

func TestDoSRateLimiter_AllowMessage_EmptyE164(t *testing.T) {
	limiter := NewDoSRateLimiterService(DoSRateLimiterConfig{Enabled: true}, testLogger(), newFakeRedis())
	allowed, _, _, _ := limiter.AllowMessage(context.Background(), "")
	if !allowed {
		t.Fatalf("empty e164 should be allowed (logged Warn but not blocked)")
	}
}

func TestDoSRateLimiter_AllowMessage_NormalizesE164(t *testing.T) {
	// Diferentes formatos do mesmo telefone devem usar a mesma key.
	redis := newFakeRedis()
	cfg := DoSRateLimiterConfig{
		Enabled:             true,
		MsgPerUserPerWindow: 1,
		WindowDuration:      15 * time.Minute,
	}
	limiter := NewDoSRateLimiterService(cfg, testLogger(), redis)

	// "55 21 98909-1014" → "5521989091014"
	_, _, _, _ = limiter.AllowMessage(context.Background(), "+55 21 98909-1014")
	// "5521989091014" — mesma key, deve bloquear (2ª chamada > cap=1)
	allowed, _, count, _ := limiter.AllowMessage(context.Background(), "5521989091014")
	if allowed {
		t.Fatalf("expected block after normalization match; count=%d", count)
	}
}

// ─── Auth failure path ──────────────────────────────────────────────────

func TestDoSRateLimiter_RecordAuthFailure_UnderLimit(t *testing.T) {
	redis := newFakeRedis()
	cfg := DoSRateLimiterConfig{
		Enabled:                true,
		AuthFailPerIPPerWindow: 5,
		AuthFailBlockDuration:  15 * time.Minute,
	}
	limiter := NewDoSRateLimiterService(cfg, testLogger(), redis)
	for i := 0; i < 4; i++ {
		blocked, _, err := limiter.RecordAuthFailure(context.Background(), "1.2.3.4")
		if err != nil {
			t.Fatalf("iter %d: unexpected err %v", i, err)
		}
		if blocked {
			t.Fatalf("iter %d: should not block before cap", i)
		}
	}
}

func TestDoSRateLimiter_RecordAuthFailure_AtCap(t *testing.T) {
	redis := newFakeRedis()
	cfg := DoSRateLimiterConfig{
		Enabled:                true,
		AuthFailPerIPPerWindow: 5,
		AuthFailBlockDuration:  15 * time.Minute,
	}
	limiter := NewDoSRateLimiterService(cfg, testLogger(), redis)
	var blocked bool
	for i := 0; i < 5; i++ {
		blocked, _, _ = limiter.RecordAuthFailure(context.Background(), "1.2.3.4")
	}
	if !blocked {
		t.Fatalf("expected blocked on 5th auth fail (cap=5)")
	}
}

func TestDoSRateLimiter_IsIPBlocked_Idempotent(t *testing.T) {
	redis := newFakeRedis()
	cfg := DoSRateLimiterConfig{
		Enabled:                true,
		AuthFailPerIPPerWindow: 3,
		AuthFailBlockDuration:  15 * time.Minute,
	}
	limiter := NewDoSRateLimiterService(cfg, testLogger(), redis)

	// Não bloqueado inicialmente
	blocked, _, _ := limiter.IsIPBlocked(context.Background(), "1.2.3.4")
	if blocked {
		t.Fatalf("expected not blocked initially")
	}
	// Esgotar
	for i := 0; i < 3; i++ {
		_, _, _ = limiter.RecordAuthFailure(context.Background(), "1.2.3.4")
	}
	// Agora bloqueado, e IsIPBlocked não muda estado (consultas idempotentes)
	blocked, _, _ = limiter.IsIPBlocked(context.Background(), "1.2.3.4")
	if !blocked {
		t.Fatalf("expected blocked after 3 auth fails")
	}
	blocked2, _, _ := limiter.IsIPBlocked(context.Background(), "1.2.3.4")
	if !blocked2 {
		t.Fatalf("IsIPBlocked should remain true on second call")
	}
}

// ─── Helpers tests ───────────────────────────────────────────────────────

func TestNormalizeDoSE164(t *testing.T) {
	cases := map[string]string{
		"+55 21 98909-1014":  "5521989091014",
		"5521989091014":      "5521989091014",
		"":                   "",
		"abc":                "",
		"+1-(415)-555-1234":  "14155551234",
	}
	for in, want := range cases {
		got := normalizeDoSE164(in)
		if got != want {
			t.Errorf("normalizeDoSE164(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestMaskDoSE164(t *testing.T) {
	cases := map[string]string{
		"5521989091014": "55219890…",
		"123":           "123",
		"":              "",
	}
	for in, want := range cases {
		got := maskDoSE164(in)
		if got != want {
			t.Errorf("maskDoSE164(%q) = %q, want %q", in, got, want)
		}
	}
}
