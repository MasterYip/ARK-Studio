# Resources graph

**Execute › Resources** (`#/execute/resources`) shows the devices a project
uses, the resources on each device and how the devices connect, as a node
graph. It answers "what do we have, is it up, and how much are we using?":

- **Devices**: the ARK server, GPU servers, members' machines registered
  through setup resources, paired phones, git hosts, and compute resources
  ARK does not monitor (for example a Slurm cluster).
- **Resources** on each device: GPUs, CPU and memory, project storage and
  database, repositories and checkouts, storage, environments, workspaces,
  compute allocations, agent sessions and their worktrees.
- **Connections**: SSH from the ARK server to GPU servers, git remotes,
  machines that used the ARK API (setup verification), paired phones.

It is read-only. Opening the page never probes a GPU server, opens an SSH
connection or runs a process; GPU data comes from the last explicit probe
(GPU board, Settings, or **Check now** on a GPU server card).

## Status

| State | Meaning |
| --- | --- |
| online | a passive signal within the last 15 minutes: this server answering, a successful probe, a phone request, setup activity or a verification from that machine |
| stale | the last signal is older (shown as "last seen 3 h ago"); GPU rows show their last known values |
| offline | the last probe of a GPU server failed (the reason is shown) |
| unknown | no signal yet (never probed, external git host, private device) |

Hover a card for the reason, the time and the signal source. Offline and
stale devices are dimmed; links to them, and links not confirmed by a
signal, are dashed.

**Ours vs others**: a GPU counts as ours when a running GPU queue job of this
project holds it; memory is split by the probe's per-process `job_id`
(`ARK_GPU_JOB_ID`). Other used memory or utilisation above 10% counts as
others'. The queue is only read when its worker is already running.

## Controls

The graph is driven like the Node graph (Map › Node layout):

| Input | Action |
| --- | --- |
| toolbar | Read / Edit mode, expand / collapse all details, reset layout, zoom in / out, fit, full screen, `?` shortcuts |
| `R` / `E` | read / edit mode (remembered) |
| `F`, `?`, `Esc` | fit, shortcuts, close shortcuts or clear the selection and close the details |
| arrows, `Enter` | move the selection to the nearest card; open its details (read mode) |
| `Ctrl+A` | select every card |
| `+` / `=`, `-` | expand / collapse the selected cards' details |
| `*`, `/` | the same, recursively (Mind's keys; a card has one level) |
| click | select; read mode also opens the details panel, edit mode only selects (an open panel follows the selection) |
| `Ctrl` / `Shift`+click | add to or remove from the selection |
| left-drag on empty space | box select (`Ctrl` adds, `Shift` selects only cards fully inside) |
| drag a card | move it (with the rest of the selection); positions are remembered until **Reset layout** |
| right-drag, right-click | pan; context menu |
| wheel, `Ctrl`+wheel, `Shift`+wheel | zoom at the pointer, pan vertically, pan horizontally |
| double-click a card | centre it and open its details |
| `▾` / `▸` in a card header | fold that card's details, or the whole selection when the card is part of it |

Folded cards keep their header and summary bar and say how many rows are
hidden. Fold state and moved cards are kept in the browser.

## Personal devices

A machine registered in a member's own setup layer (and not referenced by
the project layer or named like a GPU server) and a paired phone are
personal. Other members see that the device exists, its owner, and the
project's shared resources on it without paths. Name, status, metrics,
paths and the owner's own resources stay private unless the owner turns on
**Share details**. The owner can also **Hide** the device from the project
graph; its shared resources then appear under "Not located yet" without
paths. Both settings are per account (org user settings, scope `devices`)
and enforced by the server.

## API (also the Go contract)

### `GET /api/resources/graph`

Project read permission (`X-ARK-Project` selects the project). Not available
to paired phones. Response:

