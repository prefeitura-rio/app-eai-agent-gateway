from fastapi import APIRouter, HTTPException
from loguru import logger

from src.schemas.webhook_schema import WebhookSchema
from src.services.letta_service import letta_service

router = APIRouter(prefix="/message", tags=["Messages"])


@router.post("/webhook")
async def webhook(request: WebhookSchema):
    try:
        messages, usage = await letta_service.send_message(request.agent_id, request.message)
        
        if messages is None or usage is None:
            # TODO: Call LangGraph Fallback Agent to handle the message
            pass
        
        return {"messages": messages, "usage": usage}
    except Exception as e:
        logger.error(f"Error sending message to agent {request.agent_id} on Letta: {e}")
        raise HTTPException(status_code=500, detail=str(e))

@router.get("/message")
async def get_message(message_id: str):
    return {"message": "Hello, World!"}