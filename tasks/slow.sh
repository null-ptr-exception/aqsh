#!/bin/bash
set -e

echo "Starting slow task..."
for i in 1 2 3 4 5; do
    echo "Step $i of 5"
    sleep 1
done
echo "Slow task completed!"
