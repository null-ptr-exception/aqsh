# Build stage
FROM golang:1.24-alpine AS builder

# Set DEBUG=true for coverage instrumentation
ARG DEBUG=false
ARG VERSION=dev

WORKDIR /build

# Copy go mod files first for better caching
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY cmd/ cmd/
COPY internal/ internal/

# Build binary (coverage instrumentation when DEBUG=true)
RUN if [ "$DEBUG" = "true" ]; then \
        CGO_ENABLED=0 GOOS=linux go build -cover -covermode=atomic -ldflags="-X main.Version=${VERSION}" -o aqsh ./cmd/aqsh; \
    else \
        CGO_ENABLED=0 GOOS=linux go build -ldflags="-w -s -X main.Version=${VERSION}" -o aqsh ./cmd/aqsh; \
    fi

# Runtime stage
FROM debian:bookworm-slim

ARG DEBUG=false

RUN apt-get update && apt-get install -y --no-install-recommends \
    bash ca-certificates tzdata wget \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app

# Copy binary from builder
COPY --from=builder /build/aqsh /usr/local/bin/aqsh

# Create directories for config and task scripts
RUN mkdir -p /etc/aqsh /tasks /coverage

# Default environment
ENV AQSH_MODE=both \
    AQSH_BIND=0.0.0.0:8080 \
    AQSH_TASKS_CONFIG=/etc/aqsh/tasks.yaml \
    AQSH_TASKS_DIR=/tasks \
    AQSH_REDIS_ADDR=redis:6379

# Bake DEBUG into image - sets GOCOVERDIR at runtime if true
ENV AQSH_DEBUG=$DEBUG

EXPOSE 8080

# Wrapper sets GOCOVERDIR only when AQSH_DEBUG=true
ENTRYPOINT ["sh", "-c", "[ \"$AQSH_DEBUG\" = true ] && export GOCOVERDIR=/coverage; exec aqsh \"$@\"", "--"]
