from loguru import logger
import httpx
import json

from src.config import env
from src.config.telemetry import get_tracer
from src.constants.fallbacks import SYSTEM_PROMPT_FALLBACK, MEMORY_BLOCKS_FALLBACK
from src.services.redis_service import (
    store_string_cache_async, 
    get_string_cache_async,
    store_json_cache_async, 
    get_json_cache_async
)

tracer = get_tracer("external-dependencies")

CACHE_TTL_SECONDS = 420  # 7 minutos

async def get_system_prompt_from_api(agent_type: str = "agentic_search") -> str:
    """Obtém o system prompt via API"""
    with tracer.start_as_current_span("external.get_system_prompt") as span:
        span.set_attribute("external.agent_type", agent_type)
        
        cache_key = f"system_prompt:{agent_type}"
        
        cached_result = await get_string_cache_async(cache_key)
        if cached_result:
            span.set_attribute("external.cache_hit", True)
            return cached_result
        
        span.set_attribute("external.cache_hit", False)
        
        base_url = getattr(env, "EAI_AGENT_URL", "http://localhost:8000")
        api_url = f"{base_url}system-prompt?agent_type={agent_type}"
        bearer_token = getattr(env, "EAI_AGENT_TOKEN", "")

        headers = {}
        
        if bearer_token:
            headers["Authorization"] = f"Bearer {bearer_token}"

        span.set_attribute("external.api_url", api_url)
        span.set_attribute("external.has_auth", bool(bearer_token))

        for attempt in range(3):
            span.set_attribute("external.attempt", attempt + 1)
            
            try:
                async with httpx.AsyncClient(timeout=10.0) as client:
                    response = await client.get(api_url, headers=headers)
                    
                    span.set_attribute("external.http_status_code", response.status_code)
                    
                    if 500 <= response.status_code <= 599 and attempt < 2:
                        span.set_attribute("external.server_error", True)
                        logger.warning(f"Server error {response.status_code} on attempt {attempt + 1}/3")
                        continue
                    
                    response.raise_for_status()
                    data = response.json()
                    
                    prompt = data.get("prompt")
                    if prompt:
                        await store_string_cache_async(cache_key, prompt, CACHE_TTL_SECONDS)
                        span.set_attribute("external.success", True)
                        span.set_attribute("external.prompt_length", len(prompt))
                    
                    return prompt

            except httpx.HTTPStatusError as e:
                span.record_exception(e)
                span.set_attribute("error", True)
                span.set_attribute("external.http_error", True)
                span.set_attribute("external.http_status_code", e.response.status_code)
                
                if 500 <= e.response.status_code <= 599 and attempt < 2:
                    span.set_attribute("external.server_error", True)
                    logger.warning(f"Server error {e.response.status_code} on attempt {attempt + 1}/3")
                    continue
                logger.warning(
                    f"Error getting system prompt from API: {str(e)}. Using fallback."
                )
                span.set_attribute("external.using_fallback", True)
                return SYSTEM_PROMPT_FALLBACK.format(agent_type=agent_type)
            except Exception as e:
                span.record_exception(e)
                span.set_attribute("error", True)
                span.set_attribute("external.request_error", True)
                
                if attempt < 2:
                    logger.warning(f"Request error on attempt {attempt + 1}/3: {str(e)}")
                    continue
                logger.warning(
                    f"Error getting system prompt from API: {str(e)}. Using fallback."
                )
                span.set_attribute("external.using_fallback", True)
                return SYSTEM_PROMPT_FALLBACK.format(agent_type=agent_type)

async def get_agent_config_from_api(agent_type: str = "agentic_search") -> dict:
    """Obtém a configuração do agente via API"""
    with tracer.start_as_current_span("external.get_agent_config") as span:
        span.set_attribute("external.agent_type", agent_type)
        
        cache_key = f"agent_config:{agent_type}"
        
        cached_result = await get_json_cache_async(cache_key)
        if cached_result:
            span.set_attribute("external.cache_hit", True)
            span.set_attribute("external.config_keys", list(cached_result.keys()))
            return cached_result
        
        span.set_attribute("external.cache_hit", False)
        
        base_url = getattr(env, "EAI_AGENT_URL", "http://localhost:8000")
        api_url = f"{base_url}agent-config?agent_type={agent_type}"
        bearer_token = getattr(env, "EAI_AGENT_TOKEN", "")

        headers = {}
        if bearer_token:
            headers["Authorization"] = f"Bearer {bearer_token}"

        span.set_attribute("external.api_url", api_url)
        span.set_attribute("external.has_auth", bool(bearer_token))

        for attempt in range(3):
            span.set_attribute("external.attempt", attempt + 1)
            
            try:
                async with httpx.AsyncClient(timeout=10.0) as client:
                    response = await client.get(api_url, headers=headers)
                    
                    span.set_attribute("external.http_status_code", response.status_code)
                    
                    if 500 <= response.status_code <= 599 and attempt < 2:
                        span.set_attribute("external.server_error", True)
                        logger.warning(f"Server error {response.status_code} on attempt {attempt + 1}/3")
                        continue
                    
                    response.raise_for_status()
                    data = response.json()

                    await store_json_cache_async(cache_key, data, CACHE_TTL_SECONDS)
                    
                    span.set_attribute("external.success", True)
                    span.set_attribute("external.config_keys", list(data.keys()))
                    
                    return data

            except httpx.HTTPStatusError as e:
                span.record_exception(e)
                span.set_attribute("error", True)
                span.set_attribute("external.http_error", True)
                span.set_attribute("external.http_status_code", e.response.status_code)
                
                if 500 <= e.response.status_code <= 599 and attempt < 2:
                    span.set_attribute("external.server_error", True)
                    logger.warning(f"Server error {e.response.status_code} on attempt {attempt + 1}/3")
                    continue
                logger.warning(
                    f"Error getting agent config from API: {str(e)}. Using fallback."
                )
                span.set_attribute("external.using_fallback", True)
                return {
                    "memory_blocks": MEMORY_BLOCKS_FALLBACK,
                    "tools": [],
                    "model_name": env.LLM_MODEL,
                    "embedding_name": env.EMBEDDING_MODEL,
                }
            except Exception as e:
                span.record_exception(e)
                span.set_attribute("error", True)
                span.set_attribute("external.request_error", True)
                
                if attempt < 2:
                    logger.warning(f"Request error on attempt {attempt + 1}/3: {str(e)}")
                    continue
                logger.warning(
                    f"Error getting agent config from API: {str(e)}. Using fallback."
                )
                span.set_attribute("external.using_fallback", True)
                return {
                    "memory_blocks": MEMORY_BLOCKS_FALLBACK,
                    "tools": [],
                    "model_name": env.LLM_MODEL,
                    "embedding_name": env.EMBEDDING_MODEL,
                }