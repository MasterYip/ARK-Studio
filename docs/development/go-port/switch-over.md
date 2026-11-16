# Running ark-server in place of Python: switch-over and rollback

The Python server (`python -m arkstudio serve`) is **deprecated** in favour of
`ark-server`, the Go server built from `ark_studio/server/`. Python stays in the repository,
fully working, as the reference implementation and as the rollback path. This
page describes how to run `ark-server` in place of Python, what data the two
share, and the exact procedure for switching a production host over and back.
Nothing here has been performed on a production host; it is the plan to review.

Placeholders: `<instance>` is the systemd instance name (`arkstudio@<instance>`),
`<checkout>` the ARK-Studio git checkout on the host, `<config>` the server JSON
config (`~/.config/arkstudio/<instance>.json`), `<projects>` the projects root,
`<private>` the private directory, `<port>` the bound port (default 8890).
Read the real values from the host's existing unit, config and
[server operations](../../operations/server-operations.md) notes; never copy
secrets into tickets or docs.

## What is the same

`ark-server serve` takes the same flags, defaults, environment fallbacks and
server JSON config as `python -m arkstudio serve` (all 21 config keys and 74
environment variables have a Go reader; see [config-parity.md](config-parity.md)).
Every route of the contract (253 endpoint records, 393 method+route pairs plus
13 dispatchers) answers byte for byte like Python under the differential replay,
the route probe and the CLI diff ([README](README.md)).

Data is shared unchanged; both servers can run against the same files (one at a
time, never both on one data directory):

| data | compatibility |
|---|---|
| project databases `<project>.ark/ark.sqlite3` | identical DDL, the same additive migrations in the same order (`ark_studio/server/internal/store/schema_gen.go` is generated from `ark_studio/python/arkstudio/db.py`), WAL journal mode, the same `meta` rows (schema_version, change_log_id, migration flags) |
| `project.json`, `activity.jsonl`, assets, `history.git/`, `sessions/` | same file formats and names; LF on Linux (CRLF only on Windows, as Python writes it) |
| `org.sqlite3` | same schema (org v3), DELETE journal mode kept as Python sets it |
| `.auth-secret`, `.auth-sessions.json`, `.agent-token` | read and written in Python's format; a remembered browser session survives a switch in both directions (`diffreplay.py --handoff`) |
| sealed personal credentials | same SecretBox format and key file in `<private>`; Go opens what Python sealed and vice versa |
| agent session folders (`meta.json`, logs, transcripts) and internal mirrors | same layout; compared file by file after every replay |
| GPU queue database and config | same schema and YAML subset |

After a full replay both servers' data directories are compared table by table
and file by file (`statediff.py`): 0 differences in every mode.

Differences that remain (all documented in the [README](README.md)):

* `Server` header `ARKStudio/2.0 Go`.
* The Go server runs the creator-assignee backfill at startup; Python on the
  first project request. The result is identical.
* `desktop` (pywebview window) is Python-only; the Tauri shell runs
  `ark-server serve`.
* SQLite WAL: under sustained heavy write load the WAL of a project grows
  (hundreds of MB at 1,500 users) and is truncated within seconds once writes
  stop. Python's WAL stays small. Watch disk space during the first days.
* modernc SQLite (pure Go) caps one project's writer at roughly 100 writes/s
  under a very heavy read load; writes queue instead of failing
  ([concurrency.md](concurrency.md)).

### Schema versions and rollback

Each server migrates a database to *its own* `SCHEMA_VERSION` when it opens it,
and every migration so far is additive (new tables, columns, triggers, meta
rows). Consequences:

* Go and Python built from the **same commit** have the same schema version, so
  either can open what the other wrote. Always roll back to the Python of the
  same commit as the deployed Go binary.
* An older Python (lower `SCHEMA_VERSION`) still runs on a newer database: it
  ignores columns it does not know (for example `topic_members.w`, v12) and
  rewrites `meta.schema_version` to its own number; the next newer server sees
  the column already present and does not add it again. Features that rely on
  the newer columns lose their stored values only if the older server rewrites
  those rows.

## Building

On a build machine (any OS; no C toolchain needed):

```sh
cd ark_studio/server
go generate ./...                                   # embed ark_studio/web/ into the binary
GOOS=linux GOARCH=amd64 go build -trimpath -o ark-server-linux-amd64 ./cmd/ark-server
sha256sum ark-server-linux-amd64 > ark-server-linux-amd64.sha256
go vet ./... && go test ./...
```

Build from the same commit that the host's `<checkout>` is at (record it with
`git rev-parse HEAD`), so Python and Go agree on the schema and on `ark_studio/web/`.
`ark-server version` prints the build version. `ark-server routes` lists the
contract ids the binary implements.

## systemd unit

The current Python unit is `deploy/arkstudio@.service`. The Go unit keeps the
same instance name scheme, working directory, config file and supervision
settings; only `ExecStart` and the Python environment line change:

```ini
# ~/.config/systemd/user/ark-server@.service
[Unit]
Description=ARK Studio, Go server (%i)
After=network-online.target
Wants=network-online.target
StartLimitIntervalSec=60
StartLimitBurst=5
Conflicts=arkstudio@%i.service

[Service]
Type=simple
WorkingDirectory=%h/ARK-Studio
ExecStart=%h/.local/bin/ark-server serve --config %h/.config/arkstudio/%i.json
Restart=on-failure
RestartSec=3
TimeoutStopSec=20
KillMode=control-group
UMask=0077

[Install]
WantedBy=default.target
```

