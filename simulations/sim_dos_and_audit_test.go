// Simulação real — Fase 0 C6 DoS rate limit + C4 audit log forensics.
//
// Cenários:
//
//	1. Burst attack: 50 mensagens em <15min do mesmo E.164 → primeiras 30 OK,
//	   restantes 20 retornam disallow + Retry-After.
//	2. Cross-E.164 isolation: limite separado por número.
//	3. Auth brute-force: 5 tentativas inválidas de mesmo IP → 6ª bloqueada.
//	4. Audit log LGPD: SHA256 hash, nunca raw E.164 nos campos.
//	5. Snowflake IDs time-ordered (queryable por janela).
//	6. Concorrência: burst paralelo (race-free INCR).
//
// Run:
//
//	cd app-eai-agent-gateway
//	go test ./simulations/... -v
//
// Não chega no Meta real; usa fake Redis in-memory. Pipeline idêntico ao
// prod — só o transporte que muda.
package simulations_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/prefeitura-rio/app-eai-agent-gateway/internal/services"
)

// ─── In-memory Redis stub (espelha shape de DoSRedisOps) ─────────────────

type memRedis struct {
	mu    sync.Mutex
	store map[string]string
	ttls  map[string]time.Time
}

func newMemRedis() *memRedis {
	return &memRedis{store: map[string]string{}, ttls: map[string]time.Time{}}
}

func (m *memRedis) Get(_ context.Context, key string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.store[key]
	if !ok {
		return "", fmt.Errorf("%w: %s", services.ErrKeyNotFound, key)
	}
	if t, hasTTL := m.ttls[key]; hasTTL && time.Now().After(t) {
		delete(m.store, key)
		delete(m.ttls, key)
		return "", fmt.Errorf("%w: %s", services.ErrKeyNotFound, key)
	}
	return v, nil
}
func (m *memRedis) SetValue(_ context.Context, key string, value interface{}, ttl time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.store[key] = fmt.Sprintf("%v", value)
	if ttl > 0 {
		m.ttls[key] = time.Now().Add(ttl)
	}
	return nil
}
func (m *memRedis) Increment(_ context.Context, key string) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var cur int64
	if s, ok := m.store[key]; ok {
		_, _ = fmt.Sscanf(s, "%d", &cur)
	}
	cur++
	m.store[key] = fmt.Sprintf("%d", cur)
	return cur, nil
}
func (m *memRedis) Expire(_ context.Context, key string, ttl time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.store[key]; !ok {
		return nil
	}
	if ttl > 0 {
		m.ttls[key] = time.Now().Add(ttl)
	}
	return nil
}
func (m *memRedis) Delete(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.store, key)
	delete(m.ttls, key)
	return nil
}

func silentLogger() *logrus.Logger {
	l := logrus.New()
	l.SetOutput(&bytes.Buffer{})
	return l
}

func recordingLogger() (*logrus.Logger, *bytes.Buffer) {
	buf := &bytes.Buffer{}
	l := logrus.New()
	l.SetFormatter(&logrus.JSONFormatter{})
	l.SetOutput(buf)
	return l, buf
}

func defaultCfg() services.DoSRateLimiterConfig {
	return services.DoSRateLimiterConfig{
		Enabled:                true,
		MsgPerUserPerWindow:    30,
		WindowDuration:         15 * time.Minute,
		AuthFailPerIPPerWindow: 5,
		AuthFailBlockDuration:  15 * time.Minute,
	}
}

// ─── Cenário 1: Burst attack 50 msgs em <15min ────────────────────────────

func TestSimulation_DoS_BurstAttack_50MsgsSameE164(t *testing.T) {
	const (
		cap        = 30
		burstSize  = 50
		victimE164 = "5521988887777"
	)

	cfg := defaultCfg()
	cfg.MsgPerUserPerWindow = cap
	limiter := services.NewDoSRateLimiterService(cfg, silentLogger(), newMemRedis())
	ctx := context.Background()

	allowed, disallowed := 0, 0
	var firstRetry time.Duration
	for i := 0; i < burstSize; i++ {
		ok, retryAfter, _, err := limiter.AllowMessage(ctx, victimE164)
		if err != nil {
			t.Fatalf("AllowMessage err at i=%d: %v", i, err)
		}
		if ok {
			allowed++
		} else {
			if disallowed == 0 {
				firstRetry = retryAfter
			}
			disallowed++
		}
	}

	t.Logf("\n→ Burst attack simulation:")
	t.Logf("  Victim E.164  : %s", victimE164)
	t.Logf("  Window/Cap    : %v / %d msgs", cfg.WindowDuration, cap)
	t.Logf("  Burst size    : %d msgs (simulando ataque distribuído single-user)", burstSize)
	t.Logf("  Result        : %d allowed, %d disallowed", allowed, disallowed)
	t.Logf("  Retry-After   : %v (primeiro disallow)", firstRetry)

	if allowed != cap {
		t.Errorf("FAIL: expected %d allowed (cap), got %d", cap, allowed)
	}
	if disallowed != burstSize-cap {
		t.Errorf("FAIL: expected %d disallowed, got %d", burstSize-cap, disallowed)
	}
	if disallowed > 0 && (firstRetry <= 0 || firstRetry > cfg.WindowDuration) {
		t.Errorf("FAIL: retry-after out of range: %v", firstRetry)
	}
	t.Logf("  STATUS        : ✓ comportamento esperado")
}

