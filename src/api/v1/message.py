import json
import uuid
import asyncio
from fastapi import APIRouter, HTTPException, Response, Query
from loguru import logger

from src.queue.tasks.message_tasks import send_agent_message
from src.queue.celery_app import celery
from src.schemas.webhook_schema import AgentWebhookSchema, UserWebhookSchema
from src.services.letta_service import letta_service, LettaAPIError, LettaAPITimeoutError
from src.services.redis_service import get_response_async, store_response_async

router = APIRouter(prefix="/message", tags=["Messages"])


@router.post("/webhook/agent")
async def agent_webhook(request: AgentWebhookSchema):
    try:
        message_id = str(uuid.uuid4())
        
        task_result = send_agent_message.delay(message_id=message_id, agent_id=request.agent_id, message=request.message)

        await store_response_async(f"{message_id}_task_id", task_result.id, ttl=300)

        try:
            await asyncio.wait_for(asyncio.to_thread(lambda: task_result.ready() or True), timeout=2.0)
        except asyncio.TimeoutError:
            logger.warning(f"Task {message_id} não foi aceita rapidamente, mas continua processando")
        
        return {
            "message_id": message_id,
            "status": "processing",
            "polling_endpoint": f"/api/v1/message/response?message_id={message_id}"
        }
    except LettaAPITimeoutError as e:
        logger.error(f"Letta API timeout for agent {request.agent_id}: {e}")
        raise HTTPException(status_code=408, detail=f"Timeout da API Letta: {e.message}")
    except LettaAPIError as e:
        logger.error(f"Letta API error for agent {request.agent_id}: {e}")
        status_code = e.status_code if e.status_code else 500
        raise HTTPException(status_code=status_code, detail=f"Erro da API Letta: {e.message}")
    except Exception as e:
        logger.error(f"Error sending message to agent {request.agent_id} on Letta: {e}")
        raise HTTPException(status_code=500, detail=str(e))
    
@router.post("/webhook/user")
async def user_webhook(request: UserWebhookSchema):
    try:
        agent_id = await letta_service.get_agent_id(user_number=request.user_number)
        if agent_id is None:
            agent_id = await letta_service.create_agent(user_number=request.user_number)
        
        message_id = str(uuid.uuid4())
        
        if request.previous_message is not None:
            task_result = send_agent_message.delay(message_id=message_id, agent_id=agent_id, message=request.message, previous_message=request.previous_message)
        else:
            task_result = send_agent_message.delay(message_id=message_id, agent_id=agent_id, message=request.message)
        
        await store_response_async(f"{message_id}_task_id", task_result.id, ttl=300)
        
        try:
            await asyncio.wait_for(asyncio.to_thread(lambda: task_result.ready() or True), timeout=2.0)
        except asyncio.TimeoutError:
            logger.warning(f"Task {message_id} não foi aceita rapidamente, mas continua processando")
        
        return {
            "message_id": message_id,
            "status": "processing", 
            "polling_endpoint": f"/api/v1/message/response?message_id={message_id}"
        }
    except HTTPException as e:
        raise e
    except LettaAPITimeoutError as e:
        logger.error(f"Letta API timeout for user agent at {request.user_number}: {e}")
        raise HTTPException(status_code=408, detail=f"Timeout da API Letta: {e.message}")
    except LettaAPIError as e:
        logger.error(f"Letta API error for user agent at {request.user_number}: {e}")
        status_code = e.status_code if e.status_code else 500
        raise HTTPException(status_code=status_code, detail=f"Erro da API Letta: {e.message}")
    except Exception as e:
        logger.error(f"Error sending message to user agent at {request.user_number} on Letta: {e}")
        raise HTTPException(status_code=500, detail=str(e))