If the Python unit has a drop-in override (`systemctl --user cat arkstudio@<instance>`
shows it, e.g. a different `WorkingDirectory`), copy the same override to
`ark-server@<instance>` (`systemctl --user edit ark-server@<instance>.service`).
`Conflicts=` keeps systemd from running both on the same data. Go-only tuning
flags (`--read-pool`, `--heavy-limit`, `--max-subprocesses`, `--max-in-flight`,
`--request-timeout`) are optional; the defaults were used for every load test.

## Switch-over procedure (production host)

Do this in a quiet hour; announce a 10-minute window (sign-ins survive, open
agent sessions and terminals are stopped as with any restart).

1. **Go/no-go gate on real data (off the host or on a copy):**
   copy the production projects root (a consistent copy, e.g. from the backup in
   step 3 or a stopped-service snapshot) to a scratch machine and run

   ```sh
   python -B tests/python/contract/diffreplay.py --go ./ark-server --prod-copy <copy-of-projects>
   ```

   It stages every `*.ark` project alone into two copies, lets Python and Go open
   it (startup migrations included), replays the read set (units, up to 2,000
   unit details and comment lists, graph, snapshot, timeline, activity, overview,
   settings, topics, JSON/Markdown export, request-parsing edges) and compares
   the replies and the resulting files. **Go only if every project reports
   `mismatches=0 state_diffs=0`.** Also run the full suite on the build commit:
   `diffreplay.py` local, `--auth`, `--owner`, `--nodes 2000`, `--fresh`,
   `--handoff`, `cli_diff.py`, `harness.py` against both servers, `go test ./...`.
2. **Install the binary:**
   ```sh
   install -m 0755 ark-server-linux-amd64 ~/.local/bin/ark-server.new
   sha256sum ~/.local/bin/ark-server.new        # must match the .sha256 from the build
   mv ~/.local/bin/ark-server.new ~/.local/bin/ark-server
   ~/.local/bin/ark-server version
   cp ark-server@.service ~/.config/systemd/user/ && systemctl --user daemon-reload
   ```
   Make sure `<checkout>` is at the build commit (`git -C <checkout> rev-parse HEAD`).
3. **Backup** (service stopped for consistency; see
   [server operations](../../operations/server-operations.md#backup-and-restore)):
   ```sh
   umask 077; mkdir -p ~/ark-backups; stamp=$(date -u +%Y%m%dT%H%M%SZ)
   systemctl --user stop arkstudio@<instance>.service
   tar -czf ~/ark-backups/pre-go-$stamp.tar.gz <projects> <org-db-dir> <private> <config>
   sha256sum ~/ark-backups/pre-go-$stamp.tar.gz
   ```
   Include the GPU queue database and the agent sessions root if they live
   elsewhere. Do not restart Python.
4. **Switch:**
   ```sh
   systemctl --user disable arkstudio@<instance>.service
   systemctl --user enable --now ark-server@<instance>.service
   systemctl --user status ark-server@<instance>.service
   journalctl --user -u ark-server@<instance>.service -n 100
   ```
5. **Smoke checks** (loopback on the host, then through the public URL):
   * `curl -s http://127.0.0.1:<port>/api/health` shows `"ok": true` and
     `"auth_required": true`; the `Server` header reads `ARKStudio/2.0 Go`.
   * `curl -sI http://127.0.0.1:<port>/js/graph.js` redirects to `/login`
     without a session (expected).
   * Sign in in a browser with an existing remembered session (should stay
     signed in) and with a fresh sign-in; open Home, the graph and a unit.
   * Write: add and resolve a comment on a scratch unit; move a node in a topic.
   * Agent sessions: Runs lists existing sessions; start and stop the echo
     preset session.
   * Editor: open a file; start a terminal, run `echo ok`, close it.
   * Live updates: a second browser tab sees the comment appear (SSE `/api/events`).
   * Agent API: `curl -H "Authorization: Bearer <agent token>" .../api/agent/v1/whoami`
     (any client of the API, e.g. the GPU-queue RPC client, keeps working).
6. **Monitor** for the first days: the journal (5xx, panics, `request timeout`),
   process RSS, the size of `ark.sqlite3-wal` files under `<projects>` (large
   WAL files should shrink to 0 shortly after busy periods), disk space, and
   response times. With `ARK_PPROF=127.0.0.1:6061` in the unit's environment,
   `curl -s http://127.0.0.1:6061/debug/vars` shows the `ark_sqlite` counters
   (writer queue wait, busy/locked counts, WAL checkpoints and peak WAL size); loopback only.

## Rollback

Python of the same commit reads everything the Go server wrote, so rollback
needs no data conversion:

```sh
systemctl --user disable --now ark-server@<instance>.service
systemctl --user enable --now arkstudio@<instance>.service
curl -s http://127.0.0.1:<port>/api/health
```

* Remembered sessions, personal tokens, sealed credentials, comments and edits
  made under Go carry over.
* Agent processes started by Go are stopped with the unit (as on any restart);
  their session records remain and can be relaunched from Python.
* If the Go build was newer than the checkout's Python (it should not be; see
  "Schema versions"), check out the matching commit before starting Python.
* Restore from the step 3 archive only if data are damaged (verify with
  `sqlite3 <db> 'PRAGMA integrity_check'`): stop the unit, move the current
  directories aside (keep them), extract the archive to the original absolute
  paths, start the Python unit.

Python prints a one-time deprecation warning at `serve` start; behaviour is
otherwise unchanged.
