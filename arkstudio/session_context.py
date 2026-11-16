"""Stored session context: external agent transcripts uploaded into a project.

See ``docs/agents/session-context.md``.  External sessions live in the project
folder::

    <project>.ark/sessions/
      ark-ses-<id>.jsonl   redacted JSONL transcript
      ark-ses-<id>.json    metadata (received byte offset, chunk hashes, links)

Uploads are idempotent by source byte offset: the uploader says where in its
own file a chunk starts; ARK appends only at the expected offset and treats a
re-sent chunk (same offset and SHA-256) as a no-op.  Every line is redacted by
:mod:`arkstudio.secret_redact` before it is written.

The module also holds the pure helpers shared with internal sessions: Codex
rollout normalisation, metadata sniffing, compact context summaries and the
provider resume commands.  Standard library only.
"""
from __future__ import annotations

import hashlib
import json
import os
import re
import secrets
import shlex
import shutil
import threading
from datetime import datetime, timezone
from pathlib import Path
from typing import Any, Iterable

from . import secret_redact, session_transcript
from .sessions import NATIVE_RESUME_PLATFORMS, SessionError

EXTERNAL_ID_RE = re.compile(r"^ark-ses-[a-z0-9]{6,32}$")
AGENTS = ("claude", "codex", "dsh", "other")
MAX_TITLE = 160
MAX_FIELD = 400
MAX_CHUNKS = 64
MAX_LINKS = 20
SUMMARY_LIMIT = 6000
SESSIONS_DIR = "sessions"


def _env_int(name: str, default: int) -> int:
    try:
        value = int(str(os.environ.get(name) or "").strip() or default)
    except ValueError:
        return default
    return value if value > 0 else default


def limits() -> dict:
    """Effective upload limits (bytes).  Read per call so tests/operators can tune them."""
    return {
        "chunk_bytes": 4 * 1024 * 1024,
        "line_bytes": _env_int("ARK_SESSION_LINE_KB", 256) * 1024,
        "session_bytes": _env_int("ARK_SESSION_MAX_MB", 50) * 1024 * 1024,
        "project_bytes": _env_int("ARK_SESSION_PROJECT_MAX_MB", 500) * 1024 * 1024,
    }


class ContextError(SessionError):
    """A session-context request that the API reports as a clean 4xx."""


def utc_now() -> str:
    return datetime.now(timezone.utc).isoformat(timespec="seconds")


def new_external_id() -> str:
    return "ark-ses-" + secrets.token_hex(5)


def _clean(value: Any, limit: int = MAX_FIELD) -> str:
    return str(value or "").replace("\x00", "").strip()[:limit]


def _clean_agent(value: Any) -> str:
    agent = _clean(value, 40).lower()
    if agent in {"claude-code", "claude_code", "anthropic"}:
        agent = "claude"
    if agent in {"codex-cli", "openai"}:
        agent = "codex"
    if agent in {"deepseek", "deepseek-harness"}:
        agent = "dsh"
    return agent if agent in AGENTS else ("other" if agent else "")


def clean_links(value: Any) -> list[str]:
    if isinstance(value, str):
        value = [part.strip() for part in value.split(",")]
    if not isinstance(value, (list, tuple)):
        raise ContextError("linked_units must be a list of node ids", 400, "SESSION_LINKS_INVALID")
    out: list[str] = []
    for item in value:
        uid = _clean(item, 40)
        if uid and re.fullmatch(r"[A-Za-z0-9_.:-]+", uid) and uid not in out:
            out.append(uid)
    return out[:MAX_LINKS]


