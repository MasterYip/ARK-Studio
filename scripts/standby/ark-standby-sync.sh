#!/usr/bin/env bash
# Replicate this ARK primary to its read-only standby (docs/operations/standby.md).
# Runs every few minutes from ark-standby-sync.timer (systemd user unit).
#   1. refuse when the standby reports that it was promoted (split-brain guard)
#   2. consistent SQLite snapshots (online backup API) of every ARK database
#   3. rsync of the snapshots, project files, private dir and configuration
#   4. the standby verifies the checksums and installs changed databases
set -euo pipefail
set -f  # exclude patterns stay literal
umask 077
here=$(cd -- "$(dirname -- "$0")" && pwd -P)
conf=${ARK_STANDBY_ENV:-$HOME/.config/ark-standby/ark-standby.env}
set -a; . "$conf"; set +a

log() { printf '%s %s\n' "$(date -u +%FT%TZ)" "$*"; }
die() { log "error: $*"; exit 1; }

mkdir -p -- "$SNAP_DIR"
exec 9> "$SNAP_DIR/.sync.lock"
flock -n 9 || { log 'previous sync still running'; exit 0; }

rsh="ssh -i $STANDBY_SSH_KEY -p $STANDBY_SSH_PORT -o BatchMode=yes -o ConnectTimeout=10 -o ServerAliveInterval=15 -o ServerAliveCountMax=4"
remote_rsync='nice -n 19 ionice -c3 rsync'
standby() { $rsh "$STANDBY_SSH" "$@"; }

role=$(standby "cat '$STANDBY_BASE/state/role' 2>/dev/null || echo standby") || die 'standby unreachable'
[[ $role == standby ]] || die "standby reports role '$role'; not syncing (see ark-failover.sh demote)"

python3 "$here/ark_standby.py" snapshot "$SNAP_DIR"

rsync_common=(-a --partial-dir=.rsync-partial --rsync-path="$remote_rsync" -e "$rsh")
[[ -n ${SYNC_BWLIMIT:-} ]] && rsync_common+=(--bwlimit="$SYNC_BWLIMIT")

# Database snapshots first; the standby installs them only after the files.
rsync "${rsync_common[@]}" --delete "$SNAP_DIR/db/" "$STANDBY_SSH:$STANDBY_BASE/incoming/db/"

# File trees at the same absolute paths.  Live databases are excluded by exact
# path (they travel as snapshots); excluded files are never deleted remotely.
for tree in $ARK_PROJECTS $ARK_PRIVATE ${ARK_EXTRA_DIRS:-}; do
  [[ -d $tree ]] || continue
  ex=$(mktemp)
  awk -F '\t' -v t="$tree" '$1 == t {print $2}' "$SNAP_DIR/db-excludes.tsv" > "$ex"
  args=("${rsync_common[@]}" --delete --exclude-from="$ex")
  for pat in ${SYNC_EXCLUDES:-}; do args+=(--exclude="$pat"); done
  rsync "${args[@]}" "$tree/" "$STANDBY_SSH:$tree/"
  rm -f -- "$ex"
done

standby "mkdir -p '$STANDBY_BASE/incoming/config'"
rsync "${rsync_common[@]}" "$ARK_CONFIG" "$STANDBY_SSH:$STANDBY_BASE/incoming/config/primary.json"

standby "bash '$STANDBY_TOOLS/ark-standby-apply.sh'"
log 'sync complete'
