"""Project-scoped files, unified diffs and terminal commands.

This module backs the Editor's project files, Changes and Terminal views
(formerly the Tasks → Files workspace).  It deliberately stays inside the
active ARK project folder and uses only the Python standard library:

* the file tree is lazy (one folder per request), bounded by ignored
  directories, a per-request entry cap and a depth cap;
* reads and writes reject absolute paths, ``..`` traversal, symlinks and paths
  that resolve outside the project; writes are atomic and use optimistic
  concurrency (content hash and/or mtime);
* unified diffs come from ``git diff`` when the file is tracked and from
  :mod:`difflib` for untracked files, always bounded by size;
* terminal commands run through the platform shell in a project-scoped working
  directory, stream line/chunk output over polling or SSE, accept stdin and can
  be cancelled after a timeout or when the output cap is reached; an optional
  pseudo-terminal (PTY) mode lets interactive TUIs such as Codex CLI, Claude
  CLI and DSH work, with a graceful stdio fallback when no PTY backend exists.

PTY support is optional and detected at runtime.  Windows uses ``pywinpty``
when it is installed (ConPTY under the hood); POSIX uses the standard-library
``pty`` module plus ``subprocess`` with a controlling terminal.  Both backends
stream raw UTF-8 text (ANSI escapes included) through the same entries/SSE
pipeline; the browser is responsible for rendering ANSI.  When a PTY is
requested but unavailable, ARK starts the plain stdio subprocess instead and
reports ``pty_requested=true`` with ``pty=false`` and a fallback reason.

The command string is passed as one argument to the platform shell
(``cmd.exe /d /s /c`` on Windows, ``$SHELL -lc`` on POSIX); ARK never
interpolates user text into a shell string.
"""
from __future__ import annotations

import atexit
import codecs
import difflib
import errno
import hmac
import hashlib
import os
import re
import shutil
import stat as stat_module
import struct
import subprocess
import threading
import time
import uuid
from datetime import datetime, timezone
from pathlib import Path
from typing import Any

from . import platform_support, session_transcript

MAX_TREE_ENTRIES = 300
MAX_TREE_DEPTH = 4
MAX_TREE_NODES = 2500
MAX_FILE_BYTES = 1024 * 1024
MAX_SEARCH_RESULTS = 200
MAX_SEARCH_VISITS = 6000
MAX_DIFF_BYTES = 400 * 1024
MAX_DIFF_FILES = 500
MAX_TERMINAL_COMMAND = 2000
MAX_TERMINAL_OUTPUT = 256 * 1024
MAX_TERMINAL_ENTRIES = 2000
MAX_TERMINALS = 8
TERMINAL_DEFAULT_TIMEOUT = 30
TERMINAL_MAX_TIMEOUT = 120
TERMINAL_DEFAULT_COLS = 80
TERMINAL_DEFAULT_ROWS = 24
TERMINAL_MIN_COLS = 2
TERMINAL_MAX_COLS = 1000
TERMINAL_MIN_ROWS = 1
TERMINAL_MAX_ROWS = 500
PTY_BACKEND_PYWINPTY = "pywinpty"
PTY_BACKEND_POSIX = "posix"
GIT_TIMEOUT = 15
DRIVE_RE = re.compile(r"^[A-Za-z]:")
BASE_REF_RE = re.compile(r"^[A-Za-z0-9][A-Za-z0-9._/~^@{}-]{0,120}$")

# Directory/file names that are never exposed to the workspace browser.  The
# same rule is applied to read/write/rename/delete so ignored trees cannot be
# reached by guessing a path.
IGNORED_DIRS = {
    ".git", ".hg", ".svn", "__pycache__", ".mypy_cache", ".pytest_cache",
    ".ruff_cache", "node_modules", ".venv", "venv", "env", ".tox", "dist",
    "build", "target", ".next", ".nuxt", ".cache", ".parcel-cache",
    "coverage", "htmlcov", ".idea", ".gradle", ".pytest-tmp",
}
IGNORED_FILES = {
    ".DS_Store", "Thumbs.db", "desktop.ini",
    # ARK's own binary store is never a text-editing target.
    "ark.sqlite3", "ark.sqlite3-wal", "ark.sqlite3-shm",
}
IGNORED_SUFFIXES = {".pyc", ".pyo", ".pyd", ".sqlite3-journal", ".swp", ".swo"}


class WorkspaceError(ValueError):
    """A files/terminal request that maps to a clean HTTP error."""

    def __init__(self, message: str, status: int = 400):
        super().__init__(message)
        self.status = int(status)


def utc_now() -> str:
    return datetime.now(timezone.utc).isoformat(timespec="seconds")


def _iso_time(value: float) -> str:
    try:
        return datetime.fromtimestamp(float(value), timezone.utc).isoformat(timespec="seconds")
    except (OSError, OverflowError, ValueError):
        return ""


def _hash_bytes(raw: bytes) -> str:
    return hashlib.blake2b(raw, digest_size=16).hexdigest()


def _looks_binary(raw: bytes) -> bool:
    return b"\x00" in raw[:8192]


def _language(name: str | Path) -> str:
    return session_transcript.language_for(str(name))


def _is_ignored(name: str) -> bool:
    if name in IGNORED_DIRS or name in IGNORED_FILES:
        return True
    return Path(name).suffix.lower() in IGNORED_SUFFIXES


# Folders at the root of an ARK project folder that hold other users' data:
# ``setup/`` keeps the multi-user setup layers, including private resources
# and per-host paths (docs/operations/multi-user-setup.md).  Hidden like ignored names.
PROTECTED_PROJECT_DIRS = {"setup"}


def _is_protected(root_path: Path, rel: str) -> bool:
    first = str(rel or "").replace("\\", "/").split("/", 1)[0].lower()
    if first not in PROTECTED_PROJECT_DIRS:
        return False
    return (root_path / "ark.sqlite3").exists() or (root_path / "project.json").exists()


def _is_link_like(path: Path) -> bool:
    """True for symlinks and Windows directory junctions/reparse points.

    ``Path.is_symlink`` misses junctions, and both must never be followed by a
    tree walk, read or recursive delete.
    """
    try:
        if path.is_symlink():
            return True
        stat = path.lstat()
    except OSError:
        return False
    flags = getattr(stat, "st_file_attributes", 0)
    reparse = getattr(stat, "FILE_ATTRIBUTE_REPARSE_POINT", 0x400)
    return bool(flags & reparse)


def _delete_tree(target: Path) -> int:
    """Delete a tree bottom-up without ever following symlinks/junctions.

    ``os.walk`` on Windows can descend into a directory junction even with
    ``followlinks=False`` (junctions are not reported by ``os.path.islink``), so
    this uses ``scandir`` and removes every link-like entry itself.
    """
    removed = 0

    def visit(folder: Path) -> None:
        nonlocal removed
        with os.scandir(folder) as scan:
            entries = list(scan)
        for entry in entries:
            child = Path(entry.path)
            if _is_link_like(child):
                try:
                    child.unlink()
                except OSError:
                    child.rmdir()
                removed += 1
            elif entry.is_dir(follow_symlinks=False):
                visit(child)
            else:
                child.unlink()
                removed += 1
        folder.rmdir()
        removed += 1

    visit(target)
    return removed


def _rel_parts(value: Any) -> list[str]:
    """Normalize a project-relative path to safe part segments.

    Empty means the project root.  Absolute paths (POSIX, Windows drive, UNC),
    NUL bytes and ``..`` segments are rejected before touching the filesystem.
    """
    raw = str(value or "").replace("\\", "/").strip()
    if not raw or raw in (".", "./"):
        return []
    if "\x00" in raw:
        raise WorkspaceError("invalid path")
    if raw.startswith("/") or raw.startswith("//") or DRIVE_RE.match(raw):
        raise WorkspaceError(
            "absolute paths are not allowed; use a project-relative path", 400)
    parts = [part for part in raw.split("/") if part not in ("", ".")]
    if not parts:
        return []
    if any(part == ".." for part in parts):
        raise WorkspaceError("path traversal is not allowed", 400)
    for part in parts:
        if _is_ignored(part):
            raise WorkspaceError(f"{part} is ignored by the workspace and cannot be opened", 403)
    return parts


def _join(parts: list[str]) -> str:
    return "/".join(parts)


