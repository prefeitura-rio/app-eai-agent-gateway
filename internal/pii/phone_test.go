package pii

import (
	"strings"
	"testing"
)

func TestMaskPhone(t *testing.T) {
	tests := []struct {
		name  string
		phone string
		want  string
	}{
		{name: "whatsapp number", phone: "5521900000000", want: "5521900***"},
		{name: "with plus sign", phone: "+5521900000000", want: "+5521900***"},
		{name: "minimum length", phone: "12345678", want: "12***"},
		{name: "too short", phone: "1234567", want: "****"},
		{name: "empty", phone: "", want: "****"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := MaskPhone(tt.phone)
			if got != tt.want {
				t.Fatalf("MaskPhone(%q) = %q, want %q", tt.phone, got, tt.want)
			}
			if tt.phone != "" && len(tt.phone) >= 8 && strings.Contains(got, tt.phone) {
				t.Fatalf("MaskPhone(%q) leaked the full number: %q", tt.phone, got)
			}
		})
	}
}
