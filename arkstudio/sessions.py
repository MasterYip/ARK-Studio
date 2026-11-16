"""Managed local agent sessions for the Task tab.

A task session is an ordinary local subprocess wrapped in an HTTP-managed
lifecycle.  The server owns the process tree, streams its output to append-only
JSONL logs, and exposes the session workspace as a scoped artifact root.  The
subprocess never receives ARK's write token; it receives a distinct read-only
token for ``/api/task-agent/v1/...`` and a generated read-only snapshot of the
project.

Storage (all outside the project folder)::

    <sessions-root>/<session-id>/
      meta.json          lifecycle, platform, task reference, read token
      stdout.jsonl       one JSON entry per output line
      stderr.jsonl
      workspace/         the only cwd an agent may start in
        artifacts/       conventional place for files the inner assistant reads
        ark-context.json generated read-only project snapshot
        ARK-TASK.md      the read-only contract handed to the agent

Standard library only.  Reader and monitor threads are daemons; starting a
session never blocks the HTTP server, and stopping is dispatched to a short
worker thread so the request returns immediately.
"""
from __future__ import annotations

import hmac
import json
import mimetypes
import os
import re
import secrets
import shlex
import shutil
import subprocess
import sys
import threading
import time

from . import assistant_config, linux_account, platform_support, session_transcript
from datetime import datetime, timezone
from pathlib import Path
from typing import Any, Callable

MAX_LOG_LINE = 8000
MAX_LOG_LIMIT = 500
MAX_PROMPT = 12000
MAX_INPUT = 8000
MAX_TITLE = 120
MAX_TEXT_ARTIFACT = 256 * 1024
MAX_DOWNLOAD = 8 * 1024 * 1024
MAX_ARTIFACT_ENTRIES = 400
MAX_CONTEXT_ITEMS = 12
MAX_CONTEXT_TEXT = 4000
MAX_CONTEXT_TOTAL = 24000
MAX_STEER_TEXT = 4000
MAX_STEER_QUEUE = 20
MAX_APPROVALS = 200
MAX_APPROVAL_ITEMS = 60
IDLE_AFTER_SECONDS = 90

# Permission modes are the session's write/approval policy.  ``read_only`` and
# ``plan`` withhold ARK's write token; ``ask`` and ``accept_edits`` are
# adapter-mediated approval modes (see ``APPROVAL_PROTOCOL``); ``full_access``
# is the explicit escalation that receives ARK's write token.
PERMISSION_MODES = ("read_only", "plan", "ask", "accept_edits", "full_access")
PERMISSION_LABELS = {
    "read_only": "read-only",
    "plan": "plan (read-only)",
    "ask": "ask to write",
    "accept_edits": "accept edits",
    "full_access": "full access",
}
PERMISSION_DESCRIPTIONS = {
    "read_only": "Read the project through the read-only task API; no write token.",
    "plan": "Read-only planning turn: no write token, and the contract asks for a plan instead of edits.",
    "ask": "Every mutating tool or command needs an approval from you; the adapter must request it.",
    "accept_edits": "File edits are auto-approved by ARK; commands still wait for your decision.",
    "full_access": "No prompts: the turn receives ARK's write token and may write project units.",
}
APPROVAL_MODES = ("ask", "accept_edits")
READ_ONLY_MODES = ("read_only", "plan")
EDIT_KINDS = {"file_edit", "edit", "write", "patch", "file_change", "file_write"}
APPROVAL_PROTOCOL = "ark-permission-v1"
MAX_APPROVAL_MESSAGE = 600
MAX_FORK_CONTEXT = 6000

# Backwards-compatible names used by earlier callers.
ACCESS_MODES = PERMISSION_MODES
ACCESS_LABELS = PERMISSION_LABELS

SESSION_ID_RE = re.compile(r"^s_[a-z0-9]{8,32}$")
TEXT_SUFFIXES = {
    ".bat", ".cfg", ".conf", ".css", ".csv", ".html", ".ini", ".js", ".json",
    ".jsonl", ".log", ".md", ".mjs", ".ps1", ".py", ".sh", ".sql", ".svg",
    ".toml", ".ts", ".txt", ".xml", ".yaml", ".yml",
}
ACTIVE_STATUSES = {"starting", "running", "stopping"}
STATUS_ORDER = {"running": 0, "idle": 1, "starting": 2, "stopping": 3, "errored": 4, "stopped": 5}


class SessionError(ValueError):
    """A task-agent request that the API should report as a clean 4xx."""

    def __init__(self, message: str, status: int = 400, code: str = ""):
        super().__init__(message)
        self.status = status
        self.code = code


def utc_now() -> str:
    return datetime.now(timezone.utc).isoformat(timespec="seconds")


