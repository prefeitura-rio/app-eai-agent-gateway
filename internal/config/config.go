package config

import (
	"fmt"
	"strings"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/spf13/viper"
)

// Config holds all configuration for the application
type Config struct {
	// Core Application
	AppPrefix   string `mapstructure:"APP_PREFIX"`
	MaxParallel int    `mapstructure:"MAX_PARALLEL"`

	// HTTP Server
	Server ServerConfig `mapstructure:",squash"`

	// Message Queue (RabbitMQ)
	RabbitMQ RabbitMQConfig `mapstructure:",squash"`

	// Redis
	Redis RedisConfig `mapstructure:",squash"`

	// Google Cloud
	GoogleCloud GoogleCloudConfig `mapstructure:",squash"`

	// Google Agent Engine
	GoogleAgentEngine GoogleAgentEngineConfig `mapstructure:",squash"`

	// External Services
	EAIAgent EAIAgentConfig `mapstructure:",squash"`

	// Audio Transcription
	Transcribe TranscribeConfig `mapstructure:",squash"`

	// Observability
	Observability ObservabilityConfig `mapstructure:",squash"`

	// Security
	Security SecurityConfig `mapstructure:",squash"`

	// Callback
	Callback CallbackConfig `mapstructure:",squash"`
}

type ServerConfig struct {
	Port         int           `mapstructure:"SERVER_PORT"`
	Host         string        `mapstructure:"SERVER_HOST"`
	ReadTimeout  time.Duration `mapstructure:"SERVER_READ_TIMEOUT"`
	WriteTimeout time.Duration `mapstructure:"SERVER_WRITE_TIMEOUT"`
	IdleTimeout  time.Duration `mapstructure:"SERVER_IDLE_TIMEOUT"`
}

type RabbitMQConfig struct {
	URL               string        `mapstructure:"RABBITMQ_URL"`
	Exchange          string        `mapstructure:"RABBITMQ_EXCHANGE"`
	UserQueue         string        `mapstructure:"RABBITMQ_USER_QUEUE"`
	UserMessagesQueue string        `mapstructure:"RABBITMQ_USER_MESSAGES_QUEUE"`
	DLXExchange       string        `mapstructure:"RABBITMQ_DLX_EXCHANGE"`
	MaxParallel       int           `mapstructure:"MAX_PARALLEL"`
	MaxRetries        int           `mapstructure:"RABBITMQ_MAX_RETRIES"`
	RetryDelay        int           `mapstructure:"RABBITMQ_RETRY_DELAY"`
	MessageTimeout    time.Duration `mapstructure:"RABBITMQ_MESSAGE_TIMEOUT"`
	SoftTimeLimit     int           `mapstructure:"CELERY_SOFT_TIME_LIMIT"`
	HardTimeLimit     int           `mapstructure:"CELERY_TIME_LIMIT"`
}

type RedisConfig struct {
	DSN             string        `mapstructure:"REDIS_DSN"`
	Backend         string        `mapstructure:"REDIS_BACKEND"`
	TaskResultTTL   time.Duration `mapstructure:"REDIS_TASK_RESULT_TTL"`
	TaskStatusTTL   time.Duration `mapstructure:"REDIS_TASK_STATUS_TTL"`
	CacheTTL        time.Duration `mapstructure:"CACHE_TTL_SECONDS"`
	AgentIDCacheTTL time.Duration `mapstructure:"AGENT_ID_CACHE_TTL"`

	// Connection Pool Settings
	PoolSize              int `mapstructure:"REDIS_POOL_SIZE"`
	MinIdleConnections    int `mapstructure:"REDIS_MIN_IDLE_CONNECTIONS"`
	MaxIdleConnections    int `mapstructure:"REDIS_MAX_IDLE_CONNECTIONS"`
	ConnectionMaxIdleTime int `mapstructure:"REDIS_CONNECTION_MAX_IDLE_TIME"`
	ConnectionMaxLifetime int `mapstructure:"REDIS_CONNECTION_MAX_LIFETIME"`
	DialTimeout           int `mapstructure:"REDIS_DIAL_TIMEOUT"`
	ReadTimeout           int `mapstructure:"REDIS_READ_TIMEOUT"`
	WriteTimeout          int `mapstructure:"REDIS_WRITE_TIMEOUT"`
}

