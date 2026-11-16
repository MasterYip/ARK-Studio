"""Replay steps for the session-context store (session_context.py).

Fixture (``seed_contexts``, called by fixture.py): external transcripts in
``<project>.ark/sessions/`` -- one private to the replay user (Claude Code
lines with secrets), one shared by another user (a Codex rollout), one
private to another user, plus junk files -- and a mirrored internal session
(``sessions/internal/<s_id>/``) that has no live runtime copy.  The project
accepts session uploads.

Steps cover the list and detail views (transcript, raw, context) for every
kind of record and viewer, PATCH/DELETE, continue (seeded new sessions run
the echo demo agent, never a model), the agent upload API (redaction,
offsets, duplicates, replace, bad input, ARK_SESSION_* limits set small by
``ctx_env``), the uploader script and the project ZIP with ``sessions=1``.
"""
from __future__ import annotations

import hashlib
import json
from pathlib import Path

MODULE = "session-contexts"
T0 = "2026-01-03T04:05:06+00:00"


def ctx_env() -> dict:
    """Small upload limits so the replay reaches every limit with small bodies."""
    return {"ARK_SESSION_LINE_KB": "1", "ARK_SESSION_MAX_MB": "1", "ARK_SESSION_PROJECT_MAX_MB": "2"}


def _secret(kind: str) -> str:
    # Credential shapes built at run time (no literal key in the repository).
    return {"ant": "sk-" + "ant-" + "A1b2C3d4E5f6G7h8",
            "gh": "gh" + "p_" + "abcdefghij0123456789KLMN",
            "aws": "AK" + "IA" + "ABCDEFGHIJ012345",
            "bearer": "Bearer " + "tok.en_1234567890abc"}[kind]


def _jsonl(objs) -> str:
    return "".join((o if isinstance(o, str) else json.dumps(o, ensure_ascii=False)) + "\n" for o in objs)


def claude_lines(sid: str = "claude-native-ext-1") -> list:
    return [
        {"type": "summary", "summary": "x", "sessionId": sid},
        {"type": "user", "sessionId": sid, "cwd": "C:/work/repo one", "timestamp": "2026-01-03T04:00:00Z",
         "message": {"role": "user", "content": "Please fix the parser 解析 " + _secret("ant")}},
        {"type": "assistant", "sessionId": sid, "timestamp": "2026-01-03T04:00:05Z",
         "message": {"role": "assistant", "model": "claude-x-1", "content": [
             {"type": "text", "text": "Reading files. password = \"hunter2abc9\""},
             {"type": "tool_use", "id": "tu1", "name": "Read", "input": {"file_path": "a.py"}}]}},
        {"type": "user", "sessionId": sid, "message": {"role": "user", "content": [
            {"type": "tool_result", "tool_use_id": "tu1", "content": "file body " + _secret("gh")}]}},
        {"type": "assistant", "sessionId": sid, "isSidechain": True, "message": {"role": "assistant", "content": "side"}},
        {"type": "user", "sessionId": sid, "isMeta": True, "message": {"role": "user", "content": "meta"}},
        {"type": "system", "level": "error", "error": {"message": "rate limited"}},
        {"type": "system", "level": "error", "content": "plain error"},
        {"type": "system", "level": "error"},
        {"type": "assistant", "sessionId": sid, "timestamp": "2026-01-03T04:01:00Z",
         "message": {"role": "assistant", "model": "<synthetic>", "content": [{"type": "text", "text": "Done 完成."}]}},
        {"role": "assistant", "content": "role only"},
        {"type": "notice", "message": "heads up"},
        {"type": "file-history-snapshot", "messageId": "m"},
        {"type": "ark_truncated", "bytes": 99999, "source_type": "assistant", "preview": "..."},
        {"type": "assistant", "message": "not a dict"},
    ]


