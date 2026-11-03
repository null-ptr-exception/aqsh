#!/usr/bin/env bash
# One E2E entry point. Requires a disposable local Kind cluster.
set -euo pipefail
umask 077
cd "$(dirname "$0")/.."
usage() { echo "Usage: $0 kind-<cluster> [--coverage | --profile <test-profile>]" >&2; exit 2; }
[[ $# -ge 1 && $1 == kind-?* ]] || usage
kubectl() { command kubectl --request-timeout=30s "$@"; }
context=$1
shift
coverage=false
profiles=e2e
while [[ $# -gt 0 ]]; do
    case $1 in
        --coverage) coverage=true; shift ;;
        --profile)
            [[ $# -ge 2 && $2 =~ ^[a-zA-Z0-9][a-zA-Z0-9,-]*$ ]] || usage
            # Only profile names are accepted, never context/namespace overrides.
            [[ $2 != *remote* && $2 != *coverage* ]] || usage
            profiles="$profiles,$2"; shift 2 ;;
        *) usage ;;
    esac
done
# Native API/worker coverage requires a separate collection contract.
[[ $coverage == false || $profiles == e2e ]] || usage
[[ $coverage == false ]] || profiles="$profiles,coverage"
# Fixed test namespace: must match k8s/namespace.yaml and test overlay namespaces.
readonly namespace=aqsh
options=(--kube-context "$context" --namespace "$namespace" --profile "$profiles")
work=$(mktemp -d)
claimed=false
cleanup() {
    status=$?
    trap - EXIT
    if [[ $claimed == true ]]; then
        if [[ $status -ne 0 ]]; then
            kubectl --context "$context" --namespace "$namespace" get pods,jobs,deployments -o wide || true
            kubectl --context "$context" --namespace "$namespace" get events --sort-by=.lastTimestamp || true
            kubectl --context "$context" --namespace "$namespace" logs -l app --all-containers --prefix --tail=100 || true
        fi
        skaffold delete "${options[@]}" || { [[ $status -ne 0 ]] || status=1; }
        kubectl --context "$context" --namespace "$namespace" delete namespace "$namespace" --wait=false || { [[ $status -ne 0 ]] || status=1; }
        kubectl --context "$context" --namespace "$namespace" wait --for=delete namespace/"$namespace" --timeout=120s || { [[ $status -ne 0 ]] || status=1; }
    fi
    rm -rf "$work"
    exit "$status"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
# Obtain credentials from the local Kind cluster, not a similarly named remote
# context in the user's kubeconfig. verify has no --kube-context flag.
kind get kubeconfig --name "${context#kind-}" > "$work/kubeconfig"
export KUBECONFIG="$work/kubeconfig"
existing=$(kubectl --context "$context" --namespace "$namespace" get namespace "$namespace" --ignore-not-found -o name)
if [[ -n $existing ]]; then
    echo "Namespace $namespace already exists; use an empty disposable Kind cluster." >&2
    exit 1
fi
skaffold build "${options[@]}" --file-output "$work/artifacts.json"
# Atomically claim the namespace after building. A concurrent run must not be
# overwritten or cleaned up if it created the namespace since the initial check.
kubectl --context "$context" --namespace "$namespace" create namespace "$namespace"
claimed=true
skaffold deploy "${options[@]}" --build-artifacts "$work/artifacts.json" --load-images
result=0
skaffold verify --namespace "$namespace" --profile "$profiles" --build-artifacts "$work/artifacts.json" || result=$?
collect_coverage() {
    mkdir -p coverage || return
    local output pods pod dirs=""
    output=$(mktemp -d "$PWD/coverage/e2e.XXXXXX") || return
    pods=$(kubectl --context "$context" --namespace "$namespace" get pods -l app=aqsh -o jsonpath='{.items[*].metadata.name}') || return
    [[ -n $pods ]] || { echo "No application pods for coverage." >&2; return 1; }
    for pod in $pods; do
        mkdir "$output/$pod" || return
        kubectl --context "$context" --namespace "$namespace" exec "$pod" -- wget -q -O- --timeout=10 --tries=1 --post-data='' http://localhost:8080/debug/coverage/flush || return
        kubectl --context "$context" --namespace "$namespace" cp "$pod:/coverage/." "$output/$pod" || return
        compgen -G "$output/$pod/covmeta.*" >/dev/null && compgen -G "$output/$pod/covcounters.*" >/dev/null || {
            echo "Missing coverage metadata/counters in $output/$pod" >&2; return 1;
        }
        dirs="${dirs:+$dirs,}$output/$pod"
    done
    go tool covdata percent -i="$dirs" || return
    go tool covdata textfmt -i="$dirs" -o "$output/coverage.out" || return
    echo "Coverage: $output/coverage.out"
}
if [[ $coverage == true ]]; then
    collect_coverage || { [[ $result -ne 0 ]] || result=1; }
fi
exit "$result"
