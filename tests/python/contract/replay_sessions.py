"""Replay steps for agent sessions (sessions.py, session_transcript.py).

Fixture (``seed_sessions``, called by fixture.py): three finished sessions
with hand-written logs and workspaces that exercise the transcript parser
(Claude stream-json, Codex JSONL and stderr transcript, OpenAI choices,
``[[ark-event]]`` lines, permission requests/resolutions, ANSI, reasoning
markers), the artifact browser (text, BOM, binary, 300 KB, nested, hidden)
and the privacy rules (private / shared / another owner).

Live sessions run two deterministic agents only (never a model):

* ``echo`` -- arkstudio/demo_agent.py under Python, ``ark-server
  demo-agent`` under Go (byte-identical output; N16 masks the program path);
* ``custom`` -- ARK_TASK_CUSTOM_COMMAND = this replay's ``fake_agent.py``
  run by the same Python interpreter for both servers: prints the session
  environment it was given (no secret values), structured lines, stderr, and
  answers stdin lines; ``exit3`` in the prompt makes it exit with code 3.

``claude`` (a .cmd shim on PATH) shows the start failure of a bare command
name on Windows (CreateProcess does not apply PATHEXT) in both servers.

WAIT steps (``WAIT_SESSION``) poll both servers until the session @var@ has
a log line containing a text or has finished; they are not compared.
``X-Replay-Save: name=dotted.path`` stores a value of the Python reply as
@name@ for later steps.
"""
from __future__ import annotations

import json
import os
import sys
import time
from pathlib import Path

WAIT_SESSION = "__wait_session__"
HOOK_RELEASE = "__session_release__"

FAKE_AGENT = r'''
import json, os, sys, time
prompt = " ".join(sys.argv[1:])
def out(text):
    print(text, flush=True)
def err(text):
    print(text, file=sys.stderr, flush=True)
env = os.environ
out(json.dumps({"type": "system", "subtype": "init", "session_id": "fake-native-0001"}))
out("fake agent prompt: " + prompt.splitlines()[0][:80] if prompt else "fake agent prompt: (none)")
for key in ("ARK_TASK_ACCESS_MODE", "ARK_TASK_READ_ONLY", "ARK_TASK_PLAN_MODE", "ARK_TASK_APPROVALS",
            "ARK_TASK_APPROVAL_MODE", "ARK_TASK_FULL_ACCESS", "ARK_TASK_MODEL", "ARK_TASK_EFFORT",
            "ARK_TASK_APPROVAL_PROTOCOL", "PYTHONUNBUFFERED", "ARK_TASK_WORKTREE_BRANCH"):
    out(f"env {key}={env.get(key, '<unset>')}")
for key in ("ARK_AGENT_TOKEN", "ARK_READ_TOKEN", "DEEPSEEK_API_KEY", "ARK_TASK_SESSION", "ARK_TASK_CONTEXT"):
    out(f"env {key} present: {'yes' if env.get(key) else 'no'}")
out("cwd is workspace: " + str(os.path.basename(os.getcwd()) in ("workspace", "sub", "deeper")))
out(json.dumps({"type": "assistant", "message": {"content": [{"type": "text", "text": "Plan **bold**"},
    {"type": "tool_use", "id": "tu1", "name": "Read", "input": {"file_path": "a.txt"}}]}}))
out(json.dumps({"type": "user", "message": {"content": [{"type": "tool_result", "tool_use_id": "tu1",
    "content": [{"type": "text", "text": "file body"}]}]}}))
out("[[ark-event]] " + json.dumps({"type": "tool_call", "id": "t9", "name": "bash", "input": {"command": "ls"}}))
out("[[ark-event]] " + json.dumps({"type": "tool_result", "id": "t9", "status": "ok", "output": "a\nb"}))
out("\x1b[1mplain ansi line\x1b[0m")
err("thinking:")
err("deliberating 思考")
err("dsh: status: done")
if "exit3" in prompt:
    err("fatal: something broke")
    sys.exit(3)
if "approve" in prompt:
    out("[[ark-event]] " + json.dumps({"type": "permission_request", "id": "fake-req-1", "kind": "command",
        "tool": "bash", "command": "make test", "description": "run tests"}))
    out(json.dumps({"type": "control_request", "request_id": "ctl-1", "request": {"subtype": "can_use_tool",
        "tool_name": "Write", "input": {"file_path": "x.txt", "content": "hi"}}}))
out(json.dumps({"type": "result", "result": "turn done", "usage": {"input_tokens": 12, "output_tokens": 5}}))
out("ready for input")
for raw in sys.stdin:
    line = raw.rstrip("\r\n")
    if line.strip() in ("quit", "exit"):
        out("bye")
        break
    if line.startswith("{"):
        try:
            frame = json.loads(line)
        except ValueError:
            frame = {}
        out("frame " + json.dumps(frame, sort_keys=True))
        continue
    if line.strip() == "resolve":
        out("[[ark-event]] " + json.dumps({"type": "permission_resolved", "request_id": "fake-req-1",
                                           "message": "resolved by adapter"}))
        continue
    out("echo: " + line)
'''

