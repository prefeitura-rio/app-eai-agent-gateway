package services

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"golang.org/x/oauth2"

	"github.com/prefeitura-rio/app-eai-agent-gateway/internal/config"
	"github.com/prefeitura-rio/app-eai-agent-gateway/internal/pii"
)

// Dados sintéticos: nada aqui é de cidadão real.
const (
	redactionCitizenText = "meu CPF e 000.000.000-00 e moro na Rua Ficticia 1"
	redactionMediaURL    = "https://cdn.example.invalid/whatsapp/5521999990000/audio-abc.ogg?sig=SEGREDO123"
)

type stubRoundTripper struct {
	status int
	body   string
}

func (s stubRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: s.status,
		Body:       io.NopCloser(strings.NewReader(s.body)),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Request:    req,
	}, nil
}

func newStubAgentEngineService(status int, body string) *GoogleAgentEngineService {
	cfg := &config.Config{}
	cfg.GoogleAgentEngine.ProjectID = "projeto-teste"
	cfg.GoogleAgentEngine.Location = "us-central1"

	logger := logrus.New()
	logger.SetOutput(io.Discard)

	return &GoogleAgentEngineService{
		config:     cfg,
		logger:     logger,
		httpClient: &http.Client{Transport: stubRoundTripper{status: status, body: body}},
	}
}

// TestPostQueryErrorSummary_OmitsResponseBody prova, com o erro real do
// cliente do Agent Engine (DEV-450), que o resumo usado em spans e logs não
// carrega o corpo da resposta, mas o erro original o mantém para a
// classificação de retry e de mensagem ao cidadão.
func TestPostQueryErrorSummary_OmitsResponseBody(t *testing.T) {
	googleBody := fmt.Sprintf(`{"error":{"code":429,"message":"Quota exceeded. Contexto: %s %s","status":"RESOURCE_EXHAUSTED"}}`, redactionCitizenText, redactionMediaURL)

	tests := []struct {
		name        string
		status      int
		body        string
		wantSummary string
	}{
		{
			name:        "non-2xx com corpo do Google",
			status:      http.StatusTooManyRequests,
			body:        googleBody,
			wantSummary: "non-2xx response: 429 (RESOURCE_EXHAUSTED)",
		},
		{
			name:        "2xx com corpo invalido",
			status:      http.StatusOK,
			body:        "resposta em texto: " + redactionCitizenText,
			wantSummary: "failed to unmarshal response: invalid character 'r' looking for beginning of value",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := newStubAgentEngineService(tt.status, tt.body)

			_, err := svc.postQuery(context.Background(), "token-falso", map[string]interface{}{"input": "x"}, "123")
			if err == nil {
				t.Fatalf("postQuery deveria falhar")
			}
			if !strings.Contains(err.Error(), redactionCitizenText) {
				t.Fatalf("o erro original deve manter o corpo para a classificação: %q", err.Error())
			}

			summary := pii.ErrorSummary(err)
			if summary != tt.wantSummary {
				t.Fatalf("ErrorSummary() = %q, want %q", summary, tt.wantSummary)
			}
			for _, f := range []string{redactionCitizenText, "000.000.000-00", redactionMediaURL, "SEGREDO123"} {
				if strings.Contains(summary, f) {
					t.Fatalf("resumo vazou %q: %q", f, summary)
				}
			}
		})
	}

	// A classificação continua lendo o erro original.
	svc := newStubAgentEngineService(http.StatusTooManyRequests, googleBody)
	_, err := svc.postQuery(context.Background(), "token-falso", map[string]interface{}{"input": "x"}, "123")
	if got := classifyEngineError(err.Error()); got != "ratelimited" {
		t.Fatalf("classifyEngineError() = %q, want ratelimited", got)
	}
}

// TestTraceWorkerTask_RecordsRedactedError prova que o evento de exceção do
// span do worker não carrega o corpo da resposta nem a URL da mídia, e que o
// erro devolvido ao chamador continua o original.
func TestTraceWorkerTask_RecordsRedactedError(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })

	meter := metricnoop.NewMeterProvider().Meter("test")
	svc := &OTelService{tracer: provider.Tracer("test")}
	svc.workerTasksTotal, _ = meter.Int64Counter("tasks")
	svc.workerTaskDuration, _ = meter.Float64Histogram("duration")
	svc.workerTasksInFlight, _ = meter.Int64UpDownCounter("in_flight")

	original := fmt.Errorf("failed to get AI response: %w",
		fmt.Errorf("non-2xx response: %d - %s %s", 400, redactionCitizenText, redactionMediaURL))

	err := svc.TraceWorkerTask(context.Background(), "user_message_worker", "process_user_message", func(context.Context) error {
		return original
	})
	if err != original {
		t.Fatalf("TraceWorkerTask deve devolver o erro original, devolveu %v", err)
	}

	spans := recorder.Ended()
	if len(spans) != 1 {
		t.Fatalf("esperado 1 span, veio %d", len(spans))
	}

	var exceptionMessage string
	for _, event := range spans[0].Events() {
		for _, kv := range event.Attributes {
			if kv.Key == "exception.message" {
				exceptionMessage = kv.Value.AsString()
			}
		}
	}
	if exceptionMessage != "failed to get AI response: non-2xx response: 400" {
		t.Fatalf("exception.message = %q", exceptionMessage)
	}

	for _, kv := range spans[0].Attributes() {
		for _, f := range []string{redactionCitizenText, redactionMediaURL} {
			if strings.Contains(kv.Value.Emit(), f) {
				t.Fatalf("atributo %q vazou %q", kv.Key, f)
			}
		}
	}
}

