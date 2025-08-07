package storage

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/redis/go-redis/v9"

	"eai-gateway-go/internal/config"
	"eai-gateway-go/pkg/schemas"
)

// RedisStore encapsula acesso ao Redis e o contrato de dados compatível com o Python.
type RedisStore struct {
    rdb     *redis.Client
    prefix  string
}

func NewRedisStore(cfg *config.Config) (*RedisStore, error) {
    opt, err := redis.ParseURL(cfg.RedisURL)
    if err != nil { return nil, err }
    rdb := redis.NewClient(opt)
    return &RedisStore{rdb: rdb, prefix: cfg.AppPrefix}, nil
}

func (s *RedisStore) Close() error { return s.rdb.Close() }

func (s *RedisStore) keyMessage(id string) string { return s.prefix + ":message:" + id }

// StoreProcessing grava status inicial para UX de polling.
func (s *RedisStore) StoreProcessing(ctx context.Context, messageID string, ttl time.Duration) error {
    tr := schemas.TaskResult{ Status: "processing", Message: "Sua mensagem está sendo processada. Tente novamente em alguns segundos." }
    return s.StoreResult(ctx, messageID, tr, ttl)
}

// StoreRetry grava status de retry com contadores.
func (s *RedisStore) StoreRetry(ctx context.Context, messageID string, attempt, max int, ttl time.Duration) error {
    tr := schemas.TaskResult{ Status: "retry", Message: "Sua mensagem está sendo processada.", RetryCount: attempt, MaxRetries: max }
    return s.StoreResult(ctx, messageID, tr, ttl)
}

// StoreError grava status de erro final.
func (s *RedisStore) StoreError(ctx context.Context, messageID, errMsg string, ttl time.Duration) error {
    tr := schemas.TaskResult{ Status: "error", Error: errMsg }
    return s.StoreResult(ctx, messageID, tr, ttl)
}

// StoreResult persiste o resultado final no mesmo formato lógico do Python.
func (s *RedisStore) StoreResult(ctx context.Context, messageID string, tr schemas.TaskResult, ttl time.Duration) error {
    b, _ := json.Marshal(tr)
    return s.rdb.Set(ctx, s.keyMessage(messageID), b, ttl).Err()
}

func (s *RedisStore) HasFinal(ctx context.Context, messageID string) (bool, error) {
    tr, found, err := s.GetResponse(ctx, messageID)
    if err != nil || !found { return false, err }
    return tr.Status == "done" || tr.Status == "error", nil
}

// GetResponse lê o resultado da mensagem; retorna false se chave ausente.
func (s *RedisStore) GetResponse(ctx context.Context, messageID string) (schemas.TaskResult, bool, error) {
    v, err := s.rdb.Get(ctx, s.keyMessage(messageID)).Bytes()
    if errors.Is(err, redis.Nil) {
        return schemas.TaskResult{}, false, nil
    }
    if err != nil { return schemas.TaskResult{}, false, err }
    var tr schemas.TaskResult
    if err := json.Unmarshal(v, &tr); err != nil { return schemas.TaskResult{}, false, err }
    return tr, true, nil
}


