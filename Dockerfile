
FROM python:3.12-slim

COPY --from=ghcr.io/astral-sh/uv:latest /uv /usr/local/bin/uv

RUN apt-get update -y \
 && apt-get install -y build-essential \
 && rm -rf /var/lib/apt/lists/*

WORKDIR /app

ADD . /app

RUN uv sync

RUN uv run python generate_docs.py

CMD ["uv", "run", "granian", "--host", "0.0.0.0", "--interface", "asgi", "--workers", "4", "--runtime-mode", "mt", "--task-impl", "rust", "src.main:app"]
