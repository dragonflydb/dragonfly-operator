#!/bin/sh
# preStop hook: if this pod is the master and has an online replica, hand the
# master role over with REPLTAKEOVER before the kubelet sends SIGTERM, so the
# replica has applied every write this master acknowledged.
# Best effort: any failure exits 0 so pod termination is never blocked.
PORT="${HEALTHCHECK_PORT:-9999}"

INFO=$(redis-cli -p "$PORT" info replication) || exit 0
echo "$INFO" | grep -q '^role:master' || exit 0

IP=$(echo "$INFO" | sed -n 's/^slave0:ip=\([^,]*\),.*state=online.*/\1/p')
[ -n "$IP" ] || exit 0

redis-cli -h "$IP" -p "$PORT" REPLTAKEOVER 5 || exit 0
