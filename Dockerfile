
# Multi-stage Docker build for EAI Agent Gateway (Go)
# Stage 1: Build stage
FROM golang:1.24-alpine AS builder

# Install build dependencies
RUN apk add --no-cache git ca-certificates tzdata

# Set working directory
WORKDIR /app

# Copy go mod files first for better caching
COPY go.mod go.sum ./

# Download dependencies
RUN go mod download && go mod verify

# Copy source code
COPY . .

# Install swag for Swagger documentation generation
RUN go install github.com/swaggo/swag/cmd/swag@latest

# Generate Swagger documentation
RUN /go/bin/swag init -g cmd/gateway/main.go -o docs --parseDependency --parseInternal

# Build both binaries with optimizations
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build \
    -ldflags='-w -s -extldflags "-static"' \
    -a -installsuffix cgo \
    -o gateway ./cmd/gateway && \
    CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build \
    -ldflags='-w -s -extldflags "-static"' \
    -a -installsuffix cgo \
    -o worker ./cmd/worker

# Stage 2: Runtime stage
FROM alpine:3.19

# Install runtime dependencies
RUN apk --no-cache add ca-certificates tzdata curl && \
    adduser -D -s /bin/sh appuser

# Create necessary directories
RUN mkdir -p /app/configs /app/logs /app/temp /app/docs && \
    chown -R appuser:appuser /app

# Copy timezone data and certificates
COPY --from=builder /usr/share/zoneinfo /usr/share/zoneinfo
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/

# Copy binaries from builder stage
COPY --from=builder /app/gateway /app/gateway
COPY --from=builder /app/worker /app/worker

# Copy generated Swagger documentation
COPY --from=builder /app/docs /app/docs

# Copy configuration templates
COPY configs/ /app/configs/

# Set proper permissions
RUN chmod +x /app/gateway /app/worker && \
    chown appuser:appuser /app/gateway /app/worker

# Switch to non-root user
USER appuser

# Set working directory
WORKDIR /app

# Expose port (configurable via environment)
EXPOSE 8000

# Set default environment variables
ENV GIN_MODE=release \
    LOG_LEVEL=info \
    SERVER_PORT=8000 \
    SERVER_HOST=0.0.0.0

# No default entrypoint - specify binary in K8s command
CMD ["echo", "Please specify either /app/gateway or /app/worker as command"]