T0 = "2026-01-02T03:04:05+00:00"


def _write(path: Path, text: str) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(text, encoding="utf-8")


def _log(lines) -> str:
    out = []
    for n, item in enumerate(lines):
        if isinstance(item, str):
            item = {"line": item}
        entry = {"t": item.pop("t", f"2026-01-02T03:04:{n % 60:02d}+00:00"), **item}
        out.append(json.dumps(entry, ensure_ascii=False))
    out.append("not json at all")
    out.append("[1, 2]")
    return "\n".join(out) + "\n"


def seed_sessions(sessions_root: Path, summary: dict, owner: str, other: str) -> dict:
    """Three finished sessions with crafted logs/workspaces (see module docstring)."""
    project = {"id": summary["id"], "name": summary["name"], "path": summary["path"]}
    ev = "[[ark-event]] "
    claude_out = [
        json.dumps({"type": "system", "subtype": "init", "session_id": "claude-native-1234"}),
        json.dumps({"type": "assistant", "message": {"content": [
            {"type": "text", "text": "I will **read** files 中文"},
            {"type": "tool_use", "id": "toolu_1", "name": "Read", "input": {"file_path": "README.md"}},
            {"type": "thinking", "thinking": "hmm"}]}}),
        json.dumps({"type": "user", "message": {"content": [
            {"type": "tool_result", "tool_use_id": "toolu_1", "content": [{"type": "text", "text": "readme body"}],
             "is_error": False}]}}),
        json.dumps({"type": "result", "result": "Done.", "usage": {"input_tokens": 100, "output_tokens": 40,
                                                                  "cache_read_input_tokens": 7}}),
        ev + json.dumps({"type": "permission_request", "id": "req-1", "kind": "command", "tool": "bash",
                         "command": "pytest -q", "input": {"command": "pytest -q"}}),
        ev + json.dumps({"type": "permission_request", "request_id": "req-2", "tool": "write_file",
                         "path": "src/x.py", "description": "edit"}),
        ev + json.dumps({"type": "permission_resolved", "request_id": "req-2", "message": "ok"}),
        json.dumps({"type": "control_request", "request_id": "ctl-9", "request": {
            "subtype": "can_use_tool", "tool_name": "Edit", "input": {"file_path": "a.py", "old": "x"}}}),
        json.dumps({"type": "approval_request", "approvalId": 5, "action": "fetch", "target": "https://x"}),
        ev + json.dumps({"type": "reasoning", "text": "thinking aloud"}),
        ev + json.dumps({"type": "notice", "text": "heads up", "level": "warn"}),
        ev + json.dumps({"type": "error", "message": "tool failed"}),
        ev + json.dumps({"type": "usage", "prompt_tokens": 3, "completion_tokens": "4"}),
        ev + json.dumps({"type": "user", "text": "from user"}),
        ev + json.dumps({"role": "assistant", "text": "markdown **x**", "markdown": False}),
        ev + json.dumps({"type": "weird"}),
        ev + "not json",
        ev,
        json.dumps({"choices": [{"message": {"role": "assistant", "content": "chat answer",
                                             "tool_calls": [{"id": "c1", "function": {"name": "f", "arguments": "{}"}}]}}],
                    "usage": {"total_tokens": 9}}),
        json.dumps({"type": "item.completed", "item": {"type": "command_execution", "id": "i1",
                                                       "command": "ls", "aggregated_output": "x", "exit_code": 1}}),
        json.dumps({"type": "item.started", "item": {"type": "mcp_tool_call", "id": "i2", "server": "s",
                                                     "tool": "t", "arguments": {"a": 1}}}),
        json.dumps({"type": "item.completed", "item": {"type": "file_change", "id": "i3", "changes": ["a"]}}),
        json.dumps({"type": "item.completed", "item": {"type": "agent_message", "text": "codex says"}}),
        json.dumps({"type": "tool_result", "name": "Read", "output": "late", "status": "failed"}),
        json.dumps({"role": "user", "content": [{"type": "input_text", "text": "typed"}]}),
        json.dumps({"tool_calls": [{"id": "z", "name": "g", "arguments": {"q": 1}}]}),
        json.dumps({"type": "message", "content": "plain message", "role": "assistant"}),
        json.dumps({"type": "status", "subtype": "compacting"}),
        "\x1b[31mred text\x1b[0m line",
        "second plain line",
        {"line": "› typed input", "kind": "input"},
        "  ",
        {"line": "x" * 50, "t": "2026-01-02T03:04:05+00:00"},
        {"line": "line with \u2028 separator"},
    ]
    claude_err = [
        "Reasoning:",
        "first thought",
        "dsh: tool: run",
        "warning: real stderr",
        {"line": "same second stderr", "t": "2026-01-02T03:04:05+00:00"},
    ]
    codex_err = ["OpenAI Codex v0.1", "--------", "workdir: x", "session id: 0199-codex-abcdef",
                 "user", "the prompt", "codex", "answer line 1", "answer line 2", "thinking: deep",
                 "ERROR: boom", "tokens used", "1,234", "answer line 1 again"]
    sessions = {
        "s_20260102030405claude1": dict(
            platform="claude", platform_label="Claude Code", actor=owner, shared=False, out=claude_out,
            err=claude_err, status="stopped", title="Transcript 会话", prompt="Fix the tests",
            approvals=[{"id": "req-1", "kind": "command", "tool": "bash", "command": "pytest -q",
                        "status": "allowed", "decision": "allow", "scope": "always", "auto": False,
                        "message": "ok", "mode": "ask", "requested_at": T0, "decided_at": T0,
                        "decided_by": "human:ui"},
                       {"id": "req-old", "kind": "file_edit", "status": "pending", "requested_at": T0}],
            approval_rules=[{"kind": "command", "tool": "bash", "created_at": T0, "created_by": owner}],
            steer_queue=[{"id": "st_a", "text": "first", "status": "queued", "channel": "next_turn",
                          "created_at": T0},
                         {"id": "st_b", "text": "second", "status": "delivered", "channel": "stdin",
                          "created_at": T0, "delivered_at": T0},
                         {"id": "st_c", "text": "third", "status": "queued", "created_at": T0}],
            permission_history=[{"mode": "ask", "at": T0, "turn": 1, "source": "start"},
                                {"mode": "bogus", "at": T0}, "junk"],
            linked_units=["T-1"], native_session_id="claude-native-1234", access_mode="ask", turn=2),
        "s_20260102030405codex01": dict(
            platform="codex", platform_label="Codex", actor=other, shared=True, out=[], err=codex_err,
            status="errored", error="process exited with code 1", title="Shared codex",
            prompt="Review", access_mode="read_only"),
        "s_20260102030405hidden1": dict(
            platform="echo", platform_label="Echo (local demo)", actor=other, shared=False,
            out=["private"], err=[], status="stopped", title="Private to another user",
            prompt="secret", access_mode="plan"),
    }
    for sid, spec in sessions.items():
        folder = sessions_root / sid
        meta = {"id": sid, "title": spec["title"], "platform": spec["platform"],
                "platform_label": spec["platform_label"], "model": "", "effort": "",
                "access_mode": spec["access_mode"], "status": spec["status"], "error": spec.get("error", ""),
                "created_at": T0, "started_at": T0, "last_activity_at": T0, "ended_at": T0,
                "exit_code": 1 if spec["status"] == "errored" else 0, "pid": 4242, "cwd": ".",
                "project": project, "actor": spec["actor"], "shared": spec["shared"], "prompt": spec["prompt"],
                "read_token": f"rt_{sid}_token", "followup": False,
                "counts": {"stdout": len(spec["out"]), "stderr": len(spec["err"]), "bytes": 99},
                "api": {"base": "/api/task-agent/v1"}}
        for key in ("approvals", "approval_rules", "steer_queue", "permission_history", "linked_units",
                    "native_session_id", "turn"):
            if key in spec:
                meta[key] = spec[key]
        _write(folder / "meta.json", json.dumps(meta, ensure_ascii=False, indent=2) + "\n")
        _write(folder / "stdout.jsonl", _log([dict(x) if isinstance(x, dict) else x for x in spec["out"]]))
        _write(folder / "stderr.jsonl", _log([dict(x) if isinstance(x, dict) else x for x in spec["err"]]))
    ws = sessions_root / "s_20260102030405claude1" / "workspace"
    _write(ws / "ark-context.json", json.dumps({"format": "ark-task-agent-context"}))
    _write(ws / "artifacts" / "notes.md", "# Notes\n\nline 2\n")
    _write(ws / "artifacts" / "bom.txt", "\ufeffwith bom\r\nsecond\n")
    _write(ws / "artifacts" / "deep" / "deeper" / "x.py", "print('x')\n")
    _write(ws / "artifacts" / ".hidden.txt", "hidden")
    _write(ws / "artifacts" / "Upper.TXT", "upper")
    (ws / "artifacts" / "blob.bin").write_bytes(b"\x00\x01binary")
    (ws / "artifacts" / "pic.png").write_bytes(b"\x89PNG\r\n\x1a\n" + b"0" * 64)
    (ws / "artifacts" / "clip.mp4").write_bytes(b"0" * 10)
    (ws / "artifacts" / "big.log").write_bytes(b"y" * (300 * 1024))
    _write(ws / "artifacts" / "semi;name 空格.txt", "q")
    _write(ws / "artifacts" / "doc.pdf", "%PDF-1.4")
    _write(ws / "artifacts" / "data.weird", "?")
    _write(ws / "sub" / "a.csv", "a,b\n")
    return {"claude": "s_20260102030405claude1", "codex": "s_20260102030405codex01",
            "hidden": "s_20260102030405hidden1"}


