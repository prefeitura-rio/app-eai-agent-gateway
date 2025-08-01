from typing import Optional

from pydantic import BaseModel, Field, validator


class DeleteAgentRequest(BaseModel):
    agent_id: str | None = Field(None, description="The ID of the agent to delete")
    tag_list: list[str] | None = Field(
        None,
        description="List of tags to match agents for deletion",
    )
    delete_all_agents: bool | None = Field(
        False,
        description="Delete all agents in the system",
    )

    @validator("delete_all_agents", pre=True, always=True)
    def validate_agent_deletion_params(cls, v, values):
        agent_id = values.get("agent_id")
        tag_list = values.get("tag_list")

        # Count how many deletion options are provided
        provided_options = sum([bool(agent_id), bool(tag_list), bool(v)])

        # Ensure exactly one deletion option is provided
        if provided_options == 0:
            raise ValueError(
                "One of 'agent_id', 'tag_list', or 'delete_all_agents' must be provided",
            )

        if provided_options > 1:
            raise ValueError(
                "Only one of 'agent_id', 'tag_list', or 'delete_all_agents' should be provided",
            )

        return v
