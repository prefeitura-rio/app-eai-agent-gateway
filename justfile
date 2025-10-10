# EAí Agent Gateway - Go + RabbitMQ
# Development commands for the Go implementation

# Run the application in development mode with hot reload (requires air)
dev:
    @echo "Generating Swagger documentation..."
    @if command -v swag >/dev/null 2>&1; then \
        swag init -g cmd/gateway/main.go -o docs --parseDependency --parseInternal; \
    else \
        echo "Installing swag..."; \
        go install github.com/swaggo/swag/cmd/swag@latest; \
        ~/go/bin/swag init -g cmd/gateway/main.go -o docs --parseDependency --parseInternal; \
    fi
    @echo "Starting development server with hot reload..."
    air

# Run the gateway without hot reload
dev-simple:
    CGO_ENABLED=0 go run ./cmd/gateway/

# Run the worker without hot reload
worker:
    CGO_ENABLED=0 go run ./cmd/worker/

# Build both binaries (CGO disabled for compatibility)
build:
    @echo "Building Go applications..."
    @mkdir -p bin
    @CGO_ENABLED=0 go build -o bin/gateway ./cmd/gateway/
    @CGO_ENABLED=0 go build -o bin/worker ./cmd/worker/
    @echo "Build complete: bin/gateway, bin/worker"

# Build only the gateway
build-gateway:
    @echo "Generating Swagger documentation..."
    @if command -v swag >/dev/null 2>&1; then \
        swag init -g cmd/gateway/main.go -o docs --parseDependency --parseInternal; \
    else \
        ~/go/bin/swag init -g cmd/gateway/main.go -o docs --parseDependency --parseInternal; \
    fi
    @echo "Building gateway..."
    @mkdir -p bin
    @CGO_ENABLED=0 go build -o bin/gateway ./cmd/gateway/
    @echo "Build complete: bin/gateway"

# Build only the worker
build-worker:
    @echo "Building worker..."
    @mkdir -p bin
    @CGO_ENABLED=0 go build -o bin/worker ./cmd/worker/
    @echo "Build complete: bin/worker"

# Build and run the gateway
run: build-gateway
    @echo "Running gateway..."
    @./bin/gateway

# Build and run the worker
run-worker: build-worker
    @echo "Running worker..."
    @./bin/worker

# Run both gateway and worker in background
run-all: build
    @echo "Starting gateway..."
    @./bin/gateway &
    @echo "Starting worker..."
    @./bin/worker &
    @echo "Both services started in background"

# Format and lint code
lint:
    @echo "Formatting Go code..."
    @go fmt ./...
    @echo "Running Go vet..."
    @go vet ./...
    @if command -v golangci-lint >/dev/null 2>&1; then \
        echo "Running golangci-lint..."; \
        golangci-lint run; \
    else \
        echo "golangci-lint not installed, install with: go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest"; \
    fi

# Fix formatting and imports
lint-fix:
    @echo "Formatting Go code..."
    @go fmt ./...
    @if command -v goimports >/dev/null 2>&1; then \
        echo "Running goimports..."; \
        goimports -w .; \
    else \
        echo "goimports not installed, install with: go install golang.org/x/tools/cmd/goimports@latest"; \
    fi

# Run tests (CGO disabled for Nix compatibility, short mode to skip OpenTelemetry tests)
test *args="":
    @echo "Running Go tests..."
    @CGO_ENABLED=0 go test -v -short ./... {{args}}

# Run tests with coverage (CGO disabled for Nix compatibility)
test-coverage:
    @echo "Running Go tests with coverage..."
    @CGO_ENABLED=0 go test -v -coverprofile=coverage.out ./...
    @go tool cover -html=coverage.out -o coverage.html
    @echo "Coverage report: coverage.html"

# Run tests with race detection (CGO disabled for Nix compatibility)  
test-race:
    @echo "Running Go tests with race detection..."
    @CGO_ENABLED=0 go test -race -v ./...

# Tidy dependencies
tidy:
    @echo "Tidying Go dependencies..."
    @go mod tidy

# Clean build artifacts
clean:
    @echo "Cleaning build artifacts..."
    @rm -rf bin/
    @rm -f coverage.out coverage.html

# Check application health (requires running server)
health:
    @echo "Checking application health..."
    @curl -f http://localhost:8000/health | jq . || echo "Health check failed"
    @echo ""
    @curl -f http://localhost:8000/ready | jq . || echo "Readiness check failed"  
    @echo ""
    @curl -f http://localhost:8000/live | jq . || echo "Liveness check failed"

