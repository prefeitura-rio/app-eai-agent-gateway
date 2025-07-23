from pydantic import BaseModel, Field
from typing import Optional

class WebhookSchema(BaseModel):
    agent_id: str = Field(..., description="The ID of the agent that will receive the message")
    user_id: Optional[str] = Field(None, description="The ID of the user that will receive the message")
    id_type: str = Field(..., description="The type of the ID of the user that will receive the message, agent or user")
    message: str = Field(..., description="The message to be sent to the agent")
    metadata: Optional[dict] = Field(None, description="The metadata of the message")
    