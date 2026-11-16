"""Minimal stdlib SSH transport for the GPU probe adapter.

Only one read-only remote command is ever built here: the ``nvidia-smi`` GPU
query. ``asyncssh`` is *not* required (the upstream tool used it); ARK shells
out to the OpenSSH client with ``BatchMode=yes`` so a missing key fails fast
instead of prompting. There is no code path that starts a job.
"""
from __future__ import annotations

import os
import shlex
import shutil
import subprocess
import time
from dataclasses import dataclass, field
from pathlib import Path

# The only remote command this adapter can issue. It is read-only.
PROBE_COMMAND = (
    "nvidia-smi",
    "--query-gpu=index,name,memory.total,memory.used,utilization.gpu,"
    "utilization.memory,temperature.gpu,power.draw",
    "--format=csv,noheader,nounits",
)
MAX_CAPTURE_BYTES = 64 * 1024
# Read process ownership first, then free memory, so CUDA initialization cannot
# spend a new allocation between the free-memory and reservation snapshots.
ALLOCATION_COMMAND = r"""echo __ARK_BUS__
nvidia-smi --query-gpu=index,pci.bus_id --format=csv,noheader,nounits || exit $?
echo __ARK_PROC__
apps=$(nvidia-smi --query-compute-apps=pid,gpu_bus_id,used_gpu_memory --format=csv,noheader,nounits 2>/dev/null) || apps=''
printf '%s\n' "$apps"
echo __ARK_ENV__
for pid in $(printf '%s\n' "$apps" | cut -d, -f1 | tr -d ' ' | sort -u); do
  if [ -r /proc/$pid/environ ]; then
    jid=$(tr '\0' '\n' < /proc/$pid/environ 2>/dev/null | sed -n 's/^ARK_GPU_JOB_ID=//p' | head -1)
    [ -n "$jid" ] && echo "$pid $jid"
  fi
done
echo __ARK_GPU__
""" + " ".join(PROBE_COMMAND)
SYSTEM_COMMAND = r"""echo __ARK_LOAD__; cut -d' ' -f1-3 /proc/loadavg
echo __ARK_NPROC__; nproc
echo __ARK_MEM__; free -m | awk 'NR==2 {print $2, $3, $4, $7}'
echo __ARK_CPU__; top -bn2 -d 0.5 2>/dev/null | grep 'Cpu(s)' | tail -1
echo __ARK_BUS__; nvidia-smi --query-gpu=index,pci.bus_id --format=csv,noheader,nounits 2>/dev/null || true
echo __ARK_PROC__; nvidia-smi --query-compute-apps=pid,gpu_bus_id,used_gpu_memory --format=csv,noheader,nounits 2>/dev/null || true
echo __ARK_PMON__; timeout 5 nvidia-smi pmon -c 1 -s u 2>/dev/null || true
echo __ARK_ENV__
for pid in $(nvidia-smi --query-compute-apps=pid --format=csv,noheader 2>/dev/null | tr -d ' '); do
  if [ -r /proc/$pid/environ ]; then
    jid=$(tr '\0' '\n' < /proc/$pid/environ 2>/dev/null | sed -n 's/^ARK_GPU_JOB_ID=//p' | head -1)
    [ -n "$jid" ] && echo "$pid $jid"
  fi
done
true"""
MISSING_SSH_HINT = (
    "OpenSSH client not found. Install the OpenSSH Client optional feature "
    "(Windows) or the openssh-client package (Linux/macOS), make sure `ssh` is "
    "on PATH, or set ARK_GPU_SSH to the ssh executable path."
)


@dataclass
class CommandResult:
    """Outcome of one SSH invocation, with a coarse failure category."""

    returncode: int
    stdout: str = ""
    stderr: str = ""
    error_kind: str = ""  # "", missing_ssh, timeout, unreachable, auth, command_failed, ssh_error
    message: str = ""
    duration_ms: int = 0
    warnings: list[str] = field(default_factory=list)

    @property
    def ok(self) -> bool:
        return self.returncode == 0 and not self.error_kind

    def public(self) -> dict:
        return {
            "ok": self.ok,
            "error_kind": self.error_kind,
            "message": self.message,
            "duration_ms": self.duration_ms,
        }


