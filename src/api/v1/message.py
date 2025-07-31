import json
import uuid
import asyncio
from fastapi import APIRouter, HTTPException, Response, Query
from loguru import logger

from src.queue.tasks.message_tasks import send_agent_message
from src.queue.celery_app import celery
from src.schemas.webhook_schema import AgentWebhookSchema, UserWebhookSchema
from src.services.letta_service import letta_service, LettaAPIError, LettaAPITimeoutError
from src.services.redis_service import get_response_async, store_response_async, store_task_status_async, get_task_status_async
from src.config.telemetry import get_tracer

router = APIRouter(prefix="/message", tags=["Messages"])
tracer = get_tracer("message-api")


@router.post("/webhook/agent")
async def agent_webhook(request: AgentWebhookSchema, response: Response):
    with tracer.start_as_current_span("api.agent_webhook") as span:
        span.set_attribute("api.agent_id", request.agent_id)
        span.set_attribute("api.message_length", len(request.message))
        
        try:
            message_id = str(uuid.uuid4())
            span.set_attribute("api.message_id", message_id)
            
            task_result = send_agent_message.delay(message_id=message_id, agent_id=request.agent_id, message=request.message)
            span.set_attribute("api.celery_task_id", task_result.id)

            await store_task_status_async(message_id, task_result.id)

            try:
                await asyncio.wait_for(asyncio.to_thread(lambda: task_result.ready() or True), timeout=2.0)
                span.set_attribute("api.task_ready_check", True)
            except asyncio.TimeoutError:
                span.set_attribute("api.task_ready_timeout", True)
                logger.warning(f"Task {message_id} não foi aceita rapidamente, mas continua processando")
            
            response.status_code = 201
            span.set_attribute("api.success", True)
            span.set_attribute("api.http_status_code", 201)
            
            return {
                "message_id": message_id,
                "status": "processing",
                "polling_endpoint": f"/api/v1/message/response?message_id={message_id}"
            }
        except LettaAPITimeoutError as e:
            span.record_exception(e)
            span.set_attribute("error", True)
            span.set_attribute("api.letta_timeout", True)
            logger.error(f"Letta API timeout for agent {request.agent_id}: {e}")
            raise HTTPException(status_code=408, detail=f"Timeout da API Letta: {e.message}")
        except LettaAPIError as e:
            span.record_exception(e)
            span.set_attribute("error", True)
            span.set_attribute("api.letta_api_error", True)
            if e.status_code:
                span.set_attribute("api.letta_status_code", e.status_code)
            logger.error(f"Letta API error for agent {request.agent_id}: {e}")
            status_code = e.status_code if e.status_code else 500
            raise HTTPException(status_code=status_code, detail=f"Erro da API Letta: {e.message}")
        except Exception as e:
            span.record_exception(e)
            span.set_attribute("error", True)
            span.set_attribute("api.unexpected_error", True)
            logger.error(f"Error sending message to agent {request.agent_id} on Letta: {e}")
            raise HTTPException(status_code=500, detail=str(e))
    
