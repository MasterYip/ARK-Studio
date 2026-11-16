"""Session context store, redaction, provider normalisation and the watcher."""
import hashlib
import json
import os
import sys
import tempfile
import time
import unittest
from pathlib import Path
from unittest import mock

from arkstudio import secret_redact, session_context as sc, session_watch, sessions


def jsonl(*objects) -> str:
    return "".join(json.dumps(obj) + "\n" for obj in objects)


CLAUDE_LINES = [
    {"type": "user", "sessionId": "1458aee0-c3d7-4749", "cwd": "/work/repo",
     "message": {"role": "user", "content": [{"type": "text", "text": "Plan the ablation. ARK session ark-ses-abc123"}]},
     "timestamp": "2026-11-01T10:00:00Z"},
    {"type": "assistant", "sessionId": "1458aee0-c3d7-4749",
     "message": {"role": "assistant", "model": "claude-test-model",
                 "content": [{"type": "text", "text": "Here is the plan."},
                             {"type": "tool_use", "id": "tu1", "name": "Read", "input": {"path": "a.py"}}]},
     "timestamp": "2026-11-01T10:00:05Z"},
    {"type": "user", "sessionId": "1458aee0-c3d7-4749",
     "message": {"role": "user", "content": [{"type": "tool_result", "tool_use_id": "tu1", "content": "print(1)"}]}},
    {"type": "file-history-snapshot", "snapshot": {"x": 1}},
]

CODEX_LINES = [
    {"type": "session_meta", "payload": {"id": "01a1b0ad-f5ac-7173", "cwd": "E:\\repo"}},
    {"type": "turn_context", "payload": {"model": "gpt-test"}},
    {"type": "response_item", "payload": {"type": "message", "role": "developer",
                                          "content": [{"type": "input_text", "text": "system rules"}]}},
    {"type": "response_item", "payload": {"type": "message", "role": "user",
                                          "content": [{"type": "input_text", "text": "Fix the bug"}]}},
    {"type": "response_item", "payload": {"type": "function_call", "name": "shell", "call_id": "c1",
                                          "arguments": "{\"cmd\": \"ls\"}"}},
    {"type": "response_item", "payload": {"type": "function_call_output", "call_id": "c1", "output": "a.py"}},
    {"type": "response_item", "payload": {"type": "message", "role": "assistant",
                                          "content": [{"type": "output_text", "text": "Fixed."}]}},
    {"type": "event_msg", "payload": {"type": "token_count"}},
]


class RedactionTests(unittest.TestCase):
    def test_known_shapes_and_assignments(self):
        cases = {
            "Authorization: Bearer abcdefghijklmnop1234": "[REDACTED:bearer]",
            "token arka_abcdefghijklmnopqrstuvwxyz12": "[REDACTED:ark_token]",
            "key sk-ant-api03-abcdefghijklmnop": "[REDACTED:anthropic_key]",
            "OPENAI_API_KEY=sk-proj-abcdefghijklmnopqrstuvwx": "[REDACTED:",
            "ghp_abcdefghijklmnopqrstuvwxyz0123": "[REDACTED:github_token]",
            "AKIAABCDEFGHIJKLMNOP": "[REDACTED:aws_key]",
            'password: "hunter2isMyPass9"': "[REDACTED:credential]",
            "-----BEGIN OPENSSH PRIVATE KEY-----\nabc\n-----END OPENSSH PRIVATE KEY-----": "[REDACTED:private_key]",
        }
        for text, mark in cases.items():
            with self.subTest(text=text):
                out, count = secret_redact.redact_text(text)
                self.assertIn(mark, out)
                self.assertGreaterEqual(count, 1)

    def test_code_and_placeholders_are_kept(self):
        for text in ('const apiKey = getEnv("X");', "max_tokens: 120000",
                     "password: ${DB_PASSWORD}", "api_key = env:OPENAI_KEY", "PASSWORD=changeme"):
            with self.subTest(text=text):
                self.assertEqual(secret_redact.redact_text(text), (text, 0))

    def test_extra_literal_and_nested_values(self):
        value, count = secret_redact.redact_value(
            {"a": ["x server-secret-token-123 y"], "n": 3}, ["server-secret-token-123"])
        self.assertEqual(value, {"a": ["x [REDACTED:server_token] y"], "n": 3})
        self.assertEqual(count, 1)


class StoreTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.project = Path(self.tmp.name) / "Demo.ark"
        self.project.mkdir()
        self.store = sc.ContextStore(self.project, extra_secrets=["legacy-server-token-xyz"])
        self.sid = "ark-ses-abc123"

    def tearDown(self):
        self.tmp.cleanup()

    def upload(self, content, offset=0, **extra):
        return self.store.upload(self.sid, {"offset": offset, "content": content, **extra}, actor="agent:alice")

    def test_layout_append_and_metadata_sniffing(self):
        first = jsonl(*CLAUDE_LINES[:2])
        result = self.upload(first)
        self.assertTrue(result["created"])
        self.assertEqual(result["received_bytes"], len(first.encode()))
        files = sorted(p.name for p in (self.project / "sessions").iterdir())
        self.assertEqual(files, [f"{self.sid}.json", f"{self.sid}.jsonl"])
        second = jsonl(*CLAUDE_LINES[2:])
        result = self.upload(second, offset=len(first.encode()))
        self.assertEqual(result["appended_lines"], 2)
        meta = self.store.get(self.sid)
        self.assertEqual(meta["agent"], "claude")
        self.assertEqual(meta["native_session_id"], "1458aee0-c3d7-4749")
        self.assertEqual(meta["cwd"], "/work/repo")
        self.assertEqual(meta["model"], "claude-test-model")
        self.assertEqual(meta["uploaded_by"], "agent:alice")
        self.assertEqual(meta["lines"], 4)
        public = self.store.public(meta)
        self.assertEqual(public["resume"]["command"], "cd /work/repo && claude --resume 1458aee0-c3d7-4749"
                         if os.name != "nt" else 'cd "/work/repo" && claude --resume 1458aee0-c3d7-4749')

    def test_idempotent_retry_and_offset_mismatch(self):
        chunk = jsonl(CLAUDE_LINES[0])
        self.upload(chunk, sha256=hashlib.sha256(chunk.encode()).hexdigest())
        retry = self.upload(chunk)
        self.assertTrue(retry["duplicate"])
        self.assertEqual(self.store.get(self.sid)["lines"], 1)
        with self.assertRaises(sc.ContextError) as caught:
            self.upload(jsonl(CLAUDE_LINES[1]), offset=5)
        self.assertEqual((caught.exception.status, caught.exception.code), (409, "SESSION_OFFSET_MISMATCH"))
        self.assertIn(str(len(chunk.encode())), str(caught.exception))
        with self.assertRaises(sc.ContextError) as caught:
            self.upload(chunk, offset=len(chunk.encode()), sha256="00" * 32)
        self.assertEqual(caught.exception.code, "SESSION_HASH_MISMATCH")

    def test_replace_rewrites_and_resets_offset(self):
        self.upload(jsonl(*CLAUDE_LINES))
        new = jsonl(CLAUDE_LINES[0])
        result = self.store.upload(self.sid, {"offset": 0, "content": new, "mode": "replace"})
        self.assertEqual(result["received_bytes"], len(new.encode()))
        self.assertEqual(len(self.store.lines(self.sid)), 1)
        with self.assertRaises(sc.ContextError):
            self.store.upload(self.sid, {"offset": 3, "content": new, "mode": "replace"})

    def test_rejects_non_jsonl(self):
        for content, code in (("not json\n", "SESSION_LINE_NOT_JSON"), ("[1, 2]\n", "SESSION_LINE_NOT_OBJECT"),
                              ('{"a": 1}', "SESSION_NOT_JSONL")):
            with self.subTest(content=content), self.assertRaises(sc.ContextError) as caught:
                self.upload(content)
            self.assertEqual(caught.exception.code, code)
        self.assertFalse((self.project / "sessions" / f"{self.sid}.json").exists())
        with self.assertRaises(sc.ContextError) as caught:
            self.store.upload("../../etc", {"content": "{}\n"})
        self.assertEqual(caught.exception.code, "SESSION_ID_INVALID")

    def test_redaction_before_storage(self):
        secret = "sk-ant-api03-SECRETSECRETSECRET123"
        line = {"type": "user", "message": {"role": "user", "content": f"use {secret} and legacy-server-token-xyz"}}
        result = self.upload(jsonl(line))
        self.assertEqual(result["redactions"], 2)
        stored = (self.project / "sessions" / f"{self.sid}.jsonl").read_text(encoding="utf-8")
        self.assertNotIn(secret, stored)
        self.assertNotIn("legacy-server-token-xyz", stored)
        self.assertIn("[REDACTED:anthropic_key]", stored)

    def test_line_and_storage_limits(self):
        with mock.patch.dict(os.environ, {"ARK_SESSION_LINE_KB": "1", "ARK_SESSION_MAX_MB": "1",
                                          "ARK_SESSION_PROJECT_MAX_MB": "2"}):
            big = {"type": "assistant", "message": {"role": "assistant", "content": "x" * 5000}}
            result = self.upload(jsonl(big))
            self.assertEqual(self.store.get(self.sid)["truncated_lines"], 1)
            stored = json.loads(self.store.lines(self.sid)[0])
            self.assertEqual(stored["type"], "ark_truncated")
            offset = result["received_bytes"]
            filler = jsonl(*[{"type": "notice", "message": "y" * 900}] * 1200)
            with self.assertRaises(sc.ContextError) as caught:
                self.upload(filler, offset=offset)
            self.assertEqual((caught.exception.status, caught.exception.code), (413, "SESSION_QUOTA"))
            other = sc.ContextStore(self.project)
            for index in range(2):
                part = jsonl(*[{"type": "notice", "message": "z" * 900}] * 900)
                other.upload(f"ark-ses-fill{index}a", {"content": part})
            with self.assertRaises(sc.ContextError) as caught:
                other.upload("ark-ses-fill9a", {"content": part})
            self.assertIn("project", str(caught.exception))
        with mock.patch.object(sc, "limits", return_value={**sc.limits(), "chunk_bytes": 10}):
            with self.assertRaises(sc.ContextError) as caught:
                self.upload(jsonl({"a": 1, "b": 2}), offset=10**6)
            self.assertEqual(caught.exception.code, "SESSION_CHUNK_TOO_LARGE")

    def test_transcript_summary_update_and_delete(self):
        self.upload(jsonl(*CLAUDE_LINES))
        data = self.store.transcript(self.sid)
        roles = [(item["role"], item["text"]) for item in data["items"] if item["type"] == "message"]
        self.assertEqual(roles[0][0], "user")
        self.assertIn("Here is the plan.", [text for _, text in roles])
        self.assertEqual(data["stats"]["tools"], 1)
        # File order is kept: a tool result without a timestamp follows its call.
        self.assertEqual([item["type"] for item in data["items"]], ["message", "message", "tool"])
        self.assertEqual(data["items"][-1]["status"], "ok")
        updated = self.store.update(self.sid, {"title": "Ablation plan", "linked_units": ["T-0001", "bad id!", "T-0001"]})
        self.assertEqual(updated["title"], "Ablation plan")
        self.assertEqual(updated["linked_units"], ["T-0001"])
        summary = sc.context_summary(updated, data["items"])
        self.assertIn("ark-ses-abc123", summary)
        self.assertIn("T-0001", summary)
        self.assertIn("Plan the ablation", summary)
        self.assertLessEqual(len(summary), sc.SUMMARY_LIMIT)
        self.assertEqual(self.store.delete(self.sid), {"deleted": self.sid})
        self.assertEqual(list((self.project / "sessions").iterdir()), [])
        with self.assertRaises(sc.ContextError):
            self.store.get(self.sid)

    def test_codex_rollout_normalisation(self):
        entries = sc.transcript_entries(jsonl(*CODEX_LINES).splitlines())
        self.assertEqual(len(entries), 4)  # developer message and token counters are dropped
        meta = sc.sniff_metadata(CODEX_LINES)
        self.assertEqual(meta, {"agent": "codex", "native_session_id": "01a1b0ad-f5ac-7173",
                                "cwd": "E:\\repo", "model": "gpt-test"})
        self.upload(jsonl(*CODEX_LINES))
        data = self.store.transcript(self.sid)
        texts = [item.get("text") for item in data["items"] if item["type"] == "message"]
        self.assertEqual(texts, ["Fix the bug", "Fixed."])
        tool = next(item for item in data["items"] if item["type"] == "tool")
        self.assertEqual(tool["name"], "shell")
        self.assertIn("codex resume 01a1b0ad-f5ac-7173", self.store.public(self.store.get(self.sid))["resume"]["command"])

    def test_resume_commands(self):
        self.assertEqual(sc.resume_command("codex", "abc-1234"), "codex resume abc-1234")
        self.assertEqual(sc.resume_command("claude", "abc-1234"), "claude --resume abc-1234")
        self.assertEqual(sc.resume_command("dsh", "abc-1234"), "")
        self.assertEqual(sc.resume_command("claude", "x; rm -rf /"), "")