type GoogleCloudConfig struct {
	ProjectID         string `mapstructure:"PROJECT_ID"`
	ProjectNumber     string `mapstructure:"PROJECT_NUMBER"`
	Location          string `mapstructure:"LOCATION"`
	ServiceAccount    string `mapstructure:"SERVICE_ACCOUNT"`
	GCSBucket         string `mapstructure:"GCS_BUCKET"`
	ReasoningEngineID string `mapstructure:"REASONING_ENGINE_ID"`

	// Rate Limiting
	RateLimitEnabled     bool    `mapstructure:"GOOGLE_API_RATE_LIMIT_ENABLED"`
	MaxRequestsPerMinute int     `mapstructure:"GOOGLE_API_MAX_REQUESTS_PER_MINUTE"`
	BackoffMultiplier    float64 `mapstructure:"GOOGLE_API_BACKOFF_MULTIPLIER"`
	MaxBackoffSeconds    int     `mapstructure:"GOOGLE_API_MAX_BACKOFF_SECONDS"`
	MinBackoffSeconds    int     `mapstructure:"GOOGLE_API_MIN_BACKOFF_SECONDS"`
}

type GoogleAgentEngineConfig struct {
	ProjectID         string `mapstructure:"PROJECT_ID"`
	Location          string `mapstructure:"LOCATION"`
	ReasoningEngineID string `mapstructure:"REASONING_ENGINE_ID"`

	// Authentication
	CredentialsPath string `mapstructure:"GOOGLE_AGENT_ENGINE_CREDENTIALS_PATH"`
	CredentialsJSON string `mapstructure:"SERVICE_ACCOUNT"`

	// Timeouts and Limits
	RequestTimeout   time.Duration `mapstructure:"GOOGLE_AGENT_ENGINE_REQUEST_TIMEOUT"`
	MaxMessageLength int           `mapstructure:"GOOGLE_AGENT_ENGINE_MAX_MESSAGE_LENGTH"`
	MaxRetries       int           `mapstructure:"GOOGLE_AGENT_ENGINE_MAX_RETRIES"`
	RetryBackoff     time.Duration `mapstructure:"GOOGLE_AGENT_ENGINE_RETRY_BACKOFF"`
}

type EAIAgentConfig struct {
	URL                    string `mapstructure:"EAI_AGENT_URL"`
	Token                  string `mapstructure:"EAI_AGENT_TOKEN"`
	ContextWindowLimit     int    `mapstructure:"EAI_AGENT_CONTEXT_WINDOW_LIMIT"`
	MaxGoogleSearchPerStep int    `mapstructure:"EAI_AGENT_MAX_GOOGLE_SEARCH_PER_STEP"`
	LLMModel               string `mapstructure:"LLM_MODEL"`
	EmbeddingModel         string `mapstructure:"EMBEDDING_MODEL"`
}

type TranscribeConfig struct {
	MaxDuration        int           `mapstructure:"TRANSCRIBE_MAX_DURATION"`
	MaxDurationMinutes int           `mapstructure:"TRANSCRIBE_MAX_DURATION_MINUTES"`
	AllowedURLs        string        `mapstructure:"TRANSCRIBE_ALLOWED_URLS"`
	MaxFileSizeMB      int           `mapstructure:"TRANSCRIBE_MAX_FILE_SIZE_MB"`
	SupportedFormats   string        `mapstructure:"TRANSCRIBE_SUPPORTED_FORMATS"`
	TempDir            string        `mapstructure:"TRANSCRIBE_TEMP_DIR"`
	CleanupInterval    time.Duration `mapstructure:"TRANSCRIBE_CLEANUP_INTERVAL"`
	RequestTimeout     time.Duration `mapstructure:"TRANSCRIBE_REQUEST_TIMEOUT"`
	DownloadTimeout    time.Duration `mapstructure:"TRANSCRIBE_DOWNLOAD_TIMEOUT"`

	// Google Cloud Speech configuration
	LanguageCode          string `mapstructure:"TRANSCRIBE_LANGUAGE_CODE"`
	SampleRateHertz       int    `mapstructure:"TRANSCRIBE_SAMPLE_RATE_HERTZ"`
	EnableWordTimeOffsets bool   `mapstructure:"TRANSCRIBE_ENABLE_WORD_TIME_OFFSETS"`
	EnableWordConfidence  bool   `mapstructure:"TRANSCRIBE_ENABLE_WORD_CONFIDENCE"`
	MaxAlternatives       int    `mapstructure:"TRANSCRIBE_MAX_ALTERNATIVES"`
	ProfanityFilter       bool   `mapstructure:"TRANSCRIBE_PROFANITY_FILTER"`
}