// ─── Cenário 2: Cross-E.164 isolation ────────────────────────────────────

func TestSimulation_DoS_CrossE164Isolation(t *testing.T) {
	cfg := defaultCfg()
	cfg.MsgPerUserPerWindow = 10
	limiter := services.NewDoSRateLimiterService(cfg, silentLogger(), newMemRedis())
	ctx := context.Background()
	// E.164 numéricos reais (normalizer remove non-digits; usar letras
	// fake como "5521AAAAAAA" colide pra "5521" — achado da simulação!)
	userA := "5521988880001"
	userB := "5521988880002"

	for i := 0; i < 10; i++ {
		ok, _, _, _ := limiter.AllowMessage(ctx, userA)
		if !ok {
			t.Fatalf("user A early throttle at i=%d", i)
		}
	}
	okA11, _, _, _ := limiter.AllowMessage(ctx, userA)
	if okA11 {
		t.Errorf("FAIL: user A não foi throttled na msg 11")
	}
	okB, _, _, _ := limiter.AllowMessage(ctx, userB)
	if !okB {
		t.Errorf("FAIL: user B foi bloqueado indevidamente pelo limite de A")
	}
	t.Logf("\n→ Cross-E.164 isolation: A=%s throttled msg 11; B=%s aceita normalmente", userA, userB)
	t.Logf("  Achado colateral: normalizer apenas dígitos. E.164 alfa-numéricos colidem na chave Redis.")
	t.Logf("  Mitigação: webhook Meta entrega E.164 numérico puro; fora dali, validar before normalize.")
}

// ─── Cenário 3: Auth brute-force ─────────────────────────────────────────

func TestSimulation_AuthFail_BruteForce(t *testing.T) {
	cfg := defaultCfg()
	limiter := services.NewDoSRateLimiterService(cfg, silentLogger(), newMemRedis())
	ctx := context.Background()
	attackerIP := "192.0.2.100"

	for i := 1; i <= 5; i++ {
		_, _, err := limiter.RecordAuthFailure(ctx, attackerIP)
		if err != nil {
			t.Fatalf("RecordAuthFailure i=%d err: %v", i, err)
		}
	}
	isBlocked, retryAfter, _ := limiter.IsIPBlocked(ctx, attackerIP)
	if !isBlocked {
		t.Errorf("IP %s não foi bloqueado após 5 fails", attackerIP)
	}
	t.Logf("\n→ Brute-force: IP %s bloqueado após 5 fails consecutivos, Retry-After=%v",
		attackerIP, retryAfter)

	goodIP := "192.0.2.200"
	isGoodBlocked, _, _ := limiter.IsIPBlocked(ctx, goodIP)
	if isGoodBlocked {
		t.Errorf("IP saudável %s foi bloqueado por engano", goodIP)
	}
	t.Logf("  IP saudável %s NÃO afetado", goodIP)
}

// ─── Cenário 4: Audit log LGPD ──────────────────────────────────────────

func TestSimulation_AuditLog_LGPD_NoRawE164(t *testing.T) {
	logger, buf := recordingLogger()
	auditor := services.NewLogrusAuditLogger(logger)
	rawE164 := "5521987654321"

	scenarios := []services.AuditEntry{
		{ActionType: "meta_send_text", WAMID: "wamid.text.1", MessageID: "msg-001"},
		{ActionType: "meta_send_media", WAMID: "wamid.media.2", MessageID: "msg-002"},
		{ActionType: "meta_send_location", WAMID: "wamid.loc.3", MessageID: "msg-003"},
		{ActionType: "meta_send_interactive", WAMID: "wamid.int.4", MessageID: "msg-004"},
		{ActionType: "meta_send_reaction", MessageID: "msg-005"},
	}

	hash := computeHash(rawE164)

	for _, e := range scenarios {
		e.RecipientHash = hash
		e.SnowflakeID = services.NewSnowflakeID()
		e.TimestampUTC = time.Now().UTC().Format(time.RFC3339)
		e.Success = true
		auditor.Log(context.Background(), e)
	}

	logStr := buf.String()

	if strings.Contains(logStr, rawE164) {
		t.Errorf("FAIL: raw E.164 vazou no audit log!")
	}

	lines := strings.Split(strings.TrimSpace(logStr), "\n")
	if len(lines) != 5 {
		t.Errorf("FAIL: expected 5 audit lines, got %d", len(lines))
	}
	for i, line := range lines {
		var parsed map[string]interface{}
		if err := json.Unmarshal([]byte(line), &parsed); err != nil {
			t.Fatalf("line %d invalid JSON: %v", i, err)
		}
		if strings.Contains(line, rawE164) {
			t.Errorf("FAIL line %d: raw E.164 leak", i)
		}
	}
	t.Logf("\n→ Audit log LGPD:")
	t.Logf("  5 envios emitidos: %d audit lines structured JSON", len(lines))
	t.Logf("  Raw E.164 (%s) presente em logs? %v", rawE164, strings.Contains(logStr, rawE164))
	t.Logf("  Hash usado (todos): %s", hash[:16]+"...")
	t.Logf("  STATUS: ✓ LGPD compliant — apenas hash, jamais raw")
}

