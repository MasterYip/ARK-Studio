"""ARK-facing GPU server probe capability.

This module is the adapter between ARK's HTTP API/UI and the vendored
``vendor/gpu_server_probe`` tool. It is inert until an operator opts in:

* the vendor package and a server list are both optional; a missing vendor or
  config never blocks a project or any other ARK feature;
* probing is disabled by default and requires ``enabled: true`` in the private
  server list or ``ARK_GPU_PROBE=1`` in ARK's environment;
* probes run on request; an explicitly enabled queue resumes its worker on startup;
* dispatch preparation is dry-run only; the separately enabled queue executes jobs;
* public payloads are built from an allowlist, and the config schema rejects
  passwords, keys, tokens and identity files outright.

Configure ``$ARK_GPU_SERVERS_CONFIG`` or ``$ARK_PRIVATE_DIR/gpu_servers.yaml``
(default ``~/.ark/gpu_servers.yaml``). The config stays outside the repository
and outside project folders.
"""
from __future__ import annotations

import importlib
import os
import sys
import threading
from concurrent.futures import ThreadPoolExecutor
from datetime import datetime, timezone
from pathlib import Path

VENDOR_PACKAGE = "gpu_server_probe"
VENDOR_RELATIVE = Path("vendor") / VENDOR_PACKAGE
EXAMPLE_RELATIVE = VENDOR_RELATIVE / "servers_rp.example.yaml"
DEFAULT_CONFIG_NAME = "gpu_servers.yaml"
MIN_TIMEOUT_SECONDS = 2
MAX_TIMEOUT_SECONDS = 30
MAX_PARALLEL_PROBES = 8


def _load_vendor():
    """Import the vendored adapter relative to the ARK checkout.

    Returns ``None`` when the vendor directory is absent or unreadable, which
    is a supported state (the capability reports itself unavailable).
    """
    root = Path(__file__).resolve().parent.parent
    for base in (root, root / "vendor"):
        package_dir = base / VENDOR_PACKAGE
        if not (package_dir / "__init__.py").is_file():
            continue
        entry = str(base)
        if entry not in sys.path:
            sys.path.insert(0, entry)
        try:
            module = importlib.import_module(VENDOR_PACKAGE)
        except Exception:  # pragma: no cover - a broken vendor must not break ARK
            continue
        module_file = Path(getattr(module, "__file__", "") or "").resolve()
        if module_file.parent != package_dir.resolve():
            continue
        return module
    return None


VENDOR = _load_vendor()


class GpuError(Exception):
    """A clean, user-facing GPU capability error."""

    def __init__(self, status: int, message: str, code: str = "gpu_error"):
        super().__init__(message)
        self.status = status
        self.message = message
        self.code = code


def _truthy(value) -> bool:
    return str(value or "").strip().lower() in {"1", "true", "yes", "on"}


def _now() -> str:
    return datetime.now(timezone.utc).isoformat(timespec="seconds")


def _names(raw) -> list[str]:
    if raw in (None, "", []):
        return []
    if isinstance(raw, str):
        values = [part.strip() for part in raw.split(",")]
    elif isinstance(raw, (list, tuple)):
        values = [str(part).strip() for part in raw]
    else:
        raise GpuError(400, "servers must be a name, comma-separated string or list", "invalid_request")
    names: list[str] = []
    for value in values:
        if value and value not in names:
            names.append(value)
    return names