def session_env(work: Path) -> dict:
    """The custom preset (fake_agent.py under this interpreter) and preset model lists."""
    bin_dir = work / "agent-bin"
    bin_dir.mkdir(parents=True, exist_ok=True)
    agent = bin_dir / "fake_agent.py"
    agent.write_text(FAKE_AGENT, encoding="utf-8")
    return {"ARK_TASK_CUSTOM_COMMAND": f"{sys.executable} -u {agent} {{prompt}}",
            "ARK_TASK_CUSTOM_APPROVALS": "1", "ARK_TASK_CUSTOM_LABEL": "Fake agent",
            "ARK_TASK_CUSTOM_MODELS": "fake-1,fake-2", "ARK_TASK_CUSTOM_EFFORTS": "low,high",
            "ARK_TASK_ECHO_EFFORTS": "", "PYTHONIOENCODING": "utf-8"}


def wait_session(clients, sid: str, until: str) -> None:
    """WAIT_SESSION: until "done" (not active) or "text:<substring>" in stdout."""
    for client in clients:
        deadline = time.monotonic() + 30
        while time.monotonic() < deadline:
            if until == "done":
                reply = client.get(f"/api/agent-sessions/{sid}")
                try:
                    status = reply.json().get("session", {}).get("raw_status")
                except ValueError:
                    status = None
                if status not in ("starting", "running", "stopping"):
                    break
            else:
                needle = until.split(":", 1)[1]
                reply = client.get(f"/api/agent-sessions/{sid}/logs?stream=stdout&limit=500")
                try:
                    lines = [e.get("line", "") for e in reply.json().get("entries", [])]
                except ValueError:
                    lines = []
                if any(needle in line for line in lines):
                    break
            time.sleep(0.1)
        time.sleep(0.3)  # let the reader finish the burst that carried the line


