"""Structured views over a task session's append-only logs and workspace.

The session transport is a plain subprocess: stdout/stderr are complete text
lines, not token streams.  This module turns those lines into a conversation
model for the workspace UI without inventing content:

* text lines become assistant messages (Markdown preserved);
* ``[[ark-event]] {json}`` lines are an explicit, documented event protocol an
  adapter (including the bundled echo demo) may emit for messages, tool calls,
  tool results and errors;
* well-known JSONL shapes from Claude Code, Codex and OpenAI-compatible CLIs are
  recognised and mapped to the same events;
* everything unrecognised stays visible as plain assistant text.

The output is deliberately presentation-neutral so it can be unit tested
without a browser or a running process.
"""
from __future__ import annotations

import json
import mimetypes
import re
from datetime import datetime, timezone
from pathlib import Path
from typing import Any, Iterable

ARK_EVENT_PREFIX = "[[ark-event]]"
MAX_SOURCE_LINES = 4000
MAX_TEXT_CHARS = 120_000
MAX_WORKSPACE_FILES = 400
MAX_WORKSPACE_DEPTH = 6
ANSI_RE = re.compile(r"\x1b\[[0-9;]*m")

IMAGE_SUFFIXES = {".png", ".jpg", ".jpeg", ".webp", ".gif", ".svg", ".avif", ".bmp", ".ico"}
VIDEO_SUFFIXES = {".mp4", ".webm", ".mov", ".m4v", ".ogv"}
AUDIO_SUFFIXES = {".mp3", ".wav", ".ogg", ".m4a", ".flac", ".aac"}
DOCUMENT_SUFFIXES = {
    ".md", ".markdown", ".mdx", ".txt", ".rst", ".tex", ".json", ".jsonl",
    ".csv", ".tsv", ".log", ".html", ".htm", ".xml", ".yaml", ".yml",
    ".toml", ".ini", ".cfg", ".py", ".js", ".mjs", ".ts", ".css", ".sql",
    ".ps1", ".sh", ".bat",
}
PDF_SUFFIXES = {".pdf"}

TOOL_CALL_TYPES = {"tool_call", "tool_use", "function_call", "tool_start", "command"}
TOOL_RESULT_TYPES = {"tool_result", "tool_output", "function_result", "tool_end"}
MESSAGE_TYPES = {"message", "assistant", "assistant_message", "markdown", "content"}
USER_TYPES = {"user", "user_message"}
ERROR_TYPES = {"error", "failed", "failure"}
NOTICE_TYPES = {"system", "notice", "status", "info"}
REASONING_TYPES = {"reasoning", "thinking", "thought"}
USAGE_TYPES = {"usage", "tokens", "token_usage"}

# DSH writes reasoning to stderr as a marker line followed by plain text.
# It is not a process failure; keep it as a streaming reasoning notice.
REASONING_MARKER_RE = re.compile(
    r"^\s*(?:dsh\s*:\s*)?(?:reasoning|thinking|thought)\s*:\s*$", re.IGNORECASE)
DSH_EVENT_RE = re.compile(r"^\s*dsh\s*:\s*([A-Za-z][\w-]*)\s*:\s*(.*)$")

# `codex exec` writes its human-readable transcript to stderr, not stdout:
# banner, metadata, a "user" block, a "codex" block, then "tokens used".
# Treat those lines as conversation, not process failures.  Real diagnostics
# still match CODEX_ERROR_RE and become error items.
CODEX_HEADER_RE = re.compile(
    r"^\s*(?:OpenAI Codex v|--------|workdir:|model:|provider:|approval:|"
    r"sandbox:|reasoning effort:|reasoning summaries:|session id:|"
    r"mcp startup:)", re.IGNORECASE)
CODEX_ERROR_RE = re.compile(
    r"^\s*(?:ERROR\b|error:|fatal:|failed\b|unexpected status|"
    r"invalid api key|not authenticated|panic:|request failed)", re.IGNORECASE)

# Interactive approval protocol.  An adapter that can ask before a mutating
# action emits one of these lines; ARK records the request and writes a
# ``permission_response`` (or a Claude-style ``control_response``) back to the
# adapter's stdin.  Nothing here executes a tool by itself.
PERMISSION_REQUEST_TYPES = {
    "permission_request", "permission.request", "permission_required",
    "approval_request", "approval_required", "tool_permission_request",
}
PERMISSION_RESOLUTION_TYPES = {
    "permission_resolved", "permission_resolved_event", "permission_cancelled",
    "permission_canceled", "permission_cancelled_event", "approval_resolved",
}
PERMISSION_KINDS = ("command", "file_edit", "network", "tool", "other")
PERMISSION_KIND_ALIASES = {
    "bash": "command", "shell": "command", "exec": "command", "execute": "command",
    "command_execution": "command", "terminal": "command", "process": "command",
    "edit": "file_edit", "write": "file_edit", "write_file": "file_edit",
    "file": "file_edit", "file_change": "file_edit", "filechange": "file_edit",
    "patch": "file_edit", "apply_patch": "file_edit", "file_write": "file_edit",
    "fetch": "network", "http": "network", "web": "network", "browser": "network",
    "mcp": "tool", "tool_use": "tool", "tool_call": "tool", "function": "tool",
}
PERMISSION_KIND_TOOLS = {
    "command": {"bash", "shell", "exec", "execute", "terminal", "command", "cmd", "powershell"},
    "file_edit": {"write", "edit", "write_file", "create_file", "apply_patch", "patch",
                  "str_replace", "str_replace_editor", "multiedit", "file_change"},
    "network": {"fetch", "webfetch", "web_fetch", "web_search", "http", "browse", "browser"},
}

