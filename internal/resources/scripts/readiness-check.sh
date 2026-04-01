#!/bin/sh

# Readiness probe: gates traffic until Dragonfly has fully loaded its dataset.
#
# Uses INFO persistence on the admin port (always plain-text, even when TLS is
# enabled on the main client port — --no_tls_on_admin_port is always set).
#
# Checks loading:0 rather than PING because the admin port is designed to always
# respond to commands (including PING) regardless of loading state, so a PING
# check would allow traffic to a pod that is still restoring a snapshot.
#
# Startup and liveness probes intentionally succeed during LOADING so the pod
# is not restarted mid-restore. This probe is the sole gate on traffic.

HOST="localhost"
PORT=9999  # Dragonfly admin port — always plain-text, not user-configurable

RESPONSE=$(redis-cli -h "$HOST" -p "$PORT" --no-auth-warning INFO persistence 2>/dev/null)

# Ready only when loading is complete
if echo "$RESPONSE" | grep -q "^loading:0"; then
  exit 0
fi
exit 1
