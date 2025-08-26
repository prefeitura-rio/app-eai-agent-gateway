package config

import (
	"github.com/spf13/viper"
)

// LoadForTesting loads configuration for testing without validation
func LoadForTesting() (*Config, error) {
	var config Config

	// Set defaults
	setDefaults()

	// Load from environment variables
	viper.AutomaticEnv()

	// Unmarshal the configuration
	if err := viper.Unmarshal(&config); err != nil {
		return nil, err
	}

	// Duration fields are already converted by viper automatically

	return &config, nil
}

// SetTestDefaults sets reasonable test defaults for all required fields
func SetTestDefaults() {
	testDefaults := map[string]string{
		"APP_PREFIX":          "test",
		"RABBITMQ_URL":        "amqp://guest:guest@localhost:5672/",
		"REDIS_DSN":           "redis://localhost:6379/0",
		"REDIS_BACKEND":       "redis://localhost:6379/1",
		"REASONING_ENGINE_ID": "test-engine-id",
		"PROJECT_ID":          "test-project",
		"PROJECT_NUMBER":      "123456789",
		"LOCATION":            "us-central1",
		"SERVICE_ACCOUNT":     "/path/to/test-service-account.json",
		"GCS_BUCKET":          "test-bucket",
		"EAI_AGENT_URL":       "https://test-agent.com",
		"EAI_AGENT_TOKEN":     "test-token",
		"LLM_MODEL":           "google_ai/gemini-2.5-flash-lite",
		"EMBEDDING_MODEL":     "google_ai/text-embedding-004",
	}

	for key, value := range testDefaults {
		viper.SetDefault(key, value)
	}
}