class NativeIdTests(unittest.TestCase):
    def test_native_id_from_output_lines(self):
        self.assertEqual(sessions.native_session_id_from_line(
            '{"type":"system","subtype":"init","session_id":"4f0c1234-aa"}'), "4f0c1234-aa")
        self.assertEqual(sessions.native_session_id_from_line(
            '{"type":"thread.started","thread_id":"0199abcd-ef"}'), "0199abcd-ef")
        self.assertEqual(sessions.native_session_id_from_line("session id: 0199abcd-1234-5678"), "0199abcd-1234-5678")
        self.assertEqual(sessions.native_session_id_from_line("plain text"), "")
        self.assertEqual(sessions.native_session_id_from_line('{"type":"system","session_id":"x y"}'), "")

    def test_resume_argv(self):
        self.assertEqual(sessions._resume_argv(["claude", "-p", "hi"], "claude", "abcd1234", "hi"),
                         ["claude", "--resume", "abcd1234", "-p", "hi"])
        self.assertEqual(
            sessions._resume_argv(["codex", "exec", "-m", "m", "--skip-git-repo-check", "hi"], "codex", "abcd1234", "hi"),
            ["codex", "exec", "-m", "m", "--skip-git-repo-check", "resume", "abcd1234", "hi"])
        self.assertEqual(sessions._resume_argv(["dsh", "headless", "-"], "dsh", "abcd1234", "hi"),
                         ["dsh", "headless", "-"])


