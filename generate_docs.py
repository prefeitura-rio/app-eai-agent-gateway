#!/usr/bin/env python3
"""
Script para gerar documentação Swagger/OpenAPI automaticamente.
Este script cria uma aplicação FastAPI standalone para evitar dependências de ambiente.
"""

import json
import os
from pathlib import Path

from fastapi import FastAPI
from fastapi.openapi.utils import get_openapi

# Importa apenas os routers sem dependências de ambiente
def create_standalone_app():
    """Cria uma aplicação FastAPI standalone para gerar documentação."""
    app = FastAPI(
        title="EAí Gateway", 
        version="0.1.0",
        description="API Gateway para agentes de IA integrados com diversos provedores",
        docs_url="/docs",
        redoc_url="/redoc",
        openapi_url="/openapi.json"
    )
    
    # Simula as rotas principais sem importar os módulos que dependem de env
    from fastapi import APIRouter
    
    # Router principal da API
    api_router = APIRouter(prefix="/api")
    v1_router = APIRouter(prefix="/v1")
    
    # Routers de mensagens
    message_router = APIRouter(prefix="/message", tags=["Messages"])
    
    @message_router.post("/webhook/agent")
    async def agent_webhook():
        """Webhook para receber mensagens de agentes de IA."""
        pass
    
    @message_router.post("/webhook/user")
    async def user_webhook():
        """Webhook para receber mensagens de usuários."""
        pass
    
    @message_router.get("/status/{task_id}")
    async def get_task_status():
        """Consultar status de uma tarefa de processamento."""
        pass
    
    @message_router.get("/response/{task_id}")
    async def get_task_response():
        """Obter resposta de uma tarefa processada."""
        pass
    
    # Router de agentes
    agent_router = APIRouter(prefix="/agent", tags=["Agents"])
    
    @agent_router.post("/create")
    async def create_agent():
        """Criar um novo agente de IA."""
        pass
    
    @agent_router.delete("/delete")
    async def delete_agent():
        """Remover um agente existente."""
        pass
    
    # Endpoint de métricas
    @app.get("/metrics", tags=["Monitoring"])
    async def metrics():
        """Endpoint de métricas do Prometheus."""
        pass
    
    # Monta os routers
    v1_router.include_router(message_router)
    v1_router.include_router(agent_router)
    api_router.include_router(v1_router)
    app.include_router(api_router)
    
    return app

# Cria app standalone
app = create_standalone_app()


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