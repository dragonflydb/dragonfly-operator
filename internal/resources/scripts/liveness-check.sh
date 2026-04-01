#!/bin/sh

# Liveness probe: checks that the Dragonfly process is alive and responding.
# Succeeds on any response from Dragonfly, including LOADING.
#
# LOADING is not a liveness failure — the process is healthy but busy restoring
# a dataset. Restarting during LOADING would abort the restore and create a
# crash loop on large datasets (see issues #426, #508).
#
# Traffic gating during LOADING is handled exclusively by the readiness probe,
# which only succeeds once the dataset is fully loaded.

HOST="localhost"
PORT=9999  # Dragonfly admin port — always plain-text, not user-configurable

# Use DFLY_requirepass if set (injected from spec.authentication.passwordFromSecret)
RESPONSE=$(redis-cli -h "$HOST" -p "$PORT" --no-auth-warning \
  ${DFLY_requirepass:+-a "$DFLY_requirepass"} PING 2>/dev/null)

# Succeed if Dragonfly responds at all (PONG = ready, LOADING = alive but restoring)
case "$RESPONSE" in
  PONG|*LOADING*) exit 0 ;;
  *)              exit 1 ;;
esac