type ObservabilityConfig struct {
	// OpenTelemetry
	OTelEnabled        bool   `mapstructure:"OTEL_ENABLED"`
	OTelCollectorURL   string `mapstructure:"OTEL_COLLECTOR_URL"`
	OTelServiceName    string `mapstructure:"OTEL_SERVICE_NAME"`
	OTelServiceVersion string `mapstructure:"OTEL_SERVICE_VERSION"`
	OTelEnvironment    string `mapstructure:"OTEL_ENVIRONMENT"`

	// Metrics
	MetricsEnabled bool   `mapstructure:"METRICS_ENABLED"`
	MetricsPort    int    `mapstructure:"METRICS_PORT"`
	MetricsPath    string `mapstructure:"METRICS_PATH"`

	// Logging
	LogLevel  string `mapstructure:"LOG_LEVEL"`
	LogFormat string `mapstructure:"LOG_FORMAT"`
	LogOutput string `mapstructure:"LOG_OUTPUT"`

	// Health Checks
	HealthCheckTimeout    time.Duration `mapstructure:"HEALTH_CHECK_TIMEOUT"`
	ReadinessCheckTimeout time.Duration `mapstructure:"READINESS_CHECK_TIMEOUT"`
}

type SecurityConfig struct {
	CORSEnabled       bool   `mapstructure:"CORS_ENABLED"`
	CORSOrigins       string `mapstructure:"CORS_ORIGINS"`
	MaxRequestSize    int64  `mapstructure:"MAX_REQUEST_SIZE"`
	RateLimitEnabled  bool   `mapstructure:"RATE_LIMIT_ENABLED"`
	RateLimitRequests int    `mapstructure:"RATE_LIMIT_REQUESTS"`

	// Input Validation
	MaxContentLength    int    `mapstructure:"MAX_CONTENT_LENGTH"`
	MaxUserIDLength     int    `mapstructure:"MAX_USER_ID_LENGTH"`
	MaxAgentIDLength    int    `mapstructure:"MAX_AGENT_ID_LENGTH"`
	EnableContentFilter bool   `mapstructure:"ENABLE_CONTENT_FILTER"`
	AllowedDomains      string `mapstructure:"SECURITY_ALLOWED_DOMAINS"`
	BlockedDomains      string `mapstructure:"SECURITY_BLOCKED_DOMAINS"`
	StrictMode          bool   `mapstructure:"SECURITY_STRICT_MODE"`
}

type CallbackConfig struct {
	Enabled       bool   `mapstructure:"CALLBACK_ENABLED"`
	Timeout       int    `mapstructure:"CALLBACK_TIMEOUT"`
	MaxRetries    int    `mapstructure:"CALLBACK_MAX_RETRIES"`
	EnableHMAC    bool   `mapstructure:"CALLBACK_ENABLE_HMAC"`
	HMACSecret    string `mapstructure:"CALLBACK_HMAC_SECRET"`
	RequireHTTPS  bool   `mapstructure:"CALLBACK_REQUIRE_HTTPS"`
	AllowedDomain string `mapstructure:"CALLBACK_ALLOWED_DOMAIN"`
}