@router.post("/webhook/user")
async def user_webhook(request: UserWebhookSchema, response: Response):
    with tracer.start_as_current_span("api.user_webhook") as span:
        span.set_attribute("api.user_number", request.user_number)
        span.set_attribute("api.message_length", len(request.message))
        span.set_attribute("api.has_previous_message", request.previous_message is not None)
        
        try:
            agent_id = await letta_service.get_agent_id(user_number=request.user_number)
            if agent_id is None:
                span.set_attribute("api.agent_created", True)
                agent_id = await letta_service.create_agent(user_number=request.user_number)
            else:
                span.set_attribute("api.agent_found", True)
            
            span.set_attribute("api.agent_id", agent_id)
            
            message_id = str(uuid.uuid4())
            span.set_attribute("api.message_id", message_id)
            
            if request.previous_message is not None:
                task_result = send_agent_message.delay(message_id=message_id, agent_id=agent_id, message=request.message, previous_message=request.previous_message)
            else:
                task_result = send_agent_message.delay(message_id=message_id, agent_id=agent_id, message=request.message)
            
            span.set_attribute("api.celery_task_id", task_result.id)
            
            await store_task_status_async(message_id, task_result.id)
            
            try:
                await asyncio.wait_for(asyncio.to_thread(lambda: task_result.ready() or True), timeout=2.0)
                span.set_attribute("api.task_ready_check", True)
            except asyncio.TimeoutError:
                span.set_attribute("api.task_ready_timeout", True)
                logger.warning(f"Task {message_id} não foi aceita rapidamente, mas continua processando")
            
            response.status_code = 201
            span.set_attribute("api.success", True)
            span.set_attribute("api.http_status_code", 201)
            
            return {
                "message_id": message_id,
                "status": "processing", 
                "polling_endpoint": f"/api/v1/message/response?message_id={message_id}"
            }
        except HTTPException as e:
            span.record_exception(e)
            span.set_attribute("error", True)
            span.set_attribute("api.http_exception", True)
            span.set_attribute("api.http_status_code", e.status_code)
            raise e
        except LettaAPITimeoutError as e:
            span.record_exception(e)
            span.set_attribute("error", True)
            span.set_attribute("api.letta_timeout", True)
            logger.error(f"Letta API timeout for user agent at {request.user_number}: {e}")
            raise HTTPException(status_code=408, detail=f"Timeout da API Letta: {e.message}")
        except LettaAPIError as e:
            span.record_exception(e)
            span.set_attribute("error", True)
            span.set_attribute("api.letta_api_error", True)
            if e.status_code:
                span.set_attribute("api.letta_status_code", e.status_code)
            logger.error(f"Letta API error for user agent at {request.user_number}: {e}")
            status_code = e.status_code if e.status_code else 500
            raise HTTPException(status_code=status_code, detail=f"Erro da API Letta: {e.message}")
        except Exception as e:
            span.record_exception(e)
            span.set_attribute("error", True)
            span.set_attribute("api.unexpected_error", True)
            logger.error(f"Error sending message to user agent at {request.user_number} on Letta: {e}")
            raise HTTPException(status_code=500, detail=str(e))

