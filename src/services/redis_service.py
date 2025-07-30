from __future__ import annotations
import json
from typing import Any, Optional

import redis.asyncio as aioredis
import redis as sync_redis
from loguru import logger

from src.config import env
from src.config.telemetry import get_tracer

tracer = get_tracer("redis-service")

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
    with tracer.start_as_current_span("redis.store_response_sync") as span:
        span.set_attribute("redis.message_id", message_id)
        span.set_attribute("redis.ttl", ttl)
        span.set_attribute("redis.data_type", type(data).__name__)
        
        try:
            if not isinstance(data, (str, bytes)):
                data = json.dumps(data)
            sync_client.setex(_msg_key(message_id), ttl, data)
            span.set_attribute("redis.success", True)
        except Exception as e:
            span.record_exception(e)
            span.set_attribute("error", True)
            logger.error(f"Error storing response sync for {message_id}: {e}")
            raise

def get_response_sync(message_id: str) -> Optional[str]:
    with tracer.start_as_current_span("redis.get_response_sync") as span:
        span.set_attribute("redis.message_id", message_id)
        
        try:
            result = sync_client.get(_msg_key(message_id))
            span.set_attribute("redis.found", result is not None)
            return result
        except Exception as e:
            span.record_exception(e)
            span.set_attribute("error", True)
            logger.error(f"Error getting response sync for {message_id}: {e}")
            return None


# ---------- Async API ----------
async def store_response_async(message_id: str, data: Any, ttl: int = TTL_SECONDS) -> None:
    with tracer.start_as_current_span("redis.store_response_async") as span:
        span.set_attribute("redis.message_id", message_id)
        span.set_attribute("redis.ttl", ttl)
        span.set_attribute("redis.data_type", type(data).__name__)
        
        try:
            if not isinstance(data, (str, bytes)):
                data = json.dumps(data)
            await async_client.setex(_msg_key(message_id), ttl, data)
            span.set_attribute("redis.success", True)
        except Exception as e:
            span.record_exception(e)
            span.set_attribute("error", True)
            logger.error(f"Error storing response async for {message_id}: {e}")
            raise

async def get_response_async(message_id: str) -> Optional[str]:
    with tracer.start_as_current_span("redis.get_response_async") as span:
        span.set_attribute("redis.message_id", message_id)
        
        try:
            result = await async_client.get(_msg_key(message_id))
            span.set_attribute("redis.found", result is not None)
            return result
        except Exception as e:
            span.record_exception(e)
            span.set_attribute("error", True)
            logger.error(f"Error getting response async for {message_id}: {e}")
            return None


async def store_string_cache_async(cache_key: str, data: str, ttl: int = TTL_SECONDS) -> None:
    with tracer.start_as_current_span("redis.store_string_cache") as span:
        span.set_attribute("redis.cache_key", cache_key)
        span.set_attribute("redis.ttl", ttl)
        span.set_attribute("redis.data_length", len(data))
        
        try:
            await async_client.setex(_cache_key(cache_key), ttl, data)
            span.set_attribute("redis.success", True)
            logger.debug(f"Cached string data for key: {_cache_key(cache_key)}")
        except Exception as e:
            span.record_exception(e)
            span.set_attribute("error", True)
            logger.error(f"Error storing string cache for key {cache_key}: {e}")
            raise

async def get_string_cache_async(cache_key: str) -> Optional[str]:
    with tracer.start_as_current_span("redis.get_string_cache") as span:
        span.set_attribute("redis.cache_key", cache_key)
        
        try:
            result = await async_client.get(_cache_key(cache_key))
            span.set_attribute("redis.found", result is not None)
            if result:
                span.set_attribute("redis.data_length", len(result))
                logger.debug(f"Cache hit for string key: {_cache_key(cache_key)}")
            return result
        except Exception as e:
            span.record_exception(e)
            span.set_attribute("error", True)
            logger.error(f"Error getting string cache for key {cache_key}: {e}")
            return None

async def store_json_cache_async(cache_key: str, data: dict, ttl: int = TTL_SECONDS) -> None:
    with tracer.start_as_current_span("redis.store_json_cache") as span:
        span.set_attribute("redis.cache_key", cache_key)
        span.set_attribute("redis.ttl", ttl)
        span.set_attribute("redis.data_keys", list(data.keys()))
        
        try:
            json_data = json.dumps(data)
            await async_client.setex(_cache_key(cache_key), ttl, json_data)
            span.set_attribute("redis.success", True)
            logger.debug(f"Cached JSON data for key: {_cache_key(cache_key)}")
        except (TypeError, ValueError, Exception) as e:
            span.record_exception(e)
            span.set_attribute("error", True)
            logger.error(f"Error storing JSON cache for key {cache_key}: {e}")
            raise

async def get_json_cache_async(cache_key: str) -> Optional[dict]:
    with tracer.start_as_current_span("redis.get_json_cache") as span:
        span.set_attribute("redis.cache_key", cache_key)
        
        try:
            result = await async_client.get(_cache_key(cache_key))
            if not result:
                span.set_attribute("redis.found", False)
                return None
            try:
                data = json.loads(result)
                span.set_attribute("redis.found", True)
                span.set_attribute("redis.data_keys", list(data.keys()))
                logger.debug(f"Cache hit for JSON key: {_cache_key(cache_key)}")
                return data
            except json.JSONDecodeError:
                span.set_attribute("redis.json_decode_error", True)
                logger.warning(f"Invalid JSON in cache for key {cache_key}, removing corrupted data")
                await async_client.delete(_cache_key(cache_key))
                return None
        except Exception as e:
            span.record_exception(e)
            span.set_attribute("error", True)
            logger.error(f"Error getting JSON cache for key {cache_key}: {e}")
            return None


# ---------- shutdown ----------
async def close_async():
    with tracer.start_as_current_span("redis.close_async") as span:
        try:
            await async_client.close()
            span.set_attribute("redis.success", True)
        except Exception as e:
            span.record_exception(e)
            span.set_attribute("error", True)
            logger.error(f"Error closing async redis client: {e}")

def close_sync():
    with tracer.start_as_current_span("redis.close_sync") as span:
        try:
            sync_client.close()
            span.set_attribute("redis.success", True)
        except Exception as e:
            span.record_exception(e)
            span.set_attribute("error", True)
            logger.error(f"Error closing sync redis client: {e}")
