from typing import Optional, Literal

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
    provider: Literal["letta", "google_agent_engine"] = Field(
        "google_agent_engine",
        description="The agent provider to use (default: google_agent_engine)",
    )
