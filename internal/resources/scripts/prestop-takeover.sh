#!/bin/sh
# preStop hook: if this pod is the master and has an online replica, hand the
# master role over with REPLTAKEOVER before the kubelet sends SIGTERM, so the
# replica has applied every write this master acknowledged.
#
# $1 = REPLTAKEOVER timeout in seconds (server side)
# $2 = wall-clock limit in seconds for each redis-cli call
#
# Best effort: any failure or timeout exits 0, so pod termination is never
# blocked and the rest of the grace period is left for the normal shutdown.
TAKEOVER_TIMEOUT="${1:-5}"
CMD_TIMEOUT="${2:-10}"
PORT="${HEALTHCHECK_PORT:-9999}"

INFO=$(timeout "$CMD_TIMEOUT" redis-cli -p "$PORT" info replication) || exit 0
echo "$INFO" | grep -q '^role:master' || exit 0

# First online replica among slave0, slave1, ...
IP=$(echo "$INFO" | sed -n 's/^slave[0-9][0-9]*:ip=\([^,]*\),.*state=online.*/\1/p' | sed -n '1p')
[ -n "$IP" ] || exit 0

timeout "$CMD_TIMEOUT" redis-cli -h "$IP" -p "$PORT" REPLTAKEOVER "$TAKEOVER_TIMEOUT" || exit 0
