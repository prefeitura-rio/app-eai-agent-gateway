package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"

	"github.com/prefeitura-rio/app-eai-agent-gateway/internal/config"
)

// Synthetic test data (never a real citizen).
const (
	testVictimNumber   = "5521999991234"
	testInitiateToken  = "initiate-test-token"
	testServiceContext = "consulta_dados"
)

// memRedis is an in-memory RedisServiceInterface with real per-key SETNX and
// an optional Delete, enough to drive the gov.br flow end to end.
type memRedis struct {
	mu        sync.Mutex
	data      map[string]string
	setNXErr  error
	deleteErr error
}

func newMemRedis() *memRedis { return &memRedis{data: map[string]string{}} }

func (m *memRedis) Get(_ context.Context, key string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.data[key]
	if !ok {
		return "", fmt.Errorf("key not found: %s", key)
	}
	return v, nil
}

func (m *memRedis) Set(_ context.Context, key string, value string, _ time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data[key] = value
	return nil
}

func (m *memRedis) SetNX(_ context.Context, key string, value string, _ time.Duration) (bool, error) {
	if m.setNXErr != nil {
		return false, m.setNXErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.data[key]; ok {
		return false, nil
	}
	m.data[key] = value
	return true, nil
}

func (m *memRedis) Delete(_ context.Context, key string) error {
	if m.deleteErr != nil {
		return m.deleteErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.data, key)
	return nil
}

func (m *memRedis) has(key string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.data[key]
	return ok
}

func (m *memRedis) keysWithPrefix(prefix string) []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var keys []string
	for k := range m.data {
		if strings.HasPrefix(k, prefix) {
			keys = append(keys, k)
		}
	}
	return keys
}

func (m *memRedis) SetTaskStatus(context.Context, string, string, time.Duration) error { return nil }
func (m *memRedis) GetTaskStatus(context.Context, string) (string, error)              { return "", nil }
func (m *memRedis) GetTaskResult(context.Context, string, interface{}) error           { return nil }
func (m *memRedis) StoreCallbackURL(context.Context, string, string, time.Duration) error {
	return nil
}
func (m *memRedis) GetCallbackURL(context.Context, string) (string, error) { return "", nil }
func (m *memRedis) SetUserLastActivity(context.Context, string, time.Time, time.Duration) error {
	return nil
}
func (m *memRedis) GetUserLastActivity(context.Context, string) (*time.Time, error) {
	return nil, nil
}
func (m *memRedis) GetUserLastActivityTTL(context.Context, string) (time.Duration, error) {
	return 0, nil
}
func (m *memRedis) Ping(context.Context) error { return nil }

// govBrFlow wires the handler to a fake OIDC provider, a fake inbound webhook
// (auto-resume target) and in-memory Redis instances.
type govBrFlow struct {
	t           *testing.T
	router      *gin.Engine
	mainRedis   *memRedis
	govbrRedis  *memRedis
	resumeHits  *int32
	tokenCalls  *int32
	handler     *GovBrCallbackHandler
	closeServer func()
}

