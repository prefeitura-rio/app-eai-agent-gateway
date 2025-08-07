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


