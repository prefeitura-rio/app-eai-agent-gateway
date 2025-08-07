package config

import (
	"log"
	"os"
	"strconv"
)

// Config centraliza variáveis de ambiente usadas pelo API e Worker.
type Config struct {
    HTTPPort int

    // Redis
    RedisURL   string
    AppPrefix  string

    // Rabbit
    RabbitURL        string
    RabbitExchange   string
    RabbitQueueLetta string
    RabbitQueueGAE   string
    RabbitPrefetch   int

    // Observabilidade (placeholder para evolução)
    OtelEnabled bool
}

func Load() *Config {
    cfg := &Config{
        HTTPPort:        intFromEnv("HTTP_PORT", 8080),
        RedisURL:        mustEnv("REDIS_DSN"),
        AppPrefix:       mustEnv("APP_PREFIX"),
        RabbitURL:       mustEnv("RABBITMQ_URL"),
        RabbitExchange:  getEnv("RABBIT_EXCHANGE", "messages"),
        RabbitQueueLetta: getEnv("RABBIT_QUEUE_LETTA", "queue.letta"),
        RabbitQueueGAE:   getEnv("RABBIT_QUEUE_GAE", "queue.gae"),
        RabbitPrefetch:  intFromEnv("RABBIT_PREFETCH", 8),
        OtelEnabled:     boolFromEnv("OTEL_ENABLED", false),
    }
    return cfg
}

func getEnv(key, def string) string {
    if v := os.Getenv(key); v != "" {
        return v
    }
    return def
}

func mustEnv(key string) string {
    v := os.Getenv(key)
    if v == "" {
        log.Fatalf("missing required env %s", key)
    }
    return v
}

func intFromEnv(key string, def int) int {
    v := os.Getenv(key)
    if v == "" {
        return def
    }
    i, err := strconv.Atoi(v)
    if err != nil {
        return def
    }
    return i
}

func boolFromEnv(key string, def bool) bool {
    v := os.Getenv(key)
    if v == "" {
        return def
    }
    switch v {
    case "1", "true", "TRUE", "True":
        return true
    case "0", "false", "FALSE", "False":
        return false
    default:
        return def
    }
}


