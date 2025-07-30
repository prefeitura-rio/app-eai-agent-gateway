from fastapi import APIRouter, HTTPException
from loguru import logger

from src.schemas.create_agent_schema import CreateAgentRequestAndOverridePayload
from src.services.letta_service import letta_service
from src.config.telemetry import get_tracer

router = APIRouter(prefix="/agent", tags=["Agent"])
tracer = get_tracer("agent-api")

@router.post("/create")
async def create_agent_endpoint(request: CreateAgentRequestAndOverridePayload):
  with tracer.start_as_current_span("api.create_agent") as span:
    span.set_attribute("api.user_number", request.user_number)
    span.set_attribute("api.has_override_payload", bool(request.model_dump(exclude={"user_number"})))
    
    try:
      user_number = request.user_number
      override_payload = request.model_dump(exclude={"user_number"})
      
      agent_id = await letta_service.create_agent(user_number=user_number, override_payload=override_payload)
      
      span.set_attribute("api.agent_id", agent_id)
      span.set_attribute("api.success", True)
      
      return {
        "agent_id": agent_id,
        "message": "Agent created successfully",
      }
    except Exception as e:
      span.record_exception(e)
      span.set_attribute("error", True)
      span.set_attribute("api.success", False)
      logger.error(f"Error creating agent: {e}")
      raise HTTPException(status_code=500, detail=str(e))

@router.delete("/{agent_id}")
async def delete_agent_endpoint(agent_id: str):
  try:
    await letta_service.delete_agent(agent_id=agent_id)
    return {
      "message": f"Agent {agent_id} deleted successfully",
    }
  except Exception as e:
    logger.error(f"Error deleting agent: {e}")
    raise HTTPException(status_code=500, detail=str(e))