# Presentation-only language hints for the file tab strip and code blocks.
LANGUAGE_BY_SUFFIX = {
    ".bat": "bat", ".c": "c", ".cfg": "ini", ".conf": "ini", ".cpp": "cpp",
    ".cs": "csharp", ".css": "css", ".csv": "csv", ".go": "go", ".h": "c",
    ".hpp": "cpp", ".htm": "html", ".html": "html", ".ini": "ini", ".java": "java",
    ".js": "javascript", ".json": "json", ".jsonl": "jsonl", ".jsx": "jsx",
    ".log": "log", ".md": "markdown", ".markdown": "markdown", ".mdx": "markdown",
    ".mjs": "javascript", ".ps1": "powershell", ".py": "python", ".rb": "ruby",
    ".rs": "rust", ".rst": "rst", ".sh": "bash", ".sql": "sql", ".svg": "xml",
    ".tex": "latex", ".toml": "toml", ".ts": "typescript", ".tsx": "tsx",
    ".txt": "text", ".xml": "xml", ".yaml": "yaml", ".yml": "yaml",
}


def language_for(name: Any) -> str:
    """Best-effort language id for a filename (used by UI highlighting only)."""
    suffix = Path(str(name or "")).suffix.lower()
    return LANGUAGE_BY_SUFFIX.get(suffix, "")


def utc_now() -> str:
    return datetime.now(timezone.utc).isoformat(timespec="seconds")


def clean_line(value: Any) -> str:
    return ANSI_RE.sub("", str(value or "")).replace("\r", "").rstrip()


def _truncate(value: str, limit: int = MAX_TEXT_CHARS) -> str:
    value = str(value or "")
    return value if len(value) <= limit else value[:limit] + "\n…[truncated]"


def _safe_json(value: Any) -> Any:
    """JSON-friendly value for tool input/output (never raises)."""
    if value is None or isinstance(value, (bool, int, float)):
        return value
    if isinstance(value, str):
        return _truncate(value)
    if isinstance(value, (list, tuple)):
        return [_safe_json(item) for item in value][:200]
    if isinstance(value, dict):
        return {str(key): _safe_json(item) for key, item in list(value.items())[:200]}
    return _truncate(str(value))


def _format_value(value: Any) -> str:
    if value is None:
        return ""
    if isinstance(value, str):
        return value
    try:
        return json.dumps(value, ensure_ascii=False, indent=2)
    except (TypeError, ValueError):
        return str(value)


def _tool_status(value: Any, *, default: str = "ok") -> str:
    text = str(value or "").strip().lower()
    if text in {"error", "failed", "failure", "err", "nonzero"}:
        return "error"
    if text in {"running", "started", "pending", "in_progress"}:
        return "running"
    if text in {"ok", "success", "succeeded", "completed", "done", "exit_0"}:
        return "ok"
    return default


USAGE_KEYS = ("input_tokens", "output_tokens", "total_tokens",
              "cache_read_input_tokens", "cached_input_tokens", "reasoning_tokens")
USAGE_ALIASES = {"prompt_tokens": "input_tokens", "completion_tokens": "output_tokens",
                 "cached_tokens": "cache_read_input_tokens"}


def _usage_numbers(raw: Any) -> dict:
    """Normalise token counters from adapters; never invent exact values."""
    if not isinstance(raw, dict):
        return {}
    source = raw.get("usage") if isinstance(raw.get("usage"), dict) else raw
    out: dict = {}
    for key, value in source.items():
        target = USAGE_ALIASES.get(str(key), str(key))
        if target not in USAGE_KEYS:
            continue
        try:
            number = int(value)
        except (TypeError, ValueError):
            continue
        if number >= 0:
            out[target] = out.get(target, 0) + number
    if "total_tokens" not in out and ("input_tokens" in out or "output_tokens" in out):
        out["total_tokens"] = out.get("input_tokens", 0) + out.get("output_tokens", 0)
    return out


def _event(kind: str, **payload: Any) -> dict:
    return {"event": kind, **payload}


def parse_ark_event(line: str) -> dict | None:
    """Parse the explicit adapter event protocol, or return ``None``."""
    text = line.strip()
    if not text.startswith(ARK_EVENT_PREFIX):
        return None
    raw = text[len(ARK_EVENT_PREFIX):].strip()
    if not raw:
        return None
    try:
        data = json.loads(raw)
    except ValueError:
        return None
    return data if isinstance(data, dict) else None


def _pick(sources: tuple[dict, ...], keys: tuple[str, ...]) -> Any:
    """First non-empty value for any of ``keys`` across nested request objects."""
    for source in sources:
        if not isinstance(source, dict):
            continue
        for key in keys:
            value = source.get(key)
            if value not in (None, "", [], {}):
                return value
    return None


def _text(value: Any, limit: int = 2000) -> str:
    if value is None:
        return ""
    if isinstance(value, (list, tuple)):
        value = " ".join(str(item) for item in value)
    if isinstance(value, dict):
        try:
            value = json.dumps(value, ensure_ascii=False)
        except (TypeError, ValueError):
            value = str(value)
    return str(value).strip()[:limit]


