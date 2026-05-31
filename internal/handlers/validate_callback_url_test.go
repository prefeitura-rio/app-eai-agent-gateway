package handlers

import (
	"strings"
	"testing"
)

// TestValidateCallbackURLDoesNotLeakRawURL trava o gap LGPD do #42: quando a URL
// é malformada e falha no url.Parse, o erro retornado NÃO pode ecoar a URL crua
// (que carrega o telefone do cidadão), porque esse erro vai tanto para o log
// quanto para o corpo da resposta HTTP.
func TestValidateCallbackURLDoesNotLeakRawURL(t *testing.T) {
	const phone = "5521999998888"
	const host = "cb.example.com"
	// Um caractere de controle (DEL, 0x7f) faz o url.Parse falhar; o erro do Go
	// ecoaria a string de entrada inteira se fosse embrulhado com %w.
	malformed := "https://" + host + "/notify?phone=" + phone + "\x7f"

	err := validateCallbackURL(malformed)
	if err == nil {
		t.Fatal("esperava que validateCallbackURL rejeitasse a URL malformada")
	}
	if strings.Contains(err.Error(), phone) {
		t.Fatalf("erro vaza o telefone da URL crua: %q", err.Error())
	}
	if strings.Contains(err.Error(), host) {
		t.Fatalf("erro vaza o host da URL crua: %q", err.Error())
	}
}
