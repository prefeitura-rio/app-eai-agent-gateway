from loguru import logger
from opentelemetry import trace
from opentelemetry.exporter.otlp.proto.grpc.trace_exporter import OTLPSpanExporter
from opentelemetry.instrumentation.celery import CeleryInstrumentor
from opentelemetry.instrumentation.fastapi import FastAPIInstrumentor
from opentelemetry.instrumentation.httpx import HTTPXClientInstrumentor
from opentelemetry.instrumentation.redis import RedisInstrumentor
from opentelemetry.sdk.resources import Resource
from opentelemetry.sdk.trace import TracerProvider
from opentelemetry.sdk.trace.export import BatchSpanProcessor

from src.config import env


def setup_telemetry():
    """Setup OpenTelemetry tracing with gRPC exporter"""
    if not env.OTEL_ENABLED:
        logger.info("OpenTelemetry is disabled")
        return

    try:
        # Create resource with service information
        resource = Resource.create(
            {
                "service.name": env.OTEL_SERVICE_NAME,
                "service.version": env.OTEL_SERVICE_VERSION,
                "deployment.environment": env.OTEL_ENVIRONMENT,
            },
        )

        # Create tracer provider
        tracer_provider = TracerProvider(resource=resource)

        # Create OTLP gRPC exporter
        otlp_exporter = OTLPSpanExporter(
            endpoint=env.OTEL_COLLECTOR_URL,
            insecure=True,  # Set to False if using TLS
        )

        # Add batch span processor
        tracer_provider.add_span_processor(BatchSpanProcessor(otlp_exporter))

        # Set the tracer provider
        trace.set_tracer_provider(tracer_provider)

        # Auto-instrument HTTPX and Redis
        instrument_httpx()
        instrument_redis()

        logger.info(
            f"OpenTelemetry configured with collector at {env.OTEL_COLLECTOR_URL}",
        )

    except Exception as e:
        logger.error(f"Failed to setup OpenTelemetry: {e}")
        # Don't raise the exception to avoid breaking the application


def instrument_fastapi(app):
    """Instrument FastAPI application with OpenTelemetry"""
    if not env.OTEL_ENABLED:
        return

    try:
        FastAPIInstrumentor.instrument_app(app)
        logger.info("FastAPI instrumented with OpenTelemetry")
    except Exception as e:
        logger.error(f"Failed to instrument FastAPI: {e}")


def instrument_celery():
    """Instrument Celery with OpenTelemetry"""
    if not env.OTEL_ENABLED:
        return

    try:
        CeleryInstrumentor().instrument()
        logger.info("Celery instrumented with OpenTelemetry")
    except Exception as e:
        logger.error(f"Failed to instrument Celery: {e}")


def instrument_httpx():
    """Instrument HTTPX client with OpenTelemetry"""
    if not env.OTEL_ENABLED:
        return

    try:
        HTTPXClientInstrumentor().instrument()
        logger.info("HTTPX instrumented with OpenTelemetry")
    except Exception as e:
        logger.error(f"Failed to instrument HTTPX: {e}")


def instrument_redis():
    """Instrument Redis with OpenTelemetry"""
    if not env.OTEL_ENABLED:
        return

    try:
        RedisInstrumentor().instrument()
        logger.info("Redis instrumented with OpenTelemetry")
    except Exception as e:
        logger.error(f"Failed to instrument Redis: {e}")


class NoOpSpan:
    """Span no-op quando telemetria está desabilitada."""
    def set_attribute(self, key: str, value):
        pass
    
    def record_exception(self, exception):
        pass
    
    def __enter__(self):
        return self
    
    def __exit__(self, exc_type, exc_val, exc_tb):
        pass


class NoOpTracer:
    """Tracer no-op quando telemetria está desabilitada."""
    def start_as_current_span(self, name: str):
        return NoOpSpan()


def get_tracer(name: str = None):
    """Get a tracer instance"""
    if not env.OTEL_ENABLED:
        return NoOpTracer()

    if name is None:
        name = env.OTEL_SERVICE_NAME

    return trace.get_tracer(name)
