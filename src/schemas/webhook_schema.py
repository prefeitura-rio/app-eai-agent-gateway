from typing import Optional

from pydantic import BaseModel, Field


class AgentWebhookSchema(BaseModel):
    agent_id: str = Field(
        ...,
        description="The ID of the agent that will receive the message",
    )
    message: str = Field(..., description="The message to be sent to the agent")
    metadata: dict | None = Field(None, description="The metadata of the message")


class UserWebhookSchema(BaseModel):
    user_number: str = Field(
        ...,
        description="The number of the user that will receive the message",
    )
    previous_message: str | None = Field(
        None,
        description="The previous message of the HSM",
    )
    message: str = Field(..., description="The message to be sent to the user")
    metadata: dict | None = Field(None, description="The metadata of the message")
