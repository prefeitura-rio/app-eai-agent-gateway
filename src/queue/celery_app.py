from celery import Celery
from src.config import env

celery = Celery(
    "gateway",
    broker=env.REDIS_DSN,
    backend=env.REDIS_BACKEND 
)

celery.autodiscover_tasks(["src.queue.tasks"])

celery.conf.update(
    worker_concurrency=env.MAX_PARALLEL,
    worker_prefetch_multiplier=1,
    task_acks_late=True,
    task_serializer="json",
    result_serializer="json",
    accept_content=["json"],
    result_expires=3600,
)
