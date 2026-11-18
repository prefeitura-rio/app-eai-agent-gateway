package services

import (
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"strings"
	"syscall"
)

// Proteção contra SSRF do download de áudio da transcrição (DEV-275).
//
// A URL do áudio chega no campo "message" do webhook de usuário, controlado
// por quem chama o gateway. validateURL confere o host parseado contra
// TRANSCRIBE_ALLOWED_URLS e recusa IP literal que não seja público; a
// proteção definitiva é transcribeDialControl, no net.Dialer do download,
// que confere o IP já resolvido no momento da conexão (fecha DNS rebinding e
// formatos de IP que o url.Parse não reconhece). Redirects são revalidados
// pelo mesmo allowlist.

// errTranscribeBlockedAddress indica que o destino do download é (ou resolve
// para) um endereço que não é público.
var errTranscribeBlockedAddress = errors.New("audio URL destination is not a public address")

// transcribeBlockedPrefixes lista as faixas que o download de áudio nunca pode
// alcançar. Endereços IPv4 mapeados em IPv6 são desembrulhados antes.
var transcribeBlockedPrefixes = mustParseTranscribePrefixes(
	// IPv4
	"0.0.0.0/8",       // "esta rede"
	"10.0.0.0/8",      // privada (RFC 1918): VPC, pods e services do GKE
	"100.64.0.0/10",   // CGNAT / Tailscale
	"127.0.0.0/8",     // loopback
	"169.254.0.0/16",  // link-local, inclui o metadata server 169.254.169.254
	"172.16.0.0/12",   // privada (RFC 1918)
	"192.0.0.0/24",    // atribuições de protocolo da IETF
	"192.0.2.0/24",    // documentação (TEST-NET-1)
	"192.88.99.0/24",  // relay 6to4
	"192.168.0.0/16",  // privada (RFC 1918)
	"198.18.0.0/15",   // benchmarking
	"198.51.100.0/24", // documentação (TEST-NET-2)
	"203.0.113.0/24",  // documentação (TEST-NET-3)
	"224.0.0.0/4",     // multicast
	"240.0.0.0/4",     // reservada, inclui broadcast
	// IPv6
	"::/96",          // unspecified, loopback e IPv4-compatível
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

func mustParseTranscribePrefixes(cidrs ...string) []netip.Prefix {
	prefixes := make([]netip.Prefix, 0, len(cidrs))
	for _, cidr := range cidrs {
		prefixes = append(prefixes, netip.MustParsePrefix(cidr))
	}
	return prefixes
}

// isBlockedTranscribeAddr diz se o endereço não pode ser destino do download.
// Endereço inválido é tratado como bloqueado (falha fechada).
func isBlockedTranscribeAddr(addr netip.Addr) bool {
	if !addr.IsValid() {
		return true
	}
	addr = addr.Unmap().WithZone("")
	for _, prefix := range transcribeBlockedPrefixes {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}

// transcribeDialControl é a função Control do net.Dialer do download: roda
// depois da resolução de nomes e antes de cada conexão, com o IP que de fato
// será conectado, e recusa o que não for endereço público.
func transcribeDialControl(_ string, address string, _ syscall.RawConn) error {
	addrPort, err := netip.ParseAddrPort(address)
	if err != nil {
		return fmt.Errorf("unparseable dial address: %w", errTranscribeBlockedAddress)
	}
	if isBlockedTranscribeAddr(addrPort.Addr()) {
		return errTranscribeBlockedAddress
	}
	return nil
}

// transcribeAllowedOrigin é uma entrada de TRANSCRIBE_ALLOWED_URLS já
// interpretada. host libera o próprio host e os subdomínios dele; scheme e
// port, quando a entrada os traz, precisam ser iguais.
type transcribeAllowedOrigin struct {
	scheme string
	host   string
	port   string
}

// parseTranscribeAllowedOrigins interpreta as entradas de
// TRANSCRIBE_ALLOWED_URLS: URL com esquema ("https://whatsapp.dados.rio/",
// "http://host.docker.internal:8001") ou domínio puro ("dados.rio",
// "*.dados.rio"). O path da entrada é ignorado. Entradas vazias ou sem host
// são descartadas.
func parseTranscribeAllowedOrigins(entries []string) []transcribeAllowedOrigin {
	origins := make([]transcribeAllowedOrigin, 0, len(entries))
	for _, entry := range entries {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		raw := entry
		if !strings.Contains(raw, "://") {
			raw = "//" + raw
		}
		parsed, err := url.Parse(raw)
		if err != nil {
			continue
		}
		host := normalizeTranscribeHost(parsed.Hostname())
		host = strings.TrimPrefix(host, "*.")
		host = strings.TrimPrefix(host, ".")
		if host == "" {
			continue
		}
		origins = append(origins, transcribeAllowedOrigin{
			scheme: strings.ToLower(parsed.Scheme),
			host:   host,
			port:   parsed.Port(),
		})
	}
	return origins
}

func (o transcribeAllowedOrigin) allows(scheme, host, port string) bool {
	if host != o.host && !strings.HasSuffix(host, "."+o.host) {
		return false
	}
	if o.scheme != "" && scheme != o.scheme {
		return false
	}
	wantPort := o.port
	if wantPort == "" {
		wantPort = defaultTranscribePort(scheme)
	}
	gotPort := port
	if gotPort == "" {
		gotPort = defaultTranscribePort(scheme)
	}
	return gotPort == wantPort
}

func defaultTranscribePort(scheme string) string {
	switch scheme {
	case "https":
		return "443"
	case "http":
		return "80"
	default:
		return ""
	}
}

func normalizeTranscribeHost(host string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
}
