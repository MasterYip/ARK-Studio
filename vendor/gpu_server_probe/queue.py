"""Opt-in bare-metal GPU job queue.

The probe remains read-only.  This module is the explicit execution path for
operators who opt in with ``queue.enabled: true`` in the private server list.
A single worker owns placement and launch, reserving each job's memory before
considering the next job on the same GPU.

Placement permits colocation while memory and utilization limits allow it.
``max_jobs_per_gpu: 0`` leaves the count bounded by resources; an explicit
positive limit or ``allow_colocation: false`` can restrict sharing.

Jobs are persisted in a local SQLite file next to the server list (override
with ``$ARK_GPU_QUEUE_DB``).  Remote state (pid, status, log) lives under
``queue.state_dir`` on the selected server.
"""
from __future__ import annotations

import json
import os
import re
import shlex
import sqlite3
import threading
import time
import uuid
from datetime import datetime, timezone
from pathlib import Path

from . import config as config_mod
from . import probe as probe_mod
from . import ssh as ssh_mod
from .dispatch import DispatchError, JOB_RE, _command, _env, _work_dir

JOB_STATUSES = {"queued", "starting", "running", "completed", "errored", "canceled"}
LOG_TAIL_MARKER = "__ARK_QUEUE_LOG_TAIL_V1__"


def _log_tail(value: str, lines: int = 20) -> str:
    return "\n".join(str(value or "").splitlines()[-lines:])[-8192:]


class QueueError(ValueError):
    def __init__(self, message: str, status: int = 400):
        super().__init__(message)
        self.status = status
        self.message = message


def _now() -> str:
    return datetime.now(timezone.utc).isoformat(timespec="seconds")


def _int(value, label: str, low: int, high: int, default: int) -> int:
    if value in (None, ""):
        return default
    if isinstance(value, bool):
        raise QueueError(f"{label} must be an integer")
    try:
        number = int(value)
    except (TypeError, ValueError) as exc:
        raise QueueError(f"{label} must be an integer") from exc
    if not low <= number <= high:
        raise QueueError(f"{label} must be between {low} and {high}")
    return number


def _map_path(path: str, mapping: list[dict], server_name: str = "") -> str:
    if not path:
        return path
    for item in sorted(mapping or [], key=lambda row: len(row.get("from", "")), reverse=True):
        source, target = item.get("from", ""), item.get("to", "")
        if not source:
            continue
        only = item.get("servers") or []
        if only and server_name and server_name not in only:
            continue
        if path == source or path.startswith(source + "/"):
            return target + path[len(source):]
    return path


def _map_text(text: str, mapping: list[dict], server_name: str = "") -> str:
    """Apply configured path prefixes inside a command or environment value."""
    if not text:
        return text
    for item in sorted(mapping or [], key=lambda row: len(row.get("from", "")), reverse=True):
        only = item.get("servers") or []
        if only and server_name and server_name not in only:
            continue
        text = text.replace(item.get("from", ""), item.get("to", ""))
    return text


