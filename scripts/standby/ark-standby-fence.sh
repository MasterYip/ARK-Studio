#!/usr/bin/env bash
# ExecStartPre of the primary's ark-server unit (docs/operations/standby.md):
# refuse to start when the standby reports that it was promoted, so a primary
# that comes back after a failover does not accept writes next to it.
# Any other outcome (standby unreachable, no settings) lets the primary start.
conf=${ARK_STANDBY_ENV:-$HOME/.config/ark-standby/ark-standby.env}
[[ -r $conf ]] || exit 0
set -a; . "$conf"; set +a
role=$(timeout 15 ssh -i "$STANDBY_SSH_KEY" -p "$STANDBY_SSH_PORT" -o BatchMode=yes -o ConnectTimeout=8 \
  "$STANDBY_SSH" "cat '$STANDBY_BASE/state/role' 2>/dev/null" 2>/dev/null) || exit 0
if [[ $role == primary ]]; then
  echo "The standby ($STANDBY_SSH) was promoted and serves ARK now; not starting this server." >&2
  echo "Run 'ark-failover.sh demote' on the standby to copy its changes back first." >&2
  exit 1
fi
exit 0