def _resolve(root: str | Path, value: Any, *, must_exist: bool = True) -> tuple[Path, str]:
    """Resolve a project-relative path and reject any symlink component."""
    root_path = Path(root).resolve()
    parts = _rel_parts(value)
    if parts and _is_protected(root_path, parts[0]):
        raise WorkspaceError(f"{parts[0]} is ignored by the workspace and cannot be opened", 403)
    candidate = root_path.joinpath(*parts) if parts else root_path
    current = root_path
    for part in parts:
        current = current / part
        try:
            if _is_link_like(current):
                raise WorkspaceError("symlinked paths are not allowed", 403)
        except OSError as exc:  # pragma: no cover - unusual filesystem failure
            raise WorkspaceError(f"cannot inspect path: {exc}", 400) from exc
    try:
        target = candidate.resolve()
    except OSError as exc:
        raise WorkspaceError(f"cannot resolve path: {exc}", 400) from exc
    if target != root_path and not platform_support.is_within(root_path, target):
        raise WorkspaceError("path is outside the project folder", 403)
    if parts and target == root_path:
        # e.g. a junction/case-folded alias that resolves back to the root.
        raise WorkspaceError("path is outside the project folder", 403)
    if must_exist and not target.exists():
        raise WorkspaceError(f"file not found: {_join(parts) or target.name}", 404)
    return target, _join(parts)


def _stat_payload(path: Path, rel: str) -> dict:
    try:
        stat = path.stat()
    except OSError:
        stat = None
    is_dir = path.is_dir()
    return {
        "path": rel,
        "name": path.name,
        "type": "dir" if is_dir else "file",
        "size": 0 if is_dir else (stat.st_size if stat else 0),
        "mtime": _iso_time(stat.st_mtime) if stat else "",
        "mtime_ns": stat.st_mtime_ns if stat else 0,
        "language": "" if is_dir else _language(path.name),
    }


def _atomic_write(target: Path, data: bytes) -> None:
    """Write bytes through a sibling temp file, then replace the target."""
    temp = target.with_name(f".{target.name}.ark-tmp-{uuid.uuid4().hex[:10]}")
    try:
        mode = (stat_module.S_IMODE(target.stat().st_mode) if target.exists() else None)
        with temp.open("wb") as handle:
            handle.write(data)
            handle.flush()
            os.fsync(handle.fileno())
        if mode is not None:
            os.chmod(temp, mode)
        os.replace(temp, target)
    except OSError as exc:
        raise WorkspaceError(f"cannot write file: {exc}", 400) from exc
    finally:
        try:
            if temp.exists():
                temp.unlink()
        except OSError:
            pass


# --------------------------------------------------------------------- git
def _git(root: Path, args: list[str], *, timeout: float = GIT_TIMEOUT,
         data: bytes | None = None) -> subprocess.CompletedProcess | None:
    git = shutil.which("git")
    if not git:
        return None
    try:
        return subprocess.run(
            [git, "-C", str(root), *args],
            input=data,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            timeout=timeout,
            check=False,
        )
    except (OSError, subprocess.SubprocessError):
        return None


def _decode(raw: bytes | None) -> str:
    return (raw or b"").decode("utf-8", "replace")


def _git_context(root: Path) -> dict:
    """Describe the project's git repository (never one of an outer folder)."""
    info: dict[str, Any] = {
        "available": False,
        "is_repo": False,
        "root": "",
        "prefix": "",
        "branch": "",
        "head": "",
        "reason": "",
    }
    git = shutil.which("git")
    if not git:
        info["reason"] = "git is not installed"
        return info
    inside = _git(root, ["rev-parse", "--is-inside-work-tree"])
    if inside is None or inside.returncode != 0 or _decode(inside.stdout).strip() != "true":
        info["reason"] = "the project folder is not inside a git working tree"
        return info
    top = _git(root, ["rev-parse", "--show-toplevel"])
    if top is None or top.returncode != 0:
        info["reason"] = "git could not resolve the repository root"
        return info
    top_path = Path(_decode(top.stdout).strip()).resolve()
    root_path = Path(root).resolve()
    if not platform_support.is_within(top_path, root_path):
        info["reason"] = "the git repository root is outside the project folder"
        return info
    try:
        prefix = root_path.relative_to(top_path).as_posix()
    except ValueError:  # pragma: no cover - is_within already checked
        prefix = ""
    info.update({
        "available": True,
        "is_repo": True,
        "root": top_path.name,
        "prefix": "" if prefix == "." else prefix,
    })
    branch = _git(root, ["rev-parse", "--abbrev-ref", "HEAD"])
    if branch is not None and branch.returncode == 0:
        info["branch"] = _decode(branch.stdout).strip()
    head = _git(root, ["rev-parse", "--short", "HEAD"])
    if head is not None and head.returncode == 0:
        info["head"] = _decode(head.stdout).strip()
    return info


def _check_base(root: Path, value: Any) -> str:
    base = str(value or "HEAD").strip() or "HEAD"
    if not BASE_REF_RE.fullmatch(base) or base.startswith("-"):
        raise WorkspaceError(
            "base must be a git ref such as HEAD, a branch, a tag or a commit hash", 400)
    verify = _git(root, ["rev-parse", "--verify", "--quiet", f"{base}^{{commit}}"])
    if verify is None:
        raise WorkspaceError("git is not available", 400)
    if verify.returncode != 0:
        raise WorkspaceError(f"git base not found: {base}", 400)
    return base


def _cap_text(text: str, limit: int = MAX_DIFF_BYTES) -> tuple[str, bool]:
    if len(text.encode("utf-8", "replace")) <= limit:
        return text, False
    truncated = text.encode("utf-8", "replace")[:limit].decode("utf-8", "ignore")
    cut = truncated.rfind("\n")
    if cut > limit // 2:
        truncated = truncated[:cut + 1]
    return truncated + "\n…[diff truncated by ARK]\n", True


def _numstat_entries(raw: bytes) -> dict[str, tuple[int | None, int | None, bool]]:
    """Parse ``git diff --numstat -z`` into path -> (adds, dels, binary)."""
    out: dict[str, tuple[int | None, int | None, bool]] = {}
    fields = raw.split(b"\x00")
    index = 0
    while index < len(fields):
        record = fields[index]
        index += 1
        if not record:
            continue
        parts = record.split(b"\t")
        if len(parts) < 3:
            continue
        adds_raw, dels_raw, name = parts[0], parts[1], parts[2]
        if not name and index + 1 < len(fields):
            # Rename/copy: old and new paths follow the empty path field.
            name = fields[index + 1]
            index += 2
        path = name.decode("utf-8", "replace")
        binary = adds_raw == b"-" and dels_raw == b"-"
        try:
            adds = None if binary else int(adds_raw)
            dels = None if binary else int(dels_raw)
        except ValueError:
            adds = dels = None
        out[path] = (adds, dels, binary)
    return out


def _name_status_entries(raw: bytes) -> dict[str, str]:
    """Parse ``git diff --name-status -z`` into path -> status letter."""
    out: dict[str, str] = {}
    fields = raw.split(b"\x00")
    index = 0
    while index < len(fields):
        record = fields[index]
        index += 1
        if not record:
            continue
        parts = record.split(b"\t")
        status = parts[0].decode("ascii", "replace")[:1] or "M"
        if len(parts) >= 3:
            path = parts[2]
        elif len(parts) == 2:
            path = parts[1]
        elif status in {"R", "C"} and index + 1 < len(fields):
            # With -z a rename/copy emits the old and new path as two fields.
            path = fields[index + 1]
            index += 2
        elif index < len(fields):
            path = fields[index]
            index += 1
        else:
            continue
        out[path.decode("utf-8", "replace")] = status
    return out


def _count_lines(target: Path) -> tuple[int | None, bool]:
    """Count added lines for an untracked file without ever raising."""
    try:
        size = target.stat().st_size
        if size > MAX_FILE_BYTES:
            return None, False
        raw = target.read_bytes()
    except OSError:
        return None, False
    if _looks_binary(raw):
        return None, True
    try:
        text = raw.decode("utf-8")
    except UnicodeDecodeError:
        return None, True
    return (len(text.splitlines()) if text else 0), False


