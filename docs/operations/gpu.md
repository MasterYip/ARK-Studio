# GPU servers

ARK can list configured GPU servers, probe free GPUs with `nvidia-smi`, rank
them least utilized first among GPUs that fit a job's memory claim (see *GPU dispatch rule* below), and prepare a bounded job dispatch
request for review. The capability is built into `ark-server` and is **opt-in
and read-only by default**. ARK delegates SSH authentication to your local SSH
agent/config. When the separate `queue.enabled` setting is enabled, the
persistent queue can launch submitted jobs and monitor them in the background.

Where it lives:

- `ark_studio/server/internal/gpu/` — server list parsing and validation, the
  SSH transport, `nvidia-smi` parsing, ranking, bounded dispatch requests and
  the opt-in queue; `ark_studio/server/internal/api/gpu/` serves the API.
- `ark_studio/web/js/views/settings.js` — **Settings → GPU**: status, configured servers,
  probe results and a dry-run dispatch form; **Execute → GPU** shows the queue.

Nothing besides `ark-server` and an OpenSSH client is needed.

## Enable and configure

1. Copy the safe example to your user-level private config location:

   ```bash
   mkdir -p ~/.ark
   cp docs/operations/gpu_servers.example.yaml ~/.ark/gpu_servers.yaml
   ```

   The config path resolves in this order:

   1. `gpu_servers_config` in the server's `--config` JSON, or `$ARK_GPU_SERVERS_CONFIG`
   2. `$ARK_PRIVATE_DIR/gpu_servers.yaml`
   3. `~/.ark/gpu_servers.yaml`

2. Edit the hosts/ports and set `enabled: true`. Alternatively start
   `ark-server` with `ARK_GPU_PROBE=1`.

3. Make sure the OpenSSH client is installed and your SSH agent or
   `~/.ssh/config` can reach the hosts (`ssh gpu-a.example.com true`). ARK uses
   `BatchMode=yes`, so a missing key fails fast instead of prompting. Set
   `ARK_GPU_SSH` if `ssh` is not on `PATH`.

No restart is needed after editing the server list: the file is re-read on each
request.

## Safe server-list schema

```yaml
enabled: false              # must be true (or ARK_GPU_PROBE=1) to probe
defaults:
  port: 22
  user: ubuntu
  timeout_seconds: 8        # per-server SSH timeout, 1–60
servers:
  - name: gpu-a              # letters, digits, dot, dash, underscore; unique
    host: gpu-a.example.com  # hostname or IP only
    port: 22                 # 1–65535
    user: ubuntu             # optional; defaults.user otherwise
    tags: [h200, primary]    # optional, up to 12 short tags
    note: "optional label"
    enabled: true            # optional; disabled servers are skipped
    timeout_seconds: 8       # optional per-server override
```

The parser accepts a small YAML subset (block maps/lists, quoted scalars,
booleans, inline lists). Anchors, aliases, tags, block scalars, flow maps and
tabs are rejected. Unknown fields are errors, and credential-looking fields
(`password`, `passphrase`, `secret`, `token`, `api_key`, `private_key`,
`identity_file`, …) are rejected with an actionable message: use your SSH agent
or `~/.ssh/config`. Only the fields above are ever returned by the API.

## Project scope and who sees what

The server list is one file for the whole ARK server, but **a project only
sees the GPU servers its setup registers** (ark-server only; T-0018). A server
is registered when a compute resource in the project's setup names it in
`gpu_server` (kind `ssh`), in the project layer or in any member's user layer,
in any state (draft, proposed or confirmed). Names match case-insensitively.
See [multi-user-setup.md](multi-user-setup.md) for recording resources.

| | Project registers the server | Project registers no server of the list |
|---|---|---|
| Resources graph | the server's device and GPUs | no GPU devices; unknown `gpu_not_registered` |
| `GET /api/gpu/servers`, `/status`, `/dashboard` | only registered servers (`server_count` counts them) | empty list plus `registration.hint_code: "not_registered"` |
| `POST /api/gpu/probe` | probes only registered servers | empty reply with the hint; nothing is probed |
| probe, dispatch or queue naming another server | `404 GPU_SERVER_NOT_REGISTERED` (also for names the list does not know) | same |
| queue submit / dispatch without a server | placed on (or defaulting to) registered servers only | `403 GPU_NO_REGISTERED_SERVERS` |
| `POST /api/agent/v1/setup/discover` | offers every configured server **name** (no address, tags or note), so a member can choose which to register | same |