class GpuService:
    """Stateless GPU capability service (config is re-read on every call)."""

    def __init__(self, config_path: str | Path | None = None, *, runner=None,
                 system_runner=None, environ: dict | None = None):
        self._config_path = Path(config_path).expanduser() if config_path else None
        self._runner = runner
        self._system_runner = system_runner
        self._environ = environ
        self._queue = None
        self._queue_lock = threading.Lock()
        self._queue_start_error = None
        self.last_probe_summary: dict | None = None
        # The last explicit probe (servers with GPUs/system data); read by the
        # resources graph, which never probes on its own.
        self.last_probe: dict | None = None

    # ---------------------------------------------------------------- config
    @property
    def env(self) -> dict:
        return os.environ if self._environ is None else self._environ

    def config_path(self) -> Path:
        if self._config_path is not None:
            return self._config_path
        override = str(self.env.get("ARK_GPU_SERVERS_CONFIG") or "").strip()
        if override:
            return Path(override).expanduser()
        private = str(self.env.get("ARK_PRIVATE_DIR") or "").strip()
        base = Path(private).expanduser() if private else Path.home() / ".ark"
        return base / DEFAULT_CONFIG_NAME

    def _inspect(self) -> dict:
        path = self.config_path()
        present = path.is_file()
        config = None
        error = ""
        if VENDOR is not None and present:
            try:
                config = VENDOR.config.load_config(path)
            except VENDOR.config.ConfigError as exc:
                error = str(exc)
        return {"path": path, "present": present, "config": config, "error": error}

    def _enabled(self, config) -> bool:
        if VENDOR is None or config is None:
            return False
        return _truthy(self.env.get("ARK_GPU_PROBE")) or bool(config.get("enabled"))

    def _require_enabled(self) -> tuple[dict, Path]:
        if VENDOR is None:
            raise GpuError(
                503,
                "The vendored GPU probe adapter is not installed. Restore "
                f"{VENDOR_RELATIVE.as_posix()}/ or update ARK to enable this panel.",
                "vendor_missing",
            )
        state = self._inspect()
        path = state["path"]
        if not state["present"]:
            raise GpuError(
                409,
                f"No GPU server list found at {path}. Copy "
                f"{EXAMPLE_RELATIVE.as_posix()} there (hosts and ports only) and set "
                "enabled: true.",
                "config_missing",
            )
        if state["error"]:
            raise GpuError(422, f"GPU server list is invalid: {state['error']}", "config_invalid")
        config = state["config"]
        if not self._enabled(config):
            raise GpuError(
                403,
                "GPU probing is disabled by default. Set 'enabled: true' in "
                f"{path} or start ARK with ARK_GPU_PROBE=1, then reload.",
                "disabled",
            )
        return config, path

    # ---------------------------------------------------------------- status
    def status(self) -> dict:
        state = self._inspect()
        ssh_status = (VENDOR.ssh.dependency_status(self.env) if VENDOR is not None
                      else {"available": False, "binary": "", "hint": ""})
        vendor = None
        if VENDOR is not None:
            vendor = {
                "name": VENDOR_PACKAGE,
                "version": str(getattr(VENDOR, "__version__", "") or ""),
                "path": VENDOR_RELATIVE.as_posix(),
            }
        return {
            "available": VENDOR is not None,
            "enabled": self._enabled(state["config"]),
            "enabled_source": ("environment" if _truthy(self.env.get("ARK_GPU_PROBE"))
                               else ("config" if state["config"] and state["config"].get("enabled")
                                     else "default-off")),
            "vendor": vendor,
            "config_path": str(state["path"]),
            "config_present": state["present"],
            "config_error": state["error"] or None,
            "server_count": len(state["config"]["servers"]) if state["config"] else 0,
            "defaults": state["config"]["defaults"] if state["config"] else None,
            "ssh": ssh_status,
            "asyncssh": {
                "available": bool(VENDOR.ssh.asyncssh_available()) if VENDOR is not None else False,
                "required": False,
                "note": "asyncssh is optional; ARK uses the OpenSSH client.",
            },
            "how_to_enable": [
                f"Copy {EXAMPLE_RELATIVE.as_posix()} to {state['path']} and edit hosts.",
                "Set 'enabled: true' in that file, or start ARK with ARK_GPU_PROBE=1.",
                "Keep the file outside the repository; authentication uses your SSH "
                "agent or ~/.ssh/config.",
            ],
            "security": {
                "credentials": "not supported or stored; the schema rejects passwords, "
                               "keys, tokens and identity files",
                "remote_commands": "read-only nvidia-smi probe by default; dispatch "
                                   "preparation is dry-run; the opt-in queue may execute "
                                   "allocated jobs over SSH",
                "storage": "user-level private config; never inside a project folder",
            },
            "checked_at": _now(),
        }

    def servers(self) -> dict:
        """List configured servers (public fields only). Never probes."""
        state = self._inspect()
        enabled = self._enabled(state["config"])
        payload = {
            "available": VENDOR is not None,
            "enabled": enabled,
            "config_path": str(state["path"]),
            "config_present": state["present"],
            "config_error": state["error"] or None,
            "servers": [],
            "count": 0,
            "checked_at": _now(),
        }
        if enabled and state["config"] is not None:
            payload["servers"] = [
                VENDOR.config.public_server(server) for server in state["config"]["servers"]
            ]
            payload["count"] = len(payload["servers"])
            payload["defaults"] = state["config"]["defaults"]
        elif VENDOR is None:
            payload["hint"] = "The vendored GPU probe adapter is not installed."
        elif not state["present"]:
            payload["hint"] = "No server list configured yet."
        elif state["error"]:
            payload["hint"] = "The server list is invalid; fix config_error and reload."
        elif not enabled:
            payload["hint"] = "GPU probing is disabled by default; set enabled: true."
        return payload

    # ----------------------------------------------------------------- probe
    def probe(self, body: dict | None = None) -> dict:
        """Probe selected servers over SSH (explicit request only).

        Failures are per-server data: one unreachable host does not abort the
        others, and no exception escapes for ordinary SSH/network errors.
        """
        config, path = self._require_enabled()
        body = body if isinstance(body, dict) else {}
        requested = _names(body.get("servers") if "servers" in body else body.get("server"))
        all_servers = config["servers"]
        if not all_servers:
            raise GpuError(409, "No GPU servers are configured in the server list.", "no_servers")

        if requested:
            known = {server["name"] for server in all_servers}
            unknown = [name for name in requested if name not in known]
            if unknown:
                raise GpuError(400, f"Unknown server(s): {', '.join(unknown)}", "unknown_server")
            targets = [server for server in all_servers if server["name"] in requested]
        else:
            targets = list(all_servers)

        timeout_override = self._timeout(body.get("timeout_seconds"))
        active = [server for server in targets if server.get("enabled", True)]
        skipped = [server["name"] for server in targets if not server.get("enabled", True)]
        if not active:
            raise GpuError(409, "All selected servers are disabled in the server list.", "no_servers")

        runner = self._runner or VENDOR.ssh.run_remote
        system_runner = self._system_runner
        if system_runner is None and self._runner is None:
            system_runner = VENDOR.ssh.run_remote_system

        def run_one(server: dict) -> dict:
            limit = min(
                int(server.get("timeout_seconds") or 8),
                timeout_override or MAX_TIMEOUT_SECONDS,
            )
            return VENDOR.probe.probe_server(
                server, runner, timeout=max(MIN_TIMEOUT_SECONDS, limit),
                system_runner=system_runner)

        with ThreadPoolExecutor(max_workers=min(MAX_PARALLEL_PROBES, len(active))) as pool:
            results = list(pool.map(run_one, active))

        ranked = VENDOR.probe.rank_gpus(results)
        ok_count = sum(1 for result in results if result["ok"])
        # Home shows free GPUs from the last explicit probe; it never probes.
        self.last_probe_summary = {
            "probed_at": _now(), "free_gpus": len(ranked),
            "gpus": sum(len(result.get("gpus") or []) for result in results)}
        payload = {
            "enabled": True,
            "read_only": True,
            "config_path": str(path),
            "probed_at": _now(),
            "timeout_seconds": timeout_override,
            "servers": results,
            "skipped": skipped,
            "ranked": ranked,
            "summary": {
                "configured": len(targets),
                "probed": len(results),
                "ok": ok_count,
                "failed": len(results) - ok_count,
                "gpus": sum(len(result.get("gpus") or []) for result in results),
                "free_gpus": len(ranked),
            },
        }
        self.last_probe = {"probed_at": payload["probed_at"], "servers": results}
        return payload

    def dashboard(self, *, project: str = "") -> dict:
        """One read-only monitoring payload for the Tasks GPU panel."""
        probe = self.probe({})
        jobs = self.queue_list(project=project, limit=200) if project else self.queue_list(limit=200)
        return {
            "checked_at": _now(),
            "probe": probe,
            "jobs": jobs.get("jobs", []),
            "queue_enabled": jobs.get("queue_enabled", False),
        }

    def _timeout(self, raw) -> int | None:
        if raw in (None, ""):
            return None
        if isinstance(raw, bool):
            raise GpuError(400, "timeout_seconds must be an integer", "invalid_request")
        try:
            value = int(raw)
        except (TypeError, ValueError) as exc:
            raise GpuError(400, "timeout_seconds must be an integer", "invalid_request") from exc
        if not MIN_TIMEOUT_SECONDS <= value <= MAX_TIMEOUT_SECONDS:
            raise GpuError(
                400,
                f"timeout_seconds must be between {MIN_TIMEOUT_SECONDS} and {MAX_TIMEOUT_SECONDS}",
                "invalid_request",
            )
        return value

    # ------------------------------------------------------------------ queue
    def start_configured_queue(self) -> bool:
        state = self._inspect()
        config = state["config"]
        if not self._enabled(config) or not (config.get("queue") or {}).get("enabled"):
            return False
        try:
            self._queue_service()
        except Exception as exc:  # a broken optional queue must not take down ARK
            self._queue_start_error = str(exc)
            return False
        self._queue_start_error = None
        return True

    def _queue_service(self):
        if VENDOR is None or not hasattr(VENDOR, "queue"):
            raise GpuError(503, "The vendored GPU queue module is not installed.",
                           "vendor_missing")
        with self._queue_lock:
            if self._queue is None:
                queue = VENDOR.queue.GpuQueue(self.config_path(), environ=self.env)
                queue.start()
                self._queue = queue
        return self._queue

    def queue_status(self) -> dict:
        state = self._inspect()
        queue = (state["config"] or {}).get("queue") if state["config"] else None
        return {
            "available": VENDOR is not None and hasattr(VENDOR, "queue"),
            "enabled": bool(queue and queue.get("enabled")),
            "worker_started": self._queue is not None,
            "worker_error": self._queue_start_error,
            "config_path": str(state["path"]),
            "config_present": state["present"],
            "config_error": state["error"] or None,
            "policy": {
                "allow_colocation": bool(queue.get("allow_colocation")) if queue else False,
                "max_jobs_per_gpu": queue.get("max_jobs_per_gpu") if queue else None,
                "max_utilization_pct": queue.get("max_utilization_pct") if queue else None,
                "min_free_memory_mb": queue.get("min_free_memory_mb") if queue else None,
                "path_map": queue.get("path_map") if queue else [],
            },
            "checked_at": _now(),
        }

    def queue_submit(self, body: dict | None = None, *, owner: str = "",
                     project: str = "", session: str = "", task: str = "") -> dict:
        payload = dict(body or {})
        payload["owner"] = owner
        payload["project"] = project
        payload["session"] = session
        if task:
            payload["task"] = task
        try:
            return self._queue_service().submit(payload)
        except VENDOR.queue.QueueError as exc:
            raise GpuError(exc.status, exc.message, "queue_invalid") from exc

    def queue_list(self, *, owner: str = "", project: str = "", limit: int = 100, **filters) -> dict:
        try:
            return self._queue_service().list(owner=owner, project=project, limit=limit, **filters)
        except VENDOR.queue.QueueError as exc:
            raise GpuError(exc.status, exc.message, "queue_invalid") from exc

    def queue_get(self, job_id: str) -> dict:
        try:
            return self._queue_service().get(job_id)
        except VENDOR.queue.QueueError as exc:
            raise GpuError(exc.status, exc.message, "queue_not_found") from exc

    def queue_cancel(self, job_id: str) -> dict:
        try:
            return self._queue_service().cancel(job_id)
        except VENDOR.queue.QueueError as exc:
            raise GpuError(exc.status, exc.message, "queue_not_found") from exc

    def queue_logs(self, job_id: str, limit: int = 200) -> dict:
        try:
            return self._queue_service().logs(job_id, limit=limit)
        except VENDOR.queue.QueueError as exc:
            raise GpuError(exc.status, exc.message, "queue_not_found") from exc

    def close(self) -> None:
        if self._queue is not None:
            self._queue.close()
            self._queue = None

    # -------------------------------------------------------------- dispatch
    def prepare_dispatch(self, body: dict | None = None) -> dict:
        """Validate and render a bounded dispatch request; never executes it."""
        config, path = self._require_enabled()
        request_body = body if isinstance(body, dict) else {}
        try:
            request = VENDOR.dispatch.build_request(request_body, config["servers"])
        except VENDOR.dispatch.DispatchError as exc:
            raise GpuError(400, str(exc), "dispatch_invalid") from exc
        request["config_path"] = str(path)
        request["prepared_by"] = "ARK Studio GPU adapter"
        return request
