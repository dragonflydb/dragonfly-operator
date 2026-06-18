#!/usr/bin/env bash
# Pops an interactive redis-cli connected to a Dragonfly instance — proves the
# "drop-in Redis replacement, no code changes" story.
#
# Usage:
#   ./connect.sh                 # connects to df-single
#   ./connect.sh df-ha           # connects to the HA master service
#   ./connect.sh df-auth PASS    # connects with a password (for the auth demo)
set -euo pipefail

DF="${1:-df-single}"
PASS="${2:-}"
NS="${NS:-default}"
HOST="${DF}.${NS}.svc.cluster.local"

AUTH_ARGS=()
[ -n "$PASS" ] && AUTH_ARGS=(-a "$PASS")

echo "Connecting redis-cli to $HOST ..."
echo "Try:  set hello world   /   get hello   /   info replication"
kubectl -n "$NS" run redis-cli-$RANDOM --rm -it --restart=Never \
  --image=redis:7.2 -- \
  redis-cli -h "$HOST" -p 6379 "${AUTH_ARGS[@]}"