def codex_lines() -> list:
    return [
        {"timestamp": "2026-01-02T01:00:00Z", "type": "session_meta",
         "payload": {"id": "0199-codex-rollout-1", "cwd": "/home/u/proj", "originator": "codex_cli"}},
        {"timestamp": "2026-01-02T01:00:01Z", "type": "turn_context", "payload": {"model": "gpt-x-codex"}},
        {"type": "response_item", "payload": {"type": "message", "role": "user", "content": [
            {"type": "input_text", "text": "<environment_context>cwd</environment_context>"}]}},
        {"type": "response_item", "payload": {"type": "message", "role": "user", "content": [
            {"type": "input_text", "text": "Refactor the module"}, {"type": "input_text", "text": "  "},
            {"type": "input_text", "text": "and add tests"}]}},
        {"type": "response_item", "payload": {"type": "message", "role": "developer", "content": [{"text": "x"}]}},
        {"type": "response_item", "payload": {"type": "reasoning", "summary": [{"type": "summary_text", "text": "Plan it"}]}},
        {"type": "response_item", "payload": {"type": "reasoning", "summary": []}},
        {"type": "response_item", "payload": {"type": "function_call", "name": "shell", "call_id": "c1",
                                              "arguments": "{\"cmd\": [\"ls\"]}"}},
        {"type": "response_item", "payload": {"type": "local_shell_call", "id": "c2", "action": {"cmd": "pwd"}}},
        {"type": "response_item", "payload": {"type": "custom_tool_call", "call_id": "c3", "input": "patch", "arguments": None}},
        {"type": "response_item", "payload": {"type": "function_call_output", "call_id": "c1", "output": "a.py\nb.py"}},
        {"type": "response_item", "payload": {"type": "custom_tool_call_output", "call_id": "c3",
                                              "output": [{"type": "text", "text": "ok"}, "junk", {"text": "done"}]}},
        {"type": "response_item", "payload": {"type": "message", "role": "assistant", "content": [
            {"type": "output_text", "text": "Refactored. TOKEN=abc123def456 " + _secret("aws")}]}},
        {"type": "response_item", "payload": {"type": "web_search_call"}},
        {"type": "event_msg", "payload": {"type": "token_count"}},
    ]


