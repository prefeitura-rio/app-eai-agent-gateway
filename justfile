dev:
    granian --interface asgi --workers 4 --runtime-mode mt --task-impl rust src.main:app

# Executa diagnóstico de saúde dos serviços
health:
    uv run -m src.scripts.health_check

# Inicia worker Celery para desenvolvimento
worker:
    ENABLE_EVENTLET_PATCH=true uv run celery -A src.queue.celery_app.celery worker --pool=gevent --loglevel=info --concurrency=1000

# Monitor Celery Flower
flower:
    uv run celery -A src.queue.celery_app.celery flower --port=5555

# Limpa a fila do Redis/Celery
clear-queue:
    uv run python -c "from src.services.redis_service import sync_client; sync_client.flushdb(); print('Redis limpo!')"

# Testa conectividade com serviços
test-services:
    @echo "Testando Redis..."
    @uv run python -c "from src.services.redis_service import sync_client; sync_client.ping(); print('Redis OK')"
    @echo "Testando Letta..."
    @uv run python -c "import httpx; from src.config import env; print('Letta OK' if httpx.get(f'{env.LETTA_API_URL}/health', headers={'Authorization': f'Bearer {env.LETTA_API_TOKEN}'}, timeout=5).status_code == 200 else 'Letta ERRO')"

# Executa testes de carga com k6
load-test:
    k6 run load-tests/main.js

# Executa testes de carga com k6 e token personalizado
load-test-with-token token:
    BEARER_TOKEN={{token}} k6 run load-tests/main.js

# Inicia todos os serviços em desenvolvimento
dev-full:
    just worker &
    sleep 2
    just dev