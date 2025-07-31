import httpx
import time
from loguru import logger
from celery.exceptions import SoftTimeLimitExceeded
from src.queue.celery_app import celery
from src.config.telemetry import get_tracer
from src.config import env
from src.services.redis_service import store_response_sync
from src.services.letta_service import letta_service, LettaAPIError, LettaAPITimeoutError
from src.utils.serialize_letta_response import serialize_letta_response
from src.services.prometheus_metrics import celery_tasks_total, celery_task_errors, celery_task_duration

tracer = get_tracer("message-tasks")

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
    soft_time_limit=env.CELERY_SOFT_TIME_LIMIT,
    time_limit=env.CELERY_TIME_LIMIT,
    bind=True,
    serializer='json',
    acks_late=True,
    reject_on_worker_lost=True,
)
def send_agent_message(self, message_id: str, agent_id: str, message: str, previous_message: str | None = None) -> None:
    start_time = time.time()
    task_name = "send_agent_message"
    
    with tracer.start_as_current_span("celery.send_agent_message") as span:
        span.set_attribute("celery.task_id", self.request.id)
        span.set_attribute("celery.message_id", message_id)
        span.set_attribute("celery.agent_id", agent_id)
        span.set_attribute("celery.message_length", len(message))
        span.set_attribute("celery.has_previous_message", previous_message is not None)
        span.set_attribute("celery.retry_count", self.request.retries)
        
        try:
            logger.info(f"[{self.request.id}] Processing message {message_id} for agent {agent_id}")
            
            if previous_message is not None:
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

            span.set_attribute("celery.success", True)
            span.set_attribute("celery.response_messages_count", len(messages))
            if usage:
                span.set_attribute("celery.usage_tokens", usage.total_tokens)

            logger.info(f"[{self.request.id}] Successfully processed message {message_id} for agent {agent_id}")
            
            # Record success metrics
            duration = time.time() - start_time
            celery_task_duration.labels(task_name=task_name).observe(duration)
            celery_tasks_total.labels(task_name=task_name, status="success").inc()

        except SoftTimeLimitExceeded as exc:
            span.record_exception(exc)
            span.set_attribute("error", True)
            span.set_attribute("celery.soft_timeout", True)
            logger.warning(f"[{self.request.id}] Soft time limit exceeded for message {message_id}: {exc}")
            store_response_sync(message_id, {
                "status": "retry",
                "error": "Soft time limit exceeded",
                "retry_count": self.request.retries,
                "max_retries": self.max_retries
            })
            # Record timeout error metrics
            duration = time.time() - start_time
            celery_task_duration.labels(task_name=task_name).observe(duration)
            celery_tasks_total.labels(task_name=task_name, status="timeout").inc()
            celery_task_errors.labels(task_name=task_name, error_type="timeout").inc()
            raise

        except LettaAPITimeoutError as exc:
            span.record_exception(exc)
            span.set_attribute("error", True)
            span.set_attribute("celery.letta_timeout", True)
            logger.warning(f"[{self.request.id}] Letta API timeout for message {message_id}: {exc}")
            if self.request.retries >= self.max_retries:
                span.set_attribute("celery.max_retries_exceeded", True)
                logger.error(f"[{self.request.id}] Max retries exceeded for message {message_id}")
                store_response_sync(message_id, {
                    "status": "error",
                    "error": f"Timeout da API Letta após {self.max_retries + 1} tentativas: {exc.message}",
                    "retry_count": self.request.retries,
                    "max_retries": self.max_retries,
                    "agent_id": agent_id,
                    "message_id": message_id
                })
                # Record API timeout error metrics
                duration = time.time() - start_time
                celery_task_duration.labels(task_name=task_name).observe(duration)
                celery_tasks_total.labels(task_name=task_name, status="api_timeout").inc()
                celery_task_errors.labels(task_name=task_name, error_type="api_timeout").inc()
                raise exc
            else:
                store_response_sync(message_id, {
                    "status": "retry",
                    "error": f"Timeout da API Letta: {exc.message}",
                    "retry_count": self.request.retries,
                    "max_retries": self.max_retries
                })
                # Record retry metrics
                duration = time.time() - start_time
                celery_task_duration.labels(task_name=task_name).observe(duration)
                celery_tasks_total.labels(task_name=task_name, status="retry").inc()
                raise

        except LettaAPIError as exc:
            span.record_exception(exc)
            span.set_attribute("error", True)
            span.set_attribute("celery.letta_api_error", True)
            if exc.status_code:
                span.set_attribute("celery.letta_status_code", exc.status_code)
            logger.warning(f"[{self.request.id}] Letta API error for message {message_id}: {exc}")
            if self.request.retries >= self.max_retries:
                span.set_attribute("celery.max_retries_exceeded", True)
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
            span.record_exception(exc)
            span.set_attribute("error", True)
            span.set_attribute("celery.http_error", True)
            logger.warning(f"[{self.request.id}] Retryable HTTP error for message {message_id}: {exc}")
            if self.request.retries >= self.max_retries:
                span.set_attribute("celery.max_retries_exceeded", True)
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
            span.record_exception(exc)
            span.set_attribute("error", True)
            span.set_attribute("celery.fatal_error", True)
            logger.error(f"[{self.request.id}] Fatal error processing message {message_id}: {exc}")

            if hasattr(exc, 'status_code') and hasattr(exc, 'detail'):
                error_detail = str(exc.detail) if exc.detail else "Unknown error"
                span.set_attribute("celery.http_status_code", exc.status_code)
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
