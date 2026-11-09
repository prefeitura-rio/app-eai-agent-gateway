package pii

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
)

// Dados sintéticos: nada aqui é de cidadão real.
const (
	citizenText = "meu CPF e 000.000.000-00 e moro na Rua Ficticia 1"
	mediaURL    = "https://cdn.example.invalid/whatsapp/5521999990000/audio-abc.ogg?sig=SEGREDO123"
)

func TestErrorSummary(t *testing.T) {
	googleBody := fmt.Sprintf(`{"error":{"code":400,"message":"Reasoning Engine Execution failed. Error Details: %s %s","status":"FAILED_PRECONDITION"}}`, citizenText, mediaURL)

	tests := []struct {
		name      string
		err       error
		want      string
		forbidden []string
	}{
		{
			name: "non-2xx do Agent Engine com corpo do Google",
			err: fmt.Errorf("failed to get AI response: %w",
				fmt.Errorf("failed to post query: %w",
					fmt.Errorf("non-2xx response: %d - %s", 400, googleBody))),
			want:      "failed to get AI response: failed to post query: non-2xx response: 400 (FAILED_PRECONDITION)",
			forbidden: []string{citizenText, "000.000.000-00", "Rua Ficticia", mediaURL, "SEGREDO123", "Error Details"},
		},
		{
			name:      "poll non-2xx sem JSON",
			err:       fmt.Errorf("failed to poll operation: %w", fmt.Errorf("poll non-2xx response: %d - %s", 503, citizenText)),
			want:      "failed to poll operation: poll non-2xx response: 503",
			forbidden: []string{citizenText},
		},
		{
			name: "corpo invalido (raw)",
			err: fmt.Errorf("failed to post query: %w",
				fmt.Errorf("failed to unmarshal response: %w (raw: %s)", errors.New("invalid character 'm' looking for beginning of value"), citizenText)),
			want:      "failed to post query: failed to unmarshal response: invalid character 'm' looking for beginning of value",
			forbidden: []string{citizenText, "raw:"},
		},
		{
			name:      "operacao terminou com erro",
			err:       fmt.Errorf("operation finished with error: %s", `{"code":9,"message":"`+citizenText+`","status":"FAILED_PRECONDITION"}`),
			want:      "operation finished with error (FAILED_PRECONDITION)",
			forbidden: []string{citizenText},
		},
		{
			name:      "health check com corpo",
			err:       fmt.Errorf("google Agent Engine health check returned status %d: %s", 500, citizenText),
			want:      "google Agent Engine health check returned status 500",
			forbidden: []string{citizenText},
		},
		{
			name:      "erro de transporte com URL da midia",
			err:       fmt.Errorf("failed to download file: %w", fmt.Errorf("Get %q: dial tcp: connection refused", mediaURL)),
			want:      `failed to download file: Get "https://cdn.example.invalid/[omitido]": dial tcp: connection refused`,
			forbidden: []string{mediaURL, "5521999990000", "audio-abc", "SEGREDO123"},
		},
		{
			name:      "URL com credenciais",
			err:       errors.New("dial https://usuario:senha@host.example.invalid:8443/caminho failed"),
			want:      "dial https://host.example.invalid:8443/[omitido] failed",
			forbidden: []string{"usuario", "senha", "caminho"},
		},
		{
			name: "erro sem conteudo remoto fica igual",
			err:  errors.New("thread not found: redis: nil"),
			want: "thread not found: redis: nil",
		},
		{
			name: "nil",
			err:  nil,
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ErrorSummary(tt.err)
			if got != tt.want {
				t.Fatalf("ErrorSummary() = %q, want %q", got, tt.want)
			}
			for _, f := range tt.forbidden {
				if strings.Contains(got, f) {
					t.Fatalf("ErrorSummary() vazou %q: %q", f, got)
				}
			}
		})
	}
}

func TestErrorSummary_TruncatesLongErrors(t *testing.T) {
	long := strings.Repeat("ã", maxErrorSummaryRunes*3)
	got := ErrorSummary(errors.New(long))

	if !strings.HasSuffix(got, truncatedTail) {
		t.Fatalf("resumo longo sem marca de truncamento: %q", got)
	}
	if n := utf8.RuneCountInString(got); n != maxErrorSummaryRunes+utf8.RuneCountInString(truncatedTail) {
		t.Fatalf("resumo com %d runas, esperado %d", n, maxErrorSummaryRunes+utf8.RuneCountInString(truncatedTail))
	}
	if !utf8.ValidString(got) {
		t.Fatalf("truncamento quebrou UTF-8: %q", got)
	}
}

func TestURLHost(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{name: "midia com caminho e query", raw: mediaURL, want: "cdn.example.invalid"},
		{name: "com porta e credenciais", raw: "https://u:p@host.example.invalid:8443/a.ogg", want: "host.example.invalid:8443"},
		{name: "com espacos", raw: "  https://host.example.invalid/a.mp3  ", want: "host.example.invalid"},
		{name: "nao e URL absoluta", raw: "/tmp/audio.ogg", want: "unknown"},
		{name: "vazio", raw: "", want: "unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := URLHost(tt.raw); got != tt.want {
				t.Fatalf("URLHost(%q) = %q, want %q", tt.raw, got, tt.want)
			}
		})
	}
}
