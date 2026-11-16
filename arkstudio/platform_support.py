"""Cross-platform host integration.

ARK is a small web workbench, not an all-in-one UI.  This module isolates the
few places where it must talk to the operating system:

* open a local resource in the OS default application (and a URL in the default
  browser),
* stop a managed task-agent process tree,
* construct the operator shell command for a task-session preset.

Everything else stays portable Python.  The path helpers are deliberately
conservative: an open request may only reach a file inside the active project
folder or a task-session workspace, never an arbitrary absolute path, a parent
traversal, or a symlink escape.

Standard library only.
"""
from __future__ import annotations

import os
import re
import shlex
import shutil
import signal
import subprocess
import sys
import time
import webbrowser
from pathlib import Path
from urllib.parse import unquote, urlparse

OPEN_TIMEOUT = 10
KILL_TERM_WAIT = 4.0
_DRIVE_RE = re.compile(r"^[A-Za-z]:")


class PlatformError(ValueError):
    """A clean, user-facing host-integration failure.

    ``status`` maps directly to an HTTP status in ``server.py`` (400 for a bad
    request, 403 for a path outside the allowed roots, 404 for a missing file).
    """

    def __init__(self, message: str, status: int = 400):
        super().__init__(message)
        self.status = int(status)


def platform_id() -> str:
    """Return the adapter family selected from ``sys.platform`` at runtime."""
    if sys.platform.startswith("win"):
        return "windows"
    if sys.platform == "darwin":
        return "macos"
    if sys.platform.startswith(("linux", "freebsd", "openbsd", "netbsd")):
        return "linux"
    return sys.platform or os.name


# --------------------------------------------------------------------- paths
def _normalized(path: str | os.PathLike[str]) -> str:
    return os.path.normcase(os.path.realpath(os.path.abspath(os.fspath(path))))


def is_within(root: str | os.PathLike[str], path: str | os.PathLike[str]) -> bool:
    """True when ``path`` is ``root`` itself or a descendant of it."""
    try:
        root_n = _normalized(root)
        path_n = _normalized(path)
        if root_n == path_n:
            return True
        return os.path.commonpath([root_n, path_n]) == root_n
    except (OSError, ValueError):
        return False


def ensure_within(root: str | os.PathLike[str], candidate: str | os.PathLike[str],
                  *, must_exist: bool = True) -> Path:
    """Resolve ``candidate`` and require it to stay inside ``root``.

    A final symlink is rejected, and any symlinked parent that escapes the root
    is caught by resolving the path before the containment check.
    """
    root_path = Path(root).expanduser().resolve()
    raw = Path(candidate).expanduser()
    if not raw.is_absolute():
        raw = root_path / raw
    try:
        if raw.is_symlink():
            raise PlatformError("symlinked paths are not opened", 403)
        target = raw.resolve()
    except OSError as exc:
        raise PlatformError(f"cannot resolve path: {exc}", 400) from exc
    if not is_within(root_path, target):
        raise PlatformError("path is outside the project or session workspace", 403)
    if must_exist and not target.exists():
        raise PlatformError(f"file not found: {target.name}", 404)
    return target


def safe_path(root: str | os.PathLike[str], value: str, *,
              must_exist: bool = True) -> Path:
    """Resolve a project-relative path with traversal/absolute checks.

    Absolute paths (POSIX, Windows drive, UNC), ``..`` segments, empty values
    and symlink escapes are rejected.  Both ``/`` and ``\\`` are treated as
    separators so a Windows-style request is judged consistently on POSIX.
    """
    raw = str(value or "").replace("\\", "/").strip()
    if not raw:
        raise PlatformError("path is required")
    if "\x00" in raw:
        raise PlatformError("invalid path")
    if raw.startswith("/") or _DRIVE_RE.match(raw):
        raise PlatformError("absolute paths are not allowed; use a project-relative path", 400)
    parts = [part for part in raw.split("/") if part not in ("", ".")]
    if not parts:
        raise PlatformError("path is required")
    if any(part == ".." for part in parts):
        raise PlatformError("path traversal is not allowed", 400)
    candidate = Path(root).joinpath(*parts)
    return ensure_within(root, candidate, must_exist=must_exist)


