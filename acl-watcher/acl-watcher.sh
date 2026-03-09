#!/bin/sh
set -eu

ACL_DIR="${ACL_DIR:-/var/lib/dragonfly}"
ACL_FILE="${ACL_FILE:-${ACL_DIR}/dragonfly.acl}"
REDIS_HOST="${REDIS_HOST:-127.0.0.1}"
REDIS_PORT="${REDIS_PORT:-9999}"

reload_acl() {
  if [ -f "${ACL_FILE}" ]; then
    redis-cli -h "${REDIS_HOST}" -p "${REDIS_PORT}" ACL LOAD >/dev/null 2>&1 || true
  fi
}

# Initial load
reload_acl

# Watch the directory for changes (secret updates are atomic swaps).
inotifywait -m -e create,modify,move,delete "${ACL_DIR}" | while read -r _; do
  reload_acl
done
