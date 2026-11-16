"""Parse ``nvidia-smi`` output and rank free GPUs.

The CSV shape is fixed by ``ssh.PROBE_COMMAND``:
``index, name, memory.total [MiB], memory.used [MiB], utilization.gpu [%]``.
Missing values (``[N/A]``, ``[Not Supported]``) stay ``None`` instead of being
reported as zero, so a ranked entry is only emitted when it is trustworthy.
"""
from __future__ import annotations

from . import ssh
from .config import public_server

MAX_WARNINGS = 20
MAX_RANKED = 64
FREE_MEMORY_THRESHOLD_MB = 512.0


def occupancy_pct(gpu: dict) -> float | None:
    """How occupied a GPU is: the busier of memory-used % and compute-utilization %.

    An idle GPU holding a large model is occupied even at 0% utilization, and a
    GPU at high utilization is occupied even with free memory.
    """
    total = gpu.get("memory_total_mb")
    used = gpu.get("memory_used_mb")
    util = gpu.get("utilization_pct")
    values = []
    if total and used is not None:
        values.append(max(0.0, min(100.0, 100.0 * float(used) / float(total))))
    if util is not None:
        values.append(max(0.0, min(100.0, float(util))))
    return round(max(values), 1) if values else None


def dispatch_key(gpu: dict) -> tuple:
    """Least-utilized first, then most free memory, then lowest index (the queue's order)."""
    util = gpu.get("utilization_pct")
    return (float(util) if util is not None else 101.0,
            -float(gpu.get("free_memory_mb") or 0.0),
            int(gpu.get("index") or 0))


def _number(token: str) -> float | None:
    token = (token or "").strip()
    if not token:
        return None
    lowered = token.lower()
    if lowered in {"n/a", "[n/a]", "not supported", "[not supported]", "unknown", "none"}:
        return None
    try:
        return float(token)
    except ValueError:
        return None


def _int(token: str) -> int | None:
    value = _number(token)
    if value is None or value != int(value):
        return None
    return int(value)


def parse_nvidia_smi(text: str) -> tuple[list[dict], list[str]]:
    """Return ``(gpus, warnings)`` for raw nvidia-smi CSV output."""
    gpus: list[dict] = []
    warnings: list[str] = []
    for line in (text or "").splitlines():
        raw = line.strip()
        if not raw:
            continue
        if raw.lower().startswith(("index", "no devices were found")):
            continue
        parts = [part.strip() for part in raw.split(",")]
        if len(parts) < 5:
            warnings.append(f"ignored malformed nvidia-smi line: {raw[:80]}")
            continue
        index = _int(parts[0])
        if index is None:
            warnings.append(f"ignored line with a non-numeric GPU index: {raw[:80]}")
            continue
        name = parts[1][:120] or "GPU"
        total = _number(parts[2])
        used = _number(parts[3])
        utilization = _number(parts[4])
        utilization_memory = _number(parts[5]) if len(parts) > 5 else None
        temperature = _number(parts[6]) if len(parts) > 6 else None
        power = _number(parts[7]) if len(parts) > 7 else None
        free = max(0.0, total - used) if total is not None and used is not None else None
        score = (round(free * (1.0 - min(100.0, max(0.0, utilization)) / 100.0), 1)
                 if free is not None and utilization is not None else None)
        if total is None:
            warnings.append(f"GPU {index} did not report total memory")
        gpus.append({
            "index": index,
            "name": name,
            "memory_total_mb": round(total, 1) if total is not None else None,
            "memory_used_mb": round(used, 1) if used is not None else None,
            "free_memory_mb": round(free, 1) if free is not None else None,
            "utilization_pct": round(utilization, 1) if utilization is not None else None,
            "utilization_memory_pct": (round(utilization_memory, 1)
                                       if utilization_memory is not None else None),
            "temperature_c": round(temperature, 1) if temperature is not None else None,
            "power_w": round(power, 1) if power is not None else None,
            "score": score,
        })
        gpus[-1]["occupancy_pct"] = occupancy_pct(gpus[-1])
    if len(warnings) > MAX_WARNINGS:
        warnings = warnings[:MAX_WARNINGS] + [f"… {len(warnings) - MAX_WARNINGS} more warnings"]
    return gpus, warnings


