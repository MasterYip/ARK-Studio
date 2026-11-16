"""Tests for the project-scoped Files workspace.

Everything runs in temporary folders; terminal commands are harmless local
Python one-liners and never touch the network or a model.
"""
import json
import os
import shutil
import subprocess
import sys
import tempfile
import time
import unittest
import urllib.error
import urllib.parse
import urllib.request
from pathlib import Path
from unittest import mock

from arkstudio import workspace as ws
from arkstudio.server import App, serve

GIT = shutil.which("git")


class TempWorkspace:
    """A temp folder whose cleanup never recurses on a locked child."""

    def __init__(self):
        self.name = tempfile.mkdtemp(prefix="ark-workspace-test-")

    def cleanup(self):
        shutil.rmtree(self.name, ignore_errors=True)


def write_text(path: Path, text: str) -> None:
    """Write exact LF newlines (Path.write_text translates them on Windows)."""
    path.parent.mkdir(parents=True, exist_ok=True)
    with path.open("w", encoding="utf-8", newline="") as handle:
        handle.write(text)


def make_symlink(link: Path, target: Path) -> bool:
    try:
        link.symlink_to(target)
        return True
    except (OSError, NotImplementedError):
        return False


def make_junction(link: Path, target: Path) -> bool:
    """Create a Windows directory junction (no Developer Mode privilege needed)."""
    if os.name != "nt":
        return False
    try:
        result = subprocess.run(
            ["cmd", "/c", "mklink", "/J", str(link), str(target)],
            capture_output=True, timeout=20)
    except (OSError, subprocess.SubprocessError):
        return False
    return result.returncode == 0 and link.exists()


def git(root: Path, *args: str, check: bool = True) -> subprocess.CompletedProcess:
    env = dict(os.environ)
    env.update({
        "GIT_AUTHOR_NAME": "ARK Test", "GIT_AUTHOR_EMAIL": "ark@example.invalid",
        "GIT_COMMITTER_NAME": "ARK Test", "GIT_COMMITTER_EMAIL": "ark@example.invalid",
        "GIT_CONFIG_NOSYSTEM": "1",
    })
    return subprocess.run(
        [GIT, "-C", str(root), "-c", "user.name=ARK Test", "-c", "user.email=ark@example.invalid",
         *args],
        capture_output=True, env=env, check=check, timeout=30,
    )


