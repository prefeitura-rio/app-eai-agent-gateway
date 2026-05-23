package handlers

import (
	"strings"
	"testing"
)

// TestValidateCallbackURL_RejectsLoopbackVariants verifies that string-form
// loopback (`localhost`) and IP-form loopback (incluindo IPv4-mapped IPv6
// `::ffff:127.0.0.1` e short-form `::ffff:7f00:1`) são bloqueados. SEM o fix
// do isPrivateIP/IsLoopback, atacante poderia bypass com IPv4-mapped IPv6
// encoding pra atingir loopback via Go HTTP transport (que resolve atrás).
func TestValidateCallbackURL_RejectsLoopbackVariants(t *testing.T) {
	loopbackURLs := []string{
		"http://localhost/path",
		"http://LOCALHOST/path",            // case-insensitive DNS
		"http://127.0.0.1/path",            // IPv4 form
		"http://[::1]/path",                // IPv6 form
		"http://[::ffff:127.0.0.1]/path",   // IPv4-mapped IPv6 (long form)
		"http://[::ffff:7f00:1]/path",      // IPv4-mapped IPv6 (compact hex form)
		"http://[::ffff:127.0.0.1]:80/path",
	}
	for _, u := range loopbackURLs {
		t.Run(u, func(t *testing.T) {
			err := validateCallbackURL(u)
			if err == nil {
				t.Fatalf("expected validation error for %q, got nil", u)
			}
			// Mensagem mais robusta — não checa string específica pra resistir a
			// refactor; só checa que callback foi bloqueado.
			if !strings.Contains(err.Error(), "loopback") && !strings.Contains(err.Error(), "localhost") {
				t.Errorf("expected loopback/localhost rejection, got %v", err)
			}
		})
	}
}

func TestValidateCallbackURL_RejectsPrivateIPVariants(t *testing.T) {
	// IPv4 puro + IPv4-mapped IPv6 devem ambos bater no CIDR match — sem o
	// `ip.To4()` antes do match, o CIDR `10.0.0.0/8` não contém o IPv6.
	privateURLs := []string{
		"http://10.0.0.1/x",
		"http://[::ffff:10.0.0.1]/x",
		"http://172.16.0.1/x",
		"http://[::ffff:172.16.0.1]/x",
		"http://192.168.1.1/x",
		"http://[::ffff:192.168.1.1]/x",
		"http://169.254.169.254/x", // AWS metadata (link-local)
		"http://[::ffff:169.254.169.254]/x",
	}
	for _, u := range privateURLs {
		t.Run(u, func(t *testing.T) {
			err := validateCallbackURL(u)
			if err == nil {
				t.Fatalf("expected private IP rejection for %q, got nil", u)
			}
		})
	}
}

func TestValidateCallbackURL_AcceptsPublic(t *testing.T) {
	publicURLs := []string{
		"https://api.example.com/webhook",
		"https://gateway.staging.prefeitura.rio/api",
		"http://[2001:db8::1]/x", // Public IPv6 (documentation range; not in private list)
	}
	for _, u := range publicURLs {
		t.Run(u, func(t *testing.T) {
			if err := validateCallbackURL(u); err != nil {
				t.Errorf("expected valid URL %q, got error %v", u, err)
			}
		})
	}
}

func TestValidateCallbackURL_RejectsBadScheme(t *testing.T) {
	badURLs := []string{
		"file:///etc/passwd",
		"ftp://example.com/x",
		"gopher://example.com/x",
		"ws://example.com/x",
	}
	for _, u := range badURLs {
		t.Run(u, func(t *testing.T) {
			if err := validateCallbackURL(u); err == nil {
				t.Errorf("expected scheme rejection for %q, got nil", u)
			}
		})
	}
}

func TestValidateCallbackURL_RejectsOversized(t *testing.T) {
	// Limite explícito em 2048; payload acima deve falhar com mensagem clara.
	longURL := "https://example.com/" + strings.Repeat("a", 3000)
	err := validateCallbackURL(longURL)
	if err == nil {
		t.Fatal("expected oversized URL rejection, got nil")
	}
	if !strings.Contains(err.Error(), "maximum length") {
		t.Errorf("expected length rejection message, got %v", err)
	}
}

func TestValidateCallbackURL_RejectsZoneIndexedAndLinkLocalIPv6(t *testing.T) {
	// IPv6 zone index (URL-encoded %25 vira % no Hostname). net.ParseIP
	// retorna nil pra string com zone — SEM o strip, atacante bypassa o
	// check e Go http.Client resolve via zone lo0. Strip do %... antes do
	// parse + ip.IsLinkLocalUnicast() pra fe80::/10 cobre o gap.
	rejectedURLs := []string{
		"http://[::1%25lo0]/x",            // zone-indexed loopback
		"http://[fe80::1%25eth0]/x",        // zone-indexed link-local
		"http://[fe80::1]/x",               // raw link-local (no zone)
		"http://169.254.0.1/x",             // IPv4 link-local (covered by private)
	}
	for _, u := range rejectedURLs {
		t.Run(u, func(t *testing.T) {
			err := validateCallbackURL(u)
			if err == nil {
				t.Errorf("expected zone/link-local rejection for %q, got nil", u)
			}
		})
	}
}