@router.get("/response")
async def get_agent_message_from_queue(response_obj: Response, message_id: str = Query(..., description="ID da mensagem para consultar")):
    with tracer.start_as_current_span("api.get_message_response") as span:
        span.set_attribute("api.message_id", message_id)
        
        # Validação do message_id como UUID
        try:
            uuid.UUID(message_id)
            span.set_attribute("api.valid_uuid", True)
        except ValueError:
            span.set_attribute("api.valid_uuid", False)
            span.set_attribute("error", True)
            raise HTTPException(
                status_code=400,
                detail="message_id deve ser um UUID válido"
            )
        
        try:
            # Primeiro, verifica se existe resposta direta no Redis
            resp = await get_response_async(message_id)
            if resp is not None:
                span.set_attribute("api.redis_response_found", True)
                data = json.loads(resp)
                span.set_attribute("api.response_status", data.get("status"))
                
                if data.get("status") == "error":
                    span.set_attribute("api.response_error", True)
                    response_obj.status_code = 500
                    return {
                        "status": "failed",
                        "error": data.get("error"),
                        "message": "Ocorreu um erro ao processar sua mensagem."
                    }
                elif data.get("status") == "retry":
                    span.set_attribute("api.response_retry", True)
                    response_obj.status_code = 202
                    return {
                        "status": "processing",
                        "message": f"Sua mensagem está sendo processada (tentativa {data.get('retry_count', 0)}/{data.get('max_retries', 3)}). Tente novamente em alguns segundos."
                    }
                elif data.get("status") == "done":
                    span.set_attribute("api.response_done", True)
                    return {
                        "status": "completed",
                        "data": data
                    }
                else:
                    # Resposta com status desconhecido
                    span.set_attribute("api.response_unknown_status", True)
                    return {
                        "status": "completed",
                        "data": data
                    }
            
            span.set_attribute("api.redis_response_found", False)
            
            # Se não tem resposta direta, verifica o status da task
            task_id_resp = await get_task_status_async(message_id)
            if task_id_resp:
                span.set_attribute("api.task_id_found", True)
                span.set_attribute("api.task_id", task_id_resp)
                
                task_result = celery.AsyncResult(task_id_resp)
                task_state = task_result.state
                span.set_attribute("api.task_state", task_state)
                
                logger.debug(f"Task {task_id_resp} for message {message_id} is in state: {task_state}")
                
                if task_state == "FAILURE":
                    span.set_attribute("api.task_failed", True)
                    error_info = str(task_result.info) if task_result.info else "Erro desconhecido"
                    span.set_attribute("api.task_error", error_info)
                    logger.error(f"Task {task_id_resp} failed for message {message_id}: {error_info}")
                    
                    error_data = {
                        "status": "error",
                        "error": error_info,
                        "message_id": message_id,
                        "task_id": task_id_resp
                    }
                    await store_response_async(message_id, json.dumps(error_data))

                    response_obj.status_code = 500
                    return {
                        "status": "failed",
                        "error": error_info,
                        "message": "Ocorreu um erro ao processar sua mensagem."
                    }
                elif task_state == "SUCCESS":
                    span.set_attribute("api.task_success", True)
                    # Task concluída com sucesso, mas resposta ainda não apareceu no Redis
                    logger.info(f"Task {task_id_resp} succeeded for message {message_id}, but response not yet in Redis")
                    response_obj.status_code = 202
                    return {
                        "status": "processing",
                        "message": "Sua mensagem foi processada com sucesso. Os dados estão sendo finalizados."
                    }
                elif task_state in ["PENDING", "RETRY", "STARTED"]:
                    span.set_attribute("api.task_processing", True)
                    response_obj.status_code = 202
                    return {
                        "status": "processing",
                        "message": "Sua mensagem está sendo processada. Tente novamente em alguns segundos."
                    }
            else:
                span.set_attribute("api.task_id_found", False)
            
            # não encontrou nem resposta nem task_id
            span.set_attribute("api.not_found", True)
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
            span.record_exception(e)
            span.set_attribute("error", True)
            raise e
        except json.JSONDecodeError as e:
            span.record_exception(e)
            span.set_attribute("error", True)
            span.set_attribute("api.json_decode_error", True)
            logger.error(f"Error parsing JSON response from Redis: {e}")
            raise HTTPException(status_code=500, detail="Invalid response format")
        except Exception as e:
            span.record_exception(e)
            span.set_attribute("error", True)
            span.set_attribute("api.unexpected_error", True)
            logger.error(f"Error getting message from Redis: {e}")
            raise HTTPException(status_code=500, detail=str(e))

@router.get("/debug/task-status")
async def get_task_status_debug(message_id: str):
    """
    Endpoint para debug - mostra informações detalhadas sobre o status da task
    """
    with tracer.start_as_current_span("api.debug_task_status") as span:
        span.set_attribute("api.message_id", message_id)
        
        try:
            from src.queue.celery_app import celery
            
            resp = await get_response_async(message_id)
            task_id_resp = await get_response_async(f"{message_id}_task_id")
            
            span.set_attribute("api.redis_response_found", resp is not None)
            span.set_attribute("api.task_id_found", task_id_resp is not None)
            if task_id_resp:
                span.set_attribute("api.task_id", task_id_resp)
            
            debug_info = {
                "message_id": message_id,
                "redis_response": json.loads(resp) if resp else None,
                "task_id": task_id_resp,
                "celery_info": None
            }
            
            if task_id_resp:
                task_result = celery.AsyncResult(task_id_resp)
                span.set_attribute("api.task_state", task_result.state)
                
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
            
            span.set_attribute("api.success", True)
            return debug_info
            
        except Exception as e:
            span.record_exception(e)
            span.set_attribute("error", True)
            span.set_attribute("api.unexpected_error", True)
            logger.error(f"Error in debug endpoint: {e}")
            raise HTTPException(status_code=500, detail=str(e))
