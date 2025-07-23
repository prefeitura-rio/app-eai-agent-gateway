import logging
from letta_client import AsyncLetta, MessageCreate
import httpx

from src.config import env

logger = logging.getLogger(__name__)

class LettaService:
    def __init__(self):
      
        timeout_config = httpx.Timeout(
            connect=120.0,
            read=120.0,
            write=120.0,
            pool=120.0, 
        )
        
        httpx_async_client = httpx.AsyncClient(timeout=timeout_config)
        
        self.client = AsyncLetta(base_url=env.LETTA_API_URL, token=env.LETTA_API_TOKEN, httpx_client=httpx_async_client)    
        
    async def send_message(self, agent_id: str, message: str):
      try:
        response = await self.client.agents.messages.create(
          agent_id=agent_id,
          messages=[
            MessageCreate(
              role="user",
              content=message
            )
          ],
        )
        
        return response.messages, response.usage
      
      except Exception as e:
        logger.error(f"Error sending message to agent {agent_id} on Letta: {e}")
        raise e
      
letta_service = LettaService()