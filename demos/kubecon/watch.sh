#!/usr/bin/env bash
# Live terminal view for the booth — the cheapest, most authentic visual.
# Shows each Dragonfly pod with its role (master/replica) refreshing in place,
# so the audience literally watches a replica get promoted during ./failover.sh.
#
# Usage:
#   ./watch.sh                 # watch ALL demo Dragonfly pods
#   ./watch.sh df-ha           # watch just the df-ha instance
#   ./watch.sh df-ha split     # tmux split-screen: roles (top) + live CPU (bottom)
#
# Tip: run this in one terminal and ./failover.sh in another. Or just open k9s
# (press '0' for all namespaces) if you prefer a full TUI.
set -euo pipefail

DF="${1:-}"
MODE="${2:-}"
NS="${NS:-default}"

# Selector: one instance if named, otherwise every operator-managed Dragonfly pod.
if [ -n "$DF" ] && [ "$DF" != "all" ]; then
  SEL="app=$DF"
else
  SEL="app.kubernetes.io/name=dragonfly"
fi

# Split-screen mode needs tmux: roles on top, `kubectl top` on the bottom.
if [ "$MODE" = "split" ]; then
  command -v tmux >/dev/null || { echo "tmux not found — run without 'split', or install tmux."; exit 1; }
  SESSION="df-watch"
  tmux kill-session -t "$SESSION" 2>/dev/null || true
  tmux new-session -d -s "$SESSION" \
    "watch -n1 -t 'kubectl -n $NS get pods -l $SEL -L role -o wide'"
  tmux split-window -v -t "$SESSION" \
    "watch -n2 -t 'kubectl -n $NS top pod -l $SEL 2>/dev/null || echo waiting for metrics...'"
  tmux select-layout -t "$SESSION" even-vertical
  exec tmux attach -t "$SESSION"
fi

# Default: a single live table. The 'role' column is the star of the failover demo.
if command -v watch >/dev/null; then
  exec watch -n1 -t "kubectl -n $NS get pods -l $SEL -L role -o wide"
fi

# Fallback if `watch` isn't installed (e.g. minimal macOS): hand-rolled refresh loop.
echo "(no 'watch' command — refreshing every 1s; Ctrl-C to stop)"
while true; do
  clear
  date
  kubectl -n "$NS" get pods -l "$SEL" -L role -o wide
  sleep 1
done
