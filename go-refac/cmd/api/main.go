package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	"eai-gateway-go/internal/config"
	"eai-gateway-go/internal/httpapi"
	"eai-gateway-go/internal/queue"
	"eai-gateway-go/internal/storage"
)

// main levanta o HTTP server (Gin) responsável por expor a API de webhooks e polling.
// Ele também inicializa conexões de infraestrutura compartilhadas (RabbitMQ e Redis)
// e injeta as dependências nos handlers HTTP.
func main() {
    zerolog.TimeFieldFormat = time.RFC3339
    cfg := config.Load()

    log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stderr})
    log.Info().Str("service", "eai-gateway-api").Msg("starting")

    // Redis
    redisStore, err := storage.NewRedisStore(cfg)
    if err != nil {
        log.Fatal().Err(err).Msg("failed to create redis store")
    }
    defer redisStore.Close()

    // RabbitMQ Publisher
    publisher, err := queue.NewPublisher(cfg)
    if err != nil {
        log.Fatal().Err(err).Msg("failed to initialize rabbit publisher")
    }
    defer publisher.Close()

    r := gin.New()
    r.Use(gin.Recovery())
    r.Use(httpapi.RequestLogger())

    // Health & metrics simples (pode ser substituído por Prometheus + OTel)
    r.GET("/health", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"status": "ok"}) })

    api := httpapi.NewHandler(cfg, redisStore, publisher)
    v1 := r.Group("/api/v1")
    api.RegisterRoutes(v1)

    srv := &http.Server{
        Addr:           fmt.Sprintf(":%d", cfg.HTTPPort),
        Handler:        r,
        ReadTimeout:    15 * time.Second,
        WriteTimeout:   30 * time.Second,
        IdleTimeout:    60 * time.Second,
        MaxHeaderBytes: 1 << 20,
    }

    go func() {
        log.Info().Int("port", cfg.HTTPPort).Msg("http server listening")
        if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
            log.Fatal().Err(err).Msg("http server crashed")
        }
    }()

    // Graceful shutdown
    stop := make(chan os.Signal, 1)
    signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
    <-stop

    ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
    defer cancel()
    if err := srv.Shutdown(ctx); err != nil {
        log.Error().Err(err).Msg("error during http shutdown")
    }
    log.Info().Msg("http server stopped")
}


