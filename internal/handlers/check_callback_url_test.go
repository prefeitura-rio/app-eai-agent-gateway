package handlers

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"

	"github.com/prefeitura-rio/app-eai-agent-gateway/internal/callbackguard"
	"github.com/prefeitura-rio/app-eai-agent-gateway/internal/config"
)

type staticResolver struct {
	addrs []string
	err   error
}

func (r staticResolver) LookupNetIP(_ context.Context, _ string, _ string) ([]netip.Addr, error) {
	if r.err != nil {
		return nil, r.err
	}
	out := make([]netip.Addr, 0, len(r.addrs))
	for _, a := range r.addrs {
		out = append(out, netip.MustParseAddr(a))
	}
	return out, nil
}

func withResolver(t *testing.T, r callbackguard.Resolver) {
	t.Helper()
	prev := callbackResolver
	callbackResolver = r
	t.Cleanup(func() { callbackResolver = prev })
}

func newCallbackTestHandler(allowedDomain string) *MessageHandler {
	logger := logrus.New()
	logger.SetOutput(io.Discard)
	cfg := &config.Config{}
	cfg.Callback.AllowedDomain = allowedDomain
	return &MessageHandler{logger: logger, config: cfg}
}

func TestCheckCallbackURLResolvesHostname(t *testing.T) {
	ctx := context.Background()
	h := newCallbackTestHandler("")

	for _, addr := range []string{"169.254.169.254", "10.10.0.7", "192.168.1.1", "127.0.0.1", "::1"} {
		withResolver(t, staticResolver{addrs: []string{addr}})
		err := h.checkCallbackURL(ctx, "https://attacker.example/collect")
		if !errors.Is(err, callbackguard.ErrBlockedAddress) {
			t.Fatalf("hostname resolvendo para %s: esperava ErrBlockedAddress, got %v", addr, err)
		}
	}

	withResolver(t, staticResolver{addrs: []string{"200.160.2.3"}})
	if err := h.checkCallbackURL(ctx, "https://cb.example.com/notify"); err != nil {
		t.Fatalf("hostname público deveria passar, got %v", err)
	}

	withResolver(t, staticResolver{err: errors.New("no such host")})
	if err := h.checkCallbackURL(ctx, "https://cb.example.com/notify"); err != nil {
		t.Fatalf("falha de DNS não deve recusar (o dial do worker confere), got %v", err)
	}
}

func TestCheckCallbackURLAppliesAllowedDomain(t *testing.T) {
	ctx := context.Background()
	withResolver(t, staticResolver{addrs: []string{"200.160.2.3"}})

	h := newCallbackTestHandler("mule.example")
	if err := h.checkCallbackURL(ctx, "https://mule.example/deliver"); err != nil {
		t.Fatalf("domínio liberado deveria passar, got %v", err)
	}
	if err := h.checkCallbackURL(ctx, "https://api.mule.example/deliver"); err != nil {
		t.Fatalf("subdomínio liberado deveria passar, got %v", err)
	}
	if err := h.checkCallbackURL(ctx, "https://attacker.example/collect"); err == nil {
		t.Fatal("domínio fora de CALLBACK_ALLOWED_DOMAIN deveria ser recusado")
	}

	open := newCallbackTestHandler("")
	if err := open.checkCallbackURL(ctx, "https://attacker.example/collect"); err != nil {
		t.Fatalf("sem CALLBACK_ALLOWED_DOMAIN qualquer domínio público passa, got %v", err)
	}
}

// TestHandleUserWebhookRejectsCallbackResolvingToInternal exercita a rota: um
// callback_url com hostname que resolve para o metadata server é recusado com
// 400, sem ecoar a URL (telefone do cidadão) na resposta.
func TestHandleUserWebhookRejectsCallbackResolvingToInternal(t *testing.T) {
	gin.SetMode(gin.TestMode)
	withResolver(t, staticResolver{addrs: []string{"169.254.169.254"}})

	h := newCallbackTestHandler("")
	router := gin.New()
	router.POST("/api/v1/message/webhook/user", h.HandleUserWebhook)

	const phone = "5521999998888"
	body := `{"user_number":"` + phone + `","message":"oi","callback_url":"https://rebind.attacker.example/collect?phone=` + phone + `"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/message/webhook/user", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, esperava 400; corpo: %s", rec.Code, rec.Body.String())
	}
	resp := rec.Body.String()
	if !strings.Contains(resp, "Invalid callback URL") {
		t.Fatalf("esperava erro de callback inválido, got %s", resp)
	}
	if strings.Contains(resp, phone) || strings.Contains(resp, "attacker") {
		t.Fatalf("resposta ecoa a URL: %s", resp)
	}
}
