#!/usr/bin/env bash
# Sets up a local kind cluster and installs the Dragonfly operator.
# Run this the night before the conference (and once more in the morning) so all
# images are pre-pulled and you NEVER depend on conference WiFi at the booth.
set -euo pipefail

CLUSTER_NAME="${CLUSTER_NAME:-kubecon-dragonfly}"
OPERATOR_MANIFEST="${OPERATOR_MANIFEST:-https://raw.githubusercontent.com/dragonflydb/dragonfly-operator/main/manifests/dragonfly-operator.yaml}"
DF_IMAGE="docker.dragonflydb.io/dragonflydb/dragonfly:v1.39.0"

# Every image the booth demo touches. We side-load all of them into the cluster
# so NOTHING pulls over conference WiFi: Dragonfly itself, the redis-cli pod
# (connect.sh) and the memtier_benchmark pod (bench.sh).
DEMO_IMAGES=(
  "$DF_IMAGE"
  "redis:7.2"
  "redislabs/memtier_benchmark:latest"
)

say() { printf '\n\033[1;36m==> %s\033[0m\n' "$*"; }

command -v kind >/dev/null    || { echo "kind not found. Install: https://kind.sigs.k8s.io/"; exit 1; }
command -v kubectl >/dev/null || { echo "kubectl not found."; exit 1; }
command -v docker >/dev/null  || { echo "docker not found."; exit 1; }

if kind get clusters 2>/dev/null | grep -qx "$CLUSTER_NAME"; then
  say "kind cluster '$CLUSTER_NAME' already exists — reusing it."
else
  say "Creating kind cluster '$CLUSTER_NAME' (3 nodes so HA can spread)..."
  cat <<EOF | kind create cluster --name "$CLUSTER_NAME" --config -
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
nodes:
  - role: control-plane
  - role: worker
  - role: worker
EOF
fi

say "Pre-pulling demo images and loading them into the cluster (avoids WiFi at the booth)..."
for img in "${DEMO_IMAGES[@]}"; do
  echo "  - $img"
  docker pull "$img"
  kind load docker-image "$img" --name "$CLUSTER_NAME"
done

say "Installing metrics-server (so 'kubectl top pod' works — kind ships without it)..."
kubectl apply -f https://github.com/kubernetes-sigs/metrics-server/releases/latest/download/components.yaml
# kind's kubelet serving certs aren't signed by the cluster CA, so metrics-server
# needs --kubelet-insecure-tls. This is fine for a local demo cluster.
kubectl -n kube-system patch deployment metrics-server --type=json \
  -p='[{"op":"add","path":"/spec/template/spec/containers/0/args/-","value":"--kubelet-insecure-tls"}]'
kubectl -n kube-system rollout status deployment metrics-server --timeout=120s || \
  say "metrics-server not ready yet — 'kubectl top' may take a minute to report."

say "Installing the Dragonfly operator..."
kubectl apply -f "$OPERATOR_MANIFEST"

say "Waiting for the operator to be ready..."
kubectl rollout status deployment -n dragonfly-operator-system \
  -l control-plane=controller-manager --timeout=180s || \
  kubectl -n dragonfly-operator-system get deploy

say "Done. Operator is up in namespace 'dragonfly-operator-system'."
echo "Next: kubectl apply -f manifests/01-single-node.yaml  (see README.md for the full flow)"
