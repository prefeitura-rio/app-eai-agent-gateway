from src.utils.infisical import getenv_or_action

# LETTA SERVER
LETTA_API_URL = getenv_or_action(env_name="LETTA_API_URL")
LETTA_API_TOKEN = getenv_or_action(env_name="LETTA_API_TOKEN")

# CONCURRENCY
MAX_PARALLEL = int(getenv_or_action(env_name="MAX_PARALLEL", default="8"))
LETTA_RPS = int(getenv_or_action(env_name="LETTA_RPS", default="10"))
CELERY_WORKER_POOL = getenv_or_action(env_name="CELERY_WORKER_POOL", default="prefork")
ENABLE_EVENTLET_PATCH = getenv_or_action(
    env_name="ENABLE_EVENTLET_PATCH",
    default="false",
)

# CELERY TASK LIMITS
CELERY_SOFT_TIME_LIMIT = int(
    getenv_or_action(env_name="CELERY_SOFT_TIME_LIMIT", default="90"),
)
CELERY_TIME_LIMIT = int(getenv_or_action(env_name="CELERY_TIME_LIMIT", default="120"))

# REDIS
REDIS_DSN = getenv_or_action(env_name="REDIS_DSN")
REDIS_BACKEND = getenv_or_action(env_name="REDIS_BACKEND")
# Task result TTL (for storing final responses) - 2 minutes
REDIS_TASK_RESULT_TTL = int(
    getenv_or_action(env_name="REDIS_TASK_RESULT_TTL", default="120"),
)
# Task status TTL (for storing task IDs) - 10 minutes
REDIS_TASK_STATUS_TTL = int(
    getenv_or_action(env_name="REDIS_TASK_STATUS_TTL", default="600"),
)

# AGENT CREATION
LLM_MODEL = getenv_or_action(env_name="LLM_MODEL")
EMBEDDING_MODEL = getenv_or_action(env_name="EMBEDDING_MODEL")

# EAI AGENT
EAI_AGENT_URL = getenv_or_action(env_name="EAI_AGENT_URL")
EAI_AGENT_TOKEN = getenv_or_action(env_name="EAI_AGENT_TOKEN")
EAI_AGENT_CONTEXT_WINDOW_LIMIT = int(
    getenv_or_action(env_name="EAI_AGENT_CONTEXT_WINDOW_LIMIT", default="1000000"),
)
EAI_AGENT_MAX_GOOGLE_SEARCH_PER_STEP = int(
    getenv_or_action(env_name="EAI_AGENT_MAX_GOOGLE_SEARCH_PER_STEP", default="1"),
)

# APP PREFIX
APP_PREFIX = getenv_or_action(env_name="APP_PREFIX")

# OPEN TELEMETRY
OTEL_ENABLED = (
    getenv_or_action(env_name="OTEL_ENABLED", default="false").lower() == "true"
)
OTEL_COLLECTOR_URL = getenv_or_action(
    env_name="OTEL_COLLECTOR_URL",
    default="http://localhost:4317",
)
OTEL_SERVICE_NAME = getenv_or_action(
    env_name="OTEL_SERVICE_NAME",
    default="eai-gateway",
)
OTEL_SERVICE_VERSION = getenv_or_action(
    env_name="OTEL_SERVICE_VERSION",
    default="0.1.0",
)
OTEL_ENVIRONMENT = getenv_or_action(env_name="OTEL_ENVIRONMENT", default="development")

# CACHE
CACHE_TTL_SECONDS = int(getenv_or_action(env_name="CACHE_TTL_SECONDS", default="720"))
# Agent ID cache TTL (for storing agent IDs) - 24 hours
AGENT_ID_CACHE_TTL = int(
    getenv_or_action(env_name="AGENT_ID_CACHE_TTL", default="86400")
)

# GOOGLE AGENT ENGINE CONFIG
REASONING_ENGINE_ID=getenv_or_action(env_name="REASONING_ENGINE_ID")
PROJECT_ID=getenv_or_action(env_name="PROJECT_ID")
PROJECT_NUMBER = getenv_or_action(env_name="PROJECT_NUMBER")
SERVICE_ACCOUNT = getenv_or_action(env_name="SERVICE_ACCOUNT")
LOCATION=getenv_or_action(env_name="LOCATION")
GCS_BUCKET = getenv_or_action(env_name="GCS_BUCKET")
GCS_BUCKET_STAGING = getenv_or_action(env_name="GCS_BUCKET_STAGING")