def _permission_kind(value: Any, tool: str, command: str, path: str) -> str:
    explicit = str(value or "").strip().lower().replace("-", "_").replace(" ", "_")
    if explicit in PERMISSION_KINDS:
        return explicit
    if explicit in PERMISSION_KIND_ALIASES:
        return PERMISSION_KIND_ALIASES[explicit]
    if str(tool or "").strip().lower() in PERMISSION_KIND_TOOLS["file_edit"]:
        return "file_edit"
    if command:
        return "command"
    if path:
        return "file_edit"
    lowered = str(tool or "").strip().lower()
    for kind, names in PERMISSION_KIND_TOOLS.items():
        if lowered in names:
            return kind
    return "tool" if lowered else "other"


def normalize_permission_request(obj: Any) -> dict | None:
    """Normalise a recognised permission-request JSON object, or return ``None``.

    Two shapes are understood natively:

    * the ARK/generic JSONL shape (``type|kind: permission_request`` with
      ``request_id``/``tool``/``input``), including the same object embedded in
      an ``[[ark-event]]`` line;
    * Claude Code stream-json ``control_request`` / ``can_use_tool`` frames.
    """
    if not isinstance(obj, dict):
        return None
    etype = str(obj.get("type") or obj.get("event") or "").strip().lower()
    protocol = "ark-permission-v1"
    source = obj
    if etype == "control_request":
        request = obj.get("request")
        if not isinstance(request, dict):
            return None
        subtype = str(request.get("subtype") or "").strip().lower()
        if subtype not in {"can_use_tool", "permission_request", "approval_request"}:
            return None
        protocol = "claude-control"
        source = {
            **request,
            "request_id": obj.get("request_id") or request.get("request_id"),
        }
    elif etype not in PERMISSION_REQUEST_TYPES:
        return None
    nested = source.get("request") if isinstance(source.get("request"), dict) else {}
    sources = (source, nested)
    request_id = _pick(sources, ("request_id", "requestId", "id", "approval_id",
                                 "approvalId", "tool_call_id", "call_id"))
    tool = _pick(sources, ("tool", "tool_name", "toolName", "name", "function"))
    if isinstance(tool, dict):
        tool = tool.get("name") or tool.get("tool")
    tool = _text(tool, 120)
    args = _pick(sources, ("input", "arguments", "args", "params", "parameters", "details"))
    command = _pick(sources, ("command", "cmd", "argv", "script", "command_line"))
    if not command and isinstance(args, dict):
        command = args.get("command") or args.get("cmd") or args.get("argv")
    command = _text(command, 2000)
    path = _pick(sources, ("path", "file", "file_path", "filepath", "target", "uri", "resource"))
    if not path and isinstance(args, dict):
        path = args.get("path") or args.get("file") or args.get("file_path")
    path = _text(path, 500)
    kind = _pick(sources, ("approval_kind", "permission_kind", "category", "action", "kind", "type"))
    kind = _permission_kind(kind, tool, command, path)
    description = _pick(sources, ("description", "reason", "message", "prompt", "title",
                                  "tool_description"))
    description = _text(description, 600)
    if not (request_id or tool or command or path):
        return None
    return {
        "request_id": _text(request_id, 120),
        "kind": kind,
        "tool": tool,
        "command": command,
        "path": path,
        "description": description,
        "input": _safe_json(args),
        "protocol": protocol,
        "native_id": _text(obj.get("id") or obj.get("request_id"), 120) if protocol == "claude-control" else "",
    }


def permission_request_from_line(line: str) -> dict | None:
    """Return the permission request carried by one output line, if any."""
    explicit = parse_ark_event(line)
    if explicit is not None:
        return normalize_permission_request(explicit)
    text = str(line or "").strip()
    if not (text.startswith("{") and text.endswith("}")):
        return None
    try:
        obj = json.loads(text)
    except ValueError:
        return None
    return normalize_permission_request(obj)


def normalize_permission_resolution(obj: Any) -> dict | None:
    if not isinstance(obj, dict):
        return None
    etype = str(obj.get("type") or obj.get("event") or "").strip().lower()
    if etype not in PERMISSION_RESOLUTION_TYPES:
        return None
    request_id = _pick((obj,), ("request_id", "requestId", "id", "approval_id"))
    if not request_id:
        return None
    status = "cancelled" if "cancel" in etype else "resolved"
    return {"request_id": _text(request_id, 120), "status": status,
            "message": _text(obj.get("message") or obj.get("reason"), 300)}


def permission_resolution_from_line(line: str) -> dict | None:
    explicit = parse_ark_event(line)
    if explicit is not None:
        return normalize_permission_resolution(explicit)
    text = str(line or "").strip()
    if not (text.startswith("{") and text.endswith("}")):
        return None
    try:
        obj = json.loads(text)
    except ValueError:
        return None
    return normalize_permission_resolution(obj)


def build_permission_response(request: dict, decision: str, scope: str,
                              message: str = "") -> dict:
    """The frame ARK writes to adapter stdin for one decided request.

    ``decision`` is ``allow`` or ``deny``; ``scope`` is ``once``, ``always`` or
    ``auto``.  Claude Code control frames get a matching ``control_response``;
    every other adapter receives the documented generic frame.
    """
    allow = str(decision).lower() == "allow"
    request_id = _text(request.get("request_id") or request.get("id"), 120)
    if str(request.get("protocol") or "") == "claude-control":
        native = _text(request.get("native_id") or request_id, 120)
        response: dict = {"behavior": "allow" if allow else "deny"}
        if not allow and message:
            response["message"] = _text(message, 600)
        elif allow and isinstance(request.get("input"), dict):
            response["updatedInput"] = request["input"]
        return {
            "type": "control_response",
            "response": {"subtype": "success", "request_id": native, "response": response},
        }
    return {
        "type": "permission_response",
        "protocol": "ark-permission-v1",
        "id": request_id,
        "request_id": request_id,
        "decision": "allow" if allow else "deny",
        "scope": str(scope or "once"),
        "message": _text(message, 600),
    }


