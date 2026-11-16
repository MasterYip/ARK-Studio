"""HTTP API for session context: consent, upload, listing, continue and export."""
import io
import json
import os
import tempfile
import time
import unittest
import urllib.error
import urllib.request
import zipfile
from pathlib import Path

from arkstudio.server import App, serve
from arkstudio.sessions import default_presets
from tests.python.agents.test_session_context import CLAUDE_LINES, fake_claude_preset, jsonl


class SessionContextApiTests(unittest.TestCase):
    def setUp(self):
        self._env = {key: os.environ.get(key) for key in ("ARK_CHAT_MODE", "ARK_BLACKBOX")}
        os.environ["ARK_CHAT_MODE"] = "local"
        os.environ["ARK_BLACKBOX"] = "0"
        self.tmp = tempfile.TemporaryDirectory()
        root = Path(self.tmp.name)
        presets = [p for p in default_presets() if p["id"] == "echo"] + [fake_claude_preset(root)]
        self.app = App(root / "data" / "projects", seed_demo=False,
                       task_sessions_root=root / "sessions", task_presets=presets)
        self.httpd = serve(self.app, port=0, block=False)
        self.base = f"http://127.0.0.1:{self.httpd.server_address[1]}"
        self.token = self.app.manager.agent_token
        self.sid = "ark-ses-api0001"

    def tearDown(self):
        for session in list(self.app.sessions._sessions.values()):
            if session.proc is not None:
                try:
                    if session.is_active:
                        self.app.sessions.request_stop(session.id)
                    session.proc.wait(timeout=20)
                except Exception:  # noqa: BLE001
                    pass
        self.httpd.shutdown()
        self.httpd.server_close()
        self.app.workspace.shutdown()
        self.app.shutdown()
        self.tmp.cleanup()
        for key, value in self._env.items():
            if value is None:
                os.environ.pop(key, None)
            else:
                os.environ[key] = value

    def request(self, path, *, method="GET", body=None, token=None, raw=False):
        headers = {"Content-Type": "application/json", "Origin": self.base}
        if token:
            headers["Authorization"] = f"Bearer {token}"
        data = json.dumps(body).encode() if body is not None else None
        req = urllib.request.Request(self.base + path, data=data, method=method, headers=headers)
        try:
            with urllib.request.urlopen(req, timeout=20) as response:
                payload = response.read()
                return response.status, (payload if raw else json.loads(payload or b"{}"))
        except urllib.error.HTTPError as exc:
            return exc.code, json.loads(exc.read() or b"{}")

    def upload(self, content, offset=0, sid=None, **extra):
        return self.request(f"/api/agent/v1/sessions/{sid or self.sid}/upload", method="POST",
                            token=self.token, body={"offset": offset, "content": content, **extra})

    def enable(self, value=True):
        status, data = self.request("/api/project/settings", method="PATCH",
                                    body={"sessions": {"accept_uploads": value}})
        self.assertEqual(status, 200, data)
        self.assertEqual(data["settings"]["sessions"], {"accept_uploads": value})

    def wait_done(self, session_id):
        deadline = time.time() + 20
        while time.time() < deadline:
            session = self.app.sessions.get(session_id)
            if not session.is_active and session.meta.get("status") in {"stopped", "errored"}:
                return session
            time.sleep(0.1)
        self.fail("session did not finish")

    def test_consent_auth_and_upload_flow(self):
        content = jsonl(*CLAUDE_LINES)
        status, data = self.request(f"/api/agent/v1/sessions/{self.sid}/upload", method="POST",
                                    body={"offset": 0, "content": content})
        self.assertEqual(status, 401)
        status, data = self.upload(content)
        self.assertEqual(status, 403)
        self.assertEqual(data["error"]["code"], "SESSION_UPLOADS_DISABLED")
        project_dir = self.app.require().path
        self.assertFalse((project_dir / "sessions").exists())
        status, state = self.request(f"/api/agent/v1/sessions/{self.sid}", token=self.token)
        self.assertEqual((status, state["uploads_enabled"], state["exists"]), (200, False, False))

        self.enable()
        status, data = self.upload(content)
        self.assertEqual(status, 200, data)
        self.assertEqual(data["received_bytes"], len(content.encode()))
        self.assertEqual(data["session"]["agent"], "claude")
        status, again = self.upload(content)
        self.assertTrue(again["duplicate"])
        status, data = self.upload(jsonl({"x": 1}), offset=3)
        self.assertEqual((status, data["error"]["code"]), (409, "SESSION_OFFSET_MISMATCH"))
        status, data = self.upload("garbage\n", offset=len(content.encode()))
        self.assertEqual((status, data["error"]["code"]), (400, "SESSION_LINE_NOT_JSON"))
        status, state = self.request(f"/api/agent/v1/sessions/{self.sid}", token=self.token)
        self.assertEqual(state["received_bytes"], len(content.encode()))
        self.assertTrue((project_dir / "sessions" / f"{self.sid}.jsonl").is_file())

        status, listing = self.request("/api/session-contexts")
        self.assertEqual(status, 200)
        self.assertEqual(listing["settings"], {"accept_uploads": True})
        item = next(i for i in listing["sessions"] if i["id"] == self.sid)
        self.assertEqual(item["source"], "external")
        self.assertIn("claude --resume 1458aee0-c3d7-4749", item["resume"]["command"])
        self.assertGreater(listing["usage"]["bytes"], 0)

        status, transcript = self.request(f"/api/session-contexts/{self.sid}/transcript")
        self.assertEqual(status, 200)
        self.assertGreaterEqual(transcript["stats"]["messages"], 2)
        status, ctx = self.request(f"/api/session-contexts/{self.sid}/context")
        self.assertIn("Plan the ablation", ctx["summary"])
        status, raw = self.request(f"/api/session-contexts/{self.sid}/raw", raw=True)
        self.assertEqual(len(raw.decode().splitlines()), len(CLAUDE_LINES))

        status, data = self.request(f"/api/session-contexts/{self.sid}", method="PATCH",
                                    body={"title": "Ablation", "linked_units": ["T-0001"]})
        self.assertEqual((status, data["session"]["title"], data["session"]["linked_units"]),
                         (200, "Ablation", ["T-0001"]))

        # Turning consent off again rejects further uploads.
        self.enable(False)
        status, data = self.upload(jsonl({"x": 1}), offset=len(content.encode()))
        self.assertEqual(status, 403)

        status, data = self.request(f"/api/session-contexts/{self.sid}", method="DELETE")
        self.assertEqual((status, data["deleted"]), (200, self.sid))
        self.assertFalse((project_dir / "sessions" / f"{self.sid}.jsonl").exists())
        status, data = self.request(f"/api/session-contexts/{self.sid}")
        self.assertEqual(status, 404)
        status, data = self.request("/api/session-contexts/..%2Fproject.json")
        self.assertIn(status, {400, 404})

    def test_consent_setting_is_strict_boolean_and_defaults_off(self):
        status, data = self.request("/api/project/settings")
        self.assertEqual(data["settings"]["sessions"], {"accept_uploads": False})
        for bad in ({"accept_uploads": "yes"}, {"other": True}, []):
            with self.subTest(bad=bad):
                status, _ = self.request("/api/project/settings", method="PATCH", body={"sessions": bad})
                self.assertEqual(status, 400)

    def test_watcher_download_requires_token(self):
        status, _ = self.request("/api/agent/v1/sessions/watcher.py")
        self.assertEqual(status, 401)
        status, source = self.request("/api/agent/v1/sessions/watcher.py", token=self.token, raw=True)
        self.assertEqual(status, 200)
        self.assertIn(b"def sync_once", source)

    def test_continue_external_seeds_a_new_internal_session(self):
        self.enable()
        self.assertEqual(self.upload(jsonl(*CLAUDE_LINES))[0], 200)
        status, data = self.request(f"/api/session-contexts/{self.sid}/continue", method="POST",
                                    body={"mode": "resume", "text": "go"})
        self.assertEqual((status, data["error"]["code"]), (409, "SESSION_NOT_RESUMABLE"))
        status, data = self.request(f"/api/session-contexts/{self.sid}/continue", method="POST",
                                    body={"mode": "seed", "platform": "echo", "text": "Carry on"})
        self.assertEqual(status, 200, data)
        self.assertEqual(data["mode"], "seed")
        new_id = data["session"]["id"]
        self.assertIn("Context from ARK session `ark-ses-api0001`", data["session"]["prompt"])
        self.assertTrue(data["session"]["prompt"].startswith("Carry on"))
        self.assertEqual(self.app.sessions.get(new_id).meta["continued_from"], self.sid)
        status, listing = self.request("/api/session-contexts")
        sources = {item["id"]: item["source"] for item in listing["sessions"]}
        self.assertEqual(sources[new_id], "internal")
        self.assertEqual(sources[self.sid], "external")

    def test_continue_internal_resumes_natively(self):
        status, data = self.request("/api/agent-sessions", method="POST",
                                    body={"platform": "claude", "prompt": "first"})
        self.assertEqual(status, 200, data)
        sid = data["session"]["id"]
        self.wait_done(sid)
        status, record = self.request(f"/api/session-contexts/{sid}")
        self.assertEqual(record["session"]["native_session_id"], "native-1234abcd")
        self.assertTrue(record["session"]["resume"]["native"])
        status, data = self.request(f"/api/session-contexts/{sid}/continue", method="POST",
                                    body={"text": "next step"})
        self.assertEqual((status, data["mode"]), (200, "resume"), data)
        session = self.wait_done(sid)
        lines = [entry["line"] for entry in self.app.sessions._all_log_entries(session, "stdout")]
        self.assertIn('ARGV ["--resume", "native-1234abcd", "-p", "next step"]', lines)
        status, data = self.request(f"/api/session-contexts/{sid}", method="PATCH",
                                    body={"title": "Renamed", "linked_units": ["T-0002"]})
        self.assertEqual((data["session"]["title"], data["session"]["linked_units"]), ("Renamed", ["T-0002"]))

    def test_internal_transcripts_are_mirrored_into_the_project(self):
        status, data = self.request("/api/agent-sessions", method="POST",
                                    body={"platform": "claude", "prompt": "mirror me sk-ant-api03-MIRRORSECRET12345"})
        self.assertEqual(status, 200, data)
        sid = data["session"]["id"]
        self.wait_done(sid)
        mirror = self.app.require().path / "sessions" / "internal" / sid
        for _ in range(50):
            if (mirror / "stdout.jsonl").is_file():
                break
            time.sleep(0.1)
        meta = json.loads((mirror / "meta.json").read_text(encoding="utf-8"))
        self.assertEqual(meta["id"], sid)
        self.assertNotIn("read_token", meta)
        self.assertNotIn("read_env", meta)
        text = "".join(path.read_text(encoding="utf-8") for path in mirror.iterdir())
        self.assertNotIn("MIRRORSECRET", text)
        self.assertIn("[REDACTED:anthropic_key]", (mirror / "stdout.jsonl").read_text(encoding="utf-8"))
        status, raw = self.request(f"/api/session-contexts/{sid}/raw", raw=True)
        self.assertNotIn(b"MIRRORSECRET", raw)
        # Rename/share updates the mirror; the runtime copy stays the live source.
        self.request(f"/api/session-contexts/{sid}", method="PATCH", body={"title": "Mirrored", "shared": True})
        meta = json.loads((mirror / "meta.json").read_text(encoding="utf-8"))
        self.assertEqual((meta["title"], meta["shared"]), ("Mirrored", True))
        # Without the runtime copy (e.g. a copied project) the mirror is listed and readable.
        self.app.sessions.archive(sid)
        status, listing = self.request("/api/session-contexts")
        item = next(i for i in listing["sessions"] if i["id"] == sid)
        self.assertEqual((item["source"], item["copy"], item["title"]), ("internal", True, "Mirrored"))
        status, transcript = self.request(f"/api/session-contexts/{sid}/transcript")
        self.assertEqual(status, 200)
        self.assertTrue(transcript["copy"])
        status, data = self.request(f"/api/session-contexts/{sid}/continue", method="POST",
                                    body={"mode": "resume", "text": "again"})
        self.assertEqual((status, data["error"]["code"]), (409, "SESSION_NOT_RESUMABLE"))
        status, data = self.request(f"/api/session-contexts/{sid}", method="DELETE")
        self.assertEqual(status, 200, data)
        self.assertFalse(mirror.exists())

    def test_deleting_a_live_session_removes_its_mirror(self):
        status, data = self.request("/api/agent-sessions", method="POST",
                                    body={"platform": "claude", "prompt": "short"})
        sid = data["session"]["id"]
        self.wait_done(sid)
        status, _ = self.request("/api/session-contexts")  # syncs stale mirrors
        mirror = self.app.require().path / "sessions" / "internal" / sid
        self.assertTrue(mirror.is_dir())
        status, data = self.request(f"/api/session-contexts/{sid}", method="DELETE")
        self.assertEqual(status, 200, data)
        self.assertFalse(mirror.exists())

    def test_project_export_excludes_sessions_unless_asked(self):
        self.enable()
        self.assertEqual(self.upload(jsonl(*CLAUDE_LINES))[0], 200)
        status, data = self.request("/api/agent-sessions", method="POST",
                                    body={"platform": "claude", "prompt": "export me sk-ant-api03-EXPORTSECRET12345"})
        internal = data["session"]["id"]
        self.wait_done(internal)
        for flag, expected in (("", False), ("&sessions=1", True)):
            with self.subTest(flag=flag):
                req = urllib.request.Request(self.base + "/api/export?format=project" + flag)
                with urllib.request.urlopen(req, timeout=30) as response:
                    archive = zipfile.ZipFile(io.BytesIO(response.read()))
                names = archive.namelist()
                has_external = any(name.endswith(f"sessions/{self.sid}.jsonl") for name in names)
                has_internal = any(f"sessions/internal/{internal}/stdout.jsonl" in name for name in names)
                self.assertEqual((has_external, has_internal), (expected, expected))
                manifest = json.loads(archive.read("ARK-EXPORT.json"))
                self.assertEqual(manifest["sessions_included"], expected)
                if expected:
                    meta_name = next(n for n in names if n.endswith(f"internal/{internal}/meta.json"))
                    meta = json.loads(archive.read(meta_name))
                    self.assertNotIn("read_token", meta)
                    self.assertNotIn("EXPORTSECRET", archive.read(meta_name).decode())


if __name__ == "__main__":
    unittest.main()
