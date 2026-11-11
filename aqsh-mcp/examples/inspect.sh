#!/bin/sh
set -eu
printf 'Inspecting %s\n' "$TARGET"
printf '{"target":"%s","message":"example read-only task"}\n' "$TARGET" > "$AQSH_RESULT_FILE"
