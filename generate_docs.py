#!/usr/bin/env python3
"""
Script para gerar documentação Swagger/OpenAPI automaticamente.
"""

import json
import os
from pathlib import Path

from fastapi.openapi.utils import get_openapi

# Importa a aplicação FastAPI
from src.main import app


def generate_openapi_spec():
    """Gera a especificação OpenAPI/Swagger da aplicação."""
    openapi_schema = get_openapi(
        title=app.title,
        version=app.version,
        description="API Gateway para agentes de IA - EAí",
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
    
    print(f"✅ Documentação Swagger gerada em: {swagger_file}")
    
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


if __name__ == "__main__":
    save_docs()