@unittest.skipUnless(GIT, "git is required for diff tests")
class WorkspaceFileTests(unittest.TestCase):
    def setUp(self):
        self.tmp = TempWorkspace()
        self.base = Path(self.tmp.name)
        self.root = self.base / "project.ark"
        self.root.mkdir()
        write_text(self.root / "src" / "app.py", "print('old')\n")
        write_text(self.root / "docs" / "guide.md", "# Guide\n\nHello.\n")
        write_text(self.root / "README.md", "# Project\n")
        write_text(self.root / "node_modules" / "pkg" / "index.js", "noise")
        write_text(self.root / ".gitignore",
                   "node_modules/\n*.tmp\nescape.txt\ninner-link.txt\noutside-junction/\n")
        (self.root / "image.bin").write_bytes(b"\x89PNG\x00\x01binary")
        (self.root / "bad.txt").write_bytes(b"\xff\xfe\x00bad")
        self.outside = self.base / "secret.txt"
        self.outside.write_text("top secret", encoding="utf-8")
        self.linked = make_symlink(self.root / "escape.txt", self.outside)
        self.inside_link = make_symlink(self.root / "inner-link.txt", self.root / "README.md")
        self.junction = make_junction(self.root / "outside-junction", self.base)
        git(self.root, "init", "-q")
        git(self.root, "add", "-A")
        git(self.root, "commit", "-qm", "initial")
        self.ws = ws.Workspace()

    def tearDown(self):
        self.ws.shutdown()
        self.tmp.cleanup()

    # ----------------------------------------------------------------- tree
    def test_tree_is_lazy_ignores_noise_and_caps(self):
        root_tree = self.ws.tree(self.root)
        names = {entry["name"] for entry in root_tree["entries"]}
        self.assertIn("src", names)
        self.assertIn("README.md", names)
        self.assertNotIn("node_modules", names)
        self.assertNotIn(".git", names)
        self.assertNotIn("ark.sqlite3", names)
        self.assertNotIn("escape.txt", names)
        self.assertNotIn("outside-junction", names)
        self.assertEqual(
            root_tree["symlinks_skipped"],
            int(self.linked) + int(self.inside_link) + int(self.junction))
        # Lazy: no nested children unless depth > 1.
        src = next(entry for entry in root_tree["entries"] if entry["name"] == "src")
        self.assertNotIn("children", src)
        deep = self.ws.tree(self.root, depth=2)
        src = next(entry for entry in deep["entries"] if entry["name"] == "src")
        self.assertIn("children", src)
        self.assertEqual([child["name"] for child in src["children"]], ["app.py"])
        with mock.patch.object(ws, "MAX_TREE_ENTRIES", 2):
            capped = self.ws.tree(self.root)
        self.assertTrue(capped["truncated"])
        self.assertEqual(capped["count"], 2)

    def test_tree_rejects_path_outside_and_ignored(self):
        for value in ("../", "..\\secret.txt", "/etc", "C:\\Windows", "node_modules",
                      ".git/config", "src/./../../x"):
            with self.subTest(value=value):
                with self.assertRaises(ws.WorkspaceError) as ctx:
                    self.ws.tree(self.root, value)
                self.assertIn(ctx.exception.status, (400, 403))

    def test_search_skips_ignored_and_finds_nested(self):
        result = self.ws.search(self.root, "guide")
        self.assertEqual([entry["path"] for entry in result["results"]], ["docs/guide.md"])
        self.assertEqual(result["results"][0]["language"], "markdown")
        self.assertEqual(self.ws.search(self.root, "index")["results"], [])
        self.assertEqual(self.ws.search(self.root, "")["results"], [])

    # ----------------------------------------------------------------- file
    def test_read_write_optimistic_concurrency(self):
        first = self.ws.read_file(self.root, "README.md")
        self.assertEqual(first["content"], "# Project\n")
        self.assertEqual(first["language"], "markdown")
        saved = self.ws.write_file(self.root, {
            "path": "README.md", "content": "# Changed\n", "base_hash": first["hash"]})
        self.assertEqual(saved["size"], len("# Changed\n"))
        self.assertNotEqual(saved["hash"], first["hash"])
        self.assertEqual((self.root / "README.md").read_text(encoding="utf-8"), "# Changed\n")
        with self.assertRaises(ws.WorkspaceError) as ctx:
            self.ws.write_file(self.root, {
                "path": "README.md", "content": "stale", "base_hash": first["hash"]})
        self.assertEqual(ctx.exception.status, 409)
        # mtime concurrency also works.
        current = self.ws.read_file(self.root, "README.md")
        with self.assertRaises(ws.WorkspaceError) as ctx:
            self.ws.write_file(self.root, {
                "path": "README.md", "content": "nope", "base_mtime": current["mtime_ns"] + 1})
        self.assertEqual(ctx.exception.status, 409)
        # Missing base hash is refused rather than silently overwriting.
        with self.assertRaises(ws.WorkspaceError) as ctx:
            self.ws.write_file(self.root, {"path": "README.md", "content": "nope"})
        self.assertEqual(ctx.exception.status, 409)

    def test_create_and_delete_paths(self):
        self.ws.create_path(self.root, {"path": "notes", "type": "dir"})
        created = self.ws.create_path(self.root, {"path": "notes/new.txt", "type": "file"})
        self.assertEqual(created["type"], "file")
        self.assertTrue((self.root / "notes" / "new.txt").is_file())
        with self.assertRaises(ws.WorkspaceError) as ctx:
            self.ws.create_path(self.root, {"path": "notes/new.txt", "type": "file"})
        self.assertEqual(ctx.exception.status, 409)
        self.ws.delete_path(self.root, {"path": "notes/new.txt"})
        self.assertFalse((self.root / "notes" / "new.txt").exists())
        self.ws.create_path(self.root, {"path": "notes/empty", "type": "dir"})
        self.ws.delete_path(self.root, {"path": "notes/empty"})
        self.assertFalse((self.root / "notes" / "empty").exists())
        # Non-empty folder requires recursive.
        self.ws.create_path(self.root, {"path": "notes/big", "type": "dir"})
        self.ws.create_path(self.root, {"path": "notes/big/a.txt", "type": "file", "content": "a"})
        with self.assertRaises(ws.WorkspaceError) as ctx:
            self.ws.delete_path(self.root, {"path": "notes/big"})
        self.assertEqual(ctx.exception.status, 409)
        self.ws.delete_path(self.root, {"path": "notes/big", "recursive": True})
        self.assertFalse((self.root / "notes" / "big").exists())
        with self.assertRaises(ws.WorkspaceError) as ctx:
            self.ws.delete_path(self.root, {"path": ""})
        self.assertEqual(ctx.exception.status, 400)

    def test_symlink_rejection_is_enforced_even_without_host_privileges(self):
        # The host may forbid creating symlinks (Windows without Developer
        # Mode). Patch the detector so the boundary itself is always exercised.
        with mock.patch.object(
                ws, "_is_link_like",
                side_effect=lambda path: Path(path).name == "escape.txt"):
            for action in (
                lambda: self.ws.read_file(self.root, "escape.txt"),
                lambda: self.ws.write_file(
                    self.root, {"path": "escape.txt", "content": "x", "create": True}),
                lambda: self.ws.delete_path(self.root, {"path": "escape.txt"}),
                lambda: self.ws.diff_file(self.root, "escape.txt"),
            ):
                with self.assertRaises(ws.WorkspaceError) as ctx:
                    action()
                self.assertEqual(ctx.exception.status, 403)

    def test_recursive_delete_unlinks_links_without_following(self):
        bundle = self.root / "bundle"
        bundle.mkdir()
        write_text(bundle / "keep.txt", "keep")
        nested = make_symlink(bundle / "escape.txt", self.outside)
        dir_link = make_symlink(bundle / "outside-dir", self.base)
        junction = make_junction(bundle / "outside-junction", self.base)
        removed = self.ws.delete_path(self.root, {"path": "bundle", "recursive": True})
        self.assertFalse(bundle.exists())
        self.assertTrue(self.outside.is_file())
        self.assertTrue(self.base.is_dir())
        self.assertTrue((self.root / "README.md").is_file())
        if not nested and not dir_link and not junction:
            self.skipTest("symlinks/junctions are unavailable for this user")
        self.assertGreaterEqual(removed["removed"], 2)

    def test_junction_escape_is_rejected(self):
        if not self.junction:
            self.skipTest("directory junctions are unavailable for this user")
        with self.assertRaises(ws.WorkspaceError) as ctx:
            self.ws.read_file(self.root, "outside-junction/secret.txt")
        self.assertEqual(ctx.exception.status, 403)
        with self.assertRaises(ws.WorkspaceError) as ctx:
            self.ws.tree(self.root, "outside-junction")
        self.assertEqual(ctx.exception.status, 403)

    def test_rename_rejects_existing_and_self_nesting(self):
        renamed = self.ws.rename_path(self.root, {"from": "README.md", "to": "docs/README.md"})
        self.assertEqual(renamed["to"], "docs/README.md")
        self.assertFalse((self.root / "README.md").exists())
        self.assertTrue((self.root / "docs" / "README.md").is_file())
        with self.assertRaises(ws.WorkspaceError) as ctx:
            self.ws.rename_path(self.root, {"from": "docs/README.md", "to": "src/app.py"})
        self.assertEqual(ctx.exception.status, 409)
        with self.assertRaises(ws.WorkspaceError) as ctx:
            self.ws.rename_path(self.root, {"from": "src", "to": "src/nested"})
        self.assertEqual(ctx.exception.status, 400)

    def test_binary_and_oversize_are_refused(self):
        with self.assertRaises(ws.WorkspaceError) as ctx:
            self.ws.read_file(self.root, "image.bin")
        self.assertEqual(ctx.exception.status, 415)
        with self.assertRaises(ws.WorkspaceError) as ctx:
            self.ws.read_file(self.root, "bad.txt")
        self.assertEqual(ctx.exception.status, 415)
        with self.assertRaises(ws.WorkspaceError) as ctx:
            self.ws.write_file(self.root, {
                "path": "big.txt", "content": "x" * (ws.MAX_FILE_BYTES + 1), "create": True})
        self.assertEqual(ctx.exception.status, 413)

    def test_path_escapes_symlinks_and_absolute_paths(self):
        for value in ("../secret.txt", "src/../../secret.txt", "/etc/passwd",
                      "C:\\Windows\\win.ini", "", "src\\..\\..\\x"):
            with self.subTest(value=value):
                with self.assertRaises(ws.WorkspaceError) as ctx:
                    self.ws.read_file(self.root, value)
                self.assertEqual(ctx.exception.status, 400)
        if self.linked:
            for action in (
                lambda: self.ws.read_file(self.root, "escape.txt"),
                lambda: self.ws.write_file(self.root, {
                    "path": "escape.txt", "content": "x", "create": True}),
                lambda: self.ws.delete_path(self.root, {"path": "escape.txt"}),
            ):
                with self.assertRaises(ws.WorkspaceError) as ctx:
                    action()
                self.assertEqual(ctx.exception.status, 403)
        if self.inside_link:
            with self.assertRaises(ws.WorkspaceError) as ctx:
                self.ws.read_file(self.root, "inner-link.txt")
            self.assertEqual(ctx.exception.status, 403)
        with self.assertRaises(ws.WorkspaceError) as ctx:
            self.ws.write_file(self.root, {
                "path": "../../escape.txt", "content": "x", "create": True})
        self.assertEqual(ctx.exception.status, 400)
        # A missing file is only created explicitly.
        with self.assertRaises(ws.WorkspaceError) as ctx:
            self.ws.write_file(self.root, {"path": "new.txt", "content": "x"})
        self.assertEqual(ctx.exception.status, 404)

    # ----------------------------------------------------------------- diff
    def test_changes_and_diff_against_head(self):
        write_text(self.root / "src" / "app.py", "print('new')\nprint('more')\n")
        write_text(self.root / "untracked.txt", "one\ntwo\n")
        changes = self.ws.changes(self.root, "HEAD")
        self.assertTrue(changes["git"])
        by_path = {item["path"]: item for item in changes["files"]}
        self.assertIn("src/app.py", by_path)
        self.assertEqual(by_path["src/app.py"]["status"], "M")
        self.assertEqual(by_path["src/app.py"]["additions"], 2)
        self.assertEqual(by_path["src/app.py"]["deletions"], 1)
        self.assertIn("untracked.txt", by_path)
        self.assertTrue(by_path["untracked.txt"]["untracked"])
        detail = self.ws.diff_file(self.root, "src/app.py", "HEAD")
        self.assertTrue(detail["changed"])
        self.assertIn("-print('old')", detail["diff"])
        self.assertIn("+print('new')", detail["diff"])
        self.assertEqual((detail["additions"], detail["deletions"]), (2, 1))
        untracked = self.ws.diff_file(self.root, "untracked.txt", "HEAD")
        self.assertTrue(untracked["untracked"])
        self.assertIn("+two", untracked["diff"])
        # An explicit ref is accepted; an option-looking ref is not.
        self.assertTrue(self.ws.diff_file(self.root, "src/app.py", "HEAD~0")["changed"])
        for bad in ("--stat", "-p", "HEAD; rm -rf /", "does-not-exist"):
            with self.subTest(base=bad):
                with self.assertRaises(ws.WorkspaceError) as ctx:
                    self.ws.diff_file(self.root, "src/app.py", bad)
                self.assertEqual(ctx.exception.status, 400)
        # Unchanged file has a clean diff.
        unchanged = self.ws.diff_file(self.root, "docs/guide.md", "HEAD")
        self.assertFalse(unchanged["changed"])
        self.assertEqual(unchanged["diff"], "")

    def test_diff_untracked_and_binary(self):
        write_text(self.root / "new.md", "hello\n")
        (self.root / "image.bin").write_bytes(b"\x89PNG\x00\x02changed")
        diff = self.ws.diff_file(self.root, "new.md")
        self.assertTrue(diff["untracked"])
        self.assertIn("+hello", diff["diff"])
        binary = self.ws.diff_file(self.root, "image.bin")
        self.assertTrue(binary["binary"])
        self.assertEqual(binary["diff"], "")
        with self.assertRaises(ws.WorkspaceError) as ctx:
            self.ws.diff_file(self.root, "missing.txt")
        self.assertEqual(ctx.exception.status, 404)

    def test_changes_without_git_returns_clean_empty(self):
        plain = self.base / "plain"
        plain.mkdir()
        write_text(plain / "a.txt", "a")
        result = self.ws.changes(plain)
        self.assertFalse(result["git"])
        self.assertEqual(result["files"], [])
        with self.assertRaises(ws.WorkspaceError) as ctx:
            self.ws.diff_file(plain, "a.txt")
        self.assertEqual(ctx.exception.status, 400)