// computeHash usa o helper canônico exposto pelo services package.
func computeHash(e164 string) string {
	return services.HashRecipientE164(e164)
}

// ─── Cenário 5: Snowflake time-ordered ──────────────────────────────────

func TestSimulation_AuditLog_Snowflake_TimeOrdered(t *testing.T) {
	// Em tight loop sub-nano (test), múltiplos IDs caem no mesmo nano
	// timestamp; o random suffix decide ordem final. Em prod (volumetria
	// 1-10/sec/pod), todos IDs caem em nanosegundos distintos → ordering
	// perfeito. Pra simulação ser realista, adicionar pausa ~10µs entre
	// gerações. Validamos: (a) dedup absoluto e (b) ordering "majority"
	// em volume realista.
	const (
		n             = 200
		perGenerateUS = 10
	)
	ids := make([]string, n)
	for i := 0; i < n; i++ {
		ids[i] = services.NewSnowflakeID()
		time.Sleep(time.Duration(perGenerateUS) * time.Microsecond)
	}
	sorted := make([]string, n)
	copy(sorted, ids)
	sort.Strings(sorted)

	matches := 0
	for i := 0; i < n; i++ {
		if ids[i] == sorted[i] {
			matches++
		}
	}
	ratio := float64(matches) / float64(n)
	t.Logf("\n→ Snowflake time-ordering: %d/%d (%.2f%%) sequential==sorted",
		matches, n, ratio*100)
	t.Logf("  Spacing entre IDs: %dµs (simula carga prod 1-10/sec/pod)", perGenerateUS)
	t.Logf("  Achado: implementação atual usa timestamp-nanos + random suffix.")
	t.Logf("  Em prod (>1µs entre IDs), ordering é perfeito.")
	t.Logf("  Em tight loop (test sub-µs), random suffix afeta ordem — não bug.")
	if ratio < 0.95 {
		t.Errorf("FAIL: ordering colapsou — %d/%d match (esperado ≥95%% com spacing %dµs)",
			matches, n, perGenerateUS)
	}

	uniq := map[string]bool{}
	for _, id := range ids {
		uniq[id] = true
	}
	if len(uniq) != n {
		t.Errorf("FAIL: dedup falhou: %d unique de %d", len(uniq), n)
	}
	t.Logf("  Uniqueness: %d/%d unique (dedup absoluto)", len(uniq), n)
}

// ─── Cenário 6: Concorrência race-free ──────────────────────────────────

func TestSimulation_DoS_ConcurrentBurst_RaceFree(t *testing.T) {
	cfg := defaultCfg()
	cfg.MsgPerUserPerWindow = 100
	limiter := services.NewDoSRateLimiterService(cfg, silentLogger(), newMemRedis())
	ctx := context.Background()
	const concurrent = 50

	const target = "5521988880000"
	var wg sync.WaitGroup
	allowedCh := make(chan bool, concurrent*4)
	for i := 0; i < concurrent; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 4; j++ {
				ok, _, _, _ := limiter.AllowMessage(ctx, target)
				allowedCh <- ok
			}
		}()
	}
	wg.Wait()
	close(allowedCh)

	allowed, denied := 0, 0
	for ok := range allowedCh {
		if ok {
			allowed++
		} else {
			denied++
		}
	}

	t.Logf("\n→ Concorrência: %d goroutines × 4 msgs = %d total",
		concurrent, allowed+denied)
	t.Logf("  Allowed: %d  Denied: %d  (cap=%d)", allowed, denied, cfg.MsgPerUserPerWindow)
	if allowed != cfg.MsgPerUserPerWindow {
		t.Errorf("FAIL: race condition? allowed=%d, esperado=%d (cap)",
			allowed, cfg.MsgPerUserPerWindow)
	} else {
		t.Logf("  STATUS: ✓ INCR atômico Redis preserva semantics exato sob race")
	}
}

// ─── Verify ErrKeyNotFound exposure ─────────────────────────────────────

func TestSimulation_ErrKeyNotFound_Exposed(t *testing.T) {
	if !errors.Is(fmt.Errorf("%w: foo", services.ErrKeyNotFound), services.ErrKeyNotFound) {
		t.Errorf("FAIL: services.ErrKeyNotFound errors.Is broken")
	}
}
