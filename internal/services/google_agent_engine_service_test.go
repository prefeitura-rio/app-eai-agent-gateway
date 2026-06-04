package services

import (
	"testing"
)

func TestStripBOM(t *testing.T) {
	tests := []struct {
		name     string
		input    []byte
		expected []byte
		hadBOM   bool
	}{
		{
			name:     "UTF-8 BOM",
			input:    []byte{0xEF, 0xBB, 0xBF, '{', '"', 'k', 'e', 'y', '"', ':', '"', 'v', 'a', 'l', '"', '}'},
			expected: []byte{'{', '"', 'k', 'e', 'y', '"', ':', '"', 'v', 'a', 'l', '"', '}'},
			hadBOM:   true,
		},
		{
			name:     "UTF-16 BE BOM",
			input:    []byte{0xFE, 0xFF, '{', '}'},
			expected: []byte{'{', '}'},
			hadBOM:   true,
		},
		{
			name:     "UTF-16 LE BOM",
			input:    []byte{0xFF, 0xFE, '[', ']'},
			expected: []byte{'[', ']'},
			hadBOM:   true,
		},
		{
			name:     "UTF-32 BE BOM",
			input:    []byte{0x00, 0x00, 0xFE, 0xFF, 't', 'r', 'u', 'e'},
			expected: []byte{'t', 'r', 'u', 'e'},
			hadBOM:   true,
		},
		{
			name:     "UTF-32 LE BOM",
			input:    []byte{0xFF, 0xFE, 0x00, 0x00, 'n', 'u', 'l', 'l'},
			expected: []byte{'n', 'u', 'l', 'l'},
			hadBOM:   true,
		},
		{
			name:     "No BOM - valid JSON",
			input:    []byte{'{', '"', 'f', 'o', 'o', '"', ':', '1', '}'},
			expected: []byte{'{', '"', 'f', 'o', 'o', '"', ':', '1', '}'},
			hadBOM:   false,
		},
		{
			name:     "Empty input",
			input:    []byte{},
			expected: []byte{},
			hadBOM:   false,
		},
		{
			name:     "Partial BOM (too short)",
			input:    []byte{0xEF, 0xBB},
			expected: []byte{0xEF, 0xBB},
			hadBOM:   false,
		},
		{
			name:     "False positive - looks like BOM but isn't",
			input:    []byte{0xEF, 0xBB, 0xAA, '{', '}'},
			expected: []byte{0xEF, 0xBB, 0xAA, '{', '}'},
			hadBOM:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, hadBOM := stripBOM(tt.input)

			// Check BOM detection
			if hadBOM != tt.hadBOM {
				t.Errorf("stripBOM() hadBOM = %v, want %v", hadBOM, tt.hadBOM)
			}

			// Check result
			if len(result) != len(tt.expected) {
				t.Errorf("stripBOM() result length = %d, want %d", len(result), len(tt.expected))
				return
			}

			for i := range result {
				if result[i] != tt.expected[i] {
					t.Errorf("stripBOM() result[%d] = %v, want %v", i, result[i], tt.expected[i])
				}
			}
		})
	}
}

func TestStripBOM_GoogleAPIResponseSimulation(t *testing.T) {
	// Simulate a real Google API response with UTF-8 BOM
	// This is the actual error case we're fixing
	responseWithBOM := []byte{0xEF, 0xBB, 0xBF}
	responseWithBOM = append(responseWithBOM, []byte(`{"name":"operations/123","done":true,"response":{"output":{"messages":[{"content":"Hello"}]}}}`)...)

	result, hadBOM := stripBOM(responseWithBOM)

	if !hadBOM {
		t.Error("Expected BOM to be detected in simulated Google API response")
	}

	expectedJSON := `{"name":"operations/123","done":true,"response":{"output":{"messages":[{"content":"Hello"}]}}}`
	if string(result) != expectedJSON {
		t.Errorf("stripBOM() result = %q, want %q", string(result), expectedJSON)
	}

	// Verify the first character is now '{' instead of BOM
	if len(result) == 0 || result[0] != '{' {
		t.Errorf("stripBOM() first character = %v, want '{' (0x7B)", result[0])
	}
}

func BenchmarkStripBOM_WithBOM(b *testing.B) {
	data := []byte{0xEF, 0xBB, 0xBF, '{', '"', 'k', 'e', 'y', '"', ':', '"', 'v', 'a', 'l', 'u', 'e', '"', '}'}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		stripBOM(data)
	}
}

func BenchmarkStripBOM_WithoutBOM(b *testing.B) {
	data := []byte{'{', '"', 'k', 'e', 'y', '"', ':', '"', 'v', 'a', 'l', 'u', 'e', '"', '}'}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		stripBOM(data)
	}
}

