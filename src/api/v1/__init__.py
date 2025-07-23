from fastapi import APIRouter

from src.api.v1 import message

router = APIRouter(prefix="/v1")

router.include_router(message.router)