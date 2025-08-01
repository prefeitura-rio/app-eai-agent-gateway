import time

from loguru import logger
from prometheus_client import Counter, Gauge, Histogram, generate_latest

from src.queue.celery_app import celery
from src.services.redis_service import sync_client

# Celery Queue Metrics
celery_queue_size = Gauge(
    "celery_queue_size",
    "Number of tasks in Celery queue",
    ["queue_name"],
)
celery_active_tasks = Gauge(
    "celery_active_tasks",
    "Number of active tasks",
    ["worker_name"],
)
celery_worker_count = Gauge("celery_worker_count", "Number of active workers")
celery_task_duration = Histogram(
    "celery_task_duration_seconds",
    "Task execution duration",
    ["task_name"],
)

# Task Counters
celery_tasks_total = Counter(
    "celery_tasks_total",
    "Total number of tasks processed",
    ["task_name", "status"],
)
celery_task_errors = Counter(
    "celery_task_errors",
    "Total number of task errors",
    ["task_name", "error_type"],
)

# Redis Metrics
redis_connected = Gauge("redis_connected", "Redis connection status")


def collect_celery_metrics():
    """Collect Celery queue and worker metrics"""
    try:
        # Get queue size from Redis
        queue_size = sync_client.llen("celery")
        celery_queue_size.labels(queue_name="celery").set(queue_size)

        # Get worker stats
        inspect = celery.control.inspect()
        active = inspect.active()
        stats = inspect.stats()

        if active:
            total_active = sum(len(tasks) for tasks in active.values())
            celery_active_tasks.labels(worker_name="total").set(total_active)

            # Set individual worker active tasks
            for worker_name, tasks in active.items():
                celery_active_tasks.labels(worker_name=worker_name).set(len(tasks))

        if stats:
            celery_worker_count.set(len(stats))

        # Check Redis connection
        try:
            sync_client.ping()
            redis_connected.set(1)
        except Exception:
            redis_connected.set(0)

    except Exception as e:
        logger.error(f"Error collecting Celery metrics: {e}")


def get_metrics():
    """Generate Prometheus metrics"""
    collect_celery_metrics()
    return generate_latest()


def start_metrics_collector(interval=15):
    """Start background metrics collection"""
    import threading

    def collect_loop():
        while True:
            try:
                collect_celery_metrics()
                time.sleep(interval)
            except Exception as e:
                logger.error(f"Error in metrics collection loop: {e}")
                time.sleep(interval)

    thread = threading.Thread(target=collect_loop, daemon=True)
    thread.start()
    return thread
