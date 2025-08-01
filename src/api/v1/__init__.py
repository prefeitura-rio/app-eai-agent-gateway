from fastapi import APIRouter

from src.api.v1 import agent, message

router = APIRouter(prefix="/v1")

router.include_router(message.router)
router.include_router(agent.router)