class GpuQueue:
    def __init__(self, config_path: str | Path, *, db_path: str | Path | None = None,
                 environ: dict | None = None, probe_runner=None, command_runner=None):
        self.config_path = Path(config_path).expanduser()
        self.environ = os.environ if environ is None else environ
        override = str(self.environ.get("ARK_GPU_QUEUE_DB") or "").strip()
        self.db_path = Path(db_path or override or (str(self.config_path) + ".queue.sqlite3")).expanduser()
        self.probe_runner = probe_runner or ssh_mod.run_remote_allocation
        self.command_runner = command_runner or ssh_mod.run_remote_command
        self._lock = threading.RLock()
        self._tick_lock = threading.Lock()
        self._stop = threading.Event()
        self._thread: threading.Thread | None = None
        self._ensure_db()

    # ---------------------------------------------------------------- storage
    def _connect(self):
        self.db_path.parent.mkdir(parents=True, exist_ok=True)
        conn = sqlite3.connect(str(self.db_path), timeout=15)
        conn.row_factory = sqlite3.Row
        return conn

    def _ensure_db(self) -> None:
        with self._lock, self._connect() as conn:
            conn.execute("""CREATE TABLE IF NOT EXISTS gpu_jobs (
                id TEXT PRIMARY KEY, status TEXT NOT NULL, created_at TEXT NOT NULL,
                updated_at TEXT NOT NULL, server TEXT, gpu_indices TEXT NOT NULL DEFAULT '[]',
                job TEXT NOT NULL, project TEXT NOT NULL, owner TEXT NOT NULL,
                session TEXT NOT NULL, task TEXT NOT NULL DEFAULT '', command TEXT NOT NULL, work_dir TEXT NOT NULL,
                env_json TEXT NOT NULL, timeout_minutes INTEGER NOT NULL,
                gpu_count INTEGER NOT NULL DEFAULT 1,
                min_free_memory_mb INTEGER NOT NULL DEFAULT 1024,
                max_utilization_pct INTEGER NOT NULL DEFAULT 10,
                tags_json TEXT NOT NULL DEFAULT '[]',
                pid INTEGER, exit_code INTEGER, error TEXT NOT NULL DEFAULT '',
                log TEXT NOT NULL DEFAULT '', log_path TEXT NOT NULL DEFAULT '',
                started_at TEXT, ended_at TEXT)""")
            existing = {row[1] for row in conn.execute("PRAGMA table_info(gpu_jobs)").fetchall()}
            for name, ddl in (
                ("gpu_count", "INTEGER NOT NULL DEFAULT 1"),
                ("min_free_memory_mb", "INTEGER NOT NULL DEFAULT 1024"),
                ("max_utilization_pct", "INTEGER NOT NULL DEFAULT 10"),
                ("tags_json", "TEXT NOT NULL DEFAULT '[]'"),
                ("task", "TEXT NOT NULL DEFAULT ''"),
            ):
                if name not in existing:
                    conn.execute(f"ALTER TABLE gpu_jobs ADD COLUMN {name} {ddl}")
            conn.execute("CREATE INDEX IF NOT EXISTS gpu_jobs_status ON gpu_jobs(status, created_at)")
            conn.execute("CREATE INDEX IF NOT EXISTS gpu_jobs_project_created ON gpu_jobs(project, created_at DESC, id DESC)")
            conn.execute("CREATE INDEX IF NOT EXISTS gpu_jobs_project_status ON gpu_jobs(project, status, created_at DESC, id DESC)")
            conn.execute("CREATE INDEX IF NOT EXISTS gpu_jobs_project_name ON gpu_jobs(project, job, created_at DESC, id DESC)")
            conn.commit()
        try:
            os.chmod(self.db_path, 0o600)
        except OSError:
            pass

    def _public(self, row) -> dict:
        data = dict(row)
        try:
            data["gpu_indices"] = json.loads(data.get("gpu_indices") or "[]")
        except ValueError:
            data["gpu_indices"] = []
        try:
            data["env"] = json.loads(data.get("env_json") or "{}")
        except ValueError:
            data["env"] = {}
        try:
            data["tags"] = json.loads(data.get("tags_json") or "[]")
        except ValueError:
            data["tags"] = []
        data.pop("env_json", None)
        data.pop("tags_json", None)
        data["name"] = data.get("job", "")
        data["log_tail"] = _log_tail(data.get("log", ""))
        if data.get("exit_code") not in (None, 0) and not data.get("error"):
            data["error"] = f"remote process exited with code {data['exit_code']}"
        return data

    # ---------------------------------------------------------------- lifecycle
    def start(self) -> None:
        with self._lock:
            if self._thread and self._thread.is_alive():
                return
            self._stop.clear()
            self._thread = threading.Thread(target=self._loop, name="ark-gpu-queue", daemon=True)
            self._thread.start()

    def close(self) -> None:
        self._stop.set()
        thread = self._thread
        if thread:
            thread.join(timeout=5)
        self._thread = None

    def _loop(self) -> None:
        while not self._stop.is_set():
            try:
                self.tick()
            except Exception:  # noqa: BLE001 - a queue failure must not kill ARK
                pass
            interval = 5
            try:
                cfg = config_mod.load_config(self.config_path)
                interval = int(cfg["queue"]["poll_seconds"])
            except Exception:
                pass
            self._stop.wait(max(1, interval))

    # ---------------------------------------------------------------- validation
    def _load_config(self) -> dict:
        try:
            return config_mod.load_config(self.config_path)
        except config_mod.ConfigError as exc:
            raise QueueError(f"GPU queue config is invalid: {exc}", 422) from exc

    def _validate(self, body: dict, cfg: dict) -> dict:
        if not isinstance(body, dict):
            raise QueueError("job request must be an object")
        queue = cfg["queue"]
        if not queue.get("enabled"):
            raise QueueError("GPU queue is disabled; set queue.enabled: true in the server list", 403)
        try:
            command = _command(body.get("command"))
            work_dir = _work_dir(body.get("work_dir"))
            env = _env(body.get("env"))
        except DispatchError as exc:
            raise QueueError(str(exc)) from exc
        job = str(body.get("job") or "ark-job").strip()
        project = str(body.get("project") or "ark").strip()
        if not JOB_RE.match(job):
            raise QueueError("job must be a short label using letters, digits, dot, dash or underscore")
        if not JOB_RE.match(project):
            raise QueueError("project must be a short label using letters, digits, dot, dash or underscore")
        gpu_count = _int(body.get("gpu_count", body.get("gpus")), "gpu_count", 1, 8, 1)
        # The job's GPU-memory claim per GPU (MB).  Aliases keep requests readable.
        claim = body.get("min_free_memory_mb")
        if claim is None:
            claim = body.get("gpu_memory_mb", body.get("memory_mb"))
        min_free = _int(claim,
                        "min_free_memory_mb", 0, 1_000_000,
                        int(queue.get("min_free_memory_mb") or 1024))
        max_util = _int(body.get("max_utilization_pct"),
                        "max_utilization_pct", 0, 100,
                        int(queue.get("max_utilization_pct") or 10))
        timeout = _int(body.get("timeout_minutes"), "timeout_minutes", 1, 1440,
                       int(queue.get("default_timeout_minutes") or 240))
        server = str(body.get("server") or "").strip()
        if server and not any(item["name"] == server for item in cfg["servers"]):
            raise QueueError(f"unknown server {server!r}")
        tags = body.get("tags") or []
        if not isinstance(tags, list):
            raise QueueError("tags must be a list")
        tags = [str(tag).strip() for tag in tags if str(tag).strip()][:8]
        return {
            "id": "gpu_" + uuid.uuid4().hex[:16],
            "job": job, "project": project,
            "owner": str(body.get("owner") or "").strip()[:80],
            "session": str(body.get("session") or "").strip()[:80],
            "task": str(body.get("task") or body.get("task_ref") or "").strip()[:80],
            "command": command, "work_dir": work_dir, "env": env,
            "gpu_count": gpu_count, "min_free_memory_mb": min_free,
            "max_utilization_pct": max_util,
            "timeout_minutes": timeout, "server": server, "tags": tags,
        }

    # ---------------------------------------------------------------- public API
    def submit(self, body: dict, *, start: bool = True) -> dict:
        cfg = self._load_config()
        job = self._validate(body, cfg)
        now = _now()
        with self._lock, self._connect() as conn:
            conn.execute(
                """INSERT INTO gpu_jobs
                (id,status,created_at,updated_at,server,gpu_indices,job,project,owner,
                 session,task,command,work_dir,env_json,timeout_minutes,gpu_count,
                 min_free_memory_mb,max_utilization_pct,tags_json,error,log,log_path)
                VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)""",
                (job["id"], "queued", now, now, job["server"], "[]", job["job"],
                 job["project"], job["owner"], job["session"], job["task"],
                 job["command"], job["work_dir"], json.dumps(job["env"], ensure_ascii=False),
                 job["timeout_minutes"], job["gpu_count"], job["min_free_memory_mb"],
                 job["max_utilization_pct"], json.dumps(job["tags"]), "", "", ""))
            conn.commit()
        if start:
            self.start()
        return self.get(job["id"])

    def get(self, job_id: str) -> dict:
        with self._lock, self._connect() as conn:
            row = conn.execute("SELECT * FROM gpu_jobs WHERE id=?", (str(job_id),)).fetchone()
        if not row:
            raise QueueError("GPU job not found", 404)
        return self._public(row)

    def list(self, *, owner: str = "", project: str = "", session: str = "", limit: int = 100,
             offset: int = 0, status: str = "", name_prefix: str = "", since: str = "",
             server: str = "", name: str = "", ids: list[str] | None = None) -> dict:
        limit = _int(limit, "limit", 1, 500, 100)
        offset = _int(offset, "offset", 0, 1_000_000, 0)
        clauses, values = [], []
        for key, value in (("owner", owner), ("project", project), ("session", session),
                           ("server", server), ("job", name)):
            if value:
                if not isinstance(value, str) or len(value) > 256 or any(ord(c) < 32 for c in value):
                    raise QueueError(f"invalid {key} filter")
                clauses.append(f"{key}=?")
                values.append(value)
        if status:
            if not isinstance(status, str):
                raise QueueError("status must be comma-separated job states")
            states = list(dict.fromkeys(part.strip() for part in status.split(",") if part.strip()))
            if not states or set(states) - JOB_STATUSES:
                raise QueueError("status must contain queued, starting, running, completed, errored or canceled")
            clauses.append("status IN (" + ",".join("?" for _ in states) + ")")
            values.extend(states)
        if name_prefix:
            if not isinstance(name_prefix, str) or not JOB_RE.fullmatch(name_prefix):
                raise QueueError("name_prefix must be 1 to 64 letters, digits, dots, dashes or underscores")
            clauses.append("job GLOB ?")
            values.append(name_prefix + "*")
        if since:
            try:
                text = since.replace("Z", "+00:00")
                # Python 3.10 only accepts three or six fractional digits.
                text = re.sub(r"\.(\d{1,6})(?=[+-]\d{2}:\d{2}$)",
                              lambda match: "." + match[1].ljust(6, "0"), text)
                stamp = datetime.fromisoformat(text)
                if stamp.tzinfo is None:
                    raise ValueError
                stamp = stamp.astimezone(timezone.utc).isoformat()
            except (ValueError, TypeError, AttributeError) as exc:
                raise QueueError("since must be an ISO timestamp with a timezone") from exc
            clauses.append("created_at>=?")
            values.append(stamp)
        if ids is not None:
            if (not isinstance(ids, list) or not 1 <= len(ids) <= 500
                    or any(not isinstance(value, str) or not re.fullmatch(r"gpu_[A-Za-z0-9_-]{1,64}", value) for value in ids)):
                raise QueueError("ids must contain 1 to 500 GPU job IDs")
            clauses.append("id IN (" + ",".join("?" for _ in ids) + ")")
            values.extend(ids)
        where = (" WHERE " + " AND ".join(clauses)) if clauses else ""
        with self._lock, self._connect() as conn:
            conn.execute("BEGIN")
            total = conn.execute(f"SELECT COUNT(*) FROM gpu_jobs{where}", values).fetchone()[0]
            rows = conn.execute(
                f"SELECT * FROM gpu_jobs{where} ORDER BY created_at DESC, id DESC LIMIT ? OFFSET ?",
                (*values, limit, offset)).fetchall()
        has_more = offset + len(rows) < total
        return {"jobs": [self._public(row) for row in rows],
                "total": total, "count": len(rows), "limit": limit, "offset": offset,
                "has_more": has_more, "next_offset": offset + len(rows) if has_more else None,
                "queue_enabled": bool(self._load_config()["queue"]["enabled"])}

    def cancel(self, job_id: str) -> dict:
        job = self.get(job_id)
        if job["status"] in {"completed", "errored", "canceled"}:
            return job
        cfg = self._load_config()
        server = next((item for item in cfg["servers"] if item["name"] == job.get("server")), None)
        if server and job.get("pid"):
            ssh_mod.run_remote_command(
                server, f"kill -TERM -{int(job['pid'])} 2>/dev/null || "
                        f"kill -TERM {int(job['pid'])} 2>/dev/null || true", timeout=10)
        now = _now()
        with self._lock, self._connect() as conn:
            conn.execute("UPDATE gpu_jobs SET status='canceled', updated_at=?, ended_at=?, error=? WHERE id=?",
                         (now, now, "canceled by user", str(job_id)))
            conn.commit()
        return self.get(job_id)

    def logs(self, job_id: str, limit: int = 200) -> dict:
        limit = _int(limit, "tail", 1, 500, 200)
        job = self.get(job_id)
        cached = {"log": _log_tail(job.get("log", ""), limit), "path": job.get("log_path") or "", "cached": True}
        if not job.get("server") or not job.get("log_path"):
            return cached
        cfg = self._load_config()
        server = next((item for item in cfg["servers"] if item["name"] == job["server"]), None)
        if not server:
            return cached
        result = self.command_runner(
            server, f"tail -n {limit} {shlex.quote(job['log_path'])} 2>/dev/null | tail -c 65536",
            timeout=15)
        if result.ok and result.stdout:
            return {"log": result.stdout[-65536:], "path": job["log_path"], "cached": False}
        return cached

    # ---------------------------------------------------------------- worker
    def _active_by_gpu(self, conn) -> dict[tuple[str, int], int]:
        active = {}
        rows = conn.execute("SELECT server, gpu_indices FROM gpu_jobs WHERE status IN ('starting','running')").fetchall()
        for row in rows:
            try:
                indices = json.loads(row["gpu_indices"] or "[]")
            except ValueError:
                indices = []
            for index in indices:
                key = (str(row["server"] or ""), int(index))
                active[key] = active.get(key, 0) + 1
        return active

    def _reserved_by_gpu(self, conn, results=()) -> dict[tuple[str, int], int]:
        """Reserve the unobserved part of each live job's per-GPU memory claim.

        A running shell may not have initialized CUDA yet. Only subtract memory
        attributed to that job; another user's process cannot satisfy its claim.
        Missing process attribution conservatively retains the full claim.
        """
        observed = {}
        for result in results:
            for process in (result.get("system") or {}).get("processes") or []:
                if not process.get("job_id") or process.get("gpu_index") is None:
                    continue
                key = (result["server"], int(process["gpu_index"]), process["job_id"])
                observed[key] = observed.get(key, 0) + max(0, int(process.get("memory_mb") or 0))
        reserved: dict[tuple[str, int], int] = {}
        rows = conn.execute("SELECT id, server, gpu_indices, min_free_memory_mb FROM gpu_jobs "
                            "WHERE status IN ('starting','running')").fetchall()
        for row in rows:
            try:
                indices = json.loads(row["gpu_indices"] or "[]")
            except ValueError:
                indices = []
            for index in indices:
                key = (str(row["server"] or ""), int(index))
                remaining = max(0, int(row["min_free_memory_mb"] or 0)
                                - observed.get((*key, row["id"]), 0))
                reserved[key] = reserved.get(key, 0) + remaining
        return reserved

    def _probe(self, cfg: dict, job: dict) -> dict:
        servers = [item for item in cfg["servers"] if item.get("enabled", True)]
        if job.get("server"):
            servers = [item for item in servers if item["name"] == job["server"]]
        if job.get("tags"):
            servers = [item for item in servers if set(job["tags"]).issubset(set(item.get("tags") or []))]
        results = []
        for server in servers:
            result = probe_mod.probe_server(server, self.probe_runner, timeout=server.get("timeout_seconds"))
            results.append(result)
        return results

    def _select(self, job: dict, cfg: dict, results: list[dict], active: dict,
                reserved: dict | None = None) -> tuple[dict, list[dict]] | None:
        """Dispatch rule: keep only GPUs that can hold the job's memory claim
        (free memory minus unobserved memory claimed by live jobs), then take
        the least-utilized ones.  Ties go to more free memory, then lower index."""
        queue = cfg["queue"]
        reserved = reserved or {}
        claim = int(job["min_free_memory_mb"])
        best = None
        for result in results:
            if not result.get("ok"):
                continue
            if job.get("server") and result.get("server") != job["server"]:
                continue
            server = next((item for item in cfg["servers"] if item["name"] == result["server"]), None)
            if not server:
                continue
            if job.get("tags") and not set(job["tags"]).issubset(set(server.get("tags") or [])):
                continue
            candidates = []
            for gpu in result.get("gpus") or []:
                index = int(gpu["index"])
                free = gpu.get("free_memory_mb")
                util = gpu.get("utilization_pct")
                if free is None or util is None:
                    continue
                key = (server["name"], index)
                available = float(free) - reserved.get(key, 0)
                if available < claim:
                    continue
                if util > job["max_utilization_pct"]:
                    continue
                used = active.get(key, 0)
                limit = int(queue.get("max_jobs_per_gpu", 0))
                if limit and used >= limit:
                    continue
                if not queue.get("allow_colocation", True) and used:
                    continue
                candidates.append(((float(util), -available, index), gpu))
            candidates.sort(key=lambda item: item[0])
            selected = candidates[:job["gpu_count"]]
            if len(selected) < job["gpu_count"]:
                continue
            # Across servers: the busiest GPU the job would get, then its free memory.
            worst = max(item[0] for item in selected)
            score = (worst, server["name"])
            if best is None or score < best[0]:
                best = (score, server, [item[1] for item in selected])
        return (best[1], best[2]) if best else None

    def _script(self, cfg: dict, job: dict, gpus: list[dict], server_name: str) -> tuple[str, str]:
        queue = cfg["queue"]
        state = queue["state_dir"].rstrip("/") + "/" + job["id"]
        status_path = state + "/status"
        env = dict(queue.get("env") or {})
        env.update(job.get("env") or {})
        env = {key: _map_text(str(value), queue.get("path_map") or [], server_name)
               for key, value in env.items()}
        env["CUDA_VISIBLE_DEVICES"] = ",".join(str(gpu["index"]) for gpu in gpus)
        env["ARK_GPU_JOB_ID"] = job["id"]
        env["ARK_GPU_JOB_STATUS"] = status_path
        lines = ["#!/usr/bin/env bash", "set -o pipefail"]
        if queue.get("setup"):
            lines.append(_map_text(queue["setup"], queue.get("path_map") or [], server_name))
        work_dir = _map_path(job.get("work_dir") or "", queue.get("path_map") or [], server_name)
        if work_dir:
            lines.append(f"cd {shlex.quote(work_dir)} || exit 86")
        lines.append("export " + " ".join(
            f"{key}={shlex.quote(str(value))}" for key, value in sorted(env.items())))
        lines.append(_map_text(job["command"], queue.get("path_map") or [], server_name))
        lines.append("code=$?")
        lines.append("echo \"$code\" > \"$ARK_GPU_JOB_STATUS\"")
        lines.append("exit \"$code\"")
        return state, "\n".join(lines) + "\n"

    def _launch(self, job: dict, server: dict, gpus: list[dict], cfg: dict) -> None:
        state, script = self._script(cfg, job, gpus, server["name"])
        log_path = state + "/job.log"
        pid_path = state + "/pid"
        launch_path = state + "/launch.sh"
        launcher = (
            "#!/usr/bin/env bash\n"
            f"echo $$ > {shlex.quote(pid_path)}\n"
            f"exec bash {shlex.quote(state)}/job.sh\n"
        )
        remote = (
            f"mkdir -p {shlex.quote(state)} && "
            f"printf '%s' {shlex.quote(script)} > {shlex.quote(state)}/job.sh && "
            f"printf '%s' {shlex.quote(launcher)} > {shlex.quote(launch_path)} && "
            f"chmod +x {shlex.quote(state)}/job.sh {shlex.quote(launch_path)} && "
            # setsid -f forks and returns immediately; the launcher writes its
            # own PID before exec, so SSH does not wait for the training process
            # and the queue can still kill the whole process group.
            f"setsid -f bash {shlex.quote(launch_path)} </dev/null > {shlex.quote(log_path)} 2>&1; "
            f"for i in $(seq 1 50); do [ -s {shlex.quote(pid_path)} ] && break; sleep 0.1; done; "
            f"cat {shlex.quote(pid_path)} 2>/dev/null || true"
        )
        result = self.command_runner(server, remote, timeout=20)
        pid = 0
        for token in (result.stdout or "").split():
            if token.isdigit():
                pid = int(token)
        if not result.ok or not pid:
            now = _now()
            with self._lock, self._connect() as conn:
                conn.execute("""UPDATE gpu_jobs SET status='errored', updated_at=?, ended_at=?,
                              error=?, log_path=?, log=? WHERE id=?""",
                             (now, now, (result.message or "could not launch remote job")[:500],
                              log_path, _log_tail((result.stdout or "") + "\n" + (result.stderr or "")), job["id"]))
                conn.commit()
            return
        now = _now()
        with self._lock, self._connect() as conn:
            conn.execute("""UPDATE gpu_jobs SET status='running', updated_at=?, pid=?,
                          started_at=?, log_path=? WHERE id=?""",
                         (now, pid, now, log_path, job["id"]))
            conn.commit()

    def _reap(self, cfg: dict) -> None:
        with self._lock, self._connect() as conn:
            rows = conn.execute("SELECT * FROM gpu_jobs WHERE status='running'").fetchall()
        for row in rows:
            job = self._public(row)
            server = next((item for item in cfg["servers"] if item["name"] == job.get("server")), None)
            if not server or not job.get("pid"):
                continue
            state = cfg["queue"]["state_dir"].rstrip("/") + "/" + job["id"]
            status_path = shlex.quote(state + "/status")
            failure_tail = (f"printf '\\n{LOG_TAIL_MARKER}\\n'; "
                            f"tail -n 20 {shlex.quote(state + '/job.log')} 2>/dev/null | tail -c 8192")
            command = (
                f"if [ -f {status_path} ]; then code=$(cat {status_path}); echo DONE:$code; "
                f"if [ \"$code\" != 0 ]; then {failure_tail}; fi; "
                f"elif kill -0 {int(job['pid'])} 2>/dev/null; then echo RUNNING; "
                f"else echo LOST; {failure_tail}; fi"
            )
            result = self.command_runner(server, command, timeout=12)
            if not result.ok:
                continue
            output, _, log = (result.stdout or "").partition(LOG_TAIL_MARKER)
            output = output.strip()
            log = _log_tail(log.strip())
            code = None
            if output.startswith("DONE:"):
                try:
                    code = int(output.split(":", 1)[1].strip())
                except ValueError:
                    code = None
            if code is None and output == "RUNNING":
                # Hard runtime guard even if the remote status file was lost.
                started = job.get("started_at") or ""
                try:
                    elapsed = (datetime.now(timezone.utc)
                               - datetime.fromisoformat(started)).total_seconds()
                except (TypeError, ValueError):
                    elapsed = 0
                if elapsed > int(job["timeout_minutes"]) * 60:
                    ssh_mod.run_remote_command(
                        server, f"kill -TERM -{int(job['pid'])} 2>/dev/null || "
                                f"kill -TERM {int(job['pid'])} 2>/dev/null || true", timeout=10)
                    code = -1
                else:
                    continue
            if code is None:
                code = -1
            now = _now()
            status = "completed" if code == 0 else "errored"
            error = "" if status == "completed" else f"remote process exited with code {code}"
            if error and log:
                error += ": " + log.splitlines()[-1]
            with self._lock, self._connect() as conn:
                conn.execute("""UPDATE gpu_jobs SET status=?, exit_code=?, updated_at=?,
                              ended_at=?, error=?, log=? WHERE id=? AND status='running'""",
                             (status, code, now, now, error[:500], log, job["id"]))
                conn.commit()

    def tick(self) -> None:
        if not self._tick_lock.acquire(blocking=False):
            return
        try:
            cfg = self._load_config()
            if not cfg["queue"].get("enabled"):
                return
            self._reap(cfg)
            with self._lock, self._connect() as conn:
                rows = conn.execute("SELECT * FROM gpu_jobs WHERE status='queued' ORDER BY created_at LIMIT 32").fetchall()
            if not rows:
                return
            results = self._probe(cfg, {})
            with self._lock, self._connect() as conn:
                active = self._active_by_gpu(conn)
                reserved = self._reserved_by_gpu(conn, results)
            for row in rows:
                job = self._public(row)
                selection = self._select(job, cfg, results, active, reserved)
                if not selection:
                    continue
                server, gpus = selection
                now = _now()
                with self._lock, self._connect() as conn:
                    conn.execute("""UPDATE gpu_jobs SET status='starting', server=?, gpu_indices=?,
                                  updated_at=?, started_at=? WHERE id=? AND status='queued'""",
                                 (server["name"], json.dumps([gpu["index"] for gpu in gpus]),
                                  now, now, job["id"]))
                    conn.commit()
                # Reserve in the in-memory view before the next job is considered.
                for gpu in gpus:
                    key = (server["name"], int(gpu["index"]))
                    active[key] = active.get(key, 0) + 1
                    reserved[key] = reserved.get(key, 0) + int(job["min_free_memory_mb"])
                self._launch(job, server, gpus, cfg)
        finally:
            self._tick_lock.release()