type allowRateLimiter struct{ waitErr error }

func (r allowRateLimiter) Allow(context.Context, string) (bool, error) {
	return r.waitErr == nil, r.waitErr
}
func (r allowRateLimiter) Wait(context.Context, string) error { return r.waitErr }

type threadRedisStub struct{}

func (threadRedisStub) Get(context.Context, string) (string, error)              { return "{}", nil }
func (threadRedisStub) Set(context.Context, string, string, time.Duration) error { return nil }
func (threadRedisStub) SetValue(context.Context, string, interface{}, time.Duration) error {
	return nil
}
func (threadRedisStub) Delete(context.Context, string) error { return nil }

// assertLogsOmit falha se alguma entrada de log (mensagem ou campo) contiver
// um dos textos proibidos.
func assertLogsOmit(t *testing.T, entries []logrus.Entry, forbidden ...string) {
	t.Helper()
	for _, entry := range entries {
		texts := []string{entry.Message}
		for key, value := range entry.Data {
			texts = append(texts, key+"="+fmt.Sprint(value))
		}
		for _, text := range texts {
			for _, f := range forbidden {
				if strings.Contains(text, f) {
					t.Fatalf("log %q vazou %q: %q", entry.Message, f, text)
				}
			}
		}
	}
}

// TestSendMessageLogs_OmitResponseBody prova que o SendMessage, que troca o
// erro do Agent Engine pela mensagem amigável e não o devolve, loga só o
// resumo do erro: status e enum do Google, sem o corpo da resposta (DEV-450).
func TestSendMessageLogs_OmitResponseBody(t *testing.T) {
	googleBody := fmt.Sprintf(`{"error":{"code":400,"message":"Reasoning Engine Execution failed. Contexto: %s %s","status":"FAILED_PRECONDITION"}}`, redactionCitizenText, redactionMediaURL)

	logger, hook := logtest.NewNullLogger()
	logger.SetLevel(logrus.DebugLevel)

	svc := newStubAgentEngineService(http.StatusBadRequest, googleBody)
	svc.logger = logger
	svc.rateLimiter = allowRateLimiter{}
	svc.redisService = threadRedisStub{}
	svc.tokenSource = oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "token-falso"})
	svc.config.GoogleAgentEngine.ReasoningEngineID = "123"
	svc.config.GoogleAgentEngine.ErrorMessageDefault = "mensagem amigavel"

	resp, err := svc.SendMessage(context.Background(), "5521999990000", "ola", nil, nil)
	if err != nil {
		t.Fatalf("SendMessage devolve a mensagem amigavel, nao erro: %v", err)
	}
	if resp == nil || resp.Content != "mensagem amigavel" {
		t.Fatalf("resposta inesperada: %+v", resp)
	}

	var errorFields []string
	for _, entry := range hook.AllEntries() {
		if v, ok := entry.Data[logrus.ErrorKey]; ok {
			errorFields = append(errorFields, fmt.Sprint(v))
		}
	}
	if len(errorFields) != 2 {
		t.Fatalf("esperados 2 logs com o campo error, vieram %d: %v", len(errorFields), errorFields)
	}
	want := "failed to post query: non-2xx response: 400 (FAILED_PRECONDITION)"
	for _, got := range errorFields {
		if got != want {
			t.Fatalf("campo error = %q, want %q", got, want)
		}
	}

	entries := make([]logrus.Entry, 0, len(hook.AllEntries()))
	for _, e := range hook.AllEntries() {
		entries = append(entries, *e)
	}
	assertLogsOmit(t, entries, redactionCitizenText, "000.000.000-00", redactionMediaURL, "SEGREDO123")
}

// TestTranscribeFromURLLogs_OmitAudioURL prova que o serviço de transcrição
// loga só o host da URL do áudio, nunca o caminho ou a query (DEV-450).
func TestTranscribeFromURLLogs_OmitAudioURL(t *testing.T) {
	logger, hook := logtest.NewNullLogger()
	logger.SetLevel(logrus.DebugLevel)

	svc := &TranscribeService{
		config:      &config.Config{},
		logger:      logger,
		rateLimiter: allowRateLimiter{waitErr: errors.New("limite")},
	}

	if _, err := svc.TranscribeFromURL(context.Background(), redactionMediaURL); err == nil {
		t.Fatalf("TranscribeFromURL deveria falhar no rate limit")
	}

	entries := make([]logrus.Entry, 0, len(hook.AllEntries()))
	for _, e := range hook.AllEntries() {
		entries = append(entries, *e)
	}
	if len(entries) == 0 {
		t.Fatalf("esperado ao menos o log de inicio da transcricao")
	}
	if got := entries[0].Data["audio_host"]; got != "cdn.example.invalid" {
		t.Fatalf("audio_host = %v, esperado cdn.example.invalid", got)
	}
	assertLogsOmit(t, entries, redactionMediaURL, "5521999990000", "audio-abc", "SEGREDO123")
}
