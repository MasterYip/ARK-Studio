"""Run ``python -m arkstudio`` with a request-controlled clock (replay only).

    python -B tests/python/contract/replayclock.py serve ...

Differential replays of write sequences need both servers to stamp a write
with the same second, or rows created within one second on one server and
across two on the other sort differently.  This shim patches
``arkstudio.db.utc_now`` / ``today`` to return the time named by the request
header ``X-Replay-Clock`` (Unix seconds) while that request runs; node
history commits get the same second as their Git date.  The Go
server honours the same header only when started with ARK_REPLAY_CLOCK=1.
Product code is not modified; outside the replay this file is never used.
"""
from __future__ import annotations

import runpy
import sys
import threading
from datetime import datetime, timezone
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[3]))

from arkstudio import (agent_tokens, blackbox, db, demo, markdown_bundle,  # noqa: E402
                       mobile, node_history, org, project_archive, projects, server)

_local = threading.local()
_real_utc_now, _real_today = db.utc_now, db.today


def _now() -> datetime | None:
    value = getattr(_local, "clock", None)
    return datetime.fromtimestamp(value, timezone.utc) if value is not None else None


def utc_now() -> str:
    now = _now()
    return now.isoformat(timespec="seconds") if now else _real_utc_now()


def today() -> str:
    now = _now()
    return now.date().isoformat() if now else _real_today()


db.utc_now, db.today = utc_now, today
# Environment setup (skills/setup.py, setup_layers.py) stamps records,
# history lines and the migration backup name with protocol.utc_now.
from arkstudio.skills import protocol as _protocol  # noqa: E402

_protocol.utc_now = utc_now
# The project overview reads datetime.now(timezone.utc) for "today" and its
# day windows; the Go port reads the replay clock (store.Clock).  Without
# this, a replay whose clock (start + step seconds) crosses UTC midnight
# ahead of the wall clock reports different days on the two servers.
from arkstudio import overview as _overview  # noqa: E402


class _ReplayDatetime(datetime):
    @classmethod
    def now(cls, tz=None):
        now = _now()
        if now is None:
            return datetime.now(tz)
        return now.astimezone(tz) if tz is not None else now.astimezone().replace(tzinfo=None)


_overview.datetime = _ReplayDatetime
org._utcnow = utc_now
demo._utcnow = utc_now
blackbox._utc = utc_now
_real_stamp = blackbox._stamp
blackbox._stamp = lambda: (_now().strftime("%Y%m%dT%H%M%SZ") if _now() else _real_stamp())
_ids = iter(range(1, 10**9))
# Project ids are random; the Go server counts the same way under ARK_REPLAY_CLOCK=1.
projects._new_id = lambda: f"prj_r{next(_ids):09d}"
_begin = server.Handler._begin_request


_last = {"clock": None}


def _begin_request(self) -> None:
    raw = self.headers.get("X-Replay-Clock")
    try:
        _local.clock = float(raw) if raw else None
    except ValueError:
        _local.clock = None
    if _local.clock is not None:
        _last["clock"] = _local.clock
    _begin(self)
    # GPU queue: the worker thread does not run in a replay; a request with
    # X-Replay-GpuTick runs one tick first (the Go server does the same).
    if self.headers.get("X-Replay-GpuTick"):
        queue = getattr(self.app.gpu, "_queue", None)
        if queue is not None:
            try:
                queue.tick()
            except Exception:  # noqa: BLE001 - like the worker loop
                pass


server.Handler._begin_request = _begin_request


class _ReplayDatetime(datetime):
    """datetime.now() of the replay second (local or the given zone)."""

    @classmethod
    def now(cls, tz=None):
        value = getattr(_local, "clock", None)
        if value is None:
            return datetime.now(tz)
        return datetime.fromtimestamp(value, tz)


class _Secrets:
    """A deterministic ``secrets`` module for one named stream (see go/internal/rnd)."""

    def __init__(self, stream: str):
        self.stream, self.calls = stream, 0

    def token_bytes(self, n: int = 32) -> bytes:
        import hashlib
        call, self.calls = self.calls, self.calls + 1
        out = b""
        block = 0
        while len(out) < n:
            out += hashlib.sha256(f"{self.stream}:{call}:{block}".encode()).digest()
            block += 1
        return out[:n]

    def token_hex(self, n: int = 32) -> str:
        return self.token_bytes(n).hex()

    def token_urlsafe(self, n: int = 32) -> str:
        import base64
        return base64.urlsafe_b64encode(self.token_bytes(n)).rstrip(b"=").decode("ascii")

    def __getattr__(self, name):
        import secrets
        return getattr(secrets, name)


org.secrets = _Secrets("org")
agent_tokens.secrets = _Secrets("agent_tokens")
_token_now = agent_tokens.AgentTokenStore._now


def _agent_token_now(self) -> float:
    value = getattr(_local, "clock", None)
    return float(value) if value is not None else _token_now(self)


agent_tokens.AgentTokenStore._now = _agent_token_now
mobile.secrets = _Secrets("mobile")
# meta.change_log_id (db.new_change_log_id, secrets.token_hex(8)): stream
# "change_log", the same one the Go server draws from under ARK_REPLAY_CLOCK=1.
_change_log = _Secrets("change_log")