def _write_json(path: Path, data: dict) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    tmp = path.with_suffix(path.suffix + ".tmp")
    tmp.write_text(json.dumps(data, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    tmp.replace(path)


def _read_json(path: Path) -> dict | None:
    try:
        data = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, ValueError):
        return None
    return data if isinstance(data, dict) else None


def _parse_time(value: Any) -> datetime | None:
    try:
        return datetime.fromisoformat(str(value))
    except (TypeError, ValueError):
        return None


def split_command(text: str) -> list[str]:
    """Split an operator-provided command without mangling Windows paths."""
    text = str(text or "").strip()
    if not text:
        return []
    if os.name == "nt":
        raw = shlex.split(text, posix=False)
        out = []
        for token in raw:
            if len(token) >= 2 and token[0] == token[-1] and token[0] in "\"'":
                token = token[1:-1]
            out.append(token)
        return out
    return shlex.split(text)


def _string_list(value: Any, *, limit: int = 20, max_len: int = 80) -> list[str]:
    if isinstance(value, str):
        value = [part.strip() for part in value.split(",")]
    if not isinstance(value, (list, tuple)):
        return []
    out: list[str] = []
    for item in value:
        text = str(item or "").strip()[:max_len]
        if text and text not in out:
            out.append(text)
        if len(out) >= limit:
            break
    return out


def _mode_token(value: Any) -> str:
    return str(value or "").strip().lower().replace("-", "_").replace(" ", "_")


def normalize_access_mode(value: Any, allowed: list[str] | None = None) -> str:
    """Validate a permission-mode request against the platform's allowed modes."""
    mode = _mode_token(value or "read_only")
    if mode not in PERMISSION_MODES:
        raise SessionError(
            "access_mode must be one of: " + ", ".join(PERMISSION_LABELS[m] for m in PERMISSION_MODES))
    if allowed and mode not in allowed:
        raise SessionError(
            f"this platform does not allow {PERMISSION_LABELS[mode]} access")
    return mode


def _as_bool(value: Any, default: bool = False) -> bool:
    if isinstance(value, bool):
        return value
    if value is None:
        return default
    return str(value).strip().lower() in {"1", "true", "yes", "on"}


def _normalize_preset(raw: dict) -> dict:
    preset_id = re.sub(r"[^a-z0-9_-]+", "-", str(raw.get("id") or "").lower()).strip("-")
    if not preset_id:
        raise ValueError("task preset id is required")
    command = raw.get("command") or []
    if isinstance(command, str):
        command = split_command(command)
    command = [str(part) for part in command]
    if not command:
        raise ValueError(f"task preset {preset_id} needs a command")
    prompt_mode = str(raw.get("prompt_mode") or "").strip().lower()
    if prompt_mode not in {"arg", "stdin"}:
        prompt_mode = "arg" if any("{prompt}" in part for part in command) else "stdin"
    followup = _as_bool(raw.get("followup"), prompt_mode == "stdin")
    # Approval requests are only real if the process keeps stdin open to read
    # the decision.  A one-shot print/exec CLI is honest about being unable to
    # ask, so approval modes are stripped from its offered modes.
    supports_approval = _as_bool(raw.get("supports_approval"), default=False) and followup
    declared = _string_list(raw.get("permission_modes", raw.get("access_modes")), limit=5)
    declared = [_mode_token(mode) for mode in declared]
    declared = [mode for mode in declared if mode in PERMISSION_MODES]
    if not declared:
        declared = (list(PERMISSION_MODES) if supports_approval
                    else ["read_only", "plan", "full_access"])
    # De-duplicate while preserving the operator's order.
    declared = list(dict.fromkeys(declared))
    stripped = [mode for mode in declared if mode in APPROVAL_MODES and not supports_approval]
    permission_modes = [mode for mode in declared if mode not in stripped]
    if not permission_modes:
        permission_modes = ["read_only"]
    default_mode = _mode_token(raw.get("default_permission_mode") or raw.get("access_mode"))
    if default_mode not in permission_modes:
        default_mode = "read_only" if "read_only" in permission_modes else permission_modes[0]
    return {
        "id": preset_id,
        "label": str(raw.get("label") or preset_id),
        "description": str(raw.get("description") or ""),
        "command": command,
        "prompt_mode": prompt_mode,
        "followup": followup,
        "models": _string_list(raw.get("models")),
        "efforts": _string_list(raw.get("efforts") or raw.get("reasoning"), limit=8),
        "permission_modes": permission_modes,
        "supports_approval": supports_approval,
        "unsupported_permission_modes": [mode for mode in APPROVAL_MODES
                                         if mode not in permission_modes],
        "default_permission_mode": default_mode,
        "demo": bool(raw.get("demo")),
        "available": raw.get("available"),  # explicit override, otherwise probed
    }


def _preset_available(preset: dict) -> bool:
    override = preset.get("available")
    if isinstance(override, bool):
        return override
    if preset.get("demo"):
        return True
    program = preset["command"][0]
    if os.path.isabs(program) or os.sep in program or (os.altsep and os.altsep in program):
        return Path(program).is_file()
    return shutil.which(program) is not None


def default_presets(env: dict | None = None) -> list[dict]:
    """Platform presets.  Every command is operator-configured via env vars."""
    env = os.environ if env is None else env
    python = sys.executable
    demo_agent = Path(__file__).resolve().parent / "demo_agent.py"

    def from_env(name: str, default: str) -> list[str]:
        return split_command(str(env.get(name) or default))

    def env_list(name: str, default: list[str] | None = None) -> list[str]:
        """Comma-separated choices (models / reasoning levels) per preset."""
        raw = str(env.get(name) or "").strip()
        if not raw:
            return list(default or [])
        return [part.strip() for part in raw.split(",") if part.strip()]

    # The local shell is the one preset whose command is platform-dependent.
    # An explicit ARK_TASK_SHELL_COMMAND always wins.
    shell_override = str(env.get("ARK_TASK_SHELL_COMMAND") or "").strip()
    shell_command = (split_command(shell_override) if shell_override
                     else platform_support.shell_command(env=env))

    presets = [
        {
            "id": "echo",
            "label": "Echo (local demo)",
            "description": "A tiny local Python agent for trying the session lifecycle. No model call.",
            "command": [python, "-u", str(demo_agent), "--prompt", "{prompt}"],
            "prompt_mode": "arg",
            "followup": True,
            "models": env_list("ARK_TASK_ECHO_MODELS", ["none (demo)"]),
            "efforts": env_list("ARK_TASK_ECHO_EFFORTS"),
            # The bundled demo agent implements the approval protocol, so every
            # mode can be exercised without a model or an installed CLI.
            "permission_modes": list(PERMISSION_MODES),
            "supports_approval": True,
            "demo": True,
        },
        {
            "id": "dsh",
            "label": "DeepSeek Harness",
            "description": "Headless DSH process. Reads the initial prompt from stdin.",
            "command": from_env("ARK_TASK_DSH_COMMAND", "dsh headless -"),
            "prompt_mode": "stdin",
            "followup": False,
            "models": env_list("ARK_TASK_DSH_MODELS"),
            "efforts": env_list("ARK_TASK_DSH_EFFORTS"),
        },
        {
            "id": "claude",
            "label": "Claude Code",
            "description": "Claude Code print mode with the prompt as an argument.",
            "command": from_env("ARK_TASK_CLAUDE_COMMAND", 'claude -p "{prompt}"'),
            "prompt_mode": "arg",
            "followup": False,
            "models": env_list("ARK_TASK_CLAUDE_MODELS"),
            "efforts": env_list("ARK_TASK_CLAUDE_EFFORTS"),
        },
        {
            "id": "codex",
            "label": "Codex CLI",
            "description": "Codex non-interactive exec mode.",
            "command": from_env("ARK_TASK_CODEX_COMMAND",
                                 'codex exec --skip-git-repo-check "{prompt}"'),
            "prompt_mode": "arg",
            "followup": False,
            # Models are discovered by the provider catalog (``codex debug
            # models``); the old static list is its labelled builtin fallback.
            "models": env_list("ARK_TASK_CODEX_MODELS"),
            "efforts": env_list("ARK_TASK_CODEX_EFFORTS"),
        },
        {
            "id": "opencode",
            "label": "OpenCode",
            "description": "OpenCode non-interactive run mode.",
            "command": from_env("ARK_TASK_OPENCODE_COMMAND", 'opencode run "{prompt}"'),
            "prompt_mode": "arg",
            "followup": False,
            "models": env_list("ARK_TASK_OPENCODE_MODELS"),
            "efforts": env_list("ARK_TASK_OPENCODE_EFFORTS"),
        },
        {
            "id": "cursor",
            "label": "Cursor Agent",
            "description": "Cursor Agent CLI print mode.",
            "command": from_env("ARK_TASK_CURSOR_COMMAND", 'cursor-agent -p "{prompt}"'),
            "prompt_mode": "arg",
            "followup": False,
            "models": env_list("ARK_TASK_CURSOR_MODELS"),
            "efforts": env_list("ARK_TASK_CURSOR_EFFORTS"),
        },
        {
            "id": "gemini",
            "label": "Gemini CLI",
            "description": "Gemini CLI non-interactive prompt mode.",
            "command": from_env("ARK_TASK_GEMINI_COMMAND", 'gemini -p "{prompt}"'),
            "prompt_mode": "arg",
            "followup": False,
            "models": env_list("ARK_TASK_GEMINI_MODELS"),
            "efforts": env_list("ARK_TASK_GEMINI_EFFORTS"),
        },
        {
            "id": "shell",
            "label": "Local shell",
            "description": "A plain local shell; the initial prompt is delivered on stdin.",
            "command": shell_command,
            "prompt_mode": "stdin",
            "followup": True,
            "models": env_list("ARK_TASK_SHELL_MODELS"),
            "efforts": env_list("ARK_TASK_SHELL_EFFORTS"),
        },
    ]
    custom = str(env.get("ARK_TASK_CUSTOM_COMMAND") or "").strip()
    if custom:
        custom_approvals = str(env.get("ARK_TASK_CUSTOM_APPROVALS") or "").strip().lower() in {
            "1", "true", "yes", "on"}
        presets.append(
            {
                "id": "custom",
                "label": str(env.get("ARK_TASK_CUSTOM_LABEL") or "Custom command"),
                "description": "Operator-defined command from ARK_TASK_CUSTOM_COMMAND.",
                "command": split_command(custom),
                "prompt_mode": "stdin" if "{prompt}" not in custom else "arg",
                "followup": str(env.get("ARK_TASK_CUSTOM_FOLLOWUP", "1")).lower() in {"1", "true", "yes"},
                "models": env_list("ARK_TASK_CUSTOM_MODELS"),
                "efforts": env_list("ARK_TASK_CUSTOM_EFFORTS"),
                "supports_approval": custom_approvals,
                "permission_modes": env_list("ARK_TASK_CUSTOM_PERMISSION_MODES") or None,
            }
        )
    return [dict(p, available=_preset_available(p)) for p in presets]


class ManagedSession:
    """One live or historical agent session (the manager owns persistence)."""

    def __init__(self, manager: "SessionManager", folder: Path, meta: dict):
        self.manager = manager
        self.folder = Path(folder)
        self.meta = meta
        self.proc: subprocess.Popen | None = None
        self.lock = threading.RLock()
        self._log_lock = threading.Lock()
        self._stdin_lock = threading.Lock()
        self.stop_requested = False
        self._last_save = 0.0
        self._readers: dict[str, threading.Thread] = {}

    @property
    def id(self) -> str:
        return str(self.meta.get("id"))

    @property
    def workspace(self) -> Path:
        return self.folder / "workspace"

    @property
    def is_active(self) -> bool:
        with self.lock:
            if self.proc is not None and self.proc.poll() is None:
                return True
            return str(self.meta.get("status") or "") in ACTIVE_STATUSES

    def public(self, detail: bool = False) -> dict:
        return self.manager.public(self, detail=detail)

    def save(self) -> None:
        self.manager.save(self)

    def touch(self, nbytes: int = 0) -> None:
        with self.lock:
            self.meta["last_activity_at"] = utc_now()
            counts = self.meta.setdefault("counts", {"stdout": 0, "stderr": 0})
            counts["bytes"] = int(counts.get("bytes") or 0) + int(nbytes)
            now = time.time()
            if now - self._last_save >= 1.5:
                self._last_save = now
                self.manager.save(self)

    def append_event(self, stream: str, line: str, kind: str = "") -> None:
        line = str(line).rstrip("\r\n")
        if len(line) > MAX_LOG_LINE:
            line = line[:MAX_LOG_LINE] + " …[truncated]"
        entry = {"t": utc_now(), "line": line}
        if kind:
            entry["kind"] = kind
        with self._log_lock:
            path = self.folder / f"{stream}.jsonl"
            with path.open("a", encoding="utf-8") as fh:
                fh.write(json.dumps(entry, ensure_ascii=False) + "\n")
                fh.flush()
        with self.lock:
            counts = self.meta.setdefault("counts", {"stdout": 0, "stderr": 0})
            counts[stream] = int(counts.get(stream) or 0) + 1

    def start_readers(self) -> None:
        assert self.proc is not None and self.proc.stdout is not None and self.proc.stderr is not None
        for name, pipe in (("stdout", self.proc.stdout), ("stderr", self.proc.stderr)):
            thread = threading.Thread(target=self._read_stream, args=(name, pipe),
                                      name=f"ark-session-{self.id}-{name}", daemon=True)
            thread.start()
            self._readers[name] = thread
        threading.Thread(target=self._monitor, name=f"ark-session-{self.id}-monitor", daemon=True).start()

    def _read_stream(self, name: str, pipe) -> None:
        try:
            for raw in iter(pipe.readline, b""):
                if not raw:
                    break
                try:
                    text = raw.decode("utf-8")
                except UnicodeDecodeError:
                    try:
                        text = raw.decode("mbcs", "replace")
                    except (LookupError, AttributeError):
                        text = raw.decode("utf-8", "replace")
                self.append_event(name, text)
                self.touch(len(raw))
                try:
                    self.manager.observe_native_id(self, text)
                except Exception:  # noqa: BLE001 - a bad line must not kill the reader
                    pass
                if name == "stdout":
                    try:
                        self.manager.observe_output_line(self, text)
                    except Exception:  # noqa: BLE001 - a bad line must not kill the reader
                        pass
        except (OSError, ValueError):
            pass
        finally:
            try:
                pipe.close()
            except OSError:
                pass

    def _monitor(self) -> None:
        proc = self.proc
        if proc is None:
            return
        try:
            code = proc.wait()
        except Exception:  # noqa: BLE001 - monitor must always finish
            code = None
        for thread in list(self._readers.values()):
            thread.join(timeout=2)
        self.close_pipes()
        self.finish(code)

    def close_pipes(self) -> None:
        proc = self.proc
        if proc is None:
            return
        for pipe in (proc.stdin, proc.stdout, proc.stderr):
            if pipe is not None:
                try:
                    pipe.close()
                except (OSError, ValueError):
                    pass

    def expire_pending_approvals(self, reason: str, *, save: bool = True) -> int:
        """Pending requests cannot be answered once the process is gone."""
        count = 0
        with self.lock:
            now = utc_now()
            for record in self.meta.get("approvals") or []:
                if not isinstance(record, dict) or record.get("status") != "pending":
                    continue
                record["status"] = "expired"
                record["message"] = reason
                record["decided_at"] = now
                record["decided_by"] = "system"
                count += 1
            if count:
                self.meta["last_activity_at"] = now
        if count and save:
            self.manager.save(self)
        return count

    def finish(self, code: int | None) -> None:
        self.close_pipes()
        with self.lock:
            if str(self.meta.get("status")) not in ACTIVE_STATUSES:
                return
            self.expire_pending_approvals("the process ended before a decision", save=False)
            stopped = bool(self.stop_requested) or code == 0
            self.meta["status"] = "stopped" if stopped else "errored"
            # A user stop is a kill, not a diagnostic exit code.
            self.meta["exit_code"] = None if self.stop_requested else code
            self.meta["ended_at"] = utc_now()
            self.meta["last_activity_at"] = self.meta["ended_at"]
            if code not in (None, 0) and not self.stop_requested:
                self.meta["error"] = f"process exited with code {code}"
            stderr_count = int((self.meta.get("counts") or {}).get("stderr") or 0)
            # Codex's human-readable transcript is written on stderr.  A
            # non-empty stderr is not itself a process failure for that CLI.
            stderr_is_transcript = str(self.meta.get("platform") or "").lower() == "codex"
            if (not stopped and stderr_count and not stderr_is_transcript
                    and not self.meta.get("error")):
                self.meta["error"] = "process exited with errors"
            self.manager.save(self)
        self.manager.notify_change(self, "finish")

    def write_input(self, text: str, *, record: bool = True, display: str | None = None) -> None:
        proc = self.proc
        if proc is None or proc.poll() is not None or proc.stdin is None:
            raise SessionError("session is not running", 409)
        payload = (str(text).rstrip("\r\n") + "\n").encode("utf-8")
        with self._stdin_lock:
            try:
                proc.stdin.write(payload)
                proc.stdin.flush()
            except (BrokenPipeError, OSError, ValueError) as exc:
                raise SessionError("session stdin is closed", 409) from exc
        if record:
            shown = text if display is None else display
            self.append_event("stdout", "› " + str(shown).rstrip("\r\n"), kind="input")
        self.touch(len(payload))


class SessionManager:
    def __init__(
        self,
        root: str | Path,
        *,
        project_provider: Callable[[], dict],
        context_provider: Callable[[], dict],
        presets: list[dict] | None = None,
        strip_env_values: list[str] | None = None,
        idle_after: int = IDLE_AFTER_SECONDS,
        write_token: str | None = None,
        unit_provider: Callable[[str], dict] | None = None,
        worktree_provider: Callable[[str], dict] | None = None,
        provider_catalog: Any = None,
        agent_config_provider: Callable[[str], dict] | None = None,
        assistant_config_provider: Callable[[str], dict] | None = None,
        linux_account_provider: Callable[[str], str] | None = None,
    ):
        self.root = Path(root).resolve()
        self.root.mkdir(parents=True, exist_ok=True)
        self.archive_root = self.root / ".archived"
        self.archive_root.mkdir(parents=True, exist_ok=True)
        self.project_provider = project_provider
        self.context_provider = context_provider
        self.write_token = str(write_token or "")
        self.unit_provider = unit_provider
        self.worktree_provider = worktree_provider
        self.provider_catalog = provider_catalog
        # Optional per-user effective settings.  A provider returns
        # ``{"presets": {id: {"command": [...], "env": {...}, "enabled": bool}}}``
        # where ``env`` holds that user's sealed credential values.  Nothing
        # here is ever persisted to the session metadata.
        self.agent_config_provider = agent_config_provider
        self.assistant_config_provider = assistant_config_provider
        self.linux_account_provider = linux_account_provider
        self.presets = [_normalize_preset(p) for p in (presets if presets is not None else default_presets())]
        if not self.presets:
            raise ValueError("at least one task-agent preset is required")
        self._presets_by_id = {p["id"]: p for p in self.presets}
        self.strip_env_values = [v for v in (strip_env_values or []) if v]
        self.idle_after = int(idle_after)
        self.lock = threading.RLock()
        self._sessions: dict[str, ManagedSession] = {}
        # Optional ``on_change(session, event)`` hook ("finish", "update",
        # "delete"); the server mirrors transcripts into the project with it.
        self.on_change: Callable[[ManagedSession, str], None] | None = None
        self._load_existing()

    def notify_change(self, session: ManagedSession, event: str) -> None:
        if self.on_change is None:
            return
        try:
            self.on_change(session, event)
        except Exception:  # noqa: BLE001 - a mirror failure must never break a session
            pass

    # ------------------------------------------------------------- persistence
    def _load_existing(self) -> None:
        for folder in sorted(self.root.iterdir()):
            if not folder.is_dir() or folder.name.startswith("."):
                continue
            meta = _read_json(folder / "meta.json")
            if not meta or not SESSION_ID_RE.fullmatch(str(meta.get("id") or "")):
                continue
            if str(meta.get("status") or "") in ACTIVE_STATUSES:
                meta["status"] = "stopped"
                meta["ended_at"] = utc_now()
                meta["last_activity_at"] = meta["ended_at"]
                meta["notice"] = "ARK restarted; the previous process is no longer attached."
                _write_json(folder / "meta.json", meta)
            self._sessions[str(meta["id"])] = ManagedSession(self, folder, meta)

    def save(self, session: ManagedSession) -> None:
        with session.lock:
            _write_json(session.folder / "meta.json", session.meta)

    # --------------------------------------------------------------- selection
    def _new_id(self) -> str:
        while True:
            candidate = "s_" + datetime.now(timezone.utc).strftime("%Y%m%d%H%M%S") + secrets.token_hex(3)
            if not (self.root / candidate).exists():
                return candidate

    def preset(self, preset_id: str, actor: str = "") -> dict:
        preset = self._presets_by_id.get(str(preset_id or ""))
        if not preset:
            raise SessionError(f"unknown platform: {preset_id or '(missing)'}")
        override = self.agent_overrides(actor).get(preset["id"]) or {}
        command = override.get("command")
        if command:
            merged = dict(preset)
            merged["command"] = list(command)
            return merged
        return preset

    def agent_overrides(self, actor: str = "") -> dict:
        """Effective preset overrides for one account (commands + secret env)."""
        if self.agent_config_provider is None:
            return {}
        try:
            resolved = self.agent_config_provider(actor) or {}
        except Exception:  # noqa: BLE001 - settings must never break launching
            return {}
        presets = resolved.get("presets")
        return presets if isinstance(presets, dict) else {}

    def _validate_permission_mode(self, value: Any, preset: dict) -> str:
        requested = _mode_token(value or preset.get("default_permission_mode") or "read_only")
        if requested in APPROVAL_MODES and not preset["supports_approval"]:
            raise SessionError(
                f"{preset['label']} cannot request approvals; ask/accept-edits modes "
                "need an interactive adapter that reads decisions on stdin", 400)
        return normalize_access_mode(requested, preset["permission_modes"])

    def get(self, session_id: str) -> ManagedSession:
        session_id = str(session_id or "")
        if not SESSION_ID_RE.fullmatch(session_id):
            raise SessionError("invalid session id", 404)
        with self.lock:
            session = self._sessions.get(session_id)
        if not session:
            raise SessionError("session not found", 404)
        return session

    def session_for_read_token(self, token: str) -> ManagedSession | None:
        token = str(token or "")
        if not token:
            return None
        with self.lock:
            sessions = list(self._sessions.values())
        for session in sessions:
            stored = str(session.meta.get("read_token") or "")
            if stored and hmac.compare_digest(stored, token):
                return session
        return None

    # ------------------------------------------------------------- worktrees
    def _active_worktree_sessions(self, path: str, *, exclude_id: str = "") -> list[str]:
        """Ids of *other* active sessions working in the same Git worktree."""
        target = str(path or "").strip()
        if not target:
            return []
        resolved = str(Path(target).resolve()) if target else ""
        with self.lock:
            candidates = list(self._sessions.values())
        out = []
        for session in candidates:
            if session.id == exclude_id:
                continue
            if str(session.meta.get("status") or "") not in ACTIVE_STATUSES:
                continue
            meta_worktree = session.meta.get("worktree") or {}
            if not isinstance(meta_worktree, dict):
                continue
            other = str(meta_worktree.get("path") or "")
            if not other:
                continue
            try:
                same = str(Path(other).resolve()) == resolved
            except (OSError, ValueError):
                same = other == target
            if same:
                out.append(session.id)
        return out

    def _resolve_worktree(self, ref: str, *, allow_shared: bool = False,
                          exclude_id: str = "") -> dict:
        if self.worktree_provider is None:
            raise SessionError("worktree sessions are not available on this server", 400)
        try:
            target = self.worktree_provider(ref)
        except SessionError:
            raise
        except Exception as exc:  # noqa: BLE001 - convert manager errors to 4xx
            status = int(getattr(exc, "status", 400) or 400)
            raise SessionError(str(exc), status, getattr(exc, "code", "")) from exc
        if not isinstance(target, dict) or not target.get("path"):
            raise SessionError("the worktree manager returned an invalid target", 400)
        conflicts = self._active_worktree_sessions(str(target["path"]), exclude_id=exclude_id)
        if conflicts and not allow_shared:
            raise SessionError(
                "another active session already uses this worktree "
                f"({', '.join(conflicts)}); stop it or explicitly allow a shared worktree",
                409,
            )
        return target

    def sessions_for_worktree(self, path: str, *, active_only: bool = False) -> list[str]:
        target = str(path or "").strip()
        if not target:
            return []
        resolved = str(Path(target).resolve())
        with self.lock:
            candidates = list(self._sessions.values())
        out = []
        for session in candidates:
            if active_only and str(session.meta.get("status") or "") not in ACTIVE_STATUSES:
                continue
            worktree = session.meta.get("worktree") or {}
            if isinstance(worktree, dict) and str(worktree.get("path") or ""):
                try:
                    if str(Path(worktree["path"]).resolve()) == resolved:
                        out.append(session.id)
                except (OSError, ValueError):
                    continue
        return out

    def groups(self) -> dict:
        """Group active agents by project and worktree, and flag conflicts."""
        with self.lock:
            sessions = list(self._sessions.values())
        buckets: dict[str, dict] = {}
        for session in sessions:
            meta = session.meta
            if str(meta.get("status") or "") not in ACTIVE_STATUSES:
                continue
            project = meta.get("project") or {}
            project_id = str(project.get("id") or project.get("path") or "project")
            worktree = meta.get("worktree") or {}
            path = str(worktree.get("path") or "") if isinstance(worktree, dict) else ""
            key = f"worktree:{path}" if path else f"project:{project_id}"
            bucket = buckets.get(key)
            if bucket is None:
                bucket = {
                    "id": key,
                    "kind": "worktree" if path else "project",
                    "label": (str(worktree.get("branch") or Path(path).name) if path
                              else str(project.get("name") or "Project")),
                    "project": {"id": project.get("id"), "name": project.get("name"),
                                "path": project.get("path")},
                    "worktree": ({"path": path, "branch": worktree.get("branch") or ""}
                                 if path else None),
                    "sessions": [],
                    "active": 0,
                }
                buckets[key] = bucket
            bucket["sessions"].append({
                "id": session.id,
                "title": meta.get("title") or "",
                "platform": meta.get("platform") or "",
                "status": meta.get("status") or "",
            })
            bucket["active"] += 1
        groups = list(buckets.values())
        for group in groups:
            # Private session workspaces are isolated; only a shared Git
            # worktree can produce genuinely conflicting tool runs.
            group["conflict"] = group["kind"] == "worktree" and group["active"] > 1
            group["status"] = "conflict" if group["conflict"] else "busy"
            group["session_ids"] = [item["id"] for item in group["sessions"]]
        groups.sort(key=lambda item: (0 if item["conflict"] else 1, item["label"].lower()))
        return {
            "groups": groups,
            "counts": {
                "groups": len(groups),
                "active": sum(group["active"] for group in groups),
                "conflicts": sum(1 for group in groups if group["conflict"]),
            },
        }

    def set_provider_catalog(self, catalog: Any) -> None:
        self.provider_catalog = catalog

    def presets_public(self, actor: str = "") -> list[dict]:
        overrides = self.agent_overrides(actor)
        out = []
        for base in self.presets:
            preset = self.preset(base["id"], actor)
            available = _preset_available(preset)
            override = overrides.get(preset["id"]) or {}
            if override.get("enabled") is False:
                available = False
            item = {
                "id": preset["id"],
                "label": preset["label"],
                "description": preset["description"],
                "available": available,
                "prompt_mode": preset["prompt_mode"],
                "followup": preset["followup"],
                "models": preset["models"],
                "efforts": preset["efforts"],
                # ``access_modes`` is kept as the legacy alias of the same list.
                "access_modes": preset["permission_modes"],
                "permission_modes": preset["permission_modes"],
                "permission_labels": {mode: PERMISSION_LABELS[mode]
                                      for mode in preset["permission_modes"]},
                "default_permission_mode": preset["default_permission_mode"],
                "supports_approval": preset["supports_approval"],
                "approval_modes": [mode for mode in APPROVAL_MODES
                                   if mode in preset["permission_modes"]],
                "unsupported_permission_modes": preset["unsupported_permission_modes"],
                "approval_protocol": APPROVAL_PROTOCOL,
                "demo": preset["demo"],
                "program": preset["command"][0],
                "command_source": override.get("command_source") or "global",
                "personal_override": bool(override),
                "configured_env": sorted((override.get("env") or {}).keys()),
            }
            if not available:
                item["unavailable_reason"] = (
                    "disabled in personal settings" if override.get("enabled") is False
                    else f"{preset['command'][0]} was not found")
            out.append(item)
        if self.provider_catalog is not None:
            try:
                from .providers import presets_with_provider_status
                out = presets_with_provider_status(out, self.provider_catalog)
            except Exception:  # noqa: BLE001 - discovery must not break launching
                pass
        return out

    # ----------------------------------------------------------------- payload
    @staticmethod
    def approval_public(record: dict) -> dict:
        status = str(record.get("status") or "pending")
        return {
            "id": record.get("id"),
            "kind": record.get("kind") or "other",
            "tool": record.get("tool") or "",
            "command": record.get("command") or "",
            "path": record.get("path") or "",
            "description": record.get("description") or "",
            "input": record.get("input"),
            "protocol": record.get("protocol") or APPROVAL_PROTOCOL,
            "status": status,
            "decision": record.get("decision") or "",
            "scope": record.get("scope") or "",
            "auto": bool(record.get("auto")),
            "message": record.get("message") or "",
            "mode": record.get("mode") or "",
            "requested_at": record.get("requested_at"),
            "decided_at": record.get("decided_at"),
            "decided_by": record.get("decided_by") or "",
            "pending": status == "pending",
            "active": status == "pending",
        }

    @classmethod
    def _approval_state(cls, meta: dict) -> dict:
        raw = [record for record in meta.get("approvals") or [] if isinstance(record, dict)]
        pending = [record for record in raw if record.get("status") == "pending"]
        items = [cls.approval_public(record) for record in pending]
        seen = {str(record.get("id") or "") for record in pending}
        budget = max(0, MAX_APPROVAL_ITEMS - len(items))
        for record in raw[-budget:] if budget else []:
            key = str(record.get("id") or "")
            if record.get("status") == "pending" or (key and key in seen):
                continue
            seen.add(key)
            items.append(cls.approval_public(record))
        items.sort(key=lambda item: str(item.get("requested_at") or ""))
        rules = [dict(rule) for rule in meta.get("approval_rules") or []
                 if isinstance(rule, dict)]
        return {"items": items, "pending": len(pending), "rules": rules}

    def _display_status(self, meta: dict) -> tuple[str, str]:
        """``(status, raw_status)``: a running session quiet for
        ``idle_after`` seconds shows as ``idle``."""
        raw_status = str(meta.get("status") or "stopped")
        status = raw_status
        if raw_status == "running":
            last = _parse_time(meta.get("last_activity_at"))
            if last is not None:
                idle_for = (datetime.now(timezone.utc) - last).total_seconds()
                if idle_for >= self.idle_after:
                    status = "idle"
        return status, raw_status

    def brief(self) -> list[dict]:
        """Cheap per-session summary (no presets, approvals or worktree
        scans) for Home's Running card."""
        with self.lock:
            sessions = list(self._sessions.values())
        out = []
        for session in sessions:
            with session.lock:
                meta = dict(session.meta)
            status, _raw = self._display_status(meta)
            out.append({
                "id": meta.get("id") or session.id, "title": meta.get("title") or "",
                "status": status, "agent": meta.get("agent") or "", "platform": meta.get("platform") or "",
                "task_ref": meta.get("task_ref") or "", "created_at": meta.get("created_at"),
                "last_activity_at": meta.get("last_activity_at"), "project": meta.get("project") or {},
            })
        return out

    def public(self, session: ManagedSession, *, detail: bool = False) -> dict:
        with session.lock:
            meta = dict(session.meta)
        status, raw_status = self._display_status(meta)
        access_mode = str(meta.get("access_mode") or "read_only")
        if access_mode not in PERMISSION_MODES:
            access_mode = "read_only"
        try:
            preset = self.preset(str(meta.get("platform") or ""))
            allowed_access = list(preset["permission_modes"])
            supports_approval = bool(preset["supports_approval"])
        except SessionError:
            allowed_access = list(PERMISSION_MODES)
            supports_approval = False
        approval_state = self._approval_state(meta)
        permission_history = []
        for entry in meta.get("permission_history") or []:
            if not isinstance(entry, dict):
                continue
            mode = str(entry.get("mode") or "")
            if mode not in PERMISSION_MODES:
                continue
            permission_history.append({
                "mode": mode,
                "label": PERMISSION_LABELS.get(mode, mode),
                "at": entry.get("at"),
                "turn": int(entry.get("turn") or 0),
                "source": entry.get("source") or "",
            })
        steer_queue = []
        for raw in meta.get("steer_queue") or []:
            if not isinstance(raw, dict):
                continue
            steer_queue.append({
                "id": raw.get("id"),
                "text": raw.get("text") or "",
                "status": raw.get("status") or "queued",
                "channel": raw.get("channel") or "next_turn",
                "created_at": raw.get("created_at"),
                "delivered_at": raw.get("delivered_at"),
                "edited_at": raw.get("edited_at"),
                "error": raw.get("error") or "",
            })
        worktree = meta.get("worktree")
        worktree_public = None
        if isinstance(worktree, dict) and worktree.get("path"):
            worktree_public = dict(worktree)
            others = self._active_worktree_sessions(str(worktree.get("path")), exclude_id=session.id)
            worktree_public["other_active_session_ids"] = others
            worktree_public["active_count"] = len(others) + 1
            worktree_public["conflict"] = bool(others)
            worktree_public["shared"] = bool(others)
            if others:
                worktree_public["status"] = "conflict"
        payload = {
            "id": meta.get("id"),
            "title": meta.get("title"),
            "platform": meta.get("platform"),
            "platform_label": meta.get("platform_label"),
            "model": meta.get("model") or "",
            "effort": meta.get("effort") or "",
            "access_mode": access_mode,
            "access_label": PERMISSION_LABELS.get(access_mode, access_mode),
            "permission_mode": access_mode,
            "permission_label": PERMISSION_LABELS.get(access_mode, access_mode),
            "permission_description": PERMISSION_DESCRIPTIONS.get(access_mode, ""),
            "permission_modes": allowed_access,
            "permission_history": permission_history,
            "turn": int(meta.get("turn") or 1),
            "approvals": {
                "items": approval_state["items"],
                "pending": approval_state["pending"],
                "rules": approval_state["rules"],
                "supported": supports_approval,
                "protocol": APPROVAL_PROTOCOL,
            },
            "agent": meta.get("agent") or "",
            "native_session_id": meta.get("native_session_id") or "",
            "linked_units": list(meta.get("linked_units") or []),
            "owner": meta.get("actor") or "",
            "shared": bool(meta.get("shared")),
            "task_ref": meta.get("task_ref") or "",
            "parent_id": meta.get("parent_id") or "",
            "relation": meta.get("relation") or "",
            "side_chat_of": meta.get("side_chat_of") or "",
            "forked_from": meta.get("forked_from") or "",
            "depth": int(meta.get("depth") or 0),
            "status": status,
            "raw_status": raw_status,
            "cwd": meta.get("cwd") or ".",
            "linux_account": meta.get("linux_account") or "",
            "worktree": worktree_public,
            "allow_shared_worktree": bool(meta.get("allow_shared_worktree")),
            "created_at": meta.get("created_at"),
            "started_at": meta.get("started_at"),
            "last_activity_at": meta.get("last_activity_at"),
            "ended_at": meta.get("ended_at"),
            "exit_code": meta.get("exit_code"),
            "pid": meta.get("pid"),
            "error": meta.get("error") or "",
            "notice": meta.get("notice") or "",
            "project": meta.get("project") or {},
            "steer_queue": steer_queue,
            "capabilities": {
                "followup": bool(meta.get("followup")),
                "stdin": bool(meta.get("followup")),
                "steer": True,
                "steer_channel": "stdin" if meta.get("followup") else "next_turn",
                "context": True,
                "access_modes": allowed_access,
                "permission_modes": allowed_access,
                "supports_approval": supports_approval,
                "approval_modes": [mode for mode in APPROVAL_MODES if mode in allowed_access],
                "approval_protocol": APPROVAL_PROTOCOL,
                "side_chat": True,
                "full_access": "full_access" in allowed_access,
                "read_only_project": True,
                "streams": ["stdout", "stderr"],
            },
            "counts": dict(meta.get("counts") or {"stdout": 0, "stderr": 0}),
            "logs": {
                "stdout": f"/api/agent-sessions/{meta.get('id')}/logs?stream=stdout",
                "stderr": f"/api/agent-sessions/{meta.get('id')}/logs?stream=stderr",
            },
            "artifacts": {
                "endpoint": f"/api/agent-sessions/{meta.get('id')}/artifacts",
                "download": f"/api/agent-sessions/{meta.get('id')}/artifacts/download",
            },
            "context": {
                "api": meta.get("api") or {},
                "file": "ark-context.json",
                "read_only": True,
            },
        }
        if detail:
            payload["prompt"] = meta.get("prompt") or ""
            payload["read_token"] = meta.get("read_token") or ""
            payload["command_preview"] = meta.get("command_preview") or ""
            payload["workspace"] = str(session.workspace)
            payload["read_env"] = meta.get("read_env") or {}
        return payload

    def list(self) -> dict:
        with self.lock:
            sessions = list(self._sessions.values())
        items = [self.public(session) for session in sessions]
        items.sort(key=lambda item: (STATUS_ORDER.get(item["status"], 9),
                                     _invert_time(item.get("last_activity_at"))))
        counts: dict[str, int] = {}
        for item in items:
            counts[item["status"]] = counts.get(item["status"], 0) + 1
        groups = self.groups()
        return {"sessions": items, "counts": counts, "total": len(items),
                "groups": groups["groups"], "group_counts": groups["counts"]}

    def effort_choices(self, preset: dict, model: str = "") -> list[str]:
        """Reasoning levels valid for ``model`` on ``preset``.

        Configured preset efforts win.  Otherwise the provider catalog's
        per-model levels (e.g. from ``codex debug models``) are used, then the
        provider-wide list; an empty list means effort is unsupported.
        """
        if preset.get("efforts"):
            return list(preset["efforts"])
        if self.provider_catalog is None:
            return []
        try:
            caps = (self.provider_catalog.metadata_for(preset["id"]) or {}).get("capabilities") or {}
        except Exception:  # noqa: BLE001 - discovery must not break launching
            return []
        options = caps.get("model_options") or []
        model = str(model or "").strip()
        match = next((item for item in options if item.get("id") == model), None) if model else             next((item for item in options if item.get("default")), None)
        if match and match.get("efforts"):
            return list(match["efforts"])
        return list(caps.get("efforts") or [])

    def _check_effort(self, preset: dict, model: str, effort: str) -> None:
        if not effort:
            return
        choices = self.effort_choices(preset, model)
        if not choices:
            raise SessionError("this platform does not expose a reasoning level", 400)
        if effort not in choices:
            raise SessionError(
                f"unknown reasoning level '{effort}'; expected one of {', '.join(choices)}")

    # -------------------------------------------------------------------- start
    def start(self, body: dict, *, base_url: str = "",
              actor: str = "") -> ManagedSession:
        preset = self.preset(str(body.get("platform") or ""), actor)
        if not _preset_available(preset):
            raise SessionError(f"{preset['label']} is not installed or configured", 400)
        title = str(body.get("title") or "").strip()[:MAX_TITLE] or f"{preset['label']} session"
        prompt = str(body.get("prompt") or "").strip()[:MAX_PROMPT]
        if not prompt and preset["id"] in {"dsh", "codex", "claude", "opencode", "cursor", "gemini"}:
            raise SessionError(f"{preset['label']} requires an initial prompt", 400)
        model = str(body.get("model") or "").strip()[:80]
        agent = str(body.get("agent") or "").strip()[:120]
        effort = str(body.get("effort") or "").strip()[:40]
        access_mode = self._validate_permission_mode(body.get("access_mode"), preset)
        self._check_effort(preset, model, effort)
        task_ref = str(body.get("task") or body.get("task_ref") or "").strip()[:40]
        cwd = _safe_relative(body.get("cwd") or "", "cwd")
        worktree_ref = str(body.get("worktree") or "").strip()
        worktree = None
        if worktree_ref:
            if cwd:
                raise SessionError("a worktree session starts at the worktree root; leave cwd empty", 400)
            worktree = self._resolve_worktree(
                worktree_ref,
                allow_shared=bool(body.get("allow_shared_worktree")),
            )
            if not Path(str(worktree["path"])).is_dir():
                raise SessionError("the selected worktree folder is missing", 409)
        parent_id = str(body.get("parent_id") or "").strip()[:40]
        parent = None
        if parent_id:
            parent = self.get(parent_id)
            if parent.meta.get("status") == "archived":
                raise SessionError("parent session is archived", 400)
            if int(parent.meta.get("depth") or 0) >= 3:
                raise SessionError("subagent nesting is limited to 3 levels", 400)
            if not task_ref:
                task_ref = str(parent.meta.get("task_ref") or "")[:40]
            if not cwd and parent.meta.get("cwd") not in (None, "", "."):
                cwd = _safe_relative(parent.meta.get("cwd"), "cwd")
        if preset["prompt_mode"] == "arg" and not any("{prompt}" in part for part in preset["command"]):
            raise SessionError("this platform's command has no {prompt} placeholder; use a stdin platform")

        session_id = self._new_id()
        folder = self.root / session_id
        workspace = folder / "workspace"
        artifacts = workspace / "artifacts"
        artifacts.mkdir(parents=True, exist_ok=True)
        if worktree is not None:
            cwd_dir = Path(str(worktree["path"]))
            if not cwd_dir.is_dir():
                raise SessionError("the selected worktree folder is missing", 409)
        else:
            cwd_dir = workspace / cwd if cwd else workspace
            cwd_dir.mkdir(parents=True, exist_ok=True)
            if cwd_dir.resolve() != workspace.resolve() and workspace.resolve() not in cwd_dir.resolve().parents:
                raise SessionError("cwd must stay inside the session workspace")
        cwd_label = "." if worktree is not None else (cwd or ".")

        project = dict(self.project_provider() or {})
        project_ref = {
            "id": project.get("id"),
            "name": project.get("name"),
            "path": project.get("path"),
        }
        read_token = secrets.token_urlsafe(24)
        api_base = f"{base_url.rstrip('/')}/api/task-agent/v1" if base_url else "/api/task-agent/v1"
        context = dict(self.context_provider() or {})
        context.setdefault("format", "ark-task-agent-context")
        context.setdefault("version", 1)
        context["generated_at"] = utc_now()
        context["read_only"] = True
        context["project"] = project_ref
        context["session"] = {"id": session_id, "platform": preset["id"],
                              "task_ref": task_ref, "model": model, "agent": agent,
                              "effort": effort, "access_mode": access_mode,
                              "permission_mode": access_mode,
                              "supports_approval": bool(preset["supports_approval"])}
        context["permission"] = self._permission_context(preset, access_mode)
        if worktree is not None:
            context["session"]["worktree"] = {
                "path": worktree["path"],
                "branch": worktree.get("branch") or "",
            }
        context["api"] = {"base": api_base, "token_env": "ARK_READ_TOKEN",
                          "header": "X-ARK-Read-Token", "methods": ["GET"]}
        _write_json(workspace / "ark-context.json", context)
        (workspace / "ARK-TASK.md").write_text(
            self._contract(session_id, preset, project_ref, task_ref, cwd_label, api_base,
                           access_mode, worktree=worktree, workspace=workspace),
            encoding="utf-8",
        )

        meta = {
            "id": session_id,
            "title": title,
            "platform": preset["id"],
            "platform_label": preset["label"],
            "model": model,
            "effort": effort,
            "access_mode": access_mode,
            "permission_mode": access_mode,
            "permission_history": [{"mode": access_mode, "at": utc_now(),
                                    "turn": 1, "source": "start"}],
            "turn": 1,
            "approvals": [],
            "approval_rules": [],
            "agent": agent,
            "task_ref": task_ref,
            "parent_id": parent_id,
            "relation": "",
            "side_chat_of": "",
            "forked_from": "",
            "depth": int(parent.meta.get("depth") or 0) + 1 if parent else 0,
            "status": "starting",
            "cwd": cwd_label,
            "worktree": (dict(worktree) if worktree is not None else None),
            "allow_shared_worktree": bool(body.get("allow_shared_worktree")) if worktree is not None else False,
            "created_at": utc_now(),
            "started_at": None,
            "last_activity_at": utc_now(),
            "ended_at": None,
            "exit_code": None,
            "pid": None,
            "error": "",
            "notice": "",
            "project": project_ref,
            "followup": bool(preset["followup"]),
            "prompt": prompt,
            "read_token": read_token,
            "api": {"base": api_base, "token_env": "ARK_READ_TOKEN",
                    "header": "X-ARK-Read-Token", "methods": ["GET"]},
            "read_env": {
                "ARK_PROJECT_PATH": project_ref.get("path"),
                "ARK_TASK_PROJECT": project_ref.get("path"),
                "ARK_TASK_CONTEXT": "workspace/ark-context.json",
                "ARK_READ_TOKEN": "read-only session token",
                "ARK_TASK_API_BASE": api_base,
            },
            "counts": {"stdout": 0, "stderr": 0, "bytes": 0},
            "actor": actor,
        }
        session = ManagedSession(self, folder, meta)
        with self.lock:
            self._sessions[session_id] = session
        self.save(session)

        return self._spawn(session, preset, prompt, cwd_dir=cwd_dir, workspace=workspace,
                           artifacts=artifacts, project_ref=project_ref, api_base=api_base,
                           read_token=read_token, record_prompt=False, actor=actor)

    def _spawn(self, session: ManagedSession, preset: dict, prompt: str, *, cwd_dir: Path,
               workspace: Path, artifacts: Path, project_ref: dict, api_base: str,
               read_token: str, record_prompt: bool = False,
               actor: str = "") -> ManagedSession:
        meta = session.meta
        model = str(meta.get("model") or "")
        actor = actor or str(meta.get("actor") or "")
        env = self._build_env(session, project_ref, api_base, read_token, workspace,
                              artifacts, actor=actor)
        values = {
            "prompt": prompt,
            "model": model,
            "effort": str(meta.get("effort") or ""),
            "workspace": str(cwd_dir),
            "project": str(project_ref.get("path") or ""),
            "context": str(workspace / "ark-context.json"),
            "artifacts": str(artifacts),
            "api_base": api_base,
            "session": session.id,
        }
        argv = [_expand(part, values) for part in preset["command"]]
        resume_id = str(meta.pop("resume_native_id", "") or "")
        if resume_id:
            argv = _resume_argv(argv, preset["id"], resume_id, prompt)
        if (preset["id"] == "codex" and model
                and "-m" not in argv and "--model" not in argv):
            # Codex accepts -m/--model.  Insert it before the prompt rather
            # than requiring operators to write a custom command template.
            try:
                at = argv.index("exec") + 1
            except ValueError:
                at = 1
            argv[at:at] = ["-m", model]
        effort = str(meta.get("effort") or "")
        if (preset["id"] == "codex" and effort
                and not any("model_reasoning_effort" in part for part in argv)):
            # ``-c key=value``: a bare word is not valid TOML, so Codex keeps
            # it as a literal string.
            try:
                at = argv.index("exec") + 1
            except ValueError:
                at = 1
            argv[at:at] = ["-c", f"model_reasoning_effort={effort}"]
        if preset["id"] == "claude" and len(argv) > 1:
            # Claude Code accepts --model and --effort (verified in
            # ``claude --help``); add them right after the program.
            extra = []
            if model and "--model" not in argv:
                extra += ["--model", model]
            if effort and "--effort" not in argv:
                extra += ["--effort", effort]
            argv[1:1] = extra
        # Optional Linux identity isolation: an admin-assigned account is used
        # via a password-free sudo wrapper.  Never fall back to the server
        # account when a selection existed but is no longer usable.
        previous_account = str(session.meta.get("linux_account") or "")
        selected_account = ""
        if self.linux_account_provider is not None:
            try:
                selected_account = str(self.linux_account_provider(actor) or "").strip()
            except Exception:  # noqa: BLE001 - a settings read must not crash launch
                selected_account = ""
        if previous_account and selected_account != previous_account:
            with session.lock:
                meta["status"] = "errored"
                meta["error"] = (
                    f"the assigned Linux account '{previous_account}' is no "
                    "longer selected or assigned; relaunch after an admin "
                    "reassigns it")
                meta["ended_at"] = utc_now()
            self.save(session)
            raise SessionError(str(meta["error"]), 409, "LINUX_ACCOUNT_REVOKED")
        if selected_account:
            try:
                info = linux_account.preflight(selected_account,
                                               workspace=str(cwd_dir))
            except linux_account.LinuxAccountError as exc:
                with session.lock:
                    meta["status"] = "errored"
                    meta["error"] = exc.message
                    meta["ended_at"] = utc_now()
                self.save(session)
                raise SessionError(exc.message, exc.status, exc.code) from exc
            argv = linux_account.wrap_command(argv, selected_account,
                                              home=info["home"])
            with session.lock:
                meta["linux_account"] = selected_account
                meta["linux_account_home"] = info["home"]
        meta["command_preview"] = _preview(argv, prompt)
        command_kwargs: dict[str, Any] = {}
        if os.name == "nt":
            command_kwargs["creationflags"] = (
                getattr(subprocess, "CREATE_NEW_PROCESS_GROUP", 0)
                | getattr(subprocess, "CREATE_NO_WINDOW", 0)
            )
        else:
            command_kwargs["start_new_session"] = True
        try:
            proc = subprocess.Popen(
                argv,
                cwd=str(cwd_dir),
                env=env,
                stdin=subprocess.PIPE,
                stdout=subprocess.PIPE,
                stderr=subprocess.PIPE,
                bufsize=-1,
                **command_kwargs,
            )
        except (OSError, ValueError) as exc:
            with session.lock:
                meta["status"] = "errored"
                meta["error"] = f"could not start process: {exc}"
                meta["ended_at"] = utc_now()
            self.save(session)
            raise SessionError(f"Could not start {preset['label']}: {exc}") from exc

        with session.lock:
            session.proc = proc
            meta["status"] = "running"
            meta["pid"] = proc.pid
            meta["started_at"] = utc_now()
            meta["last_activity_at"] = meta["started_at"]
        self.save(session)
        session.start_readers()
        if prompt and preset["prompt_mode"] == "stdin":
            try:
                session.write_input(prompt, record=record_prompt)
            except SessionError:
                pass
        if not preset["followup"] and proc.stdin is not None:
            # One-shot agents get EOF after the prompt so they can finish.
            try:
                proc.stdin.close()
            except (OSError, ValueError):
                pass
        return session

    def _build_env(self, session: ManagedSession, project_ref: dict, api_base: str,
                   read_token: str, workspace: Path, artifacts: Path,
                   actor: str = "") -> dict:
        """Copy the host env minus every ARK write credential and secret value.

        The current user's effective settings are applied last: a task-agent
        command override may name credential environment variables, and the
        values are that user's own sealed settings only.  Values are never
        written to session metadata, logs or the command preview.
        """
        env: dict[str, str] = {}
        for key, value in os.environ.items():
            if _is_write_credential(key):
                continue
            # Do not hand the global server account's provider credentials to a
            # task process.  The launching user's effective settings are applied
            # below; a shared key would silently make every person the operator.
            if _is_credential_env(key):
                continue
            text = str(value)
            if any(secret and secret in text for secret in self.strip_env_values):
                continue
            env[key] = text
        access_mode = str(session.meta.get("access_mode") or "read_only")
        env.update({
            "ARK_TASK_SESSION": session.id,
            "ARK_TASK_PROJECT": str(project_ref.get("path") or ""),
            "ARK_PROJECT_PATH": str(project_ref.get("path") or ""),
            "ARK_TASK_CONTEXT": str(workspace / "ark-context.json"),
            "ARK_TASK_ARTIFACTS": str(artifacts),
            "ARK_TASK_API_BASE": api_base,
            "ARK_READ_TOKEN": read_token,
            "ARK_TASK_ACCESS_MODE": access_mode,
            "ARK_TASK_PERMISSION_MODE": access_mode,
            "ARK_TASK_APPROVAL_PROTOCOL": APPROVAL_PROTOCOL,
            "ARK_TASK_MODEL": str(session.meta.get("model") or ""),
            "ARK_TASK_EFFORT": str(session.meta.get("effort") or ""),
            "PYTHONUNBUFFERED": "1",
            "PYTHONIOENCODING": "utf-8",
        })
        if os.name != "nt":
            # A server started by SSH/systemd may not inherit the user's
            # interactive PATH.  Prefer unconfined npm/pip installs over the
            # Snap codex wrapper, which cannot read /mnt or the task workspace.
            home = Path.home()
            extras = [home / ".local" / "bin", home / "bin", Path("/usr/local/bin")]
            current = env.get("PATH") or os.environ.get("PATH") or ""
            path_parts = [str(path) for path in extras if path.is_dir()]
            path_parts.extend(part for part in current.split(os.pathsep) if part)
            seen = set()
            env["PATH"] = os.pathsep.join(
                part for part in path_parts
                if not (part in seen or seen.add(part))
            )
        if access_mode == "full_access":
            # Full access is an explicit, per-session escalation: the agent gets
            # ARK's write token and may call the same /api/agent/v1 write API as
            # the UI.  The read-only task-agent token never changes.
            if self.write_token:
                env["ARK_AGENT_TOKEN"] = self.write_token
            env["ARK_TASK_FULL_ACCESS"] = "1"
        else:
            # No approval mode ever receives ARK's project write token: ARK
            # cannot intercept an adapter's own filesystem calls, so the token
            # stays reserved for the explicit full-access escalation.
            env["ARK_TASK_READ_ONLY"] = "1" if access_mode in READ_ONLY_MODES else "0"
            if access_mode == "plan":
                env["ARK_TASK_PLAN_MODE"] = "1"
            if access_mode in APPROVAL_MODES:
                env["ARK_TASK_APPROVALS"] = "1"
                env["ARK_TASK_APPROVAL_MODE"] = (
                    "edits" if access_mode == "accept_edits" else "all")
        worktree = session.meta.get("worktree") or {}
        if isinstance(worktree, dict) and worktree.get("path"):
            env["ARK_TASK_WORKTREE"] = str(worktree.get("path"))
            env["ARK_TASK_WORKTREE_BRANCH"] = str(worktree.get("branch") or "")
        if session.meta.get("platform") == "dsh":
            # DSH reads the sandbox/approval bundle at profile startup.  ARK's
            # access mode must reach the launcher as DSH_PERMISSION_MODE; merely
            # setting ARK_TASK_FULL_ACCESS does not change DSH's sandbox.
            env["DSH_PERMISSION_MODE"] = (
                "danger-full-access" if access_mode == "full_access"
                else "read-only" if access_mode in {"read_only", "plan"}
                else "workspace-write")
            # DSH headless reads the model credential from this dedicated env
            # var.  Resolve the *current user's* effective assistant config so a
            # shared server never hands one person's key to another person's
            # task session.
            try:
                if self.assistant_config_provider is not None:
                    config = self.assistant_config_provider(actor) or {}
                else:
                    config = assistant_config.load()
                if config.get("api_key"):
                    env["TENCENTHUB_API_KEY"] = config["api_key"]
            except (OSError, ValueError):
                pass
        # Per-user agent overrides: an explicit ``env`` mapping carries this
        # user's own credential values (from the sealed personal settings).
        # Apply it last so the selected provider gets the right account and so
        # no host-level value silently replaces it.
        override = self.agent_overrides(actor).get(
            str(session.meta.get("platform") or "")) or {}
        for name, value in (override.get("env") or {}).items():
            if not name or not isinstance(value, str):
                continue
            env[str(name)] = value
        return env

    @staticmethod
    def _permission_context(preset: dict, access_mode: str) -> dict:
        return {
            "mode": access_mode,
            "label": PERMISSION_LABELS.get(access_mode, access_mode),
            "description": PERMISSION_DESCRIPTIONS.get(access_mode, ""),
            "supports_approval": bool(preset["supports_approval"]),
            "approval_protocol": APPROVAL_PROTOCOL,
            "ark_write_token": access_mode == "full_access",
        }

    def _contract(self, session_id: str, preset: dict, project: dict, task_ref: str,
                  cwd: str, api_base: str, access_mode: str = "read_only", *,
                  worktree: dict | None = None,
                  workspace: Path | None = None) -> str:
        label = PERMISSION_LABELS.get(access_mode, access_mode)
        artifacts_hint = (f"`{workspace / 'artifacts'}`" if workspace is not None
                          else "`./artifacts/`")
        has_worktree = worktree is not None
        write_scope = ("this session workspace or the selected Git worktree"
                       if has_worktree else "this session workspace")
        lines = [
            f"# ARK task session `{session_id}`\n",
            f"You are a task agent managed by ARK Studio ({preset['label']}).",
            f"Permission mode: **{label}** — {PERMISSION_DESCRIPTIONS.get(access_mode, '')}\n",
        ]
        if access_mode in READ_ONLY_MODES:
            lines.extend([
                "## This ARK project is read-only\n",
                f"- Project: **{project.get('name') or '(unnamed)'}** (`{project.get('path')}`)",
                "- Do **not** modify `project.json`, `ark.sqlite3`, `assets/`, or any other file in the project folder.",
                "- The inner ARK assistant is the only writer of ARK project data. If a change is needed, propose it in `./artifacts/`.",
                f"- Read-only API base: `{api_base}`",
                "- Read token: environment variable `ARK_READ_TOKEN`; header `X-ARK-Read-Token: <token>`.",
                "- The ARK write token is never provided to this process.\n",
            ])
            if has_worktree:
                lines.append(
                    "- Editing files inside the selected Git worktree is part of this task; "
                    "the ARK project folder above remains read-only.\n")
            if access_mode == "plan":
                lines.extend([
                    "## Plan mode\n",
                    "Produce a concrete plan (steps, files, risks, acceptance checks) in your reply.",
                    "Do not perform the edits or commands the plan describes; ARK withholds the write token.",
                    ("Do not edit the selected Git worktree in plan mode either.\n"
                     if has_worktree else
                     "This is read-only enforcement plus a planning contract, not a separate execution sandbox.\n"),
                ])
        elif access_mode in APPROVAL_MODES:
            lines.extend([
                "## Mutating actions need approval\n",
                "ARK cannot intercept your filesystem calls. You must ask before every mutating tool",
                "or command by emitting an approval request and waiting for the matching response.",
                "File edits are auto-approved in accept-edits mode; commands still wait for a decision.",
                "In ask mode every request waits for the user.",
                "The ARK project write token is **not** provided in this mode; ARK project data remains read-only.",
                f"Write only inside {write_scope}.\n",
                "### Approval protocol (`" + APPROVAL_PROTOCOL + "`)\n",
                "Request line (stdout):",
                "```text",
                '[[ark-event]] {"type":"permission_request","id":"req-1","kind":"command",',
                '               "tool":"bash","command":"pytest -q","input":{"command":"pytest -q"}}',
                "```",
                "Decision line (stdin):",
                "```json",
                '{"type":"permission_response","protocol":"ark-permission-v1","id":"req-1",',
                ' "request_id":"req-1","decision":"allow","scope":"once","message":""}',
                "```",
                "Wait for the response before acting. `decision` is `allow` or `deny`; `scope` is",
                "`once`, `always` (a user rule for this tool) or `auto` (mode policy).",
                "Claude Code stream-json `control_request`/`can_use_tool` frames are also recognised",
                "and answered with a matching `control_response`.\n",
            ])
        else:
            lines.extend([
                "## Full access\n",
                f"- Project: **{project.get('name') or '(unnamed)'}** (`{project.get('path')}`)",
                "- ARK's write token is provided as `ARK_AGENT_TOKEN` for `/api/agent/v1/...`.",
                "- Writes are logged as activity; use them deliberately and only for this project.",
                "### GPU queue",
                "Do not choose a GPU with `nvidia-smi` and do not SSH to a GPU host yourself.",
                "Describe the job and let the queue allocate it:",
                "```http",
                "POST /api/task-agent/v1/gpu/jobs",
                "X-ARK-Read-Token: $ARK_READ_TOKEN",
                "Content-Type: application/json",
                "```",
                "Example body: `{\"job\":\"train\",\"project\":\"ark\",\"gpu_count\":1,`",
                "`\"min_free_memory_mb\":12000,\"command\":\"python train.py\",`",
                "`\"work_dir\":\"/absolute/path/on/the/gpu/host\"}`.",
                "Poll `GET /api/task-agent/v1/gpu/jobs/<id>` and read its logs.",
                "The queue handles GPU selection, path mapping, environment setup and SSH launch.\n",
            ])
        if has_worktree:
            lines.extend([
                "## Git worktree\n",
                f"- Working tree: `{worktree.get('path')}`",
                f"- Branch: `{worktree.get('branch') or '(detached)'}`",
                f"- Repository: `{((worktree.get('repository') or {}).get('root') or '')}`",
                ("- Editing this worktree is expected except in plan mode."
                 if access_mode == "plan" else
                 "- Editing this worktree is expected."),
                "- Commit only in this worktree, and never push, merge, or delete the worktree.\n",
            ])
        lines.extend([
            "## ARK context\n",
            "- `./ark-context.json` — generated snapshot (project summary, units, links, permission mode).",
            "- Fresh API: `GET /context`, `GET /project`, `GET /units`, `GET /links`, `GET /snapshot`.",
            f"- Read token: `ARK_READ_TOKEN`; header `X-ARK-Read-Token: <token>`; API base `{api_base}`.\n",
            "## Workspace\n",
            f"- cwd: `{cwd}`" + (" (Git worktree root)" if has_worktree
                                  else " (inside the private session workspace)"),
            f"- Session workspace artifacts: {artifacts_hint}",
            f"- Task reference: {task_ref or '(unassigned)'}\n",
        ])
        return "\n".join(lines)

    def _refresh_workspace_permission(self, session: ManagedSession, preset: dict,
                                      workspace: Path, project_ref: dict, api_base: str,
                                      cwd: str, read_token: str) -> None:
        """Refresh the on-disk contract after a turn changes permission mode."""
        access_mode = str(session.meta.get("access_mode") or "read_only")
        context = _read_json(workspace / "ark-context.json") or {}
        context.setdefault("format", "ark-task-agent-context")
        context.setdefault("version", 1)
        context.setdefault("project", project_ref)
        context.setdefault("api", {"base": api_base, "token_env": "ARK_READ_TOKEN",
                                   "header": "X-ARK-Read-Token", "methods": ["GET"]})
        context["generated_at"] = utc_now()
        context["read_only"] = access_mode in READ_ONLY_MODES
        context["session"] = {
            "id": session.id, "platform": preset["id"],
            "task_ref": session.meta.get("task_ref") or "",
            "model": session.meta.get("model") or "",
            "agent": session.meta.get("agent") or "",
            "effort": session.meta.get("effort") or "",
            "access_mode": access_mode,
            "permission_mode": access_mode,
            "supports_approval": bool(preset["supports_approval"]),
        }
        context["permission"] = self._permission_context(preset, access_mode)
        worktree = session.meta.get("worktree")
        if not (isinstance(worktree, dict) and worktree.get("path")):
            worktree = None
        else:
            context["session"]["worktree"] = {
                "path": worktree.get("path"),
                "branch": worktree.get("branch") or "",
            }
        _write_json(workspace / "ark-context.json", context)
        (workspace / "ARK-TASK.md").write_text(
            self._contract(session.id, preset, project_ref,
                           str(session.meta.get("task_ref") or ""), cwd, api_base,
                           access_mode, worktree=worktree, workspace=workspace),
            encoding="utf-8",
        )

    # -------------------------------------------------------------- operations
    def request_stop(self, session_id: str) -> dict:
        session = self.get(session_id)
        with session.lock:
            if session.proc is None or session.proc.poll() is not None:
                if str(session.meta.get("status")) in ACTIVE_STATUSES:
                    session.finish(session.proc.poll() if session.proc else None)
                return self.public(session)
            if str(session.meta.get("status")) == "stopping":
                return self.public(session)
            session.stop_requested = True
            session.meta["status"] = "stopping"
        self.save(session)
        threading.Thread(target=self._terminate, args=(session,),
                         name=f"ark-session-{session_id}-stop", daemon=True).start()
        return self.public(session)

    def _terminate(self, session: ManagedSession) -> None:
        proc = session.proc
        if proc is None or proc.poll() is not None:
            return
        try:
            # Windows: taskkill /T /F.  POSIX: SIGTERM -> SIGKILL on the
            # session's process group, guarded against ARK's own group.
            platform_support.kill_process_tree(proc.pid)
        except (OSError, ValueError, subprocess.SubprocessError):
            try:
                proc.terminate()
            except OSError:
                pass
        try:
            proc.wait(timeout=6)
        except subprocess.TimeoutExpired:
            try:
                proc.kill()
            except OSError:
                pass

    # --------------------------------------------------------------- context
    @staticmethod
    def _context_public(entry: dict) -> dict:
        return {
            "id": entry.get("id"),
            "kind": entry.get("kind"),
            "label": entry.get("label"),
            "uid": entry.get("uid") or "",
            "path": entry.get("path") or "",
            "chars": int(entry.get("chars") or 0),
        }

    def _context_entries(self, session: ManagedSession,
                         attachments: Any) -> list[dict]:
        """Validate context attachments against the live project and workspace.

        Every path is resolved through the same scoped resolver as the artifact
        browser, so ``..``, absolute paths and escaping symlinks are rejected
        before anything is appended to a prompt.
        """
        if attachments in (None, "", []):
            return []
        if not isinstance(attachments, list):
            raise SessionError("context must be a list")
        if len(attachments) > MAX_CONTEXT_ITEMS:
            raise SessionError(f"at most {MAX_CONTEXT_ITEMS} context attachments are allowed")
        entries: list[dict] = []
        seen: set[str] = set()
        total = 0
        for raw in attachments:
            if not isinstance(raw, dict):
                raise SessionError("each context attachment must be an object")
            kind = str(raw.get("kind") or raw.get("type") or "").strip().lower()
            uid = ""
            rel_path = ""
            content = ""
            label = ""
            if kind in {"unit", "task"}:
                kind = "unit"
                uid = str(raw.get("uid") or raw.get("id") or "").strip()
                if not re.fullmatch(r"[A-Za-z][A-Za-z0-9_-]{1,63}", uid):
                    raise SessionError("a context unit needs a valid uid")
                unit = None
                if self.unit_provider is not None:
                    try:
                        unit = self.unit_provider(uid)
                    except Exception:  # noqa: BLE001 - a missing unit is a 404
                        unit = None
                if isinstance(unit, dict) and isinstance(unit.get("unit"), dict):
                    unit = unit["unit"]
                if not isinstance(unit, dict) or not unit:
                    raise SessionError(f"context unit {uid} was not found", 404)
                title = str(unit.get("title") or uid)
                label = f"{str(unit.get('uid') or uid)} · {title}"[:160]
                body = str(unit.get("body") or unit.get("excerpt") or "").strip()
                content = body[:MAX_CONTEXT_TEXT]
                lines = [
                    f"### Project unit `{str(unit.get('uid') or uid)}`",
                    f"- title: {title}",
                    f"- type: {unit.get('type') or 'note'}",
                    f"- status: {unit.get('status') or '—'}",
                ]
                if content:
                    lines.extend(["", content])
                text = "\n".join(lines)
                key = f"unit:{uid}"
            elif kind in {"file", "textfile"}:
                kind = "file"
                raw_path = str(raw.get("path") or "").strip()
                if not raw_path:
                    raise SessionError("a context file needs a path")
                target, rel_path = self._artifact_target(session, raw_path)
                if not target.is_file():
                    raise SessionError(f"context file not found: {rel_path}", 404)
                size = target.stat().st_size
                if size > MAX_TEXT_ARTIFACT:
                    raise SessionError(f"context file is too large to attach: {rel_path}", 413)
                try:
                    blob = target.read_bytes()
                except OSError as exc:
                    raise SessionError(f"cannot read context file: {exc}") from exc
                if b"\x00" in blob:
                    raise SessionError("binary file; attach it as media instead")
                content = blob.decode("utf-8", "replace")[:MAX_CONTEXT_TEXT]
                language = session_transcript.language_for(target.name)
                label = rel_path
                text = f"### File `{rel_path}`\n\n```{language}\n{content}\n```"
                key = f"file:{rel_path}"
            elif kind in {"media", "image", "video", "audio"}:
                kind = "media"
                raw_path = str(raw.get("path") or "").strip()
                if not raw_path:
                    raise SessionError("a context media attachment needs a path")
                target, rel_path = self._artifact_target(session, raw_path)
                if not target.is_file():
                    raise SessionError(f"context media not found: {rel_path}", 404)
                size = target.stat().st_size
                label = rel_path
                mime = mimetypes.guess_type(target.name)[0] or "application/octet-stream"
                content = f"{mime}, {size} bytes"
                text = (
                    f"### Media `{rel_path}`\n"
                    f"- mime: {mime}\n- size: {size} bytes\n"
                    "- The file lives in this session's private workspace; open it "
                    "through the ARK artifact API if the platform supports images."
                )
                key = f"media:{rel_path}"
            elif kind in {"text", "snippet"}:
                kind = "text"
                content = str(raw.get("text") or "").strip()
                if not content:
                    raise SessionError("a text snippet cannot be empty")
                content = content[:MAX_CONTEXT_TEXT]
                first_line = content.splitlines()[0].strip()[:60]
                label = (str(raw.get("label") or "").strip()[:80] or first_line or "Text snippet")
                text = f"### Snippet `{label}`\n\n```text\n{content}\n```"
                key = f"text:{content[:120]}"
            else:
                raise SessionError(
                    "context kind must be unit, file, media, or text")
            if key in seen:
                continue
            seen.add(key)
            total += len(text)
            if total > MAX_CONTEXT_TOTAL:
                raise SessionError("attached context is too large")
            entries.append({
                "id": f"ctx-{len(entries) + 1}",
                "kind": kind,
                "label": label,
                "uid": uid,
                "path": rel_path,
                "chars": len(content),
                "text": text,
            })
        return entries

    def validate_context(self, session_id: str, attachments: Any) -> dict:
        session = self.get(session_id)
        entries = self._context_entries(session, attachments)
        return {
            "session": session.id,
            "items": [self._context_public(entry) for entry in entries],
            "count": len(entries),
        }

    @staticmethod
    def _render_context(entries: list[dict]) -> str:
        if not entries:
            return ""
        return "\n\n## Attached context\n\n" + "\n\n".join(
            str(entry.get("text") or "") for entry in entries)

    @staticmethod
    def _context_marker(entries: list[dict]) -> str:
        if not entries:
            return ""
        labels = ", ".join(str(entry.get("label") or entry.get("kind")) for entry in entries)
        return f"\n\n_[context attached: {len(entries)} item(s) — {labels}]_"

    def _compose_message(self, text: str, entries: list[dict]) -> str:
        body = str(text or "").strip("\r\n")
        if not body and entries:
            body = "Use the attached context."
        return body + self._render_context(entries)

    # --------------------------------------------------------------- approvals
    @staticmethod
    def _find_approval(session: ManagedSession, request_id: str) -> dict | None:
        target = str(request_id or "")
        for record in session.meta.get("approvals") or []:
            if isinstance(record, dict) and str(record.get("id")) == target:
                return record
        return None

    @staticmethod
    def _match_approval_rule(session: ManagedSession, record: dict) -> dict | None:
        kind = str(record.get("kind") or "")
        tool = str(record.get("tool") or "").strip().lower()
        for rule in session.meta.get("approval_rules") or []:
            if not isinstance(rule, dict):
                continue
            if str(rule.get("kind") or "") != kind:
                continue
            if str(rule.get("tool") or "").strip().lower() == tool:
                return rule
        return None

    def _approval_policy(self, session: ManagedSession,
                         record: dict) -> tuple[str | None, str, str]:
        """ARK-side policy for a request that arrived on stdout.

        ``accept_edits`` auto-approves file edits; every command still waits.
        Read-only and plan modes deny mutating requests outright.  Full access
        auto-approves because the user already opted out of prompts.
        """
        mode = str(session.meta.get("access_mode") or "read_only")
        kind = str(record.get("kind") or "other")
        label = PERMISSION_LABELS.get(mode, mode)
        if mode == "full_access":
            return "allow", "auto", "full-access mode auto-approves mutating actions"
        if mode in READ_ONLY_MODES:
            return "deny", "auto", f"{label} mode denies mutating actions"
        if mode in APPROVAL_MODES:
            if mode == "accept_edits" and kind in EDIT_KINDS:
                return "allow", "auto", "accept-edits mode auto-approves file edits"
            if self._match_approval_rule(session, record) is not None:
                return "allow", "always", "matched an always-allow rule for this tool"
        return None, "", ""

    def _deliver_permission_response(self, session: ManagedSession, record: dict,
                                     decision: str | None = None, scope: str | None = None,
                                     message: str | None = None) -> bool:
        decision = str(decision or record.get("decision") or "").lower()
        if decision not in {"allow", "deny"}:
            return False
        frame = session_transcript.build_permission_response(
            record, decision, scope or record.get("scope") or "once",
            message if message is not None else str(record.get("message") or ""))
        try:
            session.write_input(json.dumps(frame, ensure_ascii=False), record=False)
        except SessionError:
            return False
        return True

    def observe_native_id(self, session: ManagedSession, line: str) -> str:
        """Record the provider's own session id (Claude session_id / Codex thread)."""
        if session.meta.get("native_session_id"):
            return ""
        found = native_session_id_from_line(line)
        if not found:
            return ""
        with session.lock:
            session.meta["native_session_id"] = found
        self.save(session)
        return found

    def resumable(self, session: ManagedSession) -> bool:
        return bool(session.meta.get("native_session_id")) and \
            str(session.meta.get("platform") or "") in NATIVE_RESUME_PLATFORMS

    def update_context(self, session_id: str, body: dict) -> dict:
        """Rename a session or link it to project nodes (Session context UI)."""
        session = self.get(session_id)
        with session.lock:
            if "title" in body:
                title = str(body.get("title") or "").strip()[:MAX_TITLE]
                if title:
                    session.meta["title"] = title
            if "linked_units" in body:
                links = body.get("linked_units")
                if isinstance(links, str):
                    links = [part.strip() for part in links.split(",")]
                if not isinstance(links, (list, tuple)):
                    raise SessionError("linked_units must be a list of node ids")
                session.meta["linked_units"] = [
                    str(uid).strip()[:40] for uid in links
                    if re.fullmatch(r"[A-Za-z0-9_.:-]+", str(uid).strip() or "-")][:20]
            if "shared" in body:
                if not isinstance(body.get("shared"), bool):
                    raise SessionError("shared must be true or false", 400, "SESSION_SHARED_INVALID")
                session.meta["shared"] = body["shared"]
        self.save(session)
        self.notify_change(session, "update")
        return self.public(session)

    def observe_output_line(self, session: ManagedSession, line: str) -> None:
        """Record approval requests/resolutions emitted by an adapter line."""
        request = session_transcript.permission_request_from_line(line)
        if request is not None:
            self._register_permission_request(session, request)
            return
        resolution = session_transcript.permission_resolution_from_line(line)
        if resolution is None:
            return
        changed = False
        with session.lock:
            record = self._find_approval(session, resolution.get("request_id"))
            if record is not None and record.get("status") == "pending":
                record["status"] = str(resolution.get("status") or "resolved")
                record["message"] = str(resolution.get("message") or "")
                record["decided_at"] = utc_now()
                record["decided_by"] = "adapter"
                changed = True
        if changed:
            session.save()

    def _register_permission_request(self, session: ManagedSession, request: dict) -> dict:
        request = dict(request)
        request_id = str(request.get("request_id") or "").strip()
        with session.lock:
            existing = self._find_approval(session, request_id) if request_id else None
            if existing is not None:
                return existing
            if not request_id:
                request_id = "ap_" + secrets.token_hex(6)
                request["request_id"] = request_id
            request_input = request.get("input")
            try:
                encoded_input = json.dumps(request_input, ensure_ascii=False)
            except (TypeError, ValueError):
                encoded_input = str(request_input)
            if len(encoded_input) > 8000:
                request_input = {"truncated": encoded_input[:8000],
                                 "original_chars": len(encoded_input)}
            record = {
                "id": request_id,
                "kind": str(request.get("kind") or "other"),
                "tool": str(request.get("tool") or ""),
                "command": str(request.get("command") or ""),
                "path": str(request.get("path") or ""),
                "description": str(request.get("description") or ""),
                "input": request_input,
                "protocol": str(request.get("protocol") or APPROVAL_PROTOCOL),
                "native_id": str(request.get("native_id") or ""),
                "status": "pending",
                "decision": "",
                "scope": "",
                "auto": False,
                "message": "",
                "mode": str(session.meta.get("access_mode") or "read_only"),
                "requested_at": utc_now(),
                "decided_at": None,
                "decided_by": "",
            }
            approvals = session.meta.setdefault("approvals", [])
            if len(approvals) >= MAX_APPROVALS:
                for index, old in enumerate(approvals):
                    if not isinstance(old, dict) or old.get("status") != "pending":
                        del approvals[index]
                        break
                else:
                    approvals.pop(0)
            approvals.append(record)
            session.meta["last_activity_at"] = utc_now()
            decision, scope, reason = self._approval_policy(session, record)
            if decision:
                record["status"] = "allowed" if decision == "allow" else "denied"
                record["decision"] = decision
                record["scope"] = scope
                record["auto"] = True
                record["message"] = reason
                record["decided_at"] = utc_now()
                record["decided_by"] = "policy"
        session.save()
        if decision:
            delivered = self._deliver_permission_response(
                session, record, decision, scope, reason)
            if not delivered:
                with session.lock:
                    record["status"] = "failed"
                    record["message"] = (
                        f"{reason}; the response could not be written to adapter stdin").strip("; ")
                session.save()
        return record

    def approvals(self, session_id: str) -> dict:
        session = self.get(session_id)
        state = self._approval_state(session.meta)
        return {"session": session.id, "status": self.public(session)["status"],
                **state, "protocol": APPROVAL_PROTOCOL}

    def decide_approval(self, session_id: str, request_id: str, decision: str,
                        *, message: str = "", actor: str = "user") -> dict:
        """Allow/deny one pending request once or for the rest of the session."""
        session = self.get(session_id)
        token = _mode_token(decision)
        if token in {"allow", "allow_once", "once", "approve"}:
            allow, scope = True, "once"
        elif token in {"always", "allow_always", "always_allow", "allow_for_session"}:
            allow, scope = True, "always"
        elif token in {"deny", "deny_once", "reject", "block"}:
            allow, scope = False, "once"
        else:
            raise SessionError("decision must be allow, always or deny")
        message = str(message or "").strip()[:MAX_APPROVAL_MESSAGE]
        expired = False
        with session.lock:
            record = self._find_approval(session, request_id)
            if record is None:
                raise SessionError("approval request not found", 404)
            status = str(record.get("status") or "pending")
            if status != "pending":
                raise SessionError(f"approval request was already {status}", 409)
            if not session.is_active:
                record["status"] = "expired"
                record["message"] = "the session is not running"
                record["decided_at"] = utc_now()
                record["decided_by"] = actor
                expired = True
            else:
                record["status"] = "deciding"
                record["decision"] = "allow" if allow else "deny"
                record["scope"] = scope
                record["auto"] = False
                record["message"] = message or ("approved by user" if allow else "denied by user")
                record["decided_at"] = utc_now()
                record["decided_by"] = actor
                if scope == "always" and allow:
                    rules = session.meta.setdefault("approval_rules", [])
                    kind = str(record.get("kind") or "other")
                    tool = str(record.get("tool") or "")
                    duplicate = any(
                        isinstance(rule, dict) and rule.get("kind") == kind
                        and str(rule.get("tool") or "").lower() == tool.lower()
                        for rule in rules)
                    if not duplicate:
                        rules.append({"kind": kind, "tool": tool,
                                      "created_at": utc_now(), "created_by": actor})
        if expired:
            session.save()
            raise SessionError("the session is not running; the approval request expired", 409)
        session.save()
        delivered = self._deliver_permission_response(
            session, record, "allow" if allow else "deny", scope, message)
        with session.lock:
            if delivered:
                record["status"] = "allowed" if allow else "denied"
            else:
                record["status"] = "failed"
                record["message"] = (
                    (message or "") +
                    " — the decision could not be written to the adapter stdin").strip()
        session.save()
        if not delivered:
            raise SessionError(
                "the decision was recorded but could not be written to the adapter stdin", 409)
        return {"session": self.public(session, detail=True),
                "approval": self.approval_public(record),
                "decision": "allow" if allow else "deny", "scope": scope}

    @staticmethod
    def _steer_public(item: dict) -> dict:
        return {
            "id": item.get("id"),
            "text": item.get("text") or "",
            "status": item.get("status") or "queued",
            "channel": item.get("channel") or "next_turn",
            "created_at": item.get("created_at"),
            "delivered_at": item.get("delivered_at"),
            "edited_at": item.get("edited_at"),
            "error": item.get("error") or "",
        }

    def steer(self, session_id: str, text: str, *, context: Any = None) -> dict:
        """Queue an intervention for a running session and report delivery.

        Interactive platforms receive the steer immediately on stdin (status
        ``delivered``).  One-shot platforms keep it ``queued`` with channel
        ``next_turn``; ``continue_session`` drains that queue, so the message
        never silently becomes a new turn while the process is running.
        """
        session = self.get(session_id)
        text = str(text or "").strip()
        entries = self._context_entries(session, context)
        if not text and not entries:
            raise SessionError("steer text or attached context is required")
        if len(text) > MAX_STEER_TEXT:
            raise SessionError("steer text is too long")
        if not session.is_active:
            raise SessionError("session is not running; send a continue message", 409)
        payload = self._compose_message(text, entries)
        if len(payload) > MAX_PROMPT:
            raise SessionError("steer text plus attached context is too long")
        followup = bool(session.meta.get("followup"))
        item = {
            "id": "st_" + secrets.token_hex(4),
            "text": text or "Use the attached context.",
            "status": "queued",
            "channel": "stdin" if followup else "next_turn",
            "created_at": utc_now(),
            "delivered_at": None,
            "error": "",
        }
        with session.lock:
            queue = session.meta.setdefault("steer_queue", [])
            if len(queue) >= MAX_STEER_QUEUE:
                # Drop the oldest already-delivered entries before refusing.
                queue[:] = [entry for entry in queue if entry.get("status") != "delivered"]
            if len(queue) >= MAX_STEER_QUEUE:
                raise SessionError("the steer queue is full", 409)
            queue.append(item)
            session.meta["last_activity_at"] = utc_now()
        session.save()
        if followup:
            try:
                session.write_input(payload, record=True,
                                    display=(text or "Use the attached context.")
                                    + self._context_marker(entries))
            except SessionError as exc:
                item["status"] = "failed"
                item["error"] = str(exc)
                session.save()
                raise SessionError(f"steer queued but delivery failed: {exc}", 409) from exc
            item["status"] = "delivered"
            item["delivered_at"] = utc_now()
            session.save()
        return {"session": self.public(session, detail=True),
                "steer": self._steer_public(item)}

    def remove_steer(self, session_id: str, steer_id: str) -> dict:
        session = self.get(session_id)
        with session.lock:
            queue = session.meta.get("steer_queue") or []
            kept = [entry for entry in queue if entry.get("id") != steer_id]
            if len(kept) == len(queue):
                raise SessionError("steer message not found", 404)
            session.meta["steer_queue"] = kept
        session.save()
        return self.public(session, detail=True)

    def update_steer(self, session_id: str, steer_id: str, text: str) -> dict:
        """Edit a still-queued steer. Delivered input is already on stdin."""
        session = self.get(session_id)
        text = str(text or "").strip()
        if not text:
            raise SessionError("steer text is required")
        if len(text) > MAX_STEER_TEXT:
            raise SessionError("steer text is too long")
        with session.lock:
            item = next((entry for entry in session.meta.get("steer_queue") or []
                         if entry.get("id") == steer_id), None)
            if item is None:
                raise SessionError("steer message not found", 404)
            if item.get("status") == "delivered":
                raise SessionError(
                    "this steer was already delivered to the process stdin; it cannot be edited",
                    409)
            item["text"] = text
            item["edited_at"] = utc_now()
            item["status"] = "queued"
            item["error"] = ""
        session.save()
        return self.public(session, detail=True)

    def reorder_steer(self, session_id: str, order: Any) -> dict:
        """Reorder the not-yet-delivered steer messages by their ids."""
        session = self.get(session_id)
        if isinstance(order, str):
            order = [part.strip() for part in order.split(",") if part.strip()]
        if not isinstance(order, (list, tuple)):
            raise SessionError("order must be a list of steer ids")
        order = [str(item) for item in order][:MAX_STEER_QUEUE]
        if len(set(order)) != len(order):
            raise SessionError("order contains duplicate steer ids")
        with session.lock:
            queue = session.meta.get("steer_queue") or []
            queued_ids = [str(entry.get("id")) for entry in queue
                          if entry.get("status") != "delivered"]
            if set(order) != set(queued_ids):
                raise SessionError(
                    "order must list every queued steer message exactly once", 400)
            by_id = {str(entry.get("id")): entry for entry in queue}
            iterator = iter(order)
            rebuilt = [entry if entry.get("status") == "delivered"
                       else by_id[next(iterator)] for entry in queue]
            session.meta["steer_queue"] = rebuilt
        session.save()
        return self.public(session, detail=True)

    def _drain_steer(self, session: ManagedSession) -> list[str]:
        """Mark queued steers delivered by the next turn and return their text."""
        delivered: list[str] = []
        with session.lock:
            now = utc_now()
            for entry in session.meta.get("steer_queue") or []:
                if entry.get("status") == "delivered":
                    continue
                delivered.append(str(entry.get("text") or ""))
                entry["status"] = "delivered"
                entry["delivered_at"] = now
                entry["channel"] = "continue"
                entry["error"] = ""
        return [text for text in delivered if text.strip()]

    def send_input(self, session_id: str, text: str, *, context: Any = None) -> dict:
        session = self.get(session_id)
        text = str(text or "").strip()
        entries = self._context_entries(session, context)
        if not text and not entries:
            raise SessionError("input is required")
        if len(text) > MAX_INPUT:
            raise SessionError("input is too long")
        if not session.is_active:
            raise SessionError("session is not running", 409)
        if not bool(session.meta.get("followup")):
            raise SessionError("this platform does not accept follow-up input", 409)
        payload = self._compose_message(text, entries)
        if len(payload) > MAX_PROMPT:
            raise SessionError("input plus attached context is too long")
        session.write_input(payload, record=True,
                            display=(text or "Use the attached context.") + self._context_marker(entries))
        return self.public(session, detail=True)

    # -------------------------------------------------------------------- logs
    def logs(self, session_id: str, stream: str = "stdout", offset: int = 0, limit: int = 200) -> dict:
        session = self.get(session_id)
        if stream not in {"stdout", "stderr"}:
            raise SessionError("stream must be stdout or stderr")
        path = session.folder / f"{stream}.jsonl"
        entries: list[dict] = []
        total = 0
        if path.is_file():
            with session._log_lock:
                try:
                    lines = path.read_text(encoding="utf-8", errors="replace").splitlines()
                except OSError:
                    lines = []
            total = len(lines)
            offset = max(0, min(int(offset), total))
            limit = max(1, min(int(limit), MAX_LOG_LIMIT))
            for raw in lines[offset:offset + limit]:
                try:
                    entry = json.loads(raw)
                except ValueError:
                    continue
                if isinstance(entry, dict):
                    entries.append(entry)
        return {
            "session": session_id,
            "stream": stream,
            "entries": entries,
            "offset": offset + len(entries),
            "total": total,
            "status": self.public(session)["status"],
        }

    def _all_log_entries(self, session: ManagedSession, stream: str,
                         limit: int = session_transcript.MAX_SOURCE_LINES) -> list[dict]:
        """Read complete log entries (oldest first) for the transcript view."""
        path = session.folder / f"{stream}.jsonl"
        if not path.is_file():
            return []
        with session._log_lock:
            try:
                lines = path.read_text(encoding="utf-8", errors="replace").splitlines()
            except OSError:
                return []
        entries: list[dict] = []
        for raw in lines[-limit:]:
            try:
                entry = json.loads(raw)
            except ValueError:
                continue
            if isinstance(entry, dict):
                entries.append(entry)
        return entries

    def transcript(self, session_id: str) -> dict:
        """Structured conversation for the session workspace.

        The parser never invents tokens: complete stdout/stderr lines are grouped
        into messages and tool cards, and the ``active``/``streaming`` flags are
        honest about the line-buffered transport.
        """
        session = self.get(session_id)
        payload = self.public(session, detail=True)
        data = session_transcript.build_transcript(
            session_id=session.id,
            status=payload["status"],
            prompt=payload.get("prompt") or "",
            prompt_at=payload.get("started_at") or payload.get("created_at"),
            stdout=self._all_log_entries(session, "stdout"),
            stderr=self._all_log_entries(session, "stderr"),
            error=payload.get("error") or "",
            platform=str(session.meta.get("platform") or ""),
            notice=payload.get("notice") or "",
        )
        approvals = payload.get("approvals") or {}
        recorded = {str(item.get("id")): item for item in approvals.get("items") or []}
        for item in data["items"]:
            if item.get("type") != "permission":
                continue
            record = recorded.get(str(item.get("request_id") or ""))
            if record is None:
                if not data.get("active"):
                    item["status"] = "expired"
                continue
            item["status"] = record.get("status") or item.get("status")
            item["decision"] = record.get("decision") or ""
            item["scope"] = record.get("scope") or ""
            item["auto"] = bool(record.get("auto"))
            item["message"] = record.get("message") or ""
            item["mode"] = record.get("mode") or ""
            item["decided_at"] = record.get("decided_at")
            item["decided_by"] = record.get("decided_by") or ""
            for key in ("kind", "tool", "command", "path", "description"):
                if record.get(key):
                    item[key] = record[key]
        data["stats"]["pending_permissions"] = sum(
            1 for item in data["items"]
            if item.get("type") == "permission" and item.get("status") == "pending")
        data["approvals"] = approvals
        data["permission_mode"] = payload.get("permission_mode") or payload.get("access_mode")
        data["permission_label"] = payload.get("permission_label") or payload.get("access_label")
        data["permission_history"] = payload.get("permission_history") or []
        data["permission_modes"] = payload.get("permission_modes") or []
        data["supports_approval"] = bool(approvals.get("supported"))
        data["turn"] = payload.get("turn") or 1
        data["signature"] = "|".join([
            data["signature"],
            str(data["permission_mode"]),
            str(data["stats"]["pending_permissions"]),
            ",".join(f"{item.get('request_id')}:{item.get('status')}"
                     for item in data["items"] if item.get("type") == "permission"),
        ])
        data["counts"] = payload.get("counts") or {}
        data["raw_status"] = payload.get("raw_status") or payload["status"]
        return data

    def workspace_index(self, session_id: str) -> dict:
        """Read-only media/document index of the session workspace."""
        session = self.get(session_id)
        index = session_transcript.index_workspace(session.workspace)
        return {"session": session.id, **index}

    # --------------------------------------------------------------- artifacts
    def _artifact_target(self, session: ManagedSession, rel: str) -> tuple[Path, str]:
        raw = str(rel or "").replace("\\", "/").strip()
        if raw.startswith("/") or re.match(r"^[A-Za-z]:", raw):
            raise SessionError("absolute paths are not allowed")
        parts = [part for part in raw.split("/") if part not in ("", ".")]
        if any(part == ".." for part in parts):
            raise SessionError("path traversal is not allowed")
        root = session.workspace.resolve()
        target = root.joinpath(*parts).resolve() if parts else root
        if target != root and root not in target.parents:
            raise SessionError("path is outside the session workspace", 403)
        if target.is_symlink():
            raise SessionError("symlinks are not served")
        rel_path = "/".join(parts)
        return target, rel_path

    def list_artifacts(self, session_id: str, rel: str = "") -> dict:
        session = self.get(session_id)
        target, rel_path = self._artifact_target(session, rel)
        if not target.exists():
            raise SessionError("folder not found", 404)
        if not target.is_dir():
            raise SessionError("not a folder")
        entries = []
        truncated = False
        try:
            children = sorted(target.iterdir(), key=lambda p: (not p.is_dir(), p.name.lower()))
        except OSError as exc:
            raise SessionError(f"cannot list folder: {exc}") from exc
        for child in children:
            if len(entries) >= MAX_ARTIFACT_ENTRIES:
                truncated = True
                break
            if child.is_symlink():
                continue
            try:
                stat = child.stat()
            except OSError:
                continue
            is_dir = child.is_dir()
            entries.append({
                "name": child.name,
                "path": f"{rel_path}/{child.name}" if rel_path else child.name,
                "type": "dir" if is_dir else "file",
                "size": 0 if is_dir else stat.st_size,
                "mtime": datetime.fromtimestamp(stat.st_mtime, timezone.utc).isoformat(timespec="seconds"),
                "text": (not is_dir) and child.suffix.lower() in TEXT_SUFFIXES,
                "language": "" if is_dir else session_transcript.language_for(child.name),
            })
        parent = "/".join(rel_path.split("/")[:-1]) if rel_path else None
        return {
            "session": session_id,
            "path": rel_path,
            "parent": parent,
            "entries": entries,
            "truncated": truncated,
            "count": len(entries),
        }

    def read_artifact(self, session_id: str, rel: str) -> dict:
        session = self.get(session_id)
        target, rel_path = self._artifact_target(session, rel)
        if not target.is_file():
            raise SessionError("file not found", 404)
        size = target.stat().st_size
        if size > MAX_TEXT_ARTIFACT:
            raise SessionError("file is too large to preview; download it instead", 413)
        try:
            raw = target.read_bytes()
        except OSError as exc:
            raise SessionError(f"cannot read file: {exc}") from exc
        if b"\x00" in raw:
            raise SessionError("binary file; download it instead", 415)
        text = raw.decode("utf-8", "replace").lstrip("\ufeff")
        return {
            "session": session_id,
            "path": rel_path,
            "name": target.name,
            "content": text,
            "size": size,
            "mime": mimetypes.guess_type(target.name)[0] or "text/plain",
            "language": session_transcript.language_for(target.name),
            "lines": len(text.splitlines()) if text else 0,
            "truncated": False,
        }

    def artifact_download(self, session_id: str, rel: str) -> tuple[Path, str, str]:
        session = self.get(session_id)
        target, rel_path = self._artifact_target(session, rel)
        if not target.is_file():
            raise SessionError("file not found", 404)
        size = target.stat().st_size
        if size > MAX_DOWNLOAD:
            raise SessionError("file is too large to download", 413)
        ctype = mimetypes.guess_type(target.name)[0] or "application/octet-stream"
        return target, ctype, target.name

    def artifact_file(self, session_id: str, rel: str) -> Path:
        """Resolve one workspace file for an OS-level open request.

        Reuses the same scoped path checks as the artifact browser: absolute
        paths, ``..`` and escaping symlinks never leave the workspace.
        """
        session = self.get(session_id)
        target, _ = self._artifact_target(session, rel)
        if not target.is_file():
            raise SessionError("file not found", 404)
        return target

    # ---------------------------------------------------------- archive/delete
    def _output_tail(self, session: ManagedSession, limit: int = 4000) -> str:
        path = session.folder / "stdout.jsonl"
        if not path.is_file():
            return ""
        chunks: list[str] = []
        size = 0
        try:
            lines = path.read_text(encoding="utf-8", errors="replace").splitlines()
        except OSError:
            return ""
        for raw in reversed(lines[-80:]):
            try:
                entry = json.loads(raw)
            except (TypeError, ValueError):
                continue
            if entry.get("kind") == "input":
                continue
            line = str(entry.get("line") or "").rstrip()
            if not line:
                continue
            chunks.append(line)
            size += len(line) + 1
            if size >= limit:
                break
        return "\n".join(reversed(chunks))[-limit:]

    def continue_session(self, session_id: str, text: str, *, platform: str | None = None,
                         model: str | None = None, effort: str | None = None,
                         access_mode: str | None = None, context: Any = None,
                         actor: str = "", resume: bool = False) -> dict:
        session = self.get(session_id)
        actor = actor or str(session.meta.get("actor") or "")
        if resume:
            if not self.resumable(session):
                raise SessionError("this session has no provider session id to resume", 409,
                                   "SESSION_NOT_RESUMABLE")
            if platform not in (None, "", session.meta.get("platform")):
                raise SessionError("a provider-native resume keeps the original platform", 400,
                                   "SESSION_RESUME_PLATFORM")
        text = str(text or "").strip()[:MAX_PROMPT]
        overrides = any(value not in (None, "") for value in (platform, model, effort, access_mode))
        if session.is_active:
            if overrides:
                raise SessionError("stop the running session before changing provider or access", 409)
            if bool(session.meta.get("followup")):
                return self.send_input(session_id, text, context=context)
            raise SessionError("stop the running one-shot session before correcting it", 409)
        entries = self._context_entries(session, context)
        if not text and not entries:
            raise SessionError("correction text or attached context is required", 400)
        new_platform = str(platform or session.meta.get("platform") or "").strip()
        preset = self.preset(new_platform, actor)
        if not _preset_available(preset):
            raise SessionError(f"{preset['label']} is not installed or configured", 400)
        new_access = self._validate_permission_mode(
            access_mode if access_mode not in (None, "") else session.meta.get("access_mode"),
            preset)
        new_effort = session.meta.get("effort") or ""
        if effort is not None:
            new_effort = str(effort or "").strip()[:40]
        new_model = str(model if model is not None else session.meta.get("model") or "").strip()[:80]
        self._check_effort(preset, new_model, new_effort)
        original = str(session.meta.get("prompt") or "").strip()
        tail = self._output_tail(session)
        queued = self._drain_steer(session)
        if resume:
            # The provider keeps the history; send only what is new.
            prompt = text
            if queued:
                prompt += "\n\n## Queued interventions\n" + "\n\n".join(queued)
            if entries:
                prompt += self._render_context(entries)
            prompt = prompt.strip()[:MAX_PROMPT]
        else:
            prompt = original
            if tail:
                prompt += "\n\n## Previous output\n" + tail
            if queued:
                prompt += "\n\n## Queued interventions\n" + "\n\n".join(queued)
            if entries:
                prompt += self._render_context(entries)
            if text:
                prompt += "\n\n## User correction\n" + text
            prompt = prompt[:MAX_PROMPT]
        if text:
            session.append_event("stdout", "› " + text, kind="input")
        session.expire_pending_approvals("a new turn started before a decision")
        worktree = session.meta.get("worktree")
        if isinstance(worktree, dict) and worktree.get("path"):
            worktree = self._resolve_worktree(
                str(worktree["path"]),
                allow_shared=bool(session.meta.get("allow_shared_worktree")),
                exclude_id=session.id,
            )
            cwd = "."
        else:
            worktree = None
            cwd = _safe_relative(session.meta.get("cwd") or "", "cwd")
        with session.lock:
            session.meta["platform"] = preset["id"]
            session.meta["platform_label"] = preset["label"]
            session.meta["actor"] = actor
            session.meta["model"] = new_model
            session.meta["effort"] = new_effort
            session.meta["access_mode"] = new_access
            session.meta["permission_mode"] = new_access
            session.meta["turn"] = int(session.meta.get("turn") or 1) + 1
            history = session.meta.setdefault("permission_history", [])
            if not isinstance(history, list):
                history = session.meta["permission_history"] = []
            history.append({"mode": new_access, "at": utc_now(),
                            "turn": session.meta["turn"], "source": "continue"})
            session.meta["followup"] = bool(preset["followup"])
            session.meta["worktree"] = (dict(worktree) if worktree is not None
                                        else session.meta.get("worktree"))
            session.meta["status"] = "starting"
            session.meta["error"] = ""
            session.meta["notice"] = ""
            session.meta["ended_at"] = None
            session.meta["exit_code"] = None
            session.meta["pid"] = None
            session.meta["started_at"] = None
            session.meta["last_activity_at"] = utc_now()
            if resume:
                session.meta["resume_native_id"] = str(session.meta.get("native_session_id") or "")
                session.meta["resumed_natively_at"] = utc_now()
        session.stop_requested = False
        session.proc = None
        session._readers = {}
        workspace = session.workspace
        artifacts = workspace / "artifacts"
        artifacts.mkdir(parents=True, exist_ok=True)
        if worktree is not None:
            cwd_dir = Path(str(worktree["path"]))
        else:
            cwd_dir = workspace / cwd if cwd else workspace
            cwd_dir.mkdir(parents=True, exist_ok=True)
        project_ref = dict(session.meta.get("project") or {})
        api_base = str((session.meta.get("api") or {}).get("base") or "")
        self._refresh_workspace_permission(
            session, preset, workspace, project_ref, api_base, cwd or ".",
            str(session.meta.get("read_token") or ""))
        self._spawn(session, preset, prompt, cwd_dir=cwd_dir, workspace=workspace,
                    artifacts=artifacts, project_ref=project_ref, api_base=api_base,
                    read_token=str(session.meta.get("read_token") or ""), actor=actor)
        session.meta["continued_at"] = utc_now()
        self.save(session)
        return self.public(session, detail=True)

    def _fork_context(self, parent: ManagedSession) -> str:
        lines = [
            "## Side chat context",
            f"You are a side chat branched from ARK session `{parent.id}` "
            f"({parent.meta.get('title') or 'untitled'}).",
            "- The parent process keeps running independently; this side chat is a new process "
            "with its own private workspace. Files are not shared with the parent.",
            f"- Parent status: {parent.meta.get('status') or 'unknown'}",
            f"- Parent permission mode: {PERMISSION_LABELS.get(str(parent.meta.get('access_mode') or 'read_only'), 'read-only')}",
            "- Use the parent context below to answer; do not assume you can see the parent's files.",
        ]
        try:
            data = self.transcript(parent.id)
            messages = [item for item in data.get("items") or []
                        if item.get("type") == "message" and str(item.get("text") or "").strip()]
            if messages:
                lines.append("### Recent parent conversation")
                for item in messages[-6:]:
                    text = str(item.get("text") or "").strip()
                    lines.append(f"**{item.get('role') or 'assistant'}:** {text[:900]}")
        except (SessionError, OSError, ValueError):
            pass
        tail = self._output_tail(parent, limit=1200)
        if tail:
            lines.append("### Latest parent output\n```\n" + tail + "\n```")
        return "\n\n" + "\n\n".join(lines)[:MAX_FORK_CONTEXT]

    def fork_session(self, session_id: str, body: dict, *, base_url: str = "",
                     actor: str = "") -> ManagedSession:
        """Start a linked side chat (a real child session, not a fake drawer)."""
        parent = self.get(session_id)
        actor = actor or str(parent.meta.get("actor") or "")
        if parent.meta.get("status") == "archived":
            raise SessionError("the parent session is archived", 400)
        prompt = str(body.get("prompt") or body.get("text") or "").strip()[:MAX_PROMPT]
        if not prompt:
            raise SessionError("a side-chat prompt is required", 400)
        relation = str(body.get("mode") or "side_chat").strip().lower().replace("-", "_")
        if relation not in {"side_chat", "fork"}:
            raise SessionError("mode must be side_chat or fork")
        platform = str(body.get("platform") or parent.meta.get("platform") or "")
        preset = self.preset(platform, actor)
        access_mode = self._validate_permission_mode(
            body.get("access_mode") if body.get("access_mode") not in (None, "")
            else parent.meta.get("access_mode"), preset)
        same_platform = platform == str(parent.meta.get("platform") or "")
        model = str(body.get("model") if body.get("model") is not None
                    else (parent.meta.get("model") if same_platform else "") or "").strip()[:80]
        effort = str(body.get("effort") if body.get("effort") is not None
                     else (parent.meta.get("effort") if same_platform else "") or "").strip()[:40]
        self._check_effort(preset, model, effort)
        title = str(body.get("title") or f"Side chat · {parent.meta.get('title') or parent.id}")[:MAX_TITLE]
        context = self._fork_context(parent)
        composed = (prompt + context)[:MAX_PROMPT]
        new = self.start({
            "platform": platform,
            "title": title,
            "prompt": composed,
            "model": model,
            "effort": effort,
            "access_mode": access_mode,
            "agent": str(body.get("agent") or parent.meta.get("agent") or ""),
            "task": str(body.get("task") or parent.meta.get("task_ref") or ""),
            "cwd": "" if parent.meta.get("cwd") in (None, "", ".") else parent.meta.get("cwd"),
            "parent_id": parent.id,
        }, base_url=base_url, actor=actor)
        with new.lock:
            new.meta["relation"] = relation
            new.meta["side_chat_of"] = parent.id
            new.meta["forked_from"] = parent.id
            new.meta["forked_at"] = utc_now()
            new.meta["fork_prompt"] = prompt
        self.save(new)
        return new

    def relaunch(self, session_id: str, *, base_url: str = "",
                 actor: str = "") -> ManagedSession:
        old = self.get(session_id)
        if old.is_active:
            raise SessionError("stop the session before relaunching it", 409)
        meta = dict(old.meta)
        actor = actor or str(meta.get("actor") or "")
        prompt = str(meta.get("prompt") or "").strip()
        if not prompt:
            raise SessionError("this session has no original prompt; launch a new session", 400)
        worktree = meta.get("worktree") if isinstance(meta.get("worktree"), dict) else {}
        body = {
            "platform": meta.get("platform"),
            "title": meta.get("title"),
            "model": meta.get("model"),
            "effort": meta.get("effort") or "",
            "access_mode": meta.get("access_mode") or "read_only",
            "agent": meta.get("agent"),
            "task": meta.get("task_ref"),
            "cwd": "" if (meta.get("cwd") in (None, "", ".")) else meta.get("cwd"),
            "worktree": str(worktree.get("path") or ""),
            "allow_shared_worktree": bool(meta.get("allow_shared_worktree")),
            "prompt": prompt,
            "parent_id": meta.get("parent_id") or "",
        }
        new = self.start(body, base_url=base_url, actor=actor)
        new.meta["relaunched_from"] = session_id
        self.save(new)
        try:
            self.archive(session_id)
        except SessionError:
            pass
        return new

    def archive(self, session_id: str) -> dict:
        session = self.get(session_id)
        if session.is_active:
            raise SessionError("stop the session before archiving it", 409)
        payload = self.public(session, detail=True)
        dest = self.archive_root / session.id
        if dest.exists():
            dest = self.archive_root / f"{session.id}-{int(time.time())}"
        with session.lock:
            session.meta["status"] = "archived"
            session.meta["archived_at"] = utc_now()
            session.meta["archived_path"] = str(dest)
        payload["status"] = "archived"
        payload["raw_status"] = "archived"
        try:
            self.save(session)
            session.folder.rename(dest)
        except OSError as exc:
            raise SessionError(f"could not archive session: {exc}") from exc
        with self.lock:
            self._sessions.pop(session.id, None)
        return payload

    def delete(self, session_id: str) -> dict:
        session = self.get(session_id)
        if session.is_active:
            raise SessionError("stop the session before deleting it", 409)
        folder = session.folder.resolve()
        if folder.parent != self.root or not SESSION_ID_RE.fullmatch(folder.name):
            raise SessionError("refusing to delete outside the session root")
        try:
            shutil.rmtree(folder)
        except OSError as exc:
            raise SessionError(f"could not delete session: {exc}") from exc
        with self.lock:
            self._sessions.pop(session.id, None)
        self.notify_change(session, "delete")
        return {"deleted": session.id}

    # ------------------------------------------------------------------ helper
    def context_snapshot(self, session: ManagedSession) -> dict:
        data = _read_json(session.workspace / "ark-context.json")
        if data is None:
            raise SessionError("context snapshot is missing", 404)
        return data


NATIVE_RESUME_PLATFORMS = ("claude", "codex")
_NATIVE_ID_RE = re.compile(r"^[A-Za-z0-9._:-]{8,120}$")
_CODEX_SESSION_HEADER_RE = re.compile(r"^\s*session id:\s*([A-Za-z0-9._:-]{8,120})\s*$", re.IGNORECASE)


def native_session_id_from_line(line: str) -> str:
    """Provider session id announced on one output line, else ''.

    Claude Code ``stream-json`` starts with ``{"type": "system", "session_id": ...}``;
    ``codex exec --json`` emits ``{"type": "thread.started", "thread_id": ...}``
    and its text mode prints a ``session id: <uuid>`` header line.
    """
    text = str(line or "").strip()
    header = _CODEX_SESSION_HEADER_RE.match(text)
    if header:
        return header.group(1)
    if not (text.startswith("{") and text.endswith("}")):
        return ""
    try:
        obj = json.loads(text)
    except ValueError:
        return ""
    if not isinstance(obj, dict):
        return ""
    etype = str(obj.get("type") or "")
    candidate = ""
    if etype == "thread.started":
        candidate = str(obj.get("thread_id") or "")
    elif etype in {"system", "result"} or "session_id" in obj:
        candidate = str(obj.get("session_id") or "")
    return candidate if _NATIVE_ID_RE.fullmatch(candidate) else ""


def _resume_argv(argv: list[str], platform: str, native_id: str, prompt: str) -> list[str]:
    """Insert the provider's native resume arguments into a launch command.

    Claude Code: ``claude --resume <id> -p "<prompt>"``.  Codex:
    ``codex exec [options] resume <id> "<prompt>"`` (``codex exec resume``).
    """
    argv = list(argv)
    if platform == "claude":
        if "--resume" not in argv and "-r" not in argv and len(argv) >= 1:
            argv[1:1] = ["--resume", native_id]
        return argv
    if platform == "codex" and "exec" in argv and "resume" not in argv:
        at = argv.index(prompt) if prompt and prompt in argv else len(argv)
        argv[at:at] = ["resume", native_id]
    return argv


def _invert_time(value: Any) -> float:
    parsed = _parse_time(value)
    return -parsed.timestamp() if parsed is not None else float("inf")


def _safe_relative(value: Any, field: str) -> str:
    raw = str(value or "").replace("\\", "/").strip().strip("/")
    if not raw:
        return ""
    if re.match(r"^[A-Za-z]:", raw):
        raise SessionError(f"{field} must be relative to the session workspace")
    parts = [part for part in raw.split("/") if part not in ("", ".")]
    if any(part == ".." for part in parts):
        raise SessionError(f"{field} may not contain '..'")
    return "/".join(parts)


def _expand(token: str, values: dict) -> str:
    for key, value in values.items():
        token = token.replace("{" + key + "}", str(value))
    return token


def _preview(argv: list[str], prompt: str) -> str:
    parts = list(argv)
    if prompt:
        parts = [part.replace(prompt, "…") for part in parts]
    preview = " ".join(parts[:4])
    if len(parts) > 4:
        preview += " …"
    return preview[:300]


def _is_write_credential(name: str) -> bool:
    upper = str(name).upper()
    if upper in {"ARK_AGENT_TOKEN", "ARK_TOKEN", "ARK_WRITE_TOKEN", "ARK_API_TOKEN",
                 "ARK_TASK_WRITE_TOKEN"}:
        return True
    return upper.startswith("ARK_") and any(
        marker in upper for marker in ("TOKEN", "SECRET", "PASSWORD", "CREDENTIAL")
    )


def _is_credential_env(name: str) -> bool:
    """Provider/account credential values that must not be inherited globally."""
    upper = str(name).upper()
    if _is_write_credential(upper):
        return True
    if "BASE_URL" in upper or "ENDPOINT" in upper:
        return False
    return any(marker in upper for marker in
               ("API_KEY", "_TOKEN", "TOKEN_", "SECRET", "PASSWORD",
                "CREDENTIAL", "OAUTH"))
