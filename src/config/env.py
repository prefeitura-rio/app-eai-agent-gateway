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
REDIS_TTL=int(getenv_or_action(env_name="REDIS_TTL", default="120"))

# AGENT CREATION
LLM_MODEL=getenv_or_action(env_name="LLM_MODEL")
EMBEDDING_MODEL=getenv_or_action(env_name="EMBEDDING_MODEL")

# EAI AGENT
EAI_AGENT_URL=getenv_or_action(env_name="EAI_AGENT_URL")
EAI_AGENT_TOKEN=getenv_or_action(env_name="EAI_AGENT_TOKEN")
EAI_AGENT_CONTEXT_WINDOW_LIMIT=int(getenv_or_action(env_name="EAI_AGENT_CONTEXT_WINDOW_LIMIT", default="1000000"))

# APP PREFIX
APP_PREFIX=getenv_or_action(env_name="APP_PREFIX")

# CACHE
CACHE_TTL_SECONDS=int(getenv_or_action(env_name="CACHE_TTL_SECONDS", default="720")) 