def _truncate(text: str | None) -> str:
    text = text or ""
    if len(text.encode("utf-8", "replace")) <= MAX_CAPTURE_BYTES:
        return text
    return text.encode("utf-8", "replace")[:MAX_CAPTURE_BYTES].decode("utf-8", "replace") + "\n…[truncated]"


def ssh_binary(environ: dict | None = None) -> str:
    """Resolve the ssh executable, honouring the ARK_GPU_SSH override."""
    env = os.environ if environ is None else environ
    override = (env.get("ARK_GPU_SSH") or "").strip()
    if override:
        found = shutil.which(override)
        if found:
            return found
        candidate = Path(override).expanduser()
        return str(candidate) if candidate.is_file() else ""
    return shutil.which("ssh") or ""


def dependency_status(environ: dict | None = None) -> dict:
    """Report the local SSH dependency without touching the network."""
    binary = ssh_binary(environ)
    return {
        "available": bool(binary),
        "binary": binary,
        "hint": "" if binary else MISSING_SSH_HINT,
    }


def asyncssh_available() -> bool:
    """Optional dependency detection; asyncssh is never required by ARK."""
    try:
        import importlib.util

        return importlib.util.find_spec("asyncssh") is not None
    except (ImportError, ValueError):  # pragma: no cover - defensive
        return False


def build_ssh_argv(server: dict, binary: str, timeout: int) -> list[str]:
    """Build an argv list; no user-controlled string is ever shell-parsed."""
    port = int(server.get("port") or 22)
    host = str(server.get("host") or "")
    user = str(server.get("user") or "")
    target = f"{user}@{host}" if user else host
    argv = [
        binary,
        "-o", "BatchMode=yes",
        "-o", f"ConnectTimeout={max(1, int(timeout))}",
        "-o", "StrictHostKeyChecking=accept-new",
        "-o", "ServerAliveInterval=5",
        "-o", "ServerAliveCountMax=2",
        "-p", str(port),
        "--", target,
    ]
    argv.extend(PROBE_COMMAND)
    return argv


def probe_command_text() -> str:
    return " ".join(PROBE_COMMAND)


def _classify(stderr: str, returncode: int) -> tuple[str, str]:
    lowered = stderr.lower()
    message = stderr.strip()[:300]
    if "permission denied" in lowered or "no supported authentication" in lowered:
        return "auth", message or "SSH authentication failed; check your SSH agent or ~/.ssh/config"
    unreachable_markers = (
        "could not resolve", "name or service not known", "nodename nor servname",
        "connection refused", "connection timed out", "no route to host",
        "network is unreachable", "connection closed", "connection reset",
        "host key verification failed", "remote host identification has changed",
    )
    if any(marker in lowered for marker in unreachable_markers):
        return "unreachable", message or "SSH connection failed"
    if returncode == 255:
        return "ssh_error", message or "SSH exited with status 255"
    return "command_failed", message or f"remote command exited with status {returncode}"


def build_command_argv(server: dict, binary: str, timeout: int, command: str) -> list[str]:
    """Build an SSH argv for one explicit, queue-managed remote command.

    Unlike the probe path this is intentionally general: it is only called by
    the opt-in queue launcher after a job has been validated and allocated.
    """
    port = int(server.get("port") or 22)
    host = str(server.get("host") or "")
    user = str(server.get("user") or "")
    target = f"{user}@{host}" if user else host
    return [
        binary,
        "-o", "BatchMode=yes",
        "-o", f"ConnectTimeout={max(1, int(timeout))}",
        "-o", "StrictHostKeyChecking=accept-new",
        "-o", "ServerAliveInterval=5",
        "-o", "ServerAliveCountMax=2",
        "-p", str(port),
        "--", target, "bash", "-lc", shlex.quote(command),
    ]


