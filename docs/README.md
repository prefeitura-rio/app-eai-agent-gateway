# Documentação da API

Esta pasta contém a documentação automaticamente gerada da API do EAí Gateway.

## Arquivos Gerados

- `swagger.json` - Especificação OpenAPI/Swagger em formato JSON
- `index.html` - Interface web do Swagger UI para visualizar a documentação

## Como Gerar Localmente

Para gerar a documentação localmente, execute:

```bash
uv run python generate_docs.py
```

## Como Visualizar

1. **Via API**: Acesse `/docs` ou `/redoc` quando a aplicação estiver rodando
2. **Via arquivo**: Abra o arquivo `docs/index.html` em um navegador
3. **Via artifacts**: Baixe os artifacts do GitHub Actions após cada build

## Automação

A documentação é gerada automaticamente:

- **Durante o build Docker**: O comando é executado no Dockerfile
- **No CI/CD**: O GitHub Actions gera e faz commit no repositório
- **A cada push**: Para branches `main` e `staging`
- **Disponível no GitHub**: Os arquivos ficam disponíveis diretamente no repositório

## Estrutura da API

A API segue o padrão de versionamento com prefixos `/api/v1/`, conforme as boas práticas de design de APIs REST.