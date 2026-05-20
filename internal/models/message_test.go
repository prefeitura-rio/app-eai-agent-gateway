package models

import "testing"

func TestIsKnownMessageType(t *testing.T) {
	tests := []struct {
		name        string
		messageType string
		want        bool
	}{
		{name: "empty preserves legacy text behavior", messageType: "", want: true},
		{name: "text", messageType: "text", want: true},
		{name: "audio", messageType: "audio", want: true},
		{name: "image", messageType: "image", want: true},
		{name: "video", messageType: "video", want: true},
		{name: "location", messageType: "location", want: true},
		{name: "interactive", messageType: "interactive", want: true},
		{name: "unsupported sentinel", messageType: "unsupported", want: true},
		{name: "unknown sentinel", messageType: "unknown", want: true},
		{name: "typo", messageType: "imgae", want: false},
		{name: "outbound-only registry type", messageType: "document", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsKnownMessageType(tt.messageType); got != tt.want {
				t.Fatalf("IsKnownMessageType(%q) = %v, want %v", tt.messageType, got, tt.want)
			}
		})
	}
}

func TestAllowsEmptyMedia(t *testing.T) {
	tests := []struct {
		messageType string
		want        bool
	}{
		{messageType: "unsupported", want: true},
		{messageType: "unknown", want: true},
		{messageType: "interactive", want: false},
		{messageType: "image", want: false},
		{messageType: "", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.messageType, func(t *testing.T) {
			if got := AllowsEmptyMedia(tt.messageType); got != tt.want {
				t.Fatalf("AllowsEmptyMedia(%q) = %v, want %v", tt.messageType, got, tt.want)
			}
		})
	}
}
