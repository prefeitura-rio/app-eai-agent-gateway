package services

import (
	"errors"
	"net/url"
	"strings"
	"testing"
)

// TestSanitizeURLErrorStripsRawURL trava o fix LGPD do caminho realista: quando
// httpClient.Do / http.NewRequestWithContext falham (timeout, DNS, conn recusada
// — a falha mais comum de callback), o *url.Error devolvido ecoa a URL inteira
// no .Error(), e essa URL carrega o telefone do cidadão no path/query. Esse erro
// flui pra logs, span OTel e corpo do Data Relay. sanitizeURLError tem de remover
// a URL preservando o motivo.
func TestSanitizeURLErrorStripsRawURL(t *testing.T) {
	const phone = "5521999998888"
	const host = "cb.example.com"
	raw := "https://" + host + "/notify?phone=" + phone

	ue := &url.Error{
		Op:  "Post",
		URL: raw,
		Err: errors.New("dial tcp: connection refused"),
	}
	// Pré-condição: o *url.Error cru de fato vaza o telefone (senão o teste é vácuo).
	if !strings.Contains(ue.Error(), phone) {
		t.Fatalf("pré-condição falhou: *url.Error deveria conter o telefone: %q", ue.Error())
	}

	got := sanitizeURLError(ue)
	if strings.Contains(got.Error(), phone) {
		t.Fatalf("sanitizeURLError ainda vaza o telefone: %q", got.Error())
	}
	if strings.Contains(got.Error(), host) {
		t.Fatalf("sanitizeURLError ainda vaza o host: %q", got.Error())
	}
	// O motivo é preservado (observabilidade do tipo de falha não regride).
	if !strings.Contains(got.Error(), "connection refused") {
		t.Fatalf("sanitizeURLError perdeu o motivo do erro: %q", got.Error())
	}

	// Erro que NÃO é *url.Error passa inalterado.
	plain := errors.New("some other error")
	if sanitizeURLError(plain) != plain {
		t.Fatal("erro não-url.Error deveria passar inalterado")
	}
}
