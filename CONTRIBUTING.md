# Contributing to aqsh

## Prerequisites

- Go toolchain from `go.mod`
- Docker / Docker Compose
- kubectl + Skaffold (for Kubernetes development)
- Kind, Python 3 and Go (for E2E and coverage)

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

# Stop services
docker compose down
```

## Kubernetes Development

`k8s/` is a complete example deployment, including its ConfigMaps. The default
Skaffold configuration builds and deploys only the example application; test tasks
and mock services are added only by the `e2e` profile.

```bash
skaffold dev --kube-context kind-aqsh-dev --namespace aqsh
# Or apply the complete example directly:
kubectl --context kind-aqsh-dev --namespace aqsh apply -k k8s
```

## E2E and Coverage

Kind/Skaffold is the supported E2E path and the path exercised in CI. Compose is
for local development. Install Kind v0.31.0, kubectl v1.33.x, Skaffold v2.25.0,
Python 3 and the Go toolchain from `go.mod`.

```bash
kind create cluster --name aqsh-e2e --wait 120s \
  --image kindest/node:v1.33.7@sha256:d26ef333bdb2cbe9862a0f7c3803ecc7b4303d8cea8e814b481b09949d353040
make test-e2e KUBE_CONTEXT=kind-aqsh-e2e
make test-e2e KUBE_CONTEXT=kind-aqsh-e2e E2E_ARGS=--coverage
kind delete cluster --name aqsh-e2e
```

Both Make invocations use `test/e2e.sh`, the only E2E entry point. The runner requires
a disposable **local Kind** cluster and a `kind-<cluster>` context. It obtains that
cluster's kubeconfig from Kind rather than trusting a similarly named remote
context, and leaves the user's kubeconfig unchanged. An existing `aqsh` namespace
is refused; after building, namespace creation atomically excludes concurrent runs. The runner
keeps that claim until resource cleanup finishes, then deletes the namespace last.
The namespace is fixed and must match `k8s/namespace.yaml` and all test overlays;
the harness contract tests check that agreement. Arbitrary Skaffold flags and
context/namespace overrides are rejected.

The `e2e` overlay under `test/fixtures/` adds the test task configuration and mock
values server. Compose uses the same fixtures. Each run builds once, deploys,
runs the shell suite as a Kubernetes Job, and cleans up its claimed resources.
Build/preflight failures do not delete existing resources; test failures retain
their nonzero status through coverage collection and cleanup.

`--coverage` adds instrumentation and flushes/copies coverage before cleanup, even
when verification fails. Each run creates a fresh `coverage/e2e.*/` directory and
prints its `coverage.out` path. Missing pods, failed flush/copy, missing binary
coverage data, or covdata conversion errors fail the command. Coverage is currently
supported for the baseline suite, not in combination with additional test profiles.

## Running Checks

```bash
go test ./...
python3 test/e2e_runner_test.py
```

The Python checks use fake CLIs for failure contracts and offline `kubectl kustomize`
for example/test separation and namespace consistency; they do not deploy a cluster.

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
