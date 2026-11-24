# Standby Server

A standby is a second machine that holds a read-only copy of an ARK server,
refreshed every five minutes, plus seven verified daily snapshots. It is the
off-machine backup of the primary, and it can take over when the primary
fails. The tools are in `scripts/standby/`.

| | Primary | Standby |
| --- | --- | --- |
| Serves | everyone | read-only copy, `ARK_READ_ONLY=1` |
| Writes | yes | refused with `503 STANDBY_READ_ONLY` (sign-in and sign-out still work) |
| Agent sessions, GPU queue, mobile tunnel, session nudges | run here | off |
| Timers | `ark-standby-sync.timer` (every 5 min) | `ark-standby-daily.timer` (04:30) |

## Read-Only Mode

`ARK_READ_ONLY=1` on `ark-server serve`:

- refuses every `POST`, `PATCH` and `DELETE` except `/login`, `/logout` and
  `/api/auth/logout` with `503`, error code `STANDBY_READ_ONLY` and
  `Retry-After: 300`;
- does not start the GPU queue, the mobile Cloudflare tunnel or the session
  nudger, so nothing is dispatched twice;
- shows a banner at the top of the web app with the time of the last sync;
- reports `"read_only": true` and, with `ARK_STANDBY_STATUS` naming the
  status file, `"standby": {"last_sync", "primary", "databases"}` in
  `/api/health`.

## What Is Replicated

`ark-standby-sync.sh` runs on the primary:

1. It asks the standby for its role. A standby that was promoted is never
   overwritten.
2. `ark_standby.py snapshot` copies every ARK SQLite database (project
   databases, the organization database, the GPU queue and any other SQLite
   file in the replicated folders) through the SQLite online backup API.
   Live database files are never copied directly. Unchanged databases are
   skipped.
3. rsync over SSH sends the snapshots, the project folders (assets,
   `history.git`, setup files, `.auth-secret`, remembered sessions), the private
   folder (tokens, keys, sealed credentials, assistant settings) and the server
   configuration. Caches, temporary files and `deployment-backups/` stay behind.
4. On the standby, `ark-standby-apply.sh` checks every changed snapshot against
   its SHA-256 from the manifest. It then copies each one into the live
   database through the backup API, and the running server picks the change
   up like a write by another process, so nobody is signed out. A changed
   configuration restarts the standby unit. The status file
   (`state/status.json`) records the sync time and a checksum per database.

Files and databases keep the same absolute paths on both machines. The
organization database stores absolute project paths, so the standby needs the
same folder layout. Symlinks don't work here, because ARK resolves them.

`ark-standby-daily.sh` keeps one snapshot per day in `daily/YYYY-MM-DD`: the
databases pass `PRAGMA integrity_check`, the file trees are hard links to the
replica (unchanged files take no space), and `manifest.json` lists the
checksums. Seven days are kept (`DAILY_KEEP`).

## Setup

Both machines need `rsync`, `python3`, the same `ark-server` build and the same
user account layout.

1. Install the tools on both machines: copy `scripts/standby/*` to
   `~/.local/share/ark-standby/`, and copy `ark-standby.env.example` to
   `~/.config/ark-standby/ark-standby.env` with the real paths and hosts. The
   same file works on both sides.
2. SSH keys, restricted to the internal addresses:
   - On the primary, generate `~/.ssh/ark_standby_ed25519` and add it to the
     standby's `authorized_keys` as `from="<primary internal IP>",restrict ssh-ed25519 …`.
   - On the standby, generate `~/.ssh/ark_primary_ed25519` and add it to the
     primary's `authorized_keys` the same way (used by `demote` and the
     failover checks).
3. On the standby, create the data folders at the primary's paths. Install
   `systemd/ark-server@.service`, `standby.conf` (from `standby.conf.example`)
   and `readonly.conf` as drop-ins of `ark-server@bkp.service`, plus
   `ark-standby-daily.{service,timer}`. Run `loginctl enable-linger` for the
   account so user units run without a login.
4. On the primary, install `ark-standby-sync.{service,timer}` and the fence
   drop-in (`fence.conf.example`) on the primary's server unit.
   Put `SNAP_DIR` (the primary's snapshot folder, the size of all databases) on
   a filesystem that keeps free space when the data disk fills up, so the
   replication still runs then. Folder patterns in `SYNC_EXCLUDES` (such as
   `deployment-backups/`) are neither copied nor scanned for databases.
5. Run the first sync by hand (`systemctl --user start ark-standby-sync.service`)
   and follow it with `journalctl --user -u ark-standby-sync -f`. Then start
   `ark-server@bkp`, compare project and node counts with the primary, and
   enable both timers.
6. HTTPS: nginx in front of the standby on its own name
   (`nginx-standby.conf.example`), with a certificate for the standby name and
   for the primary's name, so DNS can move without a certificate change.

## Checks

```sh
ark-failover.sh status                       # on the standby: role, sync age, versions
curl -fsS http://127.0.0.1:8891/api/health   # read_only: true, standby.last_sync
journalctl --user -u ark-standby-sync -n 50  # on the primary
ls "$STANDBY_BASE/daily"                     # seven days, newest today
```

A sync older than 15 minutes, an apply error or a daily integrity failure needs
attention. Deploy every new `ark-server` build to both machines. The status
shows both versions.

## Promotion

Promotion is the owner's decision. The script needs `--approved`, and it does
nothing while the primary still answers:

```sh
ark-failover.sh promote --approved
```

The script checks the primary's public health endpoint three times over 40
seconds. If the primary host still answers SSH and ARK there is down, it stops
and disables the primary's server and sync. If the host is unreachable, the
fence drop-in keeps the primary's server from starting after a reboot. The
script then marks the standby `primary`, removes `readonly.conf`, restarts the
server and prints the client steps.

Data written on the primary after the last sync (up to five minutes) is not on
the standby. Running agent sessions and GPU jobs stay on the primary: the
standby starts with the queue as it was replicated, and agents ARK launched
there need relaunching.

## Return to the Primary

When the primary is repaired, run this on the standby:

```sh
ark-failover.sh demote
```

It stops the primary's server and sync and makes the standby read-only, so
nothing changes during the copy. It then snapshots the standby, backs up the
primary's databases to `pre-demote-<time>`, copies the databases and files
back, starts the primary and the sync, and returns the standby to its standby
role. Then switch clients and DNS back.

## Clients

- **Desktop app:** Settings > Servers > **Connect to a server…** saves the standby's
  address next to the primary's. Both appear in the project switcher with an
  online mark, and a click opens either one.
- **Phone app:** More > **Switch server** keeps a list of servers, so add the standby's
  address there. Phone sign-in creates a device pairing, which a read-only
  standby refuses. Read the standby in the phone's browser, or sign in from the
  app after promotion.
- **Browser:** open the standby's address and sign in with the usual account.
- **Agents:** the agent API on the standby answers reads. Writes return
  `503 STANDBY_READ_ONLY`, so agents should wait for the primary rather than
  switch.

## Risks

- **Two writable servers on one data set:** the role file, the sync's role check,
  the fence on the primary's unit and the promotion checks guard against it.
  Never remove `readonly.conf` by hand.
- **Secrets:** the replica carries the private folder, including tokens, keys
  and sealed provider credentials. Protect the standby account and its
  `daily/` folder like the primary.
- **Sign-ins:** the standby keeps its own browser sign-ins in memory, so a
  standby restart signs those readers out.
- **Shared disks:** the dailies sit on the standby's disk. They protect against
  losing the primary, not against losing both machines.
