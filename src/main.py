from fastapi import FastAPI

from src.api import router
from src.config.telemetry import setup_telemetry, instrument_fastapi

# Setup OpenTelemetry before creating the FastAPI app
setup_telemetry()

app = FastAPI(title="EAí Gateway", version="0.1.0")

# Instrument FastAPI with OpenTelemetry
instrument_fastapi(app)

app.include_router(router)