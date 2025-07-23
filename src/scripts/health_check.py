import asyncio
import httpx
from loguru import logger
from src.config import env
from src.services.redis_service import async_client, sync_client

async def check_redis():
    """Verifica conectividade com Redis"""
    try:
        # Testa cliente síncrono
        sync_client.ping()
        logger.info("✅ Redis (sync): Conectado")
        
        # Testa cliente assíncrono
        await async_client.ping()
        logger.info("✅ Redis (async): Conectado")
        
        # Verifica estatísticas
        info = sync_client.info()
        logger.info(f"Redis versão: {info.get('redis_version')}")
        logger.info(f"Conexões ativas: {info.get('connected_clients')}")
        
        return True
    except Exception as e:
        logger.error(f"❌ Redis: Erro de conexão - {e}")
        return False

async def check_letta_api():
    """Verifica conectividade com a API Letta"""
    try:
        async with httpx.AsyncClient() as client:
            response = await client.get(
                f"{env.LETTA_API_URL}v1/health", 
            )
            if response.status_code == 200 or response.status_code == 307:
                logger.info("✅ Letta API: Conectada e saudável")
                return True
            else:
                logger.warning(f"⚠️ Letta API: Status {response.status_code}")
                return False
    except Exception as e:
        logger.error(f"❌ Letta API: Erro de conexão - {e}")
        return False

def check_celery_workers():
    """Verifica o status dos workers Celery"""
    try:
        from src.queue.celery_app import celery
        
        # Inspeciona workers ativos
        inspect = celery.control.inspect()
        
        # Verifica workers ativos
        active_workers = inspect.active()
        if active_workers:
            logger.info(f"✅ Celery Workers: {len(active_workers)} worker(s) ativo(s)")
            for worker_name, tasks in active_workers.items():
                logger.info(f"  - {worker_name}: {len(tasks)} task(s) ativa(s)")
            return True
        else:
            logger.error("❌ Celery Workers: Nenhum worker ativo encontrado")
            return False
            
    except Exception as e:
        logger.error(f"❌ Celery Workers: Erro ao verificar - {e}")
        return False

def check_celery_queue():
    """Verifica a fila do Celery"""
    try:
        from src.queue.celery_app import celery
        
        # Verifica tamanho da fila
        inspect = celery.control.inspect()
        reserved = inspect.reserved()
        
        if reserved:
            total_reserved = sum(len(tasks) for tasks in reserved.values())
            logger.info(f"📋 Celery Queue: {total_reserved} task(s) na fila")
        else:
            logger.info("📋 Celery Queue: Vazia")
            
        return True
    except Exception as e:
        logger.error(f"❌ Celery Queue: Erro ao verificar - {e}")
        return False

async def run_health_check():
    """Executa todas as verificações de saúde"""
    logger.info("🔍 Iniciando diagnóstico do sistema...")
    logger.info("=" * 50)
    
    # Verifica Redis
    redis_ok = await check_redis()
    
    # Verifica Letta API
    letta_ok = await check_letta_api()
    
    # Verifica Celery Workers
    workers_ok = check_celery_workers()
    
    # Verifica fila do Celery
    queue_ok = check_celery_queue()
    
    # Resumo
    logger.info("=" * 50)
    logger.info("📊 RESUMO DO DIAGNÓSTICO:")
    
    if redis_ok and letta_ok and workers_ok:
        logger.info("✅ Todos os serviços estão funcionais")
    else:
        logger.error("❌ Problemas detectados:")
        if not redis_ok:
            logger.error("  - Redis não está acessível")
        if not letta_ok:
            logger.error("  - Letta API não está acessível")
        if not workers_ok:
            logger.error("  - Workers Celery não estão ativos")
            
    logger.info("=" * 50)
    logger.info("💡 DICAS PARA RESOLUÇÃO:")
    
    if not redis_ok:
        logger.info("Redis:")
        logger.info("  - Verifique se o Redis está rodando: docker-compose up redis")
        logger.info("  - Verifique as variáveis REDIS_DSN e REDIS_BACKEND no .env")
    
    if not letta_ok:
        logger.info("Letta API:")
        logger.info("  - Verifique se o serviço Letta está rodando")
        logger.info("  - Verifique as variáveis LETTA_API_URL e LETTA_API_TOKEN no .env")
        logger.info("  - Teste manualmente: curl -H 'Authorization: Bearer TOKEN' URL/health")
    
    if not workers_ok:
        logger.info("Celery Workers:")
        logger.info("  - Inicie o worker: docker-compose up worker")
        logger.info("  - Ou manualmente: uv run celery -A src.queue.celery_app.celery worker --loglevel=info")

if __name__ == "__main__":
    asyncio.run(run_health_check()) 