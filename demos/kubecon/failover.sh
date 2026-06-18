#!/usr/bin/env bash
# The crowd-pleaser: kill the master and watch the operator promote a replica.
# Assumes manifests/02-ha.yaml is applied (Dragonfly named "df-ha").
set -euo pipefail

DF="${1:-df-ha}"
NS="${NS:-default}"

say() { printf '\n\033[1;36m==> %s\033[0m\n' "$*"; }

say "Current roles (watch the 'role' column):"
kubectl -n "$NS" get pods -l "app=$DF" -L role -o wide

MASTER=$(kubectl -n "$NS" get pods -l "app=$DF,role=master" -o jsonpath='{.items[0].metadata.name}')
[ -n "$MASTER" ] || { echo "No master pod found for '$DF'. Is it applied and ready?"; exit 1; }

say "Killing the master pod: $MASTER"
kubectl -n "$NS" delete pod "$MASTER" --wait=false

say "Watching failover — a replica gets promoted to master (Ctrl-C when you've made the point)..."
kubectl -n "$NS" get pods -l "app=$DF" -L role -w
