"""Out-of-band SQLite snapshots for project rollback.

The black box lives next to the project root (``runtime/blackbox`` by
default), never inside a project folder.  It uses SQLite's online backup API
so snapshots are consistent while ARK is serving requests, then compresses the
database and records a small manifest.  Assets are represented by a manifest
and are not duplicated for every snapshot.
"""
from __future__ import annotations

import gzip
import hashlib
import json
import os
import shutil
import sqlite3
import threading
import time
import uuid
from datetime import datetime, timezone
from pathlib import Path



def _utc() -> str:
    return datetime.now(timezone.utc).isoformat(timespec="seconds")


def _stamp() -> str:
    return datetime.now(timezone.utc).strftime("%Y%m%dT%H%M%SZ")


def _sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        for chunk in iter(lambda: handle.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def _safe_id(value: str) -> str:
    cleaned = "".join(ch for ch in str(value or "") if ch.isalnum() or ch in "._-")
    return cleaned[:120] or "project"


def _write_json(path: Path, payload: dict) -> None:
    path.write_text(json.dumps(payload, ensure_ascii=False, indent=2, sort_keys=True) + "\n",
                    encoding="utf-8")


class BlackBox:
    """Small, dependency-free project snapshot vault."""

    def __init__(self, root: str | Path, *, project_provider=None,
                 interval_seconds: int = 900, enabled: bool = True):
        self.root = Path(root).expanduser().resolve()
        self.root.mkdir(parents=True, exist_ok=True)
        try:
            os.chmod(self.root, 0o700)
        except OSError:
            pass
        self.project_provider = project_provider
        self.interval_seconds = max(60, int(interval_seconds or 900))
        self.enabled = bool(enabled)
        self._lock = threading.RLock()
        self._stop = threading.Event()
        self._thread: threading.Thread | None = None
        self._last_revision: dict[str, str] = {}
        self._last_snapshot_at: dict[str, float] = {}

    # --------------------------------------------------------------- lifecycle
    def start(self) -> None:
        if not self.enabled or (self._thread and self._thread.is_alive()):
            return
        self._stop.clear()
        self._thread = threading.Thread(target=self._loop, name="ark-blackbox",
                                        daemon=True)
        self._thread.start()

    def stop(self) -> None:
        self._stop.set()
        thread = self._thread
        self._thread = None
        if thread and thread.is_alive():
            thread.join(timeout=3)

    def _loop(self) -> None:
        while not self._stop.wait(30):
            if not self.enabled or self.project_provider is None:
                continue
            project = None
            try:
                project = self.project_provider()
            except Exception:
                continue
            if project is None:
                continue
            try:
                with self._lock:
                    revision = project.store.revision()
                    last = self._last_revision.get(project.id)
                    last_at = self._last_snapshot_at.get(project.id, 0)
                    if revision == last:
                        continue
                    if time.time() - last_at < self.interval_seconds:
                        continue
                self.snapshot(project, "periodic")
            except Exception:
                # Snapshots are best effort; a failed backup must never stop
                # the web server or the active project.
                continue

    # ---------------------------------------------------------------- snapshots
    def _project_root(self, project) -> Path:
        path = self.root / _safe_id(project.id)
        path.mkdir(parents=True, exist_ok=True)
        try:
            os.chmod(path, 0o700)
        except OSError:
            pass
        return path

    def _snapshot_dirs(self, project) -> list[Path]:
        root = self._project_root(project)
        return sorted((item for item in root.iterdir()
                       if item.is_dir() and not item.name.startswith(".")
                       and (item / "manifest.json").is_file()),
                      key=lambda item: item.name, reverse=True)

    def list(self, project) -> list[dict]:
        snapshots = []
        for folder in self._snapshot_dirs(project):
            try:
                manifest = json.loads((folder / "manifest.json").read_text(encoding="utf-8"))
                manifest["id"] = folder.name
                snapshots.append(manifest)
            except (OSError, ValueError):
                continue
        return snapshots

    def get(self, project, snapshot_id: str) -> dict | None:
        safe = _safe_id(snapshot_id)
        folder = self._project_root(project) / safe
        manifest_path = folder / "manifest.json"
        if not manifest_path.is_file():
            return None
        try:
            manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
        except (OSError, ValueError):
            return None
        manifest["id"] = safe
        return manifest

    def snapshot(self, project, reason: str = "manual", *, force: bool = False) -> dict:
        if not self.enabled:
            raise ValueError("black box is disabled")
        db_path = project.path / "ark.sqlite3"
        if not db_path.is_file():
            raise FileNotFoundError("project database does not exist")
        with self._lock:
            revision = project.store.revision()
            if not force and reason == "periodic" and revision == self._last_revision.get(project.id):
                return self.list(project)[0]
            project_root = self._project_root(project)
            base = f"{_stamp()}-{_safe_id(reason)}"
            target = project_root / base
            if target.exists():
                target = project_root / f"{base}-{uuid.uuid4().hex[:8]}"
            temp = project_root / f".tmp-{uuid.uuid4().hex}"
            temp.mkdir(parents=True)
            try:
                # A source connection independent from Store.con sees the live
                # WAL state and makes a consistent online backup.
                src = sqlite3.connect(str(db_path), timeout=10)
                dst = sqlite3.connect(str(temp / "ark.sqlite3"))
                try:
                    src.backup(dst)
                    integrity = dst.execute("PRAGMA integrity_check").fetchone()[0]
                    units = dst.execute("SELECT COUNT(*) FROM units").fetchone()[0]
                    links = dst.execute("SELECT COUNT(*) FROM unit_links").fetchone()[0]
                finally:
                    dst.close()
                    src.close()
                if str(integrity).lower() != "ok":
                    raise ValueError(f"snapshot integrity check failed: {integrity}")
                raw = temp / "ark.sqlite3"
                raw_sha = _sha256(raw)
                raw_size = raw.stat().st_size
                packed = temp / "ark.sqlite3.gz"
                with raw.open("rb") as source, gzip.open(packed, "wb", compresslevel=6) as sink:
                    shutil.copyfileobj(source, sink, length=1024 * 1024)
                raw.unlink()
                packed_sha = _sha256(packed)
                manifest_path = project.path / "project.json"
                if manifest_path.is_file():
                    shutil.copy2(manifest_path, temp / "project.json")
                assets_dir = project.path / "assets"
                assets = []
                if assets_dir.is_dir():
                    for item in sorted(assets_dir.rglob("*")):
                        if not item.is_file():
                            continue
                        rel = item.relative_to(project.path).as_posix()
                        assets.append({"path": rel, "size": item.stat().st_size,
                                       "sha256": _sha256(item)})
                _write_json(temp / "assets.json", {"assets": assets,
                                                   "count": len(assets)})
                manifest = {
                    "format": "ark-blackbox-snapshot",
                    "version": 1,
                    "reason": _safe_id(reason),
                    "created_at": _utc(),
                    "project_id": project.id,
                    "project_name": project.name,
                    "project_path": str(project.path),
                    "revision": revision,
                    "db_sha256": raw_sha,
                    "db_size": raw_size,
                    "snapshot_sha256": packed_sha,
                    "snapshot_size": packed.stat().st_size,
                    "unit_count": int(units),
                    "link_count": int(links),
                    "asset_count": len(assets),
                    "integrity": "ok",
                    "ark_version": "2.0",
                }
                _write_json(temp / "manifest.json", manifest)
                temp.rename(target)
            except Exception:
                shutil.rmtree(temp, ignore_errors=True)
                raise
            self._last_revision[project.id] = revision
            self._last_snapshot_at[project.id] = time.time()
            self.prune(project)
            manifest["id"] = target.name
            return manifest

    # ------------------------------------------------------------------ retention
    def prune(self, project) -> None:
        snapshots = self.list(project)
        if not snapshots:
            return
        keep: set[str] = set(item["id"] for item in snapshots[:12])
        pre_agent = [item for item in snapshots if item.get("reason") == "pre-agent"]
        keep.update(item["id"] for item in pre_agent[:20])
        hourly: dict[str, str] = {}
        daily: dict[str, str] = {}
        for item in snapshots:
            created = str(item.get("created_at") or "")
            if len(created) >= 13:
                hourly.setdefault(created[:13], item["id"])
            if len(created) >= 10:
                daily.setdefault(created[:10], item["id"])
        keep.update(list(hourly.values())[:24])
        keep.update(list(daily.values())[:14])
        for item in snapshots:
            if item["id"] in keep:
                continue
            shutil.rmtree(self._project_root(project) / item["id"], ignore_errors=True)

    # -------------------------------------------------------------------- restore
    def restore(self, project, snapshot_id: str) -> dict:
        """Restore a snapshot and replace the live Store connection.

        Caller should hold the application lock (the HTTP route does); a
        pre-restore snapshot is always created first.
        """
        if not self.enabled:
            raise ValueError("black box is disabled")
        manifest = self.get(project, snapshot_id)
        if manifest is None:
            raise FileNotFoundError("snapshot not found")
        folder = self._project_root(project) / _safe_id(snapshot_id)
        archive = folder / "ark.sqlite3.gz"
        if not archive.is_file():
            raise FileNotFoundError("snapshot database is missing")
        temp = project.path / f".restore-{uuid.uuid4().hex}.sqlite3"
        try:
            # Materialize and verify the target before pruning can touch an old
            # snapshot, then take the mandatory pre-restore snapshot.
            with gzip.open(archive, "rb") as source, temp.open("wb") as sink:
                shutil.copyfileobj(source, sink, length=1024 * 1024)
            check = sqlite3.connect(str(temp))
            try:
                result = check.execute("PRAGMA integrity_check").fetchone()[0]
            finally:
                check.close()
            if str(result).lower() != "ok":
                raise ValueError(f"snapshot integrity check failed: {result}")
            self.snapshot(project, "pre-restore", force=True)
            with project.store.lock:
                project.store.close()
                for suffix in ("-wal", "-shm"):
                    try:
                        (project.path / f"ark.sqlite3{suffix}").unlink()
                    except FileNotFoundError:
                        pass
                os.replace(temp, project.path / "ark.sqlite3")
                from .store import Store
                project.store = Store(project.path, project.manifest)
                # Cached client snapshots of the replaced database are not
                # deltas of this one (GET /api/changes resets them).
                project.store.reset_change_log_id()
            self._last_revision[project.id] = project.store.revision()
            self._last_snapshot_at[project.id] = time.time()
            return {"snapshot": manifest, "revision": self._last_revision[project.id]}
        finally:
            try:
                temp.unlink()
            except FileNotFoundError:
                pass
