
FROM python:3.12-slim

COPY --from=ghcr.io/astral-sh/uv:latest /uv /usr/local/bin/uv

RUN apt-get update -y \
 && apt-get install -y build-essential \
 && rm -rf /var/lib/apt/lists/*

WORKDIR /app

ADD . /app

RUN uv sync

CMD ["uv", "run", "granian", "--interface", "asgi", "--workers", "4", "--runtime-mode", "mt", "--task-impl", "rust", "src.main:app"]
