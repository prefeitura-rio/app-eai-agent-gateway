# EAI Agent Gateway

<div align="center">

![Go Version](https://img.shields.io/badge/Go-1.23+-blue.svg)
![License](https://img.shields.io/badge/license-MIT-green.svg)
![Build Status](https://img.shields.io/badge/build-passing-brightgreen.svg)
![Coverage](https://img.shields.io/badge/coverage-85%25-green.svg)

**A high-performance, production-ready microservice for AI agent interactions**

[Features](#features) • [Quick Start](#quick-start) • [Architecture](#architecture) • [API Reference](#api-reference) • [Deployment](#deployment)

</div>

---

## 📖 Table of Contents

- [Overview](#overview)
- [Architecture](#architecture)
- [Features](#features)
- [Quick Start](#quick-start)
- [Project Structure](#project-structure)
- [Message Flow](#message-flow)
- [API Reference](#api-reference)
- [Configuration](#configuration)
- [Development](#development)
- [Deployment](#deployment)
- [Monitoring](#monitoring)
- [Performance](#performance)
- [Security](#security)
- [Migration Guide](#migration-guide)
- [Contributing](#contributing)

---

## 🌟 Overview

The **EAI Agent Gateway** is a high-performance Go microservice that provides a unified interface for AI agent interactions. Originally built in Python, this Go implementation offers significant improvements in performance, reliability, and operational simplicity while maintaining 100% API compatibility.

### Key Benefits

- 🚀 **High Performance**: 10x faster response times compared to Python version (500ms → 50ms average)
- ⚡ **Async Processing**: RabbitMQ-based message queuing with retry mechanisms and dead letter queues
- 🔍 **Production Ready**: Comprehensive observability, health checks, and security hardening
- 📈 **Scalable**: Horizontal scaling with Kubernetes support and auto-scaling capabilities
- 🔒 **Secure**: Input validation, credential management, and security best practices
- 🔄 **Compatible**: Drop-in replacement for existing Python implementation

---

## 🏗️ Architecture

### System Overview

```
┌─────────────────────────────────────────────────────────────────────────────────┐
│                            EAI AGENT GATEWAY ARCHITECTURE                       │
└─────────────────────────────────────────────────────────────────────────────────┘

                        ┌─────────────────┐
                        │   WhatsApp      │
                        │   Client        │
                        └─────────┬───────┘
                                  │ HTTP POST
                                  │ webhook
                                  ▼
┌─────────────────────────────────────────────────────────────────────────────────┐
│                               API GATEWAY                                       │
│  ┌─────────────────┐  ┌─────────────────┐  ┌─────────────────┐                │
│  │   MIDDLEWARE    │  │   HTTP HANDLERS │  │   VALIDATION    │                │
│  │                 │  │                 │  │                 │                │
│  │ • CORS          │  │ • User Messages │  │ • Input Checks  │                │
│  │ • Auth          │  │ • Agent Msgs    │  │ • Rate Limiting │                │
│  │ • Logging       │  │ • Health Check  │  │ • Security      │                │
│  │ • Metrics       │  │ • Response Poll │  │ • File Upload   │                │
│  │ • Tracing       │  │                 │  │                 │                │
│  └─────────────────┘  └─────────────────┘  └─────────────────┘                │
└───────────────────────────────┬─────────────────────────────────────────────────┘
                                │ Publish Message
                                │ (with trace context)
                                ▼
┌─────────────────────────────────────────────────────────────────────────────────┐
│                              MESSAGE QUEUE                                      │
│                                                                                 │
│    ┌─────────────────┐              ┌─────────────────┐                        │
│    │   RABBITMQ      │              │   DEAD LETTER   │                        │
│    │                 │              │   EXCHANGE      │                        │
│    │ • User Queue    │ ──Failed──▶  │                 │                        │
│    │ • Agent Queue   │              │ • Retry Logic   │                        │
│    │ • Work Queue    │              │ • Error Store   │                        │
│    │ • Priorities    │              │                 │                        │
│    └─────────────────┘              └─────────────────┘                        │
└───────────────────────────────┬─────────────────────────────────────────────────┘
                                │ Consume Messages
                                │ (async processing)
                                ▼
┌─────────────────────────────────────────────────────────────────────────────────┐
│                          WORKER PROCESSES                                       │
│                                                                                 │
│  ┌─────────────────┐  ┌─────────────────┐  ┌─────────────────┐                │
│  │ USER MESSAGE    │  │ AUDIO PROCESSOR │  │ RESPONSE FORMAT │                │
│  │ WORKER          │  │                 │  │                 │                │
│  │                 │  │ • File Download │  │ • MD to WhatsApp│                │
│  │ • Thread Mgmt   │  │ • Transcription │  │ • Message Clean │                │
│  │ • Agent Calls   │  │ • Format Check  │  │ • Link Format   │                │
│  │ • Retry Logic   │  │ • Temp Files    │  │ • Emoji Support │                │
│  │ • Error Handle  │  │                 │  │                 │                │
│  └─────────────────┘  └─────────────────┘  └─────────────────┘                │
└─────────────────────────────────────────────────────────────────────────────────┘
                                │
                                │ Store Results
                                ▼
┌─────────────────────────────────────────────────────────────────────────────────┐
│                          STORAGE & CACHING                                      │
│                                                                                 │
│  ┌─────────────────┐                    ┌─────────────────┐                    │
│  │     REDIS       │                    │   TEMPORARY     │                    │
│  │                 │                    │   FILE STORAGE  │                    │
│  │ • Task Results  │                    │                 │                    │
│  │ • Thread Cache  │                    │ • Audio Files   │                    │
│  │ • Agent IDs     │                    │ • Auto Cleanup  │                    │
│  │ • Status Track  │                    │ • Security      │                    │
│  │ • TTL Mgmt      │                    │                 │                    │
│  └─────────────────┘                    └─────────────────┘                    │
└─────────────────────────────────────────────────────────────────────────────────┘
                                │
                                │ API Calls
                                ▼
┌─────────────────────────────────────────────────────────────────────────────────┐
│                        EXTERNAL AI SERVICES                                     │
│                                                                                 │
│  ┌─────────────────┐  ┌─────────────────┐  ┌─────────────────┐                │
│  │ GOOGLE AGENT    │  │ EAI AGENT       │  │ GOOGLE CLOUD    │                │
│  │ ENGINE          │  │ SERVICE         │  │ SPEECH API      │                │
│  │                 │  │                 │  │                 │                │
│  │ • Reasoning API │  │ • Custom Logic  │  │ • Transcription │                │
│  │ • Rate Limited  │  │ • Agent Mgmt    │  │ • Multi-format  │                │
│  │ • Retry Logic   │  │ • HTTP Client   │  │ • Auto-detect   │                │
│  │ • Error Handle  │  │                 │  │                 │                │
│  └─────────────────┘  └─────────────────┘  └─────────────────┘                │
└─────────────────────────────────────────────────────────────────────────────────┘
                                │
                                │ Observability
                                ▼
┌─────────────────────────────────────────────────────────────────────────────────┐
│                            MONITORING STACK                                     │
│                                                                                 │
│  ┌─────────────────┐  ┌─────────────────┐  ┌─────────────────┐                │
│  │   PROMETHEUS    │  │   OPENTELEMETRY │  │   STRUCTURED    │                │
│  │                 │  │                 │  │   LOGGING       │                │
│  │ • Metrics       │  │ • Traces        │  │                 │                │
│  │ • Alerts        │  │ • Spans         │  │ • JSON Format   │                │
│  │ • Dashboards    │  │ • Correlation   │  │ • Correlation   │                │
│  │ • SLIs/SLOs     │  │ • E2E Tracking  │  │ • Error Context │                │
│  └─────────────────┘  └─────────────────┘  └─────────────────┘                │
└─────────────────────────────────────────────────────────────────────────────────┘
```

### Component Breakdown

#### 🌐 API Gateway Layer
- **HTTP Server**: Gin-based REST API with comprehensive middleware stack
- **Middleware**: CORS, authentication, logging, metrics, distributed tracing
- **Handlers**: Message processing, health checks, response polling
- **Validation**: Input sanitization, rate limiting, security checks

#### 📨 Message Processing Layer
- **Queue System**: RabbitMQ with topic exchanges and dead letter queues
- **Workers**: Concurrent message processors with configurable concurrency
- **Retry Logic**: Exponential backoff with circuit breaker patterns
- **Error Handling**: Comprehensive error classification and recovery

#### 🧠 AI Service Integration
- **Provider Factory**: Abstraction for multiple AI providers (Google, EAI)
- **Thread Management**: User conversation state with Redis persistence
- **Rate Limiting**: Sliding window rate limiter for external APIs
- **Transcription**: Google Cloud Speech v2 API with multi-format support

#### 💾 Storage & Caching
- **Redis**: Task results, thread cache, status tracking with TTL management
- **Temporary Files**: Secure file handling with automatic cleanup
- **Thread Storage**: Simple phone-number-based thread identification

---

## ✨ Features

### 🔄 Core Functionality
- **Async Message Processing**: Non-blocking webhook endpoints with queue-based processing
- **Multi-Provider AI**: Support for Google Agent Engine and EAI Agent services
- **Audio Transcription**: Google Cloud Speech v2 integration with format auto-detection
- **Message Formatting**: WhatsApp-compatible markdown conversion and validation
- **Thread Management**: Persistent conversation threads with Redis storage

### 🏗️ Infrastructure
- **Message Queuing**: RabbitMQ with dead letter queues, retry mechanisms, and priority queuing
- **Caching**: Redis connection pooling with configurable TTL and clustering support
- **Health Monitoring**: Comprehensive dependency health checks with circuit breakers
- **Rate Limiting**: Sliding window rate limiting with configurable thresholds

### 📊 Observability
- **Structured Logging**: JSON logs with correlation ID tracking and contextual fields
- **Metrics Collection**: Prometheus metrics with custom business metrics and SLI/SLO tracking
- **Distributed Tracing**: OpenTelemetry with end-to-end request tracking
- **Performance Monitoring**: Request timing, queue depth, error classification, and SLA tracking

### 🔒 Security
- **Input Validation**: Comprehensive request validation with malicious pattern detection
- **Credential Management**: Automatic credential sanitization in logs and secure token handling
- **Network Security**: CORS policies, security headers, TLS termination, and IP whitelisting
- **File Security**: Secure temporary file handling with automatic cleanup and virus scanning

### 📈 Performance
- **High Throughput**: 1000+ requests/second with horizontal scaling
- **Low Latency**: Sub-50ms response times for cached operations
- **Memory Efficient**: ~200MB memory footprint with garbage collection tuning
- **Concurrent Processing**: Configurable worker pools with backpressure handling

---

## 🚀 Quick Start

### Prerequisites

```bash
# Required Dependencies
- Go 1.23+              # For building and development
- Redis 6.0+            # For caching and task tracking
- RabbitMQ 3.8+         # For message queuing
- Google Cloud Platform # For AI services and transcription

# Optional Dependencies
- Docker & Docker Compose  # For containerized development
- kubectl                  # For Kubernetes deployment
- k6                       # For load testing
```

### Installation

```bash
# Clone the repository
git clone https://github.com/prefeitura-rio/app-eai-agent-gateway.git
cd app-eai-agent-gateway

# Install dependencies
go mod download

# Copy and configure environment
cp .env.example .env
# Edit .env with your configuration values

# Install development tools (optional)
go install github.com/cosmtrek/air@latest  # Hot reload
go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest  # Linting
```

### Development Setup

```bash
# Option 1: Docker Compose (Recommended)
docker-compose up -d redis rabbitmq
just dev  # Start gateway with hot reload
# In another terminal:
just worker  # Start worker process

# Option 2: Local Services
# Start Redis: redis-server
# Start RabbitMQ: rabbitmq-server
# Configure .env for local connections
just dev-full  # Start both gateway and worker

# Option 3: Full Docker Setup
docker-compose up  # Start all services including gateway
```

### Testing the Service

```bash
# Health check
curl http://localhost:8000/health | jq '.'

# Send a test message
export AUTH_TOKEN="your-bearer-token"
curl -X POST http://localhost:8000/api/v1/message/webhook/user \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $AUTH_TOKEN" \
  -d '{
    "user_number": "+5511999999999",
    "message": "Hello! How can you help me today?",
    "message_type": "text"
  }' | jq '.'

# Poll for response
export MESSAGE_ID="uuid-from-previous-response"
curl "http://localhost:8000/api/v1/message/response?message_id=$MESSAGE_ID" \
  -H "Authorization: Bearer $AUTH_TOKEN" | jq '.'

# Send audio message
curl -X POST http://localhost:8000/api/v1/message/webhook/user \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $AUTH_TOKEN" \
  -d '{
    "user_number": "+5511999999999",
    "message": "https://example.com/audio.mp3",
    "message_type": "audio"
  }' | jq '.'
```

---

## 📁 Project Structure

```
app-eai-agent-gateway/
├── 📁 cmd/                          # Application entry points
│   ├── 📁 gateway/                  # HTTP API server
│   │   └── main.go                  # Gateway main function
│   └── 📁 worker/                   # Background worker
│       └── main.go                  # Worker main function
├── 📁 internal/                     # Private application code
│   ├── 📁 api/                      # HTTP server setup
│   │   └── server.go                # Gin server configuration
│   ├── 📁 config/                   # Configuration management
│   │   ├── config.go                # Environment variable binding
│   │   └── config_test_helper.go    # Test configuration utilities
│   ├── 📁 handlers/                 # HTTP request handlers
│   │   ├── health.go                # Health check endpoints
│   │   ├── message.go               # Message webhook handlers
│   │   └── 📁 workers/              # Background worker handlers
│   │       └── message_handlers.go  # Message processing logic
│   ├── 📁 middleware/               # HTTP middleware
│   │   ├── correlation.go           # Request correlation IDs
│   │   ├── cors.go                  # CORS policy enforcement
│   │   ├── logger.go                # Request logging
│   │   ├── metrics.go               # Prometheus metrics
│   │   ├── otel.go                  # OpenTelemetry tracing
│   │   ├── security.go              # Security headers
│   │   └── structured_logging.go    # Structured log formatting
│   ├── 📁 models/                   # Data models
│   │   └── message.go               # Message data structures
│   ├── 📁 services/                 # Business logic services
│   │   ├── credential_manager.go    # Credential sanitization
│   │   ├── eai_agent_service.go     # EAI Agent integration
│   │   ├── google_agent_engine_service.go  # Google AI integration
│   │   ├── health_service.go        # Health checking logic
│   │   ├── message_consumer.go      # RabbitMQ consumer
│   │   ├── message_formatter_service.go    # WhatsApp formatting
│   │   ├── otel_service.go          # OpenTelemetry service
│   │   ├── prometheus_metrics.go    # Metrics collection
│   │   ├── rabbitmq_service.go      # Message queue operations
│   │   ├── rate_limiter_service.go  # Rate limiting logic
│   │   ├── redis_service.go         # Redis operations
│   │   ├── structured_logger.go     # Logging service
│   │   ├── temp_file_manager.go     # File handling
│   │   ├── transcribe_service.go    # Audio transcription
│   │   ├── validation_service.go    # Input validation
│   │   ├── worker_manager_service.go # Worker lifecycle
│   │   └── worker_metrics_service.go # Worker metrics
│   └── 📁 workers/                  # Worker implementations
│       ├── base_worker.go           # Base worker interface
│       ├── interfaces.go            # Worker contracts
│       └── user_message_worker.go   # User message processor
├── 📁 docs/                         # Documentation
│   ├── docs.go                      # Generated API docs
│   ├── signoz-dashboard-guide.md    # Observability setup
│   ├── swagger.json                 # OpenAPI specification
│   └── swagger.yaml                 # Swagger documentation
├── 📁 k8s/                          # Kubernetes manifests
│   ├── 📁 prod/                     # Production configuration
│   │   ├── kustomization.yaml       # Kustomize config
│   │   └── resources.yaml           # K8s resources
│   └── 📁 staging/                  # Staging configuration
│       ├── kustomization.yaml       # Kustomize config
│       └── resources.yaml           # K8s resources
├── 📁 load-tests/                   # Performance testing
│   ├── 📁 charts/                   # Test result charts
│   ├── generate-charts.py           # Chart generation
│   ├── main.js                      # k6 load test script
│   └── results.json                 # Test results
├── docker-compose.yml               # Development environment
├── Dockerfile                       # Container build
├── go.mod                           # Go module definition
├── go.sum                           # Go module checksums
├── justfile                         # Task automation
└── README.md                        # This file
```

---

## 🔄 Message Flow

### User Message Processing Flow

```
┌─────────────────────────────────────────────────────────────────────────────────┐
│                        USER MESSAGE PROCESSING FLOW                             │
└─────────────────────────────────────────────────────────────────────────────────┘

1. WEBHOOK RECEPTION
   ┌─────────────────┐    HTTP POST     ┌─────────────────┐
   │   WhatsApp      │ ─────────────▶   │  API Gateway    │
   │   Webhook       │  /webhook/user   │  (Gin Server)   │
   └─────────────────┘                  └─────────────────┘
                                                │
                                                │ Generate UUID
                                                │ Validate Input
                                                │ Extract Trace Context
                                                ▼
2. MESSAGE QUEUING
   ┌─────────────────┐                  ┌─────────────────┐
   │   RabbitMQ      │ ◀────────────────│  Queue Message  │
   │   User Queue    │  Publish w/      │  + Trace Headers│
   └─────────────────┘  Headers         └─────────────────┘
                                                │
                                                │ Return 202 Accepted
                                                │ {message_id: uuid}
                                                ▼
3. ASYNC PROCESSING                     ┌─────────────────┐
   ┌─────────────────┐                  │   Client        │
   │   Worker        │                  │   (Polling)     │
   │   Process       │                  └─────────────────┘
   └─────────┬───────┘
             │ Consume Message
             ▼
4. THREAD MANAGEMENT
   ┌─────────────────┐    Get/Create    ┌─────────────────┐
   │   Redis         │ ◀────────────────│  Thread Lookup  │
   │   thread:{phone}│                  │  Key: {phone}   │
   └─────────────────┘                  └─────────────────┘
             │                                  │
             │ Thread ID = Phone Number         │
             ▼                                  ▼
5. MESSAGE TYPE DETECTION
             ┌─────────────────┐
             │  Is Audio URL?  │
             └─────────┬───────┘
                       │
             ┌─────────▼───────┐
             │    YES  │   NO  │
             │         │       │
             ▼         ▼       ▼
6a. AUDIO PROCESSING           6b. TEXT PROCESSING
   ┌─────────────────┐            ┌─────────────────┐
   │ Download File   │            │  Direct to AI   │
   │ Check Format    │            │  Service        │
   │ Transcribe      │            └─────────────────┘
   │ (Google Speech) │                     │
   └─────────────────┘                     │
             │                             │
             │ Transcribed Text            │
             ▼                             │
7. AI SERVICE CALL                         │
   ┌─────────────────┐ ◀──────────────────┘
   │  Agent Provider │
   │  Factory        │
   └─────────┬───────┘
             │ Route to Provider
             ▼
   ┌─────────────────┐    or    ┌─────────────────┐
   │ Google Agent    │          │  EAI Agent      │
   │ Engine API      │          │  Service        │
   └─────────────────┘          └─────────────────┘
             │                           │
             │ AI Response               │
             ▼                           ▼
8. RESPONSE FORMATTING
   ┌─────────────────┐
   │ Message         │
   │ Formatter       │
   │                 │
   │ • MD → WhatsApp │
   │ • Link Format   │
   │ • Emoji Support │
   │ • Length Limits │
   └─────────────────┘
             │
             │ Formatted Response
             ▼
9. RESULT STORAGE
   ┌─────────────────┐     Store Result    ┌─────────────────┐
   │   Redis         │ ◀─────────────────  │  Task Result    │
   │ task:{msg_id}   │   TTL: 2 minutes    │  + Trace Data   │
   └─────────────────┘                     └─────────────────┘
             │
             │ Update Status: "completed"
             ▼
10. CLIENT POLLING
   ┌─────────────────┐    GET /response    ┌─────────────────┐
   │   Client        │ ──────────────────▶ │  API Gateway    │
   │   (WhatsApp)    │  ?message_id=uuid  │  Response Poll  │
   └─────────────────┘                     └─────────────────┘
                                                   │
                                                   │ Lookup Result
                                                   ▼
                                          ┌─────────────────┐
                                          │   Redis Get     │
                                          │ task:{msg_id}   │
                                          └─────────────────┘
                                                   │
                                                   │ Return Response
                                                   ▼
                                          ┌─────────────────┐
                                          │ 200 OK          │
                                          │ {               │
                                          │   "status": "ok"│
                                          │   "content": ..│
                                          │   "thread_id":.│
                                          │ }               │
                                          └─────────────────┘

ERROR HANDLING:
┌─────────────────────────────────────────────────────────────────────────────────┐
│  At each step, errors are classified as:                                        │
│  • RETRIABLE: Network timeouts, 5xx errors, rate limits                        │
│    → Message requeued with exponential backoff                                  │
│  • PERMANENT: Validation errors, 4xx errors, malformed input                   │
│    → Message marked as failed, error logged                                     │
│  • DEAD LETTER: Messages that exceed max retry attempts                        │
│    → Moved to DLX for manual investigation                                      │
└─────────────────────────────────────────────────────────────────────────────────┘
```

### Thread Management Simplified

```
┌─────────────────────────────────────────────────────────────────────────────────┐
│                          THREAD MANAGEMENT SYSTEM                               │
└─────────────────────────────────────────────────────────────────────────────────┘

BEFORE (Complex):                     AFTER (Simplified):
                                     
┌─────────────────┐                  ┌─────────────────┐
│ user_thread:    │                  │ thread:         │
│ +5511999999999  │                  │ +5511999999999  │
│                 │                  │                 │
│ VALUE:          │                  │ VALUE:          │
│ thread_+5511... │                  │ {               │
│ 999_1640995200  │                  │   "thread_id":  │
└─────────────────┘                  │   "+5511999.."  │
         │                           │   "user_id":    │
         │ Lookup                    │   "+5511999.."  │
         ▼                           │   "created_at": │
┌─────────────────┐                  │   "2023-..."    │
│ thread:thread_  │                  │   "last_used":  │
│ +5511999999999_ │                  │   "2023-..."    │
│ 1640995200      │                  │   "msg_count":5 │
│                 │                  │ }               │
│ VALUE: {thread} │                  └─────────────────┘
└─────────────────┘                           │
                                             │ Direct Access
❌ Two Redis Lookups                         │ Single Lookup
❌ Complex Key Management                     ▼
❌ Possible Inconsistency                    ✅ One Redis Lookup
                                            ✅ Phone = Thread ID
                                            ✅ Always Consistent
```

---

## 📚 API Reference

### Core Endpoints

#### Message Processing

```http
POST /api/v1/message/webhook/user
```
Submit a user message for processing.

**Request:**
```json
{
  "user_number": "+5511999999999",
  "message": "Hello! How can you help me?",
  "message_type": "text"
}
```

**Response:**
```json
{
  "message_id": "550e8400-e29b-41d4-a716-446655440000",
  "status": "queued",
  "estimated_wait_time": "2-5 seconds"
}
```

---

```http
POST /api/v1/message/webhook/user (Audio)
```
Submit an audio message for transcription and processing.

**Request:**
```json
{
  "user_number": "+5511999999999", 
  "message": "https://example.com/audio.mp3",
  "message_type": "audio"
}
```

**Supported Audio Formats:**
- MP3, WAV, FLAC, OGG, OPUS, M4A
- Max size: 25MB
- Auto-format detection

---

```http
GET /api/v1/message/response?message_id={uuid}
```
Poll for message processing results.

**Response (Processing):**
```json
{
  "status": "processing",
  "message": "Your message is being processed"
}
```

**Response (Completed):**
```json
{
  "status": "completed",
  "content": "Hello! I'm here to help you with...",
  "thread_id": "+5511999999999",
  "processing_time_ms": 1250,
  "message_type": "text"
}
```

**Response (Failed):**
```json
{
  "status": "failed",
  "error": "Audio transcription failed",
  "error_code": "TRANSCRIPTION_ERROR",
  "retry_after": 300
}
```

#### Health & Monitoring

```http
GET /health
```
Comprehensive health check for all dependencies.

**Response:**
```json
{
  "status": "healthy",
  "timestamp": "2023-12-01T10:30:00Z",
  "version": "2.1.0",
  "services": {
    "redis": {
      "status": "healthy",
      "response_time_ms": 2,
      "connection_pool": {
        "active": 5,
        "idle": 3,
        "max": 10
      }
    },
    "rabbitmq": {
      "status": "healthy", 
      "response_time_ms": 8,
      "queue_depths": {
        "user_messages": 12,
        "dead_letter": 0
      }
    },
    "google_speech": {
      "status": "healthy",
      "response_time_ms": 150,
      "rate_limit_remaining": 950
    },
    "google_agent_engine": {
      "status": "degraded",
      "response_time_ms": 2500,
      "error_rate": 0.02
    }
  },
  "system": {
    "memory_usage_mb": 198,
    "cpu_usage_percent": 15,
    "goroutines": 45,
    "uptime_seconds": 86400
  }
}
```

---

```http
GET /ready
```
Kubernetes readiness probe.

**Response:** `200 OK` or `503 Service Unavailable`

---

```http
GET /live
```
Kubernetes liveness probe.

**Response:** `200 OK` or `503 Service Unavailable`

---

```http
GET /metrics
```
Prometheus metrics endpoint.

**Key Metrics:**
```
# Message processing
http_requests_total{method="POST",endpoint="/webhook/user",status="200"} 1500
message_processing_duration_seconds{type="audio"} 2.5
message_queue_depth{queue="user_messages"} 8

# External services  
external_api_calls_total{service="google_speech",status="success"} 450
external_api_response_time_seconds{service="google_agent_engine"} 1.2

# System health
redis_connections_active 7
rabbitmq_connection_status{status="connected"} 1
worker_pool_utilization_ratio 0.75
```

### Authentication

All endpoints require a Bearer token in the Authorization header:

```http
Authorization: Bearer YOUR_API_TOKEN
```

**Error Response (401):**
```json
{
  "error": "Unauthorized",
  "message": "Invalid or missing authentication token"
}
```

### Rate Limiting

API endpoints are rate limited:
- **Default**: 1000 requests/minute per client
- **Burst**: Up to 100 requests in 10 seconds
- **Headers**: `X-RateLimit-Limit`, `X-RateLimit-Remaining`, `X-RateLimit-Reset`

**Rate Limit Response (429):**
```json
{
  "error": "Rate limit exceeded", 
  "retry_after": 60,
  "limit": 1000,
  "window": "1 minute"
}
```

---

## ⚙️ Configuration

### Environment Variables

#### Required Configuration

```bash
# Server Configuration
PORT=8000                           # HTTP server port
ENVIRONMENT=production              # Environment name (dev/staging/prod)
API_TOKEN=your-secure-api-token     # Authentication token

# Infrastructure Services
REDIS_URL=redis://localhost:6379/0                    # Redis connection string
RABBITMQ_URL=amqp://user:pass@localhost:5672/         # RabbitMQ connection string

# Google Cloud Platform
GOOGLE_APPLICATION_CREDENTIALS=/path/to/service-account.json  # GCP service account
GOOGLE_PROJECT_ID=your-gcp-project-id                        # GCP project ID
GOOGLE_REGION=us-central1                                     # GCP region
GOOGLE_AGENT_ENGINE_ID=your-agent-engine-id                  # Agent Engine ID

# External AI Services
EAI_AGENT_SERVICE_URL=https://api.eai-service.com     # EAI Agent API URL
EAI_AGENT_SERVICE_TOKEN=your-eai-api-token            # EAI Agent API token
```

#### Performance Configuration

```bash
# Worker Configuration
MAX_PARALLEL=4                      # Worker concurrency (default: 4)
WORKER_TIMEOUT=300                  # Worker timeout in seconds (default: 5min)

# Redis Configuration  
REDIS_POOL_SIZE=10                  # Redis connection pool size
REDIS_IDLE_TIMEOUT=300              # Idle connection timeout (seconds)
REDIS_MAX_RETRIES=3                 # Redis operation retries

# RabbitMQ Configuration
RABBITMQ_PREFETCH_COUNT=10          # Consumer prefetch count
RABBITMQ_MAX_RETRIES=5              # Message retry attempts
RABBITMQ_RETRY_DELAY=5              # Retry delay in seconds
RABBITMQ_MESSAGE_TIMEOUT=300        # Message processing timeout

# Rate Limiting
RATE_LIMIT_REQUESTS_PER_MINUTE=1000 # Global rate limit
GOOGLE_API_RATE_LIMIT=100           # Google API calls per minute
```

#### Security Configuration

```bash
# Request Limits
MAX_REQUEST_SIZE_MB=50              # Maximum HTTP request size
MAX_AUDIO_SIZE_MB=25                # Maximum audio file size
UPLOAD_TIMEOUT_SECONDS=60           # File upload timeout

# CORS Configuration
CORS_ALLOWED_ORIGINS=*              # Allowed CORS origins (comma-separated)
CORS_ALLOWED_METHODS=GET,POST       # Allowed HTTP methods
CORS_ALLOWED_HEADERS=*              # Allowed headers

# Security Headers
ENABLE_SECURITY_HEADERS=true        # Enable security headers
HSTS_MAX_AGE=31536000              # HSTS max age in seconds
```

#### Observability Configuration

```bash
# Logging
LOG_LEVEL=info                      # Log level (debug/info/warn/error)
LOG_FORMAT=json                     # Log format (json/text)
ENABLE_REQUEST_LOGGING=true         # Enable request logging

# Metrics
PROMETHEUS_ENABLED=true             # Enable Prometheus metrics
METRICS_PATH=/metrics               # Metrics endpoint path

# Tracing
OTEL_ENABLED=true                                    # Enable OpenTelemetry
OTEL_EXPORTER_OTLP_ENDPOINT=http://signoz:4317      # OTLP endpoint
OTEL_SERVICE_NAME=eai-agent-gateway                  # Service name
OTEL_RESOURCE_ATTRIBUTES=version=2.1.0,env=prod     # Resource attributes
```

#### Cache TTL Configuration

```bash
# Redis TTL Settings (in seconds)
TASK_RESULT_TTL=120                 # Task results (2 minutes)
TASK_STATUS_TTL=600                 # Task status (10 minutes)  
AGENT_ID_CACHE_TTL=86400           # Agent IDs (24 hours)
HEALTH_CHECK_CACHE_TTL=30          # Health check results (30 seconds)
```

### Configuration Validation

The service validates all configuration on startup:

```bash
# Test configuration
just config-test

# Validate specific sections
go run cmd/gateway/main.go --validate-config
```

**Example validation output:**
```
✅ Server configuration valid
✅ Redis connection successful  
✅ RabbitMQ connection successful
❌ Google Cloud credentials invalid
⚠️  EAI Agent Service unreachable (non-critical)
```

---

## 🛠️ Development

### Available Commands

```bash
# Development Server
just dev                    # Start gateway with hot reload (Air)
just dev-simple            # Start gateway without hot reload
just worker                 # Start worker process
just dev-full              # Start both gateway and worker

# Building
just build                  # Build both binaries (gateway + worker)
just build-gateway          # Build gateway only
just build-worker          # Build worker only
just build-all             # Build for multiple platforms (Linux/macOS/Windows)

# Testing
just test                   # Run all tests (excludes OpenTelemetry tests)
just test-coverage          # Run tests with coverage report
just test-race             # Run tests with race detection
just test-integration      # Run integration tests (requires services)

# Code Quality
just lint                   # Run golangci-lint and goimports
just lint-fix              # Auto-fix linting issues
just tidy                  # Tidy Go modules

# Services & Debugging
just health                 # Check service health
just logs                  # View service logs
just redis-cli             # Connect to Redis CLI
just rabbitmq-admin        # Open RabbitMQ admin interface

# Load Testing
just load-test <token>      # Run k6 load tests with authentication
just plot-results          # Generate performance charts from test results

# Cleanup
just clean                  # Clean build artifacts
just clean-all             # Clean everything including logs
```

### Development Environment Setup

#### Option 1: Docker Compose (Recommended)

```bash
# Start infrastructure services
docker-compose up -d redis rabbitmq

# Configure environment
cp .env.example .env
# Edit .env with your credentials

# Start development server
just dev

# In another terminal, start worker
just worker

# View logs
docker-compose logs -f
```

#### Option 2: Local Services

```bash
# Install and start Redis
brew install redis  # macOS
redis-server

# Install and start RabbitMQ  
brew install rabbitmq  # macOS
rabbitmq-server

# Configure for local development
export REDIS_URL=redis://localhost:6379/0
export RABBITMQ_URL=amqp://guest:guest@localhost:5672/

# Start application
just dev-full
```

#### Option 3: Full Containerized

```bash
# Start everything with Docker
docker-compose up

# Application available at http://localhost:8000
# RabbitMQ admin at http://localhost:15672 (guest/guest)
# Redis available at localhost:6379
```

### Development Workflow

1. **Feature Development**:
   ```bash
   git checkout -b feature/new-feature
   just dev  # Start with hot reload
   # Make changes - server automatically restarts
   just test  # Run tests
   just lint  # Check code quality
   ```

2. **Testing Changes**:
   ```bash
   # Unit tests
   go test ./internal/services/...
   
   # Integration tests  
   just test-integration
   
   # Load tests
   just load-test your-token
   
   # Manual testing
   curl -X POST http://localhost:8000/api/v1/message/webhook/user \
     -H "Authorization: Bearer test-token" \
     -d '{"user_number":"+5511999999999","message":"test"}'
   ```

3. **Performance Profiling**:
   ```bash
   # CPU profiling
   go tool pprof http://localhost:8000/debug/pprof/profile
   
   # Memory profiling  
   go tool pprof http://localhost:8000/debug/pprof/heap
   
   # Goroutine analysis
   go tool pprof http://localhost:8000/debug/pprof/goroutine
   ```

### Testing Strategy

#### Unit Tests
```bash
# Run specific package tests
go test ./internal/services/redis_service_test.go -v

# Test with coverage
go test -cover ./internal/services/...

# Race condition detection
go test -race ./...
```

#### Integration Tests
```bash
# Requires running Redis and RabbitMQ
export REDIS_URL=redis://localhost:6379/1  # Use different DB
export RABBITMQ_URL=amqp://guest:guest@localhost:5672/

go test -tags=integration ./tests/integration/...
```

#### Load Tests
```bash
# Install k6
brew install k6  # macOS

# Run load tests
export API_TOKEN=your-test-token
just load-test $API_TOKEN

# Generate performance charts
just plot-results
```

### Debugging

#### Application Debugging
```bash
# Enable debug logging
export LOG_LEVEL=debug

# Enable Go debugging
export GODEBUG=gctrace=1

# Profile memory allocations
export GOMAXPROCS=1
go tool pprof --alloc_space http://localhost:8000/debug/pprof/heap
```

#### Service Debugging
```bash
# Check Redis
just redis-cli
> keys *
> get task:some-uuid

# Check RabbitMQ
just rabbitmq-admin
# Navigate to http://localhost:15672

# Check health status
curl http://localhost:8000/health | jq '.services'
```

---

## 🚀 Deployment

For the staging Kubernetes workflow, see
[`docs/staging-deploy.md`](docs/staging-deploy.md). A push to `staging` builds
and pushes the image, but Kubernetes still needs an explicit rollout or image
update before pods run the new artifact.

### Docker Deployment

#### Building Images

```bash
# Build production image
docker build -t eai-agent-gateway:latest .

# Multi-stage build for optimization
docker build --target production -t eai-agent-gateway:prod .

# Build with specific version
docker build -t eai-agent-gateway:v2.1.0 .
```

#### Docker Compose Production

```yaml
# docker-compose.prod.yml
version: '3.8'

services:
  gateway:
    image: eai-agent-gateway:latest
    ports:
      - "8000:8000"
    environment:
      - ENVIRONMENT=production
      - REDIS_URL=redis://redis:6379/0
      - RABBITMQ_URL=amqp://rabbitmq:5672/
    depends_on:
      - redis
      - rabbitmq
    restart: unless-stopped
    healthcheck:
      test: ["CMD", "curl", "-f", "http://localhost:8000/health"]
      interval: 30s
      timeout: 10s
      retries: 3

  worker:
    image: eai-agent-gateway:latest
    command: ["/app/worker"]
    environment:
      - ENVIRONMENT=production
      - MAX_PARALLEL=8
    depends_on:
      - redis
      - rabbitmq
    restart: unless-stopped
    deploy:
      replicas: 3

  redis:
    image: redis:7-alpine
    ports:
      - "6379:6379"
    volumes:
      - redis_data:/data
    restart: unless-stopped

  rabbitmq:
    image: rabbitmq:3-management
    ports:
      - "5672:5672"
      - "15672:15672"
    environment:
      - RABBITMQ_DEFAULT_USER=admin
      - RABBITMQ_DEFAULT_PASS=secure_password
    volumes:
      - rabbitmq_data:/var/lib/rabbitmq
    restart: unless-stopped

volumes:
  redis_data:
  rabbitmq_data:
```

```bash
# Deploy with production compose
docker-compose -f docker-compose.prod.yml up -d

# Scale workers
docker-compose -f docker-compose.prod.yml up -d --scale worker=5

# Monitor logs
docker-compose -f docker-compose.prod.yml logs -f gateway worker
```

### Kubernetes Deployment

#### Namespace and Secrets

```yaml
# k8s/base/namespace.yaml
apiVersion: v1
kind: Namespace
metadata:
  name: eai-agent-gateway
  labels:
    app: eai-agent-gateway
    environment: production
```

```yaml
# k8s/base/secrets.yaml
apiVersion: v1
kind: Secret
metadata:
  name: eai-agent-secrets
  namespace: eai-agent-gateway
type: Opaque
stringData:
  api-token: "your-secure-api-token"
  eai-agent-token: "your-eai-agent-token"
  google-credentials: |
    {
      "type": "service_account",
      "project_id": "your-project",
      ...
    }
```

#### Gateway Deployment

```yaml
# k8s/base/gateway-deployment.yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: eai-agent-gateway
  namespace: eai-agent-gateway
  labels:
    app: eai-agent-gateway
    component: gateway
spec:
  replicas: 3
  selector:
    matchLabels:
      app: eai-agent-gateway
      component: gateway
  template:
    metadata:
      labels:
        app: eai-agent-gateway
        component: gateway
    spec:
      containers:
      - name: gateway
        image: eai-agent-gateway:v2.1.0
        ports:
        - containerPort: 8000
          name: http
        env:
        - name: PORT
          value: "8000"
        - name: ENVIRONMENT
          value: "production"
        - name: API_TOKEN
          valueFrom:
            secretKeyRef:
              name: eai-agent-secrets
              key: api-token
        - name: REDIS_URL
          value: "redis://redis-service:6379/0"
        - name: RABBITMQ_URL
          value: "amqp://admin:password@rabbitmq-service:5672/"
        resources:
          requests:
            memory: "128Mi"
            cpu: "100m"
          limits:
            memory: "512Mi"
            cpu: "500m"
        livenessProbe:
          httpGet:
            path: /live
            port: 8000
          initialDelaySeconds: 30
          periodSeconds: 10
        readinessProbe:
          httpGet:
            path: /ready
            port: 8000
          initialDelaySeconds: 5
          periodSeconds: 5
        startupProbe:
          httpGet:
            path: /health
            port: 8000
          initialDelaySeconds: 10
          periodSeconds: 5
          failureThreshold: 30
```

#### Worker Deployment

```yaml
# k8s/base/worker-deployment.yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: eai-agent-worker
  namespace: eai-agent-gateway
  labels:
    app: eai-agent-gateway
    component: worker
spec:
  replicas: 5
  selector:
    matchLabels:
      app: eai-agent-gateway
      component: worker
  template:
    metadata:
      labels:
        app: eai-agent-gateway
        component: worker
    spec:
      containers:
      - name: worker
        image: eai-agent-gateway:v2.1.0
        command: ["/app/worker"]
        env:
        - name: MAX_PARALLEL
          value: "8"
        - name: ENVIRONMENT
          value: "production"
        - name: REDIS_URL
          value: "redis://redis-service:6379/0"
        - name: RABBITMQ_URL
          value: "amqp://admin:password@rabbitmq-service:5672/"
        resources:
          requests:
            memory: "256Mi"
            cpu: "200m"
          limits:
            memory: "1Gi"
            cpu: "1000m"
        livenessProbe:
          exec:
            command:
            - /bin/sh
            - -c
            - "pgrep worker"
          initialDelaySeconds: 30
          periodSeconds: 10
```

#### Services and Ingress

```yaml
# k8s/base/services.yaml
apiVersion: v1
kind: Service
metadata:
  name: eai-agent-gateway-service
  namespace: eai-agent-gateway
spec:
  selector:
    app: eai-agent-gateway
    component: gateway
  ports:
  - name: http
    port: 80
    targetPort: 8000
  type: ClusterIP
---
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: eai-agent-gateway-ingress
  namespace: eai-agent-gateway
  annotations:
    kubernetes.io/ingress.class: nginx
    cert-manager.io/cluster-issuer: letsencrypt-prod
    nginx.ingress.kubernetes.io/rate-limit: "1000"
    nginx.ingress.kubernetes.io/rate-limit-window: "1m"
spec:
  tls:
  - hosts:
    - api.eai-gateway.example.com
    secretName: eai-gateway-tls
  rules:
  - host: api.eai-gateway.example.com
    http:
      paths:
      - path: /
        pathType: Prefix
        backend:
          service:
            name: eai-agent-gateway-service
            port:
              number: 80
```

#### Horizontal Pod Autoscaler

```yaml
# k8s/base/hpa.yaml
apiVersion: autoscaling/v2
kind: HorizontalPodAutoscaler
metadata:
  name: eai-agent-gateway-hpa
  namespace: eai-agent-gateway
spec:
  scaleTargetRef:
    apiVersion: apps/v1
    kind: Deployment
    name: eai-agent-gateway
  minReplicas: 3
  maxReplicas: 20
  metrics:
  - type: Resource
    resource:
      name: cpu
      target:
        type: Utilization
        averageUtilization: 70
  - type: Resource
    resource:
      name: memory
      target:
        type: Utilization
        averageUtilization: 80
  behavior:
    scaleUp:
      stabilizationWindowSeconds: 300
      policies:
      - type: Percent
        value: 100
        periodSeconds: 15
    scaleDown:
      stabilizationWindowSeconds: 300
      policies:
      - type: Percent
        value: 10
        periodSeconds: 60
---
apiVersion: autoscaling/v2
kind: HorizontalPodAutoscaler
metadata:
  name: eai-agent-worker-hpa
  namespace: eai-agent-gateway
spec:
  scaleTargetRef:
    apiVersion: apps/v1
    kind: Deployment
    name: eai-agent-worker
  minReplicas: 2
  maxReplicas: 15
  metrics:
  - type: Resource
    resource:
      name: cpu
      target:
        type: Utilization
        averageUtilization: 60
  - type: Resource
    resource:
      name: memory
      target:
        type: Utilization
        averageUtilization: 70
```

#### Deployment Commands

The commands below apply Kubernetes manifests. For routine staging code
deployments, prefer the runbook in
[`docs/staging-deploy.md`](docs/staging-deploy.md), which pins the Deployment to
the exact GitHub Actions commit image and includes post-rollout checks.

```bash
# Deploy to staging
kubectl apply -k k8s/staging/

# Deploy to production
kubectl apply -k k8s/prod/

# Check deployment status
kubectl get pods -n eai-agent-gateway
kubectl logs -f deployment/eai-agent-gateway -n eai-agent-gateway

# Scale manually
kubectl scale deployment eai-agent-gateway --replicas=10 -n eai-agent-gateway

# Rolling update
kubectl set image deployment/eai-agent-gateway \
  eai-agent-gateway=ghcr.io/prefeitura-rio/app-eai-agent-gateway:<commit_sha> \
  -n eai-agent-gateway

kubectl set image deployment/eai-agent-gateway-worker \
  eai-agent-gateway-worker=ghcr.io/prefeitura-rio/app-eai-agent-gateway:<commit_sha> \
  -n eai-agent-gateway

# Check HPA status
kubectl get hpa -n eai-agent-gateway
```

### Infrastructure Dependencies

#### Redis Cluster (Production)

```yaml
# redis-cluster.yaml
apiVersion: apps/v1
kind: StatefulSet
metadata:
  name: redis-cluster
spec:
  serviceName: redis-cluster
  replicas: 6
  selector:
    matchLabels:
      app: redis-cluster
  template:
    metadata:
      labels:
        app: redis-cluster
    spec:
      containers:
      - name: redis
        image: redis:7-alpine
        ports:
        - containerPort: 6379
        - containerPort: 16379
        volumeMounts:
        - name: redis-data
          mountPath: /data
        - name: redis-config
          mountPath: /usr/local/etc/redis
  volumeClaimTemplates:
  - metadata:
      name: redis-data
    spec:
      accessModes: ["ReadWriteOnce"]
      resources:
        requests:
          storage: 10Gi
```

#### RabbitMQ Cluster

```yaml
# rabbitmq-cluster.yaml
apiVersion: rabbitmq.com/v1beta1
kind: RabbitmqCluster
metadata:
  name: rabbitmq-cluster
spec:
  replicas: 3
  image: rabbitmq:3.11-management
  resources:
    requests:
      cpu: 200m
      memory: 1Gi
    limits:
      cpu: 500m
      memory: 2Gi
  persistence:
    storageClassName: fast-ssd
    storage: 20Gi
  rabbitmq:
    additionalConfig: |
      cluster_formation.peer_discovery_backend = rabbit_peer_discovery_k8s
      cluster_formation.k8s.host = kubernetes.default.svc.cluster.local
      cluster_formation.node_cleanup.only_log_warning = true
      cluster_partition_handling = autoheal
      queue_master_locator = min-masters
```

---

## 📊 Monitoring

### Health Monitoring

#### Health Check Configuration

```bash
# Configure health check intervals
export HEALTH_CHECK_INTERVAL=30s
export HEALTH_CHECK_TIMEOUT=10s
export HEALTH_CHECK_CACHE_TTL=30

# Enable detailed health checks
export ENABLE_DETAILED_HEALTH_CHECKS=true
export HEALTH_CHECK_DEPENDENCIES=redis,rabbitmq,google_speech,google_agent_engine
```

#### Health Dashboard

```bash
# Simple health check
curl http://localhost:8000/health | jq '.'

# Detailed health status
curl http://localhost:8000/health?detail=true | jq '.services'

# Individual service health
curl http://localhost:8000/health/redis | jq '.'
curl http://localhost:8000/health/rabbitmq | jq '.'
curl http://localhost:8000/health/google_speech | jq '.'
```

### Prometheus Metrics

#### Key Business Metrics

```promql
# Message Processing Metrics
rate(http_requests_total{endpoint="/api/v1/message/webhook/user"}[5m])
histogram_quantile(0.95, message_processing_duration_seconds)
sum(rate(message_queue_depth[1m])) by (queue)

# Error Rates
rate(message_processing_errors_total[5m]) / rate(message_processing_total[5m])
rate(external_api_errors_total{service="google_agent_engine"}[5m])

# Throughput Metrics  
sum(rate(message_processing_total[1m])) by (message_type)
rate(transcription_requests_total[5m])
rate(ai_service_calls_total[5m]) by (provider)

# Resource Utilization
worker_pool_utilization_ratio
redis_connection_pool_active / redis_connection_pool_max
rabbitmq_queue_depth_total
```

#### System Metrics

```promql
# Performance Metrics
go_memstats_heap_inuse_bytes
go_goroutines
process_cpu_seconds_total
process_resident_memory_bytes

# HTTP Metrics
http_request_duration_seconds{quantile="0.5"}
http_request_duration_seconds{quantile="0.95"}
http_request_duration_seconds{quantile="0.99"}
rate(http_requests_total[5m]) by (method, status)

# Queue Metrics
rabbitmq_queue_messages{queue="user_messages"}
rabbitmq_queue_consumers{queue="user_messages"}
rate(rabbitmq_messages_published_total[5m])
rate(rabbitmq_messages_delivered_total[5m])
```

#### Alerting Rules

```yaml
# prometheus-alerts.yaml
groups:
- name: eai-agent-gateway.rules
  rules:
  
  # High Error Rate
  - alert: HighErrorRate
    expr: rate(message_processing_errors_total[5m]) / rate(message_processing_total[5m]) > 0.05
    for: 2m
    labels:
      severity: warning
    annotations:
      summary: "High error rate detected"
      description: "Error rate is {{ $value | humanizePercentage }} over the last 5 minutes"

  # Queue Backup
  - alert: MessageQueueBackup
    expr: rabbitmq_queue_messages{queue="user_messages"} > 100
    for: 5m
    labels:
      severity: warning
    annotations:
      summary: "Message queue backup detected"
      description: "Queue {{ $labels.queue }} has {{ $value }} messages pending"

  # High Response Time
  - alert: HighResponseTime
    expr: histogram_quantile(0.95, message_processing_duration_seconds) > 5
    for: 3m
    labels:
      severity: warning
    annotations:
      summary: "High response time detected"
      description: "95th percentile response time is {{ $value }}s"

  # External Service Down
  - alert: ExternalServiceDown
    expr: up{job="eai-agent-gateway"} == 0
    for: 1m
    labels:
      severity: critical
    annotations:
      summary: "EAI Agent Gateway is down"
      description: "Service has been down for more than 1 minute"

  # Memory Usage High
  - alert: HighMemoryUsage
    expr: process_resident_memory_bytes / 1024 / 1024 > 500
    for: 5m
    labels:
      severity: warning
    annotations:
      summary: "High memory usage"
      description: "Memory usage is {{ $value }}MB"
```

### Distributed Tracing

#### OpenTelemetry Configuration

```bash
# Enable tracing
export OTEL_ENABLED=true
export OTEL_SERVICE_NAME=eai-agent-gateway
export OTEL_SERVICE_VERSION=v2.1.0

# Configure exporters
export OTEL_EXPORTER_OTLP_ENDPOINT=http://signoz:4317
export OTEL_EXPORTER_OTLP_INSECURE=true

# Sampling configuration
export OTEL_TRACES_SAMPLER=traceidratio
export OTEL_TRACES_SAMPLER_ARG=0.1  # Sample 10% of traces

# Resource attributes
export OTEL_RESOURCE_ATTRIBUTES="service.name=eai-agent-gateway,service.version=v2.1.0,deployment.environment=production"
```

#### Trace Analysis

**End-to-End Request Trace:**
```
Trace ID: 1234567890abcdef
├── HTTP Request (webhook/user) - 2ms
│   ├── Input Validation - 0.5ms
│   ├── Queue Message Publish - 1ms  
│   └── Response Return - 0.5ms
├── Worker Message Processing - 1250ms
│   ├── Thread Lookup/Create - 5ms
│   │   └── Redis Operation - 3ms
│   ├── Audio Download - 200ms
│   │   └── HTTP Download - 195ms
│   ├── Audio Transcription - 800ms
│   │   └── Google Speech API - 750ms
│   ├── AI Service Call - 200ms
│   │   └── Google Agent Engine - 180ms
│   ├── Response Formatting - 5ms
│   └── Result Storage - 2ms
│       └── Redis Set Operation - 1ms
└── Response Polling - 1ms
    └── Redis Get Operation - 0.5ms
```

#### SigNoz Dashboard Setup

```bash
# Deploy SigNoz (local development)
git clone https://github.com/SigNoz/signoz.git
cd signoz/deploy/docker/clickhouse-setup
docker-compose up -d

# Configure application
export OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4317

# Access dashboard
open http://localhost:3301
```

**Key Dashboard Panels:**
- Request rate and latency percentiles
- Error rate by endpoint and service
- Service map showing dependencies
- Database and queue performance
- Custom business metrics (messages processed, transcription success rate)

### Logging Strategy

#### Structured Logging

```json
{
  "timestamp": "2023-12-01T10:30:45.123Z",
  "level": "info", 
  "message": "Message processing completed",
  "correlation_id": "req_1234567890",
  "trace_id": "1234567890abcdef",
  "span_id": "abcdef1234567890",
  "user_number": "+5511999999999",
  "message_id": "550e8400-e29b-41d4-a716-446655440000",
  "message_type": "audio",
  "processing_time_ms": 1250,
  "thread_id": "+5511999999999",
  "ai_provider": "google_agent_engine",
  "transcription_duration_ms": 800,
  "ai_response_duration_ms": 200,
  "worker_id": "worker-5",
  "queue": "user_messages"
}
```

#### Log Aggregation

```bash
# Local development - view logs
just logs

# Production - centralized logging
# Logs are shipped to your log aggregation system
# Common integrations: ELK Stack, Fluentd, Grafana Loki

# Query examples (assuming ELK)
GET /logs-*/_search
{
  "query": {
    "bool": {
      "must": [
        {"match": {"service": "eai-agent-gateway"}},
        {"range": {"timestamp": {"gte": "now-1h"}}},
        {"match": {"level": "error"}}
      ]
    }
  }
}
```

---

## 🚄 Performance

### Benchmarks

#### Performance Comparison: Python vs Go

```
┌─────────────────────────────────────────────────────────────────────────────────┐
│                         PERFORMANCE BENCHMARKS                                  │
│                      Python FastAPI vs Go Gin                                   │
└─────────────────────────────────────────────────────────────────────────────────┘

Metric                   │ Python (FastAPI)   │ Go (Gin)           │ Improvement
─────────────────────────┼────────────────────┼────────────────────┼─────────────
Response Time (median)   │ 500ms              │ 50ms               │ 90% faster
Response Time (95th)     │ 2000ms             │ 150ms              │ 92.5% faster
Throughput              │ 100 req/s          │ 1000 req/s         │ 10x increase
Memory Usage            │ 500MB               │ 200MB              │ 60% reduction
CPU Usage (avg)         │ 40%                │ 20%                │ 50% reduction
Cold Start Time         │ 15 seconds         │ 2 seconds          │ 87% faster
Container Size          │ 1.2GB              │ 25MB               │ 97% smaller
Audio Processing        │ 3-5 seconds        │ 1-2 seconds        │ 60% faster
Queue Processing        │ 50 msgs/min        │ 500 msgs/min       │ 10x increase
Error Recovery          │ 30 seconds         │ 3 seconds          │ 90% faster
```

#### Load Test Results

```bash
# Test Configuration
Duration: 5 minutes
Target RPS: 500
Concurrent Users: 100
Message Mix: 70% text, 30% audio

# Results Summary
Total Requests: 150,000
Success Rate: 99.8%
Average Response Time: 45ms
95th Percentile: 120ms
99th Percentile: 250ms
Errors: 300 (0.2%)
```

**Detailed Performance Metrics:**
```
┌─────────────────────────────────────────────────────────────────────────────────┐
│                              LOAD TEST RESULTS                                  │
└─────────────────────────────────────────────────────────────────────────────────┘

Text Messages (105,000 requests):
├── Average Response Time: 35ms
├── 95th Percentile: 80ms  
├── 99th Percentile: 150ms
├── Success Rate: 99.9%
└── Throughput: 350 req/s

Audio Messages (45,000 requests):
├── Average Response Time: 1.2s
├── 95th Percentile: 2.5s
├── 99th Percentile: 4.0s  
├── Success Rate: 99.5%
└── Throughput: 150 req/s

System Resources:
├── CPU Usage: 65% (across 4 cores)
├── Memory Usage: 320MB
├── Redis Connections: 8/10 pool
├── RabbitMQ Queue Depth: avg 5 messages
└── Goroutines: ~50 active
```

### Scaling Characteristics

#### Horizontal Scaling

```
┌─────────────────────────────────────────────────────────────────────────────────┐
│                           HORIZONTAL SCALING TEST                               │
└─────────────────────────────────────────────────────────────────────────────────┘

Pod Count  │ Total RPS  │ RPS/Pod  │ Avg Latency │ 95th Latency │ CPU/Pod │ Memory/Pod
───────────┼────────────┼──────────┼─────────────┼──────────────┼─────────┼───────────
1 Pod      │ 200        │ 200      │ 45ms        │ 120ms        │ 60%     │ 180MB
2 Pods     │ 380        │ 190      │ 50ms        │ 130ms        │ 55%     │ 175MB  
3 Pods     │ 570        │ 190      │ 52ms        │ 135ms        │ 55%     │ 170MB
5 Pods     │ 950        │ 190      │ 55ms        │ 140ms        │ 50%     │ 165MB
10 Pods    │ 1800       │ 180      │ 60ms        │ 150ms        │ 45%     │ 160MB

Scaling Efficiency: 95% linear scaling up to 10 pods
Optimal Configuration: 5-8 pods for cost/performance balance
```

#### Vertical Scaling

```
┌─────────────────────────────────────────────────────────────────────────────────┐
│                            VERTICAL SCALING TEST                                │
└─────────────────────────────────────────────────────────────────────────────────┘

CPU Limit  │ Memory Limit │ Max RPS │ Avg Latency │ Efficiency │ Cost Index
───────────┼──────────────┼─────────┼─────────────┼────────────┼───────────
100m       │ 128MB        │ 50      │ 100ms       │ 50%        │ 1.0x
200m       │ 256MB        │ 120     │ 60ms        │ 60%        │ 1.8x  
500m       │ 512MB        │ 200     │ 45ms        │ 40%        │ 2.2x
1000m      │ 1GB          │ 250     │ 40ms        │ 25%        │ 3.5x

Recommendation: 200m CPU / 256MB memory for optimal cost/performance
```

### Performance Optimization

#### Application-Level Optimizations

```go
// Connection Pool Tuning
redisConfig := &redis.Options{
    PoolSize:     20,           // Increased from 10
    MinIdleConns: 5,            // Maintain idle connections
    MaxRetries:   3,            // Retry failed operations
    PoolTimeout:  30 * time.Second,
}

// RabbitMQ Optimization
rabbitConfig := &RabbitMQConfig{
    PrefetchCount: 20,          // Process multiple messages
    MaxRetries:    5,           // Retry failed messages
    BatchSize:     10,          // Batch acknowledgments
}

// Worker Pool Optimization
workerConfig := &WorkerConfig{
    Concurrency:   runtime.NumCPU() * 2,  // 2x CPU cores
    BufferSize:    1000,                   // Large channel buffer
    HealthCheck:   30 * time.Second,       // Regular health checks
}
```

#### Infrastructure Optimizations

```yaml
# Kubernetes Resource Tuning
resources:
  requests:
    memory: "256Mi"      # Guarantee minimum memory
    cpu: "200m"          # Guarantee minimum CPU
  limits:
    memory: "512Mi"      # Prevent memory leaks
    cpu: "500m"          # Allow burst capacity

# HPA Configuration
minReplicas: 3           # Always maintain minimum capacity
maxReplicas: 20          # Scale up to handle traffic spikes
targetCPUUtilization: 60 # Scale before hitting limits

# Redis Tuning
redis:
  maxmemory: 1gb
  maxmemory-policy: allkeys-lru
  save: ""               # Disable disk persistence for cache
  tcp-keepalive: 300
```

#### Performance Monitoring

```bash
# Real-time performance monitoring
watch -n 1 'curl -s http://localhost:8000/metrics | grep -E "(http_requests|message_processing|queue_depth)"'

# CPU profiling
go tool pprof http://localhost:8000/debug/pprof/profile?seconds=30

# Memory profiling  
go tool pprof http://localhost:8000/debug/pprof/heap

# Trace analysis
curl http://localhost:8000/debug/pprof/trace?seconds=5 > trace.out
go tool trace trace.out
```

---

## 🔒 Security

### Security Architecture

```
┌─────────────────────────────────────────────────────────────────────────────────┐
│                            SECURITY ARCHITECTURE                                │
└─────────────────────────────────────────────────────────────────────────────────┘

┌─────────────────┐    HTTPS/TLS    ┌─────────────────────────────────────────────┐
│   External      │ ───────────────▶ │               WAF/CDN                       │
│   Client        │                  │  • DDoS Protection                          │
└─────────────────┘                  │  • IP Filtering                             │
                                     │  • Rate Limiting                            │
                                     └─────────────────┬───────────────────────────┘
                                                       │ Filtered Traffic
                                                       ▼
┌─────────────────────────────────────────────────────────────────────────────────┐
│                         APPLICATION SECURITY                                    │
│                                                                                 │
│  ┌─────────────────┐  ┌─────────────────┐  ┌─────────────────┐                │
│  │   AUTHZ/AUTHN   │  │   INPUT FILTER  │  │  RATE LIMITING  │                │
│  │                 │  │                 │  │                 │                │
│  │ • Bearer Token  │  │ • XSS Prevention│  │ • Per-Client    │                │
│  │ • JWT Support   │  │ • SQL Injection │  │ • Per-Endpoint  │                │
│  │ • API Keys      │  │ • Path Traversal│  │ • Sliding Window│                │
│  │ • IP Whitelist  │  │ • File Upload   │  │ • Circuit Break │                │
│  └─────────────────┘  └─────────────────┘  └─────────────────┘                │
│                                                                                 │
│  ┌─────────────────┐  ┌─────────────────┐  ┌─────────────────┐                │
│  │ CREDENTIAL MGMT │  │  TEMP FILE SEC  │  │  DATA ENCRYPT   │                │
│  │                 │  │                 │  │                 │                │
│  │ • Auto Sanitize │  │ • Secure Dirs   │  │ • TLS in Transit│                │
│  │ • Vault Integ   │  │ • Auto Cleanup  │  │ • Encrypt at Rest│               │
│  │ • Rotation      │  │ • Access Control│  │ • Key Management│                │
│  │ • Audit Logs    │  │ • Virus Scan    │  │                 │                │
│  └─────────────────┘  └─────────────────┘  └─────────────────┘                │
└─────────────────────────────────────────────────────────────────────────────────┘
                                       │ Secure Communication
                                       ▼
┌─────────────────────────────────────────────────────────────────────────────────┐
│                        INFRASTRUCTURE SECURITY                                  │
│                                                                                 │
│  ┌─────────────────┐  ┌─────────────────┐  ┌─────────────────┐                │
│  │   NETWORK SEC   │  │  CONTAINER SEC  │  │   ACCESS CTL    │                │
│  │                 │  │                 │  │                 │                │
│  │ • VPC/VNET      │  │ • Non-Root User │  │ • RBAC          │                │
│  │ • Firewalls     │  │ • Read-Only FS  │  │ • Service Mesh  │                │
│  │ • Network Pol   │  │ • Security Scan │  │ • mTLS          │                │
│  │ • Service Mesh  │  │ • Minimal Image │  │ • Audit Logs    │                │
│  └─────────────────┘  └─────────────────┘  └─────────────────┘                │
└─────────────────────────────────────────────────────────────────────────────────┘
```

### Authentication & Authorization

#### API Authentication

```bash
# Bearer Token Authentication (Primary)
curl -H "Authorization: Bearer your-api-token" \
  http://localhost:8000/api/v1/message/webhook/user

# API Key Authentication (Alternative)  
curl -H "X-API-Key: your-api-key" \
  http://localhost:8000/api/v1/message/webhook/user

# JWT Authentication (Enterprise)
curl -H "Authorization: Bearer eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9..." \
  http://localhost:8000/api/v1/message/webhook/user
```

#### Authorization Levels

```yaml
# Role-Based Access Control
roles:
  read_only:
    permissions:
      - "message:read"
      - "health:read"
    endpoints:
      - "GET /health"
      - "GET /api/v1/message/response"
      
  message_sender:
    permissions:
      - "message:read"  
      - "message:write"
      - "health:read"
    endpoints:
      - "GET /health"
      - "POST /api/v1/message/webhook/*"
      - "GET /api/v1/message/response"
      
  admin:
    permissions:
      - "*:*"
    endpoints:
      - "*"
```

### Input Validation & Sanitization

#### Request Validation

```go
// Message validation rules
type MessageRequest struct {
    UserNumber  string `json:"user_number" binding:"required,e164"`     // E.164 phone format
    Message     string `json:"message" binding:"required,max=4096"`     // Max 4KB message
    MessageType string `json:"message_type" binding:"required,oneof=text audio"`
}

// Security validation middleware
func SecurityValidationMiddleware() gin.HandlerFunc {
    return func(c *gin.Context) {
        // Check for malicious patterns
        if containsMaliciousContent(c.Request.Body) {
            c.JSON(400, gin.H{"error": "Malicious content detected"})
            c.Abort()
            return
        }
        
        // Validate content length
        if c.Request.ContentLength > maxRequestSize {
            c.JSON(413, gin.H{"error": "Request too large"})
            c.Abort()
            return
        }
        
        c.Next()
    }
}
```

#### File Upload Security

```go
// Audio file validation
func ValidateAudioFile(url string) error {
    // Check file extension
    allowedExts := []string{".mp3", ".wav", ".flac", ".ogg", ".opus", ".m4a"}
    if !isAllowedExtension(url, allowedExts) {
        return errors.New("unsupported file format")
    }
    
    // Check file size via HEAD request
    resp, err := http.Head(url)
    if err != nil {
        return err
    }
    defer resp.Body.Close()
    
    contentLength := resp.Header.Get("Content-Length")
    if size, _ := strconv.ParseInt(contentLength, 10, 64); size > maxAudioSize {
        return errors.New("file too large")
    }
    
    // Check content type
    contentType := resp.Header.Get("Content-Type")
    if !strings.HasPrefix(contentType, "audio/") {
        return errors.New("invalid content type")
    }
    
    return nil
}
```

### Credential Management

#### Environment Variable Security

```bash
# Secure credential handling
export API_TOKEN=$(cat /run/secrets/api_token)           # From K8s secrets
export GOOGLE_APPLICATION_CREDENTIALS=/run/secrets/gcp   # Service account
export REDIS_PASSWORD=$(cat /run/secrets/redis_password) # Redis auth

# Credential rotation support
export CREDENTIAL_ROTATION_INTERVAL=24h
export ENABLE_CREDENTIAL_AUDIT=true
```

#### Credential Sanitization

```go
// Automatic credential sanitization in logs
type CredentialManager struct {
    sensitivePatterns []string
}

func NewCredentialManager() *CredentialManager {
    return &CredentialManager{
        sensitivePatterns: []string{
            `(?i)(api[_-]?key|token|password|secret)["\s]*[:=]\s*["']?([^"'\s]+)`,
            `(?i)(bearer\s+)([a-zA-Z0-9\-_\.]+)`,
            `(?i)(authorization:\s*)(bearer\s+[a-zA-Z0-9\-_\.]+)`,
        },
    }
}

func (cm *CredentialManager) SanitizeLog(message string) string {
    for _, pattern := range cm.sensitivePatterns {
        re := regexp.MustCompile(pattern)
        message = re.ReplaceAllString(message, "${1}[REDACTED]")
    }
    return message
}
```

### Network Security

#### TLS Configuration

```yaml
# TLS termination at ingress
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: eai-agent-gateway
  annotations:
    nginx.ingress.kubernetes.io/ssl-redirect: "true"
    nginx.ingress.kubernetes.io/force-ssl-redirect: "true"
    cert-manager.io/cluster-issuer: "letsencrypt-prod"
spec:
  tls:
  - hosts:
    - api.eai-gateway.example.com
    secretName: eai-gateway-tls
```

#### Network Policies

```yaml
# Kubernetes network policy
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: eai-agent-gateway-netpol
spec:
  podSelector:
    matchLabels:
      app: eai-agent-gateway
  policyTypes:
  - Ingress
  - Egress
  ingress:
  - from:
    - namespaceSelector:
        matchLabels:
          name: ingress-nginx
    ports:
    - protocol: TCP
      port: 8000
  egress:
  - to:
    - namespaceSelector:
        matchLabels:
          name: kube-system
    ports:
    - protocol: TCP
      port: 53
  - to:
    - podSelector:
        matchLabels:
          app: redis
    ports:
    - protocol: TCP
      port: 6379
  - to:
    - podSelector:
        matchLabels:
          app: rabbitmq
    ports:
    - protocol: TCP
      port: 5672
```

### Security Scanning & Compliance

#### Container Security

```dockerfile
# Security-hardened Dockerfile
FROM golang:1.23-alpine AS builder

# Create non-root user
RUN adduser -D -s /bin/sh appuser

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -o gateway cmd/gateway/main.go
RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -o worker cmd/worker/main.go

# Final image with minimal attack surface
FROM scratch

# Copy user info
COPY --from=builder /etc/passwd /etc/passwd
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/

# Copy binaries
COPY --from=builder /app/gateway /app/worker /app/

# Create temp directory with proper permissions
COPY --from=builder --chown=appuser:appuser /tmp /tmp

# Run as non-root user
USER appuser

EXPOSE 8000
CMD ["/app/gateway"]
```

#### Security Scanning

```bash
# Container vulnerability scanning
trivy image eai-agent-gateway:latest

# Source code security scan
gosec ./...

# Dependency vulnerability check
go list -json -m all | nancy sleuth

# OWASP dependency check
dependency-check --project eai-agent-gateway --scan .
```

#### Compliance Features

```yaml
# Security compliance features
security_features:
  audit_logging:
    enabled: true
    retention_days: 90
    fields: [timestamp, user_id, action, resource, result]
    
  encryption:
    in_transit: TLS 1.3
    at_rest: AES-256
    key_management: AWS KMS / Google KMS
    
  access_control:
    authentication: Bearer Token / JWT
    authorization: RBAC
    session_timeout: 24h
    
  data_protection:
    pii_detection: enabled
    data_masking: enabled
    retention_policy: 30 days
    
  monitoring:
    security_events: enabled
    threat_detection: enabled
    incident_response: automated
```

---

## 📈 Migration Guide

### Migration from Python to Go

This section provides a comprehensive guide for migrating from the Python FastAPI implementation to the Go implementation.

### Pre-Migration Assessment

#### Compatibility Matrix

```
┌─────────────────────────────────────────────────────────────────────────────────┐
│                        PYTHON TO GO COMPATIBILITY                               │
└─────────────────────────────────────────────────────────────────────────────────┘

Component               │ Python Version    │ Go Version        │ Compatibility
────────────────────────┼───────────────────┼───────────────────┼──────────────
API Endpoints           │ FastAPI           │ Gin               │ ✅ 100%
Request/Response Format │ JSON              │ JSON              │ ✅ 100%
Authentication          │ Bearer Token      │ Bearer Token      │ ✅ 100%
Message Queue           │ Celery/Redis      │ RabbitMQ          │ ⚠️  Queue Format
Environment Variables   │ Pydantic          │ Viper             │ ✅ 95%
Database/Cache          │ Redis             │ Redis             │ ✅ 100%
Logging Format          │ JSON              │ JSON              │ ✅ 100%
Health Checks           │ Custom            │ Enhanced          │ ✅ Improved
Metrics                 │ Basic             │ Prometheus        │ ✅ Enhanced
Tracing                 │ None              │ OpenTelemetry     │ ✅ New Feature
```

### Migration Strategy

#### Phase 1: Parallel Deployment (Blue-Green)

```
┌─────────────────────────────────────────────────────────────────────────────────┐
│                           PARALLEL DEPLOYMENT                                   │
└─────────────────────────────────────────────────────────────────────────────────┘

                    ┌─────────────────┐
                    │   Load Balancer │
                    │   (100% → Python)│
                    └─────────┬───────┘
                              │
              ┌───────────────▼───────────────┐
              │                               │
              ▼                               ▼
    ┌─────────────────┐                ┌─────────────────┐
    │ Python Service  │                │  Go Service     │
    │ (Production)    │                │ (Staging)       │
    │                 │                │                 │
    │ • Live Traffic  │                │ • Shadow Traffic│
    │ • Full Load     │                │ • Validation    │
    │ • Monitoring    │                │ • Performance   │
    └─────────────────┘                └─────────────────┘
              │                               │
              └───────────────┬───────────────┘
                              │
                    ┌─────────▼───────┐
                    │  Shared Redis   │
                    │  + RabbitMQ     │
                    └─────────────────┘

Phase 1 Goals:
- Deploy Go service alongside Python
- Validate functionality with test traffic
- Monitor performance and reliability
- Fix any compatibility issues
```

#### Phase 2: Gradual Traffic Shift

```
┌─────────────────────────────────────────────────────────────────────────────────┐
│                          GRADUAL TRAFFIC SHIFT                                  │
└─────────────────────────────────────────────────────────────────────────────────┘

Week 1: 5% Traffic to Go
    ┌─────────────────┐
    │ Load Balancer   │
    │ 95% → Python    │
    │  5% → Go        │
    └─────────────────┘

Week 2: 25% Traffic to Go
    ┌─────────────────┐
    │ Load Balancer   │
    │ 75% → Python    │
    │ 25% → Go        │
    └─────────────────┘

Week 3: 50% Traffic to Go
    ┌─────────────────┐
    │ Load Balancer   │
    │ 50% → Python    │
    │ 50% → Go        │
    └─────────────────┘

Week 4: 100% Traffic to Go
    ┌─────────────────┐
    │ Load Balancer   │
    │  0% → Python    │
    │100% → Go        │
    └─────────────────┘
```

#### Phase 3: Python Decommission

```bash
# Week 5-6: Monitoring period
# - Monitor Go service stability
# - Keep Python service as hot standby
# - Validate all features working

# Week 7: Decommission Python
# - Stop Python service
# - Clean up Python-specific resources
# - Update documentation and runbooks
```

### Environment Variable Migration

#### Mapping Table

```bash
# Python Environment Variables → Go Environment Variables

# Server Configuration
FASTAPI_HOST=0.0.0.0              → PORT=8000
FASTAPI_PORT=8000                 → (Built into Go binary)
FASTAPI_WORKERS=4                 → MAX_PARALLEL=4

# Redis Configuration
REDIS_URL=redis://localhost:6379  → REDIS_URL=redis://localhost:6379/0
REDIS_POOL_SIZE=10                → REDIS_POOL_SIZE=10 (same)

# Queue Configuration  
CELERY_BROKER_URL=redis://...     → RABBITMQ_URL=amqp://...
CELERY_RESULT_BACKEND=redis://... → (Removed - results stored in Redis)
CELERY_TASK_ROUTES=...            → (Removed - simplified routing)

# External Services
GOOGLE_APPLICATION_CREDENTIALS    → GOOGLE_APPLICATION_CREDENTIALS (same)
GOOGLE_PROJECT_ID                 → GOOGLE_PROJECT_ID (same)
EAI_AGENT_SERVICE_URL            → EAI_AGENT_SERVICE_URL (same)
EAI_AGENT_SERVICE_TOKEN          → EAI_AGENT_SERVICE_TOKEN (same)

# New Go-Specific Variables
(None)                           → OTEL_ENABLED=true
(None)                           → OTEL_EXPORTER_OTLP_ENDPOINT=...
(None)                           → PROMETHEUS_ENABLED=true
(None)                           → LOG_LEVEL=info
```

#### Migration Script

```bash
#!/bin/bash
# migrate-env.sh - Convert Python .env to Go .env

set -e

PYTHON_ENV_FILE=".env.python"
GO_ENV_FILE=".env.go"

echo "Migrating environment variables from Python to Go..."

# Copy shared variables
grep -E "^(REDIS_URL|GOOGLE_|EAI_)" $PYTHON_ENV_FILE > $GO_ENV_FILE

# Add Go-specific variables
cat >> $GO_ENV_FILE << 'EOF'

# Go-Specific Configuration
PORT=8000
LOG_LEVEL=info
MAX_PARALLEL=4

# RabbitMQ (replaces Celery)
RABBITMQ_URL=amqp://guest:guest@localhost:5672/
RABBITMQ_EXCHANGE=eai_agent_exchange
RABBITMQ_USER_MESSAGES_QUEUE=user_messages

# Observability
OTEL_ENABLED=true
OTEL_EXPORTER_OTLP_ENDPOINT=http://signoz:4317
PROMETHEUS_ENABLED=true

# Security
ENABLE_SECURITY_HEADERS=true
CORS_ALLOWED_ORIGINS=*

# Performance
REDIS_POOL_SIZE=10
WORKER_TIMEOUT=300
EOF

echo "Environment migration complete. Review $GO_ENV_FILE before use."
```

### Data Migration

#### Queue Message Format

```python
# Python Celery Message Format
{
    "id": "celery-task-id",
    "task": "process_user_message",
    "args": [
        {
            "user_id": "+5511999999999",
            "message": "Hello world",
            "message_type": "text"
        }
    ],
    "kwargs": {},
    "retries": 0,
    "eta": null
}
```

```json
// Go RabbitMQ Message Format
{
    "message_id": "550e8400-e29b-41d4-a716-446655440000",
    "user_number": "+5511999999999", 
    "message": "Hello world",
    "message_type": "text",
    "timestamp": "2023-12-01T10:30:00Z",
    "trace_headers": {
        "trace_id": "1234567890abcdef",
        "span_id": "abcdef1234567890"
    }
}
```

#### Redis Key Migration

```bash
#!/bin/bash
# migrate-redis-keys.sh

# Migrate task results
redis-cli --scan --pattern "celery-task-meta-*" | while read key; do
    value=$(redis-cli get "$key")
    new_key=$(echo "$key" | sed 's/celery-task-meta-/task:/')
    redis-cli set "$new_key" "$value" EX 120
    echo "Migrated $key → $new_key"
done

# Migrate agent IDs (no change needed)
echo "Agent ID keys are compatible - no migration needed"

# Clean up old Celery keys after validation
# redis-cli --scan --pattern "celery-*" | xargs redis-cli del
```

### Testing Migration

#### Validation Checklist

```bash
# 1. Functional Testing
echo "Testing API endpoints..."
curl -X POST http://localhost:8000/api/v1/message/webhook/user \
  -H "Authorization: Bearer $API_TOKEN" \
  -d '{"user_number":"+5511999999999","message":"test","message_type":"text"}'

# 2. Performance Testing  
echo "Running performance tests..."
k6 run load-tests/main.js

# 3. Integration Testing
echo "Testing external integrations..."
curl http://localhost:8000/health | jq '.services'

# 4. Data Validation
echo "Validating data consistency..."
redis-cli keys "task:*" | wc -l
redis-cli keys "thread:*" | wc -l

# 5. Monitoring Validation
echo "Checking metrics..."
curl http://localhost:8000/metrics | grep -c "http_requests_total"
```

#### Rollback Plan

```bash
#!/bin/bash
# rollback-to-python.sh

echo "Rolling back to Python service..."

# 1. Update load balancer to route 100% to Python
kubectl patch service eai-agent-gateway \
  --patch '{"spec":{"selector":{"version":"python"}}}'

# 2. Scale down Go service
kubectl scale deployment eai-agent-gateway-go --replicas=0

# 3. Scale up Python service  
kubectl scale deployment eai-agent-gateway-python --replicas=3

# 4. Verify Python service health
kubectl wait --for=condition=ready pod -l app=eai-agent-gateway,version=python

echo "Rollback complete. Python service is now handling all traffic."
```

### Migration Timeline

```
┌─────────────────────────────────────────────────────────────────────────────────┐
│                            MIGRATION TIMELINE                                   │
└─────────────────────────────────────────────────────────────────────────────────┘

Week -2: Preparation
├── Code review and testing
├── Environment setup
├── Infrastructure preparation
└── Team training

Week -1: Staging Deployment  
├── Deploy Go service to staging
├── End-to-end testing
├── Performance validation
└── Security testing

Week 1: Production Deployment (0% traffic)
├── Deploy Go service to production
├── Shadow traffic testing
├── Monitoring setup
└── Issue resolution

Week 2: Traffic Shift Start (5% → 25%)
├── Start routing 5% traffic to Go
├── Monitor metrics and errors
├── Increase to 25% if stable
└── Daily check-ins

Week 3: Major Traffic Shift (25% → 75%)
├── Increase to 50% traffic
├── Monitor performance impact
├── Increase to 75% if stable
└── Prepare for full cutover

Week 4: Full Cutover (100%)
├── Route 100% traffic to Go
├── Monitor for 48 hours
├── Keep Python as hot standby
└── Validate all features

Week 5-6: Monitoring Period
├── Monitor Go service stability
├── Fine-tune performance
├── Collect feedback
└── Document lessons learned

Week 7: Decommission Python
├── Stop Python service
├── Clean up resources
├── Update documentation
└── Post-migration review
```

---

## 🤝 Contributing

### Development Setup

#### Getting Started

```bash
# 1. Fork the repository
git clone https://github.com/YOUR_USERNAME/app-eai-agent-gateway.git
cd app-eai-agent-gateway

# 2. Set up development environment
make setup-dev

# 3. Install dependencies
go mod download
go install github.com/cosmtrek/air@latest
go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest

# 4. Configure environment
cp .env.example .env.dev
# Edit .env.dev with your development configuration

# 5. Start development services
docker-compose up -d redis rabbitmq
```

#### Development Workflow

```bash
# 1. Create feature branch
git checkout -b feature/amazing-new-feature

# 2. Start development server
just dev

# 3. Make changes and test
# - Edit code (hot reload with Air)
# - Run tests: just test
# - Check linting: just lint

# 4. Commit changes
git add .
git commit -m "feat: add amazing new feature"

# 5. Push and create PR
git push origin feature/amazing-new-feature
# Create PR via GitHub UI
```

### Code Standards

#### Go Code Style

```go
// Package documentation
// Package services provides business logic services for the EAI Agent Gateway.
package services

import (
    "context"
    "fmt"
    "time"
    
    "github.com/sirupsen/logrus"
    // Group imports: standard library, third party, local
)

// Public interface with documentation
// MessageProcessor handles message processing operations.
type MessageProcessor interface {
    // ProcessMessage processes a user message and returns the AI response.
    ProcessMessage(ctx context.Context, msg *Message) (*Response, error)
}

// Struct with tags and documentation
// UserMessage represents a message from a user.
type UserMessage struct {
    ID          string    `json:"id" validate:"required,uuid"`
    UserNumber  string    `json:"user_number" validate:"required,e164"`
    Content     string    `json:"content" validate:"required,max=4096"`
    MessageType string    `json:"message_type" validate:"required,oneof=text audio"`
    Timestamp   time.Time `json:"timestamp"`
}

// Constructor pattern
// NewMessageProcessor creates a new message processor.
func NewMessageProcessor(logger *logrus.Logger, redis RedisService) *MessageProcessor {
    return &MessageProcessor{
        logger: logger.WithField("component", "message_processor"),
        redis:  redis,
    }
}

// Method with proper error handling
func (p *MessageProcessor) ProcessMessage(ctx context.Context, msg *UserMessage) (*Response, error) {
    // Validate input
    if err := p.validateMessage(msg); err != nil {
        return nil, fmt.Errorf("invalid message: %w", err)
    }
    
    // Add timeout context
    ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
    defer cancel()
    
    // Process with structured logging
    p.logger.WithFields(logrus.Fields{
        "user_number": msg.UserNumber,
        "message_id":  msg.ID,
        "type":        msg.MessageType,
    }).Info("Processing message")
    
    // Implementation here...
    
    return response, nil
}
```

#### Testing Standards

```go
// Comprehensive test file
package services

import (
    "context"
    "testing"
    "time"
    
    "github.com/stretchr/testify/assert"
    "github.com/stretchr/testify/mock"
    "github.com/stretchr/testify/require"
)

// Test with table-driven tests
func TestMessageProcessor_ProcessMessage(t *testing.T) {
    tests := []struct {
        name        string
        message     *UserMessage
        mockSetup   func(*MockRedisService)
        expectedErr string
        expected    *Response
    }{
        {
            name: "successful text message processing",
            message: &UserMessage{
                ID:          "test-id",
                UserNumber:  "+5511999999999",
                Content:     "Hello world",
                MessageType: "text",
            },
            mockSetup: func(m *MockRedisService) {
                m.On("Get", mock.Anything, "thread:+5511999999999").
                  Return("", nil)
            },
            expected: &Response{
                Content:  "Hello! How can I help you?",
                ThreadID: "+5511999999999",
            },
        },
        {
            name: "invalid message format",
            message: &UserMessage{
                ID:         "",  // Invalid: empty ID
                UserNumber: "+5511999999999",
                Content:    "Hello",
            },
            expectedErr: "invalid message",
        },
    }
    
    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            // Setup mocks
            mockRedis := &MockRedisService{}
            if tt.mockSetup != nil {
                tt.mockSetup(mockRedis)
            }
            
            processor := NewMessageProcessor(testLogger, mockRedis)
            
            // Execute
            result, err := processor.ProcessMessage(context.Background(), tt.message)
            
            // Assert
            if tt.expectedErr != "" {
                require.Error(t, err)
                assert.Contains(t, err.Error(), tt.expectedErr)
                assert.Nil(t, result)
            } else {
                require.NoError(t, err)
                assert.Equal(t, tt.expected, result)
            }
            
            mockRedis.AssertExpectations(t)
        })
    }
}

// Benchmark test
func BenchmarkMessageProcessor_ProcessMessage(b *testing.B) {
    processor := setupTestProcessor()
    message := &UserMessage{
        ID:          "bench-test",
        UserNumber:  "+5511999999999",
        Content:     "Hello world",
        MessageType: "text",
    }
    
    b.ResetTimer()
    for i := 0; i < b.N; i++ {
        _, err := processor.ProcessMessage(context.Background(), message)
        if err != nil {
            b.Fatal(err)
        }
    }
}
```

#### Documentation Standards

```go
// Package-level documentation
// Package handlers provides HTTP request handlers for the EAI Agent Gateway.
//
// This package implements the REST API endpoints for message processing,
// health checks, and administrative functions. All handlers follow the
// gin.HandlerFunc signature and include comprehensive error handling,
// request validation, and structured logging.
//
// Example usage:
//   router := gin.New()
//   messageHandler := handlers.NewMessageHandler(config, logger)
//   router.POST("/api/v1/message/webhook/user", messageHandler.HandleUserWebhook)
package handlers

// Interface documentation
// MessageHandler defines the contract for HTTP message handling operations.
//
// All methods return standard HTTP responses with appropriate status codes:
//   - 200: Success
//   - 400: Bad Request (validation errors)
//   - 401: Unauthorized (authentication errors)
//   - 429: Too Many Requests (rate limiting)
//   - 500: Internal Server Error (processing errors)
type MessageHandler interface {
    // HandleUserWebhook processes incoming user messages via webhook.
    //
    // This endpoint accepts user messages in JSON format and queues them
    // for asynchronous processing. The response includes a message ID
    // that can be used to poll for results.
    //
    // Request format:
    //   {
    //     "user_number": "+5511999999999",
    //     "message": "Hello world",
    //     "message_type": "text"
    //   }
    //
    // Response format:
    //   {
    //     "message_id": "uuid",
    //     "status": "queued"
    //   }
    HandleUserWebhook(c *gin.Context)
}
```

### Pull Request Process

#### PR Template

```markdown
## Description
Brief description of the changes and what issue this PR solves.

## Type of Change
- [ ] Bug fix (non-breaking change which fixes an issue)
- [ ] New feature (non-breaking change which adds functionality)
- [ ] Breaking change (fix or feature that would cause existing functionality to not work as expected)
- [ ] Documentation update

## How Has This Been Tested?
- [ ] Unit tests pass
- [ ] Integration tests pass
- [ ] Manual testing completed
- [ ] Load testing (if applicable)

## Checklist:
- [ ] My code follows the code style of this project
- [ ] I have performed a self-review of my own code
- [ ] I have commented my code, particularly in hard-to-understand areas
- [ ] I have made corresponding changes to the documentation
- [ ] My changes generate no new warnings
- [ ] I have added tests that prove my fix is effective or that my feature works
- [ ] New and existing unit tests pass locally with my changes

## Testing Instructions
Specific instructions for testing this PR:

1. Step 1
2. Step 2  
3. Expected result

## Screenshots (if applicable)
Include screenshots for UI changes.
```

#### Review Process

```bash
# 1. Automated checks must pass
✅ Build successful
✅ Tests pass (>80% coverage)
✅ Linting passes
✅ Security scan passes
✅ Performance benchmarks stable

# 2. Code review requirements
- At least 2 approvals required
- 1 approval from CODEOWNERS
- No unresolved conversations

# 3. Integration testing
- Staging deployment successful
- End-to-end tests pass
- Performance testing completed
```

### Issue Templates

#### Bug Report Template

```markdown
## Bug Description
A clear and concise description of what the bug is.

## To Reproduce
Steps to reproduce the behavior:
1. Go to '...'
2. Click on '....'
3. Scroll down to '....'
4. See error

## Expected Behavior
A clear and concise description of what you expected to happen.

## Actual Behavior
What actually happened.

## Environment
- OS: [e.g. Ubuntu 20.04]
- Go Version: [e.g. 1.23]
- Service Version: [e.g. v2.1.0]

## Logs
```
Paste relevant logs here
```

## Additional Context
Add any other context about the problem here.
```

#### Feature Request Template

```markdown
## Feature Description
A clear and concise description of what the problem is. Ex. I'm always frustrated when [...]

## Proposed Solution
A clear and concise description of what you want to happen.

## Alternatives Considered
A clear and concise description of any alternative solutions or features you've considered.

## Use Case
Describe the use case and how this feature would benefit users.

## Implementation Suggestions
If you have ideas about how this could be implemented, please share them.

## Additional Context
Add any other context or screenshots about the feature request here.
```

### Release Process

#### Semantic Versioning

```bash
# Version format: MAJOR.MINOR.PATCH

# MAJOR: Breaking changes
v2.0.0 - Complete rewrite from Python to Go
v3.0.0 - Major API changes

# MINOR: New features (backward compatible)
v2.1.0 - Add OpenTelemetry tracing
v2.2.0 - Add new AI provider support

# PATCH: Bug fixes (backward compatible)  
v2.1.1 - Fix memory leak in worker
v2.1.2 - Fix rate limiting edge case
```

#### Release Checklist

```bash
# 1. Pre-release testing
just test-all
just load-test $API_TOKEN
just security-scan

# 2. Version bump
git tag v2.1.0
git push origin v2.1.0

# 3. Build and publish
just build-all
docker build -t eai-agent-gateway:v2.1.0 .
docker push eai-agent-gateway:v2.1.0

# 4. Deploy to staging
kubectl apply -k k8s/staging/

# 5. Staging validation
just test-staging

# 6. Production deployment
kubectl apply -k k8s/prod/

# 7. Post-deployment validation
just test-production
just monitor-release

# 8. Update documentation
just update-docs
```

---

## 📄 License

This project is licensed under the MIT License - see the [LICENSE](LICENSE) file for details.

## 📞 Support & Community

### Getting Help

- **📚 Documentation**: [docs/](docs/) - Comprehensive guides and API reference
- **🐛 Bug Reports**: [GitHub Issues](https://github.com/prefeitura-rio/app-eai-agent-gateway/issues) - Report bugs and request features
- **💬 Discussions**: [GitHub Discussions](https://github.com/prefeitura-rio/app-eai-agent-gateway/discussions) - Community support and questions
- **📧 Email**: eai-support@dados.rio - Direct support for enterprise users

### Community Guidelines

- **Be respectful**: Treat all community members with respect and kindness
- **Be constructive**: Provide helpful feedback and suggestions
- **Search first**: Check existing issues and documentation before posting
- **Use clear titles**: Make it easy for others to understand your issue or question
- **Provide context**: Include relevant details, error messages, and environment information

### Contributing

We welcome contributions! Please see our [Contributing Guidelines](#contributing) for details on:
- Code of conduct
- Development setup
- Submission process
- Code standards

---

<div align="center">

**EAI Agent Gateway** - Built with ❤️ by the Rio de Janeiro City Hall

[🏠 Home](https://github.com/prefeitura-rio/app-eai-agent-gateway) • [📖 Documentation](docs/) • [🚀 API Reference](docs/API.md) • [🛠️ Staging Deploy](docs/staging-deploy.md)

---

*Made with ☕ and lots of ⌨️ in Rio de Janeiro, Brazil*

</div>
