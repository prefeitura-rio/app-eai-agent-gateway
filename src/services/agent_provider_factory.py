"""
Factory para diferentes provedores de agentes.
Permite alternar entre Letta e Google Agent Engine com a mesma interface.
"""

import logging
from abc import ABC, abstractmethod
from typing import Any, Literal

from src.services.google_agent_engine_service import (
    GoogleAgentEngineAPIError,
    GoogleAgentEngineAPITimeoutError,
    google_agent_engine_service,
)
from src.services.letta_service import (
    LettaAPIError,
    LettaAPITimeoutError,
    letta_service,
)

logger = logging.getLogger(__name__)

AgentProviderType = Literal["letta", "google_agent_engine"]


class AgentProviderInterface(ABC):
    """Interface comum para todos os provedores de agentes."""

    @abstractmethod
    async def send_message(self, agent_id: str, message: str) -> tuple[list[Any], Any]:
        """Enviar mensagem para o agente."""
        pass

    @abstractmethod
    async def get_agent_id(self, user_number: str) -> str | None:
        """Obter ID do agente para um usuário."""
        pass

    @abstractmethod
    async def create_agent(
        self, user_number: str, override_payload: dict | None = None
    ) -> str | None:
        """Criar um novo agente para um usuário."""
        pass

    @abstractmethod
    async def delete_agent(
        self,
        agent_id: str = "",
        tag_list: list[str] | None = None,
        delete_all_agents: bool = False,
    ) -> dict:
        """Deletar agente(s)."""
        pass

    @abstractmethod
    def send_message_sync(
        self,
        agent_id: str,
        message: str,
        previous_message: str | None = None,
    ) -> tuple[list[Any], Any]:
        """Enviar mensagem para o agente (síncrono)."""
        pass

    @abstractmethod
    def get_agent_id_sync(self, user_number: str) -> str | None:
        """Obter ID do agente para um usuário (síncrono)."""
        pass

    @abstractmethod
    def create_agent_sync(
        self, user_number: str, override_payload: dict | None = None
    ) -> str | None:
        """Criar um novo agente para um usuário (síncrono)."""
        pass


class LettaAgentProvider(AgentProviderInterface):
    """Provedor de agentes usando Letta."""

    async def send_message(self, agent_id: str, message: str) -> tuple[list[Any], Any]:
        return await letta_service.send_message(agent_id, message)

    async def get_agent_id(self, user_number: str) -> str | None:
        return await letta_service.get_agent_id(user_number)

    async def create_agent(
        self, user_number: str, override_payload: dict | None = None
    ) -> str | None:
        return await letta_service.create_agent(user_number, override_payload)

    async def delete_agent(
        self,
        agent_id: str = "",
        tag_list: list[str] | None = None,
        delete_all_agents: bool = False,
    ) -> dict:
        return await letta_service.delete_agent(agent_id, tag_list, delete_all_agents)

    def send_message_sync(
        self,
        agent_id: str,
        message: str,
        previous_message: str | None = None,
    ) -> tuple[list[Any], Any]:
        return letta_service.send_message_sync(agent_id, message, previous_message)

    def get_agent_id_sync(self, user_number: str) -> str | None:
        return letta_service.get_agent_id_sync(user_number)

    def create_agent_sync(
        self, user_number: str, override_payload: dict | None = None
    ) -> str | None:
        return letta_service.create_agent_sync(user_number, override_payload)


class GoogleAgentEngineProvider(AgentProviderInterface):
    """Provedor de agentes usando Google Agent Engine."""

    async def send_message(self, agent_id: str, message: str) -> tuple[list[Any], Any]:
        # Para Google Agent Engine, agent_id é o thread_id (user_number)
        return await google_agent_engine_service.send_message(agent_id, message)

    async def get_agent_id(self, user_number: str) -> str | None:
        # Para Google Agent Engine, retorna o thread_id (que é o próprio user_number)
        return await google_agent_engine_service.get_thread_id(user_number)

    async def create_agent(
        self, user_number: str, override_payload: dict | None = None
    ) -> str | None:
        return await google_agent_engine_service.create_agent(user_number, override_payload)

    async def delete_agent(
        self,
        agent_id: str = "",
        tag_list: list[str] | None = None,
        delete_all_agents: bool = False,
    ) -> dict:
        # Para Google Agent Engine, agent_id é o thread_id (user_number)
        return await google_agent_engine_service.delete_agent(agent_id, tag_list, delete_all_agents)

    def get_agent_id_sync(self, user_number: str) -> str | None:
        return google_agent_engine_service.get_thread_id_sync(user_number)

    def create_agent_sync(
        self, user_number: str, override_payload: dict | None = None
    ) -> str | None:
        return google_agent_engine_service.create_agent_sync(user_number, override_payload)

    def send_message_sync(
        self,
        agent_id: str,
        message: str,
        previous_message: str | None = None,
    ) -> tuple[list[Any], Any]:
        # Para Google Agent Engine, agent_id é o thread_id (user_number)
        return google_agent_engine_service.send_message_sync(agent_id, message, previous_message)


class AgentProviderFactory:
    """Factory para criar provedores de agentes."""

    _providers = {
        "letta": LettaAgentProvider(),
        "google_agent_engine": GoogleAgentEngineProvider(),
    }

    @classmethod
    def get_provider(cls, provider_type: AgentProviderType) -> AgentProviderInterface:
        """Obter provedor de agentes baseado no tipo."""
        if provider_type not in cls._providers:
            raise ValueError(f"Provedor desconhecido: {provider_type}")

        logger.debug(f"Using agent provider: {provider_type}")
        return cls._providers[provider_type]

    @classmethod
    def get_available_providers(cls) -> list[str]:
        """Obter lista de provedores disponíveis."""
        return list(cls._providers.keys())


# Instância global do factory
agent_provider_factory = AgentProviderFactory()