class TerminalTests(unittest.TestCase):
    def setUp(self):
        self.tmp = TempWorkspace()
        self.root = Path(self.tmp.name)
        (self.root / "sub").mkdir()
        write_text(self.root / "sub" / "marker.txt", "sub")
        self.manager = ws.TerminalManager()

    def tearDown(self):
        self.manager.shutdown()
        time.sleep(0.2)
        self.tmp.cleanup()

    def wait_for(self, terminal, predicate, timeout=15):
        deadline = time.time() + timeout
        snapshot = None
        while time.time() < deadline:
            snapshot = terminal.snapshot(0, 500)
            if predicate(snapshot):
                return snapshot
            time.sleep(0.08)
        self.fail(f"timed out waiting for terminal; last={snapshot!r}")

    def text(self, snapshot):
        return "".join(entry["text"] for entry in snapshot["entries"])

    def test_streams_output_and_accepts_stdin(self):
        terminal = self.manager.start(
            self.root, f'"{sys.executable}" -u -c '
                       '"import sys; print(\'ready\', flush=True); '
                       'print(\'got:\' + sys.stdin.readline().strip(), flush=True)"',
            timeout=20)
        ready = self.wait_for(terminal, lambda snap: "ready" in self.text(snap))
        self.assertIn(ready["status"], {"running", "stopping", "exited"})
        self.manager.write(terminal.id, "hello")
        final = self.wait_for(terminal, lambda snap: snap["status"] == "exited")
        self.assertEqual(final["exit_code"], 0)
        self.assertIn("got:hello", self.text(final))
        self.assertIn("› hello", self.text(final))

    def test_cwd_is_project_scoped(self):
        terminal = self.manager.start(
            self.root, f'"{sys.executable}" -c "import os; print(os.getcwd())"',
            cwd="sub", timeout=15)
        final = self.wait_for(terminal, lambda snap: snap["status"] == "exited")
        self.assertIn(str((self.root / "sub").resolve()), self.text(final))
        for value in ("..", "../", "C:\\Windows", "/etc"):
            with self.subTest(cwd=value):
                with self.assertRaises(ws.WorkspaceError) as ctx:
                    self.manager.start(self.root, "echo x", cwd=value)
                self.assertIn(ctx.exception.status, (400, 403))
        with self.assertRaises(ws.WorkspaceError) as ctx:
            self.manager.start(self.root, "echo x", cwd="missing")
        self.assertEqual(ctx.exception.status, 404)

    def test_cancel_and_timeout(self):
        terminal = self.manager.start(
            self.root, f'"{sys.executable}" -u -c "import time; '
                       'print(\'started\', flush=True); time.sleep(30)"', timeout=60)
        self.wait_for(terminal, lambda snap: "started" in self.text(snap))
        started = time.time()
        self.manager.cancel(terminal.id)
        final = self.wait_for(terminal, lambda snap: snap["status"] == "cancelled", timeout=15)
        self.assertLess(time.time() - started, 12)
        self.assertEqual(final["stop_reason"], "cancelled")
        sleeper = self.manager.start(
            self.root, f'"{sys.executable}" -u -c "import time; time.sleep(30)"', timeout=1)
        timed = self.wait_for(sleeper, lambda snap: snap["status"] == "timeout", timeout=15)
        self.assertEqual(timed["stop_reason"], "timeout")

    def test_output_cap_truncates_and_stops(self):
        with mock.patch.object(ws, "MAX_TERMINAL_OUTPUT", 800):
            terminal = self.manager.start(
                self.root, f'"{sys.executable}" -u -c "'
                           'print(\'x\' * 5000, flush=True)"', timeout=20)
            final = self.wait_for(
                terminal, lambda snap: snap["status"] in {"output_limit", "exited"})
        self.assertTrue(final["truncated"])
        # The cap applies to command output; the truncation marker is metadata.
        self.assertLessEqual(final["total_bytes"], 800 + 200)
        self.assertIn("output limit", self.text(final))
        self.assertEqual(final["stop_reason"], "output_limit")

    def test_rejects_bad_commands_and_bounds(self):
        with self.assertRaises(ws.WorkspaceError) as ctx:
            self.manager.start(self.root, "")
        self.assertEqual(ctx.exception.status, 400)
        with self.assertRaises(ws.WorkspaceError) as ctx:
            self.manager.start(self.root, "echo x\nrm -rf /")
        self.assertEqual(ctx.exception.status, 400)
        with self.assertRaises(ws.WorkspaceError) as ctx:
            self.manager.start(self.root, "x" * (ws.MAX_TERMINAL_COMMAND + 1))
        self.assertEqual(ctx.exception.status, 400)
        with mock.patch.object(ws, "MAX_TERMINALS", 1):
            first = self.manager.start(
                self.root, f'"{sys.executable}" -u -c "import time; time.sleep(30)"', timeout=60)
            try:
                with self.assertRaises(ws.WorkspaceError) as ctx:
                    self.manager.start(self.root, "echo second")
                self.assertEqual(ctx.exception.status, 409)
            finally:
                self.manager.cancel(first.id)
                self.wait_for(first, lambda snap: snap["status"] == "cancelled")
        self.manager.sweep()
        with self.assertRaises(ws.WorkspaceError) as ctx:
            self.manager.get("term_missing")
        self.assertEqual(ctx.exception.status, 404)

    def test_popen_uses_platform_shell_without_interpolation(self):
        with mock.patch.object(ws.platform_support, "run_command_args",
                               return_value=[sys.executable, "-c", "print('mocked')"]) as built:
            terminal = self.manager.start(self.root, "echo $HOME; whoami")
        built.assert_called_once_with("echo $HOME; whoami")
        final = self.wait_for(terminal, lambda snap: snap["status"] == "exited")
        self.assertIn("mocked", self.text(final))


