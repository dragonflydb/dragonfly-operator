#!/bin/sh

# Readiness: succeeds only when dataset loading is complete (loading:0).

HOST="localhost"
PORT=${HEALTHCHECK_PORT:-9999}  # injected by operator — admin port, no TLS, no auth

RESPONSE=$(redis-cli -h "$HOST" -p "$PORT" INFO persistence 2>/dev/null)

if echo "$RESPONSE" | grep -q "^loading:0"; then
  exit 0
fi
exit 1
