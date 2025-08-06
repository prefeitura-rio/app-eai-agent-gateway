#!/usr/bin/env python3
"""
Script para gerar documentação Swagger/OpenAPI automaticamente.
Este script importa a aplicação real e faz mock das dependências de ambiente.
"""

import json
import os
import sys
import types
from pathlib import Path
from unittest.mock import patch, MagicMock

# Adiciona o diretório raiz do projeto ao PYTHONPATH
project_root = Path(__file__).parent
sys.path.insert(0, str(project_root))

# Mock das variáveis de ambiente antes de importar qualquer coisa
os.environ.setdefault("LETTA_API_URL", "http://localhost:8000")
os.environ.setdefault("LETTA_API_TOKEN", "mock-token")
os.environ.setdefault("REDIS_DSN", "redis://localhost:6379")
os.environ.setdefault("REDIS_BACKEND", "redis://localhost:6379")
os.environ.setdefault("OTEL_EXPORTER_OTLP_ENDPOINT", "http://localhost:4317")
os.environ.setdefault("GOOGLE_PROJECT_ID", "test-project")
os.environ.setdefault("GOOGLE_REGION", "us-central1")
os.environ.setdefault("LLM_MODEL", "mock-model")
os.environ.setdefault("EMBEDDING_MODEL", "mock-embedding")
os.environ.setdefault("EAI_AGENT_URL", "http://localhost:8001")
os.environ.setdefault("EAI_AGENT_TOKEN", "mock-eai-token")
os.environ.setdefault("APP_PREFIX", "mock-prefix")
os.environ.setdefault("REASONING_ENGINE_ID", "mock-engine-id")
os.environ.setdefault("PROJECT_ID", "mock-project-id")
os.environ.setdefault("PROJECT_NUMBER", "123456789")
os.environ.setdefault("SERVICE_ACCOUNT", "mock-service-account@mock.iam.gserviceaccount.com")
os.environ.setdefault("LOCATION", "us-central1")
os.environ.setdefault("GCS_BUCKET", "mock-bucket")
os.environ.setdefault("GCS_BUCKET_STAGING", "mock-bucket-staging")

def mock_infisical_function(env_name, *, action="raise", default=None):
    """Mock da função getenv_or_action do infisical para não falhar."""
    return os.environ.get(env_name, default or f"mocked-{env_name}")

def mock_setup_telemetry():
    """Mock da função setup_telemetry para não inicializar telemetria."""
    pass

def mock_start_metrics_collector():
    """Mock da função start_metrics_collector para não inicializar métricas."""
    pass

def mock_get_tracer(name):
    """Mock da função get_tracer para retornar um tracer mock."""
    tracer = MagicMock()
    tracer.start_as_current_span.return_value.__enter__ = MagicMock()
    tracer.start_as_current_span.return_value.__exit__ = MagicMock()
    return tracer

# Cria mocks dos módulos para evitar problemas de importação
infisical_module = types.ModuleType('infisical')
infisical_module.getenv_or_action = mock_infisical_function
infisical_module.getenv_list_or_action = lambda env_name, *, action="raise", default=None: []
infisical_module.mask_string = lambda string, *, mask="*": string
sys.modules['src.utils.infisical'] = infisical_module

# Mock do módulo de telemetria
telemetry_module = types.ModuleType('telemetry')
telemetry_module.setup_telemetry = mock_setup_telemetry
telemetry_module.get_tracer = mock_get_tracer
telemetry_module.instrument_fastapi = lambda x: None
telemetry_module.instrument_celery = lambda: None
sys.modules['src.config.telemetry'] = telemetry_module

# Mock do módulo de métricas
prometheus_module = types.ModuleType('prometheus_metrics')
prometheus_module.start_metrics_collector = mock_start_metrics_collector
prometheus_module.get_metrics = lambda: "# Mock metrics"
# Mock das métricas específicas
prometheus_module.celery_task_duration = MagicMock()
prometheus_module.celery_task_errors = MagicMock()
prometheus_module.celery_tasks_total = MagicMock()
sys.modules['src.services.prometheus_metrics'] = prometheus_module

