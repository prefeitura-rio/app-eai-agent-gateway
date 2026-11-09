package workers

import (
	"fmt"
	"strings"
	"testing"

	"go.opentelemetry.io/otel/attribute"

	"github.com/prefeitura-rio/app-eai-agent-gateway/internal/pii"
)

// Dados sintéticos: nada aqui é de cidadão real.
const (
	redactionCitizenText = "meu CPF e 000.000.000-00 e moro na Rua Ficticia 1"
	redactionAudioURL    = "https://cdn.example.invalid/whatsapp/5521999990000/audio-abc.ogg?sig=SEGREDO123.ogg"
)

// TestAudioTranscriptionSpanAttributes_NoAudioURL garante que o span
// audio_transcription não exporta a URL da mídia do cidadão (DEV-450): só o
// host e o formato.
func TestAudioTranscriptionSpanAttributes_NoAudioURL(t *testing.T) {
	attrs := audioTranscriptionSpanAttributes(redactionAudioURL)

	got := map[attribute.Key]attribute.Value{}
	for _, kv := range attrs {
		got[kv.Key] = kv.Value
	}

	if _, ok := got["audio.url"]; ok {
		t.Fatalf("span nao pode ter o atributo audio.url")
	}
	for key, value := range got {
		for _, f := range []string{redactionAudioURL, "5521999990000", "audio-abc", "SEGREDO123", "/whatsapp/"} {
			if strings.Contains(value.Emit(), f) {
				t.Fatalf("atributo %q expoe a URL da midia (%q)", key, f)
			}
		}
	}

	if v := got["audio.host"]; v.AsString() != "cdn.example.invalid" {
		t.Fatalf("audio.host = %q, esperado cdn.example.invalid", v.Emit())
	}
	if v := got["audio.format"]; v.AsString() != "ogg" {
		t.Fatalf("audio.format = %q, esperado ogg", v.Emit())
	}
	if v, ok := got["audio.detected"]; !ok || !v.AsBool() {
		t.Fatalf("audio.detected = %v, esperado true", v.Emit())
	}
}

// TestErrorSummaryKeepsRetryClassification prova que os atributos *.error e os
// logs do worker (pii.ErrorSummary) não carregam o corpo do Agent Engine, e que
// a decisão de retry, que lê o erro original, não muda.
func TestErrorSummaryKeepsRetryClassification(t *testing.T) {
	body := fmt.Sprintf(`{"error":{"code":400,"message":"Reasoning Engine Execution failed. Error Details: connection was closed. %s","status":"FAILED_PRECONDITION"}}`, redactionCitizenText)
	err := fmt.Errorf("failed to get AI response: %w",
		fmt.Errorf("failed to post query: %w",
			fmt.Errorf("non-2xx response: %d - %s", 400, body)))

	if !isRetriableError(err) {
		t.Fatalf("o erro original deve continuar retriable")
	}

	summary := pii.ErrorSummary(err)
	want := "failed to get AI response: failed to post query: non-2xx response: 400 (FAILED_PRECONDITION)"
	if summary != want {
		t.Fatalf("ErrorSummary() = %q, want %q", summary, want)
	}
	for _, f := range []string{redactionCitizenText, "000.000.000-00", "Error Details"} {
		if strings.Contains(summary, f) {
			t.Fatalf("resumo vazou %q: %q", f, summary)
		}
	}

	transcriptionErr := fmt.Errorf("failed to download file: %w",
		fmt.Errorf("Get %q: dial tcp: connection refused", redactionAudioURL))
	transcriptionSummary := pii.ErrorSummary(transcriptionErr)
	for _, f := range []string{redactionAudioURL, "5521999990000", "SEGREDO123"} {
		if strings.Contains(transcriptionSummary, f) {
			t.Fatalf("resumo do erro de transcricao vazou %q: %q", f, transcriptionSummary)
		}
	}
	if got := classifyTranscriptionError(transcriptionErr); got != "network_error" {
		t.Fatalf("classifyTranscriptionError() = %q, want network_error", got)
	}
}
