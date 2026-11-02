package services

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/sirupsen/logrus"

	"github.com/prefeitura-rio/app-eai-agent-gateway/internal/callbackguard"
	"github.com/prefeitura-rio/app-eai-agent-gateway/internal/config"
	"github.com/prefeitura-rio/app-eai-agent-gateway/internal/models"
)

func newTestCallbackService(allowedDomain string) *CallbackService {
	logger := logrus.New()
	logger.SetOutput(io.Discard)
	cfg := &config.Config{}
	cfg.Callback.Timeout = 5
	cfg.Callback.MaxRetries = 0
	cfg.Callback.AllowedDomain = allowedDomain
	return NewCallbackService(logger, cfg, nil, nil)
}

// TestCallbackClientRefusesNonPublicAtDial: o cliente HTTP do callback recusa
// a conexão a loopback mesmo quando a URL traz um hostname (localhost resolve
// para 127.0.0.1/::1), porque o IP é conferido no dial. Antes da correção a
// requisição chegava ao servidor.
func TestCallbackClientRefusesNonPublicAtDial(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	_, port, err := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	if err != nil {
		t.Fatalf("porta do servidor de teste: %v", err)
	}

	s := newTestCallbackService("")
	logger := logrus.NewEntry(s.logger)
	for _, target := range []string{srv.URL + "/cb", "http://localhost:" + port + "/cb"} {
		err := s.sendCallbackRequest(context.Background(), target, []byte(`{}`), logger)
		if err == nil {
			t.Fatalf("esperava a conexão recusada para o destino de teste em loopback")
		}
		if !errors.Is(err, callbackguard.ErrBlockedAddress) {
			t.Fatalf("esperava ErrBlockedAddress, got %v", err)
		}
		if isRetriableError(err) {
			t.Fatal("destino bloqueado não deve ser retentado")
		}
	}
	if n := hits.Load(); n != 0 {
		t.Fatalf("o servidor interno recebeu %d requisições", n)
	}
}

func TestExecuteCallbackEnforcesAllowedDomainAndIPLiteral(t *testing.T) {
	s := newTestCallbackService("mule.example")
	payload := models.CallbackPayload{MessageID: "msg-1", Status: "completed"}

	for _, target := range []string{
		"https://attacker.example/collect",
		"https://evilmule.example/collect",
		"http://169.254.169.254/computeMetadata/v1/",
		"http://10.20.0.5/x",
	} {
		err := s.ExecuteCallback(context.Background(), target, payload, "5521000000000")
		if err == nil {
			t.Fatalf("esperava recusa antes do envio (caso %d)", len(target))
		}
		if !strings.Contains(err.Error(), "callback URL rejected") {
			t.Fatalf("esperava recusa na validação, got %v", err)
		}
	}
}

func TestValidateCallbackURLAppliesConfig(t *testing.T) {
	s := newTestCallbackService("mule.example")
	s.config.Callback.RequireHTTPS = true

	if err := s.ValidateCallbackURL("https://api.mule.example/deliver"); err != nil {
		t.Fatalf("esperava aceitar subdomínio liberado, got %v", err)
	}
	if err := s.ValidateCallbackURL("https://attacker.example/collect"); err == nil {
		t.Fatal("esperava recusar domínio fora de CALLBACK_ALLOWED_DOMAIN")
	}
	if err := s.ValidateCallbackURL("http://mule.example/deliver"); err == nil {
		t.Fatal("esperava recusar http com CALLBACK_REQUIRE_HTTPS")
	}
	if err := s.ValidateCallbackURL("https://192.168.0.10/x"); err == nil {
		t.Fatal("esperava recusar IP privado literal")
	}
}

func TestCheckRedirectRevalidatesTarget(t *testing.T) {
	s := newTestCallbackService("mule.example")
	req := func(raw string) *http.Request {
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatalf("url de teste: %v", err)
		}
		return &http.Request{URL: u}
	}
	via := []*http.Request{req("https://mule.example/deliver")}

	if err := s.checkRedirect(req("https://api.mule.example/other"), via); err != nil {
		t.Fatalf("redirect para subdomínio liberado deveria seguir, got %v", err)
	}
	for _, target := range []string{"https://attacker.example/collect", "http://169.254.169.254/", "http://localhost/x"} {
		if err := s.checkRedirect(req(target), via); err == nil {
			t.Fatalf("redirect para destino proibido deveria ser recusado")
		}
	}

	many := make([]*http.Request, maxCallbackRedirects)
	if err := s.checkRedirect(req("https://mule.example/x"), many); err == nil {
		t.Fatal("esperava parar depois do limite de redirects")
	}
}