class TerminalFallbackTests(unittest.TestCase):
    """PTY-unavailable and spawn-failure paths always fall back to stdio."""

    def setUp(self):
        self.tmp = TempWorkspace()
        self.root = Path(self.tmp.name)
        self.manager = ws.TerminalManager()

    def tearDown(self):
        self.manager.shutdown()
        time.sleep(0.2)
        self.tmp.cleanup()

    def text(self, snapshot):
        return "".join(entry["text"] for entry in snapshot["entries"])

    def wait_for(self, terminal, predicate, timeout=15):
        deadline = time.time() + timeout
        snapshot = None
        while time.time() < deadline:
            snapshot = terminal.snapshot(0, 500)
            if predicate(snapshot):
                return snapshot
            time.sleep(0.08)
        self.fail(f"timed out waiting for terminal; last={snapshot!r}")

    def test_unavailable_pty_reports_honestly_and_uses_stdio(self):
        unavailable = {
            "available": False,
            "backend": "",
            "version": "",
            "reason": "pywinpty is not installed",
            "hint": "pip install pywinpty",
        }
        with mock.patch.object(ws, "pty_availability", return_value=unavailable):
            terminal = self.manager.start(
                self.root,
                f'"{sys.executable}" -u -c "print(\'fallback-ready\', flush=True)"',
                pty=True, cols=100, rows=30, timeout=20)
            snapshot = terminal.public()
            self.assertFalse(snapshot["pty"])
            self.assertTrue(snapshot["pty_requested"])
            self.assertIn("pywinpty", snapshot["pty_fallback"])
            self.assertEqual((snapshot["cols"], snapshot["rows"]), (100, 30))
            final = self.wait_for(terminal, lambda snap: snap["status"] == "exited")
        self.assertIn("fallback-ready", self.text(final))

    def test_pty_spawn_error_falls_back_to_stdio(self):
        handle = "_WindowsPtyHandle" if os.name == "nt" else "_PosixPtyHandle"
        # Report a working backend so the spawn path runs whether or not
        # pywinpty is installed; without it start() never reaches the handle.
        available = {"available": True, "backend": "test", "version": "", "reason": "", "hint": ""}
        with mock.patch.object(ws, "pty_availability", return_value=available),                 mock.patch.object(ws, handle, side_effect=OSError("backend exploded")):
            terminal = self.manager.start(
                self.root,
                f'"{sys.executable}" -u -c "print(\'stdio-after-error\', flush=True)"',
                pty=True, timeout=20)
            snapshot = terminal.public()
            self.assertFalse(snapshot["pty"])
            self.assertTrue(snapshot["pty_requested"])
            self.assertIn("backend exploded", snapshot["pty_fallback"])
            final = self.wait_for(terminal, lambda snap: snap["status"] == "exited")
        self.assertIn("stdio-after-error", self.text(final))

    def test_terminal_env_scrubs_tokens_and_switches_term(self):
        with mock.patch.dict(os.environ, {
                "ARK_WRITE_TOKEN": "super-secret",
                "ARK_READ_TOKEN": "also-secret",
                "NO_COLOR": "1",
                "CLICOLOR": "1",
        }):
            pty_env = ws._terminal_env(pty=True)
            stdio_env = ws._terminal_env(pty=False)
        self.assertNotIn("ARK_WRITE_TOKEN", pty_env)
        self.assertNotIn("ARK_READ_TOKEN", pty_env)
        self.assertEqual(pty_env["TERM"], "xterm-256color")
        self.assertNotIn("NO_COLOR", pty_env)
        self.assertEqual(stdio_env["TERM"], "dumb")
        self.assertEqual(stdio_env["NO_COLOR"], "1")

    def test_info_reports_pty_unavailable_with_hint(self):
        unavailable = {
            "available": False,
            "backend": "",
            "version": "",
            "reason": "pywinpty is not installed",
            "hint": "Install pywinpty: pip install pywinpty",
        }
        with mock.patch.object(ws, "pty_availability", return_value=unavailable):
            workspace = ws.Workspace()
            try:
                info = workspace.info(self.root)
            finally:
                workspace.shutdown()
        self.assertFalse(info["pty"])
        self.assertFalse(info["pty_available"])
        self.assertEqual(info["pty_reason"], "pywinpty is not installed")
        self.assertIn("pip install pywinpty", info["pty_hint"])
        self.assertEqual(info["pty_backend"], "")


