package services

import (
	"context"
	stdio "io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sirupsen/logrus"

	"github.com/prefeitura-rio/app-eai-agent-gateway/internal/config"
)

func newSilentLogger() *logrus.Logger {
	l := logrus.New()
	l.SetOutput(stdio.Discard)
	return l
}

// fakeMetaServer permite injetar URL local pro client. Como o client
// constrói URL via cfg.PhoneNumberID + graph.facebook.com, precisamos
// substituir o transport pra interceptar.
func newClientWithStubURL(stubURL string, cfg *config.MetaConfig) *MetaGraphService {
	return &MetaGraphService{
		cfg:        cfg,
		httpClient: &http.Client{Transport: &stubTransport{stubURL: stubURL}},
		logger:     newSilentLogger(),
	}
}

// stubTransport redireciona qualquer request pro stubURL preservando method
// e body. Permite o Service "achar" que está falando com Meta.
type stubTransport struct{ stubURL string }

func (s *stubTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	newReq := req.Clone(req.Context())
	parsed, err := http.NewRequest(req.Method, s.stubURL, req.Body)
	if err != nil {
		return nil, err
	}
	parsed.Header = newReq.Header
	return http.DefaultTransport.RoundTrip(parsed)
}

func baseCfg() *config.MetaConfig {
	return &config.MetaConfig{
		Enabled:         true,
		AppSecret:       "secret",
		VerifyToken:     "verify",
		SystemUserToken: "tok",
		PhoneNumberID:   "ph123",
		GraphAPIVersion: "v21.0",
	}
}

func TestSendText_DisabledFlag(t *testing.T) {
	cfg := baseCfg()
	cfg.Enabled = false
	svc := NewMetaGraphService(cfg, newSilentLogger())
	_, err := svc.SendText(context.Background(), "55", "hi")
	if err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Errorf("expected disabled error, got %v", err)
	}
}

func TestSendText_MissingCredentials(t *testing.T) {
	cfg := baseCfg()
	cfg.SystemUserToken = ""
	svc := NewMetaGraphService(cfg, newSilentLogger())
	_, err := svc.SendText(context.Background(), "55", "hi")
	if err == nil || !strings.Contains(err.Error(), "credentials missing") {
		t.Errorf("expected credentials missing, got %v", err)
	}
}

func TestSendText_EmptyInputs(t *testing.T) {
	cfg := baseCfg()
	svc := NewMetaGraphService(cfg, newSilentLogger())
	_, err := svc.SendText(context.Background(), "", "x")
	if err == nil {
		t.Error("expected error empty recipient")
	}
	_, err = svc.SendText(context.Background(), "55", "")
	if err == nil {
		t.Error("expected error empty body")
	}
}

func TestSendText_HappyPath(t *testing.T) {
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.Header.Get("Authorization") != "Bearer tok" {
			t.Errorf("expected bearer tok, got %q", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"messaging_product": "whatsapp",
			"contacts": [{"input": "55", "wa_id": "55"}],
			"messages": [{"id": "wamid.ABC"}]
		}`))
	}))
	defer stub.Close()

	svc := newClientWithStubURL(stub.URL, baseCfg())
	wamid, err := svc.SendText(context.Background(), "55", "Olá")
	if err != nil {
		t.Fatalf("expected ok, got %v", err)
	}
	if wamid != "wamid.ABC" {
		t.Errorf("expected wamid.ABC, got %q", wamid)
	}
}

func TestSendText_MetaErrorStatus(t *testing.T) {
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error": {"message": "blocked", "code": 200}}`))
	}))
	defer stub.Close()

	svc := newClientWithStubURL(stub.URL, baseCfg())
	_, err := svc.SendText(context.Background(), "55", "x")
	if err == nil || !strings.Contains(err.Error(), "403") {
		t.Errorf("expected 403 error, got %v", err)
	}
}

func TestSendText_MissingWAMID(t *testing.T) {
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"messages": []}`))
	}))
	defer stub.Close()

	svc := newClientWithStubURL(stub.URL, baseCfg())
	_, err := svc.SendText(context.Background(), "55", "x")
	if err == nil || !strings.Contains(err.Error(), "no wamid") {
		t.Errorf("expected no wamid error, got %v", err)
	}
}

func TestRecipientPrefix(t *testing.T) {
	if got := recipientPrefix("5521965850470"); got != "55219658…" {
		t.Errorf("expected mask, got %q", got)
	}
	if got := recipientPrefix("123"); got != "123" {
		t.Errorf("short phone returned as-is, got %q", got)
	}
}
