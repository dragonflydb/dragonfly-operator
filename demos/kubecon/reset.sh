#!/usr/bin/env bash
# Wipes all demo Dragonfly instances (and their PVCs) so you can re-run the flow
# cleanly for the next person walking up. Leaves the cluster + operator intact.
set -euo pipefail
NS="${NS:-default}"

say() { printf '\n\033[1;36m==> %s\033[0m\n' "$*"; }

say "Deleting all demo Dragonfly instances..."
kubectl -n "$NS" delete dragonflies.dragonflydb.io -l demo=kubecon --ignore-not-found

say "Deleting leftover demo PVCs (snapshots)..."
kubectl -n "$NS" delete pvc -l app=df-persist --ignore-not-found
# The operator labels owned PVCs with the instance name; catch any stragglers.
kubectl -n "$NS" delete pvc -l demo=kubecon --ignore-not-found

say "Deleting the auth secret..."
kubectl -n "$NS" delete secret df-auth-secret --ignore-not-found

say "Clean. Re-apply a manifest from manifests/ to start again."
