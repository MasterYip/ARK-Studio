#!/usr/bin/env bash
# On the standby: one verified full snapshot per day, DAILY_KEEP kept
# (docs/operations/standby.md).  Runs from ark-standby-daily.timer.
set -euo pipefail
umask 077
here=$(cd -- "$(dirname -- "$0")" && pwd -P)
conf=${ARK_STANDBY_ENV:-$HOME/.config/ark-standby/ark-standby.env}
set -a; . "$conf"; set +a
mkdir -p -- "$STANDBY_BASE/state"
exec 9> "$STANDBY_BASE/state/apply.lock"
flock -w 3600 9 || { echo 'daily: lock busy' >&2; exit 1; }
exec nice -n 19 ionice -c3 python3 "$here/ark_standby.py" daily