def seed_contexts(project_path: Path, owner: str, other: str) -> dict:
    """External transcripts and an internal mirror in the project folder."""
    root = Path(project_path) / "sessions"
    root.mkdir(parents=True, exist_ok=True)

    def ext(sid, owner_, shared, lines, **extra):
        (root / f"{sid}.jsonl").write_text(_jsonl(lines), encoding="utf-8", newline="\n")
        size = (root / f"{sid}.jsonl").stat().st_size
        meta = {"id": sid, "kind": "external", "created_at": T0, "owner": owner_, "shared": shared,
                "received_bytes": size, "bytes": size, "lines": len(lines), "redactions": 2,
                "truncated_lines": 0, "chunks": [{"offset": 0, "length": size, "sha256": "0" * 64}],
                "linked_units": ["T-0001"], "updated_at": T0, "uploaded_by": owner_, **extra}
        (root / f"{sid}.json").write_text(json.dumps(meta, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")

    ext("ark-ses-own001", owner, False, claude_lines(), agent="claude", title="My Claude 会话",
        native_session_id="claude-native-ext-1", cwd="C:/work/repo one", model="claude-x-1", task_ref="T-0002")
    ext("ark-ses-shared1", other, True, codex_lines(), agent="codex", title="Shared rollout",
        native_session_id="0199-codex-rollout-1", cwd="/home/u/proj", updated_at="2026-01-04T00:00:00+00:00")
    ext("ark-ses-hidden1", other, False, [{"type": "user", "message": {"role": "user", "content": "private"}}],
        agent="other", title="Private", updated_at="2025-12-31T00:00:00+00:00")
    # Junk: invalid id inside, not JSON, a transcript without metadata, a tmp file.
    (root / "ark-ses-badid01.json").write_text(json.dumps({"id": "Not-An-Id"}), encoding="utf-8")
    (root / "ark-ses-broken1.json").write_text("{not json", encoding="utf-8")
    (root / "ark-ses-orphan1.jsonl").write_text(_jsonl([{"type": "notice", "message": "x"}]), encoding="utf-8")
    (root / "ark-ses-own001.json.tmp").write_text("{}", encoding="utf-8")
    (root / "notes.txt").write_text("not a session", encoding="utf-8")
    # A mirrored internal session without a runtime folder (a copied project).
    mirror = root / "internal" / "s_20251230101010mirror1"
    mirror.mkdir(parents=True, exist_ok=True)
    meta = {"id": mirror.name, "title": "Copied run", "platform": "claude", "platform_label": "Claude Code",
            "status": "stopped", "created_at": T0, "started_at": T0, "last_activity_at": "2026-01-03T05:00:00+00:00",
            "actor": owner, "shared": False, "prompt": "Original prompt 原始", "native_session_id": "claude-native-copy",
            "worktree": {"path": "D:/trees/feature x"}, "linked_units": ["T-0001", "T-0003"],
            "task_ref": "T-0001", "counts": {"stdout": 3, "stderr": 1}, "mirrored_at": T0}
    (mirror / "meta.json").write_text(json.dumps(meta, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    out = [{"t": T0, "line": json.dumps({"type": "assistant", "message": {"content": [{"type": "text", "text": "copied answer"}]}})},
           {"t": T0, "line": "plain copied line"}, "not json"]
    (mirror / "stdout.jsonl").write_text("\n".join(o if isinstance(o, str) else json.dumps(o) for o in out) + "\n",
                                         encoding="utf-8")
    (mirror / "stderr.jsonl").write_text(json.dumps({"t": T0, "line": "warn"}) + "\n", encoding="utf-8")
    other_mirror = root / "internal" / "s_20251230101010mirror2"
    other_mirror.mkdir(parents=True, exist_ok=True)
    (other_mirror / "meta.json").write_text(json.dumps({"id": other_mirror.name, "actor": other, "shared": True,
                                                        "platform": "codex", "title": "Other copy"}), encoding="utf-8")
    (root / "internal" / "s_bad").mkdir(parents=True, exist_ok=True)
    (root / "internal" / "s_20251230101010nometa1").mkdir(parents=True, exist_ok=True)
    # The project accepts uploads (a project setting).
    manifest_path = Path(project_path) / "project.json"
    manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
    manifest.setdefault("settings", {})["sessions"] = {"accept_uploads": True}
    manifest_path.write_text(json.dumps(manifest, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    # Pin folder times (as fixture._git_workspace does): the Editor tree lists them.
    import os
    pinned = 1767225600 * 10 ** 9
    for folder in [Path(project_path), root, root / "internal", *(root / "internal").iterdir()]:
        os.utime(folder, ns=(pinned, pinned))
    return {"own": "ark-ses-own001", "shared": "ark-ses-shared1", "hidden": "ark-ses-hidden1",
            "mirror": mirror.name, "mirror2": other_mirror.name}


def _upload(content: str, **extra) -> dict:
    body = {"content": content, **extra}
    return body


def _sha(content: str) -> str:
    return hashlib.sha256(content.encode("utf-8")).hexdigest()


def ctx_steps(fixture: dict) -> list[tuple]:
    import replay_sessions
    m = {"X-Replay-Module": MODULE}
    other = {**m, "X-Replay-As": "1"}
    read = {**m, "X-ARK-Read-Token": fixture["tokens"]["read_token"]}
    agent = {**m, "X-ARK-Token": fixture["tokens"]["server"], "X-Agent-Name": "uploader"}
    personal = {**m, "Authorization": "Bearer " + fixture["tokens"]["personal_write"]}
    seeded = fixture.get("seeded_contexts") or {}
    tasks = fixture.get("seeded_sessions") or {}
    own, shared, hidden = seeded.get("own", "x"), seeded.get("shared", "x"), seeded.get("hidden", "x")
    mirror, mirror2 = seeded.get("mirror", "x"), seeded.get("mirror2", "x")
    claude, codex, hidden_task = tasks.get("claude", "x"), tasks.get("codex", "x"), tasks.get("hidden", "x")
    R = {"list": "exact:/api/session-contexts", "get": "prefix:/api/session-contexts/",
         "one": "fullmatch:/api/session-contexts/([^/]+)",
         "continue": "fullmatch:/api/session-contexts/([^/]+)/continue",
         "watcher": "exact:/api/agent/v1/sessions/watcher.py",
         "status": "fullmatch:/api/agent/v1/sessions/([^/]+)",
         "upload": "fullmatch:/api/agent/v1/sessions/([^/]+)/upload",
         "export": "exact:/api/export", "agent_export": "exact:/api/agent/v1/export",
         "settings": "exact:/api/project/settings", "sessions": r"regex:^/api/agent-sessions/([^/]+)/stop$"}
    steps: list[tuple] = []

    def add(key, method, path, body=None, headers=None, save=""):
        h = dict(headers or m)
        if save:
            h["X-Replay-Save"] = save
        steps.append((R[key], method, path, body, h))

    def wait(var, until):
        steps.append((replay_sessions.WAIT_SESSION, var, until, None, {"X-Replay-Module": MODULE}))

    base = "/api/session-contexts"
    add("list", "GET", base)
    add("list", "GET", base, headers=other)
    add("list", "HEAD", base)
    add("list", "GET", base, headers=read)
    ids = (own, shared, hidden, mirror, mirror2, claude, codex, hidden_task, "s_20260101000000abc123",
           own.upper(), "ark-ses-x", "ark-ses-nope0001", "s_00000000000000nope00", "s_bad", "bad-id", "%20")
    for sid in ids:
        for suffix in ("", "/transcript", "/raw", "/context"):
            add("get", "GET", f"{base}/{sid}{suffix}")
        add("get", "GET", f"{base}/{sid}", headers=other)
    for sid in (own, shared, mirror, codex):
        add("get", "GET", f"{base}/{sid}/transcript", headers=other)
        add("get", "GET", f"{base}/{sid}/raw", headers=other)
    add("get", "GET", f"{base}/{own}/bogus")
    add("get", "GET", f"{base}/{own}/transcript/x")
    add("get", "GET", f"{base}/")
    add("get", "HEAD", f"{base}/{own}/raw")
    add("get", "GET", f"{base}/{own}/raw", headers={**m, "Accept-Encoding": "gzip"})

    # PATCH: own, invalid input, others' records, the mirror, a live task session.
    add("one", "PATCH", f"{base}/{own}", {"title": "  Renamed\u0000 会话  ", "linked_units": "T-1, T-2,,bad id,T-1",
                                         "shared": True})
    add("one", "PATCH", f"{base}/{own}", {"linked_units": 5})
    add("one", "PATCH", f"{base}/{own}", {"shared": "yes"})
    add("one", "PATCH", f"{base}/{own}", {"title": "x" * 300, "linked_units": [f"N-{i}" for i in range(30)]})
    add("one", "PATCH", f"{base}/{own}", {"title": 7, "shared": False})
    add("one", "PATCH", f"{base}/{shared}", {"title": "mine now"})
    add("one", "PATCH", f"{base}/{hidden}", {"title": "mine now"})
    add("one", "PATCH", f"{base}/{own}", {"title": "other"}, headers=other)
    add("one", "PATCH", f"{base}/{mirror}", {"title": "", "linked_units": ["T-9"], "shared": True})
    add("one", "PATCH", f"{base}/{mirror}", {"title": "Copy renamed"})
    add("one", "PATCH", f"{base}/{mirror2}", {"title": "x"})
    add("one", "PATCH", f"{base}/{claude}", {"title": "Live renamed", "linked_units": ["T-4"]})
    add("one", "PATCH", f"{base}/{codex}", {"title": "x"})
    add("one", "PATCH", f"{base}/ark-ses-x", {"title": "x"})
    add("one", "PATCH", f"{base}/{own}", {"title": "x"}, headers=read)
    add("list", "GET", base)
    add("list", "GET", base, headers=other)

    # Agent upload API.
    up = "/api/agent/v1/sessions"
    add("watcher", "GET", f"{up}/watcher.py", headers=agent)
    add("watcher", "HEAD", f"{up}/watcher.py", headers=agent)
    add("watcher", "GET", f"{up}/watcher.py", headers={**agent, "Accept-Encoding": "gzip"})
    add("watcher", "GET", f"{up}/watcher.py", headers=m)
    for sid in (own, shared, hidden, "ark-ses-new0001", "ARK-SES-NEW0001", "ark-ses-x", "s_20260101000000abc123"):
        add("status", "GET", f"{up}/{sid}", headers=agent)
        add("status", "GET", f"{up}/{sid}", headers=personal)
    first = _jsonl(claude_lines("claude-native-up-1")[:4])
    second = _jsonl(claude_lines("claude-native-up-1")[4:])
    new = "ark-ses-up00001"
    add("upload", "POST", f"{up}/{new}/upload", _upload(first, offset=0, sha256=_sha(first), agent="claude-code",
                                                       title="Uploaded 会话", task="T-0001"), agent)
    add("upload", "POST", f"{up}/{new}/upload", _upload(first, offset=0, sha256=_sha(first)), agent)  # duplicate
    add("upload", "POST", f"{up}/{new}/upload", _upload(second, offset=3), agent)  # offset mismatch
    add("upload", "POST", f"{up}/{new}/upload", _upload(second, offset=len(first.encode()), length=len(second.encode()) + 10,
                                                       model="override-model", cwd=" /x\u0000y ", prompt="p" * 500), agent)
    add("upload", "POST", f"{up}/{new}/upload", _upload("", offset=len(first.encode()) + len(second.encode()) + 10), agent)
    add("status", "GET", f"{up}/{new}", headers=agent)
    add("upload", "POST", f"{up}/{new.upper()}/upload", _upload("", offset="0"), agent)
    rollout = _jsonl(codex_lines())
    add("upload", "POST", f"{up}/ark-ses-codex01/upload", _upload(rollout, agent="OpenAI", native_session_id=""), personal)
    add("upload", "POST", f"{up}/ark-ses-codex01/upload", _upload(rollout, mode="replace", agent="weird-agent"), personal)
    add("upload", "POST", f"{up}/ark-ses-codex01/upload", _upload(rollout, mode="REPLACE ", offset=5), personal)
    add("upload", "POST", f"{up}/{shared}/upload", _upload(rollout, offset=0), personal)  # another user's id
    add("upload", "POST", f"{up}/{hidden}/upload", _upload(rollout, offset=0), personal)
    for body in ({"content": 5}, {}, {"content": "{\"a\": 1}"}, {"content": "{bad\n"},
                 {"content": "[1, 2]\n"}, {"content": "\n\r\n  \n"}, {"content": "{}\n", "sha256": "ABC"},
                 {"content": "{}\n", "sha256": " " + _sha("{}\n").upper() + " "},
                 {"content": "{}\n", "mode": "merge"}, {"content": "{}\n", "offset": "x"},
                 {"content": "{}\n", "offset": -1}, {"content": "{}\n", "offset": 2.9},
                 {"content": "{}\n", "offset": [1]}, {"content": "{}\n", "length": 0},
                 {"content": "{}\n", "length": "abc"}, {"content": "{}\n", "length": 2 ** 31},
                 {"content": "{}\n", "length": -4}, {"content": "", "length": 0},
                 {"content": "{}\n", "task": "bad task!", "title": ["t"]},
                 {"content": "{\"type\":\"x\"}\r\n{\"type\": \"y\"}\n"}):
        add("upload", "POST", f"{up}/ark-ses-misc001/upload", body, agent)
    add("upload", "POST", f"{up}/ark-ses-x/upload", {"content": "{}\n"}, agent)
    add("upload", "POST", f"{up}/ark-ses-misc002/upload", {"content": "{}\n"}, m)  # no token
    add("upload", "POST", f"{up}/ark-ses-misc002/upload", {"content": "{}\n"},
        {**m, "X-ARK-Token": fixture["tokens"]["server"], "Origin": "http://evil.example"})
    # Line limit (ARK_SESSION_LINE_KB=1): oversize lines become markers.
    long_line = json.dumps({"type": "assistant", "message": {"role": "assistant", "content": "z" * 1500 + " " + _secret("ant")}})
    add("upload", "POST", f"{up}/ark-ses-long001/upload",
        {"content": long_line + "\n" + json.dumps({"type": "user", "pad": "é" * 600}) + "\n"
         + json.dumps({"text": _secret("bearer") + " and " + fixture["tokens"]["server"]}) + "\n"}, agent)
    # Session (1 MB) and project (2 MB) quotas; the chunk limit (4 MB).
    blob = json.dumps({"type": "notice", "message": "q" * 900}) + "\n"
    big = blob * 1000  # ~0.9 MB
    add("upload", "POST", f"{up}/ark-ses-big0001/upload", {"content": big}, agent)
    add("upload", "POST", f"{up}/ark-ses-big0001/upload", {"content": big, "offset": len(big.encode())}, agent)
    add("upload", "POST", f"{up}/ark-ses-big0001/upload", {"content": big, "mode": "replace"}, agent)
    add("upload", "POST", f"{up}/ark-ses-big0002/upload", {"content": big}, agent)
    add("upload", "POST", f"{up}/ark-ses-big0003/upload", {"content": big}, agent)
    add("upload", "POST", f"{up}/ark-ses-big0004/upload", {"content": "x" * (4 * 1024 * 1024 + 1)}, agent)
    add("list", "GET", base)
    add("get", "GET", f"{base}/{new}")
    add("get", "GET", f"{base}/{new}/transcript")
    add("get", "GET", f"{base}/{new}/raw")
    add("get", "GET", f"{base}/{new}/context")
    add("get", "GET", f"{base}/ark-ses-codex01/transcript")
    add("get", "GET", f"{base}/ark-ses-codex01/context")
    add("get", "GET", f"{base}/ark-ses-long001/transcript")
    add("get", "GET", f"{base}/ark-ses-codex01", headers=other)

    # Continue: validation, resume checks, seeded new sessions (echo agent).
    add("continue", "POST", f"{base}/{own}/continue", {"mode": "later"})
    add("continue", "POST", f"{base}/{own}/continue", {"mode": "resume", "text": "x"})
    add("continue", "POST", f"{base}/{claude}/continue", {"mode": "resume"})
    add("continue", "POST", f"{base}/{claude}/continue", {"mode": " RESUME ", "text": "  "})
    add("continue", "POST", f"{base}/{codex}/continue", {"mode": "resume", "text": "go"})
    add("continue", "POST", f"{base}/{hidden}/continue", {"text": "go"})
    add("continue", "POST", f"{base}/ark-ses-hidden1/continue", {"text": "go"}, headers=other)
    add("continue", "POST", f"{base}/{mirror2}/continue", {"text": "go"})
    add("continue", "POST", f"{base}/ark-ses-misc001/continue", {"text": "go"})
    add("continue", "POST", f"{base}/{own}/continue", {"text": "go"}, headers=read)
    add("continue", "POST", f"{base}/{own}/continue", {"platform": "nope", "text": "x"})
    add("continue", "POST", f"{base}/{own}/continue", {"platform": "echo", "message": "keep going 继续",
                                                      "mode": "seed"}, save="cx1=session.id")
    wait("cx1", "text:Commands:")
    add("get", "GET", f"{base}/@cx1@")
    add("continue", "POST", f"{base}/{mirror}/continue", {"platform": "echo", "model": ""}, save="cx2=session.id")
    wait("cx2", "text:Commands:")
    for var in ("cx1", "cx2"):
        add("sessions", "POST", f"/api/agent-sessions/@{var}@/stop", {})
        wait(var, "done")
    add("get", "GET", f"{base}/@cx1@")
    add("get", "GET", f"{base}/@cx1@/transcript")
    add("get", "GET", f"{base}/@cx1@/raw")
    add("continue", "POST", f"{base}/@cx1@/continue", {"platform": "echo", "text": "again"}, save="cx3=session.id")
    wait("cx3", "text:Commands:")
    add("sessions", "POST", f"/api/agent-sessions/@cx3@/stop", {})
    wait("cx3", "done")
    add("list", "GET", base)
    add("list", "GET", base, headers=other)

    # Project ZIP with sessions (mirrors first); then deletes.
    for query in ("format=project&sessions=1", "format=project&sessions=yes", "format=project&sessions=0"):
        add("export", "GET", f"/api/export?{query}")
    add("agent_export", "GET", "/api/agent/v1/export?format=project&sessions=true", headers=agent)
    add("one", "DELETE", f"{base}/{own}", headers=other)
    add("one", "DELETE", f"{base}/{shared}")
    add("one", "DELETE", f"{base}/{own}", headers=read)
    add("one", "DELETE", f"{base}/{own.upper()}")
    add("one", "DELETE", f"{base}/{own}")
    add("one", "DELETE", f"{base}/{mirror}")
    add("one", "DELETE", f"{base}/{mirror}")
    add("one", "DELETE", f"{base}/@cx3@")
    add("one", "DELETE", f"{base}/{hidden_task}")
    add("one", "DELETE", f"{base}/s_bad")
    add("list", "GET", base)
    add("export", "GET", "/api/export?format=project&sessions=1")
    # Uploads switched off by the project setting.
    add("settings", "PATCH", "/api/project/settings", {"sessions": {"accept_uploads": False}})
    add("upload", "POST", f"{up}/ark-ses-off0001/upload", {"content": "{}\n"}, agent)
    add("status", "GET", f"{up}/ark-ses-up00001", headers=agent)
    add("list", "GET", base)
    return steps
