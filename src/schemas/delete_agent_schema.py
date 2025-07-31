from pydantic import BaseModel, Field, validator
from typing import Optional

class DeleteAgentRequest(BaseModel):
    agent_id: Optional[str] = Field(None, description="The ID of the agent to delete")
    tag_list: Optional[list[str]] = Field(None, description="List of tags to match agents for deletion")
    
    @validator('tag_list', pre=True, always=True)
    def validate_agent_deletion_params(cls, v, values):
        agent_id = values.get('agent_id')
        
        # Ensure exactly one of agent_id or tag_list is provided
        if not agent_id and not v:
            raise ValueError("Either 'agent_id' or 'tag_list' must be provided")
        
        if agent_id and v:
            raise ValueError("Only one of 'agent_id' or 'tag_list' should be provided, not both")
        
        return v