# --------------------------------------------------------------- JSONL helpers
def normalize_line(obj: dict) -> dict | None:
    """Map one provider session-file object to a shape ``session_transcript`` knows.

    Claude Code session files already use ``{"type": "user"|"assistant",
    "message": {...}}``.  Codex rollout files wrap items in
    ``{"type": "response_item", "payload": {...}}``.  Bookkeeping records
    (request blobs, snapshots, token counters...) return ``None``.
    """
    etype = str(obj.get("type") or "")
    if etype in {"user", "assistant"} and isinstance(obj.get("message"), dict):
        if obj.get("isMeta") or obj.get("isSidechain"):
            return None
        return obj
    if etype == "system" and str(obj.get("level") or "") == "error":
        err = obj.get("error")
        text = err.get("message") if isinstance(err, dict) else (err or obj.get("content"))
        return {"type": "error", "message": str(text or "error")}
    if etype == "ark_truncated":
        return {"type": "notice", "message": f"[line of {obj.get('bytes')} bytes truncated by ARK]"}
    if etype == "response_item" and isinstance(obj.get("payload"), dict):
        item = obj["payload"]
        itype = str(item.get("type") or "")
        if itype == "message":
            role = str(item.get("role") or "")
            if role not in {"user", "assistant"}:
                return None
            texts = [str(part.get("text") or "") for part in item.get("content") or []
                     if isinstance(part, dict) and part.get("text")]
            text = "\n".join(t for t in texts if t.strip())
            if not text or text.lstrip().startswith(("<environment_context>", "<user_instructions>",
                                                       "<permissions instructions>")):
                return None
            return {"role": role, "content": [{"type": "text", "text": text}]}
        if itype in {"function_call", "custom_tool_call", "local_shell_call"}:
            return {"type": "tool_call", "id": item.get("call_id") or item.get("id"),
                    "name": item.get("name") or itype,
                    "arguments": item.get("arguments", item.get("input", item.get("action")))}
        if itype in {"function_call_output", "custom_tool_call_output"}:
            output = item.get("output")
            if isinstance(output, list):
                output = "\n".join(str(part.get("text") or "") for part in output if isinstance(part, dict))
            return {"type": "tool_result", "tool_call_id": item.get("call_id"), "output": output}
        if itype == "reasoning":
            summary = item.get("summary") or []
            text = "\n".join(str(part.get("text") or "") for part in summary if isinstance(part, dict))
            return {"type": "reasoning", "text": text} if text.strip() else None
        return None
    if etype in {"tool_call", "tool_result", "tool_use", "message", "error", "notice",
                 "reasoning", "result"}:
        return obj
    if obj.get("role") in {"user", "assistant"}:
        return obj
    return None


def transcript_entries(lines: Iterable[str], fallback_at: str | None = None) -> list[dict]:
    """Stored JSONL lines -> ``build_transcript`` stdout entries.

    Lines without a timestamp inherit the previous line's time so that the
    transcript keeps file order (tool results in Claude files carry none).
    """
    out = []
    last_at: str | None = None
    for raw in lines:
        raw = raw.strip()
        if not raw:
            continue
        try:
            obj = json.loads(raw)
        except ValueError:
            continue
        if not isinstance(obj, dict):
            continue
        mapped = normalize_line(obj)
        if mapped is None:
            continue
        stamp = obj.get("timestamp") or obj.get("t")
        if stamp:
            last_at = str(stamp)
        out.append({"t": last_at or fallback_at, "line": json.dumps(mapped, ensure_ascii=False)})
    return out


def sniff_metadata(objects: Iterable[dict]) -> dict:
    """Best-effort agent / native id / cwd / model from provider JSONL objects."""
    found: dict[str, str] = {}
    for obj in objects:
        etype = str(obj.get("type") or "")
        payload = obj.get("payload") if isinstance(obj.get("payload"), dict) else {}
        if etype == "session_meta" and payload:
            found.setdefault("agent", "codex")
            if payload.get("id") or payload.get("session_id"):
                found.setdefault("native_session_id", str(payload.get("id") or payload.get("session_id")))
            if payload.get("cwd"):
                found.setdefault("cwd", str(payload["cwd"]))
        elif etype == "turn_context" and payload.get("model"):
            found.setdefault("model", str(payload["model"]))
        elif obj.get("sessionId") and etype in {"user", "assistant", "summary", "system", "attachment"}:
            found.setdefault("agent", "claude")
            found.setdefault("native_session_id", str(obj["sessionId"]))
            if obj.get("cwd"):
                found.setdefault("cwd", str(obj["cwd"]))
            message = obj.get("message") if isinstance(obj.get("message"), dict) else {}
            if etype == "assistant" and message.get("model") and not str(message["model"]).startswith("<"):
                found.setdefault("model", str(message["model"]))
    return found


