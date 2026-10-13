#!/usr/bin/env bash
# One-off cleanup of the RBAC that repo-agent namespace bootstrap granted to the
# review-sandbox and issue-sandbox ServiceAccounts before #1406.
#
# Bootstrap used to add each tenant's sandbox ServiceAccounts to the shared
# review-sandbox / issue-sandbox ClusterRoleBindings, which gave any sandbox
# cluster-wide access to sandboxes and sandboxtasks. #1406 removed that code
# and the manifests, but deploy-to-kube does not prune, so clusters deployed
# before it still carry the grants.
#
# The script:
#   1. Deletes the review-sandbox / issue-sandbox ClusterRoleBindings and
#      ClusterRoles. This alone closes the cross-tenant access.
#   2. Deletes the leftover per-namespace RoleBindings and ServiceAccounts.
#      They are found by name across all namespaces, so a re-run after a
#      partial failure still finds everything that is left.
# Objects that don't match what bootstrap created are skipped and reported.
# So are ServiceAccounts annotated for Workload Identity or used by a running
# pod. The in-use check only sees running pods: a workload scaled to zero, a
# CronJob between runs, or a paused Sandbox that names the ServiceAccount
# would fail to start after it is deleted. Only the removed RepoWatch
# sandboxes used these ServiceAccounts.
#
# Runs against the current kubectl context and needs cluster-admin. Requires
# kubectl and jq; works with bash 3.2 (macOS) and later. Dry run by default;
# --apply asks for confirmation before deleting. The script stops at the first
# failed command; it is safe to re-run.
#
# Usage:
#   ./repo-agent/scripts/ops/cleanup-legacy-sandbox-rbac.sh          # show what would be deleted
#   ./repo-agent/scripts/ops/cleanup-legacy-sandbox-rbac.sh --apply  # delete

set -euo pipefail

APPLY=false
case "${1:-}" in
  --apply) APPLY=true ;;
  "") ;;
  *) echo "usage: $0 [--apply]" >&2; exit 2 ;;
esac

for tool in kubectl jq; do
  if ! command -v "$tool" >/dev/null 2>&1; then
    echo "error: $tool is required" >&2
    exit 1
  fi
done

NAMES=(review-sandbox issue-sandbox)
WI_ANNOTATION="iam.gke.io/gcp-service-account"
# name:serviceaccount:clusterrole
ROLEBINDINGS=(
  "review-sandbox-binding:review-sandbox:review-sandbox"
  "review-sandbox-configdir-binding:review-sandbox:configdir-controller"
  "issue-sandbox-binding:issue-sandbox:issue-sandbox"
  "issue-sandbox-configdir-binding:issue-sandbox:configdir-controller"
)

run() {
  if $APPLY; then
    echo "+ $*"
    "$@"
  else
    echo "[dry-run] $*"
  fi
}

context=$(kubectl config current-context)
echo "Context: $context"
if $APPLY; then
  read -r -p "Delete legacy sandbox RBAC in context \"$context\"? [y/N] " reply || reply=""
  case "$reply" in
    y|Y|yes|YES) ;;
    *) echo "Aborted."; exit 1 ;;
  esac
else
  echo "Dry run. Re-run with --apply to delete."
fi
echo

# 1. Cluster-scoped grants.
for name in "${NAMES[@]}"; do
  ours=true
  crb=$(kubectl get clusterrolebinding "$name" --ignore-not-found -o json)
  if [[ -n "$crb" ]]; then
    ref=$(jq -r '.roleRef.kind + "/" + .roleRef.name' <<<"$crb")
    if [[ "$ref" != "ClusterRole/$name" ]]; then
      echo "SKIP clusterrolebinding/$name: roleRef is $ref, not ClusterRole/$name"
      ours=false
    else
      run kubectl delete clusterrolebinding "$name"
    fi
  fi
  # Only delete the ClusterRole if its binding was ours or is already gone.
  if $ours && [[ -n "$(kubectl get clusterrole "$name" --ignore-not-found -o name)" ]]; then
    run kubectl delete clusterrole "$name"
  fi
done

# 2. Namespaced leftovers, found by name in every namespace.
# Newline-separated; deduplicated before use. A string rather than an array
# keeps this working under `set -u` on bash < 4.4.
namespaces=""
find_namespaces() { # <resource> <name>
  kubectl get "$1" -A --field-selector "metadata.name=$2" \
    -o jsonpath='{range .items[*]}{.metadata.namespace}{"\n"}{end}'
}
for entry in "${ROLEBINDINGS[@]}"; do
  namespaces+=$'\n'$(find_namespaces rolebindings "${entry%%:*}")
done
for sa in "${NAMES[@]}"; do
  namespaces+=$'\n'$(find_namespaces serviceaccounts "$sa")
done

while read -r ns <&3; do
  [[ -z "$ns" ]] && continue

  for entry in "${ROLEBINDINGS[@]}"; do
    IFS=: read -r rb sa role <<<"$entry"
    obj=$(kubectl get rolebinding -n "$ns" "$rb" --ignore-not-found -o json)
    [[ -z "$obj" ]] && continue
    if jq -e --arg ns "$ns" --arg sa "$sa" --arg role "$role" '
        .roleRef.kind == "ClusterRole" and .roleRef.name == $role
        and (.subjects | length) == 1
        and .subjects[0].kind == "ServiceAccount"
        and .subjects[0].name == $sa
        and .subjects[0].namespace == $ns' <<<"$obj" >/dev/null; then
      run kubectl delete rolebinding -n "$ns" "$rb"
    else
      echo "SKIP rolebinding $ns/$rb: does not match what bootstrap created"
    fi
  done

  for sa in "${NAMES[@]}"; do
    obj=$(kubectl get serviceaccount -n "$ns" "$sa" --ignore-not-found -o json)
    [[ -z "$obj" ]] && continue
    if jq -e --arg a "$WI_ANNOTATION" '(.metadata.annotations // {})[$a] != null' <<<"$obj" >/dev/null; then
      echo "SKIP serviceaccount $ns/$sa: has $WI_ANNOTATION; remove manually if unused"
      continue
    fi
    # Deleting a ServiceAccount invalidates its tokens and breaks pods using it.
    pods=$(kubectl get pods -n "$ns" -o json | jq -r --arg sa "$sa" \
      '[.items[] | select(.spec.serviceAccountName == $sa) | .metadata.name] | join(", ")')
    if [[ -n "$pods" ]]; then
      echo "SKIP serviceaccount $ns/$sa: in use by pods: $pods"
      continue
    fi
    run kubectl delete serviceaccount -n "$ns" "$sa"
  done
done 3< <(printf '%s\n' "$namespaces" | sort -u)

echo
echo "Done."