# Setup development environment
setup:
    @echo "Setting up Go development environment..."
    @go mod download
    @echo "Installing development tools..."
    @go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest
    @go install golang.org/x/tools/cmd/goimports@latest
    @go install github.com/cosmtrek/air@latest
    @echo "Copying .env.example to .env..."
    @cp .env.example .env
    @echo "Setup complete! Edit .env with your configuration."
    @echo "Use 'just dev' for hot reload or 'just dev-simple' for simple run."

# Run all quality checks
check: lint test
    @echo "All quality checks completed"

# Show build information
info:
    @echo "Build Information:"
    @echo "  Go Version: `go version | awk '{print $3}'`"
    @echo "  Git Commit: `git rev-parse --short HEAD 2>/dev/null || echo 'unknown'`"
    @echo "  Build Time: `date -u +%Y-%m-%dT%H:%M:%SZ`"

# Build for multiple platforms
build-all: clean tidy
    @echo "Building for all platforms..."
    @mkdir -p bin
    @CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o bin/gateway-linux-amd64 ./cmd/gateway/
    @CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o bin/worker-linux-amd64 ./cmd/worker/
    @CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build -o bin/gateway-darwin-amd64 ./cmd/gateway/
    @CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build -o bin/worker-darwin-amd64 ./cmd/worker/
    @CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -o bin/gateway-darwin-arm64 ./cmd/gateway/
    @CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -o bin/worker-darwin-arm64 ./cmd/worker/
    @CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -o bin/gateway-windows-amd64.exe ./cmd/gateway/
    @CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -o bin/worker-windows-amd64.exe ./cmd/worker/
    @echo "All builds complete"

# Docker build - single image with both binaries
docker-build:
    @echo "Building Docker image with both binaries..."
    @docker build -t eai-gateway:dev .
    @echo "Docker image built: eai-gateway:dev"

# Docker run - gateway
docker-run-gateway:
    @echo "Running gateway Docker container..."
    @docker run --rm -p 8000:8000 --env-file .env eai-gateway:dev /app/gateway

# Docker run - worker
docker-run-worker:
    @echo "Running worker Docker container..."
    @docker run --rm --env-file .env eai-gateway:dev /app/worker

# Docker Compose - start all services (gateway, workers, redis, rabbitmq)
compose-up:
    @echo "Starting all services with Docker Compose..."
    @docker-compose up -d

# Docker Compose - start with monitoring (prometheus, grafana)
compose-up-monitoring:
    @echo "Starting all services with monitoring..."
    @docker-compose --profile monitoring up -d

# Docker Compose - stop all services
compose-down:
    @echo "Stopping all Docker Compose services..."
    @docker-compose down

# Docker Compose - view logs
compose-logs service="":
    @if [ -z "{{service}}" ]; then \
        echo "Showing logs for all services..."; \
        docker-compose logs -f; \
    else \
        echo "Showing logs for {{service}}..."; \
        docker-compose logs -f {{service}}; \
    fi

# Docker Compose - restart specific service
compose-restart service:
    @echo "Restarting {{service}}..."
    @docker-compose restart {{service}}

# Docker Compose - scale workers
compose-scale-workers count="3":
    @echo "Scaling workers to {{count}} instances..."
    @docker-compose up -d --scale worker={{count}}

# Docker Compose - rebuild and restart
compose-rebuild:
    @echo "Rebuilding and restarting services..."
    @docker-compose down
    @docker-compose build --no-cache
    @docker-compose up -d

# Generate Swagger documentation
swagger-generate:
    @echo "Generating Swagger documentation..."
    @if command -v swag >/dev/null 2>&1; then \
        swag init -g cmd/gateway/main.go -o docs --parseDependency --parseInternal; \
    else \
        echo "Installing swag..."; \
        go install github.com/swaggo/swag/cmd/swag@latest; \
        ~/go/bin/swag init -g cmd/gateway/main.go -o docs --parseDependency --parseInternal; \
    fi
    @echo "Swagger documentation generated in docs/"

# View Swagger documentation (opens browser)
swagger-ui:
    @echo "Swagger UI will be available at: http://localhost:8000/docs/"
    @echo "Make sure the gateway is running with 'just dev' or 'just run'"

# Install Swagger CLI tool
swagger-install:
    @echo "Installing Swagger CLI tool..."
    @go install github.com/swaggo/swag/cmd/swag@latest
    @echo "Swagger CLI installed"

# Clean Swagger documentation
swagger-clean:
    @echo "Cleaning Swagger documentation..."
    @rm -rf docs/
    @echo "Swagger documentation cleaned"

load-test token:
    BEARER_TOKEN={{token}} k6 run --out json=load-tests/results.json load-tests/main.js

plot-results:
    cd load-tests && python generate-charts.py results.json
