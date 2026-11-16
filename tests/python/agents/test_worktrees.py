import json
import tempfile
import unittest
import urllib.error
import urllib.request
from pathlib import Path

from arkstudio.server import App, serve
from arkstudio.worktrees import (
    WorktreeError,
    WorktreeManager,
    parse_worktree_porcelain,
    sanitize_directory,
    validate_branch,
)


class FakeGit:
    """Small deterministic Git double; never touches a real repository."""

    def __init__(self, repo: Path):
        self.repo = repo
        self.entries = [{"path": str(repo), "branch": "main", "head": "a" * 40}]
        self.branches = {"main"}
        self.dirty = {}
        self.calls = []
        self.is_repo = True
        self.fail_list = False

    def _porcelain(self):
        chunks = []
        for entry in self.entries:
            chunks.append(f"worktree {entry['path']}")
            chunks.append(f"HEAD {entry['head']}")
            if entry.get("branch"):
                chunks.append(f"branch refs/heads/{entry['branch']}")
            else:
                chunks.append("detached")
            chunks.append("")
        return "\n".join(chunks)

    def __call__(self, args, cwd):
        self.calls.append((list(args), str(cwd)))
        joined = " ".join(args)
        if args[:2] == ["rev-parse", "--show-toplevel"]:
            if not self.is_repo:
                return {"returncode": 128, "stdout": "", "stderr": "not a git repository"}
            return {"returncode": 0, "stdout": str(self.repo) + "\n", "stderr": ""}
        if args[:2] == ["rev-parse", "--abbrev-ref"]:
            return {"returncode": 0, "stdout": "main\n", "stderr": ""}
        if args[:2] == ["rev-parse", "--short"]:
            return {"returncode": 0, "stdout": "abc1234\n", "stderr": ""}
        if args[:3] == ["worktree", "list", "--porcelain"]:
            if self.fail_list:
                return {"returncode": 1, "stdout": "", "stderr": "list failed"}
            return {"returncode": 0, "stdout": self._porcelain(), "stderr": ""}
        if args[:2] == ["status", "--porcelain=v1"]:
            return {"returncode": 0, "stdout": self.dirty.get(str(cwd), ""), "stderr": ""}
        if args[:2] == ["branch", "--list"]:
            branch = args[2]
            return {"returncode": 0, "stdout": (branch + "\n") if branch in self.branches else "",
                    "stderr": ""}
        if args[:2] == ["rev-parse", "--verify"]:
            ref = args[2].replace("^{commit}", "")
            ok = ref in self.branches or len(ref) >= 7
            return {"returncode": 0 if ok else 128,
                    "stdout": ("b" * 40 + "\n") if ok else "", "stderr": "" if ok else "not found"}
        if args[:2] == ["worktree", "add"]:
            target = Path(args[2])
            target.mkdir(parents=True, exist_ok=True)
            if "-b" in args:
                index = args.index("-b")
                branch = args[index + 1]
                self.branches.add(branch)
            else:
                branch = args[3]
            self.entries.append({"path": str(target), "branch": branch, "head": "c" * 40})
            return {"returncode": 0, "stdout": "", "stderr": ""}
        if args[:2] == ["worktree", "remove"]:
            target = args[-1]
            self.entries = [entry for entry in self.entries if entry["path"] != target]
            Path(target).rmdir()
            return {"returncode": 0, "stdout": "", "stderr": ""}
        if args[:1] == ["branch"] and args[1] == "-D":
            self.branches.discard(args[2])
            return {"returncode": 0, "stdout": "", "stderr": ""}
        return {"returncode": 1, "stdout": "", "stderr": f"unexpected git call: {joined}"}


class WorktreeHelpersTests(unittest.TestCase):
    def test_porcelain_parser_handles_locked_detached_and_prunable(self):
        raw = (
            "worktree /repo\nHEAD aaaa\nbranch refs/heads/main\n\n"
            "worktree /repo-worktrees/agent-x\nHEAD bbbb\nbranch refs/heads/agent/x\nlocked busy\n\n"
            "worktree /repo-worktrees/detached\nHEAD cccc\ndetached\nprunable gone\n\n"
        )
        entries = parse_worktree_porcelain(raw)
        self.assertEqual([entry["branch"] for entry in entries], ["main", "agent/x", ""])
        self.assertTrue(entries[1]["locked"])
        self.assertTrue(entries[2]["detached"])
        self.assertTrue(entries[2]["prunable"])
        self.assertEqual(entries[0]["head_short"], "aaaa")

    def test_branch_and_folder_validation(self):
        self.assertEqual(validate_branch("agent/feature-x"), "agent/feature-x")
        for bad in ("-x", "agent//x", "agent/.hidden", "agent/x.", "agent\\x", "HEAD", "a b",
                    "x..y", "agent/x.lock", "/absolute", "agent/"):
            with self.assertRaises(WorktreeError, msg=bad):
                validate_branch(bad)
        self.assertEqual(sanitize_directory("agent/feature-x"), "agent-feature-x")
        self.assertEqual(sanitize_directory("agent/a:b*c"), "agent-a-b-c")
        with self.assertRaises(WorktreeError):
            sanitize_directory("///")


class WorktreeManagerTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.base = Path(self.tmp.name)
        self.repo = self.base / "repo"
        self.repo.mkdir()
        self.git = FakeGit(self.repo)
        self.manager = WorktreeManager(
            project_provider=lambda: {"path": str(self.repo)},
            root=self.base / "repo-worktrees",
            env={},
            run_git=self.git,
        )

    def tearDown(self):
        self.tmp.cleanup()

    def test_list_and_safe_root(self):
        listing = self.manager.list()
        self.assertTrue(listing["available"])
        self.assertEqual(listing["repository"]["root"], str(self.repo))
        self.assertEqual(listing["root"], str(self.base / "repo-worktrees"))
        self.assertEqual(listing["worktrees"][0]["main"], True)
        self.assertEqual(listing["managed_total"], 0)

    def test_default_root_is_a_sibling_of_the_repository(self):
        manager = WorktreeManager(project_provider=lambda: {"path": str(self.repo)},
                                  env={}, run_git=self.git)
        self.assertEqual(str(manager.safe_root()), str(self.base / "repo-worktrees"))

    def test_environment_root_override(self):
        manager = WorktreeManager(project_provider=lambda: {"path": str(self.repo)},
                                  env={"ARK_AGENT_WORKTREE_ROOT": str(self.base / "elsewhere")},
                                  run_git=self.git)
        self.assertEqual(str(manager.safe_root()), str(self.base / "elsewhere"))

    def test_non_git_project_reports_unavailable(self):
        self.git.is_repo = False
        listing = self.manager.list()
        self.assertFalse(listing["available"])
        self.assertIn("not inside a Git repository", listing["reason"])
        with self.assertRaises(WorktreeError) as raised:
            self.manager.create({"name": "x"})
        self.assertEqual(raised.exception.code, "NOT_A_GIT_REPOSITORY")

    def test_create_uses_agent_branch_and_clean_check(self):
        result = self.manager.create({"name": "Feature X"})
        self.assertTrue(result["created"])
        self.assertEqual(result["worktree"]["branch"], "agent/feature-x")
        self.assertEqual(result["base"], "main")
        add = [call for call in self.git.calls if call[0][:2] == ["worktree", "add"]][0]
        self.assertIn("-b", add[0])
        self.assertIn("agent/feature-x", add[0])
        self.assertTrue(Path(result["worktree"]["path"]).is_dir())

    def test_create_refuses_dirty_base(self):
        self.git.dirty[str(self.repo)] = " M README.md\x00"
        with self.assertRaises(WorktreeError) as raised:
            self.manager.create({"name": "Feature"})
        self.assertEqual(raised.exception.code, "WORKTREE_BASE_DIRTY")
        self.assertFalse(any(call[0][:2] == ["worktree", "add"] for call in self.git.calls))

    def test_create_reuses_an_existing_branch(self):
        self.git.branches.add("agent/existing")
        result = self.manager.create({"branch": "agent/existing"})
        self.assertTrue(result["reused_branch"])
        self.assertFalse(result["created_branch"])
        add = [call for call in self.git.calls if call[0][:2] == ["worktree", "add"]][0]
        self.assertNotIn("-b", add[0])

    def test_create_refuses_branch_checked_out_elsewhere(self):
        other = self.base / "repo-worktrees" / "other"
        other.mkdir(parents=True)
        self.git.entries.append({"path": str(other), "branch": "agent/busy", "head": "d" * 40})
        with self.assertRaises(WorktreeError) as raised:
            self.manager.create({"branch": "agent/busy"})
        self.assertEqual(raised.exception.code, "BRANCH_ALREADY_CHECKED_OUT")

    def test_create_refuses_main_branch(self):
        with self.assertRaises(WorktreeError) as raised:
            self.manager.create({"branch": "main"})
        self.assertEqual(raised.exception.code, "BRANCH_IS_MAIN")

    def test_create_is_limited_by_max_worktrees(self):
        manager = WorktreeManager(project_provider=lambda: {"path": str(self.repo)},
                                  root=self.base / "repo-worktrees", env={},
                                  run_git=self.git, max_worktrees=1)
        first = manager.create({"name": "one"})["worktree"]
        with self.assertRaises(WorktreeError) as raised:
            manager.create({"name": "two"})
        self.assertEqual(raised.exception.code, "WORKTREE_LIMIT")
        self.assertTrue(Path(first["path"]).is_dir())

    def test_resolve_session_target_rejects_main_and_unknown(self):
        with self.assertRaises(WorktreeError) as raised:
            self.manager.resolve_session_target(str(self.repo))
        self.assertEqual(raised.exception.code, "WORKTREE_NOT_FOUND")
        with self.assertRaises(WorktreeError):
            self.manager.resolve_session_target("agent/missing")
        created = self.manager.create({"name": "Target"})["worktree"]
        resolved = self.manager.resolve_session_target("agent/target")
        self.assertEqual(resolved["path"], created["path"])

    def test_remove_requires_confirmation_and_never_main(self):
        created = self.manager.create({"name": "Remove Me"})["worktree"]
        with self.assertRaises(WorktreeError) as raised:
            self.manager.remove({"path": created["path"]})
        self.assertEqual(raised.exception.code, "CONFIRM_REQUIRED")
        with self.assertRaises(WorktreeError) as raised:
            self.manager.remove({"path": str(self.repo), "confirm": True})
        self.assertEqual(raised.exception.code, "WORKTREE_NOT_FOUND")

    def test_remove_refuses_dirty_worktree_until_forced(self):
        created = self.manager.create({"name": "Dirty"})["worktree"]
        self.git.dirty[created["path"]] = "?? scratch.txt\x00"
        with self.assertRaises(WorktreeError) as raised:
            self.manager.remove({"path": created["path"], "confirm": True})
        self.assertEqual(raised.exception.code, "WORKTREE_DIRTY")
        result = self.manager.remove({"path": created["path"], "confirm": True, "force": True,
                                      "delete_branch": True})
        self.assertTrue(result["removed"])
        self.assertTrue(result["forced"])
        self.assertTrue(result["branch_deleted"])
        self.assertFalse(Path(created["path"]).exists())


class WorktreeApiTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.tmp = tempfile.TemporaryDirectory()
        root = Path(cls.tmp.name)
        repo = root / "repo"
        repo.mkdir()
        cls.git = FakeGit(repo)
        manager = WorktreeManager(project_provider=lambda: {"path": str(repo)},
                                  root=root / "repo-worktrees", env={}, run_git=cls.git)
        cls.app = App(root / "projects", seed_demo=False, task_sessions_root=root / "sessions",
                      task_presets=[{"id": "echo", "label": "Echo", "demo": True,
                                     "command": ["python", "-c", "pass"], "prompt_mode": "stdin"}],
                      worktree_manager=manager)
        cls.httpd = serve(cls.app, port=0, block=False)
        cls.base = f"http://127.0.0.1:{cls.httpd.server_address[1]}"

    @classmethod
    def tearDownClass(cls):
        cls.httpd.shutdown()
        cls.httpd.server_close()
        cls.app.project.close()
        cls.tmp.cleanup()

    def request(self, path, method="GET", body=None):
        data = json.dumps(body).encode() if body is not None else None
        req = urllib.request.Request(self.base + path, data=data, method=method,
                                     headers={"Content-Type": "application/json"})
        try:
            with urllib.request.urlopen(req, timeout=10) as response:
                payload = response.read()
                return response.status, json.loads(payload) if payload else {}
        except urllib.error.HTTPError as exc:
            payload = exc.read()
            try:
                return exc.code, json.loads(payload or b"{}")
            except ValueError:
                return exc.code, {}

    def test_list_create_and_remove_with_explicit_confirmation(self):
        status, listing = self.request("/api/worktrees")
        self.assertEqual(status, 200)
        self.assertTrue(listing["available"])
        self.assertEqual(listing["managed_total"], 0)

        status, created = self.request("/api/worktrees/create", "POST", {"name": "Api Feature"})
        self.assertEqual(status, 200, created)
        self.assertEqual(created["worktree"]["branch"], "agent/api-feature")
        self.assertEqual(len(created["worktrees"]["managed"]), 1)

        status, listed = self.request("/api/worktrees")
        self.assertEqual(status, 200)
        self.assertEqual(listed["managed_total"], 1)
        self.assertEqual(listed["managed"][0]["branch"], "agent/api-feature")

        status, rejected = self.request("/api/worktrees/remove", "POST",
                                        {"path": created["worktree"]["path"]})
        self.assertEqual(status, 400)
        self.assertEqual(rejected["error"]["code"], "CONFIRM_REQUIRED")

        status, removed = self.request("/api/worktrees/remove", "POST",
                                       {"path": created["worktree"]["path"], "confirm": True})
        self.assertEqual(status, 200, removed)
        self.assertTrue(removed["removed"])
        self.assertEqual(removed["worktrees"]["managed_total"], 0)

    def test_create_requires_clean_base(self):
        self.git.dirty[str(self.git.repo)] = " M dirty.txt\x00"
        try:
            status, payload = self.request("/api/worktrees/create", "POST", {"name": "Nope"})
            self.assertEqual(status, 409)
            self.assertEqual(payload["error"]["code"], "WORKTREE_BASE_DIRTY")
        finally:
            self.git.dirty.clear()


if __name__ == "__main__":
    unittest.main()
