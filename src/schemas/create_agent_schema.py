from pydantic import BaseModel, Field


class CreateAgentRequestAndOverridePayload(BaseModel):
    user_number: str = Field(
        ...,
        description="The user number/identifier for the agent",
    )
    agent_type: str = Field(
        default="memgpt_v2_agent",
        description="The type of agent to create",
    )
    name: str = Field(default="", description="The name of the agent")
    tags: list[str] = Field(
        default=["agentic_search"],
        description="The tags of the agent",
    )
    system: str = Field(
        default="You are an AI assistant...",
        description="The system prompt of the agent",
    )
    memory_blocks: list[dict] = Field(
        default=[
            {
                "label": "human",
                "limit": 10000,
                "value": "",
            },
            {
                "label": "persona",
                "limit": 5000,
                "value": "",
            },
        ],
        description="The memory blocks of the agent",
    )
    tools: list[str] = Field(
        default=["google_search", "web_search_surkai"],
        description="The tools of the agent",
    )
    model: str = Field(
        default="google_ai/gemini-2.5-flash-lite",
        description="The model of the agent",
    )
    embedding: str = Field(
        default="google_ai/text-embedding-004",
        description="The embedding of the agent",
    )
    context_window_limit: int = Field(
        default=30000,
        description="The context window limit of the agent",
    )
    include_base_tool_rules: bool = Field(
        default=True,
        description="Whether to include base tool rules",
    )
    include_base_tools: bool = Field(
        default=True,
        description="Whether to include base tools",
    )
    timezone: str = Field(
        default="America/Sao_Paulo",
        description="The timezone of the agent",
    )
