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
