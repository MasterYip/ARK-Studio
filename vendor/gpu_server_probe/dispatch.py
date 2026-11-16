"""Bounded, dry-run-only job dispatch requests.

ARK must never launch a remote job implicitly. This module validates a small
job descriptor and renders a reviewable request plus an illustrative command.
It contains no execution path: no subprocess, no SSH write, no scheduler call.
"""
from __future__ import annotations

import re
import shlex
from datetime import datetime, timezone

from .config import public_server

MAX_GPUS = 8
MAX_GPU_INDEX = 15
MAX_COMMAND_CHARS = 16384
MAX_TIMEOUT_MINUTES = 1440
MAX_ENV_VARS = 16
JOB_RE = re.compile(r"^[A-Za-z0-9._-]{1,64}$")
ENV_NAME_RE = re.compile(r"^[A-Za-z_][A-Za-z0-9_]{0,63}$")


class DispatchError(ValueError):
    """The requested dispatch descriptor is invalid or out of bounds."""


def _server_by_name(servers: list[dict], name: str) -> dict:
    for server in servers:
        if server["name"] == name:
            return server
    known = ", ".join(server["name"] for server in servers) or "none configured"
    raise DispatchError(f"unknown server {name!r}; configured servers: {known}")


def _gpus(raw) -> list[int]:
    if raw is None or raw == "":
        return []
    if isinstance(raw, str):
        tokens = [token for token in re.split(r"[,\s]+", raw.strip()) if token]
    elif isinstance(raw, (list, tuple)):
        tokens = list(raw)
    else:
        raise DispatchError("gpus must be a comma-separated string or a list of indices")
    indices: list[int] = []
    for token in tokens:
        if isinstance(token, bool):
            raise DispatchError("gpus must contain integer GPU indices")
        try:
            index = int(token)
        except (TypeError, ValueError) as exc:
            raise DispatchError(f"invalid GPU index {token!r}") from exc
        if not 0 <= index <= MAX_GPU_INDEX:
            raise DispatchError(f"GPU index {index} must be between 0 and {MAX_GPU_INDEX}")
        if index in indices:
            raise DispatchError(f"duplicate GPU index {index}")
        indices.append(index)
    if len(indices) > MAX_GPUS:
        raise DispatchError(f"at most {MAX_GPUS} GPUs can be requested")
    return indices


def _work_dir(raw) -> str:
    if raw in (None, ""):
        return ""
    if not isinstance(raw, str):
        raise DispatchError("work_dir must be a string")
    path = raw.strip()
    if len(path) > 240:
        raise DispatchError("work_dir must be at most 240 characters")
    if not path.startswith("/"):
        raise DispatchError("work_dir must be an absolute POSIX path")
    if ".." in path.split("/"):
        raise DispatchError("work_dir must not contain '..'")
    if any(ord(char) < 32 for char in path):
        raise DispatchError("work_dir must not contain control characters")
    return path


def _command(raw) -> str:
    if not isinstance(raw, str):
        raise DispatchError("command is required")
    command = raw.strip()
    if not command:
        raise DispatchError("command is required")
    if len(command) > MAX_COMMAND_CHARS:
        raise DispatchError(f"command must be at most {MAX_COMMAND_CHARS} characters")
    if "\x00" in command:
        raise DispatchError("command must not contain NUL bytes")
    if any(ord(char) < 32 and char not in "\n\t" for char in command):
        raise DispatchError("command must not contain control characters")
    return command


def _env(raw) -> dict[str, str]:
    if raw in (None, ""):
        return {}
    if not isinstance(raw, dict):
        raise DispatchError("env must be an object")
    if len(raw) > MAX_ENV_VARS:
        raise DispatchError(f"env can have at most {MAX_ENV_VARS} entries")
    env: dict[str, str] = {}
    for key, value in raw.items():
        if not ENV_NAME_RE.match(str(key)):
            raise DispatchError(f"invalid environment variable name {key!r}")
        if isinstance(value, bool):
            value = "1" if value else "0"
        elif isinstance(value, (int, float)):
            value = str(value)
        elif not isinstance(value, str):
            raise DispatchError(f"env value for {key!r} must be a string")
        if len(value) > 200 or "\x00" in value:
            raise DispatchError(f"env value for {key!r} is too long or contains NUL")
        env[str(key)] = value
    return env


