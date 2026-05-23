package services

import (
	"context"
	"strings"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

// ─── buildSampler strategy tests ─────────────────────────────────────────

func TestBuildSampler_AlwaysOn(t *testing.T) {
	cfg := OTelConfig{SamplingStrategy: "always_on"}
	s := buildSampler(cfg)
	if !strings.Contains(s.Description(), "AlwaysOnSampler") {
		t.Errorf("expected AlwaysOnSampler description, got %q", s.Description())
	}
}

func TestBuildSampler_DefaultEmpty(t *testing.T) {
	// Empty string falls back to always_on (backward-compat preservada).
	s := buildSampler(OTelConfig{})
	if !strings.Contains(s.Description(), "AlwaysOnSampler") {
		t.Errorf("default sampling strategy should be always_on; got %q", s.Description())
	}
}

func TestBuildSampler_Never(t *testing.T) {
	cfg := OTelConfig{SamplingStrategy: "never"}
	s := buildSampler(cfg)
	if !strings.Contains(s.Description(), "AlwaysOffSampler") {
		t.Errorf("expected AlwaysOffSampler (never), got %q", s.Description())
	}
}

func TestBuildSampler_Adaptive(t *testing.T) {
	cfg := OTelConfig{SamplingStrategy: "adaptive", SamplingRatio: 0.15, LatencySlowThresholdMs: 5000}
	s := buildSampler(cfg)
	if _, ok := s.(*forceSampler); !ok {
		t.Errorf("adaptive strategy should produce *forceSampler, got %T", s)
	}
	if !strings.Contains(s.Description(), "force-sampler") {
		t.Errorf("expected force-sampler in description, got %q", s.Description())
	}
}

func TestBuildSampler_AdaptiveDefaultsRatio(t *testing.T) {
	// Ratio inválido → fallback pra 0.15.
	cfg := OTelConfig{SamplingStrategy: "adaptive", SamplingRatio: 0}
	s := buildSampler(cfg)
	if _, ok := s.(*forceSampler); !ok {
		t.Errorf("adaptive with invalid ratio should still produce *forceSampler; got %T", s)
	}
	cfg2 := OTelConfig{SamplingStrategy: "adaptive", SamplingRatio: 1.5}
	s2 := buildSampler(cfg2)
	if _, ok := s2.(*forceSampler); !ok {
		t.Errorf("adaptive with ratio>1 should still produce *forceSampler; got %T", s2)
	}
}

func TestBuildSampler_UnknownStrategy_FallbackSafe(t *testing.T) {
	cfg := OTelConfig{SamplingStrategy: "xyz"}
	s := buildSampler(cfg)
	if !strings.Contains(s.Description(), "AlwaysOnSampler") {
		t.Errorf("unknown strategy should fallback to always_on; got %q", s.Description())
	}
}

// ─── forceSampler decision tests ────────────────────────────────────────

// neverSampler — base sampler que NUNCA amostra, pra isolar decisões do
// forceSampler (sample só quando attrs forçam).
type neverSampler struct{}

func (neverSampler) ShouldSample(p sdktrace.SamplingParameters) sdktrace.SamplingResult {
	return sdktrace.SamplingResult{Decision: sdktrace.Drop}
}
func (neverSampler) Description() string { return "neverSampler" }

func newSamplingParams(attrs []attribute.KeyValue) sdktrace.SamplingParameters {
	tid := trace.TraceID{1, 2, 3}
	return sdktrace.SamplingParameters{
		ParentContext: context.Background(),
		TraceID:       tid,
		Name:          "test-span",
		Attributes:    attrs,
	}
}

func TestForceSampler_ErrorAttr_ForcesSample(t *testing.T) {
	fs := &forceSampler{base: neverSampler{}, latencySlowThresholdMs: 5000}
	result := fs.ShouldSample(newSamplingParams([]attribute.KeyValue{
		attribute.Bool("error", true),
	}))
	if result.Decision != sdktrace.RecordAndSample {
		t.Errorf("expected RecordAndSample on error=true, got %v", result.Decision)
	}
}

func TestForceSampler_ErrorFalse_DelegatesToBase(t *testing.T) {
	fs := &forceSampler{base: neverSampler{}, latencySlowThresholdMs: 5000}
	result := fs.ShouldSample(newSamplingParams([]attribute.KeyValue{
		attribute.Bool("error", false),
	}))
	if result.Decision != sdktrace.Drop {
		t.Errorf("expected Drop (delegated to never), got %v", result.Decision)
	}
}

func TestForceSampler_ForceSampleAttr(t *testing.T) {
	fs := &forceSampler{base: neverSampler{}, latencySlowThresholdMs: 5000}
	result := fs.ShouldSample(newSamplingParams([]attribute.KeyValue{
		attribute.Bool("force_sample", true),
	}))
	if result.Decision != sdktrace.RecordAndSample {
		t.Errorf("expected RecordAndSample on force_sample=true, got %v", result.Decision)
	}
}

func TestForceSampler_LatencyOverThreshold(t *testing.T) {
	fs := &forceSampler{base: neverSampler{}, latencySlowThresholdMs: 5000}
	result := fs.ShouldSample(newSamplingParams([]attribute.KeyValue{
		attribute.Int64("latency_ms", 6000),
	}))
	if result.Decision != sdktrace.RecordAndSample {
		t.Errorf("expected RecordAndSample on latency_ms=6000 > threshold 5000, got %v", result.Decision)
	}
}

func TestForceSampler_LatencyUnderThreshold(t *testing.T) {
	fs := &forceSampler{base: neverSampler{}, latencySlowThresholdMs: 5000}
	result := fs.ShouldSample(newSamplingParams([]attribute.KeyValue{
		attribute.Int64("latency_ms", 100),
	}))
	if result.Decision != sdktrace.Drop {
		t.Errorf("expected Drop on latency_ms=100 < threshold 5000, got %v", result.Decision)
	}
}

func TestForceSampler_NoAttrs_Delegates(t *testing.T) {
	fs := &forceSampler{base: neverSampler{}, latencySlowThresholdMs: 5000}
	result := fs.ShouldSample(newSamplingParams(nil))
	if result.Decision != sdktrace.Drop {
		t.Errorf("expected Drop without forcing attrs, got %v", result.Decision)
	}
}

func TestForceSampler_LatencyDisabled_NotForced(t *testing.T) {
	// LatencySlowThresholdMs=0 → latency_ms attr não força sampling.
	fs := &forceSampler{base: neverSampler{}, latencySlowThresholdMs: 0}
	result := fs.ShouldSample(newSamplingParams([]attribute.KeyValue{
		attribute.Int64("latency_ms", 99999),
	}))
	if result.Decision != sdktrace.Drop {
		t.Errorf("expected Drop when threshold=0 (disabled), got %v", result.Decision)
	}
}

// Fix codex P2 2026-05-23: error attribute pode chegar como string
// ("true"/"false") nas instrumentations existentes do OTelService — sampler
// deve forçar sampling em ambos os tipos.

func TestForceSampler_ErrorAsString_ForcesSample(t *testing.T) {
	fs := &forceSampler{base: neverSampler{}, latencySlowThresholdMs: 5000}
	cases := []string{"true", "True", "TRUE", "1", "yes"}
	for _, val := range cases {
		t.Run("val="+val, func(t *testing.T) {
			result := fs.ShouldSample(newSamplingParams([]attribute.KeyValue{
				attribute.String("error", val),
			}))
			if result.Decision != sdktrace.RecordAndSample {
				t.Errorf("expected RecordAndSample on error=%q (string), got %v", val, result.Decision)
			}
		})
	}
}

func TestForceSampler_ErrorAsStringFalse_Delegates(t *testing.T) {
	fs := &forceSampler{base: neverSampler{}, latencySlowThresholdMs: 5000}
	result := fs.ShouldSample(newSamplingParams([]attribute.KeyValue{
		attribute.String("error", "false"),
	}))
	if result.Decision != sdktrace.Drop {
		t.Errorf("expected Drop on error=\"false\" (delegated), got %v", result.Decision)
	}
}