def _blocks(value: Any) -> list[dict]:
    if isinstance(value, list):
        return [item for item in value if isinstance(item, dict)]
    return []


def _events_from_tool_call(obj: dict) -> list[dict]:
    name = obj.get("name") or obj.get("tool") or obj.get("tool_name") or obj.get("function")
    if isinstance(name, dict):
        name = name.get("name")
    args = obj.get("input", obj.get("arguments", obj.get("args", obj.get("parameters"))))
    call_id = obj.get("id") or obj.get("tool_call_id") or obj.get("call_id")
    output = obj.get("output", obj.get("result", obj.get("content")))
    status = _tool_status(obj.get("status"), default="ok" if output is not None else "running")
    if "exit_code" in obj:
        status = "ok" if obj.get("exit_code") in (0, None) else "error"
    if not name and output is None:
        return []
    out = [_event("tool_call", id=call_id, name=str(name or "tool"),
                  input=_safe_json(args), status="running")]
    if output is not None:
        out.append(_event("tool_result", id=call_id, name=str(name or "tool"),
                          output=_safe_json(output), status=status))
    return out


def _events_from_content(content: Any, *, role: str = "assistant") -> list[dict]:
    if isinstance(content, str):
        return [_event("message", role=role, text=content)] if content.strip() else []
    events: list[dict] = []
    for block in _blocks(content):
        btype = str(block.get("type") or "").lower()
        if btype in {"text", "output_text", "input_text"}:
            text = block.get("text") or block.get("content") or ""
            if text:
                events.append(_event("message", role=role, text=text))
        elif btype in TOOL_CALL_TYPES or btype in {"tool_use", "server_tool_use"}:
            events.extend(_events_from_tool_call(block))
        elif btype in TOOL_RESULT_TYPES:
            call_id = block.get("tool_use_id") or block.get("tool_call_id") or block.get("id")
            output = block.get("content", block.get("output", block.get("result")))
            if isinstance(output, list):
                output = "\n".join(str(item.get("text") or "") for item in output if isinstance(item, dict))
            events.append(_event("tool_result", id=call_id,
                                 output=_safe_json(output),
                                 status=_tool_status(block.get("is_error") and "error" or block.get("status"))))
        elif btype in REASONING_TYPES:
            events.append(_event("reasoning", text=str(block.get("text") or block.get("thinking") or "")))
    return events


def _events_from_codex_item(item: dict, completed: bool) -> list[dict]:
    itype = str(item.get("type") or "").lower()
    status = "ok" if completed else "running"
    if itype in {"agent_message", "assistant_message"}:
        text = item.get("text") or item.get("content") or ""
        return [_event("message", role="assistant", text=text, markdown=True)] if text else []
    if itype in {"reasoning", "agent_reasoning"}:
        text = item.get("text") or item.get("content") or ""
        return [_event("reasoning", text=text)] if text else []
    if itype in {"command_execution", "command"}:
        output = item.get("aggregated_output", item.get("output", item.get("stdout")))
        if output is None and item.get("stderr"):
            output = item.get("stderr")
        if "exit_code" in item:
            status = "ok" if item.get("exit_code") in (0, None) else "error"
        call_id = item.get("id")
        out = [_event("tool_call", id=call_id, name="command",
                      input=_safe_json(item.get("command") or item.get("input")),
                      status="running")]
        if completed or output is not None:
            out.append(_event("tool_result", id=call_id, name="command",
                              output=_safe_json(output), status=status))
        return out
    if itype in {"mcp_tool_call", "tool_call", "web_search"}:
        name = item.get("tool") or item.get("name")
        if item.get("server") and item.get("tool"):
            name = f"{item.get('server')}.{item.get('tool')}"
        call_id = item.get("id")
        out = [_event("tool_call", id=call_id, name=str(name or itype),
                      input=_safe_json(item.get("arguments", item.get("input"))),
                      status="running")]
        output = item.get("result", item.get("output"))
        if completed or output is not None:
            out.append(_event("tool_result", id=call_id, name=str(name or itype),
                              output=_safe_json(output), status=status))
        return out
    if itype == "file_change":
        call_id = item.get("id")
        changes = item.get("changes", item.get("paths"))
        return [
            _event("tool_call", id=call_id, name="file_change", input=_safe_json(changes), status="running"),
            _event("tool_result", id=call_id, name="file_change", output="file change recorded",
                   status=status),
        ]
    return []


