## EAí Gateway (Go Refactor)

Este diretório contém o refactor completo do gateway Python (FastAPI + Celery + Redis) para Go usando Gin + RabbitMQ + Redis, mantendo contratos de API e o modelo de polling de status.

### Visão Geral

- API HTTP (Gin) expõe rotas compatíveis em `/api/v1/`:
  - `POST /api/v1/message/webhook/user`
  - `POST /api/v1/message/webhook/agent`
  - `GET  /api/v1/message/response?message_id=<uuid>`
- Mensagens são publicadas no RabbitMQ (exchange `messages`) roteadas por provider:
  - `provider.letta` → `queue.letta`
  - `provider.gae`   → `queue.gae`
- Workers Go consomem filas, chamam providers (mock por padrão) e gravam resultados no Redis no mesmo formato do Python.
- Redis mantém status e resposta final: `processing`, `retry`, `error`, `done`.

### Estrutura

```
go-refac/
  cmd/
    api/main.go        # servidor HTTP
    worker/main.go     # consumidor RabbitMQ
  internal/
    config/            # leitura de env
    httpapi/           # handlers e middlewares
    queue/             # publisher, consumer e topologia
    providers/         # interfaces + mock (substituir por Letta/GAE reais)
    storage/           # Redis store com contrato compatível
  pkg/
    schemas/           # tipos compartilhados (payloads, mensagens e respostas)
  go.mod
  README.md
```

### Variáveis de Ambiente

- `HTTP_PORT` (default: 8080)
- `APP_PREFIX` (ex: `eai-gateway`)
- `REDIS_DSN` (ex: `redis://:password@localhost:6379/0`)
- `RABBITMQ_URL` (ex: `amqp://guest:guest@localhost:5672/`)
- `RABBIT_EXCHANGE` (default: `messages`)
- `RABBIT_QUEUE_LETTA` (default: `queue.letta`)
- `RABBIT_QUEUE_GAE` (default: `queue.gae`)
- `RABBIT_PREFETCH` (default: 8)

Para providers reais (Letta/GAE), adicione as respectivas variáveis (URL/token, projeto, localização, etc.).

### Executando Localmente

1) Suba Redis e RabbitMQ (via docker-compose do seu projeto ou local):

```bash
export APP_PREFIX=eai-gateway
export REDIS_DSN=redis://localhost:6379/0
export RABBITMQ_URL=amqp://guest:guest@localhost:5672/
```

2) Inicie o API:

```bash
cd go-refac
go run ./cmd/api
```

3) Inicie o Worker (em outro terminal):

```bash
cd go-refac
go run ./cmd/worker
```

### Rotas

- POST `/api/v1/message/webhook/user`

```json
{
  "user_number": "5511999999999",
  "message": "Olá",
  "previous_message": null,
  "provider": "google_agent_engine"
}
```
Resposta 201:
```json
{ "message_id": "<uuid>", "status": "processing", "polling_endpoint": "/api/v1/message/response?message_id=<uuid>" }
```

- POST `/api/v1/message/webhook/agent`

```json
{ "agent_id": "agent-123", "message": "Olá" }
```

- GET `/api/v1/message/response?message_id=<uuid>`
  - 202 `{"status":"processing", ...}`
  - 200 `{"status":"completed","data":{...}}`
  - 500 `{"status":"failed", ...}`
  - 404 quando ausente/expirado

### Semântica de Processamento

- O API publica uma mensagem no RabbitMQ e grava `processing` no Redis.
- O Worker consome, resolve `agentID` (ou `thread_id`), envia ao provider, e grava `done` no Redis com `messages`, `usage`, `agent_id`, `processed_at`.
- Em erro transitório, o Worker grava `retry` e solicita requeue. Ao exceder tentativas, grava `error` e `ack` definitivo.

### Substituindo Mocks por Providers Reais

- Implemente `internal/providers/letta` e `internal/providers/gae` com `Provider`.
- Para Letta, use `net/http` com timeouts e autenticação via header.
- Para Google Agent Engine (LangGraph/Vertex AI), se não houver SDK Go apropriado, você pode manter um microserviço fino em Python e integrar via HTTP/gRPC.
- Atualize `NewFactory` para retornar implementações reais.

### Observabilidade

- O esqueleto inclui logs estruturados. É recomendável adicionar OpenTelemetry (otelgin, otel para AMQP) e Prometheus conforme necessário.

### Escalabilidade

- Escale horizontalmente o Worker (réplicas) e use `RABBIT_PREFETCH` para backpressure.
- Separe filas por provider para isolar throughput.
- Adicione DLQs e políticas de retry com backoff no RabbitMQ conforme seu SLO.

### Compatibilidade

- Contratos de resposta e chaves do Redis mantêm a forma do Python, minimizando impacto nos clientes.


