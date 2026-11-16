"""Restorable project ZIPs with an online SQLite backup and internal files."""
from __future__ import annotations

from contextlib import closing
from datetime import datetime, timezone
import json
import os
from pathlib import Path
import sqlite3
import stat
import tempfile
import zipfile

from .projects import Project


_EXCLUDE_DIRS = {".git", ".venv", "venv", "__pycache__", ".pytest_cache", "node_modules", "exports"}
_EXCLUDE_FILES = {"ark.sqlite3", "ark.sqlite3-wal", "ark.sqlite3-shm", "ark.sqlite3-journal", "project.json"}
# Stored agent session transcripts (docs/agents/session-context.md) are opt-in.
SESSIONS_DIR = "sessions"


def _is_link(path: Path) -> bool:
    return path.is_symlink() or bool(getattr(path.lstat(), "st_file_attributes", 0)
                                    & getattr(stat, "FILE_ATTRIBUTE_REPARSE_POINT", 0))


def _internal_files(root: Path, *, include_sessions: bool = False):
    """Do not follow symlinks, Windows junctions or references outside the project."""
    for folder, directories, files in os.walk(root, followlinks=False):
        base = Path(folder)
        directories[:] = sorted(name for name in directories if name not in _EXCLUDE_DIRS
                                and not (base == root and name == SESSIONS_DIR and not include_sessions)
                                and not _is_link(base / name)
                                and (base / name).resolve().is_relative_to(root))
        for name in sorted(files):
            source = base / name
            if base == root and name in _EXCLUDE_FILES:
                continue
            if not _is_link(source) and source.resolve().is_relative_to(root) and source.is_file():
                yield source, source.relative_to(root).as_posix()


def write_project_archive(project: Project, target, *, include_sessions: bool = False) -> dict:
    """``include_sessions`` adds ``sessions/``: uploaded external transcripts and the
    redacted mirrors of ARK task sessions (``sessions/internal/<id>/``)."""
    root = project.path.resolve()
    if isinstance(target, (str, os.PathLike)) and Path(target).resolve().is_relative_to(root):
        raise ValueError("Write the project archive outside the project folder")
    folder = root.name
    if folder in {"", ".", ".."} or "\\" in folder:
        raise ValueError("Invalid project folder name")
    if not folder.endswith(".ark"):
        folder += ".ark"
    manifest = {"format": "ark-studio-project-archive", "version": 1, "folder": folder,
                "exported_at": datetime.now(timezone.utc).isoformat(timespec="seconds"),
                "excluded": ["external files and symlinks", "exports/", "development caches"]
                + ([] if include_sessions else ["sessions/ (agent session transcripts)"]),
                "sessions_included": bool(include_sessions),
                "restore": "Extract the .ark folder into an ARK projects directory, then open it in ARK."}
    with tempfile.TemporaryDirectory(prefix="ark-project-snapshot-") as scratch:
        database = Path(scratch) / "ark.sqlite3"
        with zipfile.ZipFile(target, "w", compression=zipfile.ZIP_DEFLATED, compresslevel=5, allowZip64=True) as archive:
            # History objects and pending text state must describe the same
            # checkpoint. Copy these while writes to this Store are excluded.
            with project.store.lock:
                with closing(sqlite3.connect(database)) as destination:
                    project.store.con.backup(destination)
                archive.write(database, f"{folder}/ark.sqlite3")
                archive.writestr(f"{folder}/project.json", json.dumps(project.manifest, ensure_ascii=False, indent=2) + "\n")
                files = list(_internal_files(root, include_sessions=include_sessions))
                text_state = [(source, rel) for source, rel in files if rel.startswith(("history.git/", "logs/"))]
                for source, relative in text_state:
                    archive.write(source, f"{folder}/{relative}")
            # Large videos and attachments stream directly into the ZIP after
            # releasing the database lock; no whole-project buffer is retained.
            for source, relative in files:
                if not relative.startswith(("history.git/", "logs/")):
                    archive.write(source, f"{folder}/{relative}")
            if include_sessions:
                manifest["internal_sessions"] = len({rel.split("/")[2] for _, rel in files
                                                     if rel.startswith(f"{SESSIONS_DIR}/internal/")
                                                     and rel.count("/") >= 3})
            manifest["files"] = len(archive.infolist())
            archive.writestr("ARK-EXPORT.json", json.dumps(manifest, ensure_ascii=False, indent=2) + "\n")
    return manifest
