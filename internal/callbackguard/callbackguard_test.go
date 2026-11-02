package callbackguard

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"strings"
	"testing"
)

func TestIsBlockedAddr(t *testing.T) {
	blocked := []string{
		"127.0.0.1", "127.8.9.10", "0.0.0.0", "10.0.0.1", "10.20.3.4", "172.16.0.1",
		"172.31.255.255", "192.168.1.1", "169.254.169.254", "100.64.0.1", "100.127.255.254",
		"224.0.0.1", "255.255.255.255", "240.0.0.1", "198.18.0.1",
		"::", "::1", "::ffff:127.0.0.1", "::ffff:169.254.169.254", "::ffff:10.0.0.1",
		"::127.0.0.1", "fc00::1", "fd00:ec2::254", "fe80::1", "fe80::1%eth0", "ff02::1",
		"64:ff9b::a00:1", "2002:a00:1::1", "2001::1",
	}
	for _, s := range blocked {
		if !IsBlockedAddr(netip.MustParseAddr(s)) {
			t.Errorf("IsBlockedAddr(%s) = false, esperava bloqueado", s)
		}
	}

	public := []string{
		"8.8.8.8", "1.1.1.1", "200.160.2.3", "172.32.0.1", "100.128.0.1", "11.0.0.1",
		"2001:4860:4860::8888", "2800:3f0:4001:80e::200e", "::ffff:8.8.8.8",
	}
	for _, s := range public {
		if IsBlockedAddr(netip.MustParseAddr(s)) {
			t.Errorf("IsBlockedAddr(%s) = true, esperava público", s)
		}
	}

	if !IsBlockedAddr(netip.Addr{}) {
		t.Error("endereço inválido deve ser bloqueado (falha fechada)")
	}
}

func TestParseAllowedDomainsAndHostAllowed(t *testing.T) {
	if got := ParseAllowedDomains(""); len(got) != 0 {
		t.Fatalf("vazio deve liberar tudo, got %v", got)
	}
	if !HostAllowed("qualquer.example", nil) {
		t.Fatal("allow-list vazio deve liberar qualquer host")
	}

	allowed := ParseAllowedDomains(" Mule.Example. , *.salesforce.example,.other.example ")
	want := []string{"mule.example", "salesforce.example", "other.example"}
	if strings.Join(allowed, "|") != strings.Join(want, "|") {
		t.Fatalf("ParseAllowedDomains = %v, want %v", allowed, want)
	}

	cases := map[string]bool{
		"mule.example":              true,
		"MULE.example.":             true,
		"api.mule.example":          true,
		"a.b.salesforce.example":    true,
		"other.example":             true,
		"evilmule.example":          false,
		"mule.example.evil.example": false,
		"example":                   false,
		"":                          false,
		"10.0.0.1":                  false,
	}
	for host, want := range cases {
		if got := HostAllowed(host, allowed); got != want {
			t.Errorf("HostAllowed(%q) = %v, want %v", host, got, want)
		}
	}
}

func TestValidateURL(t *testing.T) {
	allowed := ParseAllowedDomains("mule.example")
	cases := []struct {
		name    string
		url     string
		opts    Options
		wantErr string
	}{
		{"público ok", "https://cb.example.com/notify", Options{}, ""},
		{"http ok sem RequireHTTPS", "http://cb.example.com/notify", Options{}, ""},
		{"http recusado com RequireHTTPS", "http://cb.example.com/notify", Options{RequireHTTPS: true}, "HTTPS"},
		{"esquema", "ftp://cb.example.com/x", Options{}, "HTTP or HTTPS"},
		{"sem host", "https:///x", Options{}, "include a host"},
		{"localhost", "http://localhost:8000/x", Options{}, "localhost"},
		{"localhost maiúsculo e ponto final", "http://LocalHost.:8000/x", Options{}, "localhost"},
		{"subdomínio de localhost", "http://a.localhost/x", Options{}, "localhost"},
		{"loopback literal", "http://127.0.0.2/x", Options{}, "private IP"},
		{"metadata literal", "http://169.254.169.254/computeMetadata/v1/", Options{}, "private IP"},
		{"privado literal", "https://10.20.0.5/x", Options{}, "private IP"},
		{"unspecified", "http://0.0.0.0:8000/x", Options{}, "private IP"},
		{"IPv6 loopback", "http://[::1]:8000/x", Options{}, "private IP"},
		{"IPv4 mapeado em IPv6", "http://[::ffff:169.254.169.254]/x", Options{}, "private IP"},
		{"allow-list aceita domínio", "https://mule.example/deliver", Options{AllowedDomains: allowed}, ""},
		{"allow-list aceita subdomínio", "https://api.mule.example/deliver", Options{AllowedDomains: allowed}, ""},
		{"allow-list recusa outro domínio", "https://attacker.example/collect", Options{AllowedDomains: allowed}, "allowed callback domains"},
		{"allow-list recusa sufixo sem ponto", "https://evilmule.example/collect", Options{AllowedDomains: allowed}, "allowed callback domains"},
		{"allow-list recusa IP público", "https://8.8.8.8/collect", Options{AllowedDomains: allowed}, "allowed callback domains"},
		{"tamanho", "https://cb.example.com/" + strings.Repeat("a", MaxURLLength), Options{}, "maximum length"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ValidateURL(tc.url, tc.opts)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("esperava aceitar, got %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("esperava erro contendo %q, got %v", tc.wantErr, err)
			}
		})
	}
}