@unittest.skipUnless(ws.pty_availability()["available"], "no interactive PTY backend")
class PtyTerminalTests(unittest.TestCase):
    """Real PTY sessions: open/read/write/resize/cancel/timeout/output cap."""

    def setUp(self):
        self.tmp = TempWorkspace()
        self.root = Path(self.tmp.name)
        self.manager = ws.TerminalManager()

    def tearDown(self):
        self.manager.shutdown()
        time.sleep(0.3)
        self.tmp.cleanup()

    def wait_for(self, terminal, predicate, timeout=20):
        deadline = time.time() + timeout
        snapshot = None
        while time.time() < deadline:
            snapshot = terminal.snapshot(0, 1000)
            if predicate(snapshot):
                return snapshot
            time.sleep(0.08)
        self.fail(f"timed out waiting for PTY terminal; last={snapshot!r}")

    def text(self, snapshot):
        return "".join(entry["text"] for entry in snapshot["entries"])

    def test_pty_metadata_and_availability(self):
        availability = ws.pty_availability()
        self.assertTrue(availability["available"])
        self.assertIn(availability["backend"], {"pywinpty", "posix"})
        terminal = self.manager.start(
            self.root,
            f'"{sys.executable}" -u -c "print(\'pty-meta\', flush=True)"',
            pty=True, cols=97, rows=33, timeout=20)
        snapshot = terminal.public()
        self.assertTrue(snapshot["pty"])
        self.assertTrue(snapshot["pty_requested"])
        self.assertEqual(snapshot["pty_backend"], availability["backend"])
        self.assertEqual(snapshot["pty_fallback"], "")
        self.assertEqual((snapshot["cols"], snapshot["rows"]), (97, 33))
        final = self.wait_for(terminal, lambda snap: snap["status"] == "exited")
        self.assertIn("pty-meta", self.text(final))

    def test_pty_streams_output_accepts_stdin_and_resizes(self):
        code = ("import os, sys; print('size1', os.get_terminal_size().columns, "
                "os.get_terminal_size().lines, flush=True); "
                "print(chr(27) + '[31mpty-red' + chr(27) + '[0m', flush=True); "
                "sys.stdin.readline(); "
                "print('size2', os.get_terminal_size().columns, "
                "os.get_terminal_size().lines, flush=True)")
        terminal = self.manager.start(
            self.root, f'"{sys.executable}" -u -c "{code}"',
            pty=True, cols=100, rows=30, timeout=25)
        started = self.wait_for(
            terminal, lambda snap: "size1 100 30" in self.text(snap))
        self.assertIn("100 30", self.text(started))
        resized = self.manager.resize(terminal.id, 120, 40)
        self.assertTrue(resized["resized"])
        self.assertEqual(
            (resized["terminal"]["cols"], resized["terminal"]["rows"]), (120, 40))
        self.manager.write(terminal.id, "go")
        final = self.wait_for(
            terminal, lambda snap: "size2 120 40" in self.text(snap))
        self.assertIn("120 40", self.text(final))
        final = self.wait_for(terminal, lambda snap: snap["status"] == "exited")
        raw = self.text(final)
        # PTY output is streamed raw (CR/escape sequences included), unlike the
        # normalized stdio path.
        self.assertIn("\r", raw)
        self.assertIn("\x1b[31m", raw)

    def test_python_repl_runs_interactively(self):
        terminal = self.manager.start(
            self.root, f'"{sys.executable}" -u -i',
            pty=True, cols=80, rows=24, timeout=30)
        self.wait_for(terminal, lambda snap: ">>>" in self.text(snap), timeout=25)
        self.manager.write(terminal.id, "6*7")
        result = self.wait_for(terminal, lambda snap: "42" in self.text(snap))
        self.assertIn("42", self.text(result))
        self.manager.write(terminal.id, "exit()")
        final = self.wait_for(terminal, lambda snap: snap["status"] == "exited")
        self.assertEqual(final["exit_code"], 0)

    def test_pty_cancel_timeout_and_output_cap(self):
        terminal = self.manager.start(
            self.root,
            f'"{sys.executable}" -u -c "import time; '
            'print(\'pty-cancel-ready\', flush=True); time.sleep(30)"',
            pty=True, timeout=60)
        self.wait_for(
            terminal, lambda snap: "pty-cancel-ready" in self.text(snap))
        started = time.time()
        self.manager.cancel(terminal.id)
        cancelled = self.wait_for(
            terminal, lambda snap: snap["status"] == "cancelled", timeout=15)
        self.assertLess(time.time() - started, 12)
        self.assertEqual(cancelled["stop_reason"], "cancelled")

        sleeper = self.manager.start(
            self.root,
            f'"{sys.executable}" -u -c "import time; time.sleep(30)"',
            pty=True, timeout=1)
        timed = self.wait_for(sleeper, lambda snap: snap["status"] == "timeout")
        self.assertEqual(timed["stop_reason"], "timeout")

        with mock.patch.object(ws, "MAX_TERMINAL_OUTPUT", 1500):
            noisy = self.manager.start(
                self.root,
                f'"{sys.executable}" -u -c "print(\'x\' * 8000, flush=True)"',
                pty=True, timeout=20)
            capped = self.wait_for(
                noisy, lambda snap: snap["status"] in {"output_limit", "exited"})
        self.assertTrue(capped["truncated"])
        self.assertLessEqual(capped["total_bytes"], 1500 + 200)
        self.assertEqual(capped["stop_reason"], "output_limit")

    def test_geometry_validation_and_stdio_resize_noop(self):
        for cols, rows in ((0, 24), (80, 0), (ws.TERMINAL_MAX_COLS + 1, 24),
                           (80, ws.TERMINAL_MAX_ROWS + 1), ("wide", 24)):
            with self.subTest(cols=cols, rows=rows):
                with self.assertRaises(ws.WorkspaceError) as ctx:
                    self.manager.start(self.root, "echo x", pty=True,
                                       cols=cols, rows=rows)
                self.assertEqual(ctx.exception.status, 400)
        stdio = self.manager.start(
            self.root,
            f'"{sys.executable}" -u -c "print(\'stdio-size\', flush=True)"',
            pty=False, cols=90, rows=20, timeout=20)
        snapshot = stdio.public()
        self.assertFalse(snapshot["pty"])
        self.assertEqual((snapshot["cols"], snapshot["rows"]), (90, 20))
        resized = self.manager.resize(stdio.id, 110, 28)
        self.assertFalse(resized["resized"])
        self.assertIn("not a PTY", resized["reason"])
        self.assertEqual(
            (resized["terminal"]["cols"], resized["terminal"]["rows"]), (110, 28))
        self.wait_for(stdio, lambda snap: snap["status"] == "exited")
        with self.assertRaises(ws.WorkspaceError) as ctx:
            self.manager.resize(stdio.id, 0, 24)
        self.assertEqual(ctx.exception.status, 400)
        with self.assertRaises(ws.WorkspaceError) as ctx:
            self.manager.resize(stdio.id, 80, 24)  # finished session
        self.assertEqual(ctx.exception.status, 409)