func TestClassifyEngineError(t *testing.T) {
	tests := []struct {
		name     string
		errStr   string
		expected string
	}{
		// rate limit — mais específico, vem antes de unavailable/default
		{"rate limit exceeded", "rate limit exceeded: quota", "ratelimited"},
		{"non-2xx 429", "non-2xx response: 429 - too many", "ratelimited"},
		{"too many requests phrase", "engine returned Too Many Requests", "ratelimited"},
		// Quota do Vertex/Google → ratelimited (gRPC RESOURCE_EXHAUSTED / "quota exceeded")
		{"grpc resource exhausted", "rpc error: code = ResourceExhausted desc = Quota exceeded for aiplatform.googleapis.com", "ratelimited"},
		{"resource_exhausted underscore", "RESOURCE_EXHAUSTED: quota metric limit", "ratelimited"},
		{"quota exceeded phrase", "Quota exceeded for quota metric 'Online prediction requests'", "ratelimited"},
		// 429 dentro de um ID/URL (não status) NÃO deve virar ratelimited
		{"429 in engine id is not ratelimited", "Post \"https://.../reasoningEngines/429000111:query\": context deadline exceeded", "timeout"},
		{"429 in id with no other signal is default", "failed for reasoningEngines/4290: boom", "default"},
		// timeout — inclui "timed out" (gap corrigido)
		{"polling timed out", "polling timed out after 30s", "timeout"},
		{"context deadline", "context deadline exceeded", "timeout"},
		{"literal timeout", "request timeout", "timeout"},
		{"context canceled", "context canceled", "timeout"},
		// availability — 5xx / conexão
		{"503", "non-2xx response: 503 - service down", "unavailable"},
		{"502", "non-2xx response: 502", "unavailable"},
		{"504", "non-2xx response: 504", "unavailable"},
		{"connection refused", "dial tcp: connection refused", "unavailable"},
		{"unavailable word", "engine unavailable", "unavailable"},
		// default — desconhecido
		{"thread not found", "thread not found: abc", "default"},
		{"500", "non-2xx response: 500 - internal", "default"},
		{"marshal error", "failed to marshal request: bad", "default"},
		{"empty", "", "default"},
		// case-insensitive
		{"uppercase RATE LIMIT", "RATE LIMIT EXCEEDED", "ratelimited"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := classifyEngineError(tt.errStr); got != tt.expected {
				t.Errorf("classifyEngineError(%q) = %q, want %q", tt.errStr, got, tt.expected)
			}
		})
	}
}

func TestIsPreExecutionRetriable(t *testing.T) {
	tests := []struct {
		name   string
		errStr string
		want   bool
	}{
		// SEGURO: a operação async nunca começou no engine.
		{"connection refused", "failed to make request: dial tcp: connect: connection refused", true},
		{"503 status", "non-2xx response: 503 - service unavailable", true},
		{"unavailable word", "rpc error: code = Unavailable desc = the service is currently unavailable", true},

		// INSEGURO: pode ter executado (criado chamado) → nunca re-tentar.
		{"plain timeout", "request timeout", false},
		{"timed out", "polling timed out after 30s", false},
		{"deadline exceeded", "context deadline exceeded", false},
		{"context canceled", "context canceled", false},
		{"500 whose body says unavailable is still unsafe", "non-2xx response: 500 - backend reported unavailable", false},
		{"502 bad gateway", "non-2xx response: 502 - bad gateway", false},
		{"504 gateway timeout", "non-2xx response: 504 - gateway timeout", false},

		// CRÍTICO: exclusão tem prioridade — "unavailable"/"503" + sinal inseguro → false.
		{"503 but also timed out", "non-2xx response: 503 - upstream timed out", false},
		{"unavailable but deadline", "unavailable: context deadline exceeded", false},
		{"504 response whose body mentions unavailable", "non-2xx response: 504 - upstream service unavailable", false},

		// Status casado na forma canônica: um ID com 503 (não "response: 503") e
		// sem sinal transitório real NÃO deve ser re-tentado (mesmo guard do "429").
		{"503 in engine id is not retriable", "non-2xx response: 500 - boom referencing 503 elsewhere", false},

		// Fora do conjunto seguro → false (não re-tenta).
		{"500 internal", "non-2xx response: 500 - internal server error", false},
		{"rate limit", "rate limit exceeded: quota", false},
		{"thread not found", "thread not found: abc", false},
		{"empty", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isPreExecutionRetriable(tt.errStr); got != tt.want {
				t.Errorf("isPreExecutionRetriable(%q) = %v, want %v", tt.errStr, got, tt.want)
			}
		})
	}
}