# ------------------------------------------------------------------- service
class Workspace:
    """Stateless project file operations plus a bounded terminal registry."""

    def __init__(self) -> None:
        self.terminals = TerminalManager()
        atexit.register(self.shutdown)

    def shutdown(self) -> None:
        try:
            self.terminals.shutdown()
        except Exception:  # noqa: BLE001 - shutdown must never raise
            pass

    # ----------------------------------------------------------------- tree
    def info(self, root: str | Path) -> dict:
        root_path = Path(root).resolve()
        git = _git_context(root_path)
        pty = pty_availability()
        return {
            "project": root_path.name,
            "path": str(root_path),
            "git": git,
            "shell": "cmd.exe" if os.name == "nt" else (os.environ.get("SHELL") or "sh"),
            "pty": pty["available"],
            "pty_available": pty["available"],
            "pty_backend": pty["backend"],
            "pty_version": pty.get("version", ""),
            "pty_reason": pty["reason"],
            "pty_hint": pty["hint"],
            "limits": {
                "tree_entries": MAX_TREE_ENTRIES,
                "tree_depth": MAX_TREE_DEPTH,
                "file_bytes": MAX_FILE_BYTES,
                "diff_bytes": MAX_DIFF_BYTES,
                "terminal_output_bytes": MAX_TERMINAL_OUTPUT,
                "terminal_timeout_seconds": TERMINAL_MAX_TIMEOUT,
                "terminal_cols": [TERMINAL_MIN_COLS, TERMINAL_MAX_COLS],
                "terminal_rows": [TERMINAL_MIN_ROWS, TERMINAL_MAX_ROWS],
            },
        }

    def tree(self, root: str | Path, path: str = "", *, depth: int = 1) -> dict:
        root_path = Path(root).resolve()
        target, rel = _resolve(root_path, path)
        if not target.is_dir():
            raise WorkspaceError("not a folder")
        try:
            depth = max(1, min(MAX_TREE_DEPTH, int(depth)))
        except (TypeError, ValueError):
            depth = 1
        budget = {"entries": MAX_TREE_ENTRIES, "nodes": MAX_TREE_NODES, "ignored": 0,
                  "symlinks": 0, "truncated": False}

        def walk(folder: Path, rel_path: str, level: int) -> list[dict]:
            entries: list[dict] = []
            if budget["entries"] <= 0 or budget["nodes"] <= 0:
                budget["truncated"] = True
                return entries
            try:
                children = sorted(folder.iterdir(),
                                  key=lambda child: (not child.is_dir(), child.name.lower()))
            except OSError as exc:
                raise WorkspaceError(f"cannot list folder: {exc}", 400) from exc
            for child in children:
                if budget["entries"] <= 0 or budget["nodes"] <= 0:
                    budget["truncated"] = True
                    break
                if _is_link_like(child):
                    budget["symlinks"] += 1
                    continue
                if _is_ignored(child.name) or (not rel_path
                                               and _is_protected(root_path, child.name)):
                    budget["ignored"] += 1
                    continue
                is_dir = child.is_dir()
                child_rel = f"{rel_path}/{child.name}" if rel_path else child.name
                entry = _stat_payload(child, child_rel)
                entry["has_children"] = is_dir
                if is_dir and level < depth:
                    entry["children"] = walk(child, child_rel, level + 1)
                entries.append(entry)
                budget["entries"] -= 1
                budget["nodes"] -= 1
            return entries

        entries = walk(target, rel, 1)
        return {
            "path": rel,
            "parent": "/".join(rel.split("/")[:-1]) if rel else None,
            "entries": entries,
            "depth": depth,
            "truncated": budget["truncated"],
            "ignored": budget["ignored"],
            "symlinks_skipped": budget["symlinks"],
            "count": len(entries),
        }

    def search(self, root: str | Path, query: str, *, limit: int = 100) -> dict:
        root_path = Path(root).resolve()
        text = str(query or "").strip().lower()
        if not text:
            return {"query": "", "results": [], "truncated": False, "visited": 0}
        try:
            limit = max(1, min(MAX_SEARCH_RESULTS, int(limit)))
        except (TypeError, ValueError):
            limit = 100
        results: list[dict] = []
        visited = 0
        truncated = False
        for current, dirs, files in os.walk(root_path, followlinks=False):
            dirs[:] = sorted(
                (name for name in dirs
                 if not _is_ignored(name)
                 and not (Path(current) == root_path and _is_protected(root_path, name))
                 and not _is_link_like(Path(current) / name)),
                key=str.lower)
            for name in sorted(files, key=str.lower):
                if len(results) >= limit or visited >= MAX_SEARCH_VISITS:
                    truncated = True
                    break
                child = Path(current) / name
                if _is_link_like(child) or _is_ignored(name):
                    continue
                visited += 1
                rel = child.relative_to(root_path).as_posix()
                if text not in rel.lower():
                    continue
                results.append({
                    "path": rel,
                    "name": name,
                    "type": "file",
                    "size": child.stat().st_size if child.exists() else 0,
                    "language": _language(name),
                })
            if truncated:
                break
        return {
            "query": str(query or ""),
            "results": results,
            "truncated": truncated,
            "visited": visited,
        }

    # ----------------------------------------------------------------- file
    def resolve_file(self, root: str | Path, path: str) -> Path:
        """Return an existing regular file inside ``root`` (no symlinks)."""
        if not str(path or "").strip():
            raise WorkspaceError("path is required")
        target, _rel = _resolve(Path(root).resolve(), path)
        if not target.is_file():
            raise WorkspaceError("path is a folder, not a file")
        return target

    def read_file(self, root: str | Path, path: str) -> dict:
        root_path = Path(root).resolve()
        target, rel = _resolve(root_path, path)
        if target.is_dir():
            raise WorkspaceError("path is a folder, not a file")
        try:
            stat = target.stat()
            size = stat.st_size
            if size > MAX_FILE_BYTES:
                raise WorkspaceError(
                    f"file is too large to edit ({size} bytes; limit {MAX_FILE_BYTES})", 413)
            raw = target.read_bytes()
        except WorkspaceError:
            raise
        except OSError as exc:
            raise WorkspaceError(f"cannot read file: {exc}", 400) from exc
        if _looks_binary(raw):
            raise WorkspaceError("binary file; the editor only opens UTF-8 text", 415)
        try:
            text = raw.decode("utf-8-sig")
        except UnicodeDecodeError as exc:
            raise WorkspaceError("file is not UTF-8 text", 415) from exc
        return {
            "path": rel,
            "name": target.name,
            "content": text,
            "size": size,
            "lines": len(text.splitlines()) if text else 0,
            "language": _language(target.name),
            "hash": _hash_bytes(raw),
            "mtime": _iso_time(stat.st_mtime),
            "mtime_ns": stat.st_mtime_ns,
            "eol": "crlf" if b"\r\n" in raw else "lf",
            "read_only": not os.access(target, os.W_OK),
        }

    def write_file(self, root: str | Path, body: dict) -> dict:
        root_path = Path(root).resolve()
        rel_value = body.get("path")
        content = body.get("content")
        if not isinstance(content, str):
            raise WorkspaceError("content must be a string")
        data = content.encode("utf-8")
        if len(data) > MAX_FILE_BYTES:
            raise WorkspaceError(
                f"file is too large to save ({len(data)} bytes; limit {MAX_FILE_BYTES})", 413)
        target, rel = _resolve(root_path, rel_value, must_exist=False)
        if target.exists() and target.is_dir():
            raise WorkspaceError("path is a folder, not a file")
        if not platform_support.is_within(root_path, target.parent):
            raise WorkspaceError("path is outside the project folder", 403)
        if not target.parent.is_dir():
            raise WorkspaceError("parent folder does not exist", 404)

        base_hash = str(body.get("base_hash") or "").strip()
        base_mtime = body.get("base_mtime")
        create = bool(body.get("create"))
        existed = target.exists()
        if existed:
            if not base_hash and base_mtime in (None, ""):
                raise WorkspaceError(
                    "base_hash or base_mtime is required to overwrite a file", 409)
            try:
                current_size = target.stat().st_size
                if current_size > MAX_FILE_BYTES:
                    raise WorkspaceError(
                        f"file is too large to save ({current_size} bytes; limit {MAX_FILE_BYTES})", 413)
                current = target.read_bytes()
            except WorkspaceError:
                raise
            except OSError as exc:
                raise WorkspaceError(f"cannot read file: {exc}", 400) from exc
            if base_hash:
                if not hmac.compare_digest(base_hash, _hash_bytes(current)):
                    raise WorkspaceError(
                        "the file changed on disk since it was opened; reload it before saving", 409)
            elif base_mtime not in (None, ""):
                try:
                    expected = int(base_mtime)
                except (TypeError, ValueError):
                    raise WorkspaceError("base_mtime must be an integer nanosecond timestamp")
                if expected != target.stat().st_mtime_ns:
                    raise WorkspaceError(
                        "the file changed on disk since it was opened; reload it before saving", 409)
        elif not create:
            raise WorkspaceError("file does not exist; create it explicitly", 404)
        _atomic_write(target, data)
        return {
            "path": rel,
            "created": not existed,
            **_stat_payload(target, rel),
            "size": len(data),
            "lines": len(content.splitlines()) if content else 0,
            "hash": _hash_bytes(data),
            "mtime": _iso_time(target.stat().st_mtime),
            "mtime_ns": target.stat().st_mtime_ns,
        }

    def create_path(self, root: str | Path, body: dict) -> dict:
        root_path = Path(root).resolve()
        kind = str(body.get("type") or "file").strip().lower()
        if kind not in {"file", "dir"}:
            raise WorkspaceError("type must be file or dir")
        target, rel = _resolve(root_path, body.get("path"), must_exist=False)
        if target.exists():
            raise WorkspaceError("path already exists", 409)
        if not target.parent.is_dir():
            raise WorkspaceError("parent folder does not exist", 404)
        try:
            if kind == "dir":
                target.mkdir()
            else:
                content = body.get("content")
                if content is None:
                    content = ""
                if not isinstance(content, str):
                    raise WorkspaceError("content must be a string")
                data = content.encode("utf-8")
                if len(data) > MAX_FILE_BYTES:
                    raise WorkspaceError("file is too large", 413)
                with target.open("xb") as handle:
                    handle.write(data)
        except WorkspaceError:
            raise
        except OSError as exc:
            raise WorkspaceError(f"cannot create path: {exc}", 400) from exc
        payload = _stat_payload(target, rel)
        if kind == "file":
            payload["hash"] = _hash_bytes(target.read_bytes())
        return payload

    def rename_path(self, root: str | Path, body: dict) -> dict:
        root_path = Path(root).resolve()
        source, source_rel = _resolve(root_path, body.get("from"))
        target, target_rel = _resolve(root_path, body.get("to"), must_exist=False)
        if source == root_path:
            raise WorkspaceError("the project root cannot be moved", 400)
        if target.exists():
            raise WorkspaceError("destination already exists", 409)
        if not target.parent.is_dir():
            raise WorkspaceError("destination folder does not exist", 404)
        if source.is_dir() and (target == source or source in target.parents):
            raise WorkspaceError("a folder cannot be moved inside itself", 400)
        try:
            source.rename(target)
        except OSError as exc:
            raise WorkspaceError(f"cannot rename path: {exc}", 400) from exc
        return {"from": source_rel, "to": target_rel,
                "type": "dir" if target.is_dir() else "file"}

    def delete_path(self, root: str | Path, body: dict) -> dict:
        root_path = Path(root).resolve()
        target, rel = _resolve(root_path, body.get("path"))
        if target == root_path:
            raise WorkspaceError("the project root cannot be deleted", 400)
        recursive = bool(body.get("recursive"))
        removed = 1
        if target.is_dir():
            try:
                children = list(target.iterdir())
            except OSError as exc:
                raise WorkspaceError(f"cannot read folder: {exc}", 400) from exc
            if children and not recursive:
                raise WorkspaceError("folder is not empty; pass recursive to delete it", 409)
            if recursive:
                try:
                    removed = _delete_tree(target)
                except OSError as exc:
                    raise WorkspaceError(f"cannot delete folder: {exc}", 400) from exc
            else:
                target.rmdir()
        else:
            try:
                target.unlink()
            except OSError as exc:
                raise WorkspaceError(f"cannot delete file: {exc}", 400) from exc
        return {"path": rel, "removed": removed, "recursive": recursive}

    # ------------------------------------------------------------ changes/diff
    def changes(self, root: str | Path, base: str = "HEAD") -> dict:
        root_path = Path(root).resolve()
        info = _git_context(root_path)
        if not info["is_repo"]:
            return {"base": str(base or "HEAD"), "git": False, "files": [], "totals": {},
                    "truncated": False, "reason": info.get("reason") or "not a git repository"}
        base = _check_base(root_path, base)
        num = _git(root_path, ["diff", "--relative", "--numstat", "-z", base, "--", "."])
        status = _git(root_path, ["diff", "--relative", "--name-status", "-z", base, "--", "."])
        if num is None or status is None:
            raise WorkspaceError("git diff failed", 400)
        if num.returncode != 0 or status.returncode != 0:
            detail = _decode(num.stderr or status.stderr).strip()
            raise WorkspaceError(f"git diff failed: {detail or 'unknown error'}", 400)
        stats = _numstat_entries(num.stdout)
        states = _name_status_entries(status.stdout)
        files: dict[str, dict] = {}
        for path, code in states.items():
            if (_is_ignored(path) or any(_is_ignored(part) for part in path.split("/"))
                    or _is_protected(root_path, path)):
                continue
            adds, dels, binary = stats.get(path, (None, None, False))
            files[path] = {
                "path": path,
                "status": code,
                "additions": adds,
                "deletions": dels,
                "binary": binary,
            }
        untracked = _git(root_path, ["ls-files", "--others", "--exclude-standard", "-z", "--", "."])
        if untracked is not None and untracked.returncode == 0:
            candidates = []
            for name in untracked.stdout.split(b"\x00"):
                if not name:
                    continue
                path = name.decode("utf-8", "replace")
                if (_is_ignored(path) or any(_is_ignored(part) for part in path.split("/"))
                        or _is_protected(root_path, path)):
                    continue
                candidates.append(path)
            for path in sorted(candidates, key=str.lower)[:MAX_DIFF_FILES]:
                if path in files:
                    continue
                target = root_path / path
                adds, binary = _count_lines(target)
                files[path] = {
                    "path": path,
                    "status": "A",
                    "additions": adds,
                    "deletions": 0 if adds is not None else None,
                    "binary": binary,
                    "untracked": True,
                }
        ordered = sorted(files.values(), key=lambda item: item["path"].lower())
        truncated = len(ordered) > MAX_DIFF_FILES
        ordered = ordered[:MAX_DIFF_FILES]
        added = sum(item["additions"] or 0 for item in ordered)
        deleted = sum(item["deletions"] or 0 for item in ordered)
        return {
            "base": base,
            "git": True,
            "branch": info.get("branch") or "",
            "head": info.get("head") or "",
            "files": ordered,
            "totals": {"files": len(ordered), "additions": added, "deletions": deleted},
            "truncated": truncated,
        }

    def diff_file(self, root: str | Path, path: str, base: str = "HEAD") -> dict:
        root_path = Path(root).resolve()
        info = _git_context(root_path)
        if not info["is_repo"]:
            raise WorkspaceError(info.get("reason") or "the project folder is not a git repository", 400)
        base = _check_base(root_path, base)
        target, rel = _resolve(root_path, path, must_exist=False)
        if target.is_dir():
            raise WorkspaceError("diff expects a file, not a folder")
        prefix = str(info.get("prefix") or "")
        repo_rel = f"{prefix}/{rel}" if prefix else rel
        tracked = _git(root_path, ["ls-files", "--error-unmatch", "--", rel])
        is_tracked = bool(tracked is not None and tracked.returncode == 0)
        working_bytes = b""
        deleted = not target.exists()
        if not deleted:
            try:
                if target.stat().st_size > MAX_FILE_BYTES:
                    raise WorkspaceError(
                        f"file is too large to diff ({target.stat().st_size} bytes; limit {MAX_FILE_BYTES})", 413)
                working_bytes = target.read_bytes()
            except WorkspaceError:
                raise
            except OSError as exc:
                raise WorkspaceError(f"cannot read file: {exc}", 400) from exc
        if not is_tracked and working_bytes and _looks_binary(working_bytes):
            return {
                "path": rel, "base": base, "changed": True, "binary": True,
                "diff": "", "additions": None, "deletions": None,
                "truncated": False, "deleted": deleted,
                "message": "Binary file — the editor shows a text diff only.",
            }
        if is_tracked:
            proc = _git(root_path, ["diff", "--relative", "--no-color", "--no-ext-diff",
                                    "--unified=3", base, "--", rel])
            if proc is None or proc.returncode != 0:
                detail = _decode(proc.stderr if proc else b"").strip()
                raise WorkspaceError(f"git diff failed: {detail or 'unknown error'}", 400)
            raw = _decode(proc.stdout)
            if "Binary files" in raw or "GIT binary patch" in raw:
                return {
                    "path": rel, "base": base, "changed": True, "binary": True,
                    "diff": "", "additions": None, "deletions": None,
                    "truncated": False, "deleted": deleted,
                    "message": "Binary file — the editor shows a text diff only.",
                }
            changed = bool(raw.strip())
            additions = sum(1 for line in raw.splitlines()
                            if line.startswith("+") and not line.startswith("+++"))
            deletions = sum(1 for line in raw.splitlines()
                            if line.startswith("-") and not line.startswith("---"))
            if not raw and not deleted:
                return {
                    "path": rel, "base": base, "changed": False, "binary": False,
                    "diff": "", "additions": 0, "deletions": 0,
                    "truncated": False, "deleted": False,
                    "message": f"No changes against {base}.",
                }
            text, truncated = _cap_text(raw)
            return {
                "path": rel, "base": base, "changed": changed, "binary": False,
                "diff": text, "additions": additions, "deletions": deletions,
                "truncated": truncated, "deleted": deleted,
            }
        # Untracked/new/deleted-untracked file: compare against the git blob if
        # the ref happens to contain it, otherwise against an empty file.
        shown = _git(root_path, ["show", f"{base}:{repo_rel}"])
        base_bytes = shown.stdout if shown is not None and shown.returncode == 0 else b""
        if deleted and not base_bytes:
            raise WorkspaceError(f"file not found: {rel}", 404)
        if base_bytes and _looks_binary(base_bytes):
            return {
                "path": rel, "base": base, "changed": True, "binary": True,
                "diff": "", "additions": None, "deletions": None,
                "truncated": False, "deleted": deleted,
                "message": "Binary file — the editor shows a text diff only.",
            }
        try:
            working_text = working_bytes.decode("utf-8-sig")
        except UnicodeDecodeError:
            return {
                "path": rel, "base": base, "changed": True, "binary": True,
                "diff": "", "additions": None, "deletions": None,
                "truncated": False, "deleted": deleted,
                "message": "Binary or non-UTF-8 file — the editor shows a text diff only.",
            }
        if not deleted and not working_text and not base_bytes:
            return {
                "path": rel, "base": base, "changed": True, "binary": False,
                "diff": "", "additions": 0, "deletions": 0, "truncated": False,
                "deleted": False, "message": "Empty untracked file.",
            }
        try:
            base_text = base_bytes.decode("utf-8-sig")
        except UnicodeDecodeError:
            base_text = base_bytes.decode("utf-8", "replace")
        fromfile = f"a/{rel}"
        tofile = f"b/{rel}" if not deleted else "/dev/null"
        diff = "".join(difflib.unified_diff(
            base_text.splitlines(keepends=True),
            working_text.splitlines(keepends=True),
            fromfile=fromfile, tofile=tofile, n=3,
        ))
        text, truncated = _cap_text(diff)
        additions = sum(1 for line in diff.splitlines()
                        if line.startswith("+") and not line.startswith("+++"))
        deletions = sum(1 for line in diff.splitlines()
                        if line.startswith("-") and not line.startswith("---"))
        changed = bool(diff.strip()) or deleted
        return {
            "path": rel, "base": base, "changed": changed, "binary": False,
            "diff": text, "additions": additions, "deletions": deletions,
            "truncated": truncated, "deleted": deleted,
            "untracked": True,
        }


