import asyncio

from letta_client import ContinueToolRule, MaxCountPerStepToolRule, ParentToolRule

from src.config import env
from src.config.telemetry import get_tracer
from src.services.external_dependencies import (
    get_agent_config_from_api,
    get_system_prompt_from_api,
)

tracer = get_tracer("eai-agent")


async def _build_tool_rules(tools: list[str]):
    """Builds the tool rules for the agent."""
    with tracer.start_as_current_span("eai_agent.build_tool_rules") as span:
        span.set_attribute("eai_agent.tools_count", len(tools))
        span.set_attribute("eai_agent.tools", tools)

        tool_rules = [ContinueToolRule(tool_name=tool) for tool in tools]
        if "equipments_instructions" in tools and "equipments_by_address" in tools:
            tool_rules.append(
                ParentToolRule(
                    tool_name="equipments_instructions",
                    children=["equipments_by_address"],
                ),
            )
        if "google_search" in tools:
            tool_rules.append(
                MaxCountPerStepToolRule(
                    tool_name="google_search",
                    max_count_limit=env.EAI_AGENT_MAX_GOOGLE_SEARCH_PER_STEP,
                ),
            )
        span.set_attribute("eai_agent.tool_rules_count", len(tool_rules))

        return tool_rules


def _merge_config(base_config: dict, override_payload: dict | None) -> dict:
    """Faz merge das configurações base com os overrides fornecidos."""
    with tracer.start_as_current_span("eai_agent.merge_config") as span:
        span.set_attribute("eai_agent.has_override", override_payload is not None)
        if override_payload:
            span.set_attribute("eai_agent.override_keys", list(override_payload.keys()))

        if not override_payload:
            return base_config

        merged_config = {
            key: override_payload.get(key, value) for key, value in base_config.items()
        }
        span.set_attribute("eai_agent.merged_keys", list(merged_config.keys()))

        return merged_config


async def create_eai_agent(user_number: str, override_payload: dict | None = None):
    from src.services.letta_service import letta_service

    system_prompt = await get_system_prompt_from_api(agent_type="agentic_search")
    agent_config = await get_agent_config_from_api(agent_type="agentic_search")

    base_config = {
        "agent_type": "memgpt_v2_agent",
        "name": user_number,
        "tags": ["agentic_search", user_number],
        "system": system_prompt,
        "memory_blocks": agent_config.get("memory_blocks"),
        "tools": agent_config.get("tools"),
        "model": agent_config.get("model_name"),
        "embedding": agent_config.get("embedding_name"),
        "context_window_limit": env.EAI_AGENT_CONTEXT_WINDOW_LIMIT,
        "include_base_tool_rules": True,
        "include_base_tools": True,
        "timezone": "America/Sao_Paulo",
        # "tool_rules": tool_rules,
    }

    agent_variables = _merge_config(base_config, override_payload)

    tool_rules = await _build_tool_rules(agent_variables.get("tools"))
    agent_variables["tool_rules"] = tool_rules

    return await letta_service.client.agents.create(**agent_variables)


async def delete_eai_agent(
    agent_id: str = "",
    tag_list: list[str] | None = None,
    delete_all_agents: bool = False,
):
    """Deletes an EAI agent by its ID, tag, or all agents."""
    from src.services.letta_service import letta_service

    try:
        if delete_all_agents:
            # Get all agents and delete them
            agent_list = await letta_service.client.agents.list()
            # Use asyncio.gather to delete all agents concurrently
            deletion_tasks = [
                letta_service.client.agents.delete(agent_id=agent.id)
                for agent in agent_list
            ]
            await asyncio.gather(*deletion_tasks)
            return {"message": f"All {len(agent_list)} agents deleted successfully."}
        if tag_list:
            agent_list = await letta_service.client.agents.list(
                tags=tag_list,
                match_all_tags=False,
            )
            # Use asyncio.gather to delete all agents concurrently
            deletion_tasks = [
                letta_service.client.agents.delete(agent_id=agent.id)
                for agent in agent_list
            ]
            await asyncio.gather(*deletion_tasks)
            return {"message": f"Agents with tags {tag_list} deleted successfully."}
        await letta_service.client.agents.delete(agent_id=agent_id)
    except Exception as e:
        if delete_all_agents:
            raise Exception(f"Error deleting all agents: {e!s}")
        if tag_list:
            raise Exception(f"Error deleting agent with tags {tag_list}: {e!s}")
        raise Exception(f"Error deleting agent {agent_id}: {e!s}")
    return (
        {"message": f"Agents with tags {tag_list} deleted successfully."}
        if tag_list
        else {"message": f"Agent {agent_id} deleted successfully."}
    )
