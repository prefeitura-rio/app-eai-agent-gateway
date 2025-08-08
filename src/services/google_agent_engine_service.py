import logging
import json
import vertexai
from vertexai import agent_engines
from fastapi import HTTPException
from typing import Any, Optional
from asgiref.sync import async_to_sync

from src.config import env
from src.config.telemetry import get_tracer
from src.services.redis_service import (
    get_string_cache_async,
    get_string_cache_sync,
    store_string_cache_async,
    store_string_cache_sync,
)
from src.utils.google_auth import get_service_account_path
from src.utils.serialize_google_agent_response import serialize_google_agent_response

logger = logging.getLogger(__name__)
tracer = get_tracer("google-agent-engine-service")

# Lazy loading para evitar problemas de inicialização
_vertexai_initialized = False

def _ensure_vertexai_initialized():
    """Inicializa o VertexAI apenas quando necessário."""
    global _vertexai_initialized
    if not _vertexai_initialized:
        try:
            service_account_path = get_service_account_path(env.SERVICE_ACCOUNT)
            logger.info(f"Initializing VertexAI with project={env.PROJECT_ID}, location={env.LOCATION}")
            
            # Se temos um email de service account, passamos para o VertexAI
            # Se temos um arquivo, o GOOGLE_APPLICATION_CREDENTIALS já foi configurado
            vertexai_kwargs = {
                "project": env.PROJECT_ID,
                "location": env.LOCATION,
                "staging_bucket": env.GCS_BUCKET_STAGING,
            }
            
            # Só passa service_account se for um email (contém @)
            if service_account_path and '@' in service_account_path:
                vertexai_kwargs["service_account"] = service_account_path
            
            vertexai.init(**vertexai_kwargs)
            _vertexai_initialized = True
            logger.info("VertexAI initialized successfully")
        except Exception as e:
            logger.error(f"Failed to initialize VertexAI: {e}")
            raise HTTPException(
                status_code=503, 
                detail=f"Google Cloud authentication failed: {str(e)}"
            )


class GoogleAgentEngineUsage:
    """Classe para compatibilidade com o formato de Usage do Letta."""
    
    def __init__(self, input_tokens: int = 0, output_tokens: int = 0, total_tokens: Optional[int] = None):
        self.input_tokens = input_tokens
        self.output_tokens = output_tokens
        self.total_tokens = total_tokens if total_tokens is not None else (input_tokens + output_tokens)
    
    def to_dict(self) -> dict:
        """Converte para dicionário para serialização."""
        return {
            "input_tokens": self.input_tokens,
            "output_tokens": self.output_tokens,
            "total_tokens": self.total_tokens,
        }


class GoogleAgentEngineAPITimeoutError(Exception):
    """Exceção personalizada para timeouts da API Google Agent Engine"""

    def __init__(self, message: str, thread_id: str = None):
        self.message = message
        self.thread_id = thread_id
        super().__init__(message)


class GoogleAgentEngineAPIError(Exception):
    """Exceção personalizada para erros da API Google Agent Engine"""

    def __init__(self, message: str, status_code: int = None, thread_id: str = None):
        self.message = message
        self.status_code = status_code
        self.thread_id = thread_id
        super().__init__(message)