# ------------------------------------------------------------------ terminal
_PTY_PROBE_LOCK = threading.Lock()
_pty_probe_cache: dict[str, Any] | None = None


def _probe_pty_backend() -> dict:
    """Detect the optional interactive PTY backend for this host.

    The result is intentionally honest: an import failure on Windows must not
    pretend a PTY is available, and it must not break the stdio terminal.
    """
    if os.name == "nt":
        try:
            import winpty  # type: ignore[import-not-found]  # noqa: F401
        except ImportError:
            return {
                "available": False,
                "backend": "",
                "version": "",
                "reason": "pywinpty is not installed",
                "hint": "Install the optional Windows PTY backend with "
                        "`pip install pywinpty` (or `pip install \"ark-studio[pty]\"`), "
                        "then restart ARK. The stdio terminal keeps working without it.",
            }
        except Exception as exc:  # noqa: BLE001 - a broken install is still unavailable
            return {
                "available": False,
                "backend": "",
                "version": "",
                "reason": f"pywinpty could not be loaded: {exc}",
                "hint": "Reinstall pywinpty (`pip install --force-reinstall pywinpty`) "
                        "to enable the interactive PTY terminal.",
            }
        version = str(getattr(winpty, "__version__", "") or "")
        return {"available": True, "backend": PTY_BACKEND_PYWINPTY,
                "version": version, "reason": "", "hint": ""}
    try:
        import fcntl  # noqa: F401
        import pty  # noqa: F401
        import termios  # noqa: F401
    except ImportError as exc:
        return {
            "available": False,
            "backend": "",
            "version": "",
            "reason": f"the platform PTY module is unavailable: {exc}",
            "hint": "Interactive PTY support needs the standard-library `pty` module.",
        }
    return {"available": True, "backend": PTY_BACKEND_POSIX,
            "version": "", "reason": "", "hint": ""}