@unittest.skipUnless(os.name == "posix" and ws.pty_availability()["available"],
                     "POSIX pty is unavailable")
class PosixPtyTests(unittest.TestCase):
    def setUp(self):
        self.tmp = TempWorkspace()
        self.root = Path(self.tmp.name)
        self.manager = ws.TerminalManager()

    def tearDown(self):
        self.manager.shutdown()
        time.sleep(0.2)
        self.tmp.cleanup()

    def test_posix_pty_has_a_controlling_terminal(self):
        code = ("import os; print('ctty', os.isatty(0), "
                "os.getsid(0) == os.getpid(), os.tcgetpgrp(0) == os.getpid(), "
                "flush=True)")
        terminal = self.manager.start(
            self.root, f'exec "{sys.executable}" -u -c "{code}"',
            pty=True, cols=80, rows=24, timeout=20)
        deadline = time.time() + 15
        snapshot = None
        while time.time() < deadline:
            snapshot = terminal.snapshot(0, 500)
            if "ctty True True True" in "".join(
                    entry["text"] for entry in snapshot["entries"]):
                break
            time.sleep(0.08)
        text = "".join(entry["text"] for entry in snapshot["entries"])
        self.assertIn("ctty True True True", text)


@unittest.skipUnless(os.name == "nt" and ws.pty_availability()["available"],
                     "pywinpty is unavailable")
class WindowsPtyTests(unittest.TestCase):
    def setUp(self):
        self.tmp = TempWorkspace()
        self.root = Path(self.tmp.name)
        self.manager = ws.TerminalManager()

    def tearDown(self):
        self.manager.shutdown()
        time.sleep(0.2)
        self.tmp.cleanup()

    def test_pywinpty_backend_and_quoted_command(self):
        command = (f'"{sys.executable}" -u -c "'
                   'import sys; print(\'winpty-quoted\', flush=True)"')
        terminal = self.manager.start(
            self.root, command, pty=True, cols=100, rows=30, timeout=20)
        snapshot = terminal.public()
        self.assertEqual(snapshot["pty_backend"], "pywinpty")
        deadline = time.time() + 15
        while time.time() < deadline and "winpty-quoted" not in "".join(
                entry["text"] for entry in snapshot["entries"]):
            snapshot = terminal.snapshot(0, 500)
            time.sleep(0.08)
        self.assertIn("winpty-quoted",
                      "".join(entry["text"] for entry in snapshot["entries"]))