def _events_from_json(obj: dict) -> list[dict] | None:
    """Map a well-known JSONL object to events; ``None`` means "show as text"."""
    etype = str(obj.get("type") or "").strip().lower()
    role = str(obj.get("role") or "").strip().lower()

    if etype in PERMISSION_REQUEST_TYPES or etype == "control_request":
        permission = normalize_permission_request(obj)
        return [_event("permission", request=permission)] if permission else None
    if etype in PERMISSION_RESOLUTION_TYPES:
        resolution = normalize_permission_resolution(obj)
        return [_event("permission_resolution", resolution=resolution)] if resolution else None

    if etype in {"item.completed", "item.started", "item.updated"} and isinstance(obj.get("item"), dict):
        return _events_from_codex_item(obj["item"], etype == "item.completed")

    if etype in USAGE_TYPES:
        numbers = _usage_numbers(obj)
        return [_event("usage", **numbers)] if numbers else None

    if not etype and isinstance(obj.get("usage"), dict):
        numbers = _usage_numbers(obj)
        return [_event("usage", **numbers)] if numbers else None

    if etype in {"result"} and isinstance(obj.get("result"), str):
        events = [_event("message", role="assistant", text=obj["result"], markdown=True)]
        numbers = _usage_numbers(obj)
        if numbers:
            events.append(_event("usage", **numbers))
        return events

    if etype in {"result"} and isinstance(obj.get("usage"), dict):
        numbers = _usage_numbers(obj)
        return [_event("usage", **numbers)] if numbers else None

    if etype in {"assistant", "message"} and isinstance(obj.get("message"), dict):
        message = obj["message"]
        events = _events_from_content(message.get("content"), role="assistant")
        if not events and isinstance(message.get("text"), str):
            events = [_event("message", role="assistant", text=message["text"])]
        return events or None

    if etype in {"user"} and isinstance(obj.get("message"), dict):
        return _events_from_content(obj["message"].get("content"), role="user") or None

    if etype in TOOL_CALL_TYPES:
        return _events_from_tool_call(obj)
    if etype in TOOL_RESULT_TYPES:
        return [_event("tool_result",
                       id=obj.get("tool_use_id") or obj.get("tool_call_id") or obj.get("id"),
                       name=obj.get("name") or obj.get("tool_name"),
                       output=_safe_json(obj.get("output", obj.get("content", obj.get("result")))),
                       status=_tool_status(obj.get("status")))]
    if etype in ERROR_TYPES:
        return [_event("error", text=str(obj.get("message") or obj.get("error") or obj.get("text") or ""))]
    if etype in NOTICE_TYPES:
        text = obj.get("message") or obj.get("text") or obj.get("subtype")
        return [_event("notice", text=str(text or ""))] if text else None
    if etype in REASONING_TYPES:
        text = obj.get("text") or obj.get("reasoning") or obj.get("content")
        return [_event("reasoning", text=str(text or ""))] if text else None
    if etype in MESSAGE_TYPES and not isinstance(obj.get("message"), dict):
        text = obj.get("text") or obj.get("content") or obj.get("markdown")
        if isinstance(text, str) and text.strip():
            return [_event("message", role=role or "assistant", text=text,
                           markdown=bool(obj.get("markdown") or etype in {"markdown", "content"}))]

    if isinstance(obj.get("choices"), list) and obj["choices"]:
        choice = obj["choices"][0] if isinstance(obj["choices"][0], dict) else {}
        message = choice.get("message") or choice.get("delta") or {}
        events: list[dict] = []
        if isinstance(message, dict):
            content = message.get("content")
            if isinstance(content, str) and content.strip():
                events.append(_event("message", role=str(message.get("role") or "assistant"), text=content))
            elif isinstance(content, list):
                events.extend(_events_from_content(content, role=str(message.get("role") or "assistant")))
            for call in message.get("tool_calls") or []:
                if not isinstance(call, dict):
                    continue
                fn = call.get("function") if isinstance(call.get("function"), dict) else {}
                events.extend(_events_from_tool_call({
                    "id": call.get("id"),
                    "name": fn.get("name") or call.get("name"),
                    "arguments": fn.get("arguments") or call.get("arguments"),
                }))
        numbers = _usage_numbers(obj)
        if numbers:
            events.append(_event("usage", **numbers))
        return events or None

    if role in {"assistant", "user"} and not etype:
        events = _events_from_content(obj.get("content"), role=role)
        if not events and isinstance(obj.get("text"), str):
            events = [_event("message", role=role, text=obj["text"])]
        return events or None

    if isinstance(obj.get("tool_calls"), list):
        events = []
        for call in obj["tool_calls"]:
            if isinstance(call, dict):
                fn = call.get("function") if isinstance(call.get("function"), dict) else {}
                events.extend(_events_from_tool_call({
                    "id": call.get("id"), "name": fn.get("name") or call.get("name"),
                    "arguments": fn.get("arguments") or call.get("arguments"),
                }))
        return events or None
    return None


def events_from_line(line: str) -> list[dict] | None:
    """Return explicit/recognised events for one output line, else ``None``."""
    explicit = parse_ark_event(line)
    if explicit is not None:
        permission = normalize_permission_request(explicit)
        if permission is not None:
            return [_event("permission", request=permission)]
        resolution = normalize_permission_resolution(explicit)
        if resolution is not None:
            return [_event("permission_resolution", resolution=resolution)]
        etype = str(explicit.get("type") or explicit.get("event") or "").lower()
        if etype in TOOL_CALL_TYPES:
            return [_event("tool_call", id=explicit.get("id"),
                           name=explicit.get("name") or explicit.get("tool") or "tool",
                           input=_safe_json(explicit.get("input", explicit.get("arguments"))),
                           status=_tool_status(explicit.get("status"), default="running"))]
        if etype in TOOL_RESULT_TYPES:
            return [_event("tool_result", id=explicit.get("id"),
                           name=explicit.get("name"),
                           output=_safe_json(explicit.get("output", explicit.get("result"))),
                           status=_tool_status(explicit.get("status")))]
        if etype in ERROR_TYPES:
            return [_event("error", text=str(explicit.get("text") or explicit.get("message") or ""))]
        if etype in REASONING_TYPES:
            return [_event("reasoning", text=str(explicit.get("text") or ""))]
        if etype in USAGE_TYPES:
            numbers = _usage_numbers(explicit)
            return [_event("usage", **numbers)] if numbers else None
        if etype in NOTICE_TYPES:
            return [_event("notice", text=str(explicit.get("text") or explicit.get("message") or ""),
                           level=str(explicit.get("level") or "info"))]
        if etype in USER_TYPES:
            return [_event("message", role="user", text=str(explicit.get("text") or explicit.get("content") or ""))]
        text = explicit.get("text") or explicit.get("content") or explicit.get("markdown")
        if isinstance(text, str) and text.strip():
            return [_event("message", role=str(explicit.get("role") or "assistant"), text=text,
                           markdown=bool(explicit.get("markdown", True)))]
        return None
    text = line.strip()
    if not (text.startswith("{") and text.endswith("}")):
        return None
    try:
        obj = json.loads(text)
    except ValueError:
        return None
    if not isinstance(obj, dict):
        return None
    return _events_from_json(obj)


