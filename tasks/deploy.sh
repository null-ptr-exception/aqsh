#!/bin/bash
set -e

echo "Deploying version $VERSION to $ENVIRONMENT"

if [ "$DRY_RUN" = "true" ]; then
    echo "[DRY RUN] Would deploy $VERSION to $ENVIRONMENT"
    # Write result for dry run
    cat > "$AQSH_RESULT_FILE" << EOF
{
  "status": "dry_run",
  "version": "$VERSION",
  "environment": "$ENVIRONMENT"
}
EOF
    exit 0
fi

echo "Pulling image..."
sleep 1
echo "Scaling replicas..."
sleep 1
echo "Waiting for rollout..."
sleep 1
echo "Deployment complete!"

# Write structured result
cat > "$AQSH_RESULT_FILE" << EOF
{
  "status": "deployed",
  "version": "$VERSION",
  "environment": "$ENVIRONMENT",
  "timestamp": "$(date -Iseconds)"
}
EOF