def path_from_file_url(value: str) -> Path:
    """Convert a local ``file://`` URL to a path (host must be empty/localhost)."""
    parsed = urlparse(str(value or ""))
    if parsed.scheme.lower() != "file":
        raise PlatformError("not a file:// URL")
    if parsed.netloc not in ("", "localhost"):
        raise PlatformError("network file URLs are not opened", 403)
    raw = unquote(parsed.path)
    if re.match(r"^/[A-Za-z]:", raw):
        raw = raw[1:]
    if not raw:
        raise PlatformError("file URL has no path")
    return Path(raw)


def resolve_media_path(root: str | os.PathLike[str], value: str) -> Path:
    """Resolve one media row to an existing file inside ``root``.

    Relative values (``assets/foo.pdf``) are resolved under the project.
    ``file://`` values and absolute paths are allowed only when they resolve
    inside the same root; anything outside is rejected with a 403.
    """
    text = str(value or "").strip()
    if not text:
        raise PlatformError("media URL is empty")
    if text.lower().startswith("file://"):
        target = ensure_within(root, path_from_file_url(text), must_exist=True)
    elif re.match(r"^https?://", text, re.I):
        raise PlatformError("remote media is not a local file")
    elif text.startswith("/") or _DRIVE_RE.match(text) or text.startswith("\\\\"):
        target = ensure_within(root, Path(text), must_exist=True)
    elif re.match(r"^[a-z][a-z0-9+.-]*:", text, re.I):
        raise PlatformError("unsupported media URL scheme")
    else:
        target = safe_path(root, text, must_exist=True)
    if not target.is_file():
        raise PlatformError(f"not a file: {target.name}", 404)
    return target


# --------------------------------------------------------------------- open
def _run_opener(argv: list[str], *, creationflags: int = 0) -> int:
    try:
        completed = subprocess.run(
            argv,
            stdout=subprocess.DEVNULL,
            stderr=subprocess.DEVNULL,
            timeout=OPEN_TIMEOUT,
            check=False,
            **({"creationflags": creationflags} if creationflags else {}),
        )
    except FileNotFoundError as exc:
        raise PlatformError(f"application opener not found: {argv[0]}") from exc
    except (OSError, subprocess.SubprocessError) as exc:
        raise PlatformError(f"could not launch {argv[0]}: {exc}") from exc
    try:
        return int(completed.returncode or 0)
    except (TypeError, ValueError):
        return 0


def _local_path(value: str | os.PathLike[str]) -> Path:
    raw = os.fspath(value)
    if re.match(r"^[A-Za-z][A-Za-z0-9+.-]*://", raw):
        raise PlatformError("open_external expects a local file; use default_browser_open for URLs")
    target = Path(raw).expanduser()
    if not target.is_absolute():
        target = Path.cwd() / target
    try:
        target = target.resolve()
    except OSError as exc:
        raise PlatformError(f"cannot resolve path: {exc}") from exc
    if not target.exists():
        raise PlatformError(f"file not found: {target.name}", 404)
    if not (target.is_file() or target.is_dir()):
        raise PlatformError(f"not a file or folder: {target.name}", 400)
    return target


def _desktop_session() -> bool:
    return bool(os.environ.get("DISPLAY") or os.environ.get("WAYLAND_DISPLAY"))


def open_external(value: str | os.PathLike[str]) -> dict:
    """Open a local file/folder in the operating system's default application.

    Returns a structured result instead of a bare boolean so the API and UI can
    report which opener was used.  Raises :class:`PlatformError` with a clean
    message when the platform has no opener or the process is headless.
    """
    target = _local_path(value)
    family = platform_id()
    result = {
        "opened": True,
        "kind": "file",
        "target": str(target),
        "platform": family,
    }
    if family == "windows":
        startfile = getattr(os, "startfile", None)
        if callable(startfile):
            try:
                startfile(str(target))
                return {**result, "opener": "os.startfile"}
            except OSError:
                pass  # fall through to explorer, which is more forgiving
        explorer = shutil.which("explorer") or "explorer"
        code = _run_opener([explorer, str(target)])
        if code not in (0, 1):
            raise PlatformError("Windows could not open this file with its default application")
        return {**result, "opener": "explorer"}
    if family == "macos":
        opener = shutil.which("open") or "/usr/bin/open"
        code = _run_opener([opener, str(target)])
        if code != 0:
            raise PlatformError("macOS 'open' could not launch the default application")
        return {**result, "opener": "open"}
    if not _desktop_session():
        raise PlatformError(
            "no graphical session is available; start ARK in a desktop session "
            "or open the file manually")
    attempts: list[str] = []
    for name in ("xdg-open", "gio"):
        executable = shutil.which(name)
        if not executable:
            continue
        argv = [executable, str(target)] if name == "xdg-open" else [executable, "open", str(target)]
        try:
            code = _run_opener(argv)
        except PlatformError:
            attempts.append(name)
            continue
        if code == 0:
            return {**result, "opener": f"{name} open" if name == "gio" else name}
        attempts.append(name)
    detail = f" (tried {', '.join(attempts)})" if attempts else ""
    raise PlatformError(
        "no default-application opener is available; install xdg-utils "
        f"(xdg-open) or gio{detail}")


