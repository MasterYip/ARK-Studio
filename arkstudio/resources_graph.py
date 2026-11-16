"""Read-only resources graph: devices, the resources on them, how they connect.

``GET /api/resources/graph`` aggregates what ARK already records; it never
starts a probe, an SSH connection or a subprocess.  Sources:

* the ARK server host itself (it is answering, so it is online), its project
  storage, disk usage of the projects root and running agent sessions;
* GPU servers from the GPU config and the *last* explicit probe (Settings /
  GPU board / "Check now"), plus our running queue jobs when the queue worker
  is already running;
* setup resources (repositories, storage, environments, task spaces,
  compute) and their per-host locations from the project and user setup
  layers, plus the setup history for the last activity of each host;
* paired phones (claimed, not revoked) and their last request.

Personal devices (a member's host registered through setup, or a paired
phone) are private: other members see that the device exists and the
project's shared resources on it, not its name, status, metrics, paths or the
owner's own resources, unless the owner shares details.  An owner can also
hide a personal device from the project graph.  Both preferences are stored
per account (org user settings, scope ``devices``) and enforced here.

``build_graph(sources, viewer)`` is pure so tests can feed fixtures; the
server collects ``sources`` with :func:`collect_sources`.
"""
from __future__ import annotations

import hashlib
import json
import os
import re
import shutil
import socket
from datetime import datetime, timezone
from pathlib import Path
from typing import Any, Iterable, Mapping

ONLINE_WINDOW = 15 * 60  # seconds a passive signal counts as "online"
DEVICE_PREF_KEYS = ("hidden", "share_details")
RESOURCE_KINDS = ("repository", "storage", "environment", "task_space", "compute")
_GIT_HOST_RE = re.compile(r"^(?:[a-z+]+://)?(?:[^@/]+@)?([A-Za-z0-9.-]+\.[A-Za-z]{2,})[:/]")


class ResourcesGraphError(ValueError):
    def __init__(self, status: int, message: str, code: str):
        super().__init__(message)
        self.status, self.message, self.code = status, message, code


# --------------------------------------------------------------- utilities
def _parse_time(value: Any) -> float | None:
    if value in (None, ""):
        return None
    if isinstance(value, (int, float)):
        return float(value)
    try:
        text = str(value).replace("Z", "+00:00")
        parsed = datetime.fromisoformat(text)
        if parsed.tzinfo is None:
            parsed = parsed.replace(tzinfo=timezone.utc)
        return parsed.timestamp()
    except ValueError:
        return None