func newGovBrFlow(t *testing.T, initiateToken string) *govBrFlow {
	t.Helper()
	gin.SetMode(gin.TestMode)

	var tokenCalls int32
	oidc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/token":
			atomic.AddInt32(&tokenCalls, 1)
			if err := r.ParseForm(); err != nil || r.PostForm.Get("code_verifier") == "" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			_, _ = io.WriteString(w, `{"access_token":"at-synthetic","refresh_token":"rt-synthetic","expires_in":300,"refresh_expires_in":1800,"token_type":"Bearer"}`)
		case "/userinfo":
			_, _ = io.WriteString(w, `{"sub":"sub-synthetic","name":"Cidadao Sintetico","email":"sintetico@example.com"}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))

	var resumeHits int32
	resume := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&resumeHits, 1)
		w.WriteHeader(http.StatusOK)
	}))

	logger := logrus.New()
	logger.SetOutput(io.Discard)
	cfg := &config.Config{}
	cfg.GovBr.OIDCBaseURL = oidc.URL
	cfg.GovBr.ClientID = "client-synthetic"
	cfg.GovBr.RedirectURI = "https://gateway.example/auth/govbr/callback"
	cfg.GovBr.AuthStateTTL = 300
	cfg.GovBr.AutoResumeEnabled = true
	cfg.GovBr.ResumeWebhookURL = resume.URL
	cfg.GovBr.InitiateAuthToken = initiateToken

	mainRedis := newMemRedis()
	govbrRedis := newMemRedis()
	h := NewGovBrCallbackHandler(logger, cfg, mainRedis, govbrRedis)

	router := gin.New()
	router.LoadHTMLGlob("../../templates/*.html")
	router.POST("/api/v1/auth/govbr/initiate", h.HandleInitiate)
	router.GET("/auth/govbr/callback", h.HandleCallback)
	router.POST("/auth/govbr/confirm", h.HandleConfirm)

	f := &govBrFlow{
		t:          t,
		router:     router,
		mainRedis:  mainRedis,
		govbrRedis: govbrRedis,
		resumeHits: &resumeHits,
		tokenCalls: &tokenCalls,
		handler:    h,
		closeServer: func() {
			oidc.Close()
			resume.Close()
		},
	}
	t.Cleanup(f.closeServer)
	return f
}

// initiate calls /initiate as the in-cluster caller does: the mesh token in
// Authorization and the dedicated token, when given, in X-Govbr-Initiate-Token.
func (f *govBrFlow) initiate(initiateToken string, userNumber string) *httptest.ResponseRecorder {
	body, _ := json.Marshal(map[string]string{"user_number": userNumber, "service_context": testServiceContext})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/govbr/initiate", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer mesh-token-synthetic")
	if initiateToken != "" {
		req.Header.Set(govBrInitiateTokenHeader, initiateToken)
	}
	rec := httptest.NewRecorder()
	f.router.ServeHTTP(rec, req)
	return rec
}

// startAuth runs a successful /initiate and returns the OAuth state.
func (f *govBrFlow) startAuth() string {
	f.t.Helper()
	rec := f.initiate(testInitiateToken, testVictimNumber)
	if rec.Code != http.StatusOK {
		f.t.Fatalf("initiate: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp govBrInitiateResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil || resp.State == "" {
		f.t.Fatalf("initiate: bad response %q", rec.Body.String())
	}
	return resp.State
}

func (f *govBrFlow) callback(state string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/auth/govbr/callback?code=code-synthetic&state="+url.QueryEscape(state), nil)
	rec := httptest.NewRecorder()
	f.router.ServeHTTP(rec, req)
	return rec
}

func (f *govBrFlow) confirm(confirmToken, decision string) *httptest.ResponseRecorder {
	form := url.Values{}
	form.Set("confirm_token", confirmToken)
	form.Set("decision", decision)
	req := httptest.NewRequest(http.MethodPost, "/auth/govbr/confirm", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	f.router.ServeHTTP(rec, req)
	return rec
}

func (f *govBrFlow) authState(state string) GovBrAuthState {
	f.t.Helper()
	raw, err := f.mainRedis.Get(context.Background(), "govbr_auth:"+state)
	if err != nil {
		f.t.Fatalf("auth state missing: %v", err)
	}
	var s GovBrAuthState
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		f.t.Fatalf("auth state invalid: %v", err)
	}
	return s
}

func (f *govBrFlow) tokenStored() bool {
	return f.govbrRedis.has("govbr_token:" + testVictimNumber)
}

func (f *govBrFlow) waitResumeHits(want int32) int32 {
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if got := atomic.LoadInt32(f.resumeHits); got >= want {
			return got
		}
		time.Sleep(10 * time.Millisecond)
	}
	return atomic.LoadInt32(f.resumeHits)
}

var confirmTokenPattern = regexp.MustCompile(`name="confirm_token" value="([A-Za-z0-9_-]+)"`)

func confirmTokenFrom(t *testing.T, page string) string {
	t.Helper()
	m := confirmTokenPattern.FindStringSubmatch(page)
	if m == nil {
		t.Fatalf("confirmation page without confirm_token: %s", page)
	}
	return m[1]
}

// --- /initiate token -------------------------------------------------------

func TestHandleInitiate_RequiresTokenWhenConfigured(t *testing.T) {
	f := newGovBrFlow(t, testInitiateToken)

	for name, token := range map[string]string{
		"missing":       "",
		"wrong":         "not-the-token",
		"token suffix":  testInitiateToken + "x",
		"token prefix":  testInitiateToken[:len(testInitiateToken)-1],
		"bearer scheme": "Bearer " + testInitiateToken,
	} {
		rec := f.initiate(token, testVictimNumber)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s: expected 401, got %d", name, govBrInitiateTokenHeader, rec.Code)
		}
	}

	// The dedicated token in Authorization (where the mesh token goes) is not accepted.
	body, _ := json.Marshal(map[string]string{"user_number": testVictimNumber, "service_context": testServiceContext})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/govbr/initiate", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+testInitiateToken)
	rec := httptest.NewRecorder()
	f.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("token only in Authorization: expected 401, got %d", rec.Code)
	}

	if keys := f.mainRedis.keysWithPrefix("govbr_auth"); len(keys) != 0 {
		t.Fatalf("unauthorized initiate must not touch Redis, found %v", keys)
	}

	state := f.startAuth()
	if got := f.authState(state).State; got != govBrStatePending {
		t.Fatalf("expected pending auth state, got %q", got)
	}
}

func TestHandleInitiate_WithoutConfiguredTokenStillWorks(t *testing.T) {
	f := newGovBrFlow(t, "")

	rec := f.initiate("", testVictimNumber)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 without GOVBR_INITIATE_AUTH_TOKEN, got %d: %s", rec.Code, rec.Body.String())
	}
}

// --- callback + confirmation -----------------------------------------------

func TestHandleCallback_DoesNotBindBeforeConfirmation(t *testing.T) {
	f := newGovBrFlow(t, testInitiateToken)
	state := f.startAuth()

	rec := f.callback(state)
	if rec.Code != http.StatusOK {
		t.Fatalf("callback: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	page := html.UnescapeString(rec.Body.String())

	if !strings.Contains(page, "+5521*****1234") {
		t.Fatalf("confirmation page must show the masked number, got: %s", page)
	}
	if strings.Contains(page, testVictimNumber) {
		t.Fatal("confirmation page must not show the whole phone number")
	}
	if strings.Contains(page, "at-synthetic") || strings.Contains(page, "rt-synthetic") {
		t.Fatal("confirmation page must not carry the OAuth tokens")
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("confirmation page must not be cached, got Cache-Control %q", got)
	}
	confirmToken := confirmTokenFrom(t, page)
	if confirmToken == state {
		t.Fatal("confirmation token must not be the OAuth state the /initiate caller knows")
	}

	if f.tokenStored() {
		t.Fatal("govbr_token must not be written before the citizen confirms the number")
	}
	if got := f.authState(state).State; got != govBrStateAwaitingConfirmation {
		t.Fatalf("expected awaiting_confirmation, got %q", got)
	}
	time.Sleep(50 * time.Millisecond)
	if got := atomic.LoadInt32(f.resumeHits); got != 0 {
		t.Fatalf("auto-resume must not fire before confirmation, got %d", got)
	}

	// The callback link clicked again while awaiting confirmation does not bind.
	again := f.callback(state)
	if again.Code != http.StatusBadRequest || f.tokenStored() {
		t.Fatalf("repeated callback while awaiting confirmation: expected 400 and no token, got %d", again.Code)
	}
}

func TestHandleConfirm_ConfirmBindsOnce(t *testing.T) {
	f := newGovBrFlow(t, testInitiateToken)
	state := f.startAuth()
	confirmToken := confirmTokenFrom(t, f.callback(state).Body.String())

	rec := f.confirm(confirmToken, govBrDecisionConfirm)
	if rec.Code != http.StatusOK {
		t.Fatalf("confirm: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !f.tokenStored() {
		t.Fatal("confirm must write govbr_token:<phone>")
	}
	raw, _ := f.govbrRedis.Get(context.Background(), "govbr_token:"+testVictimNumber)
	var tokenData map[string]interface{}
	if err := json.Unmarshal([]byte(raw), &tokenData); err != nil || tokenData["access_token"] != "at-synthetic" ||
		tokenData["service_context"] != testServiceContext {
		t.Fatalf("unexpected stored token data: %s", raw)
	}
	if got := f.authState(state).State; got != govBrStateCompleted {
		t.Fatalf("expected completed, got %q", got)
	}
	if got := f.waitResumeHits(1); got != 1 {
		t.Fatalf("expected exactly 1 auto-resume after confirmation, got %d", got)
	}
	if keys := f.govbrRedis.keysWithPrefix("govbr_confirm:"); len(keys) != 0 {
		t.Fatalf("pending confirmation must be discarded once consumed, found %v", keys)
	}

	// Replaying the same confirmation does nothing.
	_ = f.govbrRedis.Delete(context.Background(), "govbr_token:"+testVictimNumber)
	replay := f.confirm(confirmToken, govBrDecisionConfirm)
	if replay.Code != http.StatusBadRequest || f.tokenStored() {
		t.Fatalf("replayed confirmation: expected 400 and no token, got %d", replay.Code)
	}
	time.Sleep(50 * time.Millisecond)
	if got := atomic.LoadInt32(f.resumeHits); got != 1 {
		t.Fatalf("replay must not fire auto-resume again, got %d", got)
	}
}

func TestHandleConfirm_RejectDiscardsTokens(t *testing.T) {
	f := newGovBrFlow(t, testInitiateToken)
	state := f.startAuth()
	confirmToken := confirmTokenFrom(t, f.callback(state).Body.String())

	rec := f.confirm(confirmToken, govBrDecisionReject)
	if rec.Code != http.StatusOK {
		t.Fatalf("reject: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(html.UnescapeString(rec.Body.String()), "Vinculação cancelada") {
		t.Fatalf("reject page must say the binding was cancelled, got: %s", rec.Body.String())
	}
	if f.tokenStored() {
		t.Fatal("reject must not write govbr_token")
	}
	if keys := f.govbrRedis.keysWithPrefix("govbr_confirm:"); len(keys) != 0 {
		t.Fatalf("reject must discard the pending tokens, found %v", keys)
	}
	if got := f.authState(state).State; got != govBrStateRejected {
		t.Fatalf("expected rejected, got %q", got)
	}

	// A confirm after the reject cannot bind either.
	if late := f.confirm(confirmToken, govBrDecisionConfirm); late.Code != http.StatusBadRequest || f.tokenStored() {
		t.Fatalf("confirm after reject: expected 400 and no token, got %d", late.Code)
	}
	time.Sleep(50 * time.Millisecond)
	if got := atomic.LoadInt32(f.resumeHits); got != 0 {
		t.Fatalf("reject must not fire auto-resume, got %d", got)
	}
}

func TestHandleConfirm_InitiateCallerCannotConfirm(t *testing.T) {
	// The /initiate caller knows the OAuth state and the auth_url, never the
	// confirmation token rendered to whoever completed the login.
	f := newGovBrFlow(t, testInitiateToken)
	state := f.startAuth()
	_ = f.callback(state)

	guessed := strings.Repeat("A", govBrConfirmTokenLen)
	for name, tc := range map[string][2]string{
		"oauth state as token":  {state, govBrDecisionConfirm},
		"well-formed guess":     {guessed, govBrDecisionConfirm},
		"empty token":           {"", govBrDecisionConfirm},
		"redis key injection":   {"x:" + strings.Repeat("A", govBrConfirmTokenLen-2), govBrDecisionConfirm},
		"unknown decision":      {guessed, "maybe"},
		"missing decision":      {guessed, ""},
		"oversized token input": {strings.Repeat("A", 4096), govBrDecisionConfirm},
	} {
		rec := f.confirm(tc[0], tc[1])
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: expected 400, got %d", name, rec.Code)
		}
	}
	if f.tokenStored() {
		t.Fatal("no forged confirmation may write govbr_token")
	}
	if keys := f.mainRedis.keysWithPrefix("govbr_confirm_used:"); len(keys) != 0 {
		t.Fatalf("forged confirmations must not consume locks, found %v", keys)
	}
}

func TestHandleConfirm_SingleUseEvenWhenDeleteFails(t *testing.T) {
	f := newGovBrFlow(t, testInitiateToken)
	state := f.startAuth()
	confirmToken := confirmTokenFrom(t, f.callback(state).Body.String())
	f.govbrRedis.deleteErr = errStub // pending record survives until its TTL

	if rec := f.confirm(confirmToken, govBrDecisionReject); rec.Code != http.StatusOK {
		t.Fatalf("reject: expected 200, got %d", rec.Code)
	}
	if rec := f.confirm(confirmToken, govBrDecisionConfirm); rec.Code != http.StatusBadRequest {
		t.Fatalf("second answer with the same token: expected 400, got %d", rec.Code)
	}
	if f.tokenStored() {
		t.Fatal("the lock must stop a confirm after a reject even if the pending record survives")
	}
}

func TestHandleConfirm_FailsClosedWhenLockUnavailable(t *testing.T) {
	f := newGovBrFlow(t, testInitiateToken)
	state := f.startAuth()
	confirmToken := confirmTokenFrom(t, f.callback(state).Body.String())
	f.mainRedis.setNXErr = errStub

	rec := f.confirm(confirmToken, govBrDecisionConfirm)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 when the single-use lock fails, got %d", rec.Code)
	}
	if f.tokenStored() {
		t.Fatal("must not bind when single use cannot be guaranteed")
	}
}

// --- helpers ---------------------------------------------------------------

func TestMaskPhoneForConfirmation(t *testing.T) {
	for in, want := range map[string]string{
		"5521999991234":  "+5521*****1234",
		"+5521999991234": "+5521*****1234",
		"552112341234":   "+5521****1234",
		"2134561234":     "+2134**1234",
		"123":            "****",
		"":               "****",
	} {
		if got := maskPhoneForConfirmation(in); got != want {
			t.Fatalf("maskPhoneForConfirmation(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestConfirmationTTL(t *testing.T) {
	h := &GovBrCallbackHandler{config: &config.Config{}}
	for seconds, want := range map[int]time.Duration{
		0:   govBrDefaultConfirmationTTL,
		-5:  govBrDefaultConfirmationTTL,
		10:  govBrMinConfirmationTTL,
		600: 10 * time.Minute,
	} {
		h.config.GovBr.AuthStateTTL = seconds
		if got := h.confirmationTTL(); got != want {
			t.Fatalf("AuthStateTTL=%d: got %v, want %v", seconds, got, want)
		}
	}
}
