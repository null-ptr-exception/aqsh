#!/usr/bin/env bash
set -euo pipefail
# This task runs in a different Pod with no API reviewer credentials or config.
[[ ! -e /var/run/secrets/kubernetes.io/serviceaccount/token ]]
[[ ! -e /etc/aqsh/auth/auth.yaml ]]
[[ -z ${AQSH_AUTH_CONFIG:-} ]]
printf 'identity=%s\ngroups=%s\n' "$AQSH_IDENTITY" "$AQSH_GROUPS"
printf '{"identity":"%s","groups":"%s","reviewer_mounted":false}\n' \
    "$AQSH_IDENTITY" "$AQSH_GROUPS" > "$AQSH_RESULT_FILE"
