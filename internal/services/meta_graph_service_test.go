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

// ─── Media outbound ────────────────────────────────────────────────────────

func TestSendMedia_HappyPath(t *testing.T) {
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := stdio.ReadAll(r.Body)
		s := string(body)
		if !strings.Contains(s, `"type":"image"`) {
			t.Errorf("expected type=image in body, got %s", s)
		}
		if !strings.Contains(s, `"id":"media-xyz"`) {
			t.Errorf("expected media id in body, got %s", s)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"messages":[{"id":"wamid.IMG"}]}`))
	}))
	defer stub.Close()

	svc := newClientWithStubURL(stub.URL, baseCfg())
	wamid, err := svc.SendMedia(context.Background(), "5521", "image", MediaInput{ID: "media-xyz", Caption: "foto"})
	if err != nil {
		t.Fatalf("expected ok, got %v", err)
	}
	if wamid != "wamid.IMG" {
		t.Errorf("expected wamid.IMG, got %q", wamid)
	}
}

func TestSendMedia_InvalidType(t *testing.T) {
	svc := NewMetaGraphService(baseCfg(), newSilentLogger())
	_, err := svc.SendMedia(context.Background(), "5521", "garbage", MediaInput{ID: "x"})
	if err == nil || !strings.Contains(err.Error(), "invalid mediaType") {
		t.Errorf("expected invalid mediaType, got %v", err)
	}
}

func TestSendMedia_RequiresIDOrLink(t *testing.T) {
	svc := NewMetaGraphService(baseCfg(), newSilentLogger())
	_, err := svc.SendMedia(context.Background(), "5521", "image", MediaInput{})
	if err == nil || !strings.Contains(err.Error(), "id or link") {
		t.Errorf("expected id-or-link error, got %v", err)
	}
}

func TestSendLocation_HappyPath(t *testing.T) {
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := stdio.ReadAll(r.Body)
		s := string(body)
		if !strings.Contains(s, `"type":"location"`) {
			t.Errorf("expected location type, got %s", s)
		}
		if !strings.Contains(s, `"latitude":-22.9`) {
			t.Errorf("expected latitude in body, got %s", s)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"messages":[{"id":"wamid.LOC"}]}`))
	}))
	defer stub.Close()

	svc := newClientWithStubURL(stub.URL, baseCfg())
	wamid, err := svc.SendLocation(context.Background(), "5521", -22.9, -43.2, "Praça", "Centro")
	if err != nil || wamid != "wamid.LOC" {
		t.Errorf("expected wamid.LOC ok, got wamid=%q err=%v", wamid, err)
	}
}

func TestSendTemplate_HappyPath(t *testing.T) {
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := stdio.ReadAll(r.Body)
		s := string(body)
		if !strings.Contains(s, `"name":"hello_world"`) {
			t.Errorf("expected template name in body, got %s", s)
		}
		if !strings.Contains(s, `"code":"pt_BR"`) {
			t.Errorf("expected language code in body, got %s", s)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"messages":[{"id":"wamid.TPL"}]}`))
	}))
	defer stub.Close()

	svc := newClientWithStubURL(stub.URL, baseCfg())
	wamid, err := svc.SendTemplate(context.Background(), "5521", "hello_world", "pt_BR", nil)
	if err != nil || wamid != "wamid.TPL" {
		t.Errorf("expected wamid.TPL, got wamid=%q err=%v", wamid, err)
	}
}

func TestSendInteractive_RequiresAction(t *testing.T) {
	svc := NewMetaGraphService(baseCfg(), newSilentLogger())
	_, err := svc.SendInteractive(context.Background(), "5521", "button", nil, nil, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "action") {
		t.Errorf("expected action required error, got %v", err)
	}
}

func TestSendInteractive_HappyPath(t *testing.T) {
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := stdio.ReadAll(r.Body)
		s := string(body)
		if !strings.Contains(s, `"type":"interactive"`) {
			t.Errorf("expected interactive type, got %s", s)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"messages":[{"id":"wamid.INT"}]}`))
	}))
	defer stub.Close()

	svc := newClientWithStubURL(stub.URL, baseCfg())
	action := map[string]interface{}{
		"buttons": []map[string]interface{}{
			{"type": "reply", "reply": map[string]string{"id": "yes", "title": "Sim"}},
		},
	}
	wamid, err := svc.SendInteractive(context.Background(), "5521", "button",
		nil, map[string]interface{}{"text": "Confirma?"}, nil, action)
	if err != nil || wamid != "wamid.INT" {
		t.Errorf("expected wamid.INT, got wamid=%q err=%v", wamid, err)
	}
}

func TestSendReaction_HappyPath(t *testing.T) {
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := stdio.ReadAll(r.Body)
		s := string(body)
		if !strings.Contains(s, `"emoji":"❤️"`) {
			t.Errorf("expected emoji in body, got %s", s)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"messages":[{"id":"wamid.REACT"}]}`))
	}))
	defer stub.Close()

	svc := newClientWithStubURL(stub.URL, baseCfg())
	wamid, err := svc.SendReaction(context.Background(), "5521", "wamid.PREV", "❤️")
	if err != nil || wamid != "wamid.REACT" {
		t.Errorf("expected wamid.REACT, got wamid=%q err=%v", wamid, err)
	}
}

// ─── Media download ────────────────────────────────────────────────────────

func TestLookupMedia_HappyPath(t *testing.T) {
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			t.Errorf("expected bearer tok, got %q", r.Header.Get("Authorization"))
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"url": "https://meta-cdn.example/signed",
			"mime_type": "image/jpeg",
			"sha256": "abc",
			"file_size": 1234,
			"id": "media-id-1",
			"messaging_product": "whatsapp"
		}`))
	}))
	defer stub.Close()

	svc := newClientWithStubURL(stub.URL, baseCfg())
	meta, err := svc.LookupMedia(context.Background(), "media-id-1")
	if err != nil {
		t.Fatalf("expected ok, got %v", err)
	}
	if meta.URL != "https://meta-cdn.example/signed" {
		t.Errorf("expected signed url, got %q", meta.URL)
	}
	if meta.FileSize != 1234 {
		t.Errorf("expected file_size=1234, got %d", meta.FileSize)
	}
}

func TestLookupMedia_NonOKStatus(t *testing.T) {
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"media not found"}`))
	}))
	defer stub.Close()

	svc := newClientWithStubURL(stub.URL, baseCfg())
	_, err := svc.LookupMedia(context.Background(), "bad-id")
	if err == nil || !strings.Contains(err.Error(), "404") {
		t.Errorf("expected 404 error, got %v", err)
	}
}

func TestDownloadMedia_HappyPath(t *testing.T) {
	want := []byte("fake-bytes-content")
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(want)
	}))
	defer stub.Close()

	svc := newClientWithStubURL(stub.URL, baseCfg())
	bs, err := svc.DownloadMedia(context.Background(), stub.URL, 1<<20) // 1MB cap
	if err != nil {
		t.Fatalf("expected ok, got %v", err)
	}
	if string(bs) != string(want) {
		t.Errorf("bytes mismatch: got %s want %s", string(bs), string(want))
	}
}

func TestDownloadMedia_ExceedsMax(t *testing.T) {
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(make([]byte, 100))
	}))
	defer stub.Close()

	svc := newClientWithStubURL(stub.URL, baseCfg())
	_, err := svc.DownloadMedia(context.Background(), stub.URL, 10) // cap menor que body
	if err == nil || !strings.Contains(err.Error(), "exceeds max bytes") {
		t.Errorf("expected exceeds max bytes error, got %v", err)
	}
}
