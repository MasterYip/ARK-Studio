# GPU servers (optional adapter)

ARK can list configured GPU servers, probe free GPUs with `nvidia-smi`, rank
them least utilized first among GPUs that fit a job's memory claim (see *GPU dispatch rule* below), and prepare a bounded job dispatch
request for review. The capability is **opt-in and read-only by default**.
ARK delegates SSH authentication to your local SSH agent/config. When the
separate `queue.enabled` setting is enabled, the persistent queue can launch
submitted jobs and monitor them in the background.

The implementation lives in these components:

- `vendor/gpu_server_probe/` — a minimal, standard-library adapter that mirrors
  the internal `gpu_server_probe` tool (config, SSH transport, `nvidia-smi`
  parsing, bounded dispatch requests and the opt-in queue). See
  [`vendor/gpu_server_probe/NOTICE.md`](../../vendor/gpu_server_probe/NOTICE.md).
- `arkstudio/gpu_probe.py` — the ARK service that resolves the private config,
  enforces the enable gate, sanitizes public payloads and serves the API.
- `web/js/views/settings.js` — **Settings → GPU**: status, configured servers,
  probe results and a dry-run dispatch form.

## Enable and configure

1. Copy the safe example to your user-level private config location:

   ```bash
   mkdir -p ~/.ark
   cp vendor/gpu_server_probe/servers_rp.example.yaml ~/.ark/gpu_servers.yaml
   ```

   The config path resolves in this order:

   1. `$ARK_GPU_SERVERS_CONFIG`
   2. `$ARK_PRIVATE_DIR/gpu_servers.yaml`
   3. `~/.ark/gpu_servers.yaml`

2. Edit the hosts/ports and set `enabled: true`. Alternatively start ARK with
   `ARK_GPU_PROBE=1`.

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

## API

| Method | Path | Purpose |
|---|---|---|
| `GET` | `/api/gpu/status` | capability state, vendor/config paths, SSH availability; no hosts |
| `GET` | `/api/gpu/servers` | configured servers (public fields only); `enabled: false` + empty list when disabled |
| `POST` | `/api/gpu/probe` | explicit read-only probe; body `{servers?, timeout_seconds?}` |
| `POST` | `/api/gpu/dispatch/prepare` | validate and render a bounded dry-run request |

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
and an illustrative `ssh_preview`. Requesting `"execute": true` is rejected.

## Bounded dispatch request

- `server` must exist in the list (optional when exactly one is configured);
- `gpus` ≤ 8 unique indices in `0..15`;
- `timeout_minutes` in `1..1440`;
- `job`/`project` ≤ 64 chars of `A-Z a-z 0-9 . _ -`;
- `work_dir` absolute POSIX, no `..`, ≤ 240 chars;
- `command` 1–16384 chars, no NUL/control bytes except newline and tab;
- `env` ≤ 16 variables, names `[A-Za-z_][A-Za-z0-9_]*`, values ≤ 200 chars.

The request is not sent anywhere. Hand it to the `gpu_server_probe` CLI or your
scheduler yourself.

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

## CLI

From the repository root:

```bash
python -m vendor.gpu_server_probe status
python -m vendor.gpu_server_probe servers
python -m vendor.gpu_server_probe probe            # dry run: prints the plan
python -m vendor.gpu_server_probe probe --live     # read-only nvidia-smi
python -m vendor.gpu_server_probe dispatch --server gpu-a --gpus 0,1 \
  --command "python train.py --config config.yaml"
```

`probe` does not connect unless `--live` is passed. `dispatch` never executes.

## Security model

- **Opt-in**: `enabled: false` is the default in the schema and in the example;
  without a config, ARK reports the capability as unavailable/disabled and every
  other ARK feature keeps working.
- **No credentials**: there is no schema field for a password, key, token or
  identity file, and no code that reads one. Authentication is delegated to the
  local SSH agent/config.
- **Probe boundary**: GPU probing only runs read-only `nvidia-smi` queries.
  The separately enabled queue executes submitted commands and requires
  current project write permission; a monitoring read token cannot dispatch.
- **Explicit action**: job list/get read local state. Explicit log GET requests
  may fetch a remote log; a GPU probe requires `POST` (the UI button).
- **Private config**: the server list lives at a user-level path, outside the
  repository and outside project folders. A `.gitignore` rule also blocks a
  private `servers_rp.yaml` inside the vendor directory.
- **Clean degradation**: an unreachable host, bad auth, timeout or missing
  `ssh` is returned as a per-server error; it never fails the ARK server or the
  current project.

## Tests

`tests/python/gpu/test_gpu_probe.py` covers the safe schema, credential rejection,
`nvidia-smi` parsing, ranking, mocked probes (one success, one unreachable, one
auth failure, one disabled), missing-OpenSSH behavior, bounded dispatch
validation, disabled-by-default behavior, missing vendor/config, and the HTTP
endpoints. `tests/python/gpu/test_gpu_monitoring.py` also covers more than 500 jobs,
filtered totals, tied timestamps, attempt lookup, batch IDs, timezone boundaries,
cached failures and HTTP authorization. Tests use local HTTP and mocked SSH;
none contacts an external GPU host or executes a remote command.

## Limitations

- The queue is a bare-metal process dispatcher, not a cluster scheduler;
  external schedulers and `screen` sessions are not managed by it.
- SSH probes run in parallel (up to 8) with a per-request timeout cap of 30 s.
  A list of 64 slow hosts can still take several rounds.
- Host key policy is `StrictHostKeyChecking=accept-new`; pin hosts in
  `~/.ssh/config` if you need stricter verification.
- `asyncssh` is detected for diagnostics but never required; the OpenSSH client
  is the transport.
- The upstream `gpu_server_probe` tool had no public repository or license file
  in the supplied notes, so no submodule URL is invented and no upstream source
  is redistributed. See the vendor NOTICE for attribution.

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
