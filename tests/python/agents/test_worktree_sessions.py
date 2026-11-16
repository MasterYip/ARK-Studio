import json
import sys
import tempfile
import time
import unittest
import urllib.error
import urllib.request
from pathlib import Path

from arkstudio.server import App, serve

FAKE_AGENT = r'''
import json
import os
import sys

print("cwd=" + os.getcwd(), flush=True)
print("worktree=" + os.environ.get("ARK_TASK_WORKTREE", ""), flush=True)
print("branch=" + os.environ.get("ARK_TASK_WORKTREE_BRANCH", ""), flush=True)
for raw in sys.stdin:
    if raw.strip() == "quit":
        break
    print("echo:" + raw.strip(), flush=True)
'''


class FakeWorktrees:
    """Minimal manager double used to test the session integration contract."""

    def __init__(self, root: Path):
        self.root = root
        self.path = root / "repo-worktrees" / "agent-integration"
        self.path.mkdir(parents=True, exist_ok=True)
        self.repository = {"root": str(root / "repo"), "name": "repo", "branch": "main",
                           "head": "abc1234", "main_branch": "main"}
        self.resolve_calls = []

    def descriptor(self, path=None):
        return {
            "path": str(path or self.path),
            "name": "agent-integration",
            "branch": "agent/integration",
            "head": "abc1234",
            "base": "main",
            "main": False,
            "managed": True,
            "detached": False,
            "locked": False,
            "prunable": False,
            "missing": False,
            "dirty": 0,
            "clean": True,
            "status": "clean",
            "active_sessions": [],
            "repository": self.repository,
            "root": str(self.root / "repo-worktrees"),
        }

    def resolve_session_target(self, ref):
        self.resolve_calls.append(str(ref))
        if str(ref) in {str(self.path), "agent/integration", "agent-integration"}:
            return self.descriptor()
        from arkstudio.worktrees import WorktreeError
        raise WorktreeError(f"worktree not found: {ref}", 404, "WORKTREE_NOT_FOUND")

    def list(self):
        worktree = self.descriptor()
        return {
            "available": True,
            "reason": "",
            "repository": self.repository,
            "root": str(self.root / "repo-worktrees"),
            "root_exists": True,
            "worktrees": [
                {"path": self.repository["root"], "name": "repo", "branch": "", "main": True,
                 "managed": False, "branch_name": "main", "missing": False, "dirty": 0,
                 "clean": True, "status": "main", "active_sessions": [], "active_count": 0},
                worktree,
            ],
            "managed": [worktree],
            "total": 2,
            "managed_total": 1,
            "dirty_total": 0,
            "generated_at": "2026-10-31T00:00:00+00:00",
        }


class WorktreeSessionTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.tmp = tempfile.TemporaryDirectory()
        root = Path(cls.tmp.name)
        cls.fake_script = root / "fake_agent.py"
        cls.fake_script.write_text(FAKE_AGENT, encoding="utf-8")
        cls.worktrees = FakeWorktrees(root)
        presets = [{
            "id": "fake",
            "label": "Fake agent",
            "command": [sys.executable, "-u", str(cls.fake_script)],
            "prompt_mode": "stdin",
            "followup": True,
            "supports_approval": True,
        }]
        cls.app = App(root / "projects", seed_demo=False, task_sessions_root=root / "sessions",
                      task_presets=presets, worktree_manager=cls.worktrees)
        cls.httpd = serve(cls.app, port=0, block=False)
        cls.base = f"http://127.0.0.1:{cls.httpd.server_address[1]}"

    @classmethod
    def tearDownClass(cls):
        cls._stop_all()
        cls.httpd.shutdown()
        cls.httpd.server_close()
        cls.app.project.close()
        cls.tmp.cleanup()

    @classmethod
    def _stop_all(cls):
        for item in cls.app.sessions.list()["sessions"]:
            if item["status"] in {"running", "idle", "starting", "stopping"}:
                try:
                    cls.app.sessions.request_stop(item["id"])
                except Exception:
                    pass
        deadline = time.time() + 8
        while time.time() < deadline:
            if not any(item["status"] in {"running", "idle", "starting", "stopping"}
                       for item in cls.app.sessions.list()["sessions"]):
                return
            time.sleep(0.05)

    def setUp(self):
        self.worktrees.resolve_calls.clear()

    def tearDown(self):
        self._stop_all()

    def request(self, path, *, method="GET", body=None, headers=None):
        data = json.dumps(body).encode() if body is not None else None
        req = urllib.request.Request(self.base + path, data=data, method=method,
                                     headers={"Content-Type": "application/json", **(headers or {})})
        try:
            with urllib.request.urlopen(req, timeout=10) as response:
                return response.status, json.loads(response.read())
        except urllib.error.HTTPError as exc:
            payload = exc.read()
            try:
                return exc.code, json.loads(payload or b"{}")
            except ValueError:
                return exc.code, {}

    def start(self, **extra):
        body = {"platform": "fake", "title": "Worktree session", "prompt": "start",
                "worktree": str(self.worktrees.path)}
        body.update(extra)
        return self.request("/api/agent-sessions", method="POST", body=body)

    def wait_for_log(self, session_id, needle, timeout=8):
        deadline = time.time() + timeout
        while time.time() < deadline:
            status, data = self.request(
                f"/api/agent-sessions/{session_id}/logs?stream=stdout&offset=0&limit=200")
            if status == 200:
                text = "\n".join(entry.get("line") or "" for entry in data.get("entries", []))
                if needle in text:
                    return text
            time.sleep(0.05)
        self.fail(f"timed out waiting for {needle!r}")

    def test_session_runs_inside_the_selected_worktree(self):
        status, data = self.start()
        self.assertEqual(status, 200, data)
        session = data["session"]
        self.assertEqual(session["worktree"]["branch"], "agent/integration")
        self.assertEqual(session["worktree"]["path"], str(self.worktrees.path))
        self.assertEqual(session["cwd"], ".")
        logs = self.wait_for_log(session["id"], "branch=")
        self.assertIn(f"cwd={self.worktrees.path}", logs)
        self.assertIn(f"worktree={self.worktrees.path}", logs)
        self.assertIn("branch=agent/integration", logs)

    def test_second_session_in_same_worktree_is_refused(self):
        status, first = self.start()
        self.assertEqual(status, 200, first)
        status, blocked = self.start(title="Second")
        self.assertEqual(status, 409)
        self.assertIn("another active session already uses this worktree", blocked["error"]["message"])
        self.assertEqual(len(self.worktrees.resolve_calls), 2)

    def test_explicit_shared_worktree_is_flagged_and_grouped(self):
        status, first = self.start()
        self.assertEqual(status, 200, first)
        status, second = self.start(title="Shared", allow_shared_worktree=True)
        self.assertEqual(status, 200, second)
        self.wait_for_log(first["session"]["id"], "branch=")
        self.wait_for_log(second["session"]["id"], "branch=")

        status, listing = self.request("/api/agent-sessions")
        self.assertEqual(status, 200)
        groups = [group for group in listing["groups"] if group["kind"] == "worktree"]
        self.assertEqual(len(groups), 1)
        self.assertTrue(groups[0]["conflict"])
        self.assertEqual(groups[0]["active"], 2)
        first_session = next(item for item in listing["sessions"] if item["id"] == first["session"]["id"])
        self.assertTrue(first_session["worktree"]["conflict"])

        status, worktrees = self.request("/api/worktrees")
        self.assertEqual(status, 200)
        managed = [item for item in worktrees["worktrees"] if item["managed"]]
        self.assertEqual(managed[0]["active_count"], 2)
        self.assertTrue(managed[0]["conflict"])

    def test_worktree_cannot_be_removed_while_a_session_is_active(self):
        status, created = self.start()
        self.assertEqual(status, 200, created)
        status, payload = self.request("/api/worktrees/remove", method="POST",
                                       body={"path": str(self.worktrees.path), "confirm": True})
        self.assertEqual(status, 409)
        self.assertEqual(payload["error"]["code"], "WORKTREE_SESSION_ACTIVE")

    def test_unknown_worktree_is_a_clean_404(self):
        status, payload = self.start(worktree="missing-reference")
        self.assertEqual(status, 404)
        self.assertEqual(payload["error"]["code"], "WORKTREE_NOT_FOUND")

    def test_private_sessions_in_same_project_are_not_conflicts(self):
        for index in range(2):
            status, data = self.request(
                "/api/agent-sessions", method="POST",
                body={"platform": "fake", "title": f"Private {index}", "prompt": "start"})
            self.assertEqual(status, 200, data)
        status, listing = self.request("/api/agent-sessions")
        self.assertEqual(status, 200)
        project_groups = [group for group in listing["groups"] if group["kind"] == "project"]
        self.assertEqual(len(project_groups), 1)
        self.assertEqual(project_groups[0]["active"], 2)
        self.assertFalse(project_groups[0]["conflict"])
        self.assertEqual(listing["group_counts"]["conflicts"], 0)

    def test_task_read_token_cannot_mutate_worktrees(self):
        status, created = self.start()
        self.assertEqual(status, 200, created)
        token = created["session"]["read_token"]
        status, payload = self.request(
            "/api/worktrees/create", method="POST", body={"name": "Hijacked"},
            headers={"X-ARK-Read-Token": token})
        self.assertEqual(status, 403, payload)
        status, payload = self.request(
            "/api/worktrees/remove", method="POST",
            body={"path": str(self.worktrees.path), "confirm": True},
            headers={"X-ARK-Read-Token": token})
        self.assertEqual(status, 403, payload)
        status, payload = self.request(
            "/api/providers/refresh", method="POST", body={},
            headers={"X-ARK-Read-Token": token})
        self.assertEqual(status, 403, payload)

    def test_worktree_contract_preserves_permission_mode(self):
        status, created = self.start(access_mode="ask")
        self.assertEqual(status, 200, created)
        session = self.app.sessions.get(created["session"]["id"])
        contract = (session.workspace / "ARK-TASK.md").read_text(encoding="utf-8")
        self.assertIn("Permission mode: **ask to write**", contract)
        self.assertIn("Mutating actions need approval", contract)
        self.assertIn("Write only inside this session workspace or the selected Git worktree.", contract)
        self.assertIn("## Git worktree", contract)
        self.assertIn("Editing this worktree is expected.", contract)
        self.assertIn(f"Working tree: `{self.worktrees.path}`", contract)
        snapshot = self.app.sessions.context_snapshot(session)
        self.assertEqual(snapshot["permission"]["mode"], "ask")
        self.assertEqual(snapshot["session"]["worktree"]["branch"], "agent/integration")

    def test_plan_worktree_contract_forbids_edits(self):
        status, created = self.start(access_mode="plan")
        self.assertEqual(status, 200, created)
        session = self.app.sessions.get(created["session"]["id"])
        contract = (session.workspace / "ARK-TASK.md").read_text(encoding="utf-8")
        self.assertIn("## Plan mode", contract)
        self.assertIn("Do not edit the selected Git worktree in plan mode either.", contract)
        self.assertIn("Editing this worktree is expected except in plan mode.", contract)

    def test_continue_preserves_the_worktree(self):
        status, created = self.start()
        self.assertEqual(status, 200, created)
        session_id = created["session"]["id"]
        self.wait_for_log(session_id, "branch=")
        self.app.sessions.request_stop(session_id)
        deadline = time.time() + 8
        while time.time() < deadline and self.app.sessions.get(session_id).is_active:
            time.sleep(0.05)
        status, data = self.request(f"/api/agent-sessions/{session_id}/continue",
                                    method="POST", body={"text": "continue here"})
        self.assertEqual(status, 200, data)
        self.assertEqual(data["session"]["worktree"]["path"], str(self.worktrees.path))
        logs = self.wait_for_log(session_id, "continue here")
        self.assertIn(f"cwd={self.worktrees.path}", logs)


if __name__ == "__main__":
    unittest.main()
