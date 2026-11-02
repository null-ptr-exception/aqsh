#!/usr/bin/env bash
# Build, deploy and verify against a disposable Kubernetes cluster.
set -euo pipefail

if [[ $# -lt 1 ]]; then
    echo "Usage: $0 <kube-context> [skaffold options, e.g. --profile native-auth]" >&2
    exit 2
fi
context=$1
shift
namespace=aqsh
options=(--kube-context "$context" --namespace "$namespace" "$@")

# Never delete an existing development installation during test cleanup.
existing=$(kubectl --context "$context" --namespace "$namespace" get namespace "$namespace" --ignore-not-found -o name)
if [[ -n "$existing" ]]; then
    echo "Namespace $namespace already exists in $context; use an empty disposable cluster." >&2
    exit 1
fi

artifacts=$(mktemp)
scoped_config=$(mktemp)
cleanup() {
    status=$?
    trap - EXIT
    if [[ $status -ne 0 ]]; then
        kubectl --context "$context" --namespace "$namespace" get pods,jobs,deployments -o wide || true
        kubectl --context "$context" --namespace "$namespace" get events --sort-by=.lastTimestamp || true
        kubectl --context "$context" --namespace "$namespace" logs -l app --all-containers --prefix --tail=100 || true
    fi
    skaffold delete "${options[@]}" || status=1
    kubectl --context "$context" --namespace "$namespace" wait --for=delete namespace/"$namespace" --timeout=120s || status=1
    rm -f "$artifacts" "$scoped_config"
    exit "$status"
}
trap cleanup EXIT

# verify has no --kube-context flag. Give every stage a private kubeconfig
# containing only the requested context, without changing the user's default.
kubectl --context "$context" --namespace "$namespace" config view --minify --flatten --raw > "$scoped_config"
export KUBECONFIG="$scoped_config"

skaffold build "${options[@]}" --file-output "$artifacts"
skaffold deploy "${options[@]}" --build-artifacts "$artifacts" --load-images
skaffold verify --namespace "$namespace" "$@" --build-artifacts "$artifacts"