The scoped replies carry a Go-only `registration` object: `registered`,
`available` (configured names this project does not register), `missing`
(registered names the list does not have), `suggested` (available names this
project's queue jobs already ran on) and, when nothing is registered,
`hint_code` and `hint`. The queue worker places a queued job only on servers
its project registers; a job of a project that registers none stays queued.

**Connection details** (`host`, `port`, `user`, `note`, `timeout_seconds`,
`defaults`, `config_path`, the queue `path_map`, `ssh_preview` and the
transport address of a dispatch request) are returned only to an organization
**owner or admin**, and in **local mode without accounts**. Every other viewer
(member, viewer, task agents acting for them) gets the server name, tags,
enabled flag, status and the probe metrics (GPU utilization and memory, system
load, storage). Their probe errors have the address replaced with `<host>`,
and config errors do not name the private path.

**Local mode follows the same rule.** The single local user is the admin (full
details), but a project still shows only the servers it registers: the list is
one file for every project on the machine, and a project that registers
nothing should not start jobs on a machine meant for another. The GPU board
and Settings → GPU offer a one-click **Use these servers in this project**
button (it saves compute resources for the suggested, else all available,
servers into the viewer's own setup layer through
`POST /api/skills/setup/draft`), so the change costs one click per project.

### Migration (existing installations)

Projects that used GPU servers before this change show none until a server is
registered. Nothing is written automatically: adding a resource to a confirmed
setup layer turns it back into a draft, which would undo people's
confirmations. Per project, one member with write access does one of:

1. Open **Execute → GPU** (or **Settings → GPU**) and click **Use … in this
   project**. The button lists the servers this project's queue jobs already
   used (`registration.suggested`), else every configured server; or
2. In **Setup → Resources**, add a *compute* resource of kind `ssh` with
   `gpu_server` set to the server name (repeat per server); or
3. From an agent: `POST /api/agent/v1/setup/resource` with
   `{"resource": {"id": "gpu-a", "kind": "compute", "details": {"kind": "ssh", "gpu_server": "gpu-a"}}}`.

Queued jobs of a project that registers none of its servers wait in the queue
until the registration exists; running jobs are not affected.

## API

Every `/api/gpu/*` route answers for the current project (read access; the
queue submit needs write) and applies the project scope above.

| Method | Path | Purpose |
|---|---|---|
| `GET` | `/api/gpu/status` | capability state, SSH availability, registered server count; config path for admins only; no hosts |
| `GET` | `/api/gpu/servers` | the project's registered servers (public fields for admins; name, tags, enabled otherwise) and `registration`; `enabled: false` + empty list when disabled |
| `POST` | `/api/gpu/probe` | explicit read-only probe of registered servers; body `{servers?, timeout_seconds?}` |
| `POST` | `/api/gpu/dispatch/prepare` | validate and render a bounded dry-run request for a registered server |

`POST /api/gpu/probe` returns one result per server. SSH/network failures are
data, not exceptions:

```json
{
  "server": "gpu-a",
  "ok": false,
  "gpus": [],
  "error": {"kind": "unreachable", "message": "Connection refused"},
  "duration_ms": 812
}
```

Failure kinds: `missing_ssh`, `timeout`, `unreachable`, `auth`,
`command_failed`, `ssh_error`, `runner_error`.

`POST /api/gpu/dispatch/prepare` always returns `"dry_run": true` and
`"executed": false`, with the validated request, an illustrative remote command
and (admins only) an illustrative `ssh_preview`. Requesting `"execute": true`
is rejected.

## Bounded dispatch request

- `server` must exist in the list (optional when exactly one is configured);
- `gpus` ≤ 8 unique indices in `0..15`;
- `timeout_minutes` in `1..1440`;
- `job`/`project` ≤ 64 chars of `A-Z a-z 0-9 . _ -`;
- `work_dir` absolute POSIX, no `..`, ≤ 240 chars;
- `command` 1–16384 chars, no NUL/control bytes except newline and tab;
- `env` ≤ 16 variables, names `[A-Za-z_][A-Za-z0-9_]*`, values ≤ 200 chars.

The request is not sent anywhere. Submit it to the queue (`POST /api/gpu/jobs`,
below) or hand it to your own scheduler.

## Queue Monitoring API

`GET /api/gpu/jobs` (account session) and
`GET /api/agent/v1/gpu/jobs` (personal agent token) expose the same project-scoped
list. Personal **read** scope is sufficient; send `X-ARK-Project: <project ID>`.
Task-session clients use `/api/task-agent/v1/gpu/jobs` and their existing
`X-ARK-Read-Token`; results are restricted to that task session and its project.
Query filters never change the caller's project or permission.

| Query | Meaning |
| --- | --- |
| `status=running,errored` | Any of the comma-separated states: `queued`, `starting`, `running`, `completed`, `errored`, `canceled`. |
| `name_prefix=rebuttal-ce22-` | Literal, case-sensitive job-name prefix; 1-64 letters, digits, dots, dashes or underscores. |
| `name=rebuttal-ce22-1` | Exact, case-sensitive job name. Returns all matching attempts, including canceled jobs and retries. |
| `since=2026-11-07T00:00:00Z` | Inclusive creation-time boundary, with an explicit timezone. Fractional seconds up to microsecond precision are accepted. URL-encode `+` in numeric timezone offsets. |
| `server=gpu-a` | Exact server name. |
| `ids=gpu_a,gpu_b` | Read a batch of 1-500 job IDs in one request. Inaccessible or missing IDs are omitted; callers can compare the returned IDs with their request. |
| `limit=100` | Page size, 1-500, default 100. Invalid or oversized values return 400. |
| `offset=0` | Matching rows to skip, 0-1000000, default 0. |

Filters combine with AND; multiple statuses combine with OR. Results sort by
`created_at DESC, id DESC`, including when timestamps tie. Each page returns:

```json
{
  "jobs": [],
  "total": 650,
  "count": 0,
  "limit": 500,
  "offset": 650,
  "has_more": false,
  "next_offset": null,
  "queue_enabled": true
}
```

`total` counts **all filtered, authorized matches**, not just the current page.
Continue with `offset=next_offset` until `has_more` is false. Count and rows use
one SQLite read snapshot. Pagination is a live view: inserts or status changes
between requests can shift offsets; monitoring clients should deduplicate IDs
when collecting multiple pages. Empty pages retain the matching `total`.
The default ordering does not change the dispatcher's FIFO scheduling order.

Job records retain `job` and add its `name` alias. They include `id`, `status`,
`exit_code`, `server`, `created_at`, `ended_at`, `error` and `log_tail`. A new
failed completion captures up to the last 20 lines (bounded to 8192 characters)
in the **same SSH status check** already performed by the worker, then persists
them in SQLite. Later list/get requests read only local data and open no SSH
connection. Historical failures without a cached tail retain their existing
error/exit-code summary; list requests do not backfill remote logs.

`GET /api/gpu/jobs/<id>/logs?tail=20` and the corresponding agent/task routes
accept 1-500 lines (default 200). These explicit log requests may open SSH to
the recorded server, return at most 64 KiB, and require access to the job's
project (and the exact task session for task tokens). A reachable remote log
returns `cached: false`; unavailable logs fall back to the stored tail with
`cached: true`. Clients do not need to know remote filesystem paths.

```sh
curl --get "$ARK_URL/api/agent/v1/gpu/jobs" \
  -H "Authorization: Bearer $ARK_AGENT_TOKEN" \
  -H "X-ARK-Project: $ARK_PROJECT" \
  --data-urlencode 'status=running,errored' \
  --data-urlencode 'name_prefix=rebuttal-ce22-' \
  --data-urlencode 'since=2026-11-07T00:00:00Z' \
  --data-urlencode 'limit=500' --data-urlencode 'offset=0'
```

## Security model

- **Opt-in**: `enabled: false` is the default in the schema and in the example;
  without a config, ARK reports the capability as unavailable/disabled and every
  other ARK feature keeps working.
- **No credentials**: there is no schema field for a password, key, token or
  identity file, and no code that reads one. Authentication is delegated to the
  local SSH agent/config.
- **Probe boundary**: GPU probing only runs read-only queries: `nvidia-smi`,
  the system metrics (`/proc/loadavg`, `nproc`, `free`, `top`) and, on the Go
  server, `df -PB1` for the server's storage (Resources graph).
  The separately enabled queue executes submitted commands and requires
  current project write permission; a monitoring read token cannot dispatch.
- **Explicit action**: job list/get read local state. Explicit log GET requests
  may fetch a remote log; a GPU probe requires `POST` (the UI button).
- **Private config**: the server list lives at a user-level path, outside the
  repository and outside project folders; `.gitignore` also blocks a stray
  `gpu_servers.yaml` inside a checkout.
- **Project scope**: a project sees, probes, dispatches to and queues on only
  the servers its setup registers, and only org owners/admins (or local mode)
  see addresses and the config path (see *Project scope* above).
- **Clean degradation**: an unreachable host, bad auth, timeout or missing
  `ssh` is returned as a per-server error; it never fails the ARK server or the
  current project.

## Tests

`go test ./internal/gpu/... ./internal/api/gpu/...` (in `ark_studio/server`)
covers the safe schema, credential rejection, `nvidia-smi` parsing, ranking,
mocked probes, missing-OpenSSH behavior, bounded dispatch validation,
disabled-by-default behavior, the queue's placement rule and the HTTP
handlers; `internal/gpu/scope_test.go`, `internal/resources/registered_test.go`
and `internal/server/routes_gpu_test.go` cover the project scope, the member
view, local mode, the 403/404 refusals and setup discovery. The GPU replay
registers every server it configures in the fixture project
(`replay_gpu.register_servers`); the remaining Python differences are listed in
`tests/python/contract/go_only.py` (`gpu-*` fixes). The contract suite (`tests/python/contract/`) replays the GPU routes
against a fake `ssh` (`tests/python/contract/fakessh`). No test contacts a
real GPU host or executes a remote command.

## Limitations

- The queue is a bare-metal process dispatcher, not a cluster scheduler;
  external schedulers and `screen` sessions are not managed by it.
- SSH probes run in parallel (up to 8) with a per-request timeout cap of 30 s.
  A list of 64 slow hosts can still take several rounds.
- Host key policy is `StrictHostKeyChecking=accept-new`; pin hosts in
  `~/.ssh/config` if you need stricter verification.
- The OpenSSH client is the only transport (`ARK_GPU_SSH` names another
  `ssh` binary).

## GPU dispatch rule (queue and probe list)

Each queued job declares a **GPU-memory claim** per GPU: `min_free_memory_mb`,
or the aliases `gpu_memory_mb` / `memory_mb`. The default is
`queue.min_free_memory_mb`. Jobs are dispatched oldest first. For each job:

1. **Filter.** Keep only GPUs whose *available* memory can hold the claim.
   Available memory is free memory from `nvidia-smi` minus memory claimed by
   live ARK jobs have claimed but not yet allocated, or were placed earlier in
   the same round. Per-process memory and inherited `ARK_GPU_JOB_ID` identify
   how much of each claim is already reflected in free memory, including child
   processes. Missing attribution keeps the full claim reserved. This also
   protects jobs whose shell is running while CUDA is still initializing.
   `max_utilization_pct` still applies. Colocation defaults to enabled;
   `max_jobs_per_gpu: 0` imposes no extra job-count cap. A positive count or
   `allow_colocation: false` explicitly limits sharing. Existing private
   configurations retain their explicit policy until updated.
2. **Rank.** Take the least-utilized GPUs first. Ties go to more available
   memory, then the lower index.
3. **Dispatch.** Reserve the claim on the chosen GPUs before the next job is
   considered. A multi-GPU job takes the least-utilized GPUs that fit on one
   server. Servers are compared by the busiest GPU the job would get.

**Project.** `POST /api/gpu/jobs` files the job under the session's currently
selected project, and job lists and cancel are project-scoped. The GPU
UI endpoints need a signed-in session. Agents use
`/api/agent/v1/gpu/jobs` with their personal bearer token and `X-ARK-Project`;
listing, job detail, `/logs` and `POST /<id>/cancel` use the same namespace.
Read scope permits inspection; submission and cancellation require write scope
and the account's current project write grant. No browser password or helper
login session is needed.

Task-session tokens use `/api/task-agent/v1/gpu/jobs`. Their writes require
`full_access` and a current project write grant; another task cannot cancel a
job it does not own. The ordinary project API still uses the browser's selected
project. An explicitly enabled queue resumes during ARK startup without a GUI
request. Queue status reports `worker_started` and any `worker_error`.

Server eligibility comes before the filter. A pinned job (`server`) only
considers that server, and a job with `tags` only considers servers whose tags
include *all* of them. Dispatch is oldest first, so a job whose eligible
servers are full waits even when other servers are idle. A tag no server
carries leaves the job `queued` forever without an error. `queue.path_map`
rewrites the command, env and setup for the chosen server.

The probe's ranked list in Settings → GPU uses the same order (`rank`). The
legacy `score` and the informational `occupancy_pct` are still reported but
do not affect dispatch.


## Task-owned usage (blue highlight in Execute → GPU)

Each probe also records which GPU processes belong to ARK jobs:

- `nvidia-smi --query-compute-apps` gives every GPU process and its memory.
  `[N/A]` memory is kept as unknown and counted in the tooltip.
- `/proc/<pid>/environ` gives the `ARK_GPU_JOB_ID` that the queue launcher
  exported. Child processes (DataLoader workers, torchrun ranks) inherit it.
- `nvidia-smi pmon -c 1 -s u` gives each process's compute (SM) share.

The blue **MEM** part is the memory of processes belonging to this project's
`starting`/`running` jobs. Task filters and task status never hide a live job.
The blue **UTIL** part is the sum of those processes' measured SM share. When
`pmon` is unavailable (some drivers, MIG, WSL), it is estimated as GPU
utilization × the jobs' share of *used* memory, and the bar is labelled
`UTIL~`. Processes owned by another user, or in another PID namespace,
cannot be attributed and count as general usage.
