#!/usr/bin/env bash
# Nukes the whole kind cluster. Run after the conference.
set -euo pipefail
CLUSTER_NAME="${CLUSTER_NAME:-kubecon-dragonfly}"
kind delete cluster --name "$CLUSTER_NAME"
echo "Deleted kind cluster '$CLUSTER_NAME'."