def learn(vars_: dict, spec: str, body: bytes) -> None:
    """X-Replay-Save: "name=dotted.path" (e.g. s1=session.id)."""
    try:
        data = json.loads(body)
    except ValueError:
        return
    for item in spec.split(","):
        name, _, path = item.partition("=")
        value = data
        for part in path.split("."):
            value = value.get(part) if isinstance(value, dict) else None
        if value is not None:
            vars_[name] = value


def session_steps(fixture: dict) -> list[tuple]:
    m = {"X-Replay-Module": "agent-sessions"}
    first_uid = "T-0001"
    if fixture.get("data_dir"):
        import replay_writes
        first_uid = replay_writes.fixture_refs(Path(fixture["data_dir"]))["live"][0]
    seeded = fixture.get("seeded_sessions") or {}
    other = {**m, "X-Replay-As": "1"}
    read = {**m, "X-ARK-Read-Token": fixture["tokens"]["read_token"]}
    steps: list[tuple] = []
    R = {
        "list": "exact:/api/agent-sessions", "groups": "exact:/api/agent-sessions/groups",
        "presets": "exact:/api/agent-sessions/presets", "one": r"regex:^/api/agent-sessions/([^/]+)$",
        "logs": r"regex:^/api/agent-sessions/([^/]+)/logs$", "tr": r"regex:^/api/agent-sessions/([^/]+)/transcript$",
        "appr": r"regex:^/api/agent-sessions/([^/]+)/approvals$", "media": r"regex:^/api/agent-sessions/([^/]+)/media$",
        "arts": r"regex:^/api/agent-sessions/([^/]+)/artifacts$",
        "read": r"regex:^/api/agent-sessions/([^/]+)/artifacts/read$",
        "dl": r"regex:^/api/agent-sessions/([^/]+)/artifacts/download$",
        "decide": r"regex:^/api/agent-sessions/([^/]+)/approvals/([^/]+)$",
        "archive": r"regex:^/api/agent-sessions/([^/]+)/archive$",
        "validate": r"regex:^/api/agent-sessions/([^/]+)/context/validate$",
        "continue": r"regex:^/api/agent-sessions/([^/]+)/continue$",
        "fork": r"regex:^/api/agent-sessions/([^/]+)/fork$", "input": r"regex:^/api/agent-sessions/([^/]+)/input$",
        "relaunch": r"regex:^/api/agent-sessions/([^/]+)/relaunch$",
        "steer": r"regex:^/api/agent-sessions/([^/]+)/steer$",
        "reorder": r"regex:^/api/agent-sessions/([^/]+)/steer/reorder$",
        "steer1": r"regex:^/api/agent-sessions/([^/]+)/steer/([^/]+)$",
        "stop": r"regex:^/api/agent-sessions/([^/]+)/stop$", "task": "exact:/api/task-agent/v1/session",
    }

    def add(key, method, path, body=None, headers=None, save=""):
        h = dict(headers or m)
        if save:
            h["X-Replay-Save"] = save
        steps.append((R[key], method, path, body, h))

    def wait(var, until):
        steps.append((WAIT_SESSION, var, until, None, {"X-Replay-Module": "agent-sessions"}))

    base = "/api/agent-sessions"
    for key, path in (("list", base), ("groups", base + "/groups"), ("presets", base + "/presets")):
        add(key, "GET", path)
        add(key, "GET", path, headers=other)
    add("list", "HEAD", base)
    add("task", "GET", "/api/task-agent/v1/session", headers=read)
    add("task", "GET", "/api/task-agent/v1/session", headers={**m, "X-ARK-Read-Token": "nope"})
    claude, codex, hidden = seeded.get("claude", "x"), seeded.get("codex", "x"), seeded.get("hidden", "x")
    for sid in (claude, codex, hidden, "s_20260101000000abc123", "bad-id", "s_00000000000000nope00"):
        add("one", "GET", f"{base}/{sid}")
        add("one", "GET", f"{base}/{sid}", headers=other)
        add("tr", "GET", f"{base}/{sid}/transcript")
        add("appr", "GET", f"{base}/{sid}/approvals")
        add("media", "GET", f"{base}/{sid}/media")
    add("tr", "GET", f"{base}/{codex}/transcript", headers=other)
    for query in ("", "?stream=stderr", "?stream=bad", "?offset=3&limit=2", "?offset=999", "?limit=0",
                  "?offset=-5&limit=x", "?limit=9999"):
        add("logs", "GET", f"{base}/{claude}/logs{query}")
    for query in ("", "?path=artifacts", "?path=artifacts/deep", "?path=../x", "?path=/abs", "?path=C:/x",
                  "?path=nope", "?path=artifacts/notes.md", "?path=./artifacts/./deep/", "?path=artifacts%5Cdeep"):
        add("arts", "GET", f"{base}/{claude}/artifacts{query}")
    for target in ("artifacts/notes.md", "artifacts/bom.txt", "artifacts/blob.bin", "artifacts/big.log",
                   "artifacts/nope", "artifacts", "", "../meta.json", "artifacts/deep/deeper/x.py",
                   "sub/a.csv", "artifacts/data.weird", "ark-context.json"):
        add("read", "GET", f"{base}/{claude}/artifacts/read?path={target}")
    for target in ("artifacts/pic.png", "artifacts/notes.md", "artifacts/big.log",
                   "artifacts/nope", "artifacts", "artifacts/data.weird"):
        add("dl", "GET", f"{base}/{claude}/artifacts/download?path={target}")
    add("dl", "HEAD", f"{base}/{claude}/artifacts/download?path=artifacts/pic.png")
    # Seeded steer queue edits and privacy on writes.
    add("steer1", "PATCH", f"{base}/{claude}/steer/st_a", {"text": "first edited"})
    add("steer1", "PATCH", f"{base}/{claude}/steer/st_b", {"text": "x"})
    add("steer1", "PATCH", f"{base}/{claude}/steer/st_nope", {"text": "x"})
    add("steer1", "PATCH", f"{base}/{claude}/steer/st_a", {"text": ""})
    add("steer1", "PATCH", f"{base}/{claude}/steer/st_a", {"text": "y" * 4001})
    add("reorder", "POST", f"{base}/{claude}/steer/reorder", {"order": ["st_c", "st_a"]})
    add("reorder", "POST", f"{base}/{claude}/steer/reorder", {"order": "st_a,st_c"})
    add("reorder", "POST", f"{base}/{claude}/steer/reorder", {"order": ["st_a"]})
    add("reorder", "POST", f"{base}/{claude}/steer/reorder", {"order": ["st_a", "st_a"]})
    add("reorder", "POST", f"{base}/{claude}/steer/reorder", {"order": 5})
    add("steer1", "DELETE", f"{base}/{claude}/steer/st_b")
    add("steer1", "POST", f"{base}/{claude}/steer/st_nope", {})
    add("steer1", "PATCH", f"{base}/{codex}/steer/st_a", {"text": "x"}, headers=other)
    add("steer1", "PATCH", f"{base}/{claude}/steer/st_a", {"text": "x"}, headers=other)
    add("decide", "POST", f"{base}/{claude}/approvals/req-old", {"decision": "allow"})
    add("decide", "POST", f"{base}/{claude}/approvals/req-1", {"decision": "allow"})
    add("decide", "POST", f"{base}/{claude}/approvals/zzz", {"decision": "deny"})
    add("decide", "POST", f"{base}/{claude}/approvals/req-old", {"decision": "maybe"})
    add("decide", "POST", f"{base}/{claude}/approvals/req-old", {"decision": "allow"}, headers=read)
    add("steer", "POST", f"{base}/{claude}/steer", {"text": "nudge"})
    add("steer", "POST", f"{base}/{claude}/steer", {})
    add("input", "POST", f"{base}/{claude}/input", {"text": "hi"})
    add("input", "POST", f"{base}/{claude}/input", {})
    add("validate", "POST", f"{base}/{claude}/context/validate", {"context": [
        {"kind": "file", "path": "artifacts/notes.md"}, {"kind": "media", "path": "artifacts/pic.png"},
        {"kind": "text", "text": "snippet line\nsecond", "label": ""}, {"kind": "text", "text": "snippet line\nsecond"},
        {"type": "unit", "uid": first_uid}]})
    for bad in ([{"kind": "file", "path": "../x"}], [{"kind": "unit", "uid": "!"}], [{"kind": "unit", "uid": "NO-SUCH"}],
                [{"kind": "file", "path": "artifacts/blob.bin"}], [{"kind": "file", "path": "artifacts/big.log"}],
                [{"kind": "file", "path": "nope"}], [{"kind": "zzz"}], ["x"], {"a": 1}, [{}] * 13,
                [{"kind": "text", "text": "  "}], [{"kind": "file"}], [{"kind": "media", "path": "artifacts/nope"}]):
        add("validate", "POST", f"{base}/{claude}/context/validate", {"context": bad})
    add("validate", "POST", f"{base}/{claude}/context/validate", {"attachments": []})
    # The codex session is shared and owned by fixture user 1: others may read
    # it but not change it (403 SESSION_NOT_OWNER); the hidden one is private.
    add("continue", "POST", f"{base}/{codex}/continue", {"text": "again"})
    add("archive", "POST", f"{base}/{codex}/archive", {})
    add("one", "DELETE", f"{base}/{hidden}")
    add("steer1", "DELETE", f"{base}/{codex}/steer/st_x")
    add("stop", "POST", f"{base}/{codex}/stop", {}, headers=other)
    add("continue", "POST", f"{base}/{codex}/continue", {"text": "again", "platform": "nope"}, headers=other)
    add("one", "DELETE", f"{base}/{codex}", headers=other)

    # Start errors.
    for body in ({}, {"platform": "nope"}, {"platform": "claude"}, {"platform": "echo", "access_mode": "root"},
                 {"platform": "custom", "access_mode": "ask", "effort": "huge"},
                 {"platform": "echo", "effort": "low"}, {"platform": "echo", "cwd": "../x"},
                 {"platform": "echo", "cwd": "C:/x"}, {"platform": "echo", "cwd": "a", "worktree": "x"},
                 {"platform": "echo", "worktree": "no-such-tree"}, {"platform": "echo", "parent_id": "s_00000000000000nope00"},
                 {"platform": "shell", "access_mode": "ask"}, {"platform": "dsh"}):
        add("list", "POST", base, body)
    add("list", "POST", base, {"platform": "claude", "prompt": "hello there", "title": "fails"}, save="sfail=session.id")
    add("list", "POST", base, {"platform": "echo", "prompt": "x"}, {**m, "Origin": "http://evil.example"})
    add("list", "POST", base, {"platform": "echo", "prompt": "x"}, read, save="s0=session.id")
    wait("s0", "text:Commands:")

    # Echo: followup, approvals through the demo protocol, steer, input.
    add("list", "POST", base, {"platform": "echo", "prompt": "say hi 你好", "title": "Echo run",
                               "access_mode": "ask", "task": "T-0001", "cwd": "sub/deeper"},
        save="s1=session.id")
    wait("s1", "text:Commands:")
    add("one", "GET", f"{base}/@s1@")
    add("list", "GET", base)
    add("groups", "GET", base + "/groups")
    add("input", "POST", f"{base}/@s1@/input", {"text": "write a.txt hello file", "context": [
        {"kind": "text", "text": "ctx"}]})
    wait("s1", "text:wrote artifacts/a.txt")
    add("steer", "POST", f"{base}/@s1@/steer", {"message": "request edit approved words"}, save="st1=steer.id")
    wait("s1", "text:waiting for approval on demo-approval-1")
    add("appr", "GET", f"{base}/@s1@/approvals")
    add("decide", "POST", f"{base}/@s1@/approvals/demo-approval-1", {"decision": "always", "message": "go"})
    wait("s1", "text:approved file edit")
    add("steer1", "PATCH", f"{base}/@s1@/steer/@st1@", {"text": "edit after delivery"})
    add("input", "POST", f"{base}/@s1@/input", {"text": "request command"})
    wait("s1", "text:waiting for approval on demo-approval-2")
    add("decide", "POST", f"{base}/@s1@/approvals/demo-approval-2", {"action": "deny"})
    wait("s1", "text:denied demo command")
    add("input", "POST", f"{base}/@s1@/input", {"text": "plain words"})
    wait("s1", "text:plain words")
    add("tr", "GET", f"{base}/@s1@/transcript")
    add("media", "GET", f"{base}/@s1@/media")
    add("arts", "GET", f"{base}/@s1@/artifacts?path=artifacts")
    add("read", "GET", f"{base}/@s1@/artifacts/read?path=artifacts/approved-demo.txt")
    add("continue", "POST", f"{base}/@s1@/continue", {"text": "more", "platform": "custom"})
    add("continue", "POST", f"{base}/@s1@/continue", {"text": "via continue while running"})
    wait("s1", "text:via continue while running")
    add("archive", "POST", f"{base}/@s1@/archive", {})
    add("one", "DELETE", f"{base}/@s1@")
    add("relaunch", "POST", f"{base}/@s1@/relaunch", {})
    add("fork", "POST", f"{base}/@s1@/fork", {"prompt": "side question", "access_mode": "read_only"},
        save="s2=session.id")
    wait("s2", "text:Commands:")
    add("one", "GET", f"{base}/@s2@")
    add("groups", "GET", base + "/groups")
    for sid in ("s2", "s1"):
        add("stop", "POST", f"{base}/@{sid}@/stop", {})
        wait(sid, "done")
        add("stop", "POST", f"{base}/@{sid}@/stop", {})
    add("decide", "POST", f"{base}/@s1@/approvals/demo-approval-2", {"decision": "allow"})
    add("steer", "POST", f"{base}/@s1@/steer", {"text": "too late"})
    add("one", "GET", f"{base}/@s1@")
    add("tr", "GET", f"{base}/@s1@/transcript")
    add("continue", "POST", f"{base}/@s1@/continue", {"text": "next turn", "access_mode": "accept_edits"})
    wait("s1", "text:Commands:")
    add("stop", "POST", f"{base}/@s1@/stop", {})
    wait("s1", "done")
    add("one", "GET", f"{base}/@s1@")
    add("relaunch", "POST", f"{base}/@s1@/relaunch", {}, save="s3=session.id")
    wait("s3", "text:Commands:")
    add("stop", "POST", f"{base}/@s3@/stop", {})
    wait("s3", "done")
    add("one", "GET", f"{base}/@s1@")
    add("fork", "POST", f"{base}/@s2@/fork", {"prompt": ""})
    add("fork", "POST", f"{base}/@s2@/fork", {"prompt": "x", "mode": "weird"})

    # Custom fake agent: environment, structured output, approvals, errors.
    add("list", "POST", base, {"platform": "custom", "prompt": "approve please", "model": "fake-2",
                               "effort": "high", "access_mode": "accept_edits"}, save="c1=session.id")
    wait("c1", "text:ready for input")
    add("appr", "GET", f"{base}/@c1@/approvals")
    add("decide", "POST", f"{base}/@c1@/approvals/ctl-1", {"decision": "allow"})
    wait("c1", "text:frame")
    add("input", "POST", f"{base}/@c1@/input", {"text": "resolve"})
    wait("c1", "text:resolved by adapter")
    add("appr", "GET", f"{base}/@c1@/approvals")
    add("decide", "POST", f"{base}/@c1@/approvals/fake-req-1", {"decision": "allow"})
    add("steer", "POST", f"{base}/@c1@/steer", {"text": "steer me"}, save="st2=steer.id")
    wait("c1", "text:echo: steer me")
    add("input", "POST", f"{base}/@c1@/input", {"text": "quit"})
    wait("c1", "done")
    add("one", "GET", f"{base}/@c1@")
    add("tr", "GET", f"{base}/@c1@/transcript")
    add("logs", "GET", f"{base}/@c1@/logs?stream=stderr")
    add("list", "POST", base, {"platform": "custom", "prompt": "exit3 now", "access_mode": "full_access"},
        save="c2=session.id")
    wait("c2", "done")
    add("one", "GET", f"{base}/@c2@")
    add("tr", "GET", f"{base}/@c2@/transcript")
    add("continue", "POST", f"{base}/@c2@/continue", {"text": "retry", "model": "fake-1", "effort": "low",
                                                      "access_mode": "plan"})
    wait("c2", "done")
    add("one", "GET", f"{base}/@c2@")
    add("list", "POST", base, {"platform": "custom", "prompt": "exit3 child", "parent_id": "@c2@",
                               "access_mode": "read_only"}, save="c3=session.id")
    wait("c3", "done")
    add("one", "GET", f"{base}/@c3@")
    add("archive", "POST", f"{base}/@c3@/archive", {})
    add("one", "GET", f"{base}/@c3@")
    add("one", "DELETE", f"{base}/@c2@")
    add("one", "GET", f"{base}/@c2@")
    add("one", "DELETE", f"{base}/@sfail@")
    add("stop", "POST", f"{base}/@s0@/stop", {})
    wait("s0", "done")
    add("list", "GET", base)
    add("list", "GET", base, headers=other)
    add("groups", "GET", base + "/groups")
    return steps


if __name__ == "__main__":  # pragma: no cover - manual inspection
    for step in session_steps({"tokens": {"read_token": "rt"}}):
        print(step[1], step[2])