@router.get("/response")
async def get_agent_message_from_queue(message_id: str = Query(..., description="ID da mensagem para consultar")):
    # Validação do message_id como UUID
    try:
        uuid.UUID(message_id)
    except ValueError:
        raise HTTPException(
            status_code=400,
            detail="message_id deve ser um UUID válido"
        )
    
    try:
        # Primeiro, verifica se existe resposta direta no Redis
        resp = await get_response_async(message_id)
        if resp is not None:
            data = json.loads(resp)
            if data.get("status") == "error":
                return {
                    "status": "failed",
                    "error": data.get("error"),
                    "message": "Ocorreu um erro ao processar sua mensagem."
                }
            elif data.get("status") == "retry":
                return {
                    "status": "processing",
                    "message": f"Sua mensagem está sendo processada (tentativa {data.get('retry_count', 0)}/{data.get('max_retries', 3)}). Tente novamente em alguns segundos."
                }
            elif data.get("status") == "done":
                return {
                    "status": "completed",
                    "data": data
                }
            else:
                # Resposta com status desconhecido
                return {
                    "status": "completed",
                    "data": data
                }
        
        # Se não tem resposta direta, verifica o status da task
        task_id_resp = await get_response_async(f"{message_id}_task_id")
        if task_id_resp:
            task_result = celery.AsyncResult(task_id_resp)
            task_state = task_result.state
            
            logger.debug(f"Task {task_id_resp} for message {message_id} is in state: {task_state}")
            
            if task_state == "FAILURE":
                error_info = str(task_result.info) if task_result.info else "Erro desconhecido"
                logger.error(f"Task {task_id_resp} failed for message {message_id}: {error_info}")
                
                error_data = {
                    "status": "error",
                    "error": error_info,
                    "message_id": message_id,
                    "task_id": task_id_resp
                }
                await store_response_async(message_id, json.dumps(error_data), ttl=300)
                
                return {
                    "status": "failed",
                    "error": error_info,
                    "message": "Ocorreu um erro ao processar sua mensagem."
                }
            elif task_state == "SUCCESS":
                # Task concluída com sucesso, mas resposta ainda não apareceu no Redis
                logger.info(f"Task {task_id_resp} succeeded for message {message_id}, but response not yet in Redis")
                return {
                    "status": "processing",
                    "message": "Sua mensagem foi processada com sucesso. Os dados estão sendo finalizados."
                }
            elif task_state in ["PENDING", "RETRY", "STARTED"]:
                return {
                    "status": "processing",
                    "message": "Sua mensagem está sendo processada. Tente novamente em alguns segundos."
                }
        
        # não encontrou nem resposta nem task_id
        logger.warning(f"Message ID {message_id} not found in Redis - may have expired or never existed")
        raise HTTPException(
            status_code=404, 
            detail={
                "status": "not_found",
                "message": "Message ID não encontrado ou expirado. Verifique se o ID está correto ou envie uma nova mensagem.",
                "message_id": message_id
            }
        )
        
    except HTTPException as e:
        raise e
    except json.JSONDecodeError as e:
        logger.error(f"Error parsing JSON response from Redis: {e}")
        raise HTTPException(status_code=500, detail="Invalid response format")
    except Exception as e:
        logger.error(f"Error getting message from Redis: {e}")
        raise HTTPException(status_code=500, detail=str(e))

@router.get("/debug/task-status")
async def get_task_status_debug(message_id: str):
    """
    Endpoint para debug - mostra informações detalhadas sobre o status da task
    """
    try:
        from src.queue.celery_app import celery
        
        resp = await get_response_async(message_id)
        task_id_resp = await get_response_async(f"{message_id}_task_id")
        
        debug_info = {
            "message_id": message_id,
            "redis_response": json.loads(resp) if resp else None,
            "task_id": task_id_resp,
            "celery_info": None
        }
        
        if task_id_resp:
            task_result = celery.AsyncResult(task_id_resp)
            debug_info["celery_info"] = {
                "task_id": task_id_resp,
                "state": task_result.state,
                "info": str(task_result.info) if task_result.info else None,
                "result": str(task_result.result) if task_result.result else None,
                "traceback": str(task_result.traceback) if task_result.traceback else None,
                "ready": task_result.ready(),
                "successful": task_result.successful() if task_result.ready() else None,
                "failed": task_result.failed() if task_result.ready() else None
            }
        
        return debug_info
        
    except Exception as e:
        logger.error(f"Error in debug endpoint: {e}")
        raise HTTPException(status_code=500, detail=str(e))
