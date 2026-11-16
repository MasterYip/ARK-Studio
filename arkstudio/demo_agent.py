"""Tiny line-oriented local agent used by the built-in ``Echo (local demo)`` platform.

This is not a model and makes no network calls.  It exists so the task-session
lifecycle (start, streaming stdout/stderr, follow-up input, artifacts, stop) can
be exercised on a machine that has no Claude Code/Codex/DSH installation.

Structured session events
-------------------------
Lines that start with ``[[ark-event]]`` carry one JSON object.  The ARK session
workspace turns them into messages, tool cards and errors.  The demo emits a
small tool call/result pair at startup so the structured UI can be inspected
without a real model; the rest of the output remains plain text.

Commands understood on stdin::

    quit | exit              end the session
    write <name> <text>      write <text> into artifacts/<name>
    request command          ask ARK to approve a demo shell command
    request edit [text]      ask ARK to approve a demo file edit
    anything else            echoed to stdout

Approval requests use the documented ``ark-permission-v1`` protocol: this demo
prints a ``permission_request`` event and waits for the matching
``permission_response`` JSON line on stdin before performing the action.
"""
from __future__ import annotations

import argparse
import json
import os
import sys
from pathlib import Path

EVENT_PREFIX = "[[ark-event]]"


def emit(**event) -> None:
    print(EVENT_PREFIX + " " + json.dumps(event, ensure_ascii=False), flush=True)


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(prog="arkstudio.demo_agent")
    parser.add_argument("--prompt", default="", help="initial instruction")
    args = parser.parse_args(argv)

    project = os.environ.get("ARK_PROJECT_PATH") or ""
    emit(
        type="assistant",
        text=(
            "# ARK echo agent\n\n"
            "This is a **local demo agent**, not a model. It reads your prompt, "
            "can write files into `artifacts/`, and keeps the session lifecycle testable "
            "without an API key.\n\n"
            "| Input | Behaviour |\n|---|---|\n"
            "| `write <name> <text>` | write an artifact in the private workspace |\n"
            "| `quit` | end this session |\n"
            "| anything else | echoed back as assistant output |"
        ),
    )
    emit(type="tool_call", id="demo-context", name="read_context",
         input={"path": "ark-context.json", "mode": "read-only"})
    units = 0
    bytes_read = 0
    try:
        raw = Path("ark-context.json").read_bytes()
        bytes_read = len(raw)
        parsed = json.loads(raw.decode("utf-8"))
        units = len(parsed.get("units") or [])
    except (OSError, ValueError):
        pass
    emit(type="tool_result", id="demo-context", status="ok",
         output={"project": project or "(none)", "units": units, "bytes": bytes_read})

    print("ARK echo agent ready — local demo, no model call.", flush=True)
    print(f"project (read-only): {project or '(none)'}", flush=True)
    print(f"workspace: {os.getcwd()}", flush=True)
    print("read-only token present: "
          f"{'yes' if os.environ.get('ARK_READ_TOKEN') else 'no'}", flush=True)
    if args.prompt:
        print(f"instruction: {args.prompt}", flush=True)
    print("Commands: write <name> <text> · request command · request edit · quit", flush=True)

    approval_counter = 0
    pending_approvals: dict[str, dict] = {}

    def handle_permission_response(frame: dict) -> None:
        request_id = str(frame.get("request_id") or frame.get("id") or "")
        action = pending_approvals.pop(request_id, None)
        if action is None:
            print(f"approval response for unknown request {request_id}", flush=True)
            return
        allowed = str(frame.get("decision") or "").lower() == "allow"
        scope = str(frame.get("scope") or "once")
        if not allowed:
            emit(type="tool_result", id=request_id, name=action["tool"], status="error",
                 output={"decision": "deny", "message": frame.get("message") or ""})
            print(f"denied {action['label']} ({scope})", flush=True)
            return
        if action["kind"] == "file_edit":
            target = Path("artifacts") / action["name"]
            target.parent.mkdir(parents=True, exist_ok=True)
            target.write_text(action.get("text") or "approved demo content\n", encoding="utf-8")
            emit(type="tool_call", id=request_id, name=action["tool"],
                 input={"path": f"artifacts/{action['name']}", "approval": "allow"})
            emit(type="tool_result", id=request_id, name=action["tool"], status="ok",
                 output={"path": f"artifacts/{action['name']}", "scope": scope})
            print(f"approved file edit -> artifacts/{action['name']} ({scope})", flush=True)
        else:
            emit(type="tool_call", id=request_id, name=action["tool"],
                 input={"command": action.get("command") or "", "approval": "allow"})
            emit(type="tool_result", id=request_id, name=action["tool"], status="ok",
                 output={"exit_code": 0, "scope": scope})
            print(f"approved command: {action.get('command') or ''} ({scope})", flush=True)

    for raw in sys.stdin:
        line = raw.rstrip("\r\n")
        stripped = line.strip()
        if not stripped:
            continue
        if stripped.startswith("{"):
            try:
                frame = json.loads(stripped)
            except ValueError:
                frame = None
            if isinstance(frame, dict) and frame.get("type") == "permission_response":
                handle_permission_response(frame)
                continue
        low = stripped.lower()
        if low in {"quit", "exit"}:
            print("bye", flush=True)
            return 0
        if low == "request command" or low == "request make command":
            approval_counter += 1
            request_id = f"demo-approval-{approval_counter}"
            pending_approvals[request_id] = {
                "kind": "command", "tool": "bash", "label": "demo command",
                "command": "python -c \"print('approved by ARK')\"",
            }
            emit(type="permission_request", id=request_id, kind="command", tool="bash",
                 command="python -c \"print('approved by ARK')\"",
                 description="Run a harmless demo command")
            print(f"waiting for approval on {request_id}", flush=True)
            continue
        if low.startswith("request edit") or low.startswith("request write"):
            approval_counter += 1
            request_id = f"demo-approval-{approval_counter}"
            rest = (stripped[len("request edit"):] if low.startswith("request edit")
                    else stripped[len("request write"):]).strip()
            action = {"kind": "file_edit", "tool": "write_file", "label": "demo file edit",
                      "name": "approved-demo.txt", "text": rest or "approved demo content\n"}
            pending_approvals[request_id] = action
            emit(type="permission_request", id=request_id, kind="file_edit", tool="write_file",
                 path="artifacts/approved-demo.txt",
                 description="Write an approved demo artifact")
            print(f"waiting for approval on {request_id}", flush=True)
            continue
        if low.startswith("write "):
            rest = stripped[6:].strip()
            if not rest:
                print("usage: write <name> <text>", flush=True)
                continue
            name, _, text = rest.partition(" ")
            name = Path(name).name or "note.txt"
            target = Path("artifacts") / name
            target.parent.mkdir(parents=True, exist_ok=True)
            target.write_text(text or "(empty)\n", encoding="utf-8")
            emit(type="tool_call", id=f"write-{name}", name="write_file",
                 input={"path": f"artifacts/{name}", "chars": len(text or "")})
            emit(type="tool_result", id=f"write-{name}", status="ok",
                 output={"path": f"artifacts/{name}", "size": target.stat().st_size})
            print(f"wrote artifacts/{name}", flush=True)
            continue
        emit(type="assistant", text=f"Echo from the local demo agent:\n\n> {line}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
