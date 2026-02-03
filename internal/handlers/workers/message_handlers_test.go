package workers

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
			input:    []byte{0xEF, 0xBB, 0xBF, 'h', 'e', 'l', 'l', 'o'},
			expected: []byte{'h', 'e', 'l', 'l', 'o'},
			hadBOM:   true,
		},
		{
			name:     "UTF-16 BE BOM",
			input:    []byte{0xFE, 0xFF, 'h', 'i'},
			expected: []byte{'h', 'i'},
			hadBOM:   true,
		},
		{
			name:     "UTF-16 LE BOM",
			input:    []byte{0xFF, 0xFE, 't', 'e', 's', 't'},
			expected: []byte{'t', 'e', 's', 't'},
			hadBOM:   true,
		},
		{
			name:     "UTF-32 BE BOM",
			input:    []byte{0x00, 0x00, 0xFE, 0xFF, 'd', 'a', 't', 'a'},
			expected: []byte{'d', 'a', 't', 'a'},
			hadBOM:   true,
		},
		{
			name:     "UTF-32 LE BOM",
			input:    []byte{0xFF, 0xFE, 0x00, 0x00, 'j', 's', 'o', 'n'},
			expected: []byte{'j', 's', 'o', 'n'},
			hadBOM:   true,
		},
		{
			name:     "No BOM",
			input:    []byte{'n', 'o', 'r', 'm', 'a', 'l'},
			expected: []byte{'n', 'o', 'r', 'm', 'a', 'l'},
			hadBOM:   false,
		},
		{
			name:     "Empty input",
			input:    []byte{},
			expected: []byte{},
			hadBOM:   false,
		},
		{
			name:     "JSON with UTF-8 BOM",
			input:    []byte{0xEF, 0xBB, 0xBF, '{', '"', 'k', 'e', 'y', '"', ':', '"', 'v', 'a', 'l', '"', '}'},
			expected: []byte{'{', '"', 'k', 'e', 'y', '"', ':', '"', 'v', 'a', 'l', '"', '}'},
			hadBOM:   true,
		},
		{
			name:     "Partial BOM (too short for UTF-8)",
			input:    []byte{0xEF, 0xBB},
			expected: []byte{0xEF, 0xBB},
			hadBOM:   false,
		},
		{
			name:     "False positive - looks like BOM but isn't complete",
			input:    []byte{0xEF, 0xBB, 0xAA, 'd', 'a', 't', 'a'},
			expected: []byte{0xEF, 0xBB, 0xAA, 'd', 'a', 't', 'a'},
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

func TestStripBOM_RealWorldExample(t *testing.T) {
	// This simulates the actual error case: UTF-8 BOM before JSON
	// The BOM (0xEF 0xBB 0xBF) when incorrectly interpreted shows as 'â'
	jsonWithBOM := []byte{0xEF, 0xBB, 0xBF}
	jsonWithBOM = append(jsonWithBOM, []byte(`{"output":{"messages":[{"content":"Hello"}]}}`)...)

	result, hadBOM := stripBOM(jsonWithBOM)

	if !hadBOM {
		t.Error("Expected BOM to be detected")
	}

	// Verify the result is valid JSON without BOM
	expectedJSON := `{"output":{"messages":[{"content":"Hello"}]}}`
	if string(result) != expectedJSON {
		t.Errorf("stripBOM() result = %q, want %q", string(result), expectedJSON)
	}

	// The first character should now be '{' instead of the BOM
	if len(result) == 0 || result[0] != '{' {
		t.Errorf("stripBOM() first character = %v, want '{' (0x7B)", result[0])
	}
}

func BenchmarkStripBOM_WithBOM(b *testing.B) {
	data := []byte{0xEF, 0xBB, 0xBF, 'h', 'e', 'l', 'l', 'o', ' ', 'w', 'o', 'r', 'l', 'd'}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		stripBOM(data)
	}
}

func BenchmarkStripBOM_WithoutBOM(b *testing.B) {
	data := []byte{'h', 'e', 'l', 'l', 'o', ' ', 'w', 'o', 'r', 'l', 'd'}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		stripBOM(data)
	}
}