def build_transcript(
    *,
    session_id: str,
    status: str = "",
    prompt: str = "",
    prompt_at: str | None = None,
    stdout: Iterable[dict] | None = None,
    stderr: Iterable[dict] | None = None,
    error: str = "",
    notice: str = "",
    generated_at: str | None = None,
    platform: str = "",
) -> dict:
    """Turn raw log entries into ordered conversation items.

    ``stdout``/``stderr`` entries are ``{"t": iso, "line": str, "kind"?: str}``
    as written by :class:`arkstudio.sessions.ManagedSession`.
    """
    items: list[dict] = []
    tools: dict[str, dict] = {}
    permissions: dict[str, dict] = {}
    open_assistant: dict | None = None
    counter = {"n": 0}
    reported_usage: dict = {}
    usage_events = 0
    active = status in {"starting", "running", "idle", "stopping"}

    def next_id(prefix: str) -> str:
        counter["n"] += 1
        return f"{prefix}-{counter['n']}"

    def add_message(role: str, text: str, at: str | None, *, source: str = "stdout",
                    markdown: bool = True, level: str = "") -> dict | None:
        nonlocal open_assistant
        text = _truncate(clean_line(text))
        if not text.strip():
            return None
        if role == "assistant" and open_assistant is not None and markdown:
            open_assistant["text"] = _truncate(open_assistant["text"] + "\n" + text)
            open_assistant["at"] = at or open_assistant.get("at")
            return open_assistant
        item = {
            "id": next_id("m"), "type": "message", "role": role, "text": text,
            "at": at, "markdown": bool(markdown), "source": source,
        }
        if level:
            item["level"] = level
        items.append(item)
        open_assistant = item if role == "assistant" else None
        return item

    def add_notice(text: str, at: str | None, *, level: str = "info", source: str = "stdout") -> None:
        nonlocal open_assistant
        text = _truncate(clean_line(text))
        if not text.strip():
            return
        if items and items[-1]["type"] == "notice" and items[-1].get("level") == level and items[-1].get("source") == source:
            items[-1]["text"] = _truncate(items[-1]["text"] + "\n" + text)
            return
        open_assistant = None
        items.append({"id": next_id("n"), "type": "notice", "level": level,
                      "text": text, "at": at, "source": source})

    def add_error(text: str, at: str | None, *, source: str = "stderr") -> None:
        nonlocal open_assistant
        text = _truncate(clean_line(text))
        if not text.strip():
            return
        open_assistant = None
        items.append({"id": next_id("e"), "type": "error", "text": text,
                      "at": at, "source": source})

    def add_tool_call(call: dict) -> dict:
        nonlocal open_assistant
        open_assistant = None
        call_id = str(call.get("id") or "")
        existing = tools.get(call_id) if call_id else None
        if existing is not None:
            existing["name"] = call.get("name") or existing.get("name")
            if call.get("input") is not None:
                existing["input"] = call.get("input")
            if call.get("status"):
                existing["status"] = call["status"]
            return existing
        item = {
            "id": call_id or next_id("t"),
            "type": "tool",
            "name": str(call.get("name") or "tool"),
            "input": call.get("input"),
            "output": None,
            "status": _tool_status(call.get("status"), default="running"),
            "at": call.get("at"),
            "completed_at": None,
            "source": call.get("source") or "stdout",
        }
        items.append(item)
        if call_id:
            tools[call_id] = item
        else:
            tools[item["id"]] = item
        return item

    def add_tool_result(result: dict) -> None:
        nonlocal open_assistant
        open_assistant = None
        call_id = str(result.get("id") or "")
        item = tools.get(call_id) if call_id else None
        if item is None and result.get("name"):
            for candidate in reversed(items):
                if candidate["type"] == "tool" and candidate["name"] == result["name"] and candidate["status"] == "running":
                    item = candidate
                    break
        if item is None:
            item = add_tool_call({
                "id": call_id or None,
                "name": result.get("name") or "tool",
                "input": None,
                "status": "running",
                "at": result.get("at"),
            })
        item["output"] = result.get("output")
        item["status"] = _tool_status(result.get("status"), default="ok")
        item["completed_at"] = result.get("at") or item.get("at")

    def add_permission(request: dict) -> dict:
        nonlocal open_assistant
        open_assistant = None
        request_id = str(request.get("request_id") or "")
        existing = permissions.get(request_id) if request_id else None
        if existing is not None:
            return existing
        item = {
            "id": "perm-" + (request_id or next_id("p")),
            "type": "permission",
            "request_id": request_id,
            "kind": str(request.get("kind") or "other"),
            "tool": str(request.get("tool") or ""),
            "command": str(request.get("command") or ""),
            "path": str(request.get("path") or ""),
            "description": str(request.get("description") or ""),
            "input": request.get("input"),
            "protocol": str(request.get("protocol") or "ark-permission-v1"),
            "status": "pending",
            "at": request.get("at"),
            "source": request.get("source") or "stdout",
        }
        items.append(item)
        if request_id:
            permissions[request_id] = item
        return item

    def add_permission_resolution(resolution: dict) -> None:
        request_id = str(resolution.get("request_id") or "")
        item = permissions.get(request_id)
        if item is None or item.get("status") != "pending":
            return
        item["status"] = "cancelled" if resolution.get("status") == "cancelled" else "resolved"
        item["message"] = str(resolution.get("message") or "")
        item["resolved_at"] = resolution.get("at")

    def handle_events(events: list[dict], at: str | None, source: str) -> None:
        nonlocal usage_events
        for event in events:
            kind = str(event.get("event") or "").lower()
            if kind == "message":
                role = str(event.get("role") or "assistant").lower()
                add_message(role if role in {"user", "assistant"} else "assistant",
                            str(event.get("text") or ""), at, source=source,
                            markdown=bool(event.get("markdown", role != "user")))
            elif kind == "tool_call":
                add_tool_call({**event, "at": at, "source": source})
            elif kind == "tool_result":
                add_tool_result({**event, "at": at})
            elif kind == "permission":
                request = event.get("request")
                if isinstance(request, dict):
                    add_permission({**request, "at": at, "source": source})
            elif kind == "permission_resolution":
                resolution = event.get("resolution")
                if isinstance(resolution, dict):
                    add_permission_resolution({**resolution, "at": at})
            elif kind == "reasoning":
                text = str(event.get("text") or "").strip()
                if text:
                    add_notice(text, at, level="reasoning", source=source)
            elif kind == "usage":
                numbers = _usage_numbers(event)
                if numbers:
                    usage_events += 1
                    for key, value in numbers.items():
                        reported_usage[key] = reported_usage.get(key, 0) + value
            elif kind == "error":
                add_error(str(event.get("text") or ""), at, source=source)
            else:
                add_notice(str(event.get("text") or ""), at,
                           level=str(event.get("level") or "info"), source=source)

    if prompt and prompt.strip():
        add_message("user", prompt, prompt_at, source="prompt", markdown=False)

    combined: list[tuple[str, int, dict]] = []
    for index, entry in enumerate(stdout or []):
        combined.append(("stdout", index, entry))
    for index, entry in enumerate(stderr or []):
        combined.append(("stderr", index, entry))
    combined.sort(key=lambda row: (str(row[2].get("t") or ""), 0 if row[0] == "stdout" else 1, row[1]))

    reasoning_stream = False
    codex_mode = str(platform or "").lower() == "codex"
    codex_speaker = ""
    codex_usage_number = False
    codex_done = False
    for stream, _index, entry in combined:
        line = clean_line(entry.get("line"))
        if not line.strip():
            continue
        at = entry.get("t")
        if entry.get("kind") == "input":
            reasoning_stream = False
            add_message("user", re.sub(r"^\s*[›❯>]\s*", "", line), at, source="stdin", markdown=False)
            continue
        if stream == "stderr" and codex_mode:
            marker = line.strip()
            if codex_done:
                # `codex exec` echoes the final answer again after the
                # "tokens used" counter.  The answer is already in the
                # `codex` block above; do not render the same response twice.
                continue
            if codex_usage_number and re.fullmatch(r"[\d,]+", marker):
                codex_usage_number = False
                codex_done = True
                continue
            if marker.lower() == "tokens used":
                codex_usage_number = True
                continue
            if marker.lower() == "user":
                codex_speaker = "user"
                continue
            if marker.lower() == "codex":
                codex_speaker = "codex"
                continue
            if CODEX_HEADER_RE.match(line):
                continue
            if CODEX_ERROR_RE.match(line):
                add_error(line, at, source="stderr")
                continue
            if marker.lower().startswith(("warning:", "thinking:", "reasoning:")):
                add_notice(line, at, level="reasoning", source="stderr")
                continue
            if codex_speaker == "user":
                # The prompt is already the first transcript item.
                continue
            if codex_speaker == "codex":
                add_message("assistant", line, at, source="stderr", markdown=True)
            else:
                add_notice(line, at, level="info", source="stderr")
            continue
        if stream == "stderr":
            if REASONING_MARKER_RE.match(line):
                reasoning_stream = True
                continue
            if reasoning_stream:
                marker = DSH_EVENT_RE.match(line)
                if marker and marker.group(1).lower() not in {"reasoning", "thinking", "thought"}:
                    reasoning_stream = False
                else:
                    add_notice(line, at, level="reasoning", source="stderr")
                    continue
            add_error(line, at)
            continue
        reasoning_stream = False
        events = events_from_line(line)
        if events:
            handle_events(events, at, "stdout")
        else:
            add_message("assistant", line, at, source="stdout", markdown=True)

    if error:
        add_error(error, None, source="meta")
    if notice:
        add_notice(notice, None, level="notice", source="meta")

    active_items = [item for item in items if item["type"] in {"message", "tool"}]
    if active and active_items:
        last = active_items[-1]
        if last["type"] == "message" and last["role"] == "assistant":
            last["streaming"] = True
        elif last["type"] == "tool" and last.get("status") == "running":
            last["streaming"] = True
    if active and items and items[-1].get("type") == "notice" and items[-1].get("level") == "reasoning":
        items[-1]["streaming"] = True

    input_chars = sum(len(item.get("text") or "") for item in items
                      if item["type"] == "message" and item["role"] == "user")
    input_chars += sum(len(_format_value(item.get("input"))) for item in items
                       if item["type"] == "tool")
    output_chars = sum(len(item.get("text") or "") for item in items
                       if item["type"] == "message" and item["role"] == "assistant")
    output_chars += sum(len(_format_value(item.get("output"))) for item in items
                        if item["type"] == "tool")
    estimated_usage = {
        "input_tokens": (input_chars + 3) // 4,
        "output_tokens": (output_chars + 3) // 4,
    }
    estimated_usage["total_tokens"] = (estimated_usage["input_tokens"]
                                       + estimated_usage["output_tokens"])
    usage: dict = {
        "source": "reported" if usage_events else "estimated",
        "estimated": not usage_events,
        "reported_events": usage_events,
        "reported": dict(reported_usage),
        "estimated_usage": estimated_usage,
        "input_chars": input_chars,
        "output_chars": output_chars,
    }
    effective = reported_usage if usage_events else estimated_usage
    for key in ("input_tokens", "output_tokens", "total_tokens"):
        usage[key] = int(effective.get(key) or 0)
    if not usage["total_tokens"]:
        usage["total_tokens"] = usage["input_tokens"] + usage["output_tokens"]
    for key in ("cache_read_input_tokens", "cached_input_tokens", "reasoning_tokens"):
        if key in reported_usage:
            usage[key] = reported_usage[key]

    stats = {
        "messages": sum(1 for item in items if item["type"] == "message"),
        "user_messages": sum(1 for item in items if item["type"] == "message" and item["role"] == "user"),
        "assistant_messages": sum(1 for item in items if item["type"] == "message" and item["role"] == "assistant"),
        "tools": sum(1 for item in items if item["type"] == "tool"),
        "permissions": sum(1 for item in items if item["type"] == "permission"),
        "pending_permissions": sum(1 for item in items
                                   if item["type"] == "permission" and item.get("status") == "pending"),
        "errors": sum(1 for item in items if item["type"] == "error"),
        "tokens": usage["total_tokens"],
    }
    last = items[-1] if items else None
    signature = "|".join([
        session_id,
        status,
        str(len(items)),
        str(stats["tools"]),
        str(stats["errors"]),
        str((last or {}).get("id") or ""),
        str((last or {}).get("at") or ""),
        str((last or {}).get("status") or ""),
        str((last or {}).get("text") or "")[-80:],
        str(usage["total_tokens"]),
        ",".join(f"{item.get('request_id')}:{item.get('status')}"
                 for item in items if item["type"] == "permission"),
    ])
    return {
        "session": session_id,
        "status": status,
        "active": active,
        "generated_at": generated_at or utc_now(),
        "items": items,
        "stats": stats,
        "usage": usage,
        "signature": signature,
    }


