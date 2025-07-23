from __future__ import annotations
import redis.asyncio as aioredis
import redis as sync_redis
from src.config import env

async_client: aioredis.Redis = aioredis.from_url(
    env.REDIS_BACKEND, decode_responses=True
)

sync_client: sync_redis.Redis = sync_redis.from_url(
    env.REDIS_BACKEND, decode_responses=True
)

TTL_SECONDS = env.REDIS_TTL

async def store_response_async(message_id: str, data: dict, ttl: int = TTL_SECONDS):
    await async_client.setex(message_id, ttl, data)

def store_response_sync(message_id: str, data: str | bytes, ttl: int = TTL_SECONDS):
    """Usado pelos workers Celery (bloco síncrono)."""
    sync_client.setex(message_id, ttl, data)

async def get_response_async(message_id: str) -> str | None:
    return await async_client.get(message_id)