def _iso(ts: float | None) -> str | None:
    if ts is None:
        return None
    return datetime.fromtimestamp(ts, timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")


def _slug(text: str) -> str:
    value = re.sub(r"[^a-z0-9]+", "-", str(text or "").lower()).strip("-")
    return value[:48] or hashlib.sha1(str(text).encode()).hexdigest()[:8]


def git_host(remote: str) -> str:
    """Hostname of a git remote URL or scp-style address ('' if none)."""
    match = _GIT_HOST_RE.match(str(remote or "").strip())
    return match.group(1).lower() if match else ""


def status(state: str, reason: str, last_seen: float | None = None, source: str = "none",
           code: str = "", **params: Any) -> dict:
    """``reason`` is English; ``code`` + ``params`` let the UI translate it."""
    return {"state": state, "reason": reason, "code": code or state, "params": params,
            "last_seen": _iso(last_seen), "source": source}


def passive_status(last_seen: float | None, now: float, *, what: str, source: str) -> dict:
    """online within the window, otherwise stale ("last seen"), else unknown.

    ``what`` is a signal code: ``setup``, ``verification`` or ``request``."""
    noun = {"setup": "setup activity", "verification": "verification", "request": "request"}.get(what, what)
    if last_seen is None:
        return status("unknown", f"No {noun} recorded yet", None, "none", "no_signal", signal=what)
    age = max(0.0, now - last_seen)
    if age <= ONLINE_WINDOW:
        return status("online", f"{noun.capitalize()} {_ago(age)}", last_seen, source, "recent_signal", signal=what)
    return status("stale", f"Last {noun} {_ago(age)}", last_seen, source, "old_signal", signal=what)


def _ago(seconds: float) -> str:
    if seconds < 90:
        return "just now"
    if seconds < 5400:
        return f"{round(seconds / 60)} min ago"
    if seconds < 172800:
        return f"{round(seconds / 3600)} h ago"
    return f"{round(seconds / 86400)} d ago"


# ---------------------------------------------------------- source capture
def collect_sources(app, project, *, now: float | None = None) -> dict:
    """Read every source once.  No probes, no subprocesses, no writes."""
    from .skills import setup as setup_base
    from .skills import setup_layers

    now = now if now is not None else datetime.now(timezone.utc).timestamp()
    project_path = Path(project.path)
    layers = setup_layers.read_layers(project_path)
    history = setup_layers.recent_history(project_path)
    gpu = app.gpu
    try:
        servers = gpu.servers()
    except Exception:  # noqa: BLE001 - a broken optional config must not break the graph
        servers = {"enabled": False, "servers": []}
    jobs = []
    queue = getattr(gpu, "_queue", None)
    if queue is not None:  # only when the worker already runs; never start it here
        try:
            jobs = gpu.queue_list(project=project.id, status="starting,running", limit=200).get("jobs", [])
        except Exception:  # noqa: BLE001
            jobs = []
    try:
        sessions = app.sessions.list().get("sessions", [])
    except Exception:  # noqa: BLE001
        sessions = []
    sessions = [item for item in sessions if str(item.get("project") or "") in ("", project.id, project.name)]
    try:
        disk = shutil.disk_usage(project_path)
        disk = {"total": disk.total, "used": disk.used, "free": disk.free}
    except OSError:
        disk = None
    try:
        load = os.getloadavg()[0]
    except (AttributeError, OSError):
        load = None
    owners = set()
    for record in layers["users"]:
        owners.add(record.get("owner") or "")
    phones = app.mobile.project_devices(project.id) if hasattr(app.mobile, "project_devices") else []
    for phone in phones:
        owners.add(phone.get("user") or "")
    users = {}
    prefs = {}
    for name in sorted(filter(None, owners)):
        row = app.org.store.user_by_name(name) if getattr(app, "org", None) else None
        users[name] = {"username": name, "display_name": (row or {}).get("display_name") or name}
        prefs[name] = device_prefs(app, name)
    return {
        "now": now,
        "project": {"id": project.id, "name": project.name, "path": str(project_path),
                    "db": str(project_path / "ark.sqlite3")},
        "server": {"hostname": socket.gethostname(), "platform": _platform(),
                   "fingerprint": setup_base.machine_fingerprint(), "disk": disk, "load1": load},
        "layers": layers,
        "history": history,
        "gpu": {"enabled": bool(servers.get("enabled")), "servers": servers.get("servers") or [],
                "last_probe": getattr(gpu, "last_probe", None), "jobs": jobs},
        "sessions": sessions,
        "phones": phones,
        "users": users,
        "prefs": prefs,
    }


def _platform() -> str:
    from .platform_support import platform_id
    return platform_id()


# ------------------------------------------------- personal device settings
def device_prefs(app, username: str) -> dict:
    """``{device_id: {hidden, share_details}}`` for one account."""
    org = getattr(app, "org", None)
    if not org or not username:
        return {}
    user = org.store.user_by_name(username)
    if not user:
        return {}
    row = org.store.settings_row(user["id"], "devices") or {}
    try:
        data = json.loads(row.get("data_json") or "{}")
    except ValueError:
        data = {}
    devices = data.get("devices") if isinstance(data, dict) else None
    return devices if isinstance(devices, dict) else {}


def set_device_pref(app, username: str, device_id: str, patch: Mapping) -> dict:
    org = getattr(app, "org", None)
    user = org.store.user_by_name(username) if org and username else None
    if not user:
        raise ResourcesGraphError(400, "personal device settings need a signed-in account",
                                  "RESOURCES_ACCOUNT_REQUIRED")
    if not isinstance(patch, Mapping) or not patch or set(patch) - set(DEVICE_PREF_KEYS):
        raise ResourcesGraphError(400, "device settings accept only hidden and share_details",
                                  "RESOURCES_INVALID")
    if any(not isinstance(value, bool) for value in patch.values()):
        raise ResourcesGraphError(400, "hidden and share_details must be true or false",
                                  "RESOURCES_INVALID")
    device_id = str(device_id or "")
    if not re.fullmatch(r"[A-Za-z0-9:._-]{1,120}", device_id):
        raise ResourcesGraphError(400, "invalid device id", "RESOURCES_INVALID")
    devices = dict(device_prefs(app, username))
    current = {key: bool((devices.get(device_id) or {}).get(key)) for key in DEVICE_PREF_KEYS}
    current.update(patch)
    if any(current.values()):
        devices[device_id] = current
    else:
        devices.pop(device_id, None)
    org.store.save_settings(user["id"], "devices", {"devices": devices})
    return {"device": device_id, **current}


# --------------------------------------------------------------- the graph
class _Graph:
    def __init__(self):
        self.devices: dict[str, dict] = {}
        self.resources: list[dict] = []
        self.connections: dict[str, dict] = {}

    def device(self, device_id: str, **fields) -> dict:
        item = self.devices.get(device_id)
        if item is None:
            item = {"id": device_id, "label": device_id, "kind": "host", "scope": "shared",
                    "owner": None, "status": status("unknown", "No activity recorded yet", code="no_signal", signal="setup"),
                    "metrics": None, "occupancy": None, "probe": None, "actions": [],
                    "sources": []}
            self.devices[device_id] = item
        for key, value in fields.items():
            if value is not None:
                item[key] = value
        return item

    def resource(self, device: str, kind: str, label: str, **fields) -> dict:
        item = {"id": f"{device}/{kind}/{_slug(label)}/{len(self.resources)}", "device": device,
                "kind": kind, "label": label, "path": None, "redacted": False, "owner": None,
                "setup_key": None, "metrics": None, "last_known": False}
        item.update({k: v for k, v in fields.items() if v is not None})
        self.resources.append(item)
        return item

    def connect(self, source: str, target: str, kind: str, label: str, state: str = "unknown") -> None:
        key = f"{source}|{target}|{kind}"
        if source == target or key in self.connections:
            return
        self.connections[key] = {"id": key, "source": source, "target": target, "kind": kind,
                                 "label": label, "state": state}


def build_graph(sources: Mapping[str, Any], viewer: str) -> dict:
    now = float(sources["now"])
    g = _Graph()
    server = sources["server"]
    project = sources["project"]
    unknowns: list[str] = []  # codes of facts ARK does not record

    # 1. The ARK server host: answering this request is its heartbeat.
    ark = g.device("ark", label=server.get("hostname") or "ARK server", kind="ark", scope="shared",
                   status=status("online", "Serving this page", now, "heartbeat", "heartbeat"))
    disk = server.get("disk")
    ark["metrics"] = {
        "disk_total_gb": round(disk["total"] / 1e9, 1) if disk else None,
        "disk_used_gb": round(disk["used"] / 1e9, 1) if disk else None,
        "disk_pct": round(100 * disk["used"] / disk["total"], 1) if disk and disk["total"] else None,
        "load1": round(server["load1"], 2) if server.get("load1") is not None else None,
        "platform": server.get("platform"),
    }
    ark["sources"] = ["heartbeat"]
    g.resource("ark", "storage", "Project folder", path=project["path"],
               metrics={"disk_pct": ark["metrics"]["disk_pct"]})
    g.resource("ark", "database", "Project database", path=project["db"])
    running = [s for s in sources.get("sessions") or [] if s.get("status") in ("running", "starting", "waiting")]
    if sources.get("sessions") is not None:
        g.resource("ark", "sessions", "Agent sessions",
                   metrics={"running": len(running), "mine": sum(1 for s in running if s.get("owner") == viewer),
                            "total": len(sources.get("sessions") or [])})
    for path in sorted({s.get("worktree") for s in running if s.get("worktree")}):
        g.resource("ark", "worktree", Path(path).name or path, path=path)

    # 2. GPU servers: config + the last explicit probe.  Never probes here.
    gpu = sources.get("gpu") or {}
    last_probe = gpu.get("last_probe") or {}
    probed_at = _parse_time(last_probe.get("probed_at"))
    results = {item.get("server"): item for item in last_probe.get("servers") or []}
    ours = {}
    for job in gpu.get("jobs") or []:
        for index in job.get("gpu_indices") or []:
            ours[(job.get("server"), int(index))] = job
    gpu_names = {}
    for server_cfg in gpu.get("servers") or []:
        name = server_cfg.get("name") or server_cfg.get("host")
        device_id = f"gpu:{_slug(name)}"
        gpu_names[str(name).lower()] = device_id
        if server_cfg.get("host"):
            gpu_names[str(server_cfg["host"]).lower()] = device_id
        device = g.device(device_id, label=name, kind="gpu-server", scope="shared")
        device["sources"] = ["gpu-config"]
        device["actions"] = ["check"] if gpu.get("enabled") else []
        device["detail"] = {"host": server_cfg.get("host"), "user": server_cfg.get("user"),
                            "tags": server_cfg.get("tags") or []}
        result = results.get(name)
        if result is None:
            device["status"] = status("unknown", "Not probed since the ARK server started; use Check now",
                                      None, "probe", "probe_never")
            g.connect("ark", device_id, "ssh", "SSH", "unknown")
            continue
        device["probe"] = {"probed_at": _iso(probed_at), "ok": bool(result.get("ok")),
                           "error": (result.get("error") or {}).get("message")}
        if not result.get("ok"):
            message = (result.get("error") or {}).get("message") or "unreachable"
            device["status"] = status("offline", f"Last probe failed: {message}", probed_at, "probe",
                                      "probe_failed", error=message)
            g.connect("ark", device_id, "ssh", "SSH", "failed")
        else:
            age = now - (probed_at or now)
            device["status"] = (status("online", f"Last probe ok {_ago(age)}", probed_at, "probe", "probe_ok")
                                if age <= ONLINE_WINDOW else
                                status("stale", f"Last probe ok {_ago(age)}; metrics may be out of date",
                                       probed_at, "probe", "probe_old"))
            g.connect("ark", device_id, "ssh", "SSH", "ok")
        stale = device["status"]["state"] != "online"
        _gpu_resources(g, device, result, name, ours, last_known=stale)

    # 3. Setup layers: hosts, resource locations, compute.
    layers = sources.get("layers") or {"project": {}, "users": []}
    project_layer = layers.get("project") or {}
    host_owner: dict[str, str] = {}
    host_label: dict[str, str] = {}
    for record in layers.get("users") or []:
        for host in record.get("hosts") or []:
            host_owner.setdefault(host["id"], record.get("owner") or "")
            host_label.setdefault(host["id"], host.get("label") or host["id"])
    shared_hosts = {loc["host_id"] for loc in project_layer.get("locations") or []}
    last_activity: dict[str, tuple[float, str]] = {}

    def seen(host_id: str, when: Any, what: str) -> None:
        ts = _parse_time(when)
        if ts is not None and ts > last_activity.get(host_id, (0.0, ""))[0]:
            last_activity[host_id] = (ts, what)

    for event in sources.get("history") or []:
        if event.get("host"):
            seen(event["host"], event.get("at"), "setup")
    all_records = [project_layer, *(layers.get("users") or [])]
    for record in all_records:
        verification = record.get("verification") or {}
        if verification.get("host"):
            seen(verification["host"], verification.get("at"), "verification")
        for loc in record.get("locations") or []:
            seen(loc["host_id"], loc.get("verified_at"), "verification")

    def host_device(host_id: str, label: str = "") -> str:
        if host_id == server.get("fingerprint"):
            return "ark"
        matched = gpu_names.get(host_id.lower()) or gpu_names.get((label or "").lower())
        if matched:
            return matched
        device_id = f"host:{host_id}"
        owner = host_owner.get(host_id)
        personal = bool(owner) and host_id not in shared_hosts
        device = g.device(device_id, label=host_label.get(host_id) or label or host_id, kind="host",
                          scope="personal" if personal else "shared",
                          owner=sources["users"].get(owner, {"username": owner, "display_name": owner}) if personal else None)
        if "setup" not in device["sources"]:
            device["sources"].append("setup")
            ts, what = last_activity.get(host_id, (None, ""))
            device["status"] = passive_status(ts, now, what=what or "setup", source="setup")
            if ts is not None:
                g.connect(device_id, "ark", "agent-api", "ARK API",
                          "ok" if now - ts <= ONLINE_WINDOW else "unknown")
        return device_id

    resources = []
    for record in all_records:
        layer = record.get("layer") or ("project" if record is project_layer else "user")
        owner = record.get("owner") or ""
        prefix = "project" if layer == "project" else f"u:{owner}"
        for item in (record.get("profile") or {}).get("resources") or []:
            if item.get("kind") not in RESOURCE_KINDS:
                continue
            resources.append((f"{prefix}/{item.get('id')}", layer, owner, item))
    locations: dict[str, list[dict]] = {}
    for record in all_records:
        for loc in record.get("locations") or []:
            locations.setdefault(loc["resource"], []).append(loc)
    unplaced = None
    for key, layer, owner, item in resources:
        details = item.get("details") or {}
        kind = item.get("kind")
        name = item.get("name") or item.get("id")
        res_owner = None if layer == "project" else owner
        if kind == "compute":
            target = details.get("gpu_server")
            device_id = gpu_names.get(str(target or "").lower())
            if not device_id:
                device_id = f"compute:{_slug(name)}"
                personal = layer == "user"
                g.device(device_id, label=name, kind="compute", scope="personal" if personal else "shared",
                         owner=sources["users"].get(owner, {"username": owner, "display_name": owner}) if personal else None)
                g.devices[device_id]["sources"] = ["setup"]
                g.devices[device_id]["status"] = status(
                    "unknown", f"{details.get('kind') or 'compute'} resource; ARK does not monitor it", None, "none",
                    "unmonitored", kind=details.get("kind") or "compute")
            g.resource(device_id, "compute", name, setup_key=key, owner=res_owner,
                       metrics={"gpus": details.get("gpus"), "use": details.get("use")})
            continue
        res_kind = "database" if kind == "storage" and "database" in str(details.get("kind") or "").lower() else kind
        placed = False
        for loc in locations.get(key, []):
            device_id = host_device(loc["host_id"], loc.get("host_label") or "")
            g.resource(device_id, res_kind, name, path=loc.get("path_or_ref"), setup_key=key,
                       owner=res_owner, verified_at=loc.get("verified_at") or None,
                       metrics={"role": details.get("role")} if details.get("role") else None,
                       layer=layer)
            placed = True
            remote_host = git_host(details.get("remote") or "") if kind == "repository" else ""
            if remote_host:
                g.connect(device_id, f"git:{remote_host}", "git-remote", "git remote")
        if kind == "repository" and details.get("remote"):
            remote_host = git_host(details["remote"])
            if remote_host:
                remote_id = f"git:{remote_host}"
                g.device(remote_id, label=remote_host, kind="git-host", scope="shared",
                         status=status("unknown", "External service; ARK does not monitor it", code="external"))
                g.resource(remote_id, "repository", name, path=details["remote"], setup_key=key, owner=res_owner,
                           layer=layer)
                placed = True
        if not placed:
            if unplaced is None:
                unplaced = g.device("unplaced", label="Not located yet", kind="group", scope="shared",
                                    status=status("unknown", "Resources without a registered location", code="unplaced"))
            g.resource("unplaced", res_kind, name, setup_key=key, owner=res_owner, layer=layer)
    for record in layers.get("users") or []:
        for host in record.get("hosts") or []:
            host_device(host["id"], host.get("label") or "")

    # 4. Paired phones.
    for phone in sources.get("phones") or []:
        device_id = f"phone:{phone['id']}"
        owner = phone.get("user") or ""
        g.device(device_id, label=phone.get("label") or "Phone", kind="phone", scope="personal",
                 owner=sources["users"].get(owner, {"username": owner, "display_name": owner}),
                 status=passive_status(_parse_time(phone.get("last_seen_at")), now, what="request", source="phone"))
        g.devices[device_id]["sources"] = ["mobile"]
        g.devices[device_id]["detail"] = {"permission": phone.get("permission")}
        g.connect(device_id, "ark", "paired", "paired phone",
                  "ok" if g.devices[device_id]["status"]["state"] == "online" else "unknown")

    # 5. Privacy: personal devices of other members.
    prefs = sources.get("prefs") or {}
    hidden: set[str] = set()
    for device in g.devices.values():
        owner = (device.get("owner") or {}).get("username") if device["scope"] == "personal" else None
        if not owner:
            continue
        pref = (prefs.get(owner) or {}).get(device["id"]) or {}
        device["mine"] = owner == viewer
        if device["mine"]:
            device["prefs"] = {"hidden": bool(pref.get("hidden")), "share_details": bool(pref.get("share_details"))}
            device["actions"] = [*device["actions"], "privacy"]
            continue
        if pref.get("hidden"):
            hidden.add(device["id"])
            continue
        if pref.get("share_details"):
            device["details_shared"] = True
            continue
        device.update(label=f"{(device.get('owner') or {}).get('display_name') or owner}'s {_kind_noun(device['kind'])}",
                      status=status("unknown", "Private device; the owner has not shared its details", code="private"),
                      metrics=None, occupancy=None, detail=None, private=True)
    out_resources = []
    for res in g.resources:
        device = g.devices.get(res["device"])
        if res["device"] in hidden:
            # Shared project resources stay visible, without their location.
            if res.get("layer") == "project":
                if "unplaced" not in g.devices:
                    g.device("unplaced", label="Not located yet", kind="group", scope="shared",
                             status=status("unknown", "Resources without a registered location", code="unplaced"))
                out_resources.append({**res, "device": "unplaced", "path": None, "redacted": True})
            continue
        if device.get("private"):
            if res.get("layer") != "project":
                continue  # the owner's own resources stay private
            res = {**res, "path": None, "redacted": True, "metrics": None, "verified_at": None}
        out_resources.append(res)
    for res in out_resources:
        res.pop("layer", None)
        if g.devices[res["device"]]["status"]["state"] in ("offline", "stale") and res["device"] != "ark":
            res["last_known"] = True
    devices = [d for d in g.devices.values() if d["id"] not in hidden]
    for device in devices:
        device.pop("private", None)
    connections = [c for c in g.connections.values() if c["source"] not in hidden and c["target"] not in hidden]
    # Codes; the UI translates them (English text in docs/operations).
    unknowns.extend(["sshfs_tunnels", "browser_host", "host_metrics"])
    if not gpu.get("enabled"):
        unknowns.append("gpu_disabled")
    return {
        "generated_at": _iso(now),
        "viewer": viewer,
        "project": {"id": project["id"], "name": project["name"]},
        "devices": devices,
        "resources": out_resources,
        "connections": connections,
        "probe": {"enabled": bool(gpu.get("enabled")), "last_probed_at": _iso(probed_at)},
        "online_window_seconds": ONLINE_WINDOW,
        "unknowns": unknowns,
    }


def _kind_noun(kind: str) -> str:
    return {"phone": "phone", "compute": "compute resource"}.get(kind, "device")


def _gpu_resources(g: _Graph, device: dict, result: Mapping, name: str, ours: Mapping,
                   *, last_known: bool) -> None:
    gpus = result.get("gpus") or []
    system = result.get("system") or {}
    processes = system.get("processes") or []
    ours_count = busy_others = 0
    mem_ours = mem_others = 0.0
    for gpu in gpus:
        index = int(gpu.get("index") or 0)
        used = float(gpu.get("memory_used_mb") or 0)
        total = float(gpu.get("memory_total_mb") or 0)
        job = ours.get((name, index))
        job_ids = {job.get("id")} if job else set()
        mine = sum(float(p.get("memory_mb") or 0) for p in processes
                   if p.get("gpu_index") == index and p.get("job_id") and p.get("job_id") in job_ids)
        if job and not mine:
            mine = used  # our job holds the GPU but per-process memory is unavailable
        others = max(0.0, used - mine)
        mem_ours += mine
        mem_others += others
        if job:
            ours_count += 1
        elif (gpu.get("utilization_pct") or 0) >= 10 or others >= 1024:
            busy_others += 1
        g.resource(device["id"], "gpu", f"GPU {index} · {gpu.get('name') or 'GPU'}", last_known=last_known,
                   metrics={"index": index, "util_pct": gpu.get("utilization_pct"),
                            "mem_used_mb": used, "mem_total_mb": total,
                            "mem_ours_mb": round(mine), "mem_others_mb": round(others),
                            "temperature_c": gpu.get("temperature_c"),
                            "job": job.get("job") if job else None,
                            "ours": bool(job)})
    if system:
        g.resource(device["id"], "cpu", "CPU & memory", last_known=last_known,
                   metrics={"cpu_pct": system.get("cpu_percent") if system.get("cpu_percent") is not None
                            else system.get("cpu_load_pct"),
                            "cores": system.get("cores"), "load1": system.get("load1"),
                            "mem_used_mb": system.get("memory_used_mb"), "mem_total_mb": system.get("memory_total_mb")})
    utils = [float(x.get("utilization_pct") or 0) for x in gpus]
    device["occupancy"] = {
        "gpus_total": len(gpus), "gpus_ours": ours_count, "gpus_busy_others": busy_others,
        "gpus_free": max(0, len(gpus) - ours_count - busy_others),
        "mem_ours_mb": round(mem_ours), "mem_others_mb": round(mem_others),
        "mem_total_mb": round(sum(float(x.get("memory_total_mb") or 0) for x in gpus)),
        "util_avg_pct": round(sum(utils) / len(utils), 1) if utils else None,
    }
    device["metrics"] = {"cpu_pct": system.get("cpu_percent"), "load1": system.get("load1"),
                         "mem_used_mb": system.get("memory_used_mb"), "mem_total_mb": system.get("memory_total_mb")}


def iter_device_ids(graph: Mapping) -> Iterable[str]:
    return (device["id"] for device in graph.get("devices") or [])