def pty_availability(*, refresh: bool = False) -> dict:
    """Return PTY capability metadata (cached; ``refresh`` re-probes)."""
    global _pty_probe_cache
    with _PTY_PROBE_LOCK:
        if _pty_probe_cache is None or refresh:
            _pty_probe_cache = _probe_pty_backend()
        return dict(_pty_probe_cache)


def _terminal_size(cols: Any, rows: Any, *, strict: bool) -> tuple[int, int]:
    """Validate/clamp a PTY geometry.

    Invalid values are a clean 400 for an explicit PTY request (``strict``);
    the stdio fallback simply keeps sensible defaults.
    """
    defaults = (TERMINAL_DEFAULT_COLS, TERMINAL_DEFAULT_ROWS)
    values: list[int] = []
    for label, value, low, high, default in (
            ("cols", cols, TERMINAL_MIN_COLS, TERMINAL_MAX_COLS, defaults[0]),
            ("rows", rows, TERMINAL_MIN_ROWS, TERMINAL_MAX_ROWS, defaults[1])):
        if value in (None, ""):
            values.append(default)
            continue
        try:
            number = int(value)
        except (TypeError, ValueError):
            if strict:
                raise WorkspaceError(f"{label} must be an integer") from None
            values.append(default)
            continue
        if number < low or number > high:
            if strict:
                raise WorkspaceError(
                    f"{label} must be between {low} and {high} (got {number})")
            number = max(low, min(high, number))
        values.append(number)
    return values[0], values[1]


def _terminal_env(*, pty: bool = False) -> dict[str, str]:
    """Copy the host env but never hand ARK's write/read tokens to a command.

    Stdio commands keep the old ``TERM=dumb``/``NO_COLOR=1`` contract.  A PTY
    announces a real xterm so line editors and TUIs enable colour and cursor
    handling; the browser renders the resulting ANSI sequences.
    """
    env: dict[str, str] = {}
    for key, value in os.environ.items():
        upper = key.upper()
        if upper.startswith("ARK_") and any(
                marker in upper for marker in ("TOKEN", "SECRET", "PASSWORD", "CREDENTIAL")):
            continue
        env[key] = str(value)
    env.update({
        "PYTHONUNBUFFERED": "1",
        "PYTHONIOENCODING": "utf-8",
        "ARK_TERMINAL": "1",
    })
    if pty:
        env.update({
            "TERM": "xterm-256color",
            "COLORTERM": "truecolor",
            "TERM_PROGRAM": "ARK",
        })
        for key in ("NO_COLOR", "CLICOLOR", "FORCE_COLOR"):
            env.pop(key, None)
    else:
        env.update({"TERM": "dumb", "NO_COLOR": "1"})
    return env


def _set_winsize_fd(fd: int, cols: int, rows: int) -> None:
    """Set a tty's window size (POSIX)."""
    import fcntl
    import termios

    fcntl.ioctl(fd, termios.TIOCSWINSZ, struct.pack("HHHH", rows, cols, 0, 0))


