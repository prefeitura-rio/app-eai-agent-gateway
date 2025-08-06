from typing import Any, Callable, Iterator, Sequence
from langchain_core.load import dumpd
from langchain.load.dump import dumpd
from langgraph.prebuilt import create_react_agent
from langchain_google_vertexai import ChatVertexAI
from vertexai.agent_engines import (
    AsyncQueryable,
    AsyncStreamQueryable,
    Queryable,
    StreamQueryable,
)

from os import getenv
from langchain_google_cloud_sql_pg import (
    PostgresEngine,
    PostgresSaver,
)


class Agent(AsyncQueryable, AsyncStreamQueryable, Queryable, StreamQueryable):
    def __init__(
        self,
        *,
        model: str = "gemini-2.5-flash",
        system_prompt: str = None,
        tools: Sequence[Callable] = None,
        temperature: float = 0.7,
        project_id: str = getenv("PROJECT_ID"),
        region: str = getenv("LOCATION"),
        instance_name: str = getenv("INSTANCE"),
        database_name: str = getenv("DATABASE"),
        database_user: str = getenv("DATABASE_USER"),
        database_password: str = getenv("DATABASE_PASSWORD"),
    ):
        self._model = model
        self._tools = tools or []
        self._system_prompt = system_prompt
        self._temperature = temperature

        # Database configuration
        self._project_id = project_id
        self._region = region
        self._instance_name = instance_name
        self._database_name = database_name
        self._database_user = database_user
        self._database_password = database_password

        # Runtime components - initialized lazily
        self._graph = None
        self._checkpointer_async = None
        self._checkpointer_sync = None
        self._setup_complete_async = False
        self._setup_complete_sync = False

    def set_up(self):
        """Mark that setup is needed - actual setup happens lazily."""
        self._setup_complete_async = False
        self._setup_complete_sync = False

    def _create_llm_with_tools(self):
        """Create and configure the LLM with tools."""
        llm = ChatVertexAI(model_name=self._model, temperature=self._temperature)
        return llm.bind_tools(tools=self._tools)

    async def _ensure_async_setup(self):
        """Ensure async components are set up."""
        if self._setup_complete_async:
            return

        llm_with_tools = self._create_llm_with_tools()

        engine = await PostgresEngine.afrom_instance(
            project_id=self._project_id,
            region=self._region,
            instance=self._instance_name,
            database=self._database_name,
            user=self._database_user,
            password=self._database_password,
        )

        self._checkpointer_async = PostgresSaver.create_sync(engine=engine)
        self._graph = create_react_agent(
            llm_with_tools,
            self._tools,
            prompt=self._system_prompt,
            checkpointer=self._checkpointer_async,
        )
        self._setup_complete_async = True

    def _ensure_sync_setup(self):
        """Ensure sync components are set up."""
        if self._setup_complete_sync:
            return

        llm_with_tools = self._create_llm_with_tools()

        engine = PostgresEngine.from_instance(
            project_id=self._project_id,
            region=self._region,
            instance=self._instance_name,
            database=self._database_name,
            user=self._database_user,
            password=self._database_password,
        )

        self._checkpointer_sync = PostgresSaver.create_sync(engine=engine)
        self._graph = create_react_agent(
            llm_with_tools,
            self._tools,
            prompt=self._system_prompt,
            checkpointer=self._checkpointer_sync,
        )
        self._setup_complete_sync = True

    async def async_query(self, **kwargs) -> dict[str, Any] | Any:
        """Asynchronous query execution."""
        await self._ensure_async_setup()
        return await self._graph.ainvoke(**kwargs)

    async def async_stream_query(self, **kwargs) -> Iterator[dict[str, Any] | Any]:
        """Asynchronous streaming query execution."""
        await self._ensure_async_setup()
        async for chunk in self._graph.astream(**kwargs):
            yield dumpd(chunk)

    def query(self, **kwargs) -> dict[str, Any] | Any:
        """Synchronous query execution."""
        self._ensure_sync_setup()
        return self._graph.invoke(**kwargs)

    def stream_query(self, **kwargs) -> Iterator[dict[str, Any] | Any]:
        """Synchronous streaming query execution."""
        self._ensure_sync_setup()
        for chunk in self._graph.stream(**kwargs):
            yield dumpd(chunk)