// Load loads configuration from environment variables and files
func Load() (*Config, error) {
	viper.AutomaticEnv()

	// Set defaults
	setDefaults()

	// Explicitly bind environment variables for nested structures
	bindEnvironmentVariables()

	logrus.Info("Using environment variables and defaults (no config files)")

	var config Config
	if err := viper.Unmarshal(&config); err != nil {
		return nil, fmt.Errorf("error unmarshaling config: %w", err)
	}

	// Validate required fields
	if err := validateRequired(&config); err != nil {
		return nil, fmt.Errorf("configuration validation failed: %w", err)
	}

	// Duration fields are already converted by viper automatically

	return &config, nil
}

func setDefaults() {
	// Core Application
	viper.SetDefault("MAX_PARALLEL", 8)

	// HTTP Server
	viper.SetDefault("SERVER_PORT", 8000)
	viper.SetDefault("SERVER_HOST", "0.0.0.0")
	viper.SetDefault("SERVER_READ_TIMEOUT", "30s")
	viper.SetDefault("SERVER_WRITE_TIMEOUT", "30s")
	viper.SetDefault("SERVER_IDLE_TIMEOUT", "120s")

	// RabbitMQ
	viper.SetDefault("RABBITMQ_EXCHANGE", "eai_gateway")
	viper.SetDefault("RABBITMQ_USER_QUEUE", "user_messages")
	viper.SetDefault("RABBITMQ_AGENT_QUEUE", "agent_messages")
	viper.SetDefault("RABBITMQ_USER_MESSAGES_QUEUE", "user_messages")
	viper.SetDefault("RABBITMQ_AGENT_MESSAGES_QUEUE", "agent_messages")
	viper.SetDefault("RABBITMQ_DLX_EXCHANGE", "eai_gateway_dlx")
	viper.SetDefault("RABBITMQ_MAX_RETRIES", 3)
	viper.SetDefault("RABBITMQ_RETRY_DELAY", 30)
	viper.SetDefault("RABBITMQ_MESSAGE_TIMEOUT", "2000s") // 33+ minutes to allow Google API calls
	viper.SetDefault("CELERY_SOFT_TIME_LIMIT", 90)
	viper.SetDefault("CELERY_TIME_LIMIT", 120)

	// Redis
	viper.SetDefault("REDIS_TASK_RESULT_TTL", "120s")
	viper.SetDefault("REDIS_TASK_STATUS_TTL", "600s")
	viper.SetDefault("CACHE_TTL_SECONDS", "720s")
	viper.SetDefault("AGENT_ID_CACHE_TTL", "86400s")

	// Redis Connection Pool
	viper.SetDefault("REDIS_POOL_SIZE", 20)
	viper.SetDefault("REDIS_MIN_IDLE_CONNECTIONS", 5)
	viper.SetDefault("REDIS_MAX_IDLE_CONNECTIONS", 10)
	viper.SetDefault("REDIS_CONNECTION_MAX_IDLE_TIME", 300) // 5 minutes
	viper.SetDefault("REDIS_CONNECTION_MAX_LIFETIME", 3600) // 1 hour
	viper.SetDefault("REDIS_DIAL_TIMEOUT", 5)               // 5 seconds
	viper.SetDefault("REDIS_READ_TIMEOUT", 3)               // 3 seconds
	viper.SetDefault("REDIS_WRITE_TIMEOUT", 3)              // 3 seconds

	// Google Cloud Rate Limiting
	viper.SetDefault("GOOGLE_API_RATE_LIMIT_ENABLED", false)
	viper.SetDefault("GOOGLE_API_MAX_REQUESTS_PER_MINUTE", 60)
	viper.SetDefault("GOOGLE_API_BACKOFF_MULTIPLIER", 2.0)
	viper.SetDefault("GOOGLE_API_MAX_BACKOFF_SECONDS", 300)
	viper.SetDefault("GOOGLE_API_MIN_BACKOFF_SECONDS", 1)

	// Google Agent Engine (very large timeout for complex reasoning tasks - 30 minutes)
	viper.SetDefault("GOOGLE_AGENT_ENGINE_REQUEST_TIMEOUT", "1800s")
	viper.SetDefault("GOOGLE_AGENT_ENGINE_MAX_MESSAGE_LENGTH", 32000)
	viper.SetDefault("GOOGLE_AGENT_ENGINE_MAX_RETRIES", 3)
	viper.SetDefault("GOOGLE_AGENT_ENGINE_RETRY_BACKOFF", "1s")

	// Audio Transcription
	viper.SetDefault("TRANSCRIBE_MAX_DURATION", 60)
	viper.SetDefault("TRANSCRIBE_MAX_DURATION_MINUTES", 10)
	viper.SetDefault("TRANSCRIBE_ALLOWED_URLS", "https://whatsapp.dados.rio/")
	viper.SetDefault("TRANSCRIBE_MAX_FILE_SIZE_MB", 25)
	viper.SetDefault("TRANSCRIBE_SUPPORTED_FORMATS", "mp3,wav,ogg,oga,webm,mp4,m4a,flac,opus")
	viper.SetDefault("TRANSCRIBE_TEMP_DIR", "/tmp")
	viper.SetDefault("TRANSCRIBE_CLEANUP_INTERVAL", "5m")
	viper.SetDefault("TRANSCRIBE_REQUEST_TIMEOUT", "60s")
	viper.SetDefault("TRANSCRIBE_DOWNLOAD_TIMEOUT", "30s")
	viper.SetDefault("TRANSCRIBE_LANGUAGE_CODE", "pt-BR")
	viper.SetDefault("TRANSCRIBE_SAMPLE_RATE_HERTZ", 16000)
	viper.SetDefault("TRANSCRIBE_ENABLE_WORD_TIME_OFFSETS", false)
	viper.SetDefault("TRANSCRIBE_ENABLE_WORD_CONFIDENCE", false)
	viper.SetDefault("TRANSCRIBE_MAX_ALTERNATIVES", 1)
	viper.SetDefault("TRANSCRIBE_PROFANITY_FILTER", false)

	// EAI Agent
	viper.SetDefault("EAI_AGENT_CONTEXT_WINDOW_LIMIT", 1000000)
	viper.SetDefault("EAI_AGENT_MAX_GOOGLE_SEARCH_PER_STEP", 1)

	// Observability
	viper.SetDefault("OTEL_ENABLED", false)
	viper.SetDefault("OTEL_COLLECTOR_URL", "http://localhost:4317")
	viper.SetDefault("OTEL_SERVICE_NAME", "eai-gateway")
	viper.SetDefault("OTEL_SERVICE_VERSION", "0.1.0")
	viper.SetDefault("OTEL_ENVIRONMENT", "development")
	viper.SetDefault("METRICS_ENABLED", true)
	viper.SetDefault("METRICS_PORT", 8080)
	viper.SetDefault("METRICS_PATH", "/metrics")
	viper.SetDefault("LOG_LEVEL", "info")
	viper.SetDefault("LOG_FORMAT", "json")
	viper.SetDefault("LOG_OUTPUT", "stdout")
	viper.SetDefault("HEALTH_CHECK_TIMEOUT", "10s")
	viper.SetDefault("READINESS_CHECK_TIMEOUT", "5s")

	// Security
	viper.SetDefault("CORS_ENABLED", true)
	viper.SetDefault("CORS_ORIGINS", "*")
	viper.SetDefault("MAX_REQUEST_SIZE", 10485760) // 10MB
	viper.SetDefault("RATE_LIMIT_ENABLED", false)
	viper.SetDefault("RATE_LIMIT_REQUESTS", 100)

	// Input Validation
	viper.SetDefault("MAX_CONTENT_LENGTH", 10000) // 10KB
	viper.SetDefault("MAX_USER_ID_LENGTH", 100)
	viper.SetDefault("MAX_AGENT_ID_LENGTH", 100)
	viper.SetDefault("ENABLE_CONTENT_FILTER", true)
	viper.SetDefault("SECURITY_ALLOWED_DOMAINS", "") // Empty = allow all
	viper.SetDefault("SECURITY_BLOCKED_DOMAINS", "localhost,127.0.0.1,0.0.0.0,192.168.,10.,172.")
	viper.SetDefault("SECURITY_STRICT_MODE", false)

	// Callback
	viper.SetDefault("CALLBACK_ENABLED", true)
	viper.SetDefault("CALLBACK_TIMEOUT", 10)    // 10 seconds
	viper.SetDefault("CALLBACK_MAX_RETRIES", 3) // 3 retry attempts
	viper.SetDefault("CALLBACK_ENABLE_HMAC", false)
	viper.SetDefault("CALLBACK_HMAC_SECRET", "")
	viper.SetDefault("CALLBACK_REQUIRE_HTTPS", true)
	viper.SetDefault("CALLBACK_ALLOWED_DOMAIN", "") // Empty = allow all
}