def open_url(value: str) -> dict:
    """Open http(s) or a registered protocol (zotero://, obsidian://, ...)."""
    url = str(value or "").strip()
    parsed = urlparse(url)
    scheme = parsed.scheme.lower()
    if not scheme or scheme in {"javascript", "data", "vbscript"}:
        raise PlatformError("unsupported URL scheme")
    family = platform_id()
    if family == "windows":
        startfile = getattr(os, "startfile", None)
        if callable(startfile):
            try:
                startfile(url)
                return {"opened": True, "kind": "url", "target": url,
                        "platform": family, "opener": "os.startfile"}
            except OSError as exc:
                raise PlatformError(f"Windows could not open this URL: {exc}") from exc
    elif family == "macos":
        opener = shutil.which("open") or "/usr/bin/open"
        code = _run_opener([opener, url])
        if code == 0:
            return {"opened": True, "kind": "url", "target": url,
                    "platform": family, "opener": "open"}
    else:
        opener = shutil.which("xdg-open") or shutil.which("gio")
        if opener:
            code = _run_opener([opener, url]) if opener.endswith("xdg-open") else _run_opener([opener, "open", url])
            if code == 0:
                return {"opened": True, "kind": "url", "target": url,
                        "platform": family, "opener": Path(opener).name}
    if scheme in {"http", "https"}:
        return default_browser_open(url)
    raise PlatformError(f"no handler could open the {scheme}:// URL")


def default_browser_open(url: str) -> dict:
    """Open an http(s) URL in the operating system's default browser."""
    value = str(url or "").strip()
    parsed = urlparse(value)
    if parsed.scheme.lower() not in {"http", "https"} or not parsed.netloc:
        raise PlatformError("only http(s) URLs can be opened in the default browser")
    try:
        opened = webbrowser.open(value, new=2)
    except Exception as exc:  # noqa: BLE001 - webbrowser backends vary
        raise PlatformError(f"could not launch the default browser: {exc}") from exc
    if not opened:
        raise PlatformError("no default browser is available in this session")
    return {
        "opened": True,
        "kind": "url",
        "target": value,
        "platform": platform_id(),
        "opener": "webbrowser",
    }


# ------------------------------------------------------------------ process
def _group_alive(pgid: int) -> bool:
    try:
        os.killpg(pgid, 0)
        return True
    except ProcessLookupError:
        return False
    except PermissionError:
        return True


def _wait_group_gone(pgid: int, timeout: float) -> bool:
    deadline = time.monotonic() + max(0.0, timeout)
    while True:
        if not _group_alive(pgid):
            return True
        if time.monotonic() >= deadline:
            return False
        time.sleep(0.1)


