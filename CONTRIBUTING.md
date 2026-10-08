# Contributing to aqsh

## Prerequisites

- Go 1.25+ (the pinned toolchain is Go 1.26.8)
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

MCP protocol and ownership tests use the official SDK with local HTTP servers. When `redis-server` is installed, `go test ./...` also runs a smoke test that builds the production binary and executes a real shell task against temporary Redis. No database or Kubernetes deployment is required. Run `go test -race ./...` for concurrency checks.

```bash
# Unit tests
go test ./...

# Integration tests (requires running services)
AQSH_URL=http://localhost:8080 ./test/integration_test.sh
```

## Releasing

Releases are driven by the `VERSION` file at the repo root. To release a new version:

1. Bump `VERSION` in a PR (e.g. `0.6.0` → `0.7.0`)
2. Merge to `main` — CI will automatically create the git tag, push the Docker image, and create a GitHub release

Git tags are protected and can only be created by CI.

## Key Dependencies

| Package | Purpose |
|---------|---------|
| `github.com/hibiken/asynq` | Task queue |
| `github.com/redis/go-redis/v9` | Redis client (for log streams) |
| `gopkg.in/yaml.v3` | YAML parsing |