func validateRequired(config *Config) error {
	required := map[string]string{
		"APP_PREFIX":          config.AppPrefix,
		"RABBITMQ_URL":        config.RabbitMQ.URL,
		"REDIS_DSN":           config.Redis.DSN,
		"REDIS_BACKEND":       config.Redis.Backend,
		"REASONING_ENGINE_ID": config.GoogleCloud.ReasoningEngineID,
		"PROJECT_ID":          config.GoogleCloud.ProjectID,
		"PROJECT_NUMBER":      config.GoogleCloud.ProjectNumber,
		"LOCATION":            config.GoogleCloud.Location,
		"SERVICE_ACCOUNT":     config.GoogleCloud.ServiceAccount,
		"GCS_BUCKET":          config.GoogleCloud.GCSBucket,
		"EAI_AGENT_URL":       config.EAIAgent.URL,
		"EAI_AGENT_TOKEN":     config.EAIAgent.Token,
		"LLM_MODEL":           config.EAIAgent.LLMModel,
		"EMBEDDING_MODEL":     config.EAIAgent.EmbeddingModel,
	}

	var missing []string
	for key, value := range required {
		if strings.TrimSpace(value) == "" {
			missing = append(missing, key)
		}
	}

	if len(missing) > 0 {
		return fmt.Errorf("required environment variables missing: %s", strings.Join(missing, ", "))
	}

	return nil
}

