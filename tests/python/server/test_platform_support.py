import os
import shutil
import signal
import subprocess
import sys
import tempfile
import threading
import unittest
import webbrowser
from pathlib import Path
from unittest import mock

from arkstudio import platform_support


class _ModuleView:
    """platform_support's private view of a shared module.

    The tests patch sys.platform, os.environ, subprocess.run, shutil.which,
    os.killpg, ... as platform_support sees them.  Patching the real modules
    would change them for every thread in the process: a server or session
    thread left running by an earlier test could then call the mocked
    subprocess.run (failing assert_not_called), or see sys.platform == "linux"
    and an empty environment.  Attributes set here shadow the real module only
    inside platform_support; everything else is forwarded.
    """

    def __init__(self, real, **overrides):
        self._real = real
        self.__dict__.update(overrides)

    def __getattr__(self, name):
        return getattr(self._real, name)


def setUpModule():
    views = {"os": _ModuleView(os, environ=dict(os.environ)), "sys": _ModuleView(sys),
             "shutil": _ModuleView(shutil), "subprocess": _ModuleView(subprocess),
             "webbrowser": _ModuleView(webbrowser)}
    for name, view in views.items():
        patcher = mock.patch.object(platform_support, name, view)
        patcher.start()
        unittest.addModuleCleanup(patcher.stop)


class SafePathTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.base = Path(self.tmp.name)
        self.root = self.base / "project.ark"
        self.root.mkdir()
        self.file = self.root / "assets" / "figure.pdf"
        self.file.parent.mkdir()
        self.file.write_text("pdf", encoding="utf-8")

    def tearDown(self):
        self.tmp.cleanup()

    def test_relative_file_inside_root(self):
        target = platform_support.safe_path(self.root, "assets/figure.pdf")
        self.assertEqual(target, self.file.resolve())
        self.assertTrue(platform_support.is_within(self.root, target))

    def test_traversal_absolute_and_windows_paths_are_rejected(self):
        for value in ("../secret.txt", "assets/../../secret.txt", "/etc/passwd",
                      "C:\\Windows\\win.ini", "\\\\server\\share\\file.txt", ""):
            with self.subTest(value=value):
                with self.assertRaises(platform_support.PlatformError):
                    platform_support.safe_path(self.root, value)

    def test_absolute_outside_and_inside_are_distinct(self):
        outside = self.base / "outside.txt"
        outside.write_text("no", encoding="utf-8")
        with self.assertRaises(platform_support.PlatformError) as ctx:
            platform_support.ensure_within(self.root, outside)
        self.assertEqual(ctx.exception.status, 403)
        inside = platform_support.ensure_within(self.root, self.file)
        self.assertEqual(inside, self.file.resolve())

    def test_symlink_escape_is_rejected(self):
        outside = self.base / "outside.txt"
        outside.write_text("no", encoding="utf-8")
        link = self.root / "escape.txt"
        try:
            link.symlink_to(outside)
        except (OSError, NotImplementedError):
            self.skipTest("symlinks are unavailable for this user")
        with self.assertRaises(platform_support.PlatformError) as ctx:
            platform_support.safe_path(self.root, "escape.txt")
        self.assertEqual(ctx.exception.status, 403)

    def test_symlink_target_is_rejected_without_creating_one(self):
        real_is_symlink = Path.is_symlink

        def fake_is_symlink(self):
            return self.name == "escape.txt" or real_is_symlink(self)

        with mock.patch.object(Path, "is_symlink", autospec=True,
                               side_effect=fake_is_symlink):
            with self.assertRaises(platform_support.PlatformError) as ctx:
                platform_support.safe_path(self.root, "escape.txt")
        self.assertEqual(ctx.exception.status, 403)

    def test_file_url_conversion_and_media_resolution(self):
        parsed = platform_support.path_from_file_url("file:///C:/tmp/a.txt")
        self.assertEqual(str(parsed).replace("\\", "/"), "C:/tmp/a.txt")
        media = platform_support.resolve_media_path(self.root, "assets/figure.pdf")
        self.assertEqual(media, self.file.resolve())
        media = platform_support.resolve_media_path(self.root, self.file.as_uri())
        self.assertEqual(media, self.file.resolve())
        with self.assertRaises(platform_support.PlatformError):
            platform_support.resolve_media_path(self.root, "https://example.org/a.png")
        outside = self.base / "outside.txt"
        outside.write_text("no", encoding="utf-8")
        with self.assertRaises(platform_support.PlatformError) as ctx:
            platform_support.resolve_media_path(self.root, outside.as_uri())
        self.assertEqual(ctx.exception.status, 403)


class OpenExternalTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.file = Path(self.tmp.name) / "report.txt"
        self.file.write_text("report", encoding="utf-8")

    def tearDown(self):
        self.tmp.cleanup()

    def test_windows_uses_startfile(self):
        with mock.patch.object(platform_support.sys, "platform", "win32"), \
                mock.patch.object(platform_support.os, "startfile", create=True) as startfile, \
                mock.patch.object(platform_support.subprocess, "run") as run:
            result = platform_support.open_external(self.file)
        startfile.assert_called_once_with(str(self.file.resolve()))
        run.assert_not_called()
        self.assertEqual(result["opener"], "os.startfile")
        self.assertEqual(result["platform"], "windows")
        self.assertTrue(result["opened"])

    def test_windows_explorer_fallback(self):
        with mock.patch.object(platform_support.sys, "platform", "win32"), \
                mock.patch.object(platform_support.os, "startfile", create=True,
                                  side_effect=OSError("no association")), \
                mock.patch.object(platform_support.shutil, "which",
                                  return_value="C:\\Windows\\explorer.exe"), \
                mock.patch.object(platform_support.subprocess, "run",
                                  return_value=mock.Mock(returncode=1)) as run:
            result = platform_support.open_external(self.file)
        self.assertEqual(result["opener"], "explorer")
        self.assertEqual(run.call_args.args[0][0], "C:\\Windows\\explorer.exe")

    def test_macos_uses_open(self):
        with mock.patch.object(platform_support.sys, "platform", "darwin"), \
                mock.patch.object(platform_support.shutil, "which",
                                  side_effect=lambda name: "/usr/bin/open" if name == "open" else None), \
                mock.patch.object(platform_support.subprocess, "run",
                                  return_value=mock.Mock(returncode=0)) as run:
            result = platform_support.open_external(self.file)
        self.assertEqual(result["opener"], "open")
        self.assertEqual(run.call_args.args[0], ["/usr/bin/open", str(self.file.resolve())])

    def test_linux_prefers_xdg_open(self):
        with mock.patch.object(platform_support.sys, "platform", "linux"), \
                mock.patch.dict(platform_support.os.environ, {"DISPLAY": ":0"}, clear=False), \
                mock.patch.object(platform_support.shutil, "which",
                                  side_effect=lambda name: "/usr/bin/xdg-open" if name == "xdg-open" else None), \
                mock.patch.object(platform_support.subprocess, "run",
                                  return_value=mock.Mock(returncode=0)) as run:
            result = platform_support.open_external(self.file)
        self.assertEqual(result["opener"], "xdg-open")
        self.assertEqual(run.call_args.args[0][0], "/usr/bin/xdg-open")

    def test_linux_falls_back_to_gio(self):
        with mock.patch.object(platform_support.sys, "platform", "linux"), \
                mock.patch.dict(platform_support.os.environ, {"WAYLAND_DISPLAY": "wayland-0"}, clear=False), \
                mock.patch.object(platform_support.shutil, "which",
                                  side_effect=lambda name: "/usr/bin/gio" if name == "gio" else None), \
                mock.patch.object(platform_support.subprocess, "run",
                                  return_value=mock.Mock(returncode=0)) as run:
            result = platform_support.open_external(self.file)
        self.assertEqual(result["opener"], "gio open")
        self.assertEqual(run.call_args.args[0], ["/usr/bin/gio", "open", str(self.file.resolve())])

    def test_patches_do_not_leak_to_other_threads(self):
        real_platform = sys.platform
        with mock.patch.object(platform_support.subprocess, "run") as run,                 mock.patch.object(platform_support.sys, "platform", "linux"),                 mock.patch.dict(platform_support.os.environ, {}, clear=True):
            self.assertIsNot(subprocess.run, run)
            self.assertEqual(sys.platform, real_platform)
            self.assertIn("PATH", {key.upper() for key in os.environ})
            other = threading.Thread(target=subprocess.run, args=([sys.executable, "-c", "pass"],))
            other.start()
            other.join(30)
            run.assert_not_called()

    def test_headless_and_missing_opener_are_clean_errors(self):
        with mock.patch.object(platform_support.sys, "platform", "linux"), \
                mock.patch.dict(platform_support.os.environ, {}, clear=True), \
                mock.patch.object(platform_support.shutil, "which",
                                  return_value="/usr/bin/xdg-open"), \
                mock.patch.object(platform_support.subprocess, "run") as run:
            with self.assertRaises(platform_support.PlatformError) as ctx:
                platform_support.open_external(self.file)
            run.assert_not_called()
        self.assertIn("graphical session", str(ctx.exception))
        with mock.patch.object(platform_support.sys, "platform", "linux"), \
                mock.patch.dict(platform_support.os.environ, {"DISPLAY": ":0"}, clear=True), \
                mock.patch.object(platform_support.shutil, "which", return_value=None), \
                mock.patch.object(platform_support.subprocess, "run") as run:
            with self.assertRaises(platform_support.PlatformError) as ctx:
                platform_support.open_external(self.file)
            run.assert_not_called()
        self.assertIn("xdg-open", str(ctx.exception))

    def test_default_browser_open(self):
        with mock.patch.object(platform_support.webbrowser, "open",
                               return_value=True) as opener:
            result = platform_support.default_browser_open("https://example.org/paper")
        self.assertEqual(result["opener"], "webbrowser")
        opener.assert_called_once_with("https://example.org/paper", new=2)
        with self.assertRaises(platform_support.PlatformError):
            platform_support.default_browser_open("file:///etc/passwd")
        with mock.patch.object(platform_support.webbrowser, "open", return_value=False):
            with self.assertRaises(platform_support.PlatformError):
                platform_support.default_browser_open("https://example.org/")


