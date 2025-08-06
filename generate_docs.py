#!/usr/bin/env python3
"""
Script para gerar documentação Swagger/OpenAPI automaticamente.
Este script importa a aplicação real e faz mock das dependências de ambiente.
"""

import json
import os
import sys
from pathlib import Path
from unittest.mock import patch, MagicMock

# Mock das variáveis de ambiente antes de importar qualquer coisa
os.environ.setdefault("LETTA_API_URL", "http://localhost:8000")
os.environ.setdefault("REDIS_URL", "redis://localhost:6379")
os.environ.setdefault("CELERY_BROKER_URL", "redis://localhost:6379")
os.environ.setdefault("CELERY_RESULT_BACKEND", "redis://localhost:6379")
os.environ.setdefault("OTEL_EXPORTER_OTLP_ENDPOINT", "http://localhost:4317")
os.environ.setdefault("GOOGLE_PROJECT_ID", "test-project")
os.environ.setdefault("GOOGLE_REGION", "us-central1")

def mock_infisical_function(env_name):
    """Mock da função getenv_or_action do infisical para não falhar."""
    return os.environ.get(env_name, f"mocked-{env_name}")

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

# Aplica os mocks antes de importar os módulos
with patch('src.utils.infisical.getenv_or_action', side_effect=mock_infisical_function), \
     patch('src.config.telemetry.setup_telemetry', side_effect=mock_setup_telemetry), \
     patch('src.services.prometheus_metrics.start_metrics_collector', side_effect=mock_start_metrics_collector), \
     patch('src.config.telemetry.get_tracer', side_effect=mock_get_tracer), \
     patch('src.config.telemetry.instrument_fastapi', side_effect=lambda x: None):
    
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
                "url": "/",
                "description": "Servidor Principal"
            },
            {
                "url": "https://api.eai.com",
                "description": "Servidor de Produção"
            },
            {
                "url": "https://staging.api.eai.com",
                "description": "Servidor de Staging"
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