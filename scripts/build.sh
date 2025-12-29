#!/bin/bash
set -euo pipefail

cd "$(dirname "$0")/.."

# Determine container runtime (prefer docker, fall back to podman)
if command -v docker &>/dev/null; then
    RUNTIME=docker
elif command -v podman &>/dev/null; then
    RUNTIME=podman
else
    echo "Error: neither docker nor podman found"
    exit 1
fi

# Get version from git
VERSION=$(git describe --tags 2>/dev/null || echo "dev")

echo "Building aqsh with $RUNTIME..."
echo "Version: $VERSION"

# Build image
$RUNTIME build -t aqsh:latest .

# Tag with version
$RUNTIME tag aqsh:latest "rophy/aqsh:${VERSION}"

echo ""
echo "Built images:"
echo "  - aqsh:latest"
echo "  - rophy/aqsh:${VERSION}"
