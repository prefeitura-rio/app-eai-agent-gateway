import httpx
from loguru import logger
from celery.exceptions import SoftTimeLimitExceeded
from src.queue.celery_app import celery
from src.config import env
from src.services.redis_service import store_response_sync
from src.services.letta_service import letta_service, LettaAPIError, LettaAPITimeoutError
from src.utils.serialize_letta_response import serialize_letta_response


class SerializableHTTPError(Exception):
    """Exceção personalizada que pode ser serializada pelo Celery"""
    def __init__(self, status_code: int, detail: str):
        self.status_code = status_code
        self.detail = detail
        super().__init__(f"HTTP {status_code}: {detail}")


@celery.task(
    name="send_agent_message",
    autoretry_for=(httpx.HTTPError, httpx.TimeoutException, SoftTimeLimitExceeded, SerializableHTTPError, LettaAPIError, LettaAPITimeoutError),
    retry_backoff=True,
    retry_kwargs={"max_retries": 3},
    rate_limit=f"{env.LETTA_RPS}/s",
    soft_time_limit=50,
    time_limit=60,
    bind=True,
    serializer='json',
    acks_late=True,
    reject_on_worker_lost=True,
)
def send_agent_message(self, message_id: str, agent_id: str, message: str, previous_message: str | None = None) -> None:
    try:
        logger.info(f"[{self.request.id}] Processing message {message_id} for agent {agent_id}")
        
        if previous_message is not None or previous_message != "":
            logger.info(f"[{self.request.id}] Previous message found for agent {agent_id}, including in request. The previous message is: {previous_message}")
            messages, usage = letta_service.send_message_sync(agent_id, message, previous_message)
        else:
            messages, usage = letta_service.send_message_sync(agent_id, message)

        data = {
            "messages": serialize_letta_response(messages),
            "usage": serialize_letta_response(usage),
            "agent_id": agent_id,
            "processed_at": self.request.id,
            "status": "done"
        }
        store_response_sync(message_id, data)

        logger.info(f"[{self.request.id}] Successfully processed message {message_id} for agent {agent_id}")

    except SoftTimeLimitExceeded as exc:
        logger.warning(f"[{self.request.id}] Soft time limit exceeded for message {message_id}: {exc}")
        store_response_sync(message_id, {
            "status": "retry",
            "error": "Soft time limit exceeded",
            "retry_count": self.request.retries,
            "max_retries": self.max_retries
        })
        raise

    except LettaAPITimeoutError as exc:
        logger.warning(f"[{self.request.id}] Letta API timeout for message {message_id}: {exc}")
        if self.request.retries >= self.max_retries:
            logger.error(f"[{self.request.id}] Max retries exceeded for message {message_id}")
            store_response_sync(message_id, {
                "status": "error",
                "error": f"Timeout da API Letta após {self.max_retries + 1} tentativas: {exc.message}",
                "retry_count": self.request.retries,
                "max_retries": self.max_retries,
                "agent_id": agent_id,
                "message_id": message_id
            })
            raise exc
        else:
            store_response_sync(message_id, {
                "status": "retry",
                "error": f"Timeout da API Letta: {exc.message}",
                "retry_count": self.request.retries,
                "max_retries": self.max_retries
            })
            raise

    except LettaAPIError as exc:
        logger.warning(f"[{self.request.id}] Letta API error for message {message_id}: {exc}")
        if self.request.retries >= self.max_retries:
            logger.error(f"[{self.request.id}] Max retries exceeded for message {message_id}")
            error_msg = f"Erro da API Letta após {self.max_retries + 1} tentativas"
            if exc.status_code:
                error_msg += f" (HTTP {exc.status_code})"
            error_msg += f": {exc.message}"
            
            store_response_sync(message_id, {
                "status": "error",
                "error": error_msg,
                "retry_count": self.request.retries,
                "max_retries": self.max_retries,
                "agent_id": agent_id,
                "message_id": message_id
            })
            raise exc
        else:
            store_response_sync(message_id, {
                "status": "retry",
                "error": f"Erro da API Letta: {exc.message}",
                "retry_count": self.request.retries,
                "max_retries": self.max_retries
            })
            raise

    except (httpx.HTTPError, httpx.TimeoutException) as exc:
        logger.warning(f"[{self.request.id}] Retryable HTTP error for message {message_id}: {exc}")
        if self.request.retries >= self.max_retries:
            logger.error(f"[{self.request.id}] Max retries exceeded for message {message_id}")
            store_response_sync(message_id, {
                "status": "error",
                "error": f"Máximo de tentativas excedido: {str(exc)}",
                "retry_count": self.request.retries,
                "max_retries": self.max_retries,
                "agent_id": agent_id,
                "message_id": message_id
            })
            raise exc
        else:
            store_response_sync(message_id, {
                "status": "retry",
                "error": str(exc),
                "retry_count": self.request.retries,
                "max_retries": self.max_retries
            })
            raise

    except Exception as exc:
        logger.error(f"[{self.request.id}] Fatal error processing message {message_id}: {exc}")

        if hasattr(exc, 'status_code') and hasattr(exc, 'detail'):
            error_detail = str(exc.detail) if exc.detail else "Unknown error"
            store_response_sync(message_id, {
                "status": "error",
                "error": f"HTTP {exc.status_code}: {error_detail}",
                "agent_id": agent_id,
                "message_id": message_id
            })
            # Lançar uma exceção serializável
            raise SerializableHTTPError(exc.status_code, error_detail)
        else:
            store_response_sync(message_id, {
                "status": "error",
                "error": str(exc),
                "agent_id": agent_id,
                "message_id": message_id
            })
            raise exc
