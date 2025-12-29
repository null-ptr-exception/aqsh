# Contributing to aqsh

## Prerequisites

- Go 1.21+
- Docker / Docker Compose
- kubectl + Skaffold (for Kubernetes development)

## Project Structure

```
aqsh/
├── cmd/aqsh/           # CLI entry point
├── internal/
│   ├── api/            # HTTP handlers
│   ├── config/         # Configuration loading
│   ├── tasks/          # Task config parsing & validation
│   ├── worker/         # Asynq task handler, shell execution
│   └── logs/           # Redis Streams log handling
├── k8s/                # Kubernetes manifests
├── tasks/              # Example task scripts
├── tasks.yaml          # Task configuration
├── Dockerfile          # Production image
├── Dockerfile.test     # Integration test image
└── skaffold.yaml       # Skaffold configuration
```

## Local Development (Docker Compose)

```bash
# Start all services
docker compose up --build

# Run integration tests
docker compose --profile dev run --rm dev ./test/integration_test.sh

# Stop services
docker compose down
```

## Kubernetes Development (Skaffold)

```bash
# Deploy to Kubernetes
skaffold run

# Deploy and run integration tests
skaffold build -q | skaffold verify --build-artifacts -

# Deploy with live reload (development mode)
skaffold dev

# Clean up
skaffold delete
```

The integration tests run as a Kubernetes Job using `skaffold verify`, which:
- Builds the test container (`Dockerfile.test`)
- Deploys it to the cluster
- Streams test output and reports pass/fail status

## Running Tests

```bash
# Unit tests
go test ./...

# Integration tests (requires running services)
AQSH_URL=http://localhost:8080 ./test/integration_test.sh
```

## Key Dependencies

| Package | Purpose |
|---------|---------|
| `github.com/hibiken/asynq` | Task queue |
| `github.com/redis/go-redis/v9` | Redis client (for log streams) |
| `gopkg.in/yaml.v3` | YAML parsing |