@unittest.skipUnless(GIT, "git is required for diff API tests")
class WorkspaceApiTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.tmp = TempWorkspace()
        root = Path(cls.tmp.name)
        cls.app = App(root / "projects", seed_demo=False,
                      task_sessions_root=root / "agent-sessions")
        cls.root = cls.app.project.path
        (cls.root / "src").mkdir(exist_ok=True)
        write_text(cls.root / "src" / "main.py", "print('old')\n")
        write_text(cls.root / "notes.txt", "hello\nworld\n")
        git(cls.root, "init", "-q")
        git(cls.root, "add", "-A")
        git(cls.root, "commit", "-qm", "initial")
        write_text(cls.root / "src" / "main.py", "print('new')\n")
        cls.httpd = serve(cls.app, port=0, block=False)
        cls.base = f"http://127.0.0.1:{cls.httpd.server_address[1]}"

    @classmethod
    def tearDownClass(cls):
        cls.app.workspace.shutdown()
        cls.httpd.shutdown()
        cls.httpd.server_close()
        cls.app.project.close()
        cls.tmp.cleanup()

    def request(self, path, *, method="GET", body=None, headers=None, raw=False):
        data = json.dumps(body).encode() if body is not None else None
        req = urllib.request.Request(
            self.base + path, data=data, method=method,
            headers={"Content-Type": "application/json", **(headers or {})})
        try:
            with urllib.request.urlopen(req, timeout=15) as response:
                payload = response.read()
                ctype = response.headers.get("Content-Type", "")
                if raw:
                    return response.status, payload, ctype
                return response.status, json.loads(payload)
        except urllib.error.HTTPError as exc:
            payload = exc.read()
            if raw:
                return exc.code, payload, exc.headers.get("Content-Type", "")
            return exc.code, json.loads(payload or b"{}")

    def test_get_tree_file_changes_and_diff(self):
        status, info = self.request("/api/workspace/info")
        self.assertEqual(status, 200)
        self.assertTrue(info["git"]["is_repo"])
        self.assertEqual(info["pty"], info["pty_available"])
        self.assertIn(info["pty_backend"], {"", "pywinpty", "posix"})
        self.assertIn("pty_hint", info)
        status, tree = self.request("/api/workspace/tree")
        self.assertEqual(status, 200)
        self.assertIn("src", {entry["name"] for entry in tree["entries"]})
        status, file = self.request("/api/workspace/file?path=notes.txt")
        self.assertEqual(status, 200)
        self.assertIn("hello", file["content"])
        status, changes = self.request("/api/workspace/changes?base=HEAD")
        self.assertEqual(status, 200)
        self.assertEqual([item["path"] for item in changes["files"]], ["src/main.py"])
        status, diff = self.request("/api/workspace/diff?path=src/main.py&base=HEAD")
        self.assertEqual(status, 200)
        self.assertIn("+print('new')", diff["diff"])
        self.assertEqual((diff["additions"], diff["deletions"]), (1, 1))
        status, missing = self.request("/api/workspace/file?path=missing.txt")
        self.assertEqual(status, 404)

    def test_write_create_rename_delete_and_conflicts(self):
        status, file = self.request("/api/workspace/file?path=notes.txt")
        self.assertEqual(status, 200)
        status, saved = self.request("/api/workspace/file", method="POST", body={
            "path": "notes.txt", "content": "hello\nARK\n", "base_hash": file["hash"]})
        self.assertEqual(status, 200)
        self.assertEqual(saved["lines"], 2)
        status, conflict = self.request("/api/workspace/file", method="POST", body={
            "path": "notes.txt", "content": "stale", "base_hash": file["hash"]})
        self.assertEqual(status, 409)
        status, created = self.request("/api/workspace/create", method="POST", body={
            "path": "src/created.txt", "type": "file", "content": "created"})
        self.assertEqual(status, 200)
        status, renamed = self.request("/api/workspace/rename", method="POST", body={
            "from": "src/created.txt", "to": "src/renamed.txt"})
        self.assertEqual(status, 200)
        self.assertEqual(renamed["to"], "src/renamed.txt")
        status, deleted = self.request("/api/workspace/delete", method="POST", body={
            "path": "src/renamed.txt"})
        self.assertEqual(status, 200)
        self.assertEqual(deleted["removed"], 1)

    def test_bad_paths_and_binary_are_clean_errors(self):
        for path in ("../secret.txt", "C:\\Windows\\win.ini", "/etc/passwd"):
            for endpoint in ("/api/workspace/file?path=" + urllib.parse.quote(path, safe=""),
                             "/api/workspace/diff?path=" + urllib.parse.quote(path, safe="")):
                status, data = self.request(endpoint)
                self.assertIn(status, (400, 403))
        status, data = self.request("/api/workspace/file?path=ark.sqlite3")
        self.assertEqual(status, 403)

    def test_read_token_cannot_mutate_workspace(self):
        with mock.patch.object(self.app.sessions, "session_for_read_token",
                               return_value=object()):
            status, data = self.request(
                "/api/workspace/file", method="POST",
                body={"path": "x.txt", "content": "x", "create": True},
                headers={"X-ARK-Read-Token": "read-token"})
        self.assertEqual(status, 403)
        self.assertIn("read tokens", data["error"]["message"])
        self.assertFalse((self.root / "x.txt").exists())

    def test_terminal_poll_stdin_sse_and_cancel(self):
        status, started = self.request("/api/workspace/terminals", method="POST", body={
            "command": f'"{sys.executable}" -u -c "import sys; print(\'ready\', flush=True); '
                       'print(\'got:\' + sys.stdin.readline().strip(), flush=True)"',
            "timeout": 20,
        })
        self.assertEqual(status, 200)
        terminal = started["terminal"]
        self.assertEqual(terminal["cwd"], "")
        deadline = time.time() + 15
        text = ""
        while time.time() < deadline:
            status, polled = self.request(
                f"/api/workspace/terminals/{terminal['id']}?offset=0")
            self.assertEqual(status, 200)
            current = polled["terminal"]
            text = "".join(entry["text"] for entry in current["entries"])
            if "ready" in text:
                break
            time.sleep(0.1)
        self.assertIn("ready", text)
        status, _ = self.request(
            f"/api/workspace/terminals/{terminal['id']}/stdin", method="POST",
            body={"text": "api-hello"})
        self.assertEqual(status, 200)
        deadline = time.time() + 15
        while time.time() < deadline:
            status, polled = self.request(
                f"/api/workspace/terminals/{terminal['id']}?offset=0")
            current = polled["terminal"]
            text = "".join(entry["text"] for entry in current["entries"])
            if current["status"] == "exited":
                break
            time.sleep(0.1)
        self.assertEqual(current["status"], "exited")
        self.assertIn("got:api-hello", text)

        # A second terminal streams its output over SSE until it exits.
        status, started = self.request("/api/workspace/terminals", method="POST", body={
            "command": f'"{sys.executable}" -u -c "import time; '
                       'print(\'sse-ready\', flush=True); time.sleep(1.2)"',
            "timeout": 30,
        })
        self.assertEqual(status, 200)
        term_id = started["terminal"]["id"]
        req = urllib.request.Request(
            self.base + f"/api/workspace/terminals/{term_id}/stream?offset=0")
        events = []
        output_text = ""
        with urllib.request.urlopen(req, timeout=15) as response:
            self.assertIn("text/event-stream", response.headers.get("Content-Type", ""))
            for raw in response:
                line = raw.decode("utf-8").strip()
                if line.startswith("event:"):
                    events.append(line.split(":", 1)[1].strip())
                    if line.endswith("done"):
                        break
                elif line.startswith("data:"):
                    data = json.loads(line.split(":", 1)[1].strip())
                    if "text" in data:
                        output_text += data["text"]
        self.assertIn("output", events)
        self.assertIn("done", events)
        self.assertIn("sse-ready", output_text)

        # A long-running terminal can be cancelled and then closed.
        status, started = self.request("/api/workspace/terminals", method="POST", body={
            "command": f'"{sys.executable}" -u -c "import time; time.sleep(30)"',
            "timeout": 60,
        })
        self.assertEqual(status, 200)
        term_id = started["terminal"]["id"]
        status, cancelled = self.request(
            f"/api/workspace/terminals/{term_id}/cancel", method="POST", body={})
        self.assertEqual(status, 200)
        deadline = time.time() + 15
        current = None
        while time.time() < deadline:
            status, polled = self.request(
                f"/api/workspace/terminals/{term_id}?offset=0")
            current = polled["terminal"]
            if current["status"] == "cancelled":
                break
            time.sleep(0.1)
        self.assertEqual(current["status"], "cancelled")
        status, closed = self.request(
            f"/api/workspace/terminals/{term_id}", method="DELETE")
        self.assertEqual(status, 200)
        self.assertEqual(closed["closed"], term_id)

    def test_terminal_pty_resize_api_and_validation(self):
        status, info = self.request("/api/workspace/info")
        self.assertEqual(status, 200)
        available = bool(info["pty_available"])
        command = (f'"{sys.executable}" -u -c "import time; '
                   'print(\'api-pty-ready\', flush=True); time.sleep(30)"')
        status, started = self.request("/api/workspace/terminals", method="POST", body={
            "command": command, "timeout": 60, "pty": True, "cols": 100, "rows": 30,
        })
        self.assertEqual(status, 200)
        self.assertEqual(started["pty_available"], available)
        terminal = started["terminal"]
        term_id = terminal["id"]
        try:
            self.assertEqual(terminal["pty_requested"], True)
            self.assertEqual((terminal["cols"], terminal["rows"]), (100, 30))
            if available:
                self.assertTrue(terminal["pty"])
                self.assertIn(terminal["pty_backend"], {"pywinpty", "posix"})
            else:
                self.assertFalse(terminal["pty"])
                self.assertTrue(terminal["pty_fallback"])
            deadline = time.time() + 15
            polled = None
            while time.time() < deadline:
                status, polled = self.request(
                    f"/api/workspace/terminals/{term_id}?offset=0")
                text = "".join(entry["text"] for entry in polled["terminal"]["entries"])
                if "api-pty-ready" in text:
                    break
                time.sleep(0.1)
            self.assertIn("api-pty-ready", text)
            status, resized = self.request(
                f"/api/workspace/terminals/{term_id}/resize", method="POST",
                body={"cols": 120, "rows": 40})
            self.assertEqual(status, 200)
            self.assertEqual(resized["resized"], available)
            if available:
                self.assertEqual(
                    (resized["terminal"]["cols"], resized["terminal"]["rows"]), (120, 40))
            status, invalid = self.request(
                f"/api/workspace/terminals/{term_id}/resize", method="POST",
                body={"cols": 0, "rows": 0})
            self.assertEqual(status, 400)
            status, _ = self.request(
                f"/api/workspace/terminals/{term_id}/cancel", method="POST", body={})
            self.assertEqual(status, 200)
            deadline = time.time() + 15
            while time.time() < deadline:
                status, polled = self.request(
                    f"/api/workspace/terminals/{term_id}?offset=0")
                if polled["terminal"]["status"] == "cancelled":
                    break
                time.sleep(0.1)
            self.assertEqual(polled["terminal"]["status"], "cancelled")
        finally:
            self.request(f"/api/workspace/terminals/{term_id}", method="DELETE")

    def test_terminal_input_validation_and_cwd(self):
        status, data = self.request("/api/workspace/terminals", method="POST", body={
            "command": "", "cwd": ""})
        self.assertEqual(status, 400)
        status, data = self.request("/api/workspace/terminals", method="POST", body={
            "command": "echo x", "cwd": "../"})
        self.assertIn(status, (400, 403))
        status, data = self.request("/api/workspace/terminals", method="POST", body={
            "command": "echo x", "cwd": "missing"})
        self.assertEqual(status, 404)


if __name__ == "__main__":
    unittest.main()
