package services

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func allowAllDial(string, string, syscall.RawConn) error { return nil }

func newDownloadTestService(t *testing.T, allowedURLs string) *TranscribeService {
	t.Helper()
	s := newTestTranscribeService(allowedURLs)
	s.config.Transcribe.DownloadTimeout = 5 * time.Second
	s.config.Transcribe.MaxFileSizeMB = 1
	s.config.Transcribe.TempDir = t.TempDir()
	return s
}

// TestDownloadFileBlocksPrivateDestination: mesmo que uma URL chegue ao
// download (DNS rebinding, IP em formato não reconhecido), o dialer recusa a
// conexão a endereço não público (aqui, o loopback do httptest).
func TestDownloadFileBlocksPrivateDestination(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte("secret"))
	}))
	defer srv.Close()

	s := newDownloadTestService(t, srv.URL)
	_, err := s.downloadFile(context.Background(), srv.URL+"/a.mp3")
	if err == nil {
		t.Fatal("downloadFile conectou num endereço de loopback")
	}
	if !errors.Is(err, errTranscribeBlockedAddress) {
		t.Fatalf("erro inesperado (esperava errTranscribeBlockedAddress): %v", err)
	}
	if hits.Load() != 0 {
		t.Fatalf("o servidor interno recebeu %d requisições", hits.Load())
	}
}

// TestDownloadFileRejectsRedirectOutsideAllowlist: um host permitido não pode
// redirecionar o download para fora do allowlist.
func TestDownloadFileRejectsRedirectOutsideAllowlist(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://attacker.example/a.mp3?h=whatsapp.dados.rio", http.StatusFound)
	}))
	defer srv.Close()

	s := newDownloadTestService(t, "https://whatsapp.dados.rio/")
	s.dialControl = allowAllDial
	_, err := s.downloadFile(context.Background(), srv.URL+"/a.mp3")
	if err == nil || !strings.Contains(err.Error(), "redirect target rejected") {
		t.Fatalf("downloadFile seguiu redirect para host fora do allowlist: %v", err)
	}
}

// TestDownloadFileAllowedDestination: com o destino liberado pelo dialer, o
// download continua funcionando.
func TestDownloadFileAllowedDestination(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "audio/mpeg")
		_, _ = w.Write([]byte("audio-bytes"))
	}))
	defer srv.Close()

	s := newDownloadTestService(t, srv.URL)
	s.dialControl = allowAllDial
	path, err := s.downloadFile(context.Background(), srv.URL+"/a.mp3")
	if err != nil {
		t.Fatalf("downloadFile falhou num destino liberado: %v", err)
	}
	defer func() { _ = os.Remove(path) }()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "audio-bytes" {
		t.Fatalf("conteúdo baixado inesperado: %q", data)
	}
}

// TestDownloadFileReusesConnections: o cliente do download é único por
// serviço; downloads seguidos reaproveitam a conexão em vez de deixar um
// transporte ocioso aberto por download.
func TestDownloadFileReusesConnections(t *testing.T) {
	var newConns atomic.Int32
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "audio/mpeg")
		_, _ = w.Write([]byte("audio-bytes"))
	}))
	srv.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			newConns.Add(1)
		}
	}
	srv.Start()
	defer srv.Close()

	s := newDownloadTestService(t, srv.URL)
	s.dialControl = allowAllDial
	const downloads = 5
	for i := 0; i < downloads; i++ {
		path, err := s.downloadFile(context.Background(), srv.URL+"/a.mp3")
		if err != nil {
			t.Fatalf("download %d falhou: %v", i, err)
		}
		_ = os.Remove(path)
	}
	if got := newConns.Load(); got != 1 {
		t.Fatalf("%d downloads abriram %d conexões; esperava 1 reaproveitada", downloads, got)
	}
	if s.sharedDownloadClient() != s.sharedDownloadClient() {
		t.Fatal("sharedDownloadClient deveria devolver sempre o mesmo cliente")
	}
}

// TestDownloadClientCheckRedirect: redirect para host permitido segue; para
// host fora do allowlist, IP interno ou além do limite, é recusado.
func TestDownloadClientCheckRedirect(t *testing.T) {
	s := newDownloadTestService(t, "https://whatsapp.dados.rio/")
	client := s.newDownloadClient()
	if client.Transport.(*http.Transport).Proxy != nil {
		t.Fatal("o download não pode usar proxy de ambiente")
	}
	mustReq := func(raw string) *http.Request {
		req, err := http.NewRequest(http.MethodGet, raw, nil)
		if err != nil {
			t.Fatal(err)
		}
		return req
	}
	via := []*http.Request{mustReq("https://whatsapp.dados.rio/a.mp3")}

	if err := client.CheckRedirect(mustReq("https://media.whatsapp.dados.rio/b.mp3"), via); err != nil {
		t.Errorf("redirect permitido recusado: %v", err)
	}
	for _, raw := range []string{
		"https://attacker.example/b.mp3?h=whatsapp.dados.rio",
		"http://169.254.169.254/latest/meta-data/",
		"https://whatsapp.dados.rio.attacker.example/b.mp3",
	} {
		if err := client.CheckRedirect(mustReq(raw), via); err == nil {
			t.Errorf("redirect para %q deveria ser recusado", raw)
		}
	}
	long := make([]*http.Request, maxTranscribeDownloadRedirects)
	for i := range long {
		long[i] = via[0]
	}
	if err := client.CheckRedirect(mustReq("https://whatsapp.dados.rio/c.mp3"), long); err == nil {
		t.Error("redirect além do limite deveria ser recusado")
	}
}

func TestIsBlockedTranscribeAddr(t *testing.T) {
	blocked := []string{
		"127.0.0.1", "10.1.2.3", "172.16.0.1", "192.168.1.1", "169.254.169.254",
		"100.100.1.1", "0.0.0.0", "::1", "::", "fe80::1", "fd00::1", "::ffff:10.0.0.1",
		"::ffff:169.254.169.254", "64:ff9b::a00:1",
	}
	for _, raw := range blocked {
		if !isBlockedTranscribeAddr(netip.MustParseAddr(raw)) {
			t.Errorf("isBlockedTranscribeAddr(%s) = false, esperava bloqueado", raw)
		}
	}
	public := []string{"8.8.8.8", "142.250.0.1", "2001:4860:4860::8888"}
	for _, raw := range public {
		if isBlockedTranscribeAddr(netip.MustParseAddr(raw)) {
			t.Errorf("isBlockedTranscribeAddr(%s) = true, esperava público", raw)
		}
	}
	if !isBlockedTranscribeAddr(netip.Addr{}) {
		t.Error("endereço inválido deveria ser bloqueado (falha fechada)")
	}
}

func TestTranscribeDialControl(t *testing.T) {
	if err := transcribeDialControl("tcp", "169.254.169.254:80", nil); !errors.Is(err, errTranscribeBlockedAddress) {
		t.Errorf("metadata server deveria ser recusado: %v", err)
	}
	if err := transcribeDialControl("tcp", "[::1]:443", nil); !errors.Is(err, errTranscribeBlockedAddress) {
		t.Errorf("loopback IPv6 deveria ser recusado: %v", err)
	}
	if err := transcribeDialControl("tcp", "garbage", nil); !errors.Is(err, errTranscribeBlockedAddress) {
		t.Errorf("endereço ilegível deveria ser recusado: %v", err)
	}
	if err := transcribeDialControl("tcp", "8.8.8.8:443", nil); err != nil {
		t.Errorf("endereço público deveria passar: %v", err)
	}
}
