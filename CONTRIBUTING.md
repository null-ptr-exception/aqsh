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

## Kubernetes Development and E2E (Skaffold)

CI runs the existing shell integration suite against a disposable Kind cluster
using Skaffold. Install Docker, Kind v0.31.0, kubectl v1.33.x and Skaffold v2.25.0
to reproduce it locally:

```bash
kind create cluster --name aqsh-e2e --wait 120s \
  --image kindest/node:v1.33.7@sha256:d26ef333bdb2cbe9862a0f7c3803ecc7b4303d8cea8e814b481b09949d353040
./test/e2e.sh kind-aqsh-e2e
kind delete cluster --name aqsh-e2e
```

`test/e2e.sh` requires an explicit context and an absent `aqsh` namespace. It builds
the application and test images, deploys Redis, aqsh and the mock values service,
then runs the test container as a Kubernetes Job with `skaffold verify`. A failing
test fails the command. The script reports diagnostics on failure and removes the
test resources on exit; it refuses to use an existing `aqsh` namespace.

Kustomize generates ConfigMaps from `tasks.yaml`, `tasks.d/test.yaml`, `tasks/`
and the existing mock values server, so Compose and Kubernetes use the same test
fixtures. Docker Compose remains available for local development.

For an interactive development cluster, pass the context and namespace explicitly:

```bash
skaffold dev --kube-context kind-aqsh-dev --namespace aqsh
skaffold delete --kube-context kind-aqsh-dev --namespace aqsh
```

## Running Tests

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
