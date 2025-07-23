from src.utils.infisical import getenv_or_action

# LETTA SERVER
LETTA_API_URL = getenv_or_action(env_name="LETTA_API_URL")
LETTA_API_TOKEN = getenv_or_action(env_name="LETTA_API_TOKEN")

# CONCURRENCY
MAX_PARALLEL=int(getenv_or_action(env_name="MAX_PARALLEL", default="8"))
LETTA_RPS=int(getenv_or_action(env_name="LETTA_RPS", default="10"))

# REDIS
REDIS_DSN=getenv_or_action(env_name="REDIS_DSN")
REDIS_BACKEND=getenv_or_action(env_name="REDIS_BACKEND")
REDIS_TTL=int(getenv_or_action(env_name="REDIS_TTL", default="60"))
