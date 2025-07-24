from __future__ import annotations
import json
from typing import Any, Optional

import redis.asyncio as aioredis
import redis as sync_redis
from loguru import logger

from src.config import env

def _msg_key(message_id: str) -> str:
    return f"{env.APP_PREFIX}:message:{message_id}"

def _cache_key(key: str) -> str:
    return f"{env.APP_PREFIX}:cache:{key}"

TTL_SECONDS: int = int(env.REDIS_TTL or 60)

async_client: aioredis.Redis = aioredis.from_url(
    env.REDIS_BACKEND, decode_responses=True
)

sync_client: sync_redis.Redis = sync_redis.from_url(
    env.REDIS_BACKEND, decode_responses=True
)


# ---------- Sync API (Celery workers) ----------
def store_response_sync(message_id: str, data: Any, ttl: int = TTL_SECONDS) -> None:
    """
    Usado pelos workers Celery (contexto síncrono).
    Aceita dict e faz json.dumps internamente.
    """
    try:
        if not isinstance(data, (str, bytes)):
            data = json.dumps(data)
        sync_client.setex(_msg_key(message_id), ttl, data)
    except Exception as e:
        logger.error(f"Error storing response sync for {message_id}: {e}")

def get_response_sync(message_id: str) -> Optional[str]:
    try:
        return sync_client.get(_msg_key(message_id))
    except Exception as e:
        logger.error(f"Error getting response sync for {message_id}: {e}")
        return None


# ---------- Async API ----------
async def store_response_async(message_id: str, data: Any, ttl: int = TTL_SECONDS) -> None:
    try:
        if not isinstance(data, (str, bytes)):
            data = json.dumps(data)
        await async_client.setex(_msg_key(message_id), ttl, data)
    except Exception as e:
        logger.error(f"Error storing response async for {message_id}: {e}")

async def get_response_async(message_id: str) -> Optional[str]:
    try:
        return await async_client.get(_msg_key(message_id))
    except Exception as e:
        logger.error(f"Error getting response async for {message_id}: {e}")
        return None


async def store_string_cache_async(cache_key: str, data: str, ttl: int = TTL_SECONDS) -> None:
    try:
        await async_client.setex(_cache_key(cache_key), ttl, data)
        logger.debug(f"Cached string data for key: {_cache_key(cache_key)}")
    except Exception as e:
        logger.error(f"Error storing string cache for key {cache_key}: {e}")

async def get_string_cache_async(cache_key: str) -> Optional[str]:
    try:
        result = await async_client.get(_cache_key(cache_key))
        if result:
            logger.debug(f"Cache hit for string key: {_cache_key(cache_key)}")
        return result
    except Exception as e:
        logger.error(f"Error getting string cache for key {cache_key}: {e}")
        return None

async def store_json_cache_async(cache_key: str, data: dict, ttl: int = TTL_SECONDS) -> None:
    try:
        json_data = json.dumps(data)
        await async_client.setex(_cache_key(cache_key), ttl, json_data)
        logger.debug(f"Cached JSON data for key: {_cache_key(cache_key)}")
    except (TypeError, ValueError, Exception) as e:
        logger.error(f"Error storing JSON cache for key {cache_key}: {e}")

async def get_json_cache_async(cache_key: str) -> Optional[dict]:
    try:
        result = await async_client.get(_cache_key(cache_key))
        if not result:
            return None
        try:
            data = json.loads(result)
            logger.debug(f"Cache hit for JSON key: {_cache_key(cache_key)}")
            return data
        except json.JSONDecodeError:
            logger.warning(f"Invalid JSON in cache for key {cache_key}, removing corrupted data")
            await async_client.delete(_cache_key(cache_key))
            return None
    except Exception as e:
        logger.error(f"Error getting JSON cache for key {cache_key}: {e}")
        return None


# ---------- shutdown ----------
async def close_async():
    try:
        await async_client.close()
    except Exception as e:
        logger.error(f"Error closing async redis client: {e}")

def close_sync():
    try:
        sync_client.close()
    except Exception as e:
        logger.error(f"Error closing sync redis client: {e}")
