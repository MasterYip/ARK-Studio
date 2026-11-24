#!/usr/bin/env bash
# Promote or demote the ARK standby (docs/operations/standby.md).  Run on the standby.
#
#   ark-failover.sh status
#   ark-failover.sh promote --approved     make this standby the writable server
#   ark-failover.sh demote                 copy the changes made here back to the
#                                          repaired primary and become a standby again
#
# Promotion is an owner decision: it needs --approved, and it refuses while the
# primary still answers.  Never run two writable servers on the same data.
set -euo pipefail
umask 077
here=$(cd -- "$(dirname -- "$0")" && pwd -P)
conf=${ARK_STANDBY_ENV:-$HOME/.config/ark-standby/ark-standby.env}
set -a; . "$conf"; set +a

unit=${STANDBY_UNIT:-ark-server@bkp.service}
state=$STANDBY_BASE/state
readonly_dropin=$HOME/.config/systemd/user/$unit.d/readonly.conf
log() { printf '%s %s\n' "$(date -u +%FT%TZ)" "$*"; }
die() { log "error: $*"; exit 1; }
role() { cat "$state/role" 2>/dev/null || echo standby; }
primary_ssh() {
  timeout "${PRIMARY_SSH_TIMEOUT:-60}" ssh -i "$PRIMARY_SSH_KEY" -p "$PRIMARY_SSH_PORT" -o BatchMode=yes \
    -o ConnectTimeout=8 "$PRIMARY_SSH" "$@"
}
local_health() {
  curl --noproxy '*' -fsS --max-time 5 "http://127.0.0.1:${STANDBY_PORT}/api/health"
}

# HTTP status of the primary's public health endpoint (000 = no answer).
primary_http() {
  curl --noproxy '*' -ks -o /dev/null -w '%{http_code}' --max-time 8 \
    --resolve "$PRIMARY_DIRECT_HOST:$PRIMARY_DIRECT_PORT:$PRIMARY_PUBLIC_IP" \
    "https://$PRIMARY_DIRECT_HOST:$PRIMARY_DIRECT_PORT/api/health" || true
}

set_read_only() {
  mkdir -p -- "$(dirname -- "$readonly_dropin")"
  printf '[Service]\nEnvironment=ARK_READ_ONLY=1\n' > "$readonly_dropin"
  systemctl --user daemon-reload
}

wait_healthy() {
  local i
  for i in $(seq 1 30); do
    local_health >/dev/null 2>&1 && return 0
    sleep 1
  done
  return 1
}

cmd_status() {
  echo "role:            $(role)"
  echo "unit:            $unit $(systemctl --user is-active "$unit" || true)"
  if [[ -f $state/status.json ]]; then
    python3 - "$state/status.json" <<'PY'
import json, sys, datetime as dt
s = json.load(open(sys.argv[1]))
t = dt.datetime.fromisoformat(s["last_sync"].replace("Z", "+00:00"))
age = (dt.datetime.now(dt.timezone.utc) - t).total_seconds()
print(f"last sync:       {s['last_sync']} ({age/60:.1f} min ago), {s['databases']} databases from {s['primary']}")
print(f"primary version: {s.get('primary_version') or '?'}")
PY
  else
    echo 'last sync:       never'
  fi
  echo "standby version: $("$HOME/.local/bin/ark-server" version 2>/dev/null || echo '?')"
  echo "primary https:   $(primary_http)"
  local health
  health=$(local_health 2>/dev/null || echo 'no answer')
  echo "local health:    $health"
}

# Decide whether the primary is really down.  Returns 0 when it is safe to
# promote; with a reachable primary host it also stops and disables the
# primary's server and sync so that a reboot cannot bring it back writable.
primary_is_down() {
  local code tries=0
  for tries in 1 2 3; do
    code=$(primary_http)
    if [[ $code != 000 && $code != 5* ]]; then
      log "primary answers HTTP $code on $PRIMARY_DIRECT_HOST"
      return 1
    fi
    [[ $tries == 3 ]] || sleep 20
  done
  if primary_ssh true 2>/dev/null; then
    if primary_ssh "curl --noproxy '*' -fsS --max-time 5 http://127.0.0.1:${PRIMARY_PORT}/api/health" >/dev/null 2>&1; then
      log 'primary host is reachable and its ARK answers on loopback; fix its proxy instead of promoting'
      return 1
    fi
    log 'primary host is reachable but ARK is down: stopping and disabling it there'
    primary_ssh "systemctl --user disable --now ark-standby-sync.timer $PRIMARY_UNIT" || return 1
  else
    log 'primary host does not answer SSH either; its server unit is fenced by ark-standby-fence.sh at boot'
  fi
  return 0
}

