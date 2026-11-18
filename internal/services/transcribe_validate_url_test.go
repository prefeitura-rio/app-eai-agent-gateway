package services

import (
	"io"
	"testing"

	"github.com/sirupsen/logrus"

	"github.com/prefeitura-rio/app-eai-agent-gateway/internal/config"
)

func newTestTranscribeService(allowedURLs string) *TranscribeService {
	logger := logrus.New()
	logger.SetOutput(io.Discard)
	cfg := &config.Config{}
	cfg.Transcribe.AllowedURLs = allowedURLs
	return &TranscribeService{config: cfg, logger: logger}
}

// TestValidateURLChecksParsedHost trava o DEV-275: o allowlist de transcrição
// era um strings.Contains na URL inteira, então qualquer URL que trouxesse o
// domínio permitido no path, na query, no fragment, no userinfo ou como
// prefixo de outro host passava, e o worker fazia GET em host arbitrário.
func TestValidateURLChecksParsedHost(t *testing.T) {
	s := newTestTranscribeService("https://whatsapp.dados.rio/")

	rejected := []string{
		"https://attacker.example/x.mp3?x=whatsapp.dados.rio",
		"https://attacker.example/x.mp3?u=https://whatsapp.dados.rio/",
		"https://attacker.example/https://whatsapp.dados.rio/x.mp3",
		"https://attacker.example/x.mp3#https://whatsapp.dados.rio/",
		"https://whatsapp.dados.rio.attacker.example/x.mp3",
		"https://https://whatsapp.dados.rio/@attacker.example/x.mp3",
		"https://whatsapp.dados.rio@attacker.example/x.mp3",
		"https://user:pass@whatsapp.dados.rio/x.mp3",
		"https://evilwhatsapp.dados.rio/x.mp3",
		"https://whatsapp.dados.rio:8443/x.mp3",
		"http://whatsapp.dados.rio/x.mp3",
		"https://169.254.169.254/latest/meta-data/x.mp3?h=https://whatsapp.dados.rio/",
		"https://localhost/x.mp3?h=https://whatsapp.dados.rio/",
		"ftp://whatsapp.dados.rio/x.mp3",
		"https:///x.mp3?h=https://whatsapp.dados.rio/",
		"not a url https://whatsapp.dados.rio/",
	}
	for _, raw := range rejected {
		if err := s.validateURL(raw); err == nil {
			t.Errorf("validateURL(%q) aceitou uma URL cujo host não está no allowlist", raw)
		}
	}

	accepted := []string{
		"https://whatsapp.dados.rio/audio/abc.ogg",
		"https://WhatsApp.Dados.Rio/audio/abc.ogg",
		"https://whatsapp.dados.rio./audio/abc.ogg",
		"https://whatsapp.dados.rio:443/audio/abc.ogg",
		"https://media.whatsapp.dados.rio/audio/abc.ogg",
		"https://whatsapp.dados.rio/audio/abc.ogg?token=1#t",
	}
	for _, raw := range accepted {
		if err := s.validateURL(raw); err != nil {
			t.Errorf("validateURL(%q) recusou uma URL permitida: %v", raw, err)
		}
	}
}

// TestValidateURLAllowlistEntryFormats cobre os formatos de entrada que
// TRANSCRIBE_ALLOWED_URLS já usa (.env.example, docker-compose.yml): URL com
// esquema, com porta, e domínio sem esquema.
func TestValidateURLAllowlistEntryFormats(t *testing.T) {
	cases := []struct {
		allowed string
		raw     string
		ok      bool
	}{
		// Domínio puro: libera o domínio e subdomínios, em http ou https.
		{"dados.rio", "https://whatsapp.dados.rio/a.mp3", true},
		{"dados.rio", "http://dados.rio/a.mp3", true},
		{" dados.rio ", "https://dados.rio/a.mp3", true},
		{"*.dados.rio", "https://whatsapp.dados.rio/a.mp3", true},
		{"dados.rio", "https://dados.rio.attacker.example/a.mp3", false},
		{"dados.rio", "https://attackerdados.rio/a.mp3", false},
		{"dados.rio", "https://dados.rio:8443/a.mp3", false},
		// Entrada com porta exige a mesma porta.
		{"https://whatsapp.dados.rio/,http://host.docker.internal:8001", "http://host.docker.internal:8001/a.mp3", true},
		{"https://whatsapp.dados.rio/,http://host.docker.internal:8001", "http://host.docker.internal:9000/a.mp3", false},
		{"https://whatsapp.dados.rio/,http://host.docker.internal:8001", "http://host.docker.internal/a.mp3", false},
		{"https://whatsapp.dados.rio/,http://host.docker.internal:8001", "https://whatsapp.dados.rio/a.mp3", true},
		// Entradas vazias ou inválidas não liberam nada.
		{"", "https://whatsapp.dados.rio/a.mp3", false},
		{" , ,", "https://whatsapp.dados.rio/a.mp3", false},
		{"https://", "https://whatsapp.dados.rio/a.mp3", false},
	}
	for _, tc := range cases {
		s := newTestTranscribeService(tc.allowed)
		err := s.validateURL(tc.raw)
		if tc.ok && err != nil {
			t.Errorf("allowlist %q: validateURL(%q) recusou: %v", tc.allowed, tc.raw, err)
		}
		if !tc.ok && err == nil {
			t.Errorf("allowlist %q: validateURL(%q) aceitou", tc.allowed, tc.raw)
		}
	}
}