def run_remote_command(server: dict, command: str, *, timeout: int | None = None,
                       binary: str | None = None,
                       environ: dict | None = None) -> CommandResult:
    """Run one explicit queue command and return its captured result."""
    limit = int(timeout or server.get("timeout_seconds") or 8)
    binary = binary or ssh_binary(environ)
    started = time.monotonic()
    if not binary:
        return CommandResult(-1, error_kind="missing_ssh", message=MISSING_SSH_HINT)
    argv = build_command_argv(server, binary, limit, command)
    try:
        completed = subprocess.run(
            argv, capture_output=True, text=True, encoding="utf-8",
            errors="replace", timeout=limit + 5, check=False,
        )
    except subprocess.TimeoutExpired:
        return CommandResult(
            -1, error_kind="timeout",
            message=f"SSH to {server.get('name') or 'server'} timed out after {limit + 5}s",
            duration_ms=int((time.monotonic() - started) * 1000),
        )
    except FileNotFoundError:
        return CommandResult(-1, error_kind="missing_ssh", message=MISSING_SSH_HINT)
    except OSError as exc:
        return CommandResult(
            -1, error_kind="ssh_error",
            message=f"could not start ssh: {exc}",
            duration_ms=int((time.monotonic() - started) * 1000),
        )
    duration = int((time.monotonic() - started) * 1000)
    stdout = _truncate(completed.stdout)
    stderr = _truncate(completed.stderr)
    if completed.returncode == 0:
        return CommandResult(0, stdout=stdout, stderr=stderr, duration_ms=duration)
    kind, message = _classify(stderr, completed.returncode)
    return CommandResult(completed.returncode, stdout=stdout, stderr=stderr,
                         error_kind=kind, message=message, duration_ms=duration)


def run_remote_system(server: dict, *, timeout: int | None = None,
                      binary: str | None = None,
                      environ: dict | None = None) -> CommandResult:
    """Read CPU load/memory from a server; no mutation and no shell input."""
    return run_remote_command(server, SYSTEM_COMMAND, timeout=timeout,
                              binary=binary, environ=environ)


def run_remote_allocation(server: dict, *, timeout: int | None = None) -> CommandResult:
    """Read GPU capacity and ARK-owned memory together for queue placement."""
    return run_remote_command(server, ALLOCATION_COMMAND, timeout=timeout)


def run_remote(server: dict, *, timeout: int | None = None, binary: str | None = None,
               environ: dict | None = None) -> CommandResult:
    """Run the read-only GPU probe on one server.

    Never raises for ordinary network/auth failures: the failure is encoded in
    ``CommandResult`` so callers can degrade per server.
    """
    limit = int(timeout or server.get("timeout_seconds") or 8)
    binary = binary or ssh_binary(environ)
    started = time.monotonic()
    if not binary:
        return CommandResult(-1, error_kind="missing_ssh", message=MISSING_SSH_HINT)

    argv = build_ssh_argv(server, binary, limit)
    try:
        completed = subprocess.run(
            argv,
            capture_output=True,
            text=True,
            encoding="utf-8",
            errors="replace",
            timeout=limit + 5,
            check=False,
        )
    except subprocess.TimeoutExpired:
        return CommandResult(
            -1,
            error_kind="timeout",
            message=f"SSH to {server.get('name') or 'server'} timed out after {limit + 5}s",
            duration_ms=int((time.monotonic() - started) * 1000),
        )
    except FileNotFoundError:
        return CommandResult(-1, error_kind="missing_ssh", message=MISSING_SSH_HINT)
    except OSError as exc:
        return CommandResult(
            -1,
            error_kind="ssh_error",
            message=f"could not start ssh: {exc}",
            duration_ms=int((time.monotonic() - started) * 1000),
        )

    duration = int((time.monotonic() - started) * 1000)
    stdout = _truncate(completed.stdout)
    stderr = _truncate(completed.stderr)
    if completed.returncode == 0:
        return CommandResult(0, stdout=stdout, stderr=stderr, duration_ms=duration)
    kind, message = _classify(stderr, completed.returncode)
    return CommandResult(completed.returncode, stdout=stdout, stderr=stderr,
                         error_kind=kind, message=message, duration_ms=duration)