class GoogleAgentEngineService:
    def __init__(self):
        self._remote_agent = None
        self._reasoning_engine_id = env.REASONING_ENGINE_ID
    
    @property
    def remote_agent(self):
        """Lazy loading do remote agent."""
        if self._remote_agent is None:
            _ensure_vertexai_initialized()
            self._remote_agent = self._get_agent(self._reasoning_engine_id)
        return self._remote_agent

    def _extract_usage_from_messages(self, messages: list[Any]) -> GoogleAgentEngineUsage:
        """Extrai informações de usage das mensagens retornadas pelo Google Agent Engine."""
        total_input_tokens = 0
        total_output_tokens = 0
        
        for message in messages:
            # Para mensagens locais (LangChain)
            if hasattr(message, 'usage_metadata'):
                usage = getattr(message, 'usage_metadata', {})
                if isinstance(usage, dict):
                    total_input_tokens += usage.get('input_tokens', 0)
                    total_output_tokens += usage.get('output_tokens', 0)
            # Para mensagens remotas (dict)
            elif isinstance(message, dict):
                usage = message.get('usage_metadata', {})
                if isinstance(usage, dict):
                    total_input_tokens += usage.get('input_tokens', 0)
                    total_output_tokens += usage.get('output_tokens', 0)
        
        return GoogleAgentEngineUsage(
            input_tokens=total_input_tokens,
            output_tokens=total_output_tokens
        )

    def _get_agent(self, reasoning_engine_id: str):
        """Obtém o agent engine do Google Cloud."""
        _ensure_vertexai_initialized()
        return agent_engines.get(
            f"projects/{env.PROJECT_NUMBER}/locations/{env.LOCATION}/reasoningEngines/{reasoning_engine_id}"
        )
    
    def _get_thread_cache_key(self, user_number: str) -> str:
        """Generate cache key for thread ID lookup."""
        return f"google_thread_id:{user_number}"

    def _get_thread_cache_ttl(self) -> int:
        """Get TTL for thread ID cache (24 hours by default)."""
        return env.AGENT_ID_CACHE_TTL

    def _invalidate_thread_cache_sync(self, user_number: str) -> None:
        """Invalidate thread cache for a specific user (sync)."""
        try:
            from src.services.redis_service import sync_client

            cache_key = self._get_thread_cache_key(user_number)
            sync_client.delete(f"{env.APP_PREFIX}:cache:{cache_key}")
            logger.debug(f"Invalidated thread cache for user: {user_number}")
        except Exception as e:
            logger.warning(f"Failed to invalidate thread cache for {user_number}: {e}")

    async def _invalidate_thread_cache_async(self, user_number: str) -> None:
        """Invalidate thread cache for a specific user (async)."""
        try:
            from src.services.redis_service import async_client

            cache_key = self._get_thread_cache_key(user_number)
            await async_client.delete(f"{env.APP_PREFIX}:cache:{cache_key}")
            logger.debug(f"Invalidated thread cache for user: {user_number}")
        except Exception as e:
            logger.warning(f"Failed to invalidate thread cache for {user_number}: {e}")

    ## SYNC METHODS

    def send_message_sync(
        self,
        thread_id: str,
        message: str,
        previous_message: str | None = None,
    ) -> tuple[list[Any], Any]:
        """
        Versão síncrona do send_message para compatibilidade com Celery.
        Usa asyncio.run() de forma otimizada para máxima performance.
        """
        with tracer.start_as_current_span("google_agent_engine.send_message_sync") as span:
            span.set_attribute("google_agent_engine.thread_id", thread_id)
            span.set_attribute("google_agent_engine.message_length", len(message))
            span.set_attribute(
                "google_agent_engine.has_previous_message",
                previous_message is not None,
            )

            try:
                data = {
                    "messages": [{"role": "human", "content": message}],
                }

                config = {"configurable": {"thread_id": thread_id}}
                response = async_to_sync(self.remote_agent.async_query)(input=data, config=config)

                span.set_attribute(
                    "google_agent_engine.response_messages_count",
                    len(response.get("messages", [])),
                )

                # Return response in a format similar to Letta
                messages = response.get("messages", [])
                usage = self._extract_usage_from_messages(messages)

                # Log usage information
                if usage.total_tokens > 0:
                    span.set_attribute("google_agent_engine.usage_tokens", usage.total_tokens)
                    span.set_attribute("google_agent_engine.input_tokens", usage.input_tokens)
                    span.set_attribute("google_agent_engine.output_tokens", usage.output_tokens)
                    logger.debug(f"Google Agent Engine usage - Input: {usage.input_tokens}, Output: {usage.output_tokens}, Total: {usage.total_tokens}")

                # Serializa para o formato padrão do Letta
                serialized_response = serialize_google_agent_response(
                    messages=messages,
                    usage=usage.to_dict(),
                    agent_id=thread_id,
                    processed_at=thread_id  # Usar thread_id como processed_at por compatibilidade
                )
                
                return serialized_response.get("data", {}).get("messages", []), usage

            except Exception as e:
                span.record_exception(e)
                span.set_attribute("error", True)
                logger.error(f"Error in sync send_message for thread {thread_id}: {e}")
                
                # Converter para as exceções apropriadas
                if "timeout" in str(e).lower():
                    raise GoogleAgentEngineAPITimeoutError(
                        message=f"Timeout ao enviar mensagem: {str(e)}",
                        thread_id=thread_id
                    )
                else:
                    raise GoogleAgentEngineAPIError(
                        message=f"Erro ao enviar mensagem: {str(e)}",
                        thread_id=thread_id
                    )

    def get_thread_id_sync(self, user_number: str) -> str | None:
        with tracer.start_as_current_span("google_agent_engine.get_thread_id_sync") as span:
            span.set_attribute("google_agent_engine.user_number", user_number)

            cache_key = self._get_thread_cache_key(user_number)
            cache_ttl = self._get_thread_cache_ttl()

            # Try to get from cache first
            try:
                thread_id = get_string_cache_sync(cache_key)
                if thread_id:
                    span.set_attribute("google_agent_engine.cache_hit", True)
                    span.set_attribute("google_agent_engine.thread_id", thread_id)
                    logger.debug(f"Cache hit for thread ID: {user_number} -> {thread_id}")
                    return thread_id
            except Exception as e:
                span.record_exception(e)
                span.set_attribute("error", True)
                logger.warning(f"Cache error for user {user_number}: {e}")

            # For Google Agent Engine, thread_id is the user_number itself
            span.set_attribute("google_agent_engine.cache_hit", False)
            thread_id = user_number
            span.set_attribute("google_agent_engine.thread_id", thread_id)

            # Cache the result
            try:
                store_string_cache_sync(cache_key, thread_id, cache_ttl)
                logger.debug(f"Cached thread ID: {user_number} -> {thread_id}")
            except Exception as cache_error:
                span.record_exception(cache_error)
                logger.warning(f"Failed to cache thread ID for {user_number}: {cache_error}")

            return thread_id

    def create_agent_sync(
        self,
        user_number: str,
        override_payload: dict | None = None,
    ) -> str | None:
        with tracer.start_as_current_span("google_agent_engine.create_agent_sync") as span:
            span.set_attribute("google_agent_engine.user_number", user_number)
            span.set_attribute(
                "google_agent_engine.has_override_payload",
                override_payload is not None,
            )

            try:
                # For Google Agent Engine, we don't need to create agents
                # The thread_id is simply the user_number
                thread_id = user_number
                span.set_attribute("google_agent_engine.thread_id", thread_id)

                # Invalidate cache since we "created" a new thread
                self._invalidate_thread_cache_sync(user_number)

                return thread_id
            except Exception as e:
                span.record_exception(e)
                span.set_attribute("error", True)
                logger.error(f"Error creating thread for user {user_number}: {e}")
                raise e

    ## ASYNC METHODS

    async def send_message(self, thread_id: str, message: str):
        with tracer.start_as_current_span("google_agent_engine.send_message_async") as span:
            span.set_attribute("google_agent_engine.thread_id", thread_id)
            span.set_attribute("google_agent_engine.message_length", len(message))

            try:
                data = {
                    "messages": [{"role": "human", "content": message}],
                }

                config = {"configurable": {"thread_id": thread_id}}
                response = await self.remote_agent.async_query(input=data, config=config)

                span.set_attribute(
                    "google_agent_engine.response_messages_count",
                    len(response.get("messages", [])),
                )

                # Return response in a format similar to Letta
                messages = response.get("messages", [])
                usage = self._extract_usage_from_messages(messages)

                # Log usage information
                if usage.total_tokens > 0:
                    span.set_attribute("google_agent_engine.usage_tokens", usage.total_tokens)
                    span.set_attribute("google_agent_engine.input_tokens", usage.input_tokens)
                    span.set_attribute("google_agent_engine.output_tokens", usage.output_tokens)
                    logger.debug(f"Google Agent Engine usage - Input: {usage.input_tokens}, Output: {usage.output_tokens}, Total: {usage.total_tokens}")

                # Serializa para o formato padrão do Letta
                serialized_response = serialize_google_agent_response(
                    messages=messages,
                    usage=usage.to_dict(),
                    agent_id=thread_id,
                    processed_at=thread_id  # Usar thread_id como processed_at por compatibilidade
                )
                
                return serialized_response.get("data", {}).get("messages", []), usage

            except Exception as e:
                span.record_exception(e)
                span.set_attribute("error", True)
                logger.error(f"Error sending async message to thread {thread_id}: {e}")
                raise HTTPException(
                    status_code=500,
                    detail=f"Erro na API Google Agent Engine: {e!s}",
                )

    async def get_thread_id(self, user_number: str) -> str | None:
        with tracer.start_as_current_span("google_agent_engine.get_thread_id") as span:
            span.set_attribute("google_agent_engine.user_number", user_number)

            cache_key = self._get_thread_cache_key(user_number)
            cache_ttl = self._get_thread_cache_ttl()

            # Try to get from cache first
            try:
                thread_id = await get_string_cache_async(cache_key)
                if thread_id:
                    span.set_attribute("google_agent_engine.cache_hit", True)
                    span.set_attribute("google_agent_engine.thread_id", thread_id)
                    logger.debug(f"Cache hit for thread ID: {user_number} -> {thread_id}")
                    return thread_id
            except Exception as e:
                span.record_exception(e)
                span.set_attribute("error", True)
                logger.warning(f"Cache error for user {user_number}: {e}")

            # For Google Agent Engine, thread_id is the user_number itself
            span.set_attribute("google_agent_engine.cache_hit", False)
            thread_id = user_number
            span.set_attribute("google_agent_engine.thread_id", thread_id)

            # Cache the result
            try:
                await store_string_cache_async(cache_key, thread_id, cache_ttl)
                logger.debug(f"Cached thread ID: {user_number} -> {thread_id}")
            except Exception as cache_error:
                span.record_exception(cache_error)
                logger.warning(f"Failed to cache thread ID for {user_number}: {cache_error}")

            return thread_id

    async def create_agent(
        self,
        user_number: str,
        override_payload: dict | None = None,
    ) -> str | None:
        with tracer.start_as_current_span("google_agent_engine.create_agent") as span:
            span.set_attribute("google_agent_engine.user_number", user_number)
            span.set_attribute(
                "google_agent_engine.has_override_payload",
                override_payload is not None,
            )

            try:
                # For Google Agent Engine, we don't need to create agents
                # The thread_id is simply the user_number
                thread_id = user_number
                span.set_attribute("google_agent_engine.thread_id", thread_id)

                # Invalidate cache since we "created" a new thread
                await self._invalidate_thread_cache_async(user_number)

                return thread_id
            except Exception as e:
                span.record_exception(e)
                span.set_attribute("error", True)
                logger.error(f"Error creating thread for user {user_number}: {e}")
                raise e

    async def delete_agent(
        self,
        thread_id: str = "",
        tag_list: list[str] | None = None,
        delete_all_agents: bool = False,
    ) -> dict:
        # Google Agent Engine doesn't have explicit agent deletion
        # Just clear cache if needed
        try:
            if tag_list:
                for user_number in tag_list:
                    await self._invalidate_thread_cache_async(user_number)
                return {"message": f"Thread cache cleared for users {tag_list}"}
            elif thread_id:
                # thread_id is the user_number in our case
                await self._invalidate_thread_cache_async(thread_id)
                return {"message": f"Thread cache cleared for user {thread_id}"}
            elif delete_all_agents:
                # Clear all caches - this is a simplification
                return {"message": "All thread caches cleared"}
            else:
                return {"message": "No action needed for Google Agent Engine"}
        except Exception as e:
            logger.error(f"Error clearing thread cache: {e}")
            raise HTTPException(status_code=500, detail=str(e))


