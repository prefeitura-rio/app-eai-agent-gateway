from json import tool
from src.services.external_dependencies import get_system_prompt_from_api, get_agent_config_from_api
from src.config import env
from letta_client import ContinueToolRule, ParentToolRule

async def _build_tool_rules(tools: list[str]):
    """Builds the tool rules for the agent."""
    tool_list = [ContinueToolRule(tool_name=tool) for tool in tools]
    if "equipments_instructions" in tools and "equipments_by_address" in tools:
        tool_list.append(ParentToolRule(tool_name="equipments_instructions", children=["equipments_by_address"]))
    return tool_list

def _merge_config(base_config: dict, override_payload: dict | None) -> dict:
    """Faz merge das configurações base com os overrides fornecidos."""
    if not override_payload:
        return base_config
    
    return {key: override_payload.get(key, value) for key, value in base_config.items()}

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