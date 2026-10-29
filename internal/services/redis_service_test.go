package services

import (
	"strings"
	"testing"
)

func TestRedactCredentialURLRemovesPassword(t *testing.T) {
	const senha = "s3nh4-secreta"
	for _, raw := range []string{
		"redis://:" + senha + "@redis-master:6379/0",
		"redis://default:" + senha + "@mcp-redis.mcp.svc.cluster.local:6379/0",
		"rediss://:" + senha + "@redis-tls:6380/1",
		"amqp://gateway:" + senha + "@rabbitmq:5672/",
	} {
		got := redactCredentialURL(raw)
		if strings.Contains(got, senha) {
			t.Errorf("redactCredentialURL(%q) = %q ainda contém a senha", raw, got)
		}
		if !strings.Contains(got, ":63") && !strings.Contains(got, ":5672") {
			t.Errorf("redactCredentialURL(%q) = %q perdeu host e porta", raw, got)
		}
	}
	if got := redactCredentialURL("::nao-e-url::" + senha); strings.Contains(got, senha) {
		t.Errorf("URL inválida vazou a senha: %q", got)
	}
}
