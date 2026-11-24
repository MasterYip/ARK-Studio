#!/usr/bin/env bash
# On the standby: install the snapshot the primary just sent
# (docs/operations/standby.md).  Called by ark-standby-sync.sh over SSH.
set -euo pipefail
umask 077
here=$(cd -- "$(dirname -- "$0")" && pwd -P)
conf=${ARK_STANDBY_ENV:-$HOME/.config/ark-standby/ark-standby.env}
set -a; . "$conf"; set +a
mkdir -p -- "$STANDBY_BASE/state"
exec 9> "$STANDBY_BASE/state/apply.lock"
flock -w 900 9 || { echo 'apply: lock busy (daily snapshot running?)' >&2; exit 1; }
exec nice -n 10 ionice -c3 python3 "$here/ark_standby.py" apply
