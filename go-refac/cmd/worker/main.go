package main

import (
	"context"
	"encoding/json"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	"eai-gateway-go/internal/config"
	"eai-gateway-go/internal/providers"
	"eai-gateway-go/internal/queue"
	"eai-gateway-go/internal/storage"
	"eai-gateway-go/pkg/schemas"
)

// Worker consome mensagens do RabbitMQ, processa via provider (Letta/GAE) e
// grava o resultado final no Redis, garantindo idempotência.
func main() {
    zerolog.TimeFieldFormat = time.RFC3339
    log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stderr})

    cfg := config.Load()
    log.Info().Str("service", "eai-gateway-worker").Msg("starting")

    // Infra
    store, err := storage.NewRedisStore(cfg)
    if err != nil {
        log.Fatal().Err(err).Msg("failed to connect redis")
    }
    defer store.Close()

    consumer, err := queue.NewConsumer(cfg)
    if err != nil {
        log.Fatal().Err(err).Msg("failed to initialize consumer")
    }
    defer consumer.Close()

    factory := providers.NewFactory()

    // Handlers por provider (isola prefetch e escala por fila)
    ctx, cancel := context.WithCancel(context.Background())
    defer cancel()

    go consume(ctx, consumer, factory, cfg.RabbitQueueLetta, store)
    go consume(ctx, consumer, factory, cfg.RabbitQueueGAE, store)

    // Shutdown gracioso
    stop := make(chan os.Signal, 1)
    signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
    <-stop
    cancel()
    // dar tempo de finalizar
    time.Sleep(2 * time.Second)
}

func consume(ctx context.Context, c *queue.Consumer, f *providers.Factory, queueName string, store *storage.RedisStore) {
    log.Info().Str("queue", queueName).Msg("consuming")
    err := c.Consume(ctx, queueName, func(d queue.Delivery) error {
        // Detecta tipo de mensagem (UserMessage vs AgentMessage) sem envelope
        var probe map[string]any
        if err := json.Unmarshal(d.Body, &probe); err != nil {
            log.Error().Err(err).Msg("invalid payload")
            return d.Reject()
        }

        if _, ok := probe["agent_id"]; ok {
            // Fluxo: mensagem direta para agente Letta
            var msg schemas.AgentMessage
            if err := json.Unmarshal(d.Body, &msg); err != nil {
                log.Error().Err(err).Msg("invalid agent payload")
                return d.Reject()
            }
            if ok, _ := store.HasFinal(ctx, msg.MessageID); ok { return d.Ack() }

            p := f.Get("letta")
            if p == nil {
                _ = store.StoreError(ctx, msg.MessageID, "provider not available", cfgResultTTL())
                return d.Ack()
            }
            messages, usage, err := p.SendMessage(ctx, msg.AgentID, msg.Message, nil)
            if err != nil {
                // Reuso do caminho de retry com um UserMessage sintético (apenas para contagem)
                return handleRetry(ctx, store, d, schemas.UserMessage{MessageID: msg.MessageID, Attempt: msg.Attempt, MaxRetries: msg.MaxRetries}, err)
            }
            result := schemas.TaskResult{ Status: "done", Data: &schemas.ResultData{ Messages: messages, Usage: usage, AgentID: msg.AgentID, ProcessedAt: time.Now().UTC().Format(time.RFC3339) } }
            if err := store.StoreResult(ctx, msg.MessageID, result, cfgResultTTL()); err != nil {
                log.Error().Err(err).Msg("failed to store result")
            }
            return d.Ack()
        }

        // Fluxo: mensagem de usuário (UserMessage)
        var msg schemas.UserMessage
        if err := json.Unmarshal(d.Body, &msg); err != nil {
            log.Error().Err(err).Msg("invalid user payload")
            return d.Reject()
        }

        if ok, _ := store.HasFinal(ctx, msg.MessageID); ok { return d.Ack() }

        p := f.Get(msg.Provider)
        if p == nil {
            _ = store.StoreError(ctx, msg.MessageID, "unknown provider", cfgResultTTL())
            return d.Ack()
        }

        agentID, err := p.GetAgentID(ctx, msg.UserNumber)
        if err == nil && agentID == "" {
            agentID, err = p.CreateAgent(ctx, msg.UserNumber, nil)
        }
        if err != nil {
            return handleRetry(ctx, store, d, msg, err)
        }

        messages, usage, err := p.SendMessage(ctx, agentID, msg.Message, msg.PreviousMessage)
        if err != nil {
            return handleRetry(ctx, store, d, msg, err)
        }

        result := schemas.TaskResult{ Status: "done", Data: &schemas.ResultData{ Messages: messages, Usage: usage, AgentID: agentID, ProcessedAt: time.Now().UTC().Format(time.RFC3339) } }
        if err := store.StoreResult(ctx, msg.MessageID, result, cfgResultTTL()); err != nil {
            log.Error().Err(err).Msg("failed to store result")
        }
        return d.Ack()
    })
    if err != nil {
        log.Fatal().Err(err).Msg("consumer crashed")
    }
}

func cfgResultTTL() time.Duration { return 2 * time.Minute }

func handleRetry(ctx context.Context, store *storage.RedisStore, d queue.Delivery, msg schemas.UserMessage, err error) error {
    // Se excedeu tentativas, grava erro final no Redis
    attempt := msg.Attempt
    if attempt >= msg.MaxRetries {
        _ = store.StoreError(ctx, msg.MessageID, err.Error(), cfgResultTTL())
        return d.Ack()
    }

    // Marca status intermediário para UX
    _ = store.StoreRetry(ctx, msg.MessageID, attempt+1, msg.MaxRetries, cfgResultTTL())

    // Solicita reentrega (requeue) e deixa o atraso para política de fila de retry
    return d.Requeue()
}


