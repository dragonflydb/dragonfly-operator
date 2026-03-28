#!/bin/sh

HOST="localhost"
PORT=${HEALTHCHECK_PORT:-6379}

# Use DFLY_requirepass if set (injected from spec.authentication.passwordFromSecret)
redis-cli -h "$HOST" -p "$PORT" --no-auth-warning \
  ${DFLY_requirepass:+-a "$DFLY_requirepass"} PING 2>/dev/null | grep -q "PONG"