// bindEnvironmentVariables explicitly binds environment variables to viper keys
// This is needed because viper.AutomaticEnv() doesn't work well with nested structs
func bindEnvironmentVariables() {
	// Core Application
	_ = viper.BindEnv("APP_PREFIX")
	_ = viper.BindEnv("MAX_PARALLEL")

	// Server
	_ = viper.BindEnv("SERVER_PORT")
	_ = viper.BindEnv("SERVER_HOST")
	_ = viper.BindEnv("SERVER_READ_TIMEOUT")
	_ = viper.BindEnv("SERVER_WRITE_TIMEOUT")
	_ = viper.BindEnv("SERVER_IDLE_TIMEOUT")

	// RabbitMQ
	_ = viper.BindEnv("RABBITMQ_URL")
	_ = viper.BindEnv("RABBITMQ_EXCHANGE")
	_ = viper.BindEnv("RABBITMQ_USER_QUEUE")
	_ = viper.BindEnv("RABBITMQ_AGENT_QUEUE")
	_ = viper.BindEnv("RABBITMQ_USER_MESSAGES_QUEUE")
	_ = viper.BindEnv("RABBITMQ_AGENT_MESSAGES_QUEUE")
	_ = viper.BindEnv("RABBITMQ_DLX_EXCHANGE")
	_ = viper.BindEnv("RABBITMQ_MAX_RETRIES")
	_ = viper.BindEnv("RABBITMQ_RETRY_DELAY")
	_ = viper.BindEnv("RABBITMQ_MESSAGE_TIMEOUT")
	_ = viper.BindEnv("CELERY_SOFT_TIME_LIMIT")
	_ = viper.BindEnv("CELERY_TIME_LIMIT")

	// Redis
	_ = viper.BindEnv("REDIS_DSN")
	_ = viper.BindEnv("REDIS_BACKEND")
	_ = viper.BindEnv("REDIS_TASK_RESULT_TTL")
	_ = viper.BindEnv("REDIS_TASK_STATUS_TTL")
	_ = viper.BindEnv("CACHE_TTL_SECONDS")
	_ = viper.BindEnv("AGENT_ID_CACHE_TTL")
	_ = viper.BindEnv("REDIS_POOL_SIZE")
	_ = viper.BindEnv("REDIS_MIN_IDLE_CONNECTIONS")
	_ = viper.BindEnv("REDIS_MAX_IDLE_CONNECTIONS")
	_ = viper.BindEnv("REDIS_CONNECTION_MAX_IDLE_TIME")
	_ = viper.BindEnv("REDIS_CONNECTION_MAX_LIFETIME")
	_ = viper.BindEnv("REDIS_DIAL_TIMEOUT")
	_ = viper.BindEnv("REDIS_READ_TIMEOUT")
	_ = viper.BindEnv("REDIS_WRITE_TIMEOUT")

	// Google Cloud
	_ = viper.BindEnv("PROJECT_ID")
	_ = viper.BindEnv("PROJECT_NUMBER")
	_ = viper.BindEnv("LOCATION")
	_ = viper.BindEnv("SERVICE_ACCOUNT")
	_ = viper.BindEnv("GCS_BUCKET")
	_ = viper.BindEnv("REASONING_ENGINE_ID")
	_ = viper.BindEnv("GOOGLE_API_RATE_LIMIT_ENABLED")
	_ = viper.BindEnv("GOOGLE_API_MAX_REQUESTS_PER_MINUTE")
	_ = viper.BindEnv("GOOGLE_API_BACKOFF_MULTIPLIER")
	_ = viper.BindEnv("GOOGLE_API_MAX_BACKOFF_SECONDS")
	_ = viper.BindEnv("GOOGLE_API_MIN_BACKOFF_SECONDS")

	// Google Agent Engine (reusing existing Google Cloud env vars)
	// PROJECT_ID, LOCATION, REASONING_ENGINE_ID, SERVICE_ACCOUNT already bound above
	_ = viper.BindEnv("GOOGLE_AGENT_ENGINE_CREDENTIALS_PATH")
	_ = viper.BindEnv("GOOGLE_AGENT_ENGINE_REQUEST_TIMEOUT")
	_ = viper.BindEnv("GOOGLE_AGENT_ENGINE_MAX_MESSAGE_LENGTH")
	_ = viper.BindEnv("GOOGLE_AGENT_ENGINE_MAX_RETRIES")
	_ = viper.BindEnv("GOOGLE_AGENT_ENGINE_RETRY_BACKOFF")

	// EAI Agent
	_ = viper.BindEnv("EAI_AGENT_URL")
	_ = viper.BindEnv("EAI_AGENT_TOKEN")
	_ = viper.BindEnv("EAI_AGENT_CONTEXT_WINDOW_LIMIT")
	_ = viper.BindEnv("EAI_AGENT_MAX_GOOGLE_SEARCH_PER_STEP")
	_ = viper.BindEnv("LLM_MODEL")
	_ = viper.BindEnv("EMBEDDING_MODEL")

	// Transcribe
	_ = viper.BindEnv("TRANSCRIBE_MAX_DURATION")
	_ = viper.BindEnv("TRANSCRIBE_MAX_DURATION_MINUTES")
	_ = viper.BindEnv("TRANSCRIBE_ALLOWED_URLS")
	_ = viper.BindEnv("TRANSCRIBE_MAX_FILE_SIZE_MB")
	_ = viper.BindEnv("TRANSCRIBE_SUPPORTED_FORMATS")
	_ = viper.BindEnv("TRANSCRIBE_TEMP_DIR")
	_ = viper.BindEnv("TRANSCRIBE_CLEANUP_INTERVAL")
	_ = viper.BindEnv("TRANSCRIBE_REQUEST_TIMEOUT")
	_ = viper.BindEnv("TRANSCRIBE_DOWNLOAD_TIMEOUT")
	_ = viper.BindEnv("TRANSCRIBE_LANGUAGE_CODE")
	_ = viper.BindEnv("TRANSCRIBE_SAMPLE_RATE_HERTZ")
	_ = viper.BindEnv("TRANSCRIBE_ENABLE_WORD_TIME_OFFSETS")
	_ = viper.BindEnv("TRANSCRIBE_ENABLE_WORD_CONFIDENCE")
	_ = viper.BindEnv("TRANSCRIBE_MAX_ALTERNATIVES")
	_ = viper.BindEnv("TRANSCRIBE_PROFANITY_FILTER")

	// Observability
	_ = viper.BindEnv("OTEL_ENABLED")
	_ = viper.BindEnv("OTEL_COLLECTOR_URL")
	_ = viper.BindEnv("OTEL_SERVICE_NAME")
	_ = viper.BindEnv("OTEL_SERVICE_VERSION")
	_ = viper.BindEnv("OTEL_ENVIRONMENT")
	_ = viper.BindEnv("METRICS_ENABLED")
	_ = viper.BindEnv("METRICS_PORT")
	_ = viper.BindEnv("METRICS_PATH")
	_ = viper.BindEnv("LOG_LEVEL")
	_ = viper.BindEnv("LOG_FORMAT")
	_ = viper.BindEnv("LOG_OUTPUT")
	_ = viper.BindEnv("HEALTH_CHECK_TIMEOUT")
	_ = viper.BindEnv("READINESS_CHECK_TIMEOUT")

	// Security
	_ = viper.BindEnv("CORS_ENABLED")
	_ = viper.BindEnv("CORS_ORIGINS")
	_ = viper.BindEnv("MAX_REQUEST_SIZE")
	_ = viper.BindEnv("RATE_LIMIT_ENABLED")
	_ = viper.BindEnv("RATE_LIMIT_REQUESTS")
	_ = viper.BindEnv("MAX_CONTENT_LENGTH")
	_ = viper.BindEnv("MAX_USER_ID_LENGTH")
	_ = viper.BindEnv("MAX_AGENT_ID_LENGTH")
	_ = viper.BindEnv("ENABLE_CONTENT_FILTER")
	_ = viper.BindEnv("SECURITY_STRICT_MODE")

	// Callback
	_ = viper.BindEnv("CALLBACK_ENABLED")
	_ = viper.BindEnv("CALLBACK_TIMEOUT")
	_ = viper.BindEnv("CALLBACK_MAX_RETRIES")
	_ = viper.BindEnv("CALLBACK_ENABLE_HMAC")
	_ = viper.BindEnv("CALLBACK_HMAC_SECRET")
	_ = viper.BindEnv("CALLBACK_REQUIRE_HTTPS")
	_ = viper.BindEnv("CALLBACK_ALLOWED_DOMAIN")
}

