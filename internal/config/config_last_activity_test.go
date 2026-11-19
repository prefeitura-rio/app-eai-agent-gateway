package config

import (
	"testing"

	"github.com/spf13/viper"
)

func TestLoad_ReadsLastActivityAuthToken(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	SetTestDefaults()
	t.Setenv("LAST_ACTIVITY_AUTH_TOKEN", "synthetic-test-token")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Security.LastActivityAuthToken != "synthetic-test-token" {
		t.Fatalf("expected LAST_ACTIVITY_AUTH_TOKEN to be loaded, got %q", cfg.Security.LastActivityAuthToken)
	}
}

func TestLoad_LastActivityAuthTokenDefaultsToEmpty(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	SetTestDefaults()
	t.Setenv("LAST_ACTIVITY_AUTH_TOKEN", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Security.LastActivityAuthToken != "" {
		t.Fatalf("expected empty default, got %q", cfg.Security.LastActivityAuthToken)
	}
}
