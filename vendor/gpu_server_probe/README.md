# Vendored GPU server probe adapter

A small, standard-library-only adapter that lets ARK list GPU servers, probe
free GPUs and prepare a bounded dispatch request. It mirrors the read-only
surface of the internal `gpu_server_probe` tool; see [NOTICE.md](NOTICE.md).

- **Read-only by default.** Probing only runs `nvidia-smi`; dispatch preparation
  is dry-run.  An operator may opt into the separate bare-metal queue with
  `queue.enabled: true`; only that explicit queue path launches SSH jobs.
- **No credentials.** The server schema rejects passwords, keys, tokens and
  identity files. SSH authentication comes from your agent or `~/.ssh/config`.
- **No new dependencies.** `ssh` is the OpenSSH client on `PATH` (override with
  `ARK_GPU_SSH`). `asyncssh` is detected for diagnostics but never required.

## Server list

The schema is implemented in [`config.py`](config.py); a safe example is
[`servers_rp.example.yaml`](servers_rp.example.yaml). Copy it to your private
ARK config path and edit hosts:

```bash
mkdir -p ~/.ark
cp vendor/gpu_server_probe/servers_rp.example.yaml ~/.ark/gpu_servers.yaml
```

Resolution order for the config path:

1. `$ARK_GPU_SERVERS_CONFIG`
2. `$ARK_PRIVATE_DIR/gpu_servers.yaml`
3. `~/.ark/gpu_servers.yaml`

`enabled` defaults to `false`; probing is also enabled by `ARK_GPU_PROBE=1`.
The config is a restricted YAML subset (block maps/lists, scalars, inline
lists), so unknown fields, anchors and tabs fail with a clear error instead of
being silently ignored.

## CLI

Run from the repository root:

```bash
python -m vendor.gpu_server_probe status
python -m vendor.gpu_server_probe servers
python -m vendor.gpu_server_probe probe          # dry run: print the SSH plan
python -m vendor.gpu_server_probe probe --live   # read-only nvidia-smi
python -m vendor.gpu_server_probe dispatch --server gpu-server-1 --gpus 0,1 \
  --command "python train.py --cfg config.yaml"
```

`probe` will not connect unless `--live` is passed, and `--live` additionally
requires `enabled: true` in the server list. `dispatch` prints a JSON request
that is explicitly marked `"executed": false`; feed it to your scheduler or the
upstream tool yourself.

## API used by ARK

- `config.load_config(path)` / `config.public_server(server)`
- `ssh.run_remote(server, timeout=…)` → `CommandResult`
- `probe.parse_nvidia_smi(text)` / `probe.probe_server(…)` / `probe.rank_gpus(…)`
- `dispatch.build_request(body, servers)`


## Bare-metal queue (opt-in)

Set `queue.enabled: true` in the private server list to let ARK place and run
jobs. Agents submit a logical request (`command`, `work_dir`, `gpu_count`,
`min_free_memory_mb`); the queue probes, chooses eligible GPUs, reserves them in
its local SQLite state, and launches over SSH. It tracks PID, status and logs
under `queue.state_dir` on the remote host.

Placement defaults to one job per GPU. To stack on low-occupancy cards:

```yaml
queue:
  allow_colocation: true
  max_jobs_per_gpu: 2
  max_utilization_pct: 20
  min_free_memory_mb: 12000
```

Path mapping and environment adaptation are applied before launch:

```yaml
queue:
  setup: "source ~/miniconda3/bin/activate research"
  path_map:
    - from: /mnt/shared/research
      to: /srv/research
  env:
    PYTHONUNBUFFERED: "1"
```

The queue is not a substitute for a cluster scheduler: it coordinates ARK
jobs, but an external user can still take a GPU outside ARK. Use Slurm or
Kubernetes if you need hard multi-tenant isolation.