class _PosixPtyHandle:
    """A POSIX PTY built from ``pty.openpty`` + ``subprocess.Popen``."""

    backend = PTY_BACKEND_POSIX

    def __init__(self, argv: list[str], *, cwd: str, env: dict[str, str],
                 cols: int, rows: int):
        import fcntl
        import pty
        import termios

        master_fd, slave_fd = pty.openpty()
        try:
            _set_winsize_fd(slave_fd, cols, rows)

            def _child_setup() -> None:
                # The child becomes a session leader and adopts the slave as
                # its controlling terminal.  ``pass_fds`` keeps the fd valid
                # until this pre-exec hook runs; clearing inheritable after the
                # ioctl keeps it from leaking into the executed program.
                os.setsid()
                try:
                    fcntl.ioctl(slave_fd, termios.TIOCSCTTY, 0)
                except OSError:
                    pass
                try:
                    os.set_inheritable(slave_fd, False)
                except (OSError, ValueError):
                    pass

            self.proc = subprocess.Popen(
                argv,
                cwd=cwd,
                env=env,
                stdin=slave_fd,
                stdout=slave_fd,
                stderr=slave_fd,
                bufsize=0,
                close_fds=True,
                pass_fds=(slave_fd,),
                preexec_fn=_child_setup,
            )
        except Exception:
            for fd in (master_fd, slave_fd):
                try:
                    os.close(fd)
                except OSError:
                    pass
            raise
        os.close(slave_fd)
        self.master_fd = master_fd
        os.set_blocking(master_fd, False)
        self._closed = False
        self._exit_code: int | None = None
        self._eof = False

    @property
    def pid(self) -> int:
        return int(self.proc.pid)

    def poll(self) -> int | None:
        code = self.proc.poll()
        if code is not None:
            self._exit_code = int(code)
        return code

    def read(self, size: int = 4096) -> bytes | None:
        """Read available bytes; ``None`` on timeout, ``b""`` at EOF."""
        import select

        while True:
            try:
                return os.read(self.master_fd, size)
            except BlockingIOError:
                try:
                    ready, _, _ = select.select([self.master_fd], [], [], 0.2)
                except (OSError, ValueError):
                    return b""
                if not ready:
                    return None
            except InterruptedError:
                continue
            except OSError as exc:
                if exc.errno in (errno.EIO, errno.EBADF):
                    return b""
                raise

    def write(self, text: str) -> int:
        import select

        payload = text.encode("utf-8", "replace")
        written = 0
        view = memoryview(payload)
        while written < len(payload):
            try:
                count = os.write(self.master_fd, view[written:])
            except BlockingIOError:
                select.select([], [self.master_fd], [], 1.0)
                continue
            except InterruptedError:
                continue
            except OSError as exc:
                if exc.errno in (errno.EIO, errno.EBADF):
                    raise EOFError("terminal is closed") from exc
                raise
            if count <= 0:
                raise EOFError("terminal is closed")
            written += count
        return written

    def resize(self, cols: int, rows: int) -> None:
        _set_winsize_fd(self.master_fd, cols, rows)

    def kill_tree(self) -> None:
        try:
            platform_support.kill_process_tree(self.proc.pid)
        except (OSError, ValueError, platform_support.PlatformError):
            pass

    def force_kill(self) -> None:
        try:
            self.proc.kill()
        except OSError:
            pass

    def close(self) -> None:
        if self._closed:
            return
        self._closed = True
        try:
            os.close(self.master_fd)
        except OSError:
            pass


class _WindowsPtyHandle:
    """A Windows ConPTY built with the optional ``pywinpty`` package.

    ``PtyProcess.spawn`` escapes argv with ``list2cmdline``, which corrupts the
    quoted command string ``cmd.exe /s /c`` expects.  ARK therefore uses the
    lower-level ``winpty.PTY.spawn`` with the same raw command line as the stdio
    backend, then wraps it in ``PtyProcess`` for reading, writing and sizing.
    """

    backend = PTY_BACKEND_PYWINPTY

    def __init__(self, raw: str | list[str], *, cwd: str, env: dict[str, str],
                 cols: int, rows: int):
        import winpty

        if isinstance(raw, str):
            appname, _, arguments = raw.partition(" ")
            cmdline = " " + arguments
        else:
            argv = list(raw)
            if not argv:
                raise ValueError("empty command")
            appname = argv[0]
            cmdline = " " + subprocess.list2cmdline(argv[1:])
        resolved = shutil.which(appname) or appname
        if not resolved or not Path(resolved).exists():
            raise FileNotFoundError(f"shell not found: {appname}")
        env_blob = "\0".join(f"{key}={value}" for key, value in env.items()) + "\0"
        pty_obj = winpty.PTY(int(cols), int(rows))
        pty_obj.spawn(resolved, cmdline=cmdline, cwd=cwd, env=env_blob)
        self.pty = winpty.PtyProcess(pty_obj)
        # ``PtyProcess.spawn`` normally records this; we constructed the object
        # directly so initialize the size used by ``getwinsize``.
        self.pty.setwinsize(int(rows), int(cols))
        self.pid = int(self.pty.pid)
        self._closed = False
        self._exit_code: int | None = None

    def poll(self) -> int | None:
        try:
            alive = bool(self.pty.isalive())
        except Exception:  # noqa: BLE001 - a broken pipe means it is gone
            alive = False
        if alive:
            return None
        if self._exit_code is None:
            try:
                status = self.pty.exitstatus
                self._exit_code = int(status) if status is not None else 0
            except Exception:  # noqa: BLE001 - status can vanish with the handle
                self._exit_code = 0
        return self._exit_code

    def read(self, size: int = 4096) -> bytes | None:
        try:
            chunk = self.pty.read(size)
        except EOFError:
            return b""
        except (OSError, ValueError):
            return b""
        if isinstance(chunk, bytes):
            return chunk
        return str(chunk).encode("utf-8", "replace")

    def write(self, text: str) -> int:
        try:
            result = self.pty.write(text)
        except EOFError as exc:
            raise EOFError("terminal is closed") from exc
        except OSError as exc:
            raise EOFError("terminal is closed") from exc
        try:
            return int(result or 0)
        except (TypeError, ValueError):
            return len(text.encode("utf-8", "replace"))

    def resize(self, cols: int, rows: int) -> None:
        self.pty.setwinsize(int(rows), int(cols))

    def kill_tree(self) -> None:
        try:
            platform_support.kill_process_tree(self.pid)
        except (OSError, ValueError, platform_support.PlatformError):
            pass

    def force_kill(self) -> None:
        try:
            self.pty.terminate(force=True)
        except Exception:  # noqa: BLE001
            pass

    def close(self) -> None:
        if self._closed:
            return
        self._closed = True
        try:
            self.pty.close(force=True)
        except Exception:  # noqa: BLE001 - closing must never raise
            pass
        # pywinpty owns a reader thread and a loopback socket pair; give the
        # thread a moment to observe the closed handle instead of leaving its
        # socket for the GC (which would emit ResourceWarnings in tests).
        reader = getattr(self.pty, "_thread", None)
        if reader is not None and reader is not threading.current_thread():
            try:
                reader.join(timeout=0.5)
            except RuntimeError:
                pass



class TerminalSession:
    """One project-scoped subprocess with a bounded append-only output buffer."""

    def __init__(self, *, terminal_id: str, command: str, cwd: str, timeout: int,
                 proc: subprocess.Popen | None, argv: list[str], pty: bool = False,
                 pty_requested: bool = False, pty_backend: str = "",
                 pty_handle: Any = None, pty_fallback: str = "",
                 cols: int = TERMINAL_DEFAULT_COLS,
                 rows: int = TERMINAL_DEFAULT_ROWS,
                 project_id: str = "", project_path: str = "",
                 creator: str = ""):
        self.id = terminal_id
        self.command = command
        self.cwd = cwd
        self.project_id = str(project_id or "")
        self.project_path = str(project_path or "")
        self.creator = str(creator or "")
        self.timeout = timeout
        self.proc = proc
        self.argv = argv
        self.pty = bool(pty)
        self.pty_requested = bool(pty_requested)
        self.pty_backend = str(pty_backend or "")
        self.pty_handle = pty_handle
        self.pty_fallback = str(pty_fallback or "")
        self.cols = int(cols)
        self.rows = int(rows)
        self.started_at = utc_now()
        self.started_monotonic = time.monotonic()
        self.finished_at = ""
        self.exit_code: int | None = None
        self.status = "running"
        self.stop_reason = ""
        self.lock = threading.RLock()
        self.entries: list[dict] = []
        self.next_seq = 1
        self.total_bytes = 0
        self.truncated = False
        self.line_count = 0
        self._pumps: list[threading.Thread] = []
        self._killing = False

    @property
    def pid(self) -> int:
        if self.pty_handle is not None:
            return int(self.pty_handle.pid)
        if self.proc is not None:
            return int(self.proc.pid)
        return 0

    def poll(self) -> int | None:
        if self.pty_handle is not None:
            return self.pty_handle.poll()
        return self.proc.poll() if self.proc is not None else None

    # -- buffer ----------------------------------------------------------
    def append(self, stream: str, text: str) -> None:
        if not text:
            return
        with self.lock:
            if self.truncated:
                return
            # PTY output keeps its raw carriage returns so the ANSI renderer
            # can redraw prompt lines; stdio output keeps the old LF contract.
            data = text if self.pty else text.replace("\r\n", "\n")
            remaining = MAX_TERMINAL_OUTPUT - self.total_bytes
            if remaining <= 0:
                self._mark_truncated_locked()
                return
            if len(data.encode("utf-8", "replace")) > remaining:
                keep = data.encode("utf-8", "replace")[:max(0, remaining)].decode("utf-8", "ignore")
                if keep:
                    self._push_locked(stream, keep)
                self._mark_truncated_locked()
                return
            self._push_locked(stream, data)

    def _push_locked(self, stream: str, text: str) -> None:
        self.entries.append({"seq": self.next_seq, "stream": stream, "text": text,
                             "t": utc_now()})
        self.next_seq += 1
        self.total_bytes += len(text.encode("utf-8", "replace"))
        self.line_count += text.count("\n")
        while len(self.entries) > MAX_TERMINAL_ENTRIES:
            self.entries.pop(0)

    def _mark_truncated_locked(self) -> None:
        if self.truncated:
            return
        self.truncated = True
        self.stop_reason = self.stop_reason or "output_limit"
        self._push_locked("system", "\n…[output limit reached; command stopped by ARK]\n")

    def snapshot(self, offset: int = 0, limit: int = 200) -> dict:
        with self.lock:
            try:
                limit = max(0, min(1000, int(limit)))
            except (TypeError, ValueError):
                limit = 200
            start = 0
            if offset and offset > 0:
                # Entries are ordered by seq; skip the first entry with seq > offset.
                lo, hi = 0, len(self.entries)
                while lo < hi:
                    mid = (lo + hi) // 2
                    if self.entries[mid]["seq"] <= offset:
                        lo = mid + 1
                    else:
                        hi = mid
                start = lo
            entries = self.entries[start:start + limit]
            next_offset = entries[-1]["seq"] if entries else offset
            first_seq = self.entries[0]["seq"] if self.entries else 1
            return {
                "id": self.id,
                "command": self.command,
                "cwd": self.cwd,
                "project_id": self.project_id,
                "project_path": self.project_path,
                "creator": self.creator,
                "status": self.status,
                "exit_code": self.exit_code,
                "stop_reason": self.stop_reason,
                "entries": entries,
                "offset": next_offset,
                "dropped_before": max(0, first_seq - 1),
                "truncated": self.truncated,
                "started_at": self.started_at,
                "finished_at": self.finished_at,
                "timeout": self.timeout,
                "total_bytes": self.total_bytes,
                "lines": self.line_count,
                "pty": self.pty,
                "pty_requested": self.pty_requested,
                "pty_backend": self.pty_backend,
                "pty_fallback": self.pty_fallback,
                "cols": self.cols,
                "rows": self.rows,
            }

    @property
    def is_running(self) -> bool:
        with self.lock:
            return self.status in {"running", "stopping"}

    def public(self) -> dict:
        return self.snapshot(0, 0)