def _fake_claude(folder: Path) -> list[str]:
    """A 'claude' CLI that announces a session id and echoes its argv."""
    script = folder / "fake_claude.py"
    script.write_text(
        "import json, sys\n"
        "print(json.dumps({'type': 'system', 'subtype': 'init', 'session_id': 'native-1234abcd'}))\n"
        "print('ARGV ' + json.dumps(sys.argv[1:]))\n", encoding="utf-8")
    if os.name == "nt":
        wrapper = folder / "claude.cmd"
        wrapper.write_text(f'@"{sys.executable}" "{script}" %*\r\n', encoding="utf-8")
    else:
        wrapper = folder / "claude"
        wrapper.write_text(f'#!/bin/sh\nexec "{sys.executable}" "{script}" "$@"\n', encoding="utf-8")
        wrapper.chmod(0o755)
    return [str(wrapper), "-p", "{prompt}"]


def fake_claude_preset(folder: Path) -> dict:
    return {"id": "claude", "label": "Claude Code", "command": _fake_claude(folder),
            "prompt_mode": "arg", "followup": False}


class InternalResumeTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        root = Path(self.tmp.name)
        self.manager = sessions.SessionManager(
            root / "sessions",
            project_provider=lambda: {"id": "prj_x", "name": "X", "path": str(root / "X.ark")},
            context_provider=lambda: {},
            presets=[fake_claude_preset(root)],
        )

    def tearDown(self):
        for session in list(self.manager._sessions.values()):
            if session.proc is not None:
                session.proc.wait(timeout=20)
        self.tmp.cleanup()

    def wait_done(self, session_id):
        deadline = time.time() + 20
        while time.time() < deadline:
            session = self.manager.get(session_id)
            if not session.is_active and session.meta.get("status") in {"stopped", "errored"}:
                return session
            time.sleep(0.1)
        self.fail("session did not finish")

    def test_native_id_is_recorded_and_resume_passes_it(self):
        session = self.manager.start({"platform": "claude", "prompt": "first turn"})
        session = self.wait_done(session.id)
        self.assertEqual(session.meta.get("native_session_id"), "native-1234abcd")
        self.assertTrue(self.manager.resumable(session))
        self.manager.continue_session(session.id, "second turn", resume=True)
        session = self.wait_done(session.id)
        argv_lines = [json.loads(line.split(" ", 1)[1]) for line in
                      (entry["line"] for entry in self.manager._all_log_entries(session, "stdout"))
                      if line.startswith("ARGV ")]
        self.assertEqual(argv_lines[-1], ["--resume", "native-1234abcd", "-p", "second turn"])
        self.assertEqual(argv_lines[0], ["-p", "first turn"])

    def test_resume_requires_native_id(self):
        session = self.manager.start({"platform": "claude", "prompt": "first"})
        session = self.wait_done(session.id)
        session.meta.pop("native_session_id", None)
        with self.assertRaises(sessions.SessionError) as caught:
            self.manager.continue_session(session.id, "again", resume=True)
        self.assertEqual(caught.exception.code, "SESSION_NOT_RESUMABLE")


