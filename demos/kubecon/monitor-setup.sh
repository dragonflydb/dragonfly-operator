#!/usr/bin/env bash
# Stands up the LIVE monitoring stack for the booth: Prometheus Operator +
# Prometheus + a PodMonitor scraping every demo Dragonfly + Grafana with the
# Dragonfly dashboard ALREADY imported and the datasource ALREADY wired.
#
# Run this the night before (it pulls a few images the first time — after that
# they're cached in the kind cluster). At the booth you just run ./monitor-open.sh.
#
# Result: open Grafana, pick a Dragonfly instance, and watch ops/sec spike during
# ./bench.sh and the master move during ./failover.sh — no clicking required.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
MON_DIR="$SCRIPT_DIR/monitoring"
DASHBOARD_SRC="$SCRIPT_DIR/../../monitoring/grafana-dashboard.json"
NS="${NS:-default}"
# Always points at the newest prometheus-operator release's bundle asset.
BUNDLE_URL="${BUNDLE_URL:-https://github.com/prometheus-operator/prometheus-operator/releases/latest/download/bundle.yaml}"

say() { printf '\n\033[1;36m==> %s\033[0m\n' "$*"; }

command -v kubectl >/dev/null || { echo "kubectl not found."; exit 1; }
command -v helm >/dev/null    || { echo "helm not found. Install: https://helm.sh/docs/intro/install/"; exit 1; }
command -v python3 >/dev/null || { echo "python3 not found (needed to template the dashboard)."; exit 1; }
[ -f "$DASHBOARD_SRC" ] || { echo "Dashboard not found at $DASHBOARD_SRC"; exit 1; }

say "Installing the Prometheus Operator (CRDs + operator)..."
# server-side apply avoids the 'annotation too long' error on the big CRDs.
kubectl apply --server-side --force-conflicts -f "$BUNDLE_URL"

say "Waiting for the PodMonitor/Prometheus CRDs to register..."
kubectl wait --for=condition=Established --timeout=120s \
  crd/podmonitors.monitoring.coreos.com \
  crd/prometheuses.monitoring.coreos.com
kubectl -n "$NS" rollout status deploy/prometheus-operator --timeout=120s || true

say "Creating Prometheus (+RBAC, +Service) and the demo PodMonitor..."
kubectl apply -f "$MON_DIR/prometheus.yaml"
kubectl apply -f "$MON_DIR/podMonitor.yaml"

say "Generating a zero-click Grafana dashboard ConfigMap..."
# Pin the dashboard's datasource to our provisioned uid 'prometheus' and drop the
# manual-import scaffolding so Grafana's sidecar provisions it without prompting.
TMP_DASH="$(mktemp)"
python3 - "$DASHBOARD_SRC" "$TMP_DASH" <<'PY'
import json, sys
src, out = sys.argv[1], sys.argv[2]
d = json.load(open(src))
for k in ("__inputs", "__requires", "__elements"):
    d.pop(k, None)
d["id"] = None                       # avoid id collisions on import
s = json.dumps(d)
s = s.replace("${DS_PROMETHEUS}", "prometheus")   # match the provisioned datasource uid
open(out, "w").write(s)
PY
kubectl -n "$NS" create configmap dragonfly-dashboard \
  --from-file=dragonfly-dashboard.json="$TMP_DASH" \
  --dry-run=client -o yaml | kubectl apply -f -
kubectl -n "$NS" label configmap dragonfly-dashboard grafana_dashboard=1 --overwrite
rm -f "$TMP_DASH"

say "Installing Grafana (datasource + dashboard auto-provisioned)..."
helm repo add grafana https://grafana.github.io/helm-charts >/dev/null 2>&1 || true
helm repo update >/dev/null 2>&1 || true
helm upgrade --install grafana grafana/grafana \
  -n "$NS" -f "$MON_DIR/grafana-values.yaml" --wait --timeout 5m

say "Done. Monitoring stack is up."
echo "Next: ./monitor-open.sh   (port-forwards Grafana + Prometheus and prints the login)"
