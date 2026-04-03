#!/bin/sh

# Liveness: succeeds if Dragonfly responds (including LOADING).

HOST="localhost"
PORT=${HEALTHCHECK_PORT:-9999}  # injected by operator — admin port, no TLS, no auth

RESPONSE=$(redis-cli -h "$HOST" -p "$PORT" PING 2>/dev/null)

case "$RESPONSE" in
  PONG|*LOADING*) exit 0 ;;
  *)              exit 1 ;;
esac