class FakeClient:
    """In-memory stand-in for the ARK agent API (consent + ContextStore)."""

    def __init__(self, store: sc.ContextStore, enabled: bool = True):
        self.store = store
        self.enabled = enabled
        self.uploads: list[dict] = []
        self.fail_next_409 = False

    def status(self, session_id):
        return self.store.status(session_id, uploads_enabled=self.enabled)

    def upload(self, session_id, body):
        if not self.enabled:
            raise session_watch.UploadError(403, "SESSION_UPLOADS_DISABLED", "off")
        if self.fail_next_409:
            self.fail_next_409 = False
            raise session_watch.UploadError(409, "SESSION_OFFSET_MISMATCH", "moved")
        self.uploads.append(body)
        try:
            return self.store.upload(session_id, body)
        except sc.ContextError as exc:
            raise session_watch.UploadError(exc.status, exc.code, str(exc)) from exc


class WatcherTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.root = Path(self.tmp.name)
        (self.root / "P.ark").mkdir()
        self.store = sc.ContextStore(self.root / "P.ark")
        self.client = FakeClient(self.store)
        self.file = self.root / "session.jsonl"
        self.sid = "ark-ses-watch01"

    def tearDown(self):
        self.tmp.cleanup()

    def sync(self, **kw):
        return session_watch.sync_once(self.client, self.sid, self.file, **kw)

    def test_tails_complete_lines_only(self):
        self.file.write_bytes(jsonl(*CLAUDE_LINES[:2]).encode() + b'{"type": "us')
        first = self.sync()
        self.assertEqual(first["received_bytes"], len(jsonl(*CLAUDE_LINES[:2]).encode()))
        self.assertEqual(self.sync()["sent"], 0)  # nothing new until the line completes
        with self.file.open("ab") as fh:
            fh.write(b'er"}\n')
        second = self.sync()
        self.assertGreater(second["sent"], 0)
        self.assertEqual(second["received_bytes"], self.file.stat().st_size)
        self.assertEqual(self.store.get(self.sid)["lines"], 3)

    def test_chunking_and_oversize_line_marker(self):
        lines = [{"type": "notice", "message": f"line {i} " + "x" * 200} for i in range(40)]
        lines.insert(10, {"type": "notice", "message": "y" * 5000})
        self.file.write_text(jsonl(*lines), encoding="utf-8")
        result = self.sync(chunk_bytes=2048)
        self.assertEqual(result["received_bytes"], self.file.stat().st_size)
        self.assertGreater(len(self.client.uploads), 3)
        self.assertTrue(all(len(u["content"].encode()) <= 2048 for u in self.client.uploads))
        stored = [json.loads(line) for line in self.store.lines(self.sid)]
        self.assertEqual(len(stored), 41)
        self.assertEqual(stored[10]["type"], "ark_truncated")

    def test_shrunk_file_is_replaced_and_409_resyncs(self):
        self.file.write_text(jsonl(*CLAUDE_LINES), encoding="utf-8")
        self.sync()
        self.file.write_text(jsonl(CLAUDE_LINES[0]), encoding="utf-8")
        self.sync()
        self.assertEqual(self.client.uploads[-1]["mode"], "replace")
        self.assertEqual(len(self.store.lines(self.sid)), 1)
        with self.file.open("a", encoding="utf-8") as fh:
            fh.write(jsonl(CLAUDE_LINES[1]))
        self.client.fail_next_409 = True
        self.assertEqual(self.sync()["sent"], 0)
        self.assertGreater(self.sync()["sent"], 0)
        self.assertEqual(len(self.store.lines(self.sid)), 2)

    def test_consent_off_stops_the_watcher(self):
        self.client.enabled = False
        self.file.write_text(jsonl(CLAUDE_LINES[0]), encoding="utf-8")
        messages = []
        code = session_watch.watch(self.client, self.sid, self.file, once=True, log=messages.append)
        self.assertEqual(code, session_watch.EXIT_DISABLED)
        self.assertTrue(any("session uploads" in m for m in messages))
        self.assertFalse((self.root / "P.ark" / "sessions").exists())

    def test_watch_loop_uploads_at_interval(self):
        self.file.write_text(jsonl(CLAUDE_LINES[0]), encoding="utf-8")
        sleeps = []

        def fake_sleep(seconds):
            sleeps.append(seconds)
            with self.file.open("a", encoding="utf-8") as fh:
                fh.write(jsonl(CLAUDE_LINES[1]))

        code = session_watch.watch(self.client, self.sid, self.file, interval=30, sleep=fake_sleep,
                                   log=lambda _m: None, max_passes=3)
        self.assertEqual(code, 0)
        self.assertEqual(sleeps, [30, 30])
        self.assertEqual(len(self.store.lines(self.sid)), 3)

    def test_auto_detects_the_file_that_mentions_the_id(self):
        home = self.root / "home"
        claude = home / ".claude" / "projects" / "-work-repo"
        claude.mkdir(parents=True)
        (claude / "old.jsonl").write_text(jsonl({"type": "user", "message": {"content": "other"}}), encoding="utf-8")
        target = claude / "1458aee0.jsonl"
        target.write_text(jsonl({"type": "user", "message": {"content": f"session {self.sid}"}}), encoding="utf-8")
        codex = home / ".codex" / "sessions" / "2026" / "11" / "01"
        codex.mkdir(parents=True)
        (codex / "rollout-x.jsonl").write_text(jsonl({"type": "session_meta"}), encoding="utf-8")
        self.assertEqual(session_watch.find_session_file(self.sid, home=home, env={}), ("claude", target))
        self.assertIsNone(session_watch.find_session_file(self.sid, "codex", home=home, env={}))
        self.assertIsNone(session_watch.find_session_file("ark-ses-missing1", home=home, env={}))

    def test_cli_requires_token_from_environment(self):
        args = session_watch.build_parser().parse_args(["--id", self.sid, "--file", str(self.file), "--once"])
        messages = []
        self.assertEqual(session_watch.run(args, env={}, log=messages.append), 2)
        self.assertIn("ARK_TOKEN", messages[0])


if __name__ == "__main__":
    unittest.main()
