import json
import httpx
from loguru import logger

from src.queue.celery_app import celery
from src.config import env
from src.services.redis_service import store_response_sync

from src.services.letta_service import letta_service
from src.utils.serialize_letta_response import serialize_letta_response

@celery.task(
    name="send_agent_message",
    autoretry_for=(httpx.HTTPError, httpx.TimeoutException),
    retry_backoff=True,
    retry_kwargs={"max_retries": 3},
    rate_limit=f"{env.LETTA_RPS}/s",
    bind=True,
    serializer='json'
)
def send_agent_message(self, message_id: str, agent_id: str, message: str) -> None:
    """
    Envia mensagem para o agente Letta e guarda a resposta em Redis (TTL 60 s) sob a chave `message_id`.
    """
    try:
        logger.info(f"Processing message {message_id} for agent {agent_id}")

        messages, usage = letta_service.send_message_sync(agent_id, message)
        
        serialized_messages = serialize_letta_response(messages)
        serialized_usage = serialize_letta_response(usage)

        data = {
            "messages": serialized_messages,
            "usage": serialized_usage,
            "agent_id": agent_id,
            "processed_at": str(self.request.id)
        }

        store_response_sync(message_id, json.dumps(data), ttl=60)
        
        logger.info(f"Successfully processed message {message_id} for agent {agent_id}")

    except (httpx.HTTPError, httpx.TimeoutException) as exc:
        logger.warning(f"Retryable error for message {message_id}, agent {agent_id}: {exc}")
        
        if self.request.retries >= self.max_retries:
            logger.error(f"Max retries exceeded for message {message_id}, agent {agent_id}")
            error_data = {
                "status": "error",
                "error": f"Máximo de tentativas excedido: {str(exc)}",
                "retry_count": self.request.retries,
                "max_retries": self.max_retries,
                "agent_id": agent_id,
                "message_id": message_id
            }
            store_response_sync(message_id, json.dumps(error_data), ttl=60)
            return None
        else:
            error_data = {
                "status": "retry",
                "error": str(exc),
                "retry_count": self.request.retries,
                "max_retries": self.max_retries
            }
            store_response_sync(message_id, json.dumps(error_data), ttl=30)
            raise

    except Exception as exc:
        logger.error(f"Fatal error processing message {message_id} for agent {agent_id}: {exc}")
        
        error_data = {
            "status": "error",
            "error": str(exc),
            "agent_id": agent_id,
            "message_id": message_id
        }
        store_response_sync(message_id, json.dumps(error_data), ttl=60)
        return None
