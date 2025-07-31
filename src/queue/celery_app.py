from celery import Celery
from src.config import env
from src.config.telemetry import setup_telemetry, instrument_celery

# Setup OpenTelemetry before creating Celery app
setup_telemetry()

celery = Celery(
    "gateway",
    broker=env.REDIS_DSN,
    backend=env.REDIS_BACKEND 
)

celery.autodiscover_tasks(["src.queue.tasks"])

celery.conf.update(
    worker_prefetch_multiplier=1,
    task_acks_late=True,
    task_serializer="json",
    result_serializer="json",
    accept_content=["json"],
    result_expires=3600,
    worker_pool=env.CELERY_WORKER_POOL,
    worker_concurrency=env.MAX_PARALLEL if env.CELERY_WORKER_POOL == 'prefork' else None,
)

# Instrument Celery with OpenTelemetry
instrument_celery()