def resume_command(agent: str, native_id: str, cwd: str = "") -> str:
    """The exact CLI command that resumes a provider-native session, or ''."""
    native_id = str(native_id or "").strip()
    if not native_id or not re.fullmatch(r"[A-Za-z0-9._:-]{4,120}", native_id):
        return ""
    if agent == "claude":
        command = f"claude --resume {native_id}"
    elif agent == "codex":
        command = f"codex resume {native_id}"
    else:
        return ""
    if cwd:
        quoted = f'"{cwd}"' if os.name == "nt" or " " in cwd else shlex.quote(cwd)
        return f"cd {quoted} && {command}"
    return command


def context_summary(record: dict, items: list[dict], *, limit: int = SUMMARY_LIMIT) -> str:
    """Compact Markdown context of a stored session for seeding a new one."""
    head = [
        f"## Context from ARK session `{record.get('id')}`",
        f"- Title: {record.get('title') or 'untitled'}",
        f"- Agent: {record.get('agent') or 'unknown'}"
        + (f" · model {record['model']}" if record.get("model") else ""),
    ]
    if record.get("cwd"):
        head.append(f"- Working directory: {record['cwd']}")
    links = list(record.get("linked_units") or [])
    if record.get("task_ref") and record["task_ref"] not in links:
        links.insert(0, record["task_ref"])
    if links:
        head.append("- Linked ARK nodes: " + ", ".join(links))
    head.append("- This is a summary of the previous conversation; verify state before acting on it.")
    messages = [item for item in items
                if item.get("type") == "message" and str(item.get("text") or "").strip()]
    tools = [item for item in items if item.get("type") == "tool"]
    if tools:
        names: dict[str, int] = {}
        for tool in tools:
            names[str(tool.get("name") or "tool")] = names.get(str(tool.get("name") or "tool"), 0) + 1
        head.append("- Tools used: " + ", ".join(f"{k}×{v}" for k, v in sorted(names.items())[:8]))
    budget = limit - len("\n".join(head)) - 40
    body: list[str] = []
    first_user = next((m for m in messages if m.get("role") == "user"), None)
    if first_user is not None:
        text = str(first_user["text"]).strip()
        body.append("### Original request\n" + text[:1200] + (" …" if len(text) > 1200 else ""))
        budget -= len(body[-1])
    recent: list[str] = []
    for item in reversed(messages):
        if item is first_user:
            break
        text = str(item.get("text") or "").strip()
        snippet = text[:900] + (" …" if len(text) > 900 else "")
        entry = f"**{item.get('role') or 'assistant'}:** {snippet}"
        if budget - len(entry) < 0:
            break
        recent.append(entry)
        budget -= len(entry) + 2
    if recent:
        body.append("### Latest turns\n" + "\n\n".join(reversed(recent)))
    return ("\n".join(head) + ("\n\n" + "\n\n".join(body) if body else ""))[:limit]


# ---------------------------------------------------------------- visibility
# Sessions are private to their creator; ``shared`` opens one to every project
# member with read access.  Project admins see and manage all of them.
def can_view(record: dict, user: str, admin: bool) -> bool:
    if admin or record.get("shared"):
        return True
    owner = str(record.get("owner") or "")
    return bool(owner) and owner.lower() == str(user or "").lower()


def can_manage(record: dict, user: str, admin: bool) -> bool:
    if admin:
        return True
    owner = str(record.get("owner") or "")
    return bool(owner) and owner.lower() == str(user or "").lower()


def with_access(record: dict, user: str, admin: bool) -> dict:
    out = dict(record)
    out["can_manage"] = can_manage(record, user, admin)
    return out


def clean_shared(value: Any) -> bool:
    if not isinstance(value, bool):
        raise ContextError("shared must be true or false", 400, "SESSION_SHARED_INVALID")
    return value


# ------------------------------------------------- internal transcript mirrors
# <project>.ark/sessions/internal/<s_id>/{meta.json,stdout.jsonl,stderr.jsonl}:
# a redacted copy of an ARK task session so a copied project carries it.  The
# runtime folder stays the live source; the mirror is rewritten from it.
INTERNAL_DIR = "internal"
INTERNAL_ID_RE = re.compile(r"^s_[a-z0-9]{8,32}$")
_MIRROR_META_DROP = {"read_token", "read_env", "api", "command_preview", "linux_account_home",
                     "approval_rules", "steer_queue", "pid"}