### Como o fluxo funciona (end-to-end)

1) API recebe `POST /api/v1/message/webhook/user` ou `.../agent`.
2) API gera `message_id`, grava `processing` no Redis (`APP_PREFIX:message:<message_id>`) e publica a mensagem no RabbitMQ.
3) Worker consome da fila por provider (`queue.letta` ou `queue.gae`).
4) Worker resolve/obtém `agent_id` (ou `thread_id`), chama o provider e grava no Redis o resultado `done` com `messages`, `usage`, `agent_id`, `processed_at`.
5) Cliente usa `GET /api/v1/message/response?message_id=...` para acompanhar: `processing` (202), `failed` (500) ou `completed` (200).

### Próximos passos para usar providers reais

- **Letta**
  - Crie `internal/providers/letta/{client.go,service.go}` implementando `Provider`.
  - Use `net/http` com `context.Context` e timeouts; autentique com `Authorization: Bearer <token>`.
  - Converta a resposta para `[]schemas.Message` e `schemas.Usage` (campos: `input_tokens`, `output_tokens`, `total_tokens`).
  - Variáveis recomendadas:
    - `LETTA_API_URL`, `LETTA_API_TOKEN`
    - `LETTA_TIMEOUT_SECONDS` (opcional)
  - Onde plugar: ajuste `NewFactory()` em `internal/providers/factory.go` para `letta: letta.New(...)`.

- **Google Agent Engine (LangGraph/Vertex AI)**
  - Opção A: implementar `internal/providers/gae/...` chamando o endpoint/SDK oficial se disponível em Go.
  - Opção B (recomendada inicialmente): expor seu serviço Python atual como microserviço HTTP fino e chamar via Go.
  - Variáveis típicas:
    - `PROJECT_ID`, `PROJECT_NUMBER`, `LOCATION`, `SERVICE_ACCOUNT`
    - `REASONING_ENGINE_ID`, `GCS_BUCKET_STAGING`
  - Onde plugar: ajuste `NewFactory()` para `gae: gae.New(...)`.

### O que alterar no código

- `internal/providers/factory.go`: substituir `NewFactory()` (que hoje retorna mocks) pelas implementações reais.
- `internal/providers/*`: criar pacotes `letta` e/ou `gae` implementando a interface `Provider` de `internal/providers/provider.go`.
- `internal/queue/rabbitmq.go`: se for usar DLQ/Retry com backoff, estender `declareTopology` conforme seção abaixo.
- `internal/storage/redis_store.go`: caso deseje mudar TTLs ou mensagens padrão, ajuste `StoreProcessing/StoreRetry`.

### Topologia RabbitMQ (com DLQ e backoff)

- Exchange principal: `messages` (topic)
- Exchange DLX: `messages.dlx` (topic)
- Filas por provider: `queue.letta`, `queue.gae`
- Filas de retry: `queue.letta.retry.5s`, `queue.letta.retry.30s`, `queue.gae.retry.5s`, ... (com `x-message-ttl`)
- Filas DLQ: `queue.letta.dlq`, `queue.gae.dlq`
- Bindings:
  - `provider.letta` → `queue.letta`
  - `provider.gae` → `queue.gae`
  - Em caso de erro final, `Reject(false)` direciona para DLQ (via `x-dead-letter-exchange` configurado na fila)
  - Em caso de retry, publique/redirecione para a fila `.retry.Xs` com TTL adequado
- No worker atual usamos `Requeue()` para um retry simples; ao introduzir filas de retry, mude a estratégia para republicar em `.retry` e `Ack` a mensagem original.

### Variáveis adicionais sugeridas

- `RESULT_TTL_SECONDS` e `STATUS_TTL_SECONDS` para customizar a retenção no Redis.
- `WORKER_PREFETCH` (já mapeado como `RABBIT_PREFETCH`).
- Para providers, guarde credenciais via secrets (K8s/Infisical/Vault) e injete como envs.

### Deploy e operação

- São dois processos: `cmd/api` (HTTP) e `cmd/worker` (consumidor). Escale independentemente.
- API pede CPU baixa/latência; Worker demanda mais I/O/CPU. Ajuste `RABBIT_PREFETCH` para controlar concorrência efetiva por instância.
- Adicione readiness/liveness probes. Exponha `/health` do API.

### Observabilidade (recomendado)

- Adicionar OpenTelemetry (tracing) no Gin e no client AMQP.
- Expor métricas Prometheus de HTTP e de processamento (sucesso/falha/latência/consumo).

### Testes locais de ponta a ponta

1) Suba Redis e RabbitMQ.
2) `go run ./cmd/worker` e `go run ./cmd/api`.
3) `POST /api/v1/message/webhook/user` e faça `GET /api/v1/message/response?...` até ver `completed`.
4) Troque os mocks pelo provider real e repita.