func TestValidateURLDoesNotLeakURL(t *testing.T) {
	const phone = "5521999998888"
	urls := []string{
		"https://cb.example.com/notify?phone=" + phone + "\x7f",
		"https://attacker.example/notify?phone=" + phone,
		"http://169.254.169.254/notify?phone=" + phone,
	}
	for _, u := range urls {
		_, err := ValidateURL(u, Options{AllowedDomains: []string{"mule.example"}})
		if err == nil {
			t.Fatalf("esperava recusa para a URL de teste %d", len(u))
		}
		if strings.Contains(err.Error(), phone) || strings.Contains(err.Error(), "example") {
			t.Fatalf("erro ecoa a URL: %q", err.Error())
		}
	}
}

type fakeResolver struct {
	addrs []string
	err   error
	calls int
}

func (f *fakeResolver) LookupNetIP(_ context.Context, _ string, _ string) ([]netip.Addr, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	out := make([]netip.Addr, 0, len(f.addrs))
	for _, a := range f.addrs {
		out = append(out, netip.MustParseAddr(a))
	}
	return out, nil
}

func TestCheckResolvedHost(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name    string
		res     *fakeResolver
		host    string
		blocked bool
	}{
		{"resolve para metadata", &fakeResolver{addrs: []string{"169.254.169.254"}}, "attacker.example", true},
		{"resolve para rede privada", &fakeResolver{addrs: []string{"10.10.0.7"}}, "attacker.example", true},
		{"um dos IPs é privado", &fakeResolver{addrs: []string{"8.8.8.8", "192.168.0.10"}}, "attacker.example", true},
		{"resolve para loopback IPv6", &fakeResolver{addrs: []string{"::1"}}, "attacker.example", true},
		{"público", &fakeResolver{addrs: []string{"200.160.2.3", "2001:4860:4860::8888"}}, "cb.example.com", false},
		{"falha de DNS não recusa", &fakeResolver{err: errors.New("no such host")}, "cb.example.com", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := CheckResolvedHost(ctx, tc.res, tc.host)
			if tc.blocked {
				if !errors.Is(err, ErrBlockedAddress) {
					t.Fatalf("esperava ErrBlockedAddress, got %v", err)
				}
				if strings.Contains(err.Error(), tc.host) {
					t.Fatalf("erro ecoa o host: %q", err.Error())
				}
				return
			}
			if err != nil {
				t.Fatalf("esperava nil, got %v", err)
			}
		})
	}

	literal := &fakeResolver{addrs: []string{"10.0.0.1"}}
	if err := CheckResolvedHost(ctx, literal, "8.8.8.8"); err != nil || literal.calls != 0 {
		t.Fatalf("IP literal não deve ser resolvido: err=%v calls=%d", err, literal.calls)
	}
}

func TestDialControl(t *testing.T) {
	blocked := []string{"127.0.0.1:80", "10.0.0.1:443", "169.254.169.254:80", "[::1]:80", "[fe80::1%eth0]:443", "[::ffff:10.0.0.1]:443", "lixo"}
	for _, addr := range blocked {
		if err := DialControl("tcp", addr, nil); !errors.Is(err, ErrBlockedAddress) {
			t.Errorf("DialControl(%q) = %v, esperava ErrBlockedAddress", addr, err)
		}
	}
	for _, addr := range []string{"8.8.8.8:443", "[2001:4860:4860::8888]:443"} {
		if err := DialControl("tcp", addr, nil); err != nil {
			t.Errorf("DialControl(%q) = %v, esperava nil", addr, err)
		}
	}
}

// TestDialerRefusesResolvedLoopback prova que o guarda age depois da resolução
// de nomes: um hostname (não um IP literal) que resolve para loopback é
// recusado na conexão, que é o que fecha o DNS rebinding.
func TestDialerRefusesResolvedLoopback(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	accepted := make(chan struct{}, 1)
	go func() {
		conn, err := ln.Accept()
		if err == nil {
			accepted <- struct{}{}
			conn.Close()
		}
	}()

	_, port, _ := net.SplitHostPort(ln.Addr().String())
	dialer := &net.Dialer{Control: DialControl}
	conn, err := dialer.DialContext(context.Background(), "tcp", net.JoinHostPort("localhost", port))
	if err == nil {
		conn.Close()
		t.Fatal("esperava a conexão recusada pelo guarda")
	}
	if !errors.Is(err, ErrBlockedAddress) {
		t.Fatalf("esperava ErrBlockedAddress, got %v", err)
	}
	select {
	case <-accepted:
		t.Fatal("o servidor em loopback recebeu a conexão")
	default:
	}
}
