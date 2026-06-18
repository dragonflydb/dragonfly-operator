#!/usr/bin/env bash
# Throughput demo: hammer a Dragonfly instance with memtier_benchmark from an
# in-cluster pod and show the ops/sec. Pair with `kubectl top pod` to show one
# pod saturating all its cores.
#
# Usage: ./bench.sh [dragonfly-name]   (default: df-single)
set -euo pipefail

DF="${1:-df-single}"
NS="${NS:-default}"
HOST="${DF}.${NS}.svc.cluster.local"

# Tunables — bump THREADS/CLIENTS to push harder on a bigger instance.
THREADS="${THREADS:-4}"
CLIENTS="${CLIENTS:-50}"
REQUESTS="${REQUESTS:-100000}"
RATIO="${RATIO:-1:4}"   # SET:GET

say() { printf '\n\033[1;36m==> %s\033[0m\n' "$*"; }

say "Benchmarking $HOST (threads=$THREADS clients=$CLIENTS ratio=$RATIO)"
echo "Tip: in another terminal run:  watch kubectl top pod -l app=$DF"

kubectl -n "$NS" run memtier-$RANDOM --rm -it --restart=Never \
  --image=redislabs/memtier_benchmark:latest -- \
  memtier_benchmark \
    -s "$HOST" -p 6379 \
    --threads="$THREADS" --clients="$CLIENTS" \
    --requests="$REQUESTS" --ratio="$RATIO" \
    --data-size=64 --hide-histogram
