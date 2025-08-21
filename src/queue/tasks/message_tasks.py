import time

import httpx
from celery.exceptions import SoftTimeLimitExceeded
from loguru import logger

from src.config import env
from src.config.telemetry import get_tracer
from src.queue.celery_app import celery
from src.services.agent_provider_factory import agent_provider_factory
from src.services.google_agent_engine_service import (
    GoogleAgentEngineAPIError,
    GoogleAgentEngineAPITimeoutError,
    GoogleAgentEngineRateLimitError,
)
from src.services.letta_service import (
    LettaAPIError,
    LettaAPITimeoutError,
    letta_service,
)
from src.services.prometheus_metrics import (
    celery_task_duration,
    celery_task_errors,
    celery_tasks_total,
)
from src.services.redis_service import store_response_sync, store_task_status_sync
from src.services.transcribe_service import (
    TranscriptionError,
    transcribe_service,
)
from src.utils.message_formatter import to_gateway_format

tracer = get_tracer("message-tasks")


class SerializableHTTPError(Exception):
    """Exceção personalizada que pode ser serializada pelo Celery"""

    def __init__(self, status_code: int, detail: str):
        self.status_code = status_code
        self.detail = detail
        super().__init__(f"HTTP {status_code}: {detail}")


@celery.task(
    name="process_user_message",
    autoretry_for=(
        httpx.HTTPError,
        httpx.TimeoutException,
        SoftTimeLimitExceeded,
        SerializableHTTPError,
        LettaAPIError,
        LettaAPITimeoutError,
        GoogleAgentEngineAPIError,
        GoogleAgentEngineAPITimeoutError,
        GoogleAgentEngineRateLimitError,
    ),
    retry_backoff=True,
    retry_kwargs={"max_retries": 3},
    soft_time_limit=env.CELERY_SOFT_TIME_LIMIT,
    time_limit=env.CELERY_TIME_LIMIT,
    bind=True,
    serializer="json",
    acks_late=True,
    reject_on_worker_lost=True,
)
def process_user_message(
    self,
    message_id: str,
    user_number: str,
    message: str,
    previous_message: str | None = None,
    provider: str = "google_agent_engine",
) -> None:
    start_time = time.time()
    task_name = "process_user_message"

    with tracer.start_as_current_span("celery.process_user_message") as span:
        span.set_attribute("celery.task_id", self.request.id)
        span.set_attribute("celery.message_id", message_id)
        span.set_attribute("celery.user_number", user_number)
        span.set_attribute("celery.message_length", len(message))
        span.set_attribute("celery.has_previous_message", previous_message is not None)
        span.set_attribute("celery.retry_count", self.request.retries)
        span.set_attribute("celery.provider", provider)

        try:
            logger.info(
                f"[{self.request.id}] Processing user message {message_id} for user {user_number} using provider {provider}",
            )

            # Get the appropriate provider
            agent_provider = agent_provider_factory.get_provider(provider)

            # First, try to get existing agent ID
            agent_id = agent_provider.get_agent_id_sync(user_number=user_number)
            if agent_id is None:
                span.set_attribute("celery.agent_created", True)
                logger.info(
                    f"[{self.request.id}] Creating new agent for user {user_number} using {provider}",
                )
                agent_id = agent_provider.create_agent_sync(user_number=user_number)
            else:
                span.set_attribute("celery.agent_found", True)
                logger.info(
                    f"[{self.request.id}] Found existing agent {agent_id} for user {user_number} using {provider}",
                )

            span.set_attribute("celery.agent_id", agent_id)

            # If message is an audio URL, transcribe it before sending
            transcript_text: str | None = None
            try:
                if transcribe_service.is_audio_url(message):
                    span.set_attribute("celery.has_audio_url", True)
                    logger.info(f"[{self.request.id}] Transcrevendo áudio da URL para user {user_number}")
                    transcript_text = transcribe_service.transcribe_from_url_sync(message)
                    span.set_attribute("celery.transcript_length", len(transcript_text) if transcript_text else 0)
                    # Fallback se transcrição não retornar conteúdo
                    if transcript_text and transcript_text.strip() and transcript_text != "Áudio sem conteúdo reconhecível":
                        message = transcript_text
                    else:
                        logger.warning(f"[{self.request.id}] Transcrição sem conteúdo útil; aplicando fallback de mensagem")
                        message = "Ajuda"
            except TranscriptionError as te:
                span.record_exception(te)
                span.set_attribute("error", True)
                span.set_attribute("celery.transcription_error", True)
                logger.warning(f"[{self.request.id}] Erro ao transcrever áudio: {te}")
                # Fallback para não bloquear o fluxo
                message = "Ajuda"

            # Now send the message using the provider
            if previous_message is not None:
                messages, usage = agent_provider.send_message_sync(
                    agent_id,
                    message,
                    previous_message,
                )
            else:
                messages, usage = agent_provider.send_message_sync(agent_id, message)

            # Converter para o formato padrão do gateway usando message_formatter
            formatted_response = to_gateway_format(
                messages=messages,
                thread_id=agent_id,
                use_whatsapp_format=True
            )
            data = formatted_response.get("data", {})
            data.update({
                "agent_id": agent_id,
                "processed_at": self.request.id,
                "status": "done",
            })
            if transcript_text is not None:
                data["transcript"] = transcript_text
            store_response_sync(message_id, data)
            store_task_status_sync(message_id, self.request.id)

            span.set_attribute("celery.success", True)
            span.set_attribute("celery.response_messages_count", len(messages))
            if usage:
                span.set_attribute("celery.usage_tokens", usage.total_tokens)

            logger.info(
                f"[{self.request.id}] Successfully processed user message {message_id} for agent {agent_id}",
            )

            # Record success metrics
            duration = time.time() - start_time
            celery_task_duration.labels(task_name=task_name).observe(duration)
            celery_tasks_total.labels(task_name=task_name, status="success").inc()

        except SoftTimeLimitExceeded as exc:
            span.record_exception(exc)
            span.set_attribute("error", True)
            span.set_attribute("celery.success", False)
            span.set_attribute("celery.soft_timeout", True)
            logger.warning(
                f"[{self.request.id}] Soft time limit exceeded for user message {message_id}: {exc}",
            )
            store_response_sync(
                message_id,
                {
                    "status": "retry",
                    "error": "Soft time limit exceeded",
                    "retry_count": self.request.retries,
                    "max_retries": self.max_retries,
                },
            )
            # Record timeout error metrics
            duration = time.time() - start_time
            celery_task_duration.labels(task_name=task_name).observe(duration)
            celery_tasks_total.labels(task_name=task_name, status="timeout").inc()
            celery_task_errors.labels(task_name=task_name, error_type="timeout").inc()
            raise

        except LettaAPITimeoutError as exc:
            span.record_exception(exc)
            span.set_attribute("error", True)
            span.set_attribute("celery.success", False)
            span.set_attribute("celery.letta_timeout", True)
            logger.warning(
                f"[{self.request.id}] Letta API timeout for user message {message_id}: {exc}",
            )
            if self.request.retries >= self.max_retries:
                span.set_attribute("celery.max_retries_exceeded", True)
                logger.error(
                    f"[{self.request.id}] Max retries exceeded for user message {message_id}",
                )
                store_response_sync(
                    message_id,
                    {
                        "status": "error",
                        "error": f"Timeout da API Letta após {self.max_retries + 1} tentativas: {exc.message}",
                        "retry_count": self.request.retries,
                        "max_retries": self.max_retries,
                        "user_number": user_number,
                        "message_id": message_id,
                    },
                )
                # Record API timeout error metrics
                duration = time.time() - start_time
                celery_task_duration.labels(task_name=task_name).observe(duration)
                celery_tasks_total.labels(
                    task_name=task_name,
                    status="api_timeout",
                ).inc()
                celery_task_errors.labels(
                    task_name=task_name,
                    error_type="api_timeout",
                ).inc()
                raise exc
            store_response_sync(
                message_id,
                {
                    "status": "retry",
                    "error": f"Timeout da API Letta: {exc.message}",
                    "retry_count": self.request.retries,
                    "max_retries": self.max_retries,
                },
            )
            # Record retry metrics
            duration = time.time() - start_time
            celery_task_duration.labels(task_name=task_name).observe(duration)
            celery_tasks_total.labels(task_name=task_name, status="retry").inc()
            raise

        except LettaAPIError as exc:
            span.record_exception(exc)
            span.set_attribute("error", True)
            span.set_attribute("celery.success", False)
            span.set_attribute("celery.letta_api_error", True)
            if exc.status_code:
                span.set_attribute("celery.letta_status_code", exc.status_code)

            # Handle agent not found (404) by invalidating cache
            if exc.status_code == 404 and exc.agent_id:
                span.set_attribute("celery.agent_not_found", True)
                logger.warning(
                    f"[{self.request.id}] Agent {exc.agent_id} not found for user {user_number}, invalidating cache",
                )
                # Invalidate cache for this user
                try:
                    letta_service._invalidate_agent_cache_sync(user_number)
                except Exception as cache_error:
                    logger.warning(
                        f"Failed to invalidate cache for user {user_number}: {cache_error}"
                    )
            logger.warning(
                f"[{self.request.id}] Letta API error for user message {message_id}: {exc}",
            )
            if self.request.retries >= self.max_retries:
                span.set_attribute("celery.max_retries_exceeded", True)
                logger.error(
                    f"[{self.request.id}] Max retries exceeded for user message {message_id}",
                )
                error_msg = f"Erro da API Letta após {self.max_retries + 1} tentativas"
                if exc.status_code:
                    error_msg += f" (HTTP {exc.status_code})"
                error_msg += f": {exc.message}"

                store_response_sync(
                    message_id,
                    {
                        "status": "error",
                        "error": error_msg,
                        "retry_count": self.request.retries,
                        "max_retries": self.max_retries,
                        "user_number": user_number,
                        "message_id": message_id,
                    },
                )
                raise exc
            store_response_sync(
                message_id,
                {
                    "status": "retry",
                    "error": f"Erro da API Letta: {exc.message}",
                    "retry_count": self.request.retries,
                    "max_retries": self.max_retries,
                },
            )
            raise

        except GoogleAgentEngineRateLimitError as exc:
            span.record_exception(exc)
            span.set_attribute("error", True)
            span.set_attribute("celery.success", False)
            span.set_attribute("celery.google_rate_limit", True)
            if exc.retry_after:
                span.set_attribute("celery.retry_after", exc.retry_after)

            logger.warning(
                f"[{self.request.id}] Google Agent Engine rate limit for user message {message_id}: {exc}",
            )

            # For rate limit errors, we always retry with exponential backoff
            # The rate limiter will handle the actual delay
            store_response_sync(
                message_id,
                {
                    "status": "retry",
                    "error": f"Google API rate limit: {exc.message}",
                    "retry_count": self.request.retries,
                    "max_retries": self.max_retries,
                    "retry_after": exc.retry_after,
                },
            )
            # Record rate limit metrics
            duration = time.time() - start_time
            celery_task_duration.labels(task_name=task_name).observe(duration)
            celery_tasks_total.labels(task_name=task_name, status="rate_limit").inc()
            celery_task_errors.labels(task_name=task_name, error_type="rate_limit").inc()
            raise

        except GoogleAgentEngineAPITimeoutError as exc:
            span.record_exception(exc)
            span.set_attribute("error", True)
            span.set_attribute("celery.success", False)
            span.set_attribute("celery.google_timeout", True)
            logger.warning(
                f"[{self.request.id}] Google Agent Engine API timeout for user message {message_id}: {exc}",
            )
            if self.request.retries >= self.max_retries:
                span.set_attribute("celery.max_retries_exceeded", True)
                logger.error(
                    f"[{self.request.id}] Max retries exceeded for user message {message_id}",
                )
                store_response_sync(
                    message_id,
                    {
                        "status": "error",
                        "error": f"Timeout da API Google Agent Engine após {self.max_retries + 1} tentativas: {exc.message}",
                        "retry_count": self.request.retries,
                        "max_retries": self.max_retries,
                        "user_number": user_number,
                        "message_id": message_id,
                    },
                )
                # Record API timeout error metrics
                duration = time.time() - start_time
                celery_task_duration.labels(task_name=task_name).observe(duration)
                celery_tasks_total.labels(
                    task_name=task_name,
                    status="api_timeout",
                ).inc()
                celery_task_errors.labels(
                    task_name=task_name,
                    error_type="api_timeout",
                ).inc()
                raise exc
            store_response_sync(
                message_id,
                {
                    "status": "retry",
                    "error": f"Timeout da API Google Agent Engine: {exc.message}",
                    "retry_count": self.request.retries,
                    "max_retries": self.max_retries,
                },
            )
            # Record retry metrics
            duration = time.time() - start_time
            celery_task_duration.labels(task_name=task_name).observe(duration)
            celery_tasks_total.labels(task_name=task_name, status="retry").inc()
            raise

        except GoogleAgentEngineAPIError as exc:
            span.record_exception(exc)
            span.set_attribute("error", True)
            span.set_attribute("celery.success", False)
            span.set_attribute("celery.google_api_error", True)
            logger.warning(
                f"[{self.request.id}] Google Agent Engine API error for user message {message_id}: {exc}",
            )
            if self.request.retries >= self.max_retries:
                span.set_attribute("celery.max_retries_exceeded", True)
                logger.error(
                    f"[{self.request.id}] Max retries exceeded for user message {message_id}",
                )
                error_msg = f"Erro da API Google Agent Engine após {self.max_retries + 1} tentativas: {exc.message}"

                store_response_sync(
                    message_id,
                    {
                        "status": "error",
                        "error": error_msg,
                        "retry_count": self.request.retries,
                        "max_retries": self.max_retries,
                        "user_number": user_number,
                        "message_id": message_id,
                    },
                )
                raise exc
            store_response_sync(
                message_id,
                {
                    "status": "retry",
                    "error": f"Erro da API Google Agent Engine: {exc.message}",
                    "retry_count": self.request.retries,
                    "max_retries": self.max_retries,
                },
            )
            raise

        except (httpx.HTTPError, httpx.TimeoutException) as exc:
            span.record_exception(exc)
            span.set_attribute("error", True)
            span.set_attribute("celery.success", False)
            span.set_attribute("celery.http_error", True)
            logger.warning(
                f"[{self.request.id}] Retryable HTTP error for user message {message_id}: {exc}",
            )
            if self.request.retries >= self.max_retries:
                span.set_attribute("celery.max_retries_exceeded", True)
                logger.error(
                    f"[{self.request.id}] Max retries exceeded for user message {message_id}",
                )
                store_response_sync(
                    message_id,
                    {
                        "status": "error",
                        "error": f"Máximo de tentativas excedido: {exc!s}",
                        "retry_count": self.request.retries,
                        "max_retries": self.max_retries,
                        "user_number": user_number,
                        "message_id": message_id,
                    },
                )
                raise exc
            store_response_sync(
                message_id,
                {
                    "status": "retry",
                    "error": str(exc),
                    "retry_count": self.request.retries,
                    "max_retries": self.max_retries,
                },
            )
            raise

        except Exception as exc:
            span.record_exception(exc)
            span.set_attribute("error", True)
            span.set_attribute("celery.success", False)
            span.set_attribute("celery.fatal_error", True)
            logger.error(
                f"[{self.request.id}] Fatal error processing user message {message_id}: {exc}",
            )

            if hasattr(exc, "status_code") and hasattr(exc, "detail"):
                error_detail = str(exc.detail) if exc.detail else "Unknown error"
                span.set_attribute("celery.http_status_code", exc.status_code)
                store_response_sync(
                    message_id,
                    {
                        "status": "error",
                        "error": f"HTTP {exc.status_code}: {error_detail}",
                        "user_number": user_number,
                        "message_id": message_id,
                    },
                )
                # Lançar uma exceção serializável
                raise SerializableHTTPError(exc.status_code, error_detail)
            store_response_sync(
                message_id,
                {
                    "status": "error",
                    "error": str(exc),
                    "user_number": user_number,
                    "message_id": message_id,
                },
            )
            raise exc