# Agora pode importar os módulos sem problemas de dependências
from fastapi.openapi.utils import get_openapi

# Agora pode importar a aplicação real
try:
    from src.main import app
    print("✅ Aplicação real importada com sucesso!")
except Exception as e:
    print(f"❌ Erro ao importar aplicação: {e}")
    sys.exit(1)


def generate_openapi_spec():
    """Gera a especificação OpenAPI/Swagger da aplicação REAL."""
    openapi_schema = get_openapi(
        title=app.title,
        version=app.version,
        description=app.description,
        routes=app.routes,
    )
    
    # Adiciona informações extras ao schema
    openapi_schema["info"]["contact"] = {
        "name": "EAí Team",
        "url": "https://github.com/your-org/app-eai-agent-gateway"
    }
    
    openapi_schema["info"]["license"] = {
        "name": "MIT"
    }
    
    # Adiciona servidores
    openapi_schema["servers"] = [
        {
            "url": "https://services.staging.app.dados.rio/eai-agent-gateway/",
            "description": "Servidor de Produção"
        }
    ]
    
    return openapi_schema


def save_docs():
    """Salva a documentação em formato JSON."""
    # Cria diretório docs se não existir
    docs_dir = Path("docs")
    docs_dir.mkdir(exist_ok=True)
    
    # Gera a especificação OpenAPI
    openapi_spec = generate_openapi_spec()
    
    # Salva o JSON do Swagger
    swagger_file = docs_dir / "swagger.json"
    with open(swagger_file, "w", encoding="utf-8") as f:
        json.dump(openapi_spec, f, indent=2, ensure_ascii=False)
    
    print(f"✅ Documentação Swagger REAL gerada em: {swagger_file}")
    print(f"📊 Total de endpoints documentados: {len(openapi_spec.get('paths', {}))}")
    
    # Lista os endpoints encontrados
    paths = openapi_spec.get('paths', {})
    if paths:
        print("🔗 Endpoints documentados:")
        for path, methods in paths.items():
            for method in methods.keys():
                if method != 'parameters':  # Ignora parâmetros globais
                    print(f"   {method.upper()} {path}")
    
    # Cria um arquivo HTML simples para visualizar o Swagger
    html_content = f"""<!DOCTYPE html>
<html>
<head>
    <title>EAí Gateway - API Documentation</title>
    <link rel="stylesheet" type="text/css" href="https://unpkg.com/swagger-ui-dist@5.11.0/swagger-ui.css" />
    <style>
        html {{
            box-sizing: border-box;
            overflow: -moz-scrollbars-vertical;
            overflow-y: scroll;
        }}
        *, *:before, *:after {{
            box-sizing: inherit;
        }}
        body {{
            margin:0;
            background: #fafafa;
        }}
    </style>
</head>
<body>
    <div id="swagger-ui"></div>
    <script src="https://unpkg.com/swagger-ui-dist@5.11.0/swagger-ui-bundle.js"></script>
    <script src="https://unpkg.com/swagger-ui-dist@5.11.0/swagger-ui-standalone-preset.js"></script>
    <script>
        window.onload = function() {{
            const ui = SwaggerUIBundle({{
                url: './swagger.json',
                dom_id: '#swagger-ui',
                deepLinking: true,
                presets: [
                    SwaggerUIBundle.presets.apis,
                    SwaggerUIStandalonePreset
                ],
                plugins: [
                    SwaggerUIBundle.plugins.DownloadUrl
                ],
                layout: "StandaloneLayout"
            }});
        }};
    </script>
</body>
</html>"""
    
    html_file = docs_dir / "index.html"
    with open(html_file, "w", encoding="utf-8") as f:
        f.write(html_content)
    
    print(f"✅ Interface HTML do Swagger gerada em: {html_file}")
    
    return docs_dir


# Executa a geração se o script for executado diretamente
if __name__ == "__main__":
    save_docs()