def _new_change_log_id(con) -> str:
    value = _change_log.token_hex(8)
    db.meta_set(con, db.CHANGE_LOG_ID, value)
    return value


db.new_change_log_id = _new_change_log_id


class _UUID:
    """server.uuid with a deterministic uuid4 (stream "uuid", see go/internal/rnd)."""

    def __init__(self, stream: str = "uuid"):
        self._bytes = _Secrets(stream)

    def uuid4(self):
        import uuid
        return uuid.UUID(bytes=self._bytes.token_bytes(16), version=4)

    def __getattr__(self, name):
        import uuid
        return getattr(uuid, name)


server.uuid = _UUID()
# The Editor workspace draws terminal ids and atomic-write temp names from the
# same deterministic stream, and stamps terminal times with the replay clock.
from arkstudio import workspace as _workspace  # noqa: E402

_workspace.uuid = server.uuid
_workspace.utc_now = utc_now
# InfoHub import copies (importer.import_key copy=1) name the copy from the same stream.
from arkstudio import importer as _importer  # noqa: E402
_importer.uuid = server.uuid
# POST /api/open and /api/local/open launch nothing in a replay (the Go
# server fakes its openers the same way under ARK_REPLAY_CLOCK=1).
import os as _os  # noqa: E402
from arkstudio import platform_support as _platform  # noqa: E402
_os.startfile = lambda *args, **kwargs: None
_real_run_opener = _platform._run_opener


def _fake_run_opener(argv, **kwargs) -> int:
    # kill_process_tree runs taskkill through the same helper: keep that real
    # (stopping timed-out terminals and agent sessions), fake only openers.
    if argv and _os.path.basename(str(argv[0])).lower().startswith("taskkill"):
        return _real_run_opener(argv, **kwargs)
    return 0


_platform._run_opener = _fake_run_opener
_platform.webbrowser = type("_Browser", (), {"open": staticmethod(lambda *args, **kwargs: True)})
_mobile_now = mobile.PairingManager.__init__.__kwdefaults__["clock"]


def _mobile_clock() -> float:
    value = getattr(_local, "clock", None)
    return float(value) if value is not None else _mobile_now()


mobile.PairingManager.__init__.__kwdefaults__["clock"] = _mobile_clock
# The failed-agent-token window (FailureLimiter, 20 per 300 s): wall time made
# a failure expire on one server and not the other at the window edge.
agent_tokens.FailureLimiter.__init__.__kwdefaults__["clock"] = _mobile_clock
# worktrees generated_at: on wall time a HEAD and its GET twin (N21) could
# straddle a second on a busy machine.
from arkstudio import worktrees as _worktrees  # noqa: E402

_worktrees.utc_now = utc_now
markdown_bundle.datetime = _ReplayDatetime
project_archive.datetime = _ReplayDatetime
# The resources graph's "now" (generated_at, heartbeat, signal ages).
from arkstudio import resources_graph as _resources_graph  # noqa: E402

_resources_graph.datetime = _ReplayDatetime


class _GitClock:
    """node_history's ``subprocess`` with the replay second as the commit
    date, so both servers create identical commit ids."""

    def __getattr__(self, name):
        return getattr(_subprocess, name)

    @staticmethod
    def run(*args, **kwargs):
        value = getattr(_local, "clock", None)
        if value is not None and kwargs.get("env") is not None:
            stamp = f"{int(value)} +0000"
            kwargs["env"] = {**kwargs["env"], "GIT_AUTHOR_DATE": stamp, "GIT_COMMITTER_DATE": stamp}
        return _subprocess.run(*args, **kwargs)


_subprocess = node_history.subprocess
node_history.subprocess = _GitClock()

# Agent sessions: output read by background threads is stamped with the last
# replay second (the Go server does the same), ids and tokens come from the
# deterministic "sessions" stream, and session ids / idle detection use the
# request's replay clock.
from arkstudio import session_context as _session_context  # noqa: E402
from arkstudio import session_transcript as _session_transcript  # noqa: E402
from arkstudio import sessions as _sessions  # noqa: E402


def _last_utc_now() -> str:
    value = _last["clock"]
    if value is None:
        return _real_utc_now()
    return datetime.fromtimestamp(value, timezone.utc).isoformat(timespec="seconds")


_sessions.utc_now = _last_utc_now
_session_transcript.utc_now = _last_utc_now
_session_context.utc_now = _last_utc_now
_sessions.secrets = _Secrets("sessions")
_sessions.datetime = _ReplayDatetime

# GPU queue: job ids from the "gpu" stream, job and probe stamps from the
# replay clock, no worker thread (see _begin_request and X-Replay-GpuTick).
from arkstudio import gpu_probe as _gpu_probe  # noqa: E402

_gpu_probe.datetime = _ReplayDatetime
if _gpu_probe.VENDOR is not None:
    _gpu_probe.VENDOR.queue.uuid = _UUID("gpu")
    _gpu_probe.VENDOR.queue.datetime = _ReplayDatetime
    _gpu_probe.VENDOR.dispatch.datetime = _ReplayDatetime
    _gpu_probe.VENDOR.queue.GpuQueue.start = lambda self: None

if __name__ == "__main__":
    sys.argv = ["arkstudio", *sys.argv[1:]]
    runpy.run_module("arkstudio", run_name="__main__", alter_sys=True)
