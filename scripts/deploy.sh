#!/bin/bash
set -e

echo "Deploying version $VERSION to $ENVIRONMENT"

if [ "$DRY_RUN" = "true" ]; then
    echo "[DRY RUN] Would deploy $VERSION to $ENVIRONMENT"
    exit 0
fi

echo "Pulling image..."
sleep 1
echo "Scaling replicas..."
sleep 1
echo "Waiting for rollout..."
sleep 1
echo "Deployment complete!"