def build_request(body: dict, servers: list[dict]) -> dict:
    """Validate ``body`` and return a request that is explicitly not executed."""
    if not isinstance(body, dict):
        raise DispatchError("dispatch request must be an object")
    if body.get("execute") or body.get("launch"):
        raise DispatchError(
            "ARK prepares dispatch requests only; it never executes remote jobs. "
            "Use the gpu_server_probe CLI or your scheduler with the returned request."
        )
    if not servers:
        raise DispatchError("no GPU servers are configured")

    name = str(body.get("server") or "").strip()
    if not name:
        if len(servers) == 1:
            name = servers[0]["name"]
        else:
            raise DispatchError(
                "server is required; configured servers: "
                + ", ".join(server["name"] for server in servers)
            )
    server = _server_by_name(servers, name)
    if not server.get("enabled", True):
        raise DispatchError(f"server {name!r} is disabled in the server list")

    gpus = _gpus(body.get("gpus", body.get("gpu_indices")))
    timeout_minutes = body.get("timeout_minutes", 60)
    if isinstance(timeout_minutes, bool):
        raise DispatchError("timeout_minutes must be an integer")
    try:
        timeout_minutes = int(timeout_minutes)
    except (TypeError, ValueError) as exc:
        raise DispatchError("timeout_minutes must be an integer") from exc
    if not 1 <= timeout_minutes <= MAX_TIMEOUT_MINUTES:
        raise DispatchError(f"timeout_minutes must be between 1 and {MAX_TIMEOUT_MINUTES}")

    job = str(body.get("job") or "ark-job").strip()
    if not JOB_RE.match(job):
        raise DispatchError("job must be a short label using letters, digits, dot, dash or underscore")
    project = str(body.get("project") or "ark").strip()
    if not JOB_RE.match(project):
        raise DispatchError("project must be a short label using letters, digits, dot, dash or underscore")

    work_dir = _work_dir(body.get("work_dir"))
    command = _command(body.get("command"))
    env = _env(body.get("env"))

    cuda_visible = ",".join(str(index) for index in gpus)
    pieces: list[str] = []
    if work_dir:
        pieces.append(f"cd {shlex.quote(work_dir)}")
    env_prefix = " ".join(f"{key}={shlex.quote(value)}" for key, value in sorted(env.items()))
    device_prefix = f"CUDA_VISIBLE_DEVICES={cuda_visible}" if gpus else ""
    prefix = " ".join(part for part in (env_prefix, device_prefix) if part)
    command_prefix = f"{prefix} " if prefix else ""
    pieces.append(f"{command_prefix}timeout {timeout_minutes}m bash -lc {shlex.quote(command)}")
    remote_command = " && ".join(pieces)

    transport = {
        "host": server["host"],
        "port": server["port"],
        "user": server["user"],
        "auth": "local SSH agent or ~/.ssh/config (no secrets stored by ARK)",
    }
    ssh_hint_argv = ["ssh", "-p", str(server["port"])]
    target = f"{server['user']}@{server['host']}" if server["user"] else server["host"]
    ssh_hint_argv.append(target)
    ssh_hint_argv.extend(["--", "bash", "-lc", remote_command])

    return {
        "format": "ark-gpu-dispatch-request",
        "version": 1,
        "dry_run": True,
        "executed": False,
        "generated_at": datetime.now(timezone.utc).isoformat(timespec="seconds"),
        "server": public_server(server),
        "transport": transport,
        "request": {
            "job": job,
            "project": project,
            "server": server["name"],
            "gpus": gpus,
            "cuda_visible_devices": cuda_visible,
            "timeout_minutes": timeout_minutes,
            "work_dir": work_dir or None,
            "env": env,
            "command": command,
        },
        "remote_command_preview": remote_command,
        "ssh_preview": shlex.join(ssh_hint_argv),
        "notes": [
            "Dry run only: ARK never opens the remote shell or launches this job.",
            "Review the server, GPU indices and command before handing it to the "
            "gpu_server_probe CLI or your scheduler.",
            "Authentication uses your local SSH agent or ~/.ssh/config; ARK stores "
            "no password, key or token.",
            "env values are echoed back in this request; avoid putting secrets there.",
        ],
        "next_steps": [
            "Review the request and the illustrative SSH command.",
            "Run the upstream gpu_server_probe launcher (or your scheduler) yourself "
            "with this job descriptor.",
        ],
    }