google_agent_engine_service = GoogleAgentEngineService()


def parse_agent_response(response, is_local=False):
    """Parse the agent response and show all steps."""
    print("\n" + "=" * 60)
    print("🤖 AGENT EXECUTION STEPS")
    print("=" * 60)

    if is_local:
        # Local agent returns LangChain message objects directly
        messages = response.get("messages", [])

        for i, message in enumerate(messages):
            msg_type = message.__class__.__name__

            if "HumanMessage" in msg_type:
                print(f"\n👤 USER MESSAGE #{i+1}:")
                print(f"   {message.content}")

            elif "AIMessage" in msg_type:
                print(f"\n🤖 AI RESPONSE #{i+1}:")

                # Check for tool calls
                tool_calls = getattr(message, "tool_calls", [])
                if tool_calls:
                    print("   🔧 TOOL CALLS:")
                    for tool_call in tool_calls:
                        tool_name = tool_call.get("name", "unknown")
                        tool_args = tool_call.get("args", {})
                        print(f"      📞 Calling: {tool_name}")
                        print(f"      📋 Arguments: {json.dumps(tool_args, indent=8)}")

                # Show AI content if any
                if message.content:
                    print(f"   💬 Response: {message.content}")

                # Show usage metadata
                usage = getattr(message, "usage_metadata", {})
                if usage:
                    total_tokens = usage.get("total_tokens", 0)
                    input_tokens = usage.get("input_tokens", 0)
                    output_tokens = usage.get("output_tokens", 0)
                    print(
                        f"   📊 Tokens: {input_tokens} in, {output_tokens} out, {total_tokens} total"
                    )

            elif "ToolMessage" in msg_type:
                print(f"\n🔧 TOOL RESPONSE #{i+1}:")
                tool_name = getattr(message, "name", "unknown")
                tool_content = message.content

                print(f"   🛠️  Tool: {tool_name}")
                print(f"   📄 Response: {tool_content}")
    else:
        # Remote agent returns direct message objects
        if "messages" not in response:
            print("❌ Unexpected response format")
            return

        messages = response["messages"]

        for i, message in enumerate(messages):
            msg_type = message.get("type", "unknown")
            content = message.get("content", "")

            if msg_type == "human":
                print(f"\n👤 USER MESSAGE #{i+1}:")
                print(f"   {content}")

            elif msg_type == "ai":
                print(f"\n🤖 AI RESPONSE #{i+1}:")

                # Check for tool calls
                tool_calls = message.get("tool_calls", [])
                if tool_calls:
                    print("   🔧 TOOL CALLS:")
                    for tool_call in tool_calls:
                        tool_name = tool_call.get("name", "unknown")
                        tool_args = tool_call.get("args", {})
                        print(f"      📞 Calling: {tool_name}")
                        print(f"      📋 Arguments: {json.dumps(tool_args, indent=8)}")

                # Show AI content if any
                if content:
                    print(f"   💬 Response: {content}")

                # Show usage metadata
                usage = message.get("usage_metadata", {})
                if usage:
                    total_tokens = usage.get("total_tokens", 0)
                    input_tokens = usage.get("input_tokens", 0)
                    output_tokens = usage.get("output_tokens", 0)
                    print(
                        f"   📊 Tokens: {input_tokens} in, {output_tokens} out, {total_tokens} total"
                    )

            elif msg_type == "tool":
                print(f"\n🔧 TOOL RESPONSE #{i+1}:")
                tool_name = message.get("name", "unknown")
                tool_content = message.get("content", "")
                tool_status = message.get("status", "unknown")

                print(f"   🛠️  Tool: {tool_name}")
                print(f"   📊 Status: {tool_status}")
                print(f"   📄 Response: {tool_content}")


# Legacy function removed - use google_agent_engine_service.send_message() instead