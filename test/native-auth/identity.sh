#!/usr/bin/env bash
set -euo pipefail
# This task runs in a different Pod with no API reviewer credentials or config.
[[ ! -e /var/run/secrets/kubernetes.io/serviceaccount/token ]]
[[ -z ${AQSH_AUTH_TOKEN_FILE:-} ]]
[[ -z ${AQSH_AUTH_URL:-} ]]
printf 'identity=%s\ngroups=%s\n' "$AQSH_IDENTITY" "$AQSH_GROUPS"
printf '{"identity":"%s","groups":"%s","reviewer_mounted":false}\n' \
    "$AQSH_IDENTITY" "$AQSH_GROUPS" > "$AQSH_RESULT_FILE"
