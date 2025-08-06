from fastapi import FastAPI, Response
from prometheus_client import CONTENT_TYPE_LATEST

from src.api import router
from src.config.telemetry import instrument_fastapi, setup_telemetry
from src.services.prometheus_metrics import get_metrics, start_metrics_collector

# Setup OpenTelemetry before creating the FastAPI app
setup_telemetry()

app = FastAPI(
    title="EAí Gateway", 
    version="0.1.0",
    description="API Gateway para agentes de IA integrados com diversos provedores",
    docs_url="/docs",
    redoc_url="/redoc",
    openapi_url="/openapi.json"
)

# Instrument FastAPI with OpenTelemetry
instrument_fastapi(app)

# Start metrics collection
start_metrics_collector()


@app.get("/metrics")
async def metrics():
    """Prometheus metrics endpoint"""
    return Response(get_metrics(), media_type=CONTENT_TYPE_LATEST)


app.include_router(router)
