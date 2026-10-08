#!/bin/sh
set -eu

# TARGET is constrained by the example task schema so it is safe in this JSON.
printf '{"target":"%s","message":"example inspection finished"}\n' "$TARGET" > "$AQSH_RESULT_FILE"
echo "Example inspection finished"
