// Package callbackguard protege o envio de callbacks (callback_url informada
// pelo chamador do webhook) contra SSRF: recusa destinos que não sejam
// endereços públicos de internet e aplica o allow-list de domínios
// (CALLBACK_ALLOWED_DOMAIN).
//
// A validação da URL (ValidateURL e CheckResolvedHost) dá a resposta cedo ao
// chamador, mas não basta sozinha: entre a validação e a conexão o DNS pode
// mudar (DNS rebinding). A proteção que vale é DialControl, usada no
// net.Dialer do cliente HTTP do callback, que confere o IP já resolvido no
// momento exato da conexão.
//
// Nenhuma mensagem de erro deste pacote ecoa a URL: ela carrega o telefone do
// cidadão (LGPD) e os erros vão para log e para o corpo da resposta HTTP.
package callbackguard

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"strings"
	"syscall"
)

// MaxURLLength é o tamanho máximo aceito para uma callback_url.
const MaxURLLength = 2048

// ErrBlockedAddress indica que o destino do callback é (ou resolve para) um
// endereço que não é público: loopback, rede privada, link-local (inclusive o
// serviço de metadados da nuvem), CGNAT, multicast ou faixa reservada.
var ErrBlockedAddress = errors.New("callback destination is not a public address")

// blockedPrefixes lista as faixas que um callback nunca pode alcançar.
// Endereços IPv4 mapeados em IPv6 (::ffff:a.b.c.d) são desembrulhados antes da
// comparação, então valem as faixas IPv4.
var blockedPrefixes = mustParsePrefixes(
	// IPv4
	"0.0.0.0/8",       // "esta rede" (0.0.0.0 conecta no próprio host)
	"10.0.0.0/8",      // privada (RFC 1918): VPC, pods e services do GKE
	"100.64.0.0/10",   // CGNAT / Tailscale
	"127.0.0.0/8",     // loopback
	"169.254.0.0/16",  // link-local, inclui o metadata server 169.254.169.254
	"172.16.0.0/12",   // privada (RFC 1918)
	"192.0.0.0/24",    // atribuições de protocolo da IETF
	"192.0.2.0/24",    // documentação (TEST-NET-1)
	"192.88.99.0/24",  // relay 6to4 (anycast, depreciado)
	"192.168.0.0/16",  // privada (RFC 1918)
	"198.18.0.0/15",   // benchmarking
	"198.51.100.0/24", // documentação (TEST-NET-2)
	"203.0.113.0/24",  // documentação (TEST-NET-3)
	"224.0.0.0/4",     // multicast
	"240.0.0.0/4",     // reservada, inclui o broadcast 255.255.255.255
	// IPv6
	"::/96",          // unspecified, loopback e IPv4-compatível (depreciado)
	"64:ff9b::/96",   // NAT64: embute um IPv4 arbitrário
	"64:ff9b:1::/48", // NAT64 local
	"100::/64",       // discard
	"2001::/23",      // atribuições de protocolo da IETF (inclui Teredo)
	"2001:db8::/32",  // documentação
	"2002::/16",      // 6to4: embute um IPv4 arbitrário
	"fc00::/7",       // unique local (privada)
	"fe80::/10",      // link-local
	"ff00::/8",       // multicast
)

func mustParsePrefixes(cidrs ...string) []netip.Prefix {
	prefixes := make([]netip.Prefix, 0, len(cidrs))
	for _, cidr := range cidrs {
		prefixes = append(prefixes, netip.MustParsePrefix(cidr))
	}
	return prefixes
}

