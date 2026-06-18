#!/usr/bin/env bash
# Opens the live dashboards at the booth: port-forwards Grafana (and Prometheus)
# and prints the login. Leave this running in its own terminal during the demo.
#
#   Grafana:    http://localhost:3000   (admin / kubecon)
#               -> the "Dragonfly Dashboard" is already there; pick an instance
#                  from the "Dragonfly" dropdown (df-single, df-ha, ...).
#   Prometheus: http://localhost:9090   (for ad-hoc queries / showing targets)
set -euo pipefail
NS="${NS:-default}"

say() { printf '\n\033[1;36m==> %s\033[0m\n' "$*"; }
cleanup() { kill 0 2>/dev/null || true; }
trap cleanup EXIT INT TERM

say "Port-forwarding Grafana    -> http://localhost:3000  (admin / kubecon)"
kubectl -n "$NS" port-forward svc/grafana 3000:80 >/dev/null 2>&1 &

say "Port-forwarding Prometheus -> http://localhost:9090"
kubectl -n "$NS" port-forward svc/prometheus-svc 9090:9090 >/dev/null 2>&1 &

cat <<'EOF'

  Open Grafana:    http://localhost:3000   (admin / kubecon)
    - Dashboards -> "Dragonfly Dashboard"
    - Set the "Dragonfly" dropdown to the instance you're demoing (e.g. df-ha)
    - Run ./bench.sh df-single  -> watch ops/sec & CPU spike
    - Run ./failover.sh df-ha   -> watch the master move (dragonfly_master panel)

  Prometheus:      http://localhost:9090   (Status -> Targets to show scraping)

  Press Ctrl-C to stop the port-forwards.
EOF

wait
