# Build stage
FROM golang:1.24-alpine AS builder

WORKDIR /build

# Copy go mod files first for better caching
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY cmd/ cmd/
COPY internal/ internal/

# Build binary
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-w -s" -o aqsh ./cmd/aqsh

# Runtime stage
FROM alpine:3.20

RUN apk add --no-cache bash ca-certificates tzdata

WORKDIR /app

# Copy binary from builder
COPY --from=builder /build/aqsh /usr/local/bin/aqsh

# Create directories for hooks and scripts
RUN mkdir -p /etc/aqsh /scripts

# Default environment
ENV AQSH_MODE=both \
    AQSH_BIND=0.0.0.0:8080 \
    AQSH_HOOKS_CONFIG=/etc/aqsh/hooks.yaml \
    AQSH_SCRIPTS_DIR=/scripts \
    AQSH_REDIS_ADDR=redis:6379

EXPOSE 8080

ENTRYPOINT ["aqsh"]