class TerminalManager:
    """Bounded registry of terminal sessions owned by one App instance."""

    def __init__(self) -> None:
        self.lock = threading.RLock()
        self._sessions: dict[str, TerminalSession] = {}

    # -- lifecycle -------------------------------------------------------
    def start(self, root: str | Path, command: str, *, cwd: str = "",
              timeout: int | float = TERMINAL_DEFAULT_TIMEOUT, pty: bool = False,
              cols: int | float = TERMINAL_DEFAULT_COLS,
              rows: int | float = TERMINAL_DEFAULT_ROWS,
              project_id: str = "", project_path: str = "",
              creator: str = "") -> TerminalSession:
        root_path = Path(root).resolve()
        text = str(command or "").strip()
        if not text:
            raise WorkspaceError("command is required")
        if len(text) > MAX_TERMINAL_COMMAND:
            raise WorkspaceError(f"command is too long (limit {MAX_TERMINAL_COMMAND} characters)")
        if "\x00" in text:
            raise WorkspaceError("command contains a NUL byte")
        if "\r" in text or "\n" in text:
            raise WorkspaceError("command must be a single line")
        target, cwd_rel = _resolve(root_path, cwd)
        if not target.is_dir():
            raise WorkspaceError("cwd must be a folder inside the project")
        try:
            timeout_value = int(float(timeout))
        except (TypeError, ValueError):
            timeout_value = TERMINAL_DEFAULT_TIMEOUT
        timeout_value = max(1, min(TERMINAL_MAX_TIMEOUT, timeout_value))
        pty_requested = bool(pty)
        cols_value, rows_value = _terminal_size(cols, rows, strict=pty_requested)
        self.sweep()
        with self.lock:
            active = sum(1 for item in self._sessions.values() if item.is_running)
            if active >= MAX_TERMINALS:
                raise WorkspaceError(
                    f"too many running terminals (limit {MAX_TERMINALS}); stop one first", 409)
        try:
            popen_arg = platform_support.run_command_args(text)
        except platform_support.PlatformError as exc:
            raise WorkspaceError(str(exc), getattr(exc, "status", 400)) from exc
        display_argv = platform_support.shell_command(text)
        availability = pty_availability() if pty_requested else {
            "available": False, "backend": "", "reason": "", "hint": ""}
        pty_handle: Any = None
        pty_fallback = ""
        if pty_requested and not availability.get("available"):
            pty_fallback = availability.get("reason") or "PTY support is unavailable"
        if pty_requested and availability.get("available"):
            try:
                if os.name == "nt":
                    pty_handle = _WindowsPtyHandle(
                        popen_arg, cwd=str(target), env=_terminal_env(pty=True),
                        cols=cols_value, rows=rows_value)
                else:
                    pty_handle = _PosixPtyHandle(
                        list(popen_arg), cwd=str(target), env=_terminal_env(pty=True),
                        cols=cols_value, rows=rows_value)
            except Exception as exc:  # noqa: BLE001 - fall back to stdio, never fail open
                pty_handle = None
                pty_fallback = f"PTY could not start ({exc}); using stdio"
        proc: subprocess.Popen | None = None
        if pty_handle is None:
            kwargs: dict[str, Any] = {}
            if os.name == "nt":
                kwargs["creationflags"] = (
                    getattr(subprocess, "CREATE_NEW_PROCESS_GROUP", 0)
                    | getattr(subprocess, "CREATE_NO_WINDOW", 0)
                )
            else:
                kwargs["start_new_session"] = True
            try:
                proc = subprocess.Popen(
                    popen_arg,
                    cwd=str(target),
                    env=_terminal_env(pty=False),
                    stdin=subprocess.PIPE,
                    stdout=subprocess.PIPE,
                    stderr=subprocess.PIPE,
                    bufsize=-1,
                    **kwargs,
                )
            except (OSError, ValueError) as exc:
                raise WorkspaceError(f"could not start command: {exc}", 400) from exc
        effective_pty = pty_handle is not None
        terminal = TerminalSession(
            terminal_id="term_" + uuid.uuid4().hex[:12],
            command=text, cwd=cwd_rel, timeout=timeout_value, proc=proc, argv=display_argv,
            pty=effective_pty, pty_requested=pty_requested,
            pty_backend=(PTY_BACKEND_PYWINPTY if os.name == "nt" else PTY_BACKEND_POSIX)
            if effective_pty else "",
            pty_handle=pty_handle, pty_fallback=pty_fallback,
            cols=cols_value, rows=rows_value,
            project_id=project_id, project_path=project_path, creator=creator)
        with self.lock:
            self._sessions[terminal.id] = terminal
        if effective_pty:
            thread = threading.Thread(
                target=self._pump_pty, args=(terminal,),
                name=f"ark-terminal-{terminal.id}-pty", daemon=True)
            thread.start()
            terminal._pumps.append(thread)
        else:
            for name, pipe in (("stdout", proc.stdout), ("stderr", proc.stderr)):
                thread = threading.Thread(
                    target=self._pump, args=(terminal, name, pipe),
                    name=f"ark-terminal-{terminal.id}-{name}", daemon=True)
                thread.start()
                terminal._pumps.append(thread)
        threading.Thread(
            target=self._watch, args=(terminal,),
            name=f"ark-terminal-{terminal.id}-watch", daemon=True).start()
        return terminal

    def get(self, terminal_id: str, project_id: str | None = None) -> TerminalSession:
        with self.lock:
            terminal = self._sessions.get(str(terminal_id or ""))
        if terminal is None:
            raise WorkspaceError("terminal not found", 404)
        if project_id is not None and terminal.project_id != str(project_id or ""):
            # Never reveal another project's terminal, even to a project admin.
            raise WorkspaceError("terminal not found", 404)
        return terminal

    def list(self, project_id: str | None = None) -> dict:
        with self.lock:
            items = list(self._sessions.values())
        if project_id is not None:
            items = [item for item in items if item.project_id == str(project_id or "")]
        items.sort(key=lambda item: item.started_at, reverse=True)
        return {
            "terminals": [item.public() for item in items],
            "count": len(items),
            "running": sum(1 for item in items if item.is_running),
            "limit": MAX_TERMINALS,
        }

    def read(self, terminal_id: str, offset: int = 0, limit: int = 200) -> dict:
        terminal = self.get(terminal_id)
        try:
            offset = max(0, int(offset))
        except (TypeError, ValueError):
            offset = 0
        try:
            limit = max(1, min(1000, int(limit)))
        except (TypeError, ValueError):
            limit = 200
        return terminal.snapshot(offset, limit)

    def write(self, terminal_id: str, text: str) -> dict:
        terminal = self.get(terminal_id)
        value = str(text or "")
        if not value:
            raise WorkspaceError("stdin text is required")
        if len(value) > 8000:
            raise WorkspaceError("stdin text is too long (limit 8000 characters)")
        with terminal.lock:
            if terminal.status not in {"running", "stopping"}:
                raise WorkspaceError("terminal is not running", 409)
        if terminal.pty and terminal.pty_handle is not None:
            # Enter in a real terminal is CR; canonical tty input maps it to NL
            # for line-oriented programs, while raw-mode TUIs see the key they
            # expect.  PTY echo is produced by the child, so no synthetic echo
            # entry is added.
            payload = value if value.endswith(("\r", "\n")) else value + "\r"
            try:
                terminal.pty_handle.write(payload)
            except (EOFError, OSError, ValueError) as exc:
                raise WorkspaceError("terminal stdin is closed", 409) from exc
            return terminal.snapshot(0, 0)
        proc = terminal.proc
        if proc is None or proc.stdin is None:
            raise WorkspaceError("terminal stdin is closed", 409)
        payload = value if value.endswith("\n") else value + "\n"
        try:
            proc.stdin.write(payload.encode("utf-8"))
            proc.stdin.flush()
        except (BrokenPipeError, OSError, ValueError) as exc:
            raise WorkspaceError("terminal stdin is closed", 409) from exc
        terminal.append("stdin", "› " + value.rstrip("\r\n") + "\n")
        return terminal.snapshot(0, 0)

    def resize(self, terminal_id: str, cols: int | float, rows: int | float) -> dict:
        """Resize a live PTY (no-op for stdio sessions, with an honest flag)."""
        terminal = self.get(terminal_id)
        cols_value, rows_value = _terminal_size(cols, rows, strict=True)
        with terminal.lock:
            if terminal.status not in {"running", "stopping"}:
                raise WorkspaceError("terminal is not running", 409)
            terminal.cols = cols_value
            terminal.rows = rows_value
            is_pty = terminal.pty
            handle = terminal.pty_handle
        if not is_pty or handle is None:
            return {
                "terminal": terminal.snapshot(0, 0),
                "resized": False,
                "reason": "terminal is not a PTY; stdio pipes have no window size",
            }
        try:
            handle.resize(cols_value, rows_value)
        except (OSError, ValueError) as exc:
            raise WorkspaceError(f"could not resize terminal: {exc}", 409) from exc
        return {"terminal": terminal.snapshot(0, 0), "resized": True, "reason": ""}

    def cancel(self, terminal_id: str) -> dict:
        terminal = self.get(terminal_id)
        with terminal.lock:
            if terminal.status not in {"running", "stopping"}:
                return terminal.snapshot(0, 0)
            terminal.status = "stopping"
            terminal.stop_reason = terminal.stop_reason or "cancelled"
        threading.Thread(target=self._kill, args=(terminal,),
                         name=f"ark-terminal-{terminal.id}-cancel", daemon=True).start()
        return terminal.snapshot(0, 0)

    def close(self, terminal_id: str) -> dict:
        terminal = self.get(terminal_id)
        if terminal.is_running:
            raise WorkspaceError("stop the terminal before closing it", 409)
        with self.lock:
            self._sessions.pop(terminal.id, None)
        return {"closed": terminal.id}

    def sweep(self) -> int:
        """Drop finished terminals older than ten minutes (bounded registry)."""
        cutoff = time.monotonic() - 600
        removed = 0
        with self.lock:
            for key, item in list(self._sessions.items()):
                if not item.is_running and item.started_monotonic < cutoff:
                    self._sessions.pop(key, None)
                    removed += 1
        return removed

    def shutdown(self) -> None:
        with self.lock:
            items = list(self._sessions.values())
        for terminal in items:
            if not terminal.is_running:
                continue
            with terminal.lock:
                terminal.status = "stopping"
                terminal.stop_reason = terminal.stop_reason or "server_shutdown"
            self._kill(terminal)

    # -- internals -------------------------------------------------------
    def _pump(self, terminal: TerminalSession, stream: str, pipe) -> None:
        decoder = codecs.getincrementaldecoder("utf-8")("replace")
        try:
            while True:
                try:
                    chunk = pipe.read1(4096) if hasattr(pipe, "read1") else pipe.read(4096)
                except (OSError, ValueError):
                    break
                if not chunk:
                    break
                terminal.append(stream, decoder.decode(chunk))
        finally:
            try:
                tail = decoder.decode(b"", final=True)
                if tail:
                    terminal.append(stream, tail)
            except (UnicodeDecodeError, ValueError):
                pass
            try:
                pipe.close()
            except (OSError, ValueError):
                pass

    def _pump_pty(self, terminal: TerminalSession) -> None:
        """Read merged stdout/stderr bytes from the PTY master/handle."""
        decoder = codecs.getincrementaldecoder("utf-8")("replace")
        handle = terminal.pty_handle
        try:
            while True:
                try:
                    chunk = handle.read(4096)
                except (EOFError, OSError, ValueError):
                    break
                if chunk is None:
                    # POSIX read timed out.  Keep waiting while the child runs;
                    # once it is gone, one last read is unnecessary because a
                    # readable master is returned by select before EOF.
                    if handle.poll() is not None:
                        break
                    continue
                if not chunk:
                    break
                terminal.append("stdout", decoder.decode(chunk))
        finally:
            try:
                tail = decoder.decode(b"", final=True)
                if tail:
                    terminal.append("stdout", tail)
            except (UnicodeDecodeError, ValueError):
                pass

    def _watch(self, terminal: TerminalSession) -> None:
        while True:
            code = terminal.poll()
            if code is not None:
                break
            now = time.monotonic()
            with terminal.lock:
                reason = terminal.stop_reason
            if reason and not terminal._killing:
                terminal._killing = True
                self._kill_process(terminal)
            elif not reason and now - terminal.started_monotonic >= terminal.timeout:
                with terminal.lock:
                    terminal.status = "stopping"
                    terminal.stop_reason = "timeout"
                terminal._killing = True
                self._kill_process(terminal)
            time.sleep(0.1)
        # Drain the pumps, but never hang forever on a blocked PTY reader.
        deadline = time.monotonic() + 1.5
        for thread in terminal._pumps:
            thread.join(timeout=max(0.05, deadline - time.monotonic()))
        if terminal.pty_handle is not None and any(
                thread.is_alive() for thread in terminal._pumps):
            terminal.pty_handle.close()
            for thread in terminal._pumps:
                thread.join(timeout=0.5)
        with terminal.lock:
            terminal.exit_code = code
            terminal.finished_at = utc_now()
            terminal.status = (
                "cancelled" if terminal.stop_reason in {"cancelled", "server_shutdown"}
                else "timeout" if terminal.stop_reason == "timeout"
                else "output_limit" if terminal.stop_reason == "output_limit"
                else "exited"
            )
            if terminal.stop_reason == "output_limit" and not terminal.truncated:
                terminal._mark_truncated_locked()
        if terminal.pty_handle is not None:
            terminal.pty_handle.close()
        elif terminal.proc is not None:
            try:
                if terminal.proc.stdin is not None:
                    terminal.proc.stdin.close()
            except (OSError, ValueError):
                pass

    def _kill_process(self, terminal: TerminalSession) -> None:
        if terminal.pty_handle is not None:
            handle = terminal.pty_handle
            try:
                if handle.poll() is None:
                    handle.kill_tree()
            except (OSError, ValueError, subprocess.SubprocessError):
                pass
            deadline = time.monotonic() + 5
            while handle.poll() is None and time.monotonic() < deadline:
                time.sleep(0.05)
            try:
                if handle.poll() is None:
                    handle.force_kill()
            except (OSError, ValueError):
                pass
            return
        proc = terminal.proc
        if proc is None or proc.poll() is not None:
            return
        try:
            platform_support.kill_process_tree(proc.pid)
        except (OSError, ValueError, subprocess.SubprocessError):
            try:
                proc.terminate()
            except OSError:
                pass
        try:
            proc.wait(timeout=5)
        except subprocess.TimeoutExpired:
            try:
                proc.kill()
            except OSError:
                pass

    def _kill(self, terminal: TerminalSession) -> None:
        if terminal._killing:
            return
        terminal._killing = True
        self._kill_process(terminal)
