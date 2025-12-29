#!/bin/bash
# Run integration tests with coverage collection
#
# Usage: ./scripts/run-integration-test.sh

set -euo pipefail

PROJECT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
COVERAGE_DIR="${PROJECT_DIR}/coverage"
NAMESPACE="aqsh"

cd "$PROJECT_DIR"

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

info() { echo -e "${YELLOW}==>${NC} $1"; }
success() { echo -e "${GREEN}==>${NC} $1"; }
error() { echo -e "${RED}==>${NC} $1"; }

cleanup() {
    info "Cleaning up..."
    skaffold delete -p coverage 2>/dev/null || true
}
trap cleanup EXIT

# Step 1: Build and deploy
info "Building and deploying with coverage instrumentation..."
skaffold build -q -p coverage | skaffold deploy -p coverage --build-artifacts -

info "Waiting for deployment..."
kubectl wait --for=condition=available deployment/aqsh -n "$NAMESPACE" --timeout=120s

# Step 2: Run integration tests
info "Running integration tests..."
TEST_EXIT=0
skaffold build -q -p coverage | skaffold verify -p coverage --build-artifacts - || TEST_EXIT=$?

# Step 3: Collect coverage
info "Collecting coverage data..."
rm -rf "$COVERAGE_DIR"
mkdir -p "$COVERAGE_DIR/integration"

for pod in $(kubectl get pods -n "$NAMESPACE" -l app=aqsh -o jsonpath='{.items[*].metadata.name}'); do
    echo "  Flushing and collecting from $pod..."
    kubectl exec -n "$NAMESPACE" "$pod" -- \
        wget -q -O- --post-data='' http://localhost:8080/debug/coverage/flush || true
    kubectl cp "$NAMESPACE/$pod:/coverage" "$COVERAGE_DIR/integration/" 2>/dev/null || true
done

if [ -n "$(ls -A "$COVERAGE_DIR/integration" 2>/dev/null)" ]; then
    echo ""
    echo "=== Integration Test Coverage ==="
    go tool covdata percent -i="$COVERAGE_DIR/integration"
    echo ""
fi

# Report results
if [ $TEST_EXIT -eq 0 ]; then
    success "Integration tests passed!"
else
    error "Integration tests failed (exit code: $TEST_EXIT)"
fi

exit $TEST_EXIT
