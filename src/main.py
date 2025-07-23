from fastapi import FastAPI

from src.api import router

app = FastAPI(title="EAí Gateway", version="0.1.0")

app.include_router(router)