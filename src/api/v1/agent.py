from fastapi import APIRouter, HTTPException
from loguru import logger

from src.schemas.create_agent_schema import CreateAgentRequestAndOverridePayload
from src.schemas.delete_agent_schema import DeleteAgentRequest
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

@router.delete("/")
async def delete_agent_endpoint(request: DeleteAgentRequest):
  with tracer.start_as_current_span("api.delete_agent") as span:
    span.set_attribute("api.has_agent_id", bool(request.agent_id))
    span.set_attribute("api.has_tag_list", bool(request.tag_list))
    span.set_attribute("api.delete_all_agents", request.delete_all_agents)
    
    try:
      if request.delete_all_agents:
        result = await letta_service.delete_agent(delete_all_agents=True)
      elif request.agent_id:
        span.set_attribute("api.agent_id", request.agent_id)
        result = await letta_service.delete_agent(agent_id=request.agent_id)
      else:
        span.set_attribute("api.tag_list", request.tag_list)
        result = await letta_service.delete_agent(agent_id="", tag_list=request.tag_list)
      
      span.set_attribute("api.success", True)
      return result
    except Exception as e:
      span.record_exception(e)
      span.set_attribute("error", True)
      span.set_attribute("api.success", False)
      
      if request.delete_all_agents:
        logger.error(f"Error deleting all agents: {e}")
      elif request.agent_id:
        logger.error(f"Error deleting agent {request.agent_id}: {e}")
      else:
        logger.error(f"Error deleting agents with tags {request.tag_list}: {e}")
      
      raise HTTPException(status_code=500, detail=str(e))

@router.delete("/{agent_id}")
async def delete_agent_by_id_endpoint(agent_id: str):
  """Backwards compatibility endpoint for deleting an agent by ID"""
  with tracer.start_as_current_span("api.delete_agent_by_id") as span:
    span.set_attribute("api.agent_id", agent_id)
    
    try:
      result = await letta_service.delete_agent(agent_id=agent_id)
      span.set_attribute("api.success", True)
      return result
    except Exception as e:
      span.record_exception(e)
      span.set_attribute("error", True)
      span.set_attribute("api.success", False)
      logger.error(f"Error deleting agent {agent_id}: {e}")
      raise HTTPException(status_code=500, detail=str(e))