// Package services — Adaptive OTel sampler (plano-bot-2026 Fase 0 I10).
//
// Estratégia:
//
//	"always_on" → sdktrace.AlwaysSample() (legacy backward-compat).
//	"adaptive"  → ParentBased(TraceIDRatioBased(ratio)) wrappado num
//	              custom sampler que força RecordAndSample em spans com
//	              attribute `error=true` ou `latency_ms > threshold`.
//
// **Limitação OTel**: sampling decision é feita no Start() — atributos
// que mudam DEPOIS (após operação rodar e medir latência) não re-amostram.
// Para forçar "100% em erros e slow spans" precisamos:
//
//   - Caller setar `latency_ms` ou `error` no SpanStartOption (raro)
//   - OU usar tail-based sampling no Collector (recomendado em prod)
//
// Implementação aqui é **head-based aproximação**: span attrs avaliados no
// Start; caller que quiser garantir 100% sampling em "este é um span
// crítico" passa `attribute.Bool("force_sample", true)` no StartSpan.
// Erros descobertos depois ainda flow pro collector via ratio sampling —
// não 100%, mas com ratio 0.15 a chance de capturar é alta o suficiente
// pra debug em massa.
//
// Para "100% em erro" real, próximo passo é setup Collector tail sampler
// (fora do escopo Fase 0; documentar em ADR seguinte).
package services

import (
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

// forcedTraceState extrai TraceState do parent span context (se houver) pra
// preservação em decisões forced. SamplingParameters.ParentContext é um
// context.Context — span context vem via trace.SpanContextFromContext.
func forcedTraceState(p sdktrace.SamplingParameters) trace.TraceState {
	sc := trace.SpanContextFromContext(p.ParentContext)
	if sc.IsValid() {
		return sc.TraceState()
	}
	return trace.TraceState{}
}

// buildSampler — factory que retorna o sampler apropriado pra OTelConfig.
// Exposto pra que testes possam validar a decisão sem inicializar o
// TracerProvider completo.
func buildSampler(cfg OTelConfig) sdktrace.Sampler {
	strategy := cfg.SamplingStrategy
	if strategy == "" {
		strategy = "always_on"
	}

	switch strategy {
	case "always_on":
		return sdktrace.AlwaysSample()
	case "never":
		return sdktrace.NeverSample()
	case "adaptive":
		ratio := cfg.SamplingRatio
		if ratio <= 0 || ratio > 1 {
			ratio = 0.15
		}
		// ParentBased: root spans usam ratio; filhos herdam decisão do
		// parent (não re-amostra). Pra um trace inteiro 100% sampling
		// quando root foi capturado e 100% drop quando não.
		ratioSampler := sdktrace.TraceIDRatioBased(ratio)
		return &forceSampler{
			base: sdktrace.ParentBased(
				ratioSampler,
				sdktrace.WithRemoteParentSampled(sdktrace.AlwaysSample()),
				sdktrace.WithLocalParentSampled(sdktrace.AlwaysSample()),
			),
			latencySlowThresholdMs: cfg.LatencySlowThresholdMs,
		}
	default:
		// Unknown strategy: fallback safe (AlwaysSample). Loga seria
		// ideal mas logger não está em scope aqui; caller deve validar
		// strategy upstream antes de instanciar.
		return sdktrace.AlwaysSample()
	}
}

// forceSampler — wrapper que delega pro base sampler mas força
// RecordAndSample quando attrs indicam erro ou latência > threshold.
//
// Decisão é feita no Start(); attrs presentes em SamplingParameters são
// os passados via SpanStartOption pelo caller. Atributos seteados depois
// (via span.SetAttributes) não influenciam — limitação inerente do OTel
// SDK pra head-based sampling. Tail-based sampling no Collector resolve
// completamente; aqui é o melhor que dá pra fazer in-process.
type forceSampler struct {
	base                   sdktrace.Sampler
	latencySlowThresholdMs int
}

// ShouldSample implementa sdktrace.Sampler.
//
// Aceita "error" como bool OU como string ("true"/"false") porque o
// `OTelService` no codebase emite-o como `attribute.String("error", "true")`
// em vários paths legados (otel_service.go); novos paths podem usar
// `attribute.Bool`. Sampler tolerante a ambos garante coverage de erros
// em todos os caminhos historicamente instrumentados — codex P2 2026-05-23.
func (s *forceSampler) ShouldSample(p sdktrace.SamplingParameters) sdktrace.SamplingResult {
	// Inspect attrs do span pra forçar sample em casos críticos.
	for _, kv := range p.Attributes {
		key := string(kv.Key)
		switch key {
		case "error":
			if isErrorTrue(kv.Value) {
				return sdktrace.SamplingResult{
					Decision:   sdktrace.RecordAndSample,
					Tracestate: forcedTraceState(p),
				}
			}
		case "force_sample":
			if kv.Value.AsBool() {
				return sdktrace.SamplingResult{
					Decision:   sdktrace.RecordAndSample,
					Tracestate: forcedTraceState(p),
				}
			}
		case "latency_ms":
			if s.latencySlowThresholdMs > 0 && kv.Value.AsInt64() > int64(s.latencySlowThresholdMs) {
				return sdktrace.SamplingResult{
					Decision:   sdktrace.RecordAndSample,
					Tracestate: forcedTraceState(p),
				}
			}
		}
	}
	return s.base.ShouldSample(p)
}

// isErrorTrue detecta "error=true" tolerante a tipo (bool ou string).
// OTel attribute.Value tem `.Type()`: BOOL ou STRING ou outros; AsBool()
// retorna false silenciosamente em STRING (não converte), então testamos
// string explicitamente. Convenções aceitas: "true" (case-insensitive),
// "1", "yes". Padrão do codebase é `attribute.String("error", "true")`.
func isErrorTrue(v attribute.Value) bool {
	switch v.Type() {
	case attribute.BOOL:
		return v.AsBool()
	case attribute.STRING:
		s := v.AsString()
		return s == "true" || s == "TRUE" || s == "True" || s == "1" || s == "yes"
	default:
		// Other types (int, float) — treat as not-error.
		return false
	}
}

// Description implementa sdktrace.Sampler.
func (s *forceSampler) Description() string {
	return "force-sampler(error|force_sample|latency_ms>" + itoa(s.latencySlowThresholdMs) + "ms)+" + s.base.Description()
}

// itoa — strconv.Itoa local pra evitar import só por isso.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	negative := false
	if n < 0 {
		negative = true
		n = -n
	}
	buf := [16]byte{}
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if negative {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