class ProcessTreeTests(unittest.TestCase):
    def test_windows_taskkill(self):
        with mock.patch.object(platform_support.sys, "platform", "win32"), \
                mock.patch.object(platform_support.shutil, "which",
                                  return_value="C:\\Windows\\System32\\taskkill.exe"), \
                mock.patch.object(platform_support.subprocess, "run",
                                  return_value=mock.Mock(returncode=0)) as run:
            result = platform_support.kill_process_tree(4321)
        self.assertTrue(result["killed"])
        self.assertEqual(result["method"], "taskkill")
        self.assertEqual(
            run.call_args.args[0],
            ["C:\\Windows\\System32\\taskkill.exe", "/PID", "4321", "/T", "/F"])

    def test_posix_escalates_to_sigkill(self):
        with mock.patch.object(platform_support.sys, "platform", "linux"), \
                mock.patch.object(platform_support.os, "getpgid", create=True,
                                  return_value=4242), \
                mock.patch.object(platform_support.os, "getpgrp", create=True,
                                  return_value=100), \
                mock.patch.object(platform_support.os, "killpg", create=True) as killpg, \
                mock.patch.object(platform_support, "_group_alive", return_value=True):
            result = platform_support.kill_process_tree(4321, term_wait=0)
        self.assertTrue(result["escalated"])
        self.assertEqual(result["pgid"], 4242)
        kill_signal = getattr(signal, "SIGKILL", signal.SIGTERM)
        self.assertEqual(killpg.call_args_list,
                         [mock.call(4242, signal.SIGTERM), mock.call(4242, kill_signal)])

    def test_posix_refuses_arks_own_group_and_missing_pid(self):
        with mock.patch.object(platform_support.sys, "platform", "linux"), \
                mock.patch.object(platform_support.os, "getpgid", create=True,
                                  return_value=100), \
                mock.patch.object(platform_support.os, "getpgrp", create=True,
                                  return_value=100), \
                mock.patch.object(platform_support.os, "killpg", create=True) as killpg:
            with self.assertRaises(platform_support.PlatformError):
                platform_support.kill_process_tree(100)
            killpg.assert_not_called()
        with mock.patch.object(platform_support.sys, "platform", "linux"), \
                mock.patch.object(platform_support.os, "getpgid", create=True,
                                  side_effect=ProcessLookupError):
            result = platform_support.kill_process_tree(99999)
        self.assertFalse(result["killed"])


class ShellCommandTests(unittest.TestCase):
    def test_windows_commands(self):
        with mock.patch.object(platform_support.sys, "platform", "win32"):
            self.assertEqual(platform_support.shell_command(),
                             ["powershell", "-NoLogo", "-NoProfile", "-Command", "-"])
            self.assertEqual(platform_support.shell_command("dir"),
                             ["cmd.exe", "/d", "/s", "/c", "dir"])

    def test_posix_commands_use_shell_and_lc(self):
        with mock.patch.object(platform_support.sys, "platform", "linux"), \
                mock.patch.object(platform_support.shutil, "which", return_value=None):
            self.assertEqual(platform_support.shell_command(env={"SHELL": "/bin/sh"}),
                             ["/bin/sh", "-lc", "exec /bin/sh -l"])
            self.assertEqual(platform_support.shell_command("echo hi", env={"SHELL": "/bin/sh"}),
                             ["/bin/sh", "-lc", "echo hi"])

    def test_run_command_args_never_splices_user_text(self):
        # POSIX: the command is one argv element, not interpolated by ARK.
        with mock.patch.object(platform_support.sys, "platform", "linux"), \
                mock.patch.dict(platform_support.os.environ, {"SHELL": "/bin/sh"}), \
                mock.patch.object(platform_support.shutil, "which", return_value=None):
            args = platform_support.run_command_args('echo "$HOME" && whoami')
        self.assertIsInstance(args, list)
        self.assertEqual(args, ["/bin/sh", "-lc", 'echo "$HOME" && whoami'])

    def test_run_command_args_windows_raw_line(self):
        with mock.patch.object(platform_support.sys, "platform", "win32"):
            args = platform_support.run_command_args('python -c "print(1)"')
        self.assertEqual(args, 'cmd.exe /d /s /c "python -c "print(1)""')
        with mock.patch.object(platform_support.sys, "platform", "win32"):
            with self.assertRaises(platform_support.PlatformError):
                platform_support.run_command_args("echo one\necho two")


if __name__ == "__main__":
    unittest.main()
