from src.services.external_dependencies import get_system_prompt_from_api, get_agent_config_from_api
from letta_client import ContinueToolRule

async def _build_tool_rules(tools: list[str]):
    """Builds the tool rules for the agent."""
    return [ContinueToolRule(tool_name=tool) for tool in tools]

def _merge_config(base_config: dict, override_payload: dict | None) -> dict:
    """Faz merge das configurações base com os overrides fornecidos."""
    if not override_payload:
        return base_config
    
    return {key: override_payload.get(key, value) for key, value in base_config.items()}

async def create_eai_agent(user_number: str, override_payload: dict | None = None):
    from src.services.letta_service import letta_service
    system_prompt = await get_system_prompt_from_api(agent_type="agentic_search")
    agent_config = await get_agent_config_from_api(agent_type="agentic_search")
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
    
    return await letta_service.client.agents.create(**agent_variables)