def _classify(path: Path) -> str:
    suffix = path.suffix.lower()
    if suffix in IMAGE_SUFFIXES:
        return "image"
    if suffix in VIDEO_SUFFIXES:
        return "video"
    if suffix in AUDIO_SUFFIXES:
        return "audio"
    if suffix in PDF_SUFFIXES:
        return "pdf"
    if suffix in DOCUMENT_SUFFIXES:
        return "document"
    return "other"


def index_workspace(root: str | Path, *, limit: int = MAX_WORKSPACE_FILES,
                    max_depth: int = MAX_WORKSPACE_DEPTH) -> dict:
    """Index files under a session workspace for the media/documents rail.

    Symlinks are never followed (the artifact browser refuses them too) and the
    walk is bounded by ``limit`` and ``max_depth``.  No file content is read.
    """
    root = Path(root)
    media: list[dict] = []
    documents: list[dict] = []
    other: list[dict] = []
    truncated = False
    if not root.is_dir():
        return {"media": media, "documents": documents, "other": other,
                "truncated": False, "count": 0}

    def walk(folder: Path, depth: int) -> None:
        nonlocal truncated
        if depth > max_depth or truncated:
            return
        try:
            children = sorted(folder.iterdir(), key=lambda p: (not p.is_dir(), p.name.lower()))
        except OSError:
            return
        for child in children:
            if len(media) + len(documents) + len(other) >= limit:
                truncated = True
                return
            if child.is_symlink() or child.name.startswith("."):
                continue
            try:
                if child.is_dir():
                    walk(child, depth + 1)
                    continue
                stat = child.stat()
            except OSError:
                continue
            rel = child.relative_to(root).as_posix()
            kind = _classify(child)
            entry = {
                "name": child.name,
                "path": rel,
                "size": stat.st_size,
                "mtime": datetime.fromtimestamp(stat.st_mtime, timezone.utc).isoformat(timespec="seconds"),
                "kind": kind,
                "mime": mimetypes.guess_type(child.name)[0] or "application/octet-stream",
                "text": kind in {"document", "other"} and child.suffix.lower() in DOCUMENT_SUFFIXES,
                "language": language_for(child.name),
            }
            if kind in {"image", "video", "audio"}:
                media.append(entry)
            elif kind in {"document", "pdf"}:
                documents.append(entry)
            else:
                other.append(entry)

    walk(root, 0)
    media.sort(key=lambda item: (-item["size"], item["name"].lower()))
    documents.sort(key=lambda item: item["path"].lower())
    return {
        "media": media,
        "documents": documents,
        "other": other,
        "truncated": truncated,
        "count": len(media) + len(documents) + len(other),
    }