```json
{
  "generated_at": "2026-11-14T12:00:00Z",
  "viewer": "alice",
  "project": {"id": "prj_…", "name": "PredActor"},
  "devices": [{
    "id": "ark | gpu:<slug> | host:<host_id> | phone:<pairing id> | git:<hostname> | compute:<slug> | unplaced",
    "label": "lab-a100",
    "kind": "ark | gpu-server | host | phone | git-host | compute | group",
    "scope": "shared | personal",
    "owner": {"username": "alice", "display_name": "Alice"} ,
    "mine": true,
    "prefs": {"hidden": false, "share_details": false},
    "details_shared": true,
    "status": {"state": "online | stale | offline | unknown", "reason": "English text",
               "code": "heartbeat | probe_ok | probe_old | probe_failed | probe_never | recent_signal | old_signal | no_signal | external | unplaced | unmonitored | private",
               "params": {"error": "…", "signal": "setup | verification | request", "kind": "slurm"},
               "last_seen": "ISO time or null", "source": "heartbeat | probe | setup | phone | none"},
    "metrics": {"disk_pct": 25.0, "disk_used_gb": 250, "disk_total_gb": 1000, "load1": 0.5, "platform": "linux",
                "cpu_pct": 35, "mem_used_mb": 64000, "mem_total_mb": 256000},
    "occupancy": {"gpus_total": 4, "gpus_ours": 1, "gpus_busy_others": 2, "gpus_free": 1,
                  "mem_ours_mb": 20000, "mem_others_mb": 60000, "mem_total_mb": 160000, "util_avg_pct": 53.3},
    "probe": {"probed_at": "…", "ok": true, "error": null},
    "storage": [{"mount": "/data", "used_bytes": 7143925706752, "total_bytes": 7937876074496,
                 "free_bytes": 793950367744, "percent": 90.0, "data": true}],
    "detail": {"host": "lab-a100.lab", "user": "ark", "tags": []},
    "actions": ["check", "privacy"],
    "sources": ["gpu-config", "setup", "heartbeat", "mobile"]
  }],
  "resources": [{
    "id": "<device>/<kind>/<slug>/<n>", "device": "gpu:lab-a100",
    "kind": "gpu | cpu | storage | database | repository | environment | task_space | compute | worktree | sessions",
    "label": "GPU 0 · A100", "path": "/srv/code or null", "redacted": false,
    "owner": "alice or null (project resource)", "setup_key": "project/code or u:alice/code or null",
    "verified_at": "ISO time (setup locations)",
    "metrics": {"util_pct": 90, "mem_used_mb": 30000, "mem_total_mb": 40000, "mem_ours_mb": 12000,
                "mem_others_mb": 18000, "job": "train", "ours": true},
    "last_known": false
  }],
  "connections": [{"id": "ark|gpu:lab-a100|ssh", "source": "ark", "target": "gpu:lab-a100",
                   "kind": "ssh | git-remote | agent-api | paired", "label": "SSH",
                   "state": "ok | failed | unknown"}],
  "probe": {"enabled": true, "last_probed_at": "…"},
  "online_window_seconds": 900,
  "unknowns": ["sshfs_tunnels", "browser_host", "host_metrics", "gpu_disabled"]
}
```

Optional keys (`mine`, `prefs`, `details_shared`, `detail`, `verified_at`,
`storage`) are omitted when they do not apply. `storage` (Go server only) is
a GPU server's disks from the last probe: the same read-only system query
runs `df -PB1` (pseudo filesystems, snaps, container layers and `/boot`
skipped, bind mounts deduplicated). The mount holding a configured data
folder (`queue.path_map[].to` for that server) comes first with `data:
true`, then the largest mounts, at most four. No `df`, or output that does
not parse, just leaves `storage` out; the GPU probe is unaffected. The same
list is the `storage` member of each server in `POST /api/gpu/probe` (and
the dashboard's probe). For another member's private device
`label` becomes "<name>'s device", `status` is `unknown`/`private`,
`metrics`/`occupancy`/`detail` are null, the owner's own resources are
removed and shared ones have `path: null, redacted: true`. Hidden devices,
their resources' locations and their connections are removed for everyone
but the owner.

### `POST /api/resources/devices/<device id>`

Body `{"hidden": true}` and/or `{"share_details": true}` (booleans only).
Only the owner of a personal device (or of a device they hid) may call it;
otherwise `404 RESOURCES_DEVICE_NOT_FOUND`. Errors: `400 RESOURCES_INVALID`,
`400 RESOURCES_ACCOUNT_REQUIRED` (no account, e.g. local mode). Returns
`{"device": {"device": id, "hidden": bool, "share_details": bool}}`.

### Server-side changes the port must mirror

- `GpuService.probe()` keeps the last result (`last_probe = {probed_at,
  servers}`); the graph reads it and never probes.
- Setup layers are read without migration (`setup_layers.read_layers`) and
  the newest 2,000 `setup/history.jsonl` events give each host's last
  activity (`recent_history`).
- Paired phones: claimed, unrevoked pairings of the project with the newest
  session request (`PairingManager.project_devices`).
- Host ids equal to the server's `machine_fingerprint()` are the ARK server;
  host ids or labels equal to a GPU server name/host are that GPU server.

## What ARK does not record (yet)

Listed under "Not recorded by ARK" on the page:

- sshfs mirrors and SSH tunnels started with `ark remote` (the CLI stores
  nothing on the server), so those links cannot be drawn;
- which machine a browser session runs on (only agents send `X-ARK-Host`);
- CPU, memory and disk of machines other than the ARK server and probed GPU
  servers;
- live reachability of members' machines (only their last setup activity).