// GetLogLevel returns the logrus log level from config
func (c *Config) GetLogLevel() logrus.Level {
	level, err := logrus.ParseLevel(c.Observability.LogLevel)
	if err != nil {
		logrus.Warnf("Invalid log level %s, using info", c.Observability.LogLevel)
		return logrus.InfoLevel
	}
	return level
}

// GetCORSOrigins returns CORS origins as a slice
func (c *Config) GetCORSOrigins() []string {
	if c.Security.CORSOrigins == "*" {
		return []string{"*"}
	}
	return strings.Split(c.Security.CORSOrigins, ",")
}

// GetTranscribeAllowedDomains returns transcribe allowed domains as a slice
func (c *Config) GetTranscribeAllowedDomains() []string {
	return strings.Split(c.Transcribe.AllowedURLs, ",")
}

// GetTranscribeSupportedFormats returns transcribe supported formats as a slice
func (c *Config) GetTranscribeSupportedFormats() []string {
	return strings.Split(c.Transcribe.SupportedFormats, ",")
}

// GetSecurityAllowedDomains returns security allowed domains as a slice
func (c *Config) GetSecurityAllowedDomains() []string {
	if c.Security.AllowedDomains == "" {
		return []string{} // Empty = allow all
	}
	return strings.Split(c.Security.AllowedDomains, ",")
}

// GetSecurityBlockedDomains returns security blocked domains as a slice
func (c *Config) GetSecurityBlockedDomains() []string {
	if c.Security.BlockedDomains == "" {
		return []string{}
	}
	return strings.Split(c.Security.BlockedDomains, ",")
}