// IsBlockedAddr diz se o endereço não pode ser destino de um callback.
// Endereço inválido é tratado como bloqueado (falha fechada).
func IsBlockedAddr(addr netip.Addr) bool {
	if !addr.IsValid() {
		return true
	}
	addr = addr.Unmap().WithZone("")
	for _, prefix := range blockedPrefixes {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}

// ParseAllowedDomains interpreta CALLBACK_ALLOWED_DOMAIN: um domínio ou uma
// lista separada por vírgulas. Cada entrada libera o próprio domínio e os
// subdomínios dele; "*.exemplo.com" e ".exemplo.com" equivalem a "exemplo.com".
// Valor vazio devolve lista vazia, que significa "qualquer domínio".
func ParseAllowedDomains(raw string) []string {
	var domains []string
	for _, entry := range strings.Split(raw, ",") {
		domain := normalizeHost(entry)
		domain = strings.TrimPrefix(domain, "*.")
		domain = strings.TrimPrefix(domain, ".")
		if domain != "" {
			domains = append(domains, domain)
		}
	}
	return domains
}

// HostAllowed diz se o host atende ao allow-list. Lista vazia libera todos.
func HostAllowed(host string, allowedDomains []string) bool {
	if len(allowedDomains) == 0 {
		return true
	}
	host = normalizeHost(host)
	if host == "" {
		return false
	}
	for _, domain := range allowedDomains {
		if host == domain || strings.HasSuffix(host, "."+domain) {
			return true
		}
	}
	return false
}

func normalizeHost(host string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
}

// Options ajusta a validação estática da URL.
type Options struct {
	// AllowedDomains é o allow-list já interpretado (ParseAllowedDomains);
	// vazio libera qualquer domínio.
	AllowedDomains []string
	// RequireHTTPS recusa o esquema http.
	RequireHTTPS bool
}

// ValidateURL faz as checagens que não dependem de rede: tamanho, formato,
// esquema, host presente, localhost, IP literal não público e allow-list de
// domínios. Devolve a URL interpretada quando ela é aceita.
func ValidateURL(rawURL string, opts Options) (*url.URL, error) {
	if len(rawURL) > MaxURLLength {
		return nil, fmt.Errorf("callback URL exceeds maximum length of %d characters", MaxURLLength)
	}

	parsedURL, err := url.Parse(rawURL)
	if err != nil {
		// Não embrulhar o erro do url.Parse com %w: o .Error() dele ecoa a URL
		// crua (que carrega o telefone do cidadão — LGPD). Motivo sanitizado.
		return nil, errors.New("invalid callback URL format")
	}

	if opts.RequireHTTPS && parsedURL.Scheme != "https" {
		return nil, errors.New("callback URL must use HTTPS protocol")
	}
	if parsedURL.Scheme != "http" && parsedURL.Scheme != "https" {
		return nil, errors.New("callback URL must use HTTP or HTTPS protocol")
	}

	host := normalizeHost(parsedURL.Hostname())
	if host == "" {
		return nil, errors.New("callback URL must include a host")
	}
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return nil, errors.New("callback URL cannot use localhost")
	}

	if addr, err := netip.ParseAddr(host); err == nil {
		if IsBlockedAddr(addr) {
			return nil, errors.New("callback URL cannot use private IP addresses")
		}
	}

	if !HostAllowed(host, opts.AllowedDomains) {
		return nil, errors.New("callback URL host is not in the allowed callback domains")
	}

	return parsedURL, nil
}

// Resolver é o pedaço de *net.Resolver que CheckResolvedHost usa.
type Resolver interface {
	LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)
}

// CheckResolvedHost resolve o host e devolve um erro que embrulha
// ErrBlockedAddress se algum dos endereços resolvidos não for público.
//
// É uma checagem antecipada, de melhor esforço: host que é IP literal não é
// consultado (ValidateURL já o checou) e falha de DNS devolve nil, porque a
// proteção definitiva é DialControl, no momento da conexão.
func CheckResolvedHost(ctx context.Context, resolver Resolver, host string) error {
	host = normalizeHost(host)
	if host == "" {
		return nil
	}
	if _, err := netip.ParseAddr(host); err == nil {
		return nil
	}
	addrs, err := resolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return nil
	}
	for _, addr := range addrs {
		if IsBlockedAddr(addr) {
			return fmt.Errorf("callback URL host resolves to a private or reserved address: %w", ErrBlockedAddress)
		}
	}
	return nil
}

// DialControl é a função Control de um net.Dialer: roda depois da resolução de
// nomes e antes de cada conexão, com o IP que de fato será conectado, e recusa
// o que não for endereço público. Por conferir o IP final, fecha o DNS
// rebinding e também cobre redirects e hosts em formatos que o url.Parse não
// reconhece como IP (decimal, octal, hexadecimal).
func DialControl(_ string, address string, _ syscall.RawConn) error {
	addrPort, err := netip.ParseAddrPort(address)
	if err != nil {
		return fmt.Errorf("unparseable dial address: %w", ErrBlockedAddress)
	}
	if IsBlockedAddr(addrPort.Addr()) {
		return ErrBlockedAddress
	}
	return nil
}