def kill_process_tree(pid: int, *, term_wait: float = KILL_TERM_WAIT) -> dict:
    """Stop a managed process tree without touching ARK's own process group.

    Windows uses ``taskkill /T /F`` (the previous behavior).  POSIX sends
    SIGTERM to the session's process group, waits briefly, then escalates to
    SIGKILL.  The server starts task sessions with ``start_new_session=True``,
    so its own group is never the target; the explicit guard below still
    refuses to signal ARK's group if a caller gets it wrong.
    """
    try:
        pid = int(pid)
    except (TypeError, ValueError) as exc:
        raise PlatformError("invalid process id") from exc
    if pid <= 0:
        raise PlatformError("invalid process id")
    family = platform_id()
    if family == "windows":
        taskkill = shutil.which("taskkill") or "taskkill"
        code = _run_opener(
            [taskkill, "/PID", str(pid), "/T", "/F"],
            creationflags=getattr(subprocess, "CREATE_NO_WINDOW", 0),
        )
        return {
            "killed": code == 0,
            "pid": pid,
            "platform": family,
            "method": "taskkill",
            "escalated": False,
        }
    try:
        pgid = os.getpgid(pid)
    except ProcessLookupError:
        return {"killed": False, "pid": pid, "platform": family, "method": "killpg",
                "escalated": False, "reason": "not running"}
    if pgid <= 1 or pgid == os.getpgrp():
        raise PlatformError("refusing to kill ARK's own process group", 500)
    try:
        os.killpg(pgid, signal.SIGTERM)
    except ProcessLookupError:
        return {"killed": False, "pid": pid, "platform": family, "method": "killpg",
                "escalated": False, "reason": "not running"}
    except PermissionError:
        try:
            os.kill(pid, signal.SIGTERM)
        except ProcessLookupError:
            return {"killed": False, "pid": pid, "platform": family,
                    "method": "kill", "escalated": False, "reason": "not running"}
        except PermissionError as exc:
            raise PlatformError(f"permission denied while stopping process {pid}", 500) from exc
        escalated = False
        if not _wait_pid_gone(pid, term_wait):
            escalated = True
            try:
                os.kill(pid, getattr(signal, "SIGKILL", signal.SIGTERM))
            except (ProcessLookupError, PermissionError):
                pass
        return {"killed": True, "pid": pid, "platform": family, "method": "kill",
                "escalated": escalated}
    escalated = False
    if not _wait_group_gone(pgid, term_wait):
        escalated = True
        try:
            os.killpg(pgid, getattr(signal, "SIGKILL", signal.SIGTERM))
        except (ProcessLookupError, PermissionError):
            pass
    return {"killed": True, "pid": pid, "pgid": pgid, "platform": family,
            "method": "killpg", "escalated": escalated}


def _wait_pid_gone(pid: int, timeout: float) -> bool:
    deadline = time.monotonic() + max(0.0, timeout)
    while True:
        try:
            os.kill(pid, 0)
        except ProcessLookupError:
            return True
        except PermissionError:
            pass
        if time.monotonic() >= deadline:
            return False
        time.sleep(0.1)


# -------------------------------------------------------------------- shell
def run_command_args(command: str) -> list[str] | str:
    """Build the ``Popen`` argument that runs one command in the platform shell.

    POSIX returns an argv list; the operator's command is a single element and
    is never spliced into a shell string by ARK.  Windows returns a raw command
    line because ``subprocess.list2cmdline`` escapes embedded quotes with
    backslashes, which ``cmd.exe`` does not understand (``python -c "print(1)"``
    would silently produce no output).  ARK wraps the operator's command in
    exactly one pair of quotes and never interpolates a path or any other value
    into it.
    """
    text = str(command or "")
    if platform_id() == "windows":
        if "\x00" in text or "\r" in text or "\n" in text:
            raise PlatformError("command may not contain NUL or newline characters")
        return f'cmd.exe /d /s /c "{text}"'
    return shell_command(text)


def shell_command(command: str | None = None, *, env: dict | None = None) -> list[str]:
    """Build a platform shell command for task-session presets.

    With no ``command`` the shell stays interactive and reads the initial
    prompt from stdin (Windows keeps the previous PowerShell behavior).  POSIX
    uses ``-lc`` in both modes: the command string is passed directly for
    command mode, and interactive mode re-executes the login shell so stdin and
    the prompt pipe stay attached.  Windows command mode uses
    ``cmd.exe /d /s /c``.
    """
    environment = os.environ if env is None else env
    family = platform_id()
    if family == "windows":
        if command is not None:
            return ["cmd.exe", "/d", "/s", "/c", str(command)]
        return ["powershell", "-NoLogo", "-NoProfile", "-Command", "-"]
    shell = str(environment.get("SHELL") or "").strip()
    resolved = ""
    if shell:
        if os.path.isabs(shell) and Path(shell).is_file():
            resolved = shell
        else:
            resolved = shutil.which(Path(shell).name) or ""
    if not resolved:
        resolved = shutil.which("sh") or "/bin/sh"
    if command is not None:
        return [resolved, "-lc", str(command)]
    return [resolved, "-lc", f"exec {shlex.quote(resolved)} -l"]