def _redacted_log(path: Path, extra_secrets: tuple) -> str:
    out = []
    try:
        lines = path.read_text(encoding="utf-8", errors="replace").splitlines()
    except OSError:
        return ""
    for raw in lines:
        try:
            entry = json.loads(raw)
        except ValueError:
            entry = raw
        entry, _ = secret_redact.redact_value(entry, extra_secrets)
        out.append(entry if isinstance(entry, str) else json.dumps(entry, ensure_ascii=False))
    return "\n".join(out) + ("\n" if out else "")


def mirror_folder(project_path: str | Path, session_id: str) -> Path:
    if not INTERNAL_ID_RE.fullmatch(str(session_id or "")):
        raise ContextError("invalid session id", 404, "SESSION_NOT_FOUND")
    return Path(project_path).resolve() / SESSIONS_DIR / INTERNAL_DIR / session_id


def mirror_internal(project_path: str | Path, runtime_folder: str | Path,
                    extra_secrets: Iterable[str] = ()) -> Path | None:
    """Write the redacted mirror of one runtime session folder."""
    runtime_folder = Path(runtime_folder)
    try:
        meta = json.loads((runtime_folder / "meta.json").read_text(encoding="utf-8"))
    except (OSError, ValueError):
        return None
    if not isinstance(meta, dict) or not INTERNAL_ID_RE.fullmatch(str(meta.get("id") or "")):
        return None
    secrets_ = tuple(s for s in extra_secrets if s)
    target = mirror_folder(project_path, str(meta["id"]))
    target.mkdir(parents=True, exist_ok=True)
    clean = {key: value for key, value in meta.items() if key not in _MIRROR_META_DROP}
    clean, _ = secret_redact.redact_value(clean, secrets_)
    clean["mirrored_at"] = utc_now()
    for name in ("stdout.jsonl", "stderr.jsonl"):
        source = runtime_folder / name
        if source.is_file():
            tmp = target / (name + ".tmp")
            tmp.write_text(_redacted_log(source, secrets_), encoding="utf-8")
            tmp.replace(target / name)
    tmp = target / "meta.json.tmp"
    tmp.write_text(json.dumps(clean, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    tmp.replace(target / "meta.json")
    return target


def mirror_is_stale(project_path: str | Path, runtime_folder: str | Path) -> bool:
    runtime_folder = Path(runtime_folder)
    target = mirror_folder(project_path, runtime_folder.name) / "meta.json"
    try:
        mirrored = target.stat().st_mtime
    except OSError:
        return True
    for name in ("meta.json", "stdout.jsonl", "stderr.jsonl"):
        try:
            if (runtime_folder / name).stat().st_mtime > mirrored:
                return True
        except OSError:
            continue
    return False


def remove_mirror(project_path: str | Path, session_id: str) -> bool:
    target = mirror_folder(project_path, session_id)
    if target.is_dir():
        shutil.rmtree(target, ignore_errors=True)
        return True
    return False


def mirror_ids(project_path: str | Path) -> list[str]:
    root = Path(project_path).resolve() / SESSIONS_DIR / INTERNAL_DIR
    if not root.is_dir():
        return []
    return sorted(p.name for p in root.iterdir()
                  if p.is_dir() and INTERNAL_ID_RE.fullmatch(p.name) and (p / "meta.json").is_file())


def read_mirror_meta(project_path: str | Path, session_id: str) -> dict:
    try:
        meta = json.loads((mirror_folder(project_path, session_id) / "meta.json").read_text(encoding="utf-8"))
    except (OSError, ValueError) as exc:
        raise ContextError("session not found", 404, "SESSION_NOT_FOUND") from exc
    if not isinstance(meta, dict):
        raise ContextError("session not found", 404, "SESSION_NOT_FOUND")
    return meta


def _log_entries(path: Path) -> list[dict]:
    out = []
    try:
        lines = path.read_text(encoding="utf-8", errors="replace").splitlines()
    except OSError:
        return out
    for raw in lines[-session_transcript.MAX_SOURCE_LINES:]:
        try:
            entry = json.loads(raw)
        except ValueError:
            continue
        if isinstance(entry, dict):
            out.append(entry)
    return out


def mirror_public(project_path: str | Path, session_id: str) -> dict:
    """Session-context record of a mirrored internal session with no live runtime copy."""
    meta = read_mirror_meta(project_path, session_id)
    folder = mirror_folder(project_path, session_id)
    payload = dict(meta, status="stored", platform=meta.get("platform"))
    record = internal_public(payload, folder)
    record["owner"] = meta.get("actor") or ""
    record["shared"] = bool(meta.get("shared"))
    record["copy"] = True
    record["active"] = False
    record["resume"]["native"] = False
    return record


def mirror_transcript(project_path: str | Path, session_id: str) -> dict:
    meta = read_mirror_meta(project_path, session_id)
    folder = mirror_folder(project_path, session_id)
    data = session_transcript.build_transcript(
        session_id=session_id, status="stored", prompt=str(meta.get("prompt") or ""),
        prompt_at=meta.get("started_at") or meta.get("created_at"),
        stdout=_log_entries(folder / "stdout.jsonl"), stderr=_log_entries(folder / "stderr.jsonl"),
        platform=str(meta.get("platform") or ""))
    data["source"] = "internal"
    data["copy"] = True
    return data


def update_mirror(project_path: str | Path, session_id: str, body: dict) -> dict:
    meta = read_mirror_meta(project_path, session_id)
    if "title" in body and _clean(body.get("title"), MAX_TITLE):
        meta["title"] = _clean(body.get("title"), MAX_TITLE)
    if "linked_units" in body:
        meta["linked_units"] = clean_links(body.get("linked_units"))
    if "shared" in body:
        meta["shared"] = clean_shared(body.get("shared"))
    target = mirror_folder(project_path, session_id) / "meta.json"
    target.write_text(json.dumps(meta, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    return mirror_public(project_path, session_id)


def internal_public(payload: dict, folder: Path | None = None) -> dict:
    """Session-context view of an internal task session payload."""
    platform = str(payload.get("platform") or "")
    native = str(payload.get("native_session_id") or "")
    worktree = payload.get("worktree") if isinstance(payload.get("worktree"), dict) else {}
    cwd = str(worktree.get("path") or "") or (str(payload.get("cwd") or "") if payload.get("cwd") not in (None, ".") else "")
    size = 0
    if folder is not None:
        for name in ("stdout.jsonl", "stderr.jsonl"):
            try:
                size += (Path(folder) / name).stat().st_size
            except OSError:
                pass
    counts = payload.get("counts") or {}
    resumable = bool(native) and platform in NATIVE_RESUME_PLATFORMS
    return {
        "id": payload.get("id"),
        "source": "internal",
        "agent": platform,
        "agent_label": payload.get("platform_label") or platform,
        "title": payload.get("title") or "",
        "model": payload.get("model") or "",
        "cwd": cwd,
        "native_session_id": native,
        "created_at": payload.get("created_at"),
        "updated_at": payload.get("last_activity_at") or payload.get("ended_at") or payload.get("created_at"),
        "uploaded_by": "",
        "bytes": size,
        "lines": int(counts.get("stdout") or 0) + int(counts.get("stderr") or 0),
        "redactions": 0,
        "linked_units": list(payload.get("linked_units") or []),
        "task_ref": payload.get("task_ref") or "",
        "status": payload.get("status") or "",
        "owner": payload.get("owner") or payload.get("actor") or "",
        "shared": bool(payload.get("shared")),
        "copy": False,
        "active": str(payload.get("status") or "") in {"starting", "running", "idle", "stopping"},
        "resume": {"native": resumable,
                   "command": resume_command(platform, native, cwd) if native else "",
                   "cwd": cwd},
    }


# ------------------------------------------------------------------- the store
class ContextStore:
    """External session transcripts of one project folder."""

    _locks: dict[str, threading.Lock] = {}
    _locks_guard = threading.Lock()

    def __init__(self, project_path: str | Path, *, extra_secrets: Iterable[str] = ()):
        self.project_path = Path(project_path).resolve()
        self.root = self.project_path / SESSIONS_DIR
        self.extra_secrets = tuple(s for s in extra_secrets if s)

    # ---------------------------------------------------------------- helpers
    def _lock(self, session_id: str) -> threading.Lock:
        key = f"{self.root}|{session_id}"
        with self._locks_guard:
            return self._locks.setdefault(key, threading.Lock())

    @staticmethod
    def check_id(session_id: str) -> str:
        session_id = str(session_id or "").strip().lower()
        if not EXTERNAL_ID_RE.fullmatch(session_id):
            raise ContextError("invalid ARK session id (expected ark-ses-…)", 400, "SESSION_ID_INVALID")
        return session_id

    def _paths(self, session_id: str) -> tuple[Path, Path]:
        return self.root / f"{session_id}.jsonl", self.root / f"{session_id}.json"

    def _read_meta(self, session_id: str) -> dict | None:
        _, meta_path = self._paths(session_id)
        try:
            data = json.loads(meta_path.read_text(encoding="utf-8"))
        except (OSError, ValueError):
            return None
        return data if isinstance(data, dict) else None

    def _write_meta(self, meta: dict) -> None:
        _, meta_path = self._paths(meta["id"])
        self.root.mkdir(parents=True, exist_ok=True)
        tmp = meta_path.with_suffix(".json.tmp")
        tmp.write_text(json.dumps(meta, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
        tmp.replace(meta_path)

    def usage_bytes(self) -> int:
        total = 0
        if self.root.is_dir():
            for path in self.root.glob("ark-ses-*.jsonl"):
                try:
                    total += path.stat().st_size
                except OSError:
                    pass
        return total

    # ----------------------------------------------------------------- public
    def public(self, meta: dict) -> dict:
        agent = meta.get("agent") or "other"
        native = str(meta.get("native_session_id") or "")
        command = resume_command(agent, native, str(meta.get("cwd") or ""))
        return {
            "id": meta.get("id"),
            "source": "external",
            "agent": agent,
            "title": meta.get("title") or "",
            "model": meta.get("model") or "",
            "cwd": meta.get("cwd") or "",
            "native_session_id": native,
            "created_at": meta.get("created_at"),
            "updated_at": meta.get("updated_at"),
            "uploaded_by": meta.get("uploaded_by") or "",
            "owner": meta.get("owner") or "",
            "shared": bool(meta.get("shared")),
            "received_bytes": int(meta.get("received_bytes") or 0),
            "bytes": int(meta.get("bytes") or 0),
            "lines": int(meta.get("lines") or 0),
            "redactions": int(meta.get("redactions") or 0),
            "truncated_lines": int(meta.get("truncated_lines") or 0),
            "linked_units": list(meta.get("linked_units") or []),
            "task_ref": meta.get("task_ref") or "",
            "prompt": meta.get("prompt") or "",
            "status": "stored",
            "resume": {"native": False, "command": command, "cwd": meta.get("cwd") or ""},
        }

    def list(self) -> list[dict]:
        out = []
        if not self.root.is_dir():
            return out
        for path in sorted(self.root.glob("ark-ses-*.json")):
            meta = self._read_meta(path.stem)
            if meta and EXTERNAL_ID_RE.fullmatch(str(meta.get("id") or "")):
                out.append(self.public(meta))
        return out

    def get(self, session_id: str) -> dict:
        session_id = self.check_id(session_id)
        meta = self._read_meta(session_id)
        if meta is None:
            raise ContextError("session not found", 404, "SESSION_NOT_FOUND")
        return meta

    def status(self, session_id: str, *, uploads_enabled: bool) -> dict:
        session_id = self.check_id(session_id)
        meta = self._read_meta(session_id)
        return {
            "id": session_id,
            "uploads_enabled": bool(uploads_enabled),
            "exists": meta is not None,
            "received_bytes": int((meta or {}).get("received_bytes") or 0),
            "limits": limits(),
        }

    def lines(self, session_id: str) -> list[str]:
        session_id = self.check_id(session_id)
        path, _ = self._paths(session_id)
        try:
            return path.read_text(encoding="utf-8", errors="replace").splitlines()
        except OSError:
            return []

    def raw_path(self, session_id: str) -> Path:
        self.get(session_id)
        path, _ = self._paths(self.check_id(session_id))
        if not path.is_file():
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text("", encoding="utf-8")
        return path

    def transcript(self, session_id: str) -> dict:
        meta = self.get(session_id)
        entries = transcript_entries(self.lines(session_id), meta.get("updated_at"))
        if len(entries) > session_transcript.MAX_SOURCE_LINES:
            entries = entries[-session_transcript.MAX_SOURCE_LINES:]
        data = session_transcript.build_transcript(
            session_id=meta["id"], status="stored", stdout=entries, stderr=[],
            platform=str(meta.get("agent") or ""))
        data["source"] = "external"
        return data

    def update(self, session_id: str, body: dict) -> dict:
        session_id = self.check_id(session_id)
        with self._lock(session_id):
            meta = self.get(session_id)
            if "title" in body:
                meta["title"] = _clean(body.get("title"), MAX_TITLE)
            if "linked_units" in body:
                meta["linked_units"] = clean_links(body.get("linked_units"))
            if "shared" in body:
                meta["shared"] = clean_shared(body.get("shared"))
            meta["updated_at"] = utc_now()
            self._write_meta(meta)
        return self.public(meta)

    def delete(self, session_id: str) -> dict:
        session_id = self.check_id(session_id)
        with self._lock(session_id):
            self.get(session_id)
            for path in self._paths(session_id):
                try:
                    path.unlink()
                except FileNotFoundError:
                    pass
                except OSError as exc:
                    raise ContextError(f"could not delete session: {exc}") from exc
        return {"deleted": session_id}

    # ----------------------------------------------------------------- upload
    def _prepare(self, content: str, line_limit: int) -> tuple[list[str], list[dict], int, int]:
        if content and not content.endswith("\n"):
            raise ContextError("upload complete JSONL lines only (content must end with a newline)",
                               400, "SESSION_NOT_JSONL")
        out: list[str] = []
        objects: list[dict] = []
        redactions = 0
        truncated = 0
        for number, raw in enumerate(content.split("\n")[:-1] if content else [], start=1):
            raw = raw.rstrip("\r")
            if not raw.strip():
                continue
            try:
                obj = json.loads(raw)
            except ValueError as exc:
                raise ContextError(f"line {number} is not valid JSON", 400, "SESSION_LINE_NOT_JSON") from exc
            if not isinstance(obj, dict):
                raise ContextError(f"line {number} is not a JSON object", 400, "SESSION_LINE_NOT_OBJECT")
            objects.append(obj)
            obj, n = secret_redact.redact_value(obj, self.extra_secrets)
            redactions += n
            line = json.dumps(obj, ensure_ascii=False, separators=(",", ":"))
            if len(line.encode("utf-8")) > line_limit:
                truncated += 1
                preview, _ = secret_redact.redact_text(raw[:300], self.extra_secrets)
                line = json.dumps({"type": "ark_truncated", "bytes": len(raw.encode("utf-8")),
                                   "source_type": str(obj.get("type") or "")[:60],
                                   "preview": preview}, ensure_ascii=False)
            out.append(line)
        return out, objects, redactions, truncated

    def upload(self, session_id: str, body: dict, *, actor: str = "", owner: str = "",
               admin: bool = True) -> dict:
        session_id = self.check_id(session_id)
        lim = limits()
        content = body.get("content")
        if not isinstance(content, str):
            raise ContextError("content must be a JSONL string", 400, "SESSION_CONTENT_INVALID")
        data = content.encode("utf-8")
        if len(data) > lim["chunk_bytes"]:
            raise ContextError(
                f"upload chunk is too large ({len(data)} bytes; limit {lim['chunk_bytes']})",
                413, "SESSION_CHUNK_TOO_LARGE")
        digest = hashlib.sha256(data).hexdigest()
        sent_hash = str(body.get("sha256") or "").strip().lower()
        if sent_hash and sent_hash != digest:
            raise ContextError("sha256 does not match the uploaded content", 400, "SESSION_HASH_MISMATCH")
        mode = str(body.get("mode") or "append").strip().lower()
        if mode not in {"append", "replace"}:
            raise ContextError("mode must be append or replace", 400, "SESSION_MODE_INVALID")
        try:
            offset = int(body.get("offset") or 0)
        except (TypeError, ValueError) as exc:
            raise ContextError("offset must be a non-negative integer", 400, "SESSION_OFFSET_INVALID") from exc
        if offset < 0 or (mode == "replace" and offset != 0):
            raise ContextError("offset must be 0 for replace and non-negative otherwise",
                               400, "SESSION_REPLACE_OFFSET")
        # ``length`` is the chunk's size in the *source* file when the uploader
        # shortened it (an oversize line replaced by a marker, or bytes that were
        # not valid UTF-8).  It only moves the offset forward.
        try:
            advance = int(body.get("length") or len(data))
        except (TypeError, ValueError) as exc:
            raise ContextError("length must be a positive integer", 400, "SESSION_LENGTH_INVALID") from exc
        if advance < 0 or (data and advance == 0) or advance > 1024 * 1024 * 1024:
            raise ContextError("length must be a positive integer", 400, "SESSION_LENGTH_INVALID")

        with self._lock(session_id):
            meta = self._read_meta(session_id)
            fresh = meta is None
            if fresh:
                meta = {"id": session_id, "kind": "external", "created_at": utc_now(),
                        "owner": owner, "shared": False,
                        "received_bytes": 0, "bytes": 0, "lines": 0, "redactions": 0,
                        "truncated_lines": 0, "chunks": [], "linked_units": []}
            elif not can_manage(meta, owner, admin):
                raise ContextError("this session id belongs to another user", 403,
                                   "SESSION_UPLOAD_NOT_OWNER")
            received = int(meta.get("received_bytes") or 0)
            if mode == "append" and offset != received:
                for chunk in meta.get("chunks") or []:
                    if int(chunk.get("offset", -1)) == offset and chunk.get("sha256") == digest:
                        return {"session": self.public(meta), "received_bytes": received,
                                "appended_lines": 0, "redactions": 0, "duplicate": True}
                raise ContextError(
                    f"offset {offset} does not match the next expected offset {received}",
                    409, "SESSION_OFFSET_MISMATCH")
            lines, objects, redactions, truncated = self._prepare(content, lim["line_bytes"])
            payload = ("\n".join(lines) + "\n") if lines else ""
            added = len(payload.encode("utf-8"))
            current = 0 if mode == "replace" else int(meta.get("bytes") or 0)
            if current + added > lim["session_bytes"]:
                raise ContextError(
                    f"this session would exceed its storage limit of {lim['session_bytes'] // (1024 * 1024)} MB",
                    413, "SESSION_QUOTA")
            project_now = self.usage_bytes() - (int(meta.get("bytes") or 0) if mode == "replace" else 0)
            if project_now + added > lim["project_bytes"]:
                raise ContextError(
                    f"the project's session storage would exceed {lim['project_bytes'] // (1024 * 1024)} MB; "
                    "delete old sessions first", 413, "SESSION_PROJECT_QUOTA")
            path, _ = self._paths(session_id)
            self.root.mkdir(parents=True, exist_ok=True)
            with path.open("w" if mode == "replace" else "a", encoding="utf-8", newline="\n") as fh:
                fh.write(payload)
            sniffed = sniff_metadata(objects) if objects else {}
            for key in ("agent", "model", "cwd", "native_session_id", "title", "prompt"):
                value = body.get(key)
                value = _clean_agent(value) if key == "agent" else _clean(
                    value, MAX_TITLE if key == "title" else MAX_FIELD)
                if value:
                    meta[key] = value
                elif sniffed.get(key) and not meta.get(key):
                    meta[key] = _clean(sniffed[key])
            task = _clean(body.get("task"), 40)
            if task and re.fullmatch(r"[A-Za-z0-9_.:-]+", task):
                meta["task_ref"] = task
            if mode == "replace":
                meta["bytes"] = added
                meta["lines"] = len(lines)
                meta["redactions"] = redactions
                meta["truncated_lines"] = truncated
                meta["chunks"] = []
            else:
                meta["bytes"] = int(meta.get("bytes") or 0) + added
                meta["lines"] = int(meta.get("lines") or 0) + len(lines)
                meta["redactions"] = int(meta.get("redactions") or 0) + redactions
                meta["truncated_lines"] = int(meta.get("truncated_lines") or 0) + truncated
            meta["received_bytes"] = (0 if mode == "replace" else offset) + advance
            chunks = list(meta.get("chunks") or [])
            chunks.append({"offset": offset, "length": advance, "sha256": digest})
            meta["chunks"] = chunks[-MAX_CHUNKS:]
            meta["updated_at"] = utc_now()
            if actor:
                meta["uploaded_by"] = actor
            self._write_meta(meta)
        return {"session": self.public(meta), "received_bytes": meta["received_bytes"],
                "appended_lines": len(lines), "redactions": redactions, "duplicate": False,
                "created": fresh}