cmd_promote() {
  [[ ${1:-} == --approved ]] || die 'promotion needs the owner'"'"'s approval: ark-failover.sh promote --approved'
  [[ $(role) == standby ]] || die "role is already '$(role)'"
  [[ -f $state/status.json ]] || die 'no replica has been applied yet'
  primary_is_down || die 'the primary is not down; not promoting'
  exec 9> "$state/apply.lock"
  flock -w 900 9 || die 'apply lock busy'
  echo primary > "$state/role"
  rm -f -- "$readonly_dropin"
  systemctl --user daemon-reload
  systemctl --user enable "$unit" >/dev/null 2>&1 || true
  systemctl --user restart "$unit"
  wait_healthy || die "$unit did not become healthy; see journalctl --user -u $unit"
  if local_health | grep -q '"read_only"'; then die 'server still reports read_only'; fi
  log "promoted: $unit accepts writes"
  cat <<EOF

Next steps (clients):
  * Clients that store both servers: switch to ${STANDBY_PUBLIC_URL} in the server list.
  * To move the main name instead, point the DNS A record of $PRIMARY_DIRECT_HOST at
    $STANDBY_PUBLIC_IP (Cloudflare, DNS only, TTL 60) once this host has a certificate
    for it; the nginx site already lists both names.
  * Tell users that agent sessions and GPU jobs that ran on the primary are not running here.
When the primary is repaired: ark-failover.sh demote (on this host).
EOF
}

cmd_demote() {
  [[ $(role) == primary ]] || die "role is '$(role)'; nothing to demote"
  primary_ssh true || die 'primary host is not reachable over SSH'
  local stamp; stamp=$(date -u +%Y%m%dT%H%M%SZ)
  log 'stopping the primary server and sync (they must stay off until the copy is back)'
  primary_ssh "systemctl --user stop ark-standby-sync.timer $PRIMARY_UNIT"
  log 'freezing this server (read-only)'
  set_read_only
  systemctl --user restart "$unit"
  wait_healthy || die "$unit did not come back read-only"
  local out=$STANDBY_BASE/outgoing/$stamp
  python3 "$here/ark_standby.py" snapshot "$out"
  log 'backing up the primary databases before they are replaced'
  primary_ssh "python3 '$PRIMARY_TOOLS/ark_standby.py' backup '$PRIMARY_BACKUPS/pre-demote-$stamp'"
  local rsh="ssh -i $PRIMARY_SSH_KEY -p $PRIMARY_SSH_PORT -o BatchMode=yes -o ConnectTimeout=10"
  rsync -a -e "$rsh" --rsync-path='nice -n 19 ionice -c3 rsync' "$out/db/" "$PRIMARY_SSH:$PRIMARY_SNAP_DIR/restore-$stamp/"
  local tree ex
  for tree in $ARK_PROJECTS $ARK_PRIVATE ${ARK_EXTRA_DIRS:-}; do
    [[ -d $tree ]] || continue
    ex=$(mktemp)
    awk -F '\t' -v t="$tree" '$1 == t {print $2}' "$out/db-excludes.tsv" > "$ex"
    # No --delete: files created on the primary before it failed stay.
    rsync -a -e "$rsh" --rsync-path='nice -n 19 ionice -c3 rsync' --exclude-from="$ex" "$tree/" "$PRIMARY_SSH:$tree/"
    rm -f -- "$ex"
  done
  primary_ssh "python3 '$PRIMARY_TOOLS/ark_standby.py' restore '$PRIMARY_SNAP_DIR/restore-$stamp'"
  echo standby > "$state/role"
  primary_ssh "systemctl --user enable --now $PRIMARY_UNIT"
  sleep 3
  primary_ssh "curl --noproxy '*' -fsS --max-time 5 http://127.0.0.1:${PRIMARY_PORT}/api/health" ||
    die 'primary did not come up; this host stays read-only, fix the primary and start it by hand'
  primary_ssh "systemctl --user enable --now ark-standby-sync.timer"
  log 'demoted: the primary serves the copied data again; this host is a read-only standby'
  echo "Switch clients and DNS back to $PRIMARY_DIRECT_HOST."
}

case ${1:-status} in
  status) cmd_status ;;
  promote) shift; cmd_promote "$@" ;;
  demote) cmd_demote ;;
  *) sed -n '2,11p' "$0"; exit 2 ;;
esac