def parse_system(text: str) -> dict:
    """Parse CPU, RAM and per-process GPU usage from the system query."""
    out = {"load1": None, "load5": None, "load15": None, "cores": None,
           "cpu_load_pct": None, "cpu_percent": None,
           "memory_total_mb": None, "memory_used_mb": None,
           "memory_free_mb": None, "memory_available_mb": None,
           "processes": [], "process_util_available": False, "error": None}
    section = ""
    buses: dict[str, int] = {}
    proc_rows: list[dict] = []
    env_jobs: dict[str, str] = {}
    # Per-process compute share from `nvidia-smi pmon -s u`: (gpu, pid) -> SM %.
    pmon: dict[tuple[int, int], float] = {}
    try:
        for line in (text or "").splitlines():
            raw = line.rstrip()
            if raw.startswith("__ARK_"):
                section = raw.strip("_").replace("ARK_", "").lower()
                continue
            parts = raw.split()
            if section == "load" and len(parts) >= 3:
                out["load1"], out["load5"], out["load15"] = [float(p) for p in parts[:3]]
            elif section == "nproc" and parts:
                out["cores"] = int(parts[0])
            elif section == "mem" and len(parts) >= 4:
                out["memory_total_mb"] = int(parts[0])
                out["memory_used_mb"] = int(parts[1])
                out["memory_free_mb"] = int(parts[2])
                out["memory_available_mb"] = int(parts[3])
            elif section == "cpu" and "id" in raw:
                import re
                match = re.search(r"([0-9]+(?:\.[0-9]+)?)\s*id", raw)
                if match:
                    out["cpu_percent"] = round(max(0.0, 100.0 - float(match.group(1))), 1)
            elif section == "bus":
                fields = [part.strip() for part in raw.split(",")]
                if len(fields) >= 2:
                    try:
                        buses[fields[1]] = int(fields[0])
                    except ValueError:
                        pass
            elif section == "proc":
                fields = [part.strip() for part in raw.split(",")]
                if len(fields) >= 3:
                    try:
                        pid = int(fields[0])
                    except ValueError:
                        continue
                    try:
                        memory = int(float(fields[2]))
                    except ValueError:
                        memory = None   # "[N/A]": keep the process, memory unknown
                    proc_rows.append({"pid": pid, "bus": fields[1], "memory_mb": memory})
            elif section == "pmon":
                # "# gpu pid type sm mem enc dec ... command"; "-" means no sample.
                if not parts or parts[0].startswith("#") or len(parts) < 4:
                    continue
                try:
                    gpu_index, pid = int(parts[0]), int(parts[1])
                except ValueError:
                    continue
                out["process_util_available"] = True
                if parts[3] != "-":
                    try:
                        pmon[(gpu_index, pid)] = max(0.0, min(100.0, float(parts[3])))
                    except ValueError:
                        pass
            elif section == "env" and len(parts) >= 2:
                env_jobs[parts[0]] = parts[1]
        if out["load1"] is not None and out["cores"]:
            out["cpu_load_pct"] = round(min(100.0, out["load1"] / out["cores"] * 100.0), 1)
        if out["cpu_percent"] is None:
            out["cpu_percent"] = out["cpu_load_pct"]
        for row in proc_rows:
            row["gpu_index"] = buses.get(row.pop("bus", ""))
            row["job_id"] = env_jobs.get(str(row["pid"]), "")
            if row["gpu_index"] is not None:
                if out["process_util_available"]:
                    # pmon ran: a process with no sample used no measurable compute.
                    row["sm_pct"] = pmon.get((row["gpu_index"], row["pid"]), 0.0)
                else:
                    row["sm_pct"] = None
                out["processes"].append(row)
    except (TypeError, ValueError) as exc:
        out["error"] = f"could not parse system metrics: {exc}"
    return out


def probe_server(server: dict, runner, timeout: int | None = None,
                 system_runner=None) -> dict:
    """Probe one server; every failure becomes data, never an exception."""
    limit = int(timeout or server.get("timeout_seconds") or 8)
    public = public_server(server)
    payload = {
        "server": public["name"],
        "host": public["host"],
        "port": public["port"],
        "user": public["user"],
        "ok": False,
        "gpus": [],
        "warnings": [],
        "error": None,
        "duration_ms": 0,
        "command": ssh.probe_command_text(),
    }
    try:
        result = runner(server, timeout=limit)
    except Exception as exc:  # noqa: BLE001 - runner failures are per-server data
        payload["error"] = {"kind": "runner_error", "message": f"{type(exc).__name__}: {exc}"[:300]}
        return payload

    payload["duration_ms"] = result.duration_ms
    if not result.ok:
        payload["error"] = {
            "kind": result.error_kind or "ssh_error",
            "message": result.message or "the remote probe failed",
        }
        return payload

    metrics = result.stdout
    if "__ARK_GPU__\n" in metrics:
        processes, _, metrics = metrics.partition("__ARK_GPU__\n")
        payload["system"] = parse_system(processes)
    gpus, warnings = parse_nvidia_smi(metrics)
    payload["gpus"] = gpus
    payload["warnings"] = warnings
    payload["ok"] = True
    if not gpus:
        payload["warnings"] = warnings + ["nvidia-smi returned no GPU rows"]
    if system_runner is not None:
        try:
            system_result = system_runner(server, timeout=limit)
            if system_result.ok:
                payload["system"] = parse_system(system_result.stdout)
            else:
                payload["system"] = {"error": system_result.message or "system query failed"}
        except Exception as exc:  # noqa: BLE001 - CPU data must not break GPU probe
            payload["system"] = {"error": f"{type(exc).__name__}: {exc}"[:300]}
    return payload


def rank_gpus(results: list[dict]) -> list[dict]:
    """Flatten free GPUs across servers, least utilized first (the queue's order)."""
    ranked: list[dict] = []
    for result in results:
        if not result.get("ok"):
            continue
        for gpu in result.get("gpus") or []:
            score = gpu.get("score")
            free = gpu.get("free_memory_mb")
            utilization = gpu.get("utilization_pct")
            if score is None or free is None:
                continue
            if free < FREE_MEMORY_THRESHOLD_MB:
                continue
            if utilization is not None and utilization >= 100:
                continue
            ranked.append({
                "server": result.get("server"),
                "host": result.get("host"),
                "gpu_index": gpu.get("index"),
                "gpu_name": gpu.get("name"),
                "free_memory_mb": free,
                "utilization_pct": utilization,
                "score": score,
                "occupancy_pct": occupancy_pct(gpu),
                "_key": dispatch_key(gpu),
            })
    ranked.sort(key=lambda item: (item["_key"], str(item["server"])))
    for position, item in enumerate(ranked, 1):
        item.pop("_key", None)
        item["rank"] = position
    return ranked[:MAX_RANKED]
