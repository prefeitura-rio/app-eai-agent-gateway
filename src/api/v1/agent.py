from fastapi import APIRouter, HTTPException
from loguru import logger

from src.schemas.create_agent_schema import CreateAgentRequestAndOverridePayload
from src.services.letta_service import letta_service

router = APIRouter(prefix="/agent", tags=["Agent"])

@router.post("/create")
async def create_agent_endpoint(request: CreateAgentRequestAndOverridePayload):
  try:
    user_number = request.user_number
    override_payload = request.model_dump(exclude={"user_number"})
    
    agent_id = await letta_service.create_agent(user_number=user_number, override_payload=override_payload)
    return {
      "agent_id": agent_id,
      "message": "Agent created successfully",
    }
  except Exception as e:
    logger.error(f"Error creating agent: {e}")
    raise HTTPException(status_code=500, detail=str(e))