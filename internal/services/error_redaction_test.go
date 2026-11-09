package services

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/sirupsen/logrus"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

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
