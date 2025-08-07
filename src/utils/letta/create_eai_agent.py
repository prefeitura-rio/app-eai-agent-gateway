from letta_client import ContinueToolRule

from src.config.telemetry import get_tracer
from src.services.external_dependencies import (
    get_agent_config_from_api,
    get_system_prompt_from_api,
)
from asgiref.sync import async_to_sync

tracer = get_tracer("create-eai-agent")


async def _build_tool_rules(tools: list[str]):
    """Builds the tool rules for the agent."""
    with tracer.start_as_current_span("create_eai_agent.build_tool_rules") as span:
        span.set_attribute("create_eai_agent.tools_count", len(tools))
        span.set_attribute("create_eai_agent.tools", tools)

        tool_rules = [ContinueToolRule(tool_name=tool) for tool in tools]
        span.set_attribute("create_eai_agent.tool_rules_count", len(tool_rules))

        return tool_rules


def _build_tool_rules_sync(tools: list[str]):
    """Builds the tool rules for the agent (sync version)."""
    with tracer.start_as_current_span("create_eai_agent.build_tool_rules_sync") as span:
        span.set_attribute("create_eai_agent.tools_count", len(tools))
        span.set_attribute("create_eai_agent.tools", tools)

        tool_rules = [ContinueToolRule(tool_name=tool) for tool in tools]
        span.set_attribute("create_eai_agent.tool_rules_count", len(tool_rules))

        return tool_rules


def _merge_config(base_config: dict, override_payload: dict | None) -> dict:
    """Faz merge das configurações base com os overrides fornecidos."""
    with tracer.start_as_current_span("create_eai_agent.merge_config") as span:
        span.set_attribute(
            "create_eai_agent.has_override",
            override_payload is not None,
        )
        if override_payload:
            span.set_attribute(
                "create_eai_agent.override_keys",
                list(override_payload.keys()),
            )

        if not override_payload:
            return base_config

        merged_config = {
            key: override_payload.get(key, value) for key, value in base_config.items()
        }
        span.set_attribute("create_eai_agent.merged_keys", list(merged_config.keys()))

        return merged_config


async def create_eai_agent(user_number: str, override_payload: dict | None = None):
    with tracer.start_as_current_span("create_eai_agent.create") as span:
        span.set_attribute("create_eai_agent.user_number", user_number)
        span.set_attribute(
            "create_eai_agent.has_override_payload",
            override_payload is not None,
        )

        try:
            from src.services.letta_service import letta_service

            # Get system prompt
            system_prompt = await get_system_prompt_from_api(
                agent_type="agentic_search",
            )
            span.set_attribute(
                "create_eai_agent.system_prompt_length",
                len(system_prompt),
            )

            # Get agent config
            agent_config = await get_agent_config_from_api(agent_type="agentic_search")
            span.set_attribute(
                "create_eai_agent.config_keys",
                list(agent_config.keys()),
            )

            # Build tool rules
            tool_rules = await _build_tool_rules(agent_config.get("tools"))

            base_config = {
                "agent_type": "memgpt_v2_agent",
                "name": user_number,
                "tags": ["agentic_search", user_number],
                "system": system_prompt,
                "memory_blocks": agent_config.get("memory_blocks"),
                "tools": agent_config.get("tools"),
                "model": agent_config.get("model_name"),
                "embedding": agent_config.get("embedding_name"),
                "context_window_limit": 30000,
                "include_base_tool_rules": True,
                "include_base_tools": True,
                "timezone": "America/Sao_Paulo",
                "tool_rules": tool_rules,
            }

            agent_variables = _merge_config(base_config, override_payload)

            # Create agent
            agent = await letta_service.client.agents.create(**agent_variables)

            span.set_attribute("create_eai_agent.agent_id", agent.id)
            span.set_attribute("create_eai_agent.success", True)

            return agent

        except Exception as e:
            span.record_exception(e)
            span.set_attribute("error", True)
            span.set_attribute("create_eai_agent.success", False)
            raise


def create_eai_agent_sync(user_number: str, override_payload: dict | None = None):
    with tracer.start_as_current_span("create_eai_agent.create_sync") as span:
        span.set_attribute("create_eai_agent.user_number", user_number)
        span.set_attribute(
            "create_eai_agent.has_override_payload",
            override_payload is not None,
        )

        try:
            # Get system prompt (sync version)
            from src.services.external_dependencies import (
                get_agent_config_from_api_sync,
                get_system_prompt_from_api_sync,
            )
            from src.services.letta_service import letta_service

            system_prompt = get_system_prompt_from_api_sync(agent_type="agentic_search")
            span.set_attribute(
                "create_eai_agent.system_prompt_length",
                len(system_prompt),
            )

            # Get agent config (sync version)
            agent_config = get_agent_config_from_api_sync(agent_type="agentic_search")
            span.set_attribute(
                "create_eai_agent.config_keys",
                list(agent_config.keys()),
            )

            # Build tool rules
            tool_rules = _build_tool_rules_sync(agent_config.get("tools"))

            base_config = {
                "agent_type": "memgpt_v2_agent",
                "name": user_number,
                "tags": ["agentic_search", user_number],
                "system": system_prompt,
                "memory_blocks": agent_config.get("memory_blocks"),
                "tools": agent_config.get("tools"),
                "model": agent_config.get("model_name"),
                "embedding": agent_config.get("embedding_name"),
                "context_window_limit": 30000,
                "include_base_tool_rules": True,
                "include_base_tools": True,
                "timezone": "America/Sao_Paulo",
                "tool_rules": tool_rules,
            }

            agent_variables = _merge_config(base_config, override_payload)

            # Create agent
            agent = async_to_sync(letta_service.client.agents.create)(**agent_variables)

            span.set_attribute("create_eai_agent.agent_id", agent.id)
            span.set_attribute("create_eai_agent.success", True)

            return agent

        except Exception as e:
            span.record_exception(e)
            span.set_attribute("error", True)
            span.set_attribute("create_eai_agent.success", False)
            raise