@celery.task(
    name="send_agent_message",
    autoretry_for=(
        httpx.HTTPError,
        httpx.TimeoutException,
        SoftTimeLimitExceeded,
        SerializableHTTPError,
        LettaAPIError,
        LettaAPITimeoutError,
        GoogleAgentEngineRateLimitError,
    ),
    retry_backoff=True,
    retry_kwargs={"max_retries": 3},
    soft_time_limit=env.CELERY_SOFT_TIME_LIMIT,
    time_limit=env.CELERY_TIME_LIMIT,
    bind=True,
    serializer="json",
    acks_late=True,
    reject_on_worker_lost=True,
)
def send_agent_message(
    self,
    message_id: str,
    agent_id: str,
    message: str,
    previous_message: str | None = None,
) -> None:
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
            logger.info(
                f"[{self.request.id}] Processing message {message_id} for agent {agent_id}",
            )

            # Se for URL de áudio, transcreve antes de enviar
            transcript_text: str | None = None
            try:
                if transcribe_service.is_audio_url(message):
                    span.set_attribute("celery.has_audio_url", True)
                    logger.info(f"[{self.request.id}] Transcrevendo áudio da URL para agent {agent_id}")
                    transcript_text = transcribe_service.transcribe_from_url_sync(message)
                    span.set_attribute("celery.transcript_length", len(transcript_text) if transcript_text else 0)
                    if transcript_text and transcript_text.strip() and transcript_text != "Áudio sem conteúdo reconhecível":
                        message = transcript_text
                    else:
                        logger.warning(f"[{self.request.id}] Transcrição sem conteúdo útil; aplicando fallback de mensagem")
                        message = "Ajuda"
            except TranscriptionError as te:
                span.record_exception(te)
                span.set_attribute("error", True)
                span.set_attribute("celery.transcription_error", True)
                logger.warning(f"[{self.request.id}] Erro ao transcrever áudio: {te}")
                message = "Ajuda"

            if previous_message is not None:
                messages, usage = letta_service.send_message_sync(
                    agent_id,
                    message,
                    previous_message,
                )
            else:
                messages, usage = letta_service.send_message_sync(agent_id, message)

            # Converter para o formato padrão do gateway usando message_formatter
            formatted_response = to_gateway_format(
                messages=messages,
                thread_id=agent_id,
                use_whatsapp_format=True
            )
            data = formatted_response.get("data", {})
            data.update({
                "agent_id": agent_id,
                "processed_at": self.request.id,
                "status": "done",
            })
            if transcript_text is not None:
                data["transcript"] = transcript_text
            store_response_sync(message_id, data)

            span.set_attribute("celery.success", True)
            span.set_attribute("celery.response_messages_count", len(messages))
            if usage:
                span.set_attribute("celery.usage_tokens", usage.total_tokens)

            logger.info(
                f"[{self.request.id}] Successfully processed message {message_id} for agent {agent_id}",
            )

            # Record success metrics
            duration = time.time() - start_time
            celery_task_duration.labels(task_name=task_name).observe(duration)
            celery_tasks_total.labels(task_name=task_name, status="success").inc()

        except SoftTimeLimitExceeded as exc:
            span.record_exception(exc)
            span.set_attribute("error", True)
            span.set_attribute("celery.success", False)
            span.set_attribute("celery.soft_timeout", True)
            logger.warning(
                f"[{self.request.id}] Soft time limit exceeded for message {message_id}: {exc}",
            )
            store_response_sync(
                message_id,
                {
                    "status": "retry",
                    "error": "Soft time limit exceeded",
                    "retry_count": self.request.retries,
                    "max_retries": self.max_retries,
                },
            )
            # Record timeout error metrics
            duration = time.time() - start_time
            celery_task_duration.labels(task_name=task_name).observe(duration)
            celery_tasks_total.labels(task_name=task_name, status="timeout").inc()
            celery_task_errors.labels(task_name=task_name, error_type="timeout").inc()
            raise

        except LettaAPITimeoutError as exc:
            span.record_exception(exc)
            span.set_attribute("error", True)
            span.set_attribute("celery.success", False)
            span.set_attribute("celery.letta_timeout", True)
            logger.warning(
                f"[{self.request.id}] Letta API timeout for message {message_id}: {exc}",
            )
            if self.request.retries >= self.max_retries:
                span.set_attribute("celery.max_retries_exceeded", True)
                logger.error(
                    f"[{self.request.id}] Max retries exceeded for message {message_id}",
                )
                store_response_sync(
                    message_id,
                    {
                        "status": "error",
                        "error": f"Timeout da API Letta após {self.max_retries + 1} tentativas: {exc.message}",
                        "retry_count": self.request.retries,
                        "max_retries": self.max_retries,
                        "agent_id": agent_id,
                        "message_id": message_id,
                    },
                )
                # Record API timeout error metrics
                duration = time.time() - start_time
                celery_task_duration.labels(task_name=task_name).observe(duration)
                celery_tasks_total.labels(
                    task_name=task_name,
                    status="api_timeout",
                ).inc()
                celery_task_errors.labels(
                    task_name=task_name,
                    error_type="api_timeout",
                ).inc()
                raise exc
            store_response_sync(
                message_id,
                {
                    "status": "retry",
                    "error": f"Timeout da API Letta: {exc.message}",
                    "retry_count": self.request.retries,
                    "max_retries": self.max_retries,
                },
            )
            # Record retry metrics
            duration = time.time() - start_time
            celery_task_duration.labels(task_name=task_name).observe(duration)
            celery_tasks_total.labels(task_name=task_name, status="retry").inc()
            raise

        except LettaAPIError as exc:
            span.record_exception(exc)
            span.set_attribute("error", True)
            span.set_attribute("celery.success", False)
            span.set_attribute("celery.letta_api_error", True)
            if exc.status_code:
                span.set_attribute("celery.letta_status_code", exc.status_code)
            logger.warning(
                f"[{self.request.id}] Letta API error for message {message_id}: {exc}",
            )
            if self.request.retries >= self.max_retries:
                span.set_attribute("celery.max_retries_exceeded", True)
                logger.error(
                    f"[{self.request.id}] Max retries exceeded for message {message_id}",
                )
                error_msg = f"Erro da API Letta após {self.max_retries + 1} tentativas"
                if exc.status_code:
                    error_msg += f" (HTTP {exc.status_code})"
                error_msg += f": {exc.message}"

                store_response_sync(
                    message_id,
                    {
                        "status": "error",
                        "error": error_msg,
                        "retry_count": self.request.retries,
                        "max_retries": self.max_retries,
                        "agent_id": agent_id,
                        "message_id": message_id,
                    },
                )
                raise exc
            store_response_sync(
                message_id,
                {
                    "status": "retry",
                    "error": f"Erro da API Letta: {exc.message}",
                    "retry_count": self.request.retries,
                    "max_retries": self.max_retries,
                },
            )
            raise

        except GoogleAgentEngineRateLimitError as exc:
            span.record_exception(exc)
            span.set_attribute("error", True)
            span.set_attribute("celery.success", False)
            span.set_attribute("celery.google_rate_limit", True)
            if exc.retry_after:
                span.set_attribute("celery.retry_after", exc.retry_after)

            logger.warning(
                f"[{self.request.id}] Google Agent Engine rate limit for message {message_id}: {exc}",
            )

            # For rate limit errors, we always retry with exponential backoff
            # The rate limiter will handle the actual delay
            store_response_sync(
                message_id,
                {
                    "status": "retry",
                    "error": f"Google API rate limit: {exc.message}",
                    "retry_count": self.request.retries,
                    "max_retries": self.max_retries,
                    "retry_after": exc.retry_after,
                },
            )
            # Record rate limit metrics
            duration = time.time() - start_time
            celery_task_duration.labels(task_name=task_name).observe(duration)
            celery_tasks_total.labels(task_name=task_name, status="rate_limit").inc()
            celery_task_errors.labels(task_name=task_name, error_type="rate_limit").inc()
            raise

        except GoogleAgentEngineAPITimeoutError as exc:
            span.record_exception(exc)
            span.set_attribute("error", True)
            span.set_attribute("celery.success", False)
            span.set_attribute("celery.google_timeout", True)
            logger.warning(
                f"[{self.request.id}] Google Agent Engine API timeout for message {message_id}: {exc}",
            )
            if self.request.retries >= self.max_retries:
                span.set_attribute("celery.max_retries_exceeded", True)
                logger.error(
                    f"[{self.request.id}] Max retries exceeded for message {message_id}",
                )
                store_response_sync(
                    message_id,
                    {
                        "status": "error",
                        "error": f"Timeout da API Google Agent Engine após {self.max_retries + 1} tentativas: {exc.message}",
                        "retry_count": self.request.retries,
                        "max_retries": self.max_retries,
                        "agent_id": agent_id,
                        "message_id": message_id,
                    },
                )
                # Record API timeout error metrics
                duration = time.time() - start_time
                celery_task_duration.labels(task_name=task_name).observe(duration)
                celery_tasks_total.labels(
                    task_name=task_name,
                    status="api_timeout",
                ).inc()
                celery_task_errors.labels(
                    task_name=task_name,
                    error_type="api_timeout",
                ).inc()
                raise exc
            store_response_sync(
                message_id,
                {
                    "status": "retry",
                    "error": f"Timeout da API Google Agent Engine: {exc.message}",
                    "retry_count": self.request.retries,
                    "max_retries": self.max_retries,
                },
            )
            # Record retry metrics
            duration = time.time() - start_time
            celery_task_duration.labels(task_name=task_name).observe(duration)
            celery_tasks_total.labels(task_name=task_name, status="retry").inc()
            raise

        except GoogleAgentEngineAPIError as exc:
            span.record_exception(exc)
            span.set_attribute("error", True)
            span.set_attribute("celery.success", False)
            span.set_attribute("celery.google_api_error", True)
            logger.warning(
                f"[{self.request.id}] Google Agent Engine API error for message {message_id}: {exc}",
            )
            if self.request.retries >= self.max_retries:
                span.set_attribute("celery.max_retries_exceeded", True)
                logger.error(
                    f"[{self.request.id}] Max retries exceeded for message {message_id}",
                )
                error_msg = f"Erro da API Google Agent Engine após {self.max_retries + 1} tentativas: {exc.message}"

                store_response_sync(
                    message_id,
                    {
                        "status": "error",
                        "error": error_msg,
                        "retry_count": self.request.retries,
                        "max_retries": self.max_retries,
                        "agent_id": agent_id,
                        "message_id": message_id,
                    },
                )
                raise exc
            store_response_sync(
                message_id,
                {
                    "status": "retry",
                    "error": f"Erro da API Google Agent Engine: {exc.message}",
                    "retry_count": self.request.retries,
                    "max_retries": self.max_retries,
                },
            )
            raise

        except (httpx.HTTPError, httpx.TimeoutException) as exc:
            span.record_exception(exc)
            span.set_attribute("error", True)
            span.set_attribute("celery.success", False)
            span.set_attribute("celery.http_error", True)
            logger.warning(
                f"[{self.request.id}] Retryable HTTP error for message {message_id}: {exc}",
            )
            if self.request.retries >= self.max_retries:
                span.set_attribute("celery.max_retries_exceeded", True)
                logger.error(
                    f"[{self.request.id}] Max retries exceeded for message {message_id}",
                )
                store_response_sync(
                    message_id,
                    {
                        "status": "error",
                        "error": f"Máximo de tentativas excedido: {exc!s}",
                        "retry_count": self.request.retries,
                        "max_retries": self.max_retries,
                        "agent_id": agent_id,
                        "message_id": message_id,
                    },
                )
                raise exc
            store_response_sync(
                message_id,
                {
                    "status": "retry",
                    "error": str(exc),
                    "retry_count": self.request.retries,
                    "max_retries": self.max_retries,
                },
            )
            raise

        except Exception as exc:
            span.record_exception(exc)
            span.set_attribute("error", True)
            span.set_attribute("celery.success", False)
            span.set_attribute("celery.fatal_error", True)
            logger.error(
                f"[{self.request.id}] Fatal error processing message {message_id}: {exc}",
            )

            if hasattr(exc, "status_code") and hasattr(exc, "detail"):
                error_detail = str(exc.detail) if exc.detail else "Unknown error"
                span.set_attribute("celery.http_status_code", exc.status_code)
                store_response_sync(
                    message_id,
                    {
                        "status": "error",
                        "error": f"HTTP {exc.status_code}: {error_detail}",
                        "agent_id": agent_id,
                        "message_id": message_id,
                    },
                )
                # Lançar uma exceção serializável
                raise SerializableHTTPError(exc.status_code, error_detail)
            store_response_sync(
                message_id,
                {
                    "status": "error",
                    "error": str(exc),
                    "agent_id": agent_id,
                    "message_id": message_id,
                },
            )
            raise exc
