import logging
from letta_client import Letta, AsyncLetta, MessageCreate
import httpx
from fastapi import HTTPException

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
        httpx_client = httpx.Client(timeout=timeout_config)
        
        self.client = AsyncLetta(base_url=env.LETTA_API_URL, token=env.LETTA_API_TOKEN, httpx_client=httpx_async_client)
        self.client_sync = Letta(base_url=env.LETTA_API_URL, token=env.LETTA_API_TOKEN, httpx_client=httpx_client)
        
## SYNC METHODS
        
    def send_message_sync(self, agent_id: str, message: str, previous_message: str | None = None):
      try:
        
        if previous_message is not None:
          messages = [
            MessageCreate(
              role="system",
              content=f"Previous message sent by system: {previous_message}"
            ),
            MessageCreate(
              role="user",
              content=message
            )
          ]
        else:
          messages = [
            MessageCreate(
              role="user",
              content=message
            )
          ]
                  
        response = self.client_sync.agents.messages.create(
          agent_id=agent_id,
          messages=messages,
        )
        
        return response.messages, response.usage
      
      except httpx.TimeoutException as e:
        logger.error(f"Timeout sending message to agent {agent_id}: {e}")
        raise HTTPException(status_code=408, detail=f"Timeout communicating with Letta API: {str(e)}")
      except httpx.HTTPStatusError as e:
        logger.error(f"HTTP error sending message to agent {agent_id}: {e.response.status_code} - {e.response.text}")
        raise HTTPException(status_code=e.response.status_code, detail=f"Letta API error: {e.response.text}")
      except Exception as e:
        logger.error(f"Unexpected error sending message to agent {agent_id}: {e}")
        raise HTTPException(status_code=500, detail=f"Unexpected error: {str(e)}")
    
## ASYNC METHODS
        
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
      
      except httpx.TimeoutException as e:
        logger.error(f"Timeout sending async message to agent {agent_id}: {e}")
        raise HTTPException(status_code=408, detail=f"Timeout communicating with Letta API: {str(e)}")
      except httpx.HTTPStatusError as e:
        logger.error(f"HTTP error sending async message to agent {agent_id}: {e.response.status_code} - {e.response.text}")
        raise HTTPException(status_code=e.response.status_code, detail=f"Letta API error: {e.response.text}")
      except Exception as e:
        logger.error(f"Unexpected error sending async message to agent {agent_id}: {e}")
        raise HTTPException(status_code=500, detail=f"Unexpected error: {str(e)}")
      
    async def get_agent_id(self, user_number: str) -> str | None:
      try:
        response = await self.client.agents.list(
          tags=[user_number],
          limit=1, 
          match_all_tags=False,
        )
        
        if response:
          return response[0].id
        return None
      
      except Exception as e:
        logger.error(f"Error getting agent ID for user {user_number} on Letta: {e}")
        raise e
      
    async def create_agent(self, user_number: str) -> str | None:
      try:
        response = await self.client.agents.create(
          name=f"{user_number}",
          tags=[user_number],
          agent_type="memgpt_v2_agent",
          
        )
        return response.id
      except Exception as e:
        logger.error(f"Error creating agent for user {user_number} on Letta: {e}")
        raise e
      
letta_service = LettaService()