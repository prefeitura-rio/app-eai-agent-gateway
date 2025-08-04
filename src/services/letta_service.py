import logging

import httpx
from fastapi import HTTPException
from letta_client import AsyncLetta, Letta, MessageCreate

from src.config import env
from src.config.telemetry import get_tracer
from src.services.redis_service import (
    get_string_cache_async,
    get_string_cache_sync,
    store_string_cache_async,
    store_string_cache_sync,
)
from src.utils.letta.eai_agent import create_eai_agent, delete_eai_agent

logger = logging.getLogger(__name__)
tracer = get_tracer("letta-service")


class LettaAPITimeoutError(Exception):
    """Exceção personalizada para timeouts da API Letta"""

    def __init__(self, message: str, agent_id: str = None):
        self.message = message
        self.agent_id = agent_id
        super().__init__(message)


class LettaAPIError(Exception):
    """Exceção personalizada para erros da API Letta"""

    def __init__(self, message: str, status_code: int = None, agent_id: str = None):
        self.message = message
        self.status_code = status_code
        self.agent_id = agent_id
        super().__init__(message)


class LettaService:
    def __init__(self):
        timeout_config = httpx.Timeout(
            connect=30.0,
            read=120.0,
            write=30.0,
            pool=120.0,
        )

        # Fast timeout for agent ID lookups - fail fast and let Celery retry
        fast_timeout_config = httpx.Timeout(
            connect=5.0,
            read=15.0,  # 15 seconds max for agent ID lookup
            write=5.0,
            pool=15.0,
        )

        httpx_async_client = httpx.AsyncClient(
            timeout=timeout_config,
            follow_redirects=True,
            limits=httpx.Limits(max_connections=50, max_keepalive_connections=20),
        )
        httpx_client = httpx.Client(
            timeout=timeout_config,
            follow_redirects=True,
            limits=httpx.Limits(max_connections=50, max_keepalive_connections=20),
        )

        # Fast clients for agent ID lookups with short timeouts
        httpx_async_client_fast = httpx.AsyncClient(
            timeout=fast_timeout_config,
            follow_redirects=True,
            limits=httpx.Limits(max_connections=20, max_keepalive_connections=10),
        )
        httpx_client_fast = httpx.Client(
            timeout=fast_timeout_config,
            follow_redirects=True,
            limits=httpx.Limits(max_connections=20, max_keepalive_connections=10),
        )

        self.client = AsyncLetta(
            base_url=env.LETTA_API_URL,
            token=env.LETTA_API_TOKEN,
            httpx_client=httpx_async_client,
        )
        self.client_sync = Letta(
            base_url=env.LETTA_API_URL,
            token=env.LETTA_API_TOKEN,
            httpx_client=httpx_client,
        )

        # Fast clients for agent ID lookups
        self.client_fast = AsyncLetta(
            base_url=env.LETTA_API_URL,
            token=env.LETTA_API_TOKEN,
            httpx_client=httpx_async_client_fast,
        )
        self.client_sync_fast = Letta(
            base_url=env.LETTA_API_URL,
            token=env.LETTA_API_TOKEN,
            httpx_client=httpx_client_fast,
        )

    def _get_agent_cache_key(self, user_number: str) -> str:
        """Generate cache key for agent ID lookup."""
        return f"agent_id:{user_number}"

    def _get_agent_cache_ttl(self) -> int:
        """Get TTL for agent ID cache (24 hours by default)."""
        return env.AGENT_ID_CACHE_TTL

    def _invalidate_agent_cache_sync(self, user_number: str) -> None:
        """Invalidate agent cache for a specific user (sync)."""
        try:
            from src.services.redis_service import sync_client

            cache_key = self._get_agent_cache_key(user_number)
            sync_client.delete(f"{env.APP_PREFIX}:cache:{cache_key}")
            logger.debug(f"Invalidated agent cache for user: {user_number}")
        except Exception as e:
            logger.warning(f"Failed to invalidate agent cache for {user_number}: {e}")

    async def _invalidate_agent_cache_async(self, user_number: str) -> None:
        """Invalidate agent cache for a specific user (async)."""
        try:
            from src.services.redis_service import async_client

            cache_key = self._get_agent_cache_key(user_number)
            await async_client.delete(f"{env.APP_PREFIX}:cache:{cache_key}")
            logger.debug(f"Invalidated agent cache for user: {user_number}")
        except Exception as e:
            logger.warning(f"Failed to invalidate agent cache for {user_number}: {e}")

    ## SYNC METHODS

    def send_message_sync(
        self,
        agent_id: str,
        message: str,
        previous_message: str | None = None,
    ):
        with tracer.start_as_current_span("letta.send_message_sync") as span:
            span.set_attribute("letta.agent_id", agent_id)
            span.set_attribute("letta.message_length", len(message))
            span.set_attribute(
                "letta.has_previous_message",
                previous_message is not None,
            )

            try:
                messages: list[MessageCreate] = []
                if previous_message is not None:
                    messages.append(
                        MessageCreate(
                            role="user",
                            content=f"Previous message sent by system: {previous_message}",
                        ),
                    )
                messages.append(MessageCreate(role="user", content=message))

                response = self.client_sync.agents.messages.create(
                    agent_id=agent_id,
                    messages=messages,
                )

                span.set_attribute(
                    "letta.response_messages_count",
                    len(response.messages),
                )
                if response.usage:
                    span.set_attribute(
                        "letta.usage_tokens",
                        response.usage.total_tokens,
                    )

                return response.messages, response.usage

            except httpx.TimeoutException as e:
                span.record_exception(e)
                span.set_attribute("error", True)
                logger.exception(f"Timeout sending message to agent {agent_id}: {e}")
                raise LettaAPITimeoutError(
                    f"Timeout communicating with Letta API: {e!s}",
                    agent_id=agent_id,
                ) from e
            except httpx.HTTPStatusError as e:
                span.record_exception(e)
                span.set_attribute("error", True)
                span.set_attribute("http.status_code", e.response.status_code)
                logger.exception(
                    f"HTTP status error sending message to agent {agent_id}: {e.response.status_code} - {e.response.text}",
                )
                raise LettaAPIError(
                    f"Letta API error: {e.response.text}",
                    status_code=e.response.status_code,
                    agent_id=agent_id,
                ) from e
            except httpx.HTTPError as e:
                span.record_exception(e)
                span.set_attribute("error", True)
                logger.exception(f"HTTP error sending message to agent {agent_id}: {e}")
                raise LettaAPIError(f"HTTP error: {e!s}", agent_id=agent_id) from e
            except Exception as e:
                span.record_exception(e)
                span.set_attribute("error", True)
                logger.exception(
                    f"Unexpected error sending message to agent {agent_id}: {e}",
                )
                raise LettaAPIError(
                    f"Unexpected error: {e!s}",
                    agent_id=agent_id,
                ) from e

    def get_agent_id_sync(self, user_number: str) -> str | None:
        with tracer.start_as_current_span("letta.get_agent_id_sync") as span:
            span.set_attribute("letta.user_number", user_number)

            cache_key = self._get_agent_cache_key(user_number)
            cache_ttl = self._get_agent_cache_ttl()

            # Try to get from cache first
            try:
                agent_id = get_string_cache_sync(cache_key)
                if agent_id:
                    span.set_attribute("letta.cache_hit", True)
                    span.set_attribute("letta.agent_id", agent_id)
                    logger.debug(f"Cache hit for agent ID: {user_number} -> {agent_id}")
                    return agent_id
            except Exception as e:
                span.record_exception(e)
                span.set_attribute("error", True)
                logger.warning(f"Cache error for user {user_number}: {e}")

            # Cache miss or error, fetch from Letta API with fast timeout
            span.set_attribute("letta.cache_hit", False)
            try:
                response = self.client_sync_fast.agents.list(
                    name=user_number,
                )

                if response:
                    agent_id = response[0].id
                    span.set_attribute("letta.agent_id", agent_id)

                    # Cache the result
                    try:
                        store_string_cache_sync(cache_key, agent_id, cache_ttl)
                        logger.debug(f"Cached agent ID: {user_number} -> {agent_id}")
                    except Exception as cache_error:
                        span.record_exception(cache_error)
                        logger.warning(
                            f"Failed to cache agent ID for {user_number}: {cache_error}"
                        )

                    return agent_id
                return None

            except httpx.TimeoutException as e:
                span.record_exception(e)
                span.set_attribute("error", True)
                span.set_attribute("letta.timeout", True)
                logger.warning(
                    f"Fast timeout getting agent ID for user {user_number} on Letta: {e}",
                )
                # Let Celery retry the entire task
                raise LettaAPITimeoutError(
                    f"Fast timeout getting agent ID: {e!s}",
                    agent_id=None,
                ) from e
            except Exception as e:
                span.record_exception(e)
                span.set_attribute("error", True)
                logger.error(
                    f"Error getting agent ID for user {user_number} on Letta: {e}",
                )
                raise e

    def create_agent_sync(
        self,
        user_number: str,
        override_payload: dict | None = None,
    ) -> str | None:
        with tracer.start_as_current_span("letta.create_agent_sync") as span:
            span.set_attribute("letta.user_number", user_number)
            span.set_attribute(
                "letta.has_override_payload",
                override_payload is not None,
            )

            try:
                # Import here to avoid circular imports
                from src.utils.letta.create_eai_agent import create_eai_agent_sync

                if override_payload is None:
                    agent = create_eai_agent_sync(user_number=user_number)
                else:
                    agent = create_eai_agent_sync(
                        user_number=user_number,
                        override_payload=override_payload,
                    )

                span.set_attribute("letta.agent_id", agent.id)

                # Invalidate cache since we created a new agent
                self._invalidate_agent_cache_sync(user_number)

                return agent.id
            except Exception as e:
                span.record_exception(e)
                span.set_attribute("error", True)
                logger.error(
                    f"Error creating agent for user {user_number} on Letta: {e}",
                )
                raise e

    ## ASYNC METHODS

    async def send_message(self, agent_id: str, message: str):
        with tracer.start_as_current_span("letta.send_message_async") as span:
            span.set_attribute("letta.agent_id", agent_id)
            span.set_attribute("letta.message_length", len(message))

            try:
                response = await self.client.agents.messages.create(
                    agent_id=agent_id,
                    messages=[MessageCreate(role="user", content=message)],
                )

                span.set_attribute(
                    "letta.response_messages_count",
                    len(response.messages),
                )
                if response.usage:
                    span.set_attribute(
                        "letta.usage_tokens",
                        response.usage.total_tokens,
                    )

                return response.messages, response.usage

            except httpx.TimeoutException as e:
                span.record_exception(e)
                span.set_attribute("error", True)
                logger.error(f"Timeout sending async message to agent {agent_id}: {e}")
                raise HTTPException(
                    status_code=408,
                    detail=f"Timeout communicating with Letta API: {e!s}",
                )
            except httpx.HTTPStatusError as e:
                span.record_exception(e)
                span.set_attribute("error", True)
                span.set_attribute("http.status_code", e.response.status_code)
                logger.error(
                    f"HTTP status error sending async message to agent {agent_id}: {e.response.status_code} - {e.response.text}",
                )
                raise HTTPException(
                    status_code=e.response.status_code,
                    detail=f"Letta API error: {e.response.text}",
                )
            except httpx.HTTPError as e:
                span.record_exception(e)
                span.set_attribute("error", True)
                logger.error(
                    f"HTTP error sending async message to agent {agent_id}: {e}",
                )
                raise HTTPException(status_code=500, detail=f"HTTP error: {e!s}")
            except Exception as e:
                span.record_exception(e)
                span.set_attribute("error", True)
                logger.error(
                    f"Unexpected error sending async message to agent {agent_id}: {e}",
                )
                logger.error(f"Exception type: {type(e).__name__}")
                raise HTTPException(
                    status_code=500,
                    detail=f"Unexpected error: {e!s}",
                )

    async def get_agent_id(self, user_number: str) -> str | None:
        with tracer.start_as_current_span("letta.get_agent_id") as span:
            span.set_attribute("letta.user_number", user_number)

            cache_key = self._get_agent_cache_key(user_number)
            cache_ttl = self._get_agent_cache_ttl()

            # Try to get from cache first
            try:
                agent_id = await get_string_cache_async(cache_key)
                if agent_id:
                    span.set_attribute("letta.cache_hit", True)
                    span.set_attribute("letta.agent_id", agent_id)
                    logger.debug(f"Cache hit for agent ID: {user_number} -> {agent_id}")
                    return agent_id
            except Exception as e:
                span.record_exception(e)
                span.set_attribute("error", True)
                logger.warning(f"Cache error for user {user_number}: {e}")

            # Cache miss or error, fetch from Letta API with fast timeout
            span.set_attribute("letta.cache_hit", False)
            try:
                response = await self.client_fast.agents.list(
                    name=user_number,
                )

                if response:
                    agent_id = response[0].id
                    span.set_attribute("letta.agent_id", agent_id)

                    # Cache the result
                    try:
                        await store_string_cache_async(cache_key, agent_id, cache_ttl)
                        logger.debug(f"Cached agent ID: {user_number} -> {agent_id}")
                    except Exception as cache_error:
                        span.record_exception(cache_error)
                        logger.warning(
                            f"Failed to cache agent ID for {user_number}: {cache_error}"
                        )

                    return agent_id
                return None

            except httpx.TimeoutException as e:
                span.record_exception(e)
                span.set_attribute("error", True)
                span.set_attribute("letta.timeout", True)
                logger.warning(
                    f"Fast timeout getting agent ID for user {user_number} on Letta: {e}",
                )
                # Let Celery retry the entire task
                raise LettaAPITimeoutError(
                    f"Fast timeout getting agent ID: {e!s}",
                    agent_id=None,
                ) from e
            except Exception as e:
                span.record_exception(e)
                span.set_attribute("error", True)
                logger.error(
                    f"Error getting agent ID for user {user_number} on Letta: {e}",
                )
                raise e

    async def create_agent(
        self,
        user_number: str,
        override_payload: dict | None = None,
    ) -> str | None:
        with tracer.start_as_current_span("letta.create_agent") as span:
            span.set_attribute("letta.user_number", user_number)
            span.set_attribute(
                "letta.has_override_payload",
                override_payload is not None,
            )

            try:
                if override_payload is None:
                    agent = await create_eai_agent(user_number=user_number)
                else:
                    agent = await create_eai_agent(
                        user_number=user_number,
                        override_payload=override_payload,
                    )

                span.set_attribute("letta.agent_id", agent.id)

                # Invalidate cache since we created a new agent
                await self._invalidate_agent_cache_async(user_number)

                return agent.id
            except Exception as e:
                span.record_exception(e)
                span.set_attribute("error", True)
                logger.error(
                    f"Error creating agent for user {user_number} on Letta: {e}",
                )
                raise e

    async def delete_agent(
        self,
        agent_id: str = "",
        tag_list: list[str] | None = None,
        delete_all_agents: bool = False,
    ) -> dict:
        try:
            await delete_eai_agent(
                agent_id=agent_id,
                tag_list=tag_list,
                delete_all_agents=delete_all_agents,
            )
            if delete_all_agents:
                return {"message": "All agents deleted successfully."}
            return (
                {"message": f"Agents with tags {tag_list} deleted successfully."}
                if tag_list
                else {"message": f"Agent {agent_id} deleted successfully."}
            )
        except Exception as e:
            if delete_all_agents:
                logger.error(f"Error deleting all agents on Letta: {e}")
            elif tag_list:
                logger.error(
                    f"Error deleting agents with tags {tag_list} on Letta: {e}",
                )
            else:
                logger.error(f"Error deleting agent {agent_id} on Letta: {e}")
            raise HTTPException(status_code=500, detail=str(e))


letta_service = LettaService()
