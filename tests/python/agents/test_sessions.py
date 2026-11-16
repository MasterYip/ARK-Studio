import json
import os
import sys
import tempfile
import time
import unittest
import urllib.error
import urllib.parse
import urllib.request
from pathlib import Path
from unittest import mock

from arkstudio.server import App, serve

FAKE_AGENT = r'''
import json
import os
import sys

prompt = sys.argv[1] if len(sys.argv) > 1 else ""
print("fake agent ready", flush=True)
print("[[ark-event]] " + json.dumps({"type": "tool_call", "id": "demo-ctx", "name": "read_context",
                                    "input": {"path": "ark-context.json"}}), flush=True)
print("[[ark-event]] " + json.dumps({"type": "tool_result", "id": "demo-ctx", "status": "ok",
                                    "output": {"units": 12}}), flush=True)
print("prompt=" + prompt, flush=True)
print("has_read_token=" + str(bool(os.environ.get("ARK_READ_TOKEN"))), flush=True)
print("write_env=" + os.environ.get("ARK_AGENT_TOKEN", ""), flush=True)
print("other_secret=" + os.environ.get("ARK_OTHER_SECRET", ""), flush=True)
print("read_only_env=" + os.environ.get("ARK_TASK_READ_ONLY", ""), flush=True)
print("access_mode=" + os.environ.get("ARK_TASK_ACCESS_MODE", ""), flush=True)
print("permission_mode_env=" + os.environ.get("ARK_TASK_PERMISSION_MODE", ""), flush=True)
print("plan_env=" + os.environ.get("ARK_TASK_PLAN_MODE", ""), flush=True)
print("approvals_env=" + os.environ.get("ARK_TASK_APPROVALS", ""), flush=True)
print("approval_mode_env=" + os.environ.get("ARK_TASK_APPROVAL_MODE", ""), flush=True)
print("effort_env=" + os.environ.get("ARK_TASK_EFFORT", ""), flush=True)
print("model_env=" + os.environ.get("ARK_TASK_MODEL", ""), flush=True)
print("project=" + os.environ.get("ARK_PROJECT_PATH", ""), flush=True)
print("context=" + os.environ.get("ARK_TASK_CONTEXT", ""), flush=True)
approval_serial = 0
for raw in sys.stdin:
    line = raw.rstrip("\r\n")
    if line == "quit":
        print("bye", flush=True)
        break
    if line.startswith("{"):
        try:
            frame = json.loads(line)
        except ValueError:
            frame = {}
        if isinstance(frame, dict) and frame.get("type") == "permission_response":
            print("approval_response=" + str(frame.get("decision")) + ":"
                  + str(frame.get("scope")) + ":" + str(frame.get("request_id")), flush=True)
            continue
    if line.startswith("permission "):
        approval_serial += 1
        kind = line.split(" ", 1)[1].strip() or "command"
        if kind == "edit":
            event = {"type": "permission_request", "id": f"req-{approval_serial}",
                     "kind": "file_edit", "tool": "apply_patch",
                     "path": "artifacts/report.md",
                     "description": "apply a demo patch"}
        else:
            event = {"type": "permission_request", "id": f"req-{approval_serial}",
                     "kind": "command", "tool": "bash",
                     "command": "echo approved",
                     "description": "run a demo command"}
        print("[[ark-event]] " + json.dumps(event), flush=True)
        continue
    if line.startswith("write "):
        _, name, text = line.split(" ", 2)
        os.makedirs("artifacts", exist_ok=True)
        with open(os.path.join("artifacts", os.path.basename(name)), "w", encoding="utf-8") as fh:
            fh.write(text)
        print("wrote " + name, flush=True)
        continue
    print("echo:" + line, flush=True)
'''


class SessionApiTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.tmp = tempfile.TemporaryDirectory()
        root = Path(cls.tmp.name)
        cls.fake = root / "fake_agent.py"
        cls.fake.write_text(FAKE_AGENT, encoding="utf-8")
        presets = [
            {
                "id": "fake",
                "label": "Fake agent",
                "description": "Injected test command; never hits a model.",
                "command": [sys.executable, "-u", str(cls.fake), "{prompt}"],
                "prompt_mode": "arg",
                "followup": True,
                "models": ["test-model", "test-model-2"],
                "efforts": ["low", "high"],
                "permission_modes": ["read_only", "plan", "ask", "accept_edits", "full_access"],
                "supports_approval": True,
            },
            {
                "id": "plain",
                "label": "Plain interactive",
                "description": "Keeps stdin open but cannot request approvals.",
                "command": [sys.executable, "-u", "-c",
                            "import time; print('plain ready', flush=True); time.sleep(30)"],
                "prompt_mode": "stdin",
                "followup": True,
                "models": ["m1"],
            },
            {
                "id": "oneshot",
                "label": "One-shot sleeper",
                "description": "Runs without stdin follow-up so steer must queue for the next turn.",
                "command": [sys.executable, "-u", "-c",
                            "import time; print('oneshot ready', flush=True); time.sleep(30)"],
                "prompt_mode": "stdin",
                "followup": False,
                "models": ["m1"],
                "efforts": ["low", "high"],
                "access_modes": ["read_only"],
            },
            {
                "id": "missing",
                "label": "Missing agent",
                "command": [str(root / "definitely-not-installed"), "{prompt}"],
                "prompt_mode": "arg",
                "followup": False,
            },
            {
                "id": "fail",
                "label": "Failing agent",
                "command": [sys.executable, "-c",
                            "import sys; print('about to fail', flush=True); sys.exit(3)"],
                "prompt_mode": "stdin",
                "followup": False,
            },
        ]
        cls.app = App(root / "projects", seed_demo=True, task_sessions_root=root / "agent-sessions",
                      task_presets=presets)
        cls.httpd = serve(cls.app, port=0, block=False)
        cls.base = f"http://127.0.0.1:{cls.httpd.server_address[1]}"

    @classmethod
    def tearDownClass(cls):
        cls._stop_everything()
        cls.httpd.shutdown()
        cls.httpd.server_close()
        cls.app.project.close()
        cls.tmp.cleanup()

    @classmethod
    def _stop_everything(cls):
        for item in cls.app.sessions.list()["sessions"]:
            if item["status"] in {"running", "idle", "starting", "stopping"}:
                try:
                    cls.app.sessions.request_stop(item["id"])
                except Exception:
                    pass
        deadline = time.time() + 10
        while time.time() < deadline:
            active = [s for s in cls.app.sessions.list()["sessions"]
                      if s["status"] in {"running", "idle", "starting", "stopping"}]
            if not active:
                break
            time.sleep(0.1)

    def tearDown(self):
        self._stop_everything()

    def request(self, path, *, method="GET", body=None, headers=None, raw=False):
        data = json.dumps(body).encode() if body is not None else None
        req = urllib.request.Request(self.base + path, data=data, method=method,
                                     headers={"Content-Type": "application/json", **(headers or {})})
        try:
            with urllib.request.urlopen(req, timeout=10) as response:
                payload = response.read()
                ctype = response.headers.get("Content-Type", "")
                if raw:
                    return response.status, payload, ctype
                if "json" in ctype:
                    return response.status, json.loads(payload)
                return response.status, payload.decode("utf-8", "replace")
        except urllib.error.HTTPError as exc:
            payload = exc.read()
            if raw:
                return exc.code, payload, exc.headers.get("Content-Type", "")
            try:
                return exc.code, json.loads(payload or b"{}")
            except ValueError:
                return exc.code, payload.decode("utf-8", "replace")

    def wait_for(self, predicate, message="condition", timeout=12):
        deadline = time.time() + timeout
        last = None
        while time.time() < deadline:
            last = predicate()
            if last:
                return last
            time.sleep(0.08)
        self.fail(f"timed out waiting for {message}; last={last!r}")

    def start_session(self, **body):
        payload = {"platform": "fake", "title": "Test session", "prompt": "start here"}
        payload.update(body)
        status, data = self.request("/api/agent-sessions", method="POST", body=payload)
        self.assertEqual(status, 200, data)
        return data["session"]

    def wait_ready(self, session_id):
        def check():
            status, data = self.request(
                f"/api/agent-sessions/{session_id}/logs?stream=stdout&offset=0&limit=200")
            if status != 200:
                return None
            lines = [entry.get("line") for entry in data.get("entries", [])]
            ready = any("fake agent ready" in (line or "") for line in lines)
            env_seen = any((line or "").startswith("context=") for line in lines)
            return lines if ready and env_seen else None
        return self.wait_for(check, "fake agent ready")

    def stdout_text(self, session_id):
        status, data = self.request(
            f"/api/agent-sessions/{session_id}/logs?stream=stdout&offset=0&limit=500")
        self.assertEqual(status, 200, data)
        return "\n".join(entry.get("line", "") for entry in data.get("entries", []))

    def session_detail(self, session_id):
        status, data = self.request(f"/api/agent-sessions/{session_id}")
        self.assertEqual(status, 200, data)
        return data["session"]

    def wait_output(self, session_id, needle, timeout=10):
        self.wait_for(lambda: needle in self.stdout_text(session_id), needle, timeout=timeout)
        return self.stdout_text(session_id)

    def wait_approval(self, session_id, status_value="pending", timeout=10):
        def check():
            for item in self.session_detail(session_id)["approvals"]["items"]:
                if item["status"] == status_value:
                    return item
            return None
        return self.wait_for(check, f"approval {status_value}")

    def stop_and_wait(self, session_id):
        status, _ = self.request(f"/api/agent-sessions/{session_id}/stop", method="POST", body={})
        self.assertEqual(status, 200)
        self.wait_for(lambda: self.session_detail(session_id)["status"] == "stopped",
                      "session stopped")

    def test_presets_and_lifecycle(self):
        status, data = self.request("/api/agent-sessions/presets")
        self.assertEqual(status, 200)
        presets = {p["id"]: p for p in data["presets"]}
        self.assertTrue(presets["fake"]["available"])
        self.assertFalse(presets["missing"]["available"])

        session = self.start_session(model="test-model", agent="tester", task="T-0001")
        self.assertEqual(session["status"], "running")
        self.assertIn("read_token", session)
        sid = session["id"]
        lines = self.wait_ready(sid)
        self.assertTrue(any("prompt=start here" in line for line in lines))
        self.assertTrue(any("has_read_token=True" in line for line in lines))

        status, listing = self.request("/api/agent-sessions")
        self.assertEqual(status, 200)
        row = next(item for item in listing["sessions"] if item["id"] == sid)
        self.assertNotIn("read_token", row)
        self.assertEqual(row["task_ref"], "T-0001")

        status, detail = self.request(f"/api/agent-sessions/{sid}")
        self.assertEqual(status, 200)
        self.assertTrue(detail["session"]["read_token"])
        self.assertEqual(detail["session"]["status"], "running")

        status, sent = self.request(f"/api/agent-sessions/{sid}/input", method="POST",
                                    body={"text": "do the thing"})
        self.assertEqual(status, 200, sent)
        self.wait_for(lambda: "echo:do the thing" in "\n".join(
            entry.get("line", "") for entry in self.request(
                f"/api/agent-sessions/{sid}/logs?stream=stdout&offset=0&limit=200")[1]["entries"]),
            "follow-up echo")

        status, stopped = self.request(f"/api/agent-sessions/{sid}/stop", method="POST", body={})
        self.assertEqual(status, 200)
        self.assertIn(stopped["session"]["status"], {"stopping", "stopped"})
        self.wait_for(lambda: next(
            (s for s in self.request("/api/agent-sessions")[1]["sessions"] if s["id"] == sid),
            {}).get("status") == "stopped", "stopped status")

        status, logs = self.request(f"/api/agent-sessions/{sid}/logs?stream=stdout&offset=0&limit=500")
        self.assertEqual(status, 200)
        self.assertGreater(len(logs["entries"]), 2)
        self.assertEqual(logs["offset"], len(logs["entries"]))
        status, again = self.request(
            f"/api/agent-sessions/{sid}/logs?stream=stdout&offset={logs['offset']}&limit=500")
        self.assertEqual(again["entries"], [])

    def test_read_only_task_agent_api(self):
        session = self.start_session(title="Read-only contract")
        self.wait_ready(session["id"])
        token = session["read_token"]
        sid = session["id"]

        status, ctx = self.request("/api/task-agent/v1/context",
                                   headers={"X-ARK-Read-Token": token})
        self.assertEqual(status, 200, ctx)
        self.assertTrue(ctx["read_only"])
        self.assertGreater(len(ctx["units"]), 10)
        self.assertIn("links", ctx)

        status, units = self.request("/api/task-agent/v1/units?types=task&limit=10",
                                     headers={"Authorization": f"Bearer {token}"})
        self.assertEqual(status, 200)
        self.assertTrue(all(item["type"] == "task" for item in units["items"]))

        status, snapshot = self.request("/api/task-agent/v1/snapshot",
                                        headers={"X-ARK-Read-Token": token})
        self.assertEqual(status, 200)
        self.assertEqual(snapshot["format"], "ark-task-agent-context")
        self.assertEqual(snapshot["session"]["id"], sid)

        before = self.request("/api/project")[1]["stats"]["total"]
        status, denied = self.request("/api/task-agent/v1/units", method="POST",
                                      headers={"X-ARK-Read-Token": token},
                                      body={"title": "should not exist", "type": "note"})
        self.assertEqual(status, 403, denied)
        after = self.request("/api/project")[1]["stats"]["total"]
        self.assertEqual(before, after)

        status, _ = self.request("/api/task-agent/v1/context",
                                 headers={"X-ARK-Read-Token": self.app.manager.agent_token})
        self.assertEqual(status, 401)
        status, _ = self.request("/api/agent/v1/context",
                                 headers={"Authorization": f"Bearer {token}"})
        self.assertEqual(status, 401)
        status, _ = self.request("/api/task-agent/v1/context")
        self.assertEqual(status, 401)

    def test_task_agent_reads_unit_comments(self):
        store = self.app.project.store
        unit = store.create_unit({"title": "Commented review", "type": "task", "status": "review"})
        comment = store.add_comment(unit["uid"], "use the seed-2 run", author="alice")
        session = self.start_session(title="Comment reader")
        self.wait_ready(session["id"])
        headers = {"X-ARK-Read-Token": session["read_token"]}

        status, data = self.request(f"/api/task-agent/v1/units/{unit['uid']}", headers=headers)
        self.assertEqual(status, 200, data)
        self.assertEqual(data["unit"]["comments_open"], 1)
        self.assertEqual(data["unit"]["comments"][0]["body"], "use the seed-2 run")
        status, data = self.request(f"/api/task-agent/v1/units/{unit['uid']}/comments", headers=headers)
        self.assertEqual((data["open_user_comments"], data["read_only"]), (1, True))
        status, ctx = self.request("/api/task-agent/v1/context", headers=headers)
        row = next(u for u in ctx["units"] if u["uid"] == unit["uid"])
        self.assertEqual(row["comments_open"], 1)
        self.assertEqual([c["id"] for c in row["open_comments"]], [comment["id"]])
        # Replies go through the agent write API; the read token stays read-only.
        status, _ = self.request(f"/api/task-agent/v1/units/{unit['uid']}/comments", method="POST",
                                 headers=headers, body={"body": "reply"})
        self.assertEqual(status, 403)
        self.assertEqual(len(store.comments(unit["uid"])), 1)

    def test_artifacts_are_scoped_and_traversals_denied(self):
        session = self.start_session(title="Artifacts")
        sid = session["id"]
        self.wait_ready(sid)
        status, _ = self.request(f"/api/agent-sessions/{sid}/input", method="POST",
                                 body={"text": "write note.md hello-artifact"})
        self.assertEqual(status, 200)
        self.wait_for(lambda: "wrote note.md" in "\n".join(
            entry.get("line", "") for entry in self.request(
                f"/api/agent-sessions/{sid}/logs?stream=stdout&offset=0&limit=200")[1]["entries"]),
            "artifact written")

        status, listing = self.request(f"/api/agent-sessions/{sid}/artifacts?path=artifacts")
        self.assertEqual(status, 200, listing)
        note = next(item for item in listing["entries"] if item["name"] == "note.md")
        self.assertEqual(note["type"], "file")
        self.assertEqual(note["path"], "artifacts/note.md")

        status, read = self.request(
            f"/api/agent-sessions/{sid}/artifacts/read?path={urllib.parse.quote('artifacts/note.md')}")
        self.assertEqual(status, 200, read)
        self.assertEqual(read["content"], "hello-artifact")

        status, raw, ctype = self.request(
            f"/api/agent-sessions/{sid}/artifacts/download?path={urllib.parse.quote('artifacts/note.md')}",
            raw=True)
        self.assertEqual(status, 200)
        self.assertEqual(raw.decode(), "hello-artifact")

        for bad in ("../meta.json", "artifacts/../../meta.json", "/etc/passwd",
                    "C:/Windows/win.ini", "..\\meta.json"):
            status, data = self.request(
                f"/api/agent-sessions/{sid}/artifacts/read?path={urllib.parse.quote(bad)}")
            self.assertIn(status, (400, 403), f"{bad} -> {status} {data}")

        status, _ = self.request(f"/api/agent-sessions/{sid}/artifacts?path=../")
        self.assertIn(status, (400, 403))

    def test_open_endpoint_scopes_artifacts_and_rejects_read_tokens(self):
        session = self.start_session(title="Open artifact")
        sid = session["id"]
        token = session["read_token"]
        self.wait_ready(sid)
        status, _ = self.request(f"/api/agent-sessions/{sid}/input", method="POST",
                                 body={"text": "write note.md hello-open"})
        self.assertEqual(status, 200)
        self.wait_for(lambda: "wrote note.md" in "\n".join(
            entry.get("line", "") for entry in self.request(
                f"/api/agent-sessions/{sid}/logs?stream=stdout&offset=0&limit=200")[1]["entries"]),
            "artifact written for open")
        expected = (self.app.sessions.get(sid).workspace / "artifacts" / "note.md").resolve()
        with mock.patch("arkstudio.server.platform_support.open_external",
                        return_value={"opened": True, "kind": "file",
                                      "target": str(expected), "platform": "test",
                                      "opener": "fake"}) as opener:
            status, data = self.request("/api/open", method="POST",
                                        body={"session": sid, "path": "artifacts/note.md"})
        self.assertEqual(status, 200, data)
        self.assertEqual(data["mode"], "session")
        opener.assert_called_once_with(expected)

        with mock.patch("arkstudio.server.platform_support.open_external") as opener:
            status, data = self.request(
                "/api/open", method="POST",
                headers={"X-ARK-Read-Token": token},
                body={"session": sid, "path": "artifacts/note.md"})
            self.assertEqual(status, 403, data)
            opener.assert_not_called()

        with mock.patch("arkstudio.server.platform_support.open_external") as opener:
            for bad in ("../meta.json", "artifacts/../../meta.json", "/etc/passwd"):
                status, data = self.request("/api/open", method="POST",
                                            body={"session": sid, "path": bad})
                self.assertIn(status, (400, 403), data)
            opener.assert_not_called()

    def test_environment_never_inherits_write_credentials(self):
        os.environ["ARK_AGENT_TOKEN"] = "write-secret-value"
        os.environ["ARK_OTHER_SECRET"] = "another-secret-value"
        try:
            session = self.start_session(title="Env scrub")
            lines = self.wait_ready(session["id"])
        finally:
            os.environ.pop("ARK_AGENT_TOKEN", None)
            os.environ.pop("ARK_OTHER_SECRET", None)
        joined = "\n".join(lines)
        self.assertNotIn("write-secret-value", joined)
        self.assertNotIn("another-secret-value", joined)
        self.assertIn("has_read_token=True", joined)
        self.assertIn("project=" + str(self.app.project.path), joined)
        self.assertIn("context=", joined)

    def test_idle_and_errored_statuses(self):
        session = self.start_session(title="Idle derivation")
        sid = session["id"]
        self.wait_ready(sid)
        managed = self.app.sessions.get(sid)
        with managed.lock:
            managed.meta["last_activity_at"] = "2026-01-01T00:00:00+00:00"
        self.app.sessions.save(managed)
        status, listing = self.request("/api/agent-sessions")
        self.assertEqual(status, 200)
        row = next(item for item in listing["sessions"] if item["id"] == sid)
        self.assertEqual(row["status"], "idle")
        self.assertEqual(row["raw_status"], "running")
        self.request(f"/api/agent-sessions/{sid}/stop", method="POST", body={})
        self.wait_for(lambda: next(
            (s for s in self.request("/api/agent-sessions")[1]["sessions"] if s["id"] == sid),
            {}).get("status") == "stopped", "idle session stopped")

        status, data = self.request("/api/agent-sessions", method="POST",
                                    body={"platform": "fail", "title": "Fails on purpose"})
        self.assertEqual(status, 200, data)
        failed_id = data["session"]["id"]
        self.wait_for(lambda: next(
            (s for s in self.request("/api/agent-sessions")[1]["sessions"] if s["id"] == failed_id),
            {}).get("status") == "errored", "errored status")
        status, detail = self.request(f"/api/agent-sessions/{failed_id}")
        self.assertEqual(detail["session"]["exit_code"], 3)
        self.assertIn("exited with code 3", detail["session"]["error"])

    def test_transcript_and_media_endpoints(self):
        session = self.start_session(title="Structured thread", prompt="Inspect the graph")
        sid = session["id"]
        self.wait_ready(sid)

        status, data = self.request(f"/api/agent-sessions/{sid}/transcript")
        self.assertEqual(status, 200, data)
        self.assertTrue(data["active"])
        self.assertEqual(data["status"], "running")
        self.assertEqual(data["stats"]["user_messages"], 1)
        self.assertEqual(data["items"][0]["role"], "user")
        self.assertEqual(data["items"][0]["text"], "Inspect the graph")
        tool = next(item for item in data["items"] if item["type"] == "tool")
        self.assertEqual(tool["name"], "read_context")
        self.assertEqual(tool["input"], {"path": "ark-context.json"})
        self.assertEqual(tool["output"], {"units": 12})
        self.assertEqual(tool["status"], "ok")
        self.assertTrue(data["signature"])

        workspace = self.app.sessions.get(sid).workspace
        (workspace / "artifacts").mkdir(parents=True, exist_ok=True)
        (workspace / "artifacts" / "preview.png").write_bytes(b"\x89PNG\r\n\x1a\n")
        (workspace / "artifacts" / "notes.md").write_text("# Notes", encoding="utf-8")
        status, media = self.request(f"/api/agent-sessions/{sid}/media")
        self.assertEqual(status, 200, media)
        self.assertEqual(media["session"], sid)
        image = next(item for item in media["media"] if item["name"] == "preview.png")
        self.assertEqual(image["kind"], "image")
        self.assertEqual(image["path"], "artifacts/preview.png")
        self.assertIn("artifacts/notes.md",
                      [item["path"] for item in media["documents"]])
        self.assertTrue(any(item["name"] == "ARK-TASK.md" for item in media["documents"]))

        self.request(f"/api/agent-sessions/{sid}/stop", method="POST", body={})
        self.wait_for(lambda: next(
            (s for s in self.request("/api/agent-sessions")[1]["sessions"] if s["id"] == sid),
            {}).get("status") == "stopped", "transcript session stopped")
        status, stopped = self.request(f"/api/agent-sessions/{sid}/transcript")
        self.assertEqual(status, 200)
        self.assertFalse(stopped["active"])

    def test_archive_delete_and_validation(self):
        session = self.start_session(title="Archive me")
        sid = session["id"]
        self.wait_ready(sid)
        status, _ = self.request(f"/api/agent-sessions/{sid}/archive", method="POST", body={})
        self.assertEqual(status, 409)

        self.request(f"/api/agent-sessions/{sid}/stop", method="POST", body={})
        self.wait_for(lambda: next(
            (s for s in self.request("/api/agent-sessions")[1]["sessions"] if s["id"] == sid),
            {}).get("status") == "stopped", "stopped before archive")
        status, archived = self.request(f"/api/agent-sessions/{sid}/archive", method="POST", body={})
        self.assertEqual(status, 200, archived)
        self.assertEqual(archived["session"]["status"], "archived")
        self.assertFalse(any(s["id"] == sid for s in self.request("/api/agent-sessions")[1]["sessions"]))
        self.assertTrue((self.app.sessions.archive_root / sid / "meta.json").is_file())

        other = self.start_session(title="Delete me")
        self.wait_ready(other["id"])
        self.request(f"/api/agent-sessions/{other['id']}/stop", method="POST", body={})
        self.wait_for(lambda: next(
            (s for s in self.request("/api/agent-sessions")[1]["sessions"] if s["id"] == other["id"]),
            {}).get("status") == "stopped", "stopped before delete")
        status, deleted = self.request(f"/api/agent-sessions/{other['id']}", method="DELETE")
        self.assertEqual(status, 200, deleted)
        self.assertFalse((self.app.sessions.root / other["id"]).exists())

        status, data = self.request("/api/agent-sessions", method="POST",
                                    body={"platform": "nope", "prompt": "x"})
        self.assertEqual(status, 400, data)
        status, data = self.request("/api/agent-sessions", method="POST",
                                    body={"platform": "missing", "prompt": "x"})
        self.assertEqual(status, 400)
        status, data = self.request("/api/agent-sessions", method="POST",
                                    body={"platform": "fake", "prompt": "x", "cwd": "../escape"})
        self.assertEqual(status, 400)
        status, data = self.request("/api/agent-sessions/not-a-session")
        self.assertEqual(status, 404)


    def test_steer_queue_interactive_and_one_shot(self):
        session = self.start_session(title="Steer interactive")
        sid = session["id"]
        self.wait_ready(sid)
        status, data = self.request(f"/api/agent-sessions/{sid}/steer", method="POST",
                                    body={"text": "adjust course"})
        self.assertEqual(status, 200, data)
        self.assertEqual(data["steer"]["status"], "delivered")
        self.assertEqual(data["steer"]["channel"], "stdin")
        self.assertTrue(data["steer"]["delivered_at"])
        self.assertEqual(data["session"]["steer_queue"][-1]["status"], "delivered")
        self.wait_for(lambda: "echo:adjust course" in self.stdout_text(sid), "steer echoed")
        status, detail = self.request(f"/api/agent-sessions/{sid}")
        self.assertEqual(detail["session"]["steer_queue"][-1]["text"], "adjust course")

        status, removed = self.request(
            f"/api/agent-sessions/{sid}/steer/{data['steer']['id']}", method="DELETE")
        self.assertEqual(status, 200, removed)
        self.assertEqual(removed["session"]["steer_queue"], [])
        status, data = self.request(f"/api/agent-sessions/{sid}/steer", method="POST",
                                    body={"text": "   "})
        self.assertEqual(status, 400, data)
        self.request(f"/api/agent-sessions/{sid}/stop", method="POST", body={})
        self.wait_for(lambda: next(
            (s for s in self.request("/api/agent-sessions")[1]["sessions"] if s["id"] == sid),
            {}).get("status") == "stopped", "steer session stopped")
        status, data = self.request(f"/api/agent-sessions/{sid}/steer", method="POST",
                                    body={"text": "too late"})
        self.assertEqual(status, 409, data)

        status, one = self.request("/api/agent-sessions", method="POST",
                                   body={"platform": "oneshot", "title": "Queued steer"})
        self.assertEqual(status, 200, one)
        one_id = one["session"]["id"]
        self.wait_for(lambda: "oneshot ready" in self.stdout_text(one_id), "one-shot ready")
        status, queued = self.request(f"/api/agent-sessions/{one_id}/steer", method="POST",
                                      body={"text": "next turn instruction"})
        self.assertEqual(status, 200, queued)
        self.assertEqual(queued["steer"]["status"], "queued")
        self.assertEqual(queued["steer"]["channel"], "next_turn")
        self.assertIsNone(queued["steer"]["delivered_at"])
        self.request(f"/api/agent-sessions/{one_id}/stop", method="POST", body={})
        self.wait_for(lambda: next(
            (s for s in self.request("/api/agent-sessions")[1]["sessions"] if s["id"] == one_id),
            {}).get("status") == "stopped", "one-shot stopped")
        status, continued = self.request(f"/api/agent-sessions/{one_id}/continue", method="POST",
                                         body={"text": "resume with the queued steer"})
        self.assertEqual(status, 200, continued)
        drained = continued["session"]["steer_queue"][-1]
        self.assertEqual(drained["status"], "delivered")
        self.assertEqual(drained["channel"], "continue")
        self.assertTrue(drained["delivered_at"])
        self.request(f"/api/agent-sessions/{one_id}/stop", method="POST", body={})

    def test_access_mode_is_enforced_and_reported(self):
        token = self.app.manager.agent_token
        status, data = self.request("/api/agent-sessions", method="POST",
                                    body={"platform": "fake", "prompt": "read only",
                                          "access_mode": "read_only"})
        self.assertEqual(status, 200, data)
        read_only_id = data["session"]["id"]
        self.assertEqual(data["session"]["access_mode"], "read_only")
        self.wait_ready(read_only_id)
        text = self.stdout_text(read_only_id)
        self.assertIn("write_env=\n", text.replace("\r", ""))
        self.assertIn("read_only_env=1", text)
        self.assertIn("access_mode=read_only", text)
        self.assertNotIn(token, text)

        status, data = self.request("/api/agent-sessions", method="POST",
                                    body={"platform": "fake", "prompt": "full access",
                                          "access_mode": "full_access",
                                          "effort": "high", "model": "test-model-2"})
        self.assertEqual(status, 200, data)
        full_id = data["session"]["id"]
        self.assertEqual(data["session"]["access_mode"], "full_access")
        self.assertEqual(data["session"]["effort"], "high")
        self.wait_ready(full_id)
        self.wait_for(lambda: token in self.stdout_text(full_id), "write token delivered")
        text = self.stdout_text(full_id)
        self.assertIn("access_mode=full_access", text)
        self.assertIn("effort_env=high", text)
        self.assertIn("model_env=test-model-2", text)
        self.assertNotIn("read_only_env=1", text)

        status, data = self.request("/api/agent-sessions", method="POST",
                                    body={"platform": "oneshot", "prompt": "no",
                                          "access_mode": "full_access"})
        self.assertEqual(status, 400, data)
        status, data = self.request("/api/agent-sessions", method="POST",
                                    body={"platform": "fake", "prompt": "x",
                                          "access_mode": "god_mode"})
        self.assertEqual(status, 400, data)
        status, data = self.request("/api/agent-sessions", method="POST",
                                    body={"platform": "fake", "prompt": "x",
                                          "effort": "impossible"})
        self.assertEqual(status, 400, data)

    def test_context_validation_and_prompt_delivery(self):
        session = self.start_session(title="Context", prompt="start with context")
        sid = session["id"]
        self.wait_ready(sid)
        workspace = self.app.sessions.get(sid).workspace
        (workspace / "artifacts").mkdir(parents=True, exist_ok=True)
        (workspace / "artifacts" / "ctx.py").write_text("print('ctx')\n", encoding="utf-8")
        (workspace / "artifacts" / "pic.png").write_bytes(b"\x89PNG\r\n\x1a\n")
        unit = self.app.project.store.list_units({"types": ["task"], "limit": 1})["items"][0]
        attachments = [
            {"kind": "unit", "uid": unit["uid"]},
            {"kind": "file", "path": "artifacts/ctx.py"},
            {"kind": "media", "path": "artifacts/pic.png"},
            {"kind": "text", "text": "Snippet for the agent", "label": "Note"},
        ]
        status, validated = self.request(
            f"/api/agent-sessions/{sid}/context/validate", method="POST",
            body={"context": attachments})
        self.assertEqual(status, 200, validated)
        self.assertEqual(validated["count"], 4)
        self.assertEqual([item["kind"] for item in validated["items"]],
                         ["unit", "file", "media", "text"])
        self.assertEqual(validated["items"][1]["path"], "artifacts/ctx.py")
        self.assertEqual(validated["items"][1]["label"], "artifacts/ctx.py")
        self.assertNotIn("text", validated["items"][0])

        for bad in ({"kind": "file", "path": "../meta.json"},
                    {"kind": "media", "path": "artifacts/../../meta.json"},
                    {"kind": "file", "path": "/etc/passwd"}):
            status, data = self.request(
                f"/api/agent-sessions/{sid}/context/validate", method="POST",
                body={"context": [bad]})
            self.assertIn(status, (400, 403, 404), f"{bad} -> {status} {data}")
        status, data = self.request(
            f"/api/agent-sessions/{sid}/context/validate", method="POST",
            body={"context": [{"kind": "unit", "uid": "NOPE-9999"}]})
        self.assertEqual(status, 404, data)
        status, data = self.request(
            f"/api/agent-sessions/{sid}/context/validate", method="POST",
            body={"context": [{"kind": "mystery", "text": "x"}]})
        self.assertEqual(status, 400, data)

        status, sent = self.request(f"/api/agent-sessions/{sid}/input", method="POST",
                                    body={"text": "use context",
                                          "context": [{"kind": "text",
                                                       "text": "CTX-DELIVERY-MARKER"}]})
        self.assertEqual(status, 200, sent)
        self.wait_for(lambda: "CTX-DELIVERY-MARKER" in self.stdout_text(sid),
                      "context text delivered")
        status, transcript = self.request(f"/api/agent-sessions/{sid}/transcript")
        joined = "\n".join(item.get("text", "") for item in transcript["items"]
                           if item.get("type") == "message")
        self.assertIn("context attached", joined)
        self.assertIn("use context", joined)

    def test_continue_applies_provider_and_access_overrides(self):
        session = self.start_session(title="Continue overrides")
        sid = session["id"]
        self.wait_ready(sid)
        self.request(f"/api/agent-sessions/{sid}/stop", method="POST", body={})
        self.wait_for(lambda: next(
            (s for s in self.request("/api/agent-sessions")[1]["sessions"] if s["id"] == sid),
            {}).get("status") == "stopped", "stopped before continue")
        status, data = self.request(f"/api/agent-sessions/{sid}/continue", method="POST",
                                    body={"text": "second pass", "platform": "fake",
                                          "model": "test-model-2", "effort": "high",
                                          "access_mode": "full_access"})
        self.assertEqual(status, 200, data)
        self.assertEqual(data["session"]["access_mode"], "full_access")
        self.assertEqual(data["session"]["effort"], "high")
        self.assertEqual(data["session"]["model"], "test-model-2")
        token = self.app.manager.agent_token
        self.wait_for(lambda: token in self.stdout_text(sid), "override write token")

        status, one = self.request("/api/agent-sessions", method="POST",
                                   body={"platform": "oneshot", "title": "Restricted"})
        one_id = one["session"]["id"]
        self.wait_for(lambda: "oneshot ready" in self.stdout_text(one_id), "one-shot ready")
        self.request(f"/api/agent-sessions/{one_id}/stop", method="POST", body={})
        self.wait_for(lambda: next(
            (s for s in self.request("/api/agent-sessions")[1]["sessions"] if s["id"] == one_id),
            {}).get("status") == "stopped", "restricted stopped")
        status, data = self.request(f"/api/agent-sessions/{one_id}/continue", method="POST",
                                    body={"text": "escalate", "access_mode": "full_access"})
        self.assertEqual(status, 400, data)

    def test_artifact_file_metadata_for_tabs(self):
        session = self.start_session(title="File tabs")
        sid = session["id"]
        self.wait_ready(sid)
        workspace = self.app.sessions.get(sid).workspace
        (workspace / "artifacts").mkdir(parents=True, exist_ok=True)
        (workspace / "artifacts" / "script.py").write_text("print('你好')\n", encoding="utf-8")
        (workspace / "artifacts" / "readme.md").write_text("# 标题\n", encoding="utf-8")
        status, listing = self.request(f"/api/agent-sessions/{sid}/artifacts?path=artifacts")
        self.assertEqual(status, 200, listing)
        by_name = {entry["name"]: entry for entry in listing["entries"]}
        self.assertEqual(by_name["script.py"]["language"], "python")
        self.assertEqual(by_name["readme.md"]["language"], "markdown")
        status, read = self.request(
            f"/api/agent-sessions/{sid}/artifacts/read?path={urllib.parse.quote('artifacts/script.py')}")
        self.assertEqual(status, 200, read)
        self.assertEqual(read["language"], "python")
        self.assertEqual(read["lines"], 1)
        self.assertIn("你好", read["content"])

    def test_permission_modes_and_capabilities(self):
        status, data = self.request("/api/agent-sessions/presets")
        self.assertEqual(status, 200)
        presets = {preset["id"]: preset for preset in data["presets"]}
        fake = presets["fake"]
        self.assertEqual(fake["permission_modes"],
                         ["read_only", "plan", "ask", "accept_edits", "full_access"])
        self.assertTrue(fake["supports_approval"])
        self.assertEqual(fake["approval_modes"], ["ask", "accept_edits"])
        self.assertEqual(fake["approval_protocol"], "ark-permission-v1")
        self.assertIn("models", fake)
        self.assertIn("efforts", fake)

        plain = presets["plain"]
        self.assertFalse(plain["supports_approval"])
        self.assertEqual(plain["permission_modes"], ["read_only", "plan", "full_access"])
        self.assertEqual(plain["unsupported_permission_modes"], ["ask", "accept_edits"])
        self.assertEqual(plain["approval_modes"], [])

        session = self.start_session(title="Ask mode", access_mode="ask")
        sid = session["id"]
        self.assertEqual(session["access_mode"], "ask")
        self.assertEqual(session["permission_mode"], "ask")
        self.assertEqual(session["permission_label"], "ask to write")
        self.assertEqual(session["turn"], 1)
        self.assertEqual(session["permission_history"][0]["mode"], "ask")
        self.assertEqual(session["capabilities"]["supports_approval"], True)
        self.assertEqual(session["capabilities"]["approval_modes"], ["ask", "accept_edits"])
        self.wait_ready(sid)
        text = self.stdout_text(sid)
        self.assertIn("access_mode=ask", text)
        self.assertIn("permission_mode_env=ask", text)
        self.assertIn("approvals_env=1", text)
        self.assertIn("approval_mode_env=all", text)
        self.assertIn("read_only_env=0", text)
        self.assertNotIn(self.app.manager.agent_token, text)

        status, data = self.request("/api/agent-sessions", method="POST",
                                    body={"platform": "fake", "prompt": "x",
                                          "access_mode": "bogus"})
        self.assertEqual(status, 400, data)
        status, data = self.request("/api/agent-sessions", method="POST",
                                    body={"platform": "plain", "prompt": "x",
                                          "access_mode": "ask"})
        self.assertEqual(status, 400, data)
        self.assertIn("approval", str(data).lower())

        plan = self.start_session(title="Plan mode", access_mode="plan")
        plan_id = plan["id"]
        self.assertEqual(plan["access_mode"], "plan")
        self.wait_ready(plan_id)
        plan_text = self.stdout_text(plan_id)
        self.assertIn("plan_env=1", plan_text)
        self.assertIn("read_only_env=1", plan_text)
        self.assertNotIn(self.app.manager.agent_token, plan_text)

        self.stop_and_wait(sid)
        status, continued = self.request(f"/api/agent-sessions/{sid}/continue", method="POST",
                                         body={"text": "next turn", "access_mode": "accept_edits"})
        self.assertEqual(status, 200, continued)
        payload = continued["session"]
        self.assertEqual(payload["access_mode"], "accept_edits")
        self.assertEqual(payload["turn"], 2)
        self.assertEqual([entry["mode"] for entry in payload["permission_history"]],
                         ["ask", "accept_edits"])
        self.wait_output(sid, "approval_mode_env=edits")
        text = self.stdout_text(sid)
        self.assertIn("permission_mode_env=accept_edits", text)

        self.stop_and_wait(plan_id)

    def test_approval_allow_deny_always_and_unknown(self):
        session = self.start_session(title="Approval flow", access_mode="ask")
        sid = session["id"]
        self.wait_ready(sid)
        status, approvals = self.request(f"/api/agent-sessions/{sid}/approvals")
        self.assertEqual(status, 200, approvals)
        self.assertEqual(approvals["pending"], 0)
        self.assertEqual(approvals["protocol"], "ark-permission-v1")

        status, _ = self.request(f"/api/agent-sessions/{sid}/input", method="POST",
                                 body={"text": "permission command"})
        self.assertEqual(status, 200)
        first = self.wait_approval(sid)
        self.assertEqual(first["kind"], "command")
        self.assertEqual(first["command"], "echo approved")
        self.assertEqual(first["tool"], "bash")
        self.assertTrue(first["pending"])
        self.assertEqual(first["mode"], "ask")

        status, data = self.request(
            f"/api/agent-sessions/{sid}/approvals/{first['id']}", method="POST",
            headers={"X-ARK-Read-Token": session["read_token"]},
            body={"decision": "allow"})
        self.assertEqual(status, 403, data)
        self.assertEqual(self.session_detail(sid)["approvals"]["pending"], 1)

        status, transcript = self.request(f"/api/agent-sessions/{sid}/transcript")
        self.assertEqual(status, 200)
        permission_item = next(item for item in transcript["items"]
                               if item["type"] == "permission")
        self.assertEqual(permission_item["request_id"], first["id"])
        self.assertEqual(permission_item["status"], "pending")
        self.assertEqual(permission_item["command"], "echo approved")
        self.assertEqual(transcript["stats"]["permissions"], 1)
        self.assertEqual(transcript["stats"]["pending_permissions"], 1)

        status, decided = self.request(
            f"/api/agent-sessions/{sid}/approvals/{first['id']}", method="POST",
            body={"decision": "allow"})
        self.assertEqual(status, 200, decided)
        self.assertEqual(decided["approval"]["status"], "allowed")
        self.assertEqual(decided["approval"]["scope"], "once")
        self.assertEqual(decided["approval"]["decision"], "allow")
        self.wait_output(sid, f"approval_response=allow:once:{first['id']}")
        self.assertEqual(self.session_detail(sid)["approvals"]["pending"], 0)

        status, _ = self.request(f"/api/agent-sessions/{sid}/input", method="POST",
                                 body={"text": "permission command"})
        self.assertEqual(status, 200)
        second = self.wait_approval(sid)
        self.assertNotEqual(second["id"], first["id"])
        status, decided = self.request(
            f"/api/agent-sessions/{sid}/approvals/{second['id']}", method="POST",
            body={"decision": "always"})
        self.assertEqual(status, 200, decided)
        self.assertEqual(decided["approval"]["scope"], "always")
        rules = self.session_detail(sid)["approvals"]["rules"]
        self.assertEqual(len(rules), 1)
        self.assertEqual(rules[0]["kind"], "command")
        self.assertEqual(rules[0]["tool"], "bash")
        self.wait_output(sid, f"approval_response=allow:always:{second['id']}")

        status, _ = self.request(f"/api/agent-sessions/{sid}/input", method="POST",
                                 body={"text": "permission command"})
        self.assertEqual(status, 200)

        def auto_allowed():
            items = self.session_detail(sid)["approvals"]["items"]
            for item in items:
                if item["id"] == "req-3" and item["status"] == "allowed":
                    return item
            return None
        auto = self.wait_for(auto_allowed, "always-rule auto approval")
        self.assertTrue(auto["auto"])
        self.wait_output(sid, "approval_response=allow:always:req-3")

        status, _ = self.request(f"/api/agent-sessions/{sid}/input", method="POST",
                                 body={"text": "permission edit"})
        self.assertEqual(status, 200)
        denied = self.wait_approval(sid)
        self.assertEqual(denied["kind"], "file_edit")
        self.assertEqual(denied["path"], "artifacts/report.md")
        status, data = self.request(
            f"/api/agent-sessions/{sid}/approvals/{denied['id']}", method="POST",
            body={"decision": "maybe"})
        self.assertEqual(status, 400, data)
        status, decided = self.request(
            f"/api/agent-sessions/{sid}/approvals/{denied['id']}", method="POST",
            body={"decision": "deny", "message": "not this file"})
        self.assertEqual(status, 200, decided)
        self.assertEqual(decided["approval"]["status"], "denied")
        self.wait_output(sid, f"approval_response=deny:once:{denied['id']}")

        status, transcript = self.request(f"/api/agent-sessions/{sid}/transcript")
        statuses = {item["request_id"]: item["status"] for item in transcript["items"]
                    if item["type"] == "permission"}
        self.assertEqual(statuses[first["id"]], "allowed")
        self.assertEqual(statuses[second["id"]], "allowed")
        self.assertEqual(statuses[denied["id"]], "denied")
        self.assertEqual(transcript["stats"]["permissions"], 4)
        self.assertEqual(transcript["stats"]["pending_permissions"], 0)

        status, data = self.request(
            f"/api/agent-sessions/{sid}/approvals/not-a-request", method="POST",
            body={"decision": "allow"})
        self.assertEqual(status, 404, data)
        status, data = self.request(
            f"/api/agent-sessions/{sid}/approvals/{first['id']}", method="POST",
            body={"decision": "allow"})
        self.assertEqual(status, 409, data)

        self.stop_and_wait(sid)

    def test_approval_mode_policies_and_expiry(self):
        read_only = self.start_session(title="Read-only approvals", access_mode="read_only")
        sid = read_only["id"]
        self.wait_ready(sid)
        self.request(f"/api/agent-sessions/{sid}/input", method="POST",
                     body={"text": "permission command"})
        denied = self.wait_approval(sid, "denied")
        self.assertTrue(denied["auto"])
        self.assertEqual(denied["scope"], "auto")
        self.assertEqual(denied["decision"], "deny")
        self.assertIn("read-only", denied["message"])
        self.wait_output(sid, f"approval_response=deny:auto:{denied['id']}")
        self.assertEqual(self.session_detail(sid)["approvals"]["pending"], 0)
        self.stop_and_wait(sid)

        accept = self.start_session(title="Accept edits", access_mode="accept_edits")
        accept_id = accept["id"]
        self.wait_ready(accept_id)
        self.request(f"/api/agent-sessions/{accept_id}/input", method="POST",
                     body={"text": "permission edit"})
        auto = self.wait_approval(accept_id, "allowed")
        self.assertTrue(auto["auto"])
        self.assertEqual(auto["kind"], "file_edit")
        self.assertEqual(auto["scope"], "auto")
        self.wait_output(accept_id, f"approval_response=allow:auto:{auto['id']}")
        self.request(f"/api/agent-sessions/{accept_id}/input", method="POST",
                     body={"text": "permission command"})
        pending = self.wait_approval(accept_id)
        self.assertEqual(pending["kind"], "command")
        self.stop_and_wait(accept_id)

        full = self.start_session(title="Full approvals", access_mode="full_access")
        full_id = full["id"]
        self.wait_ready(full_id)
        self.request(f"/api/agent-sessions/{full_id}/input", method="POST",
                     body={"text": "permission command"})
        allowed = self.wait_approval(full_id, "allowed")
        self.assertTrue(allowed["auto"])
        self.wait_output(full_id, f"approval_response=allow:auto:{allowed['id']}")
        self.stop_and_wait(full_id)

        expired = self.start_session(title="Expired approval", access_mode="ask")
        expired_id = expired["id"]
        self.wait_ready(expired_id)
        self.request(f"/api/agent-sessions/{expired_id}/input", method="POST",
                     body={"text": "permission command"})
        request = self.wait_approval(expired_id)
        self.stop_and_wait(expired_id)
        detail = self.session_detail(expired_id)
        stored = next(item for item in detail["approvals"]["items"] if item["id"] == request["id"])
        self.assertEqual(stored["status"], "expired")
        self.assertIn("process", stored["message"])
        status, data = self.request(
            f"/api/agent-sessions/{expired_id}/approvals/{request['id']}", method="POST",
            body={"decision": "allow"})
        self.assertEqual(status, 409, data)

    def test_steer_queue_edit_reorder_and_remove(self):
        session = self.start_session(title="Queued edits", platform="oneshot")
        sid = session["id"]
        self.wait_for(lambda: "oneshot ready" in self.stdout_text(sid), "one-shot ready")
        status, first = self.request(f"/api/agent-sessions/{sid}/steer", method="POST",
                                     body={"text": "first queued"})
        self.assertEqual(status, 200, first)
        status, second = self.request(f"/api/agent-sessions/{sid}/steer", method="POST",
                                      body={"text": "second queued"})
        self.assertEqual(status, 200, second)
        first_id = first["steer"]["id"]
        second_id = second["steer"]["id"]
        self.assertEqual([item["status"] for item in second["session"]["steer_queue"]],
                         ["queued", "queued"])

        status, edited = self.request(
            f"/api/agent-sessions/{sid}/steer/{first_id}", method="PATCH",
            body={"text": "first edited"})
        self.assertEqual(status, 200, edited)
        self.assertEqual(edited["session"]["steer_queue"][0]["text"], "first edited")
        self.assertEqual(edited["session"]["steer_queue"][0]["status"], "queued")
        self.assertTrue(edited["session"]["steer_queue"][0]["edited_at"])

        status, reordered = self.request(
            f"/api/agent-sessions/{sid}/steer/reorder", method="POST",
            body={"order": [second_id, first_id]})
        self.assertEqual(status, 200, reordered)
        self.assertEqual([item["id"] for item in reordered["session"]["steer_queue"]],
                         [second_id, first_id])
        status, bad = self.request(
            f"/api/agent-sessions/{sid}/steer/reorder", method="POST",
            body={"order": [first_id]})
        self.assertEqual(status, 400, bad)
        status, bad = self.request(
            f"/api/agent-sessions/{sid}/steer/reorder", method="POST",
            body={"order": [first_id, first_id]})
        self.assertEqual(status, 400, bad)
        status, bad = self.request(
            f"/api/agent-sessions/{sid}/steer/{first_id}", method="PATCH",
            body={"text": "   "})
        self.assertEqual(status, 400, bad)
        status, bad = self.request(
            f"/api/agent-sessions/{sid}/steer/st_missing", method="PATCH",
            body={"text": "x"})
        self.assertEqual(status, 404, bad)

        status, removed = self.request(
            f"/api/agent-sessions/{sid}/steer/{second_id}", method="DELETE")
        self.assertEqual(status, 200, removed)
        self.assertEqual([item["id"] for item in removed["session"]["steer_queue"]], [first_id])

        interactive = self.start_session(title="Delivered steer edit")
        interactive_id = interactive["id"]
        self.wait_ready(interactive_id)
        status, delivered = self.request(
            f"/api/agent-sessions/{interactive_id}/steer", method="POST",
            body={"text": "already on stdin"})
        self.assertEqual(status, 200, delivered)
        self.assertEqual(delivered["steer"]["status"], "delivered")
        status, data = self.request(
            f"/api/agent-sessions/{interactive_id}/steer/{delivered['steer']['id']}",
            method="PATCH", body={"text": "too late"})
        self.assertEqual(status, 409, data)
        self.stop_and_wait(interactive_id)

    def test_side_chat_fork_linkage(self):
        parent = self.start_session(title="Parent session", access_mode="read_only")
        parent_id = parent["id"]
        self.wait_ready(parent_id)
        status, _ = self.request(f"/api/agent-sessions/{parent_id}/input", method="POST",
                                 body={"text": "parent context here"})
        self.assertEqual(status, 200)
        self.wait_output(parent_id, "echo:parent context here")

        status, forged = self.request(f"/api/agent-sessions/{parent_id}/fork", method="POST",
                                      body={"prompt": "Explore the parent conclusion",
                                            "access_mode": "read_only"})
        self.assertEqual(status, 200, forged)
        child = forged["session"]
        self.assertEqual(child["parent_id"], parent_id)
        self.assertEqual(child["side_chat_of"], parent_id)
        self.assertEqual(child["forked_from"], parent_id)
        self.assertEqual(child["relation"], "side_chat")
        self.assertEqual(child["access_mode"], "read_only")
        self.assertEqual(child["depth"], 1)
        self.assertIn("Side chat context", child["prompt"])
        self.assertIn(parent_id, child["prompt"])
        self.assertIn("Explore the parent conclusion", child["prompt"])
        self.assertIn("Recent parent conversation", child["prompt"])
        self.assertIn("parent context here", child["prompt"])
        self.wait_for(lambda: "fake agent ready" in self.stdout_text(child["id"]),
                      "side chat started")
        listing = self.request("/api/agent-sessions")[1]["sessions"]
        row = next(item for item in listing if item["id"] == child["id"])
        self.assertEqual(row["parent_id"], parent_id)
        self.assertEqual(row["relation"], "side_chat")

        status, data = self.request(f"/api/agent-sessions/{parent_id}/fork", method="POST",
                                    body={"prompt": "  "})
        self.assertEqual(status, 400, data)
        status, data = self.request("/api/agent-sessions/s_doesnotexist/fork", method="POST",
                                    body={"prompt": "x"})
        self.assertEqual(status, 404, data)
        status, data = self.request(f"/api/agent-sessions/{parent_id}/fork", method="POST",
                                    body={"prompt": "x", "mode": "bogus"})
        self.assertEqual(status, 400, data)

        one_shot = self.start_session(title="Restricted parent", platform="oneshot")
        status, data = self.request(f"/api/agent-sessions/{one_shot['id']}/fork", method="POST",
                                    body={"prompt": "x", "access_mode": "full_access"})
        self.assertEqual(status, 400, data)
        self.stop_and_wait(parent_id)


if __name__ == "__main__":
    unittest.main()
