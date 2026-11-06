# Contributing to aqsh

## Prerequisites

- Go toolchain from `go.mod`
- Docker / Docker Compose
- kubectl + Skaffold (for Kubernetes development)
- Kind and Go (for E2E and coverage)

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
and the Go toolchain from `go.mod`.

```bash
kind create cluster --name aqsh-e2e --wait 120s \
  --image kindest/node:v1.33.7@sha256:d26ef333bdb2cbe9862a0f7c3803ecc7b4303d8cea8e814b481b09949d353040
make test-e2e KUBE_CONTEXT=kind-aqsh-e2e
make test-e2e KUBE_CONTEXT=kind-aqsh-e2e E2E_ARGS=--coverage
kind delete cluster --name aqsh-e2e
```

Both Make invocations use `test/e2e.sh`, the only E2E entry point. Use a disposable,
**single-user local Kind** cluster with a `kind-<cluster>` context; concurrent runs
on the same cluster are unsupported. The runner obtains credentials from Kind
and leaves the user's kubeconfig unchanged. An existing `aqsh` namespace is
refused. The namespace is fixed and must match `k8s/namespace.yaml` and the overlays.
Context/namespace overrides are rejected.

The E2E overlay is the root `kustomization.yaml`, selected only by the `e2e`
profile. It generates ConfigMaps from the real `tasks.yaml` and `tasks/*.sh`,
and projects `tasks.d/test.yaml` into the config directory to exercise `include`.
Compose uses the same files. Mock server resources live under `test/fixtures/`.
Each run builds once, deploys, verifies and cleans up. Build/preflight failures
do not delete resources; verification and coverage failures remain nonzero.

`--coverage` adds instrumentation and flushes/copies coverage before cleanup, even
when verification fails. Each run creates a fresh `coverage/e2e.*/` directory and
prints its `coverage.out` path. Missing pods, failed flush/copy, missing binary
coverage data, or covdata conversion errors fail the command. Coverage is currently
supported for the baseline suite, not in combination with additional test profiles.

## Native TokenReview E2E

Both native modes use the same runner and the cluster's real TokenReview API:

```bash
make test-e2e KUBE_CONTEXT=kind-aqsh-e2e E2E_ARGS="--profile native-auth"
make test-e2e KUBE_CONTEXT=kind-aqsh-e2e E2E_ARGS="--profile native-auth,qualified-identity"
```

The profiles use separate reviewer/caller ServiceAccounts, projected tokens with
valid and wrong audiences, and a worker without reviewer credentials. The driver
checks forged headers, shared/qualified user and group ACLs, verified identity in
Redis, worker identity, execution results and SSE logs. Tokens for both another
service and the Kubernetes API audience must be rejected by aqsh. The caller has
no TokenReview or ServiceAccount-read permission. Both overlays include the namespace for
Skaffold deployment and cleanup. Coverage collection is baseline-only.

These tests exercise a real single-cluster API; kube-federated-auth and execution
ownership isolation are outside their scope. Ordinary `go test ./...` skips the
in-cluster driver unless `AQSH_NATIVE_E2E_URL` is set. The test image always passes
`-require-e2e`, so a missing URL fails instead of reporting a skipped success.

## Running Checks

```bash
go test ./...
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
