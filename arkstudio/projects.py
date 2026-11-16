"""Project folders, registry by discovery, and the first-run demo.

A project is a folder ending in ``.ark``::

    PredActor.ark/
      project.json          identity + display metadata
      ark.sqlite3           units, tags, links, media, activity
      assets/               local media (optional)
      exports/              generated exports
      logs/activity.jsonl   append-only mirror of the activity table

There is no hidden global database: copying a folder copies the project.
"""
from __future__ import annotations

import json
import re
import secrets
import shutil
from datetime import datetime, timezone
from pathlib import Path

from . import db
from .settings import project_settings
from .store import Store


class ProjectError(ValueError):
    pass


def _slugify(name: str) -> str:
    slug = re.sub(r"[^a-z0-9]+", "-", name.lower()).strip("-")
    slug = re.sub(r"-{2,}", "-", slug)[:48].strip("-")
    return slug or "project"


def _new_id() -> str:
    return "prj_" + secrets.token_hex(5)


class Project:
    def __init__(self, path: Path, manifest: dict, store: Store):
        self.path = Path(path)
        self.manifest = manifest
        self.store = store

    @property
    def id(self) -> str:
        return str(self.manifest.get("id") or self.path.name)

    @property
    def name(self) -> str:
        return str(self.manifest.get("name") or self.path.stem)

    def summary(self) -> dict:
        return {
            "id": self.id,
            "name": self.name,
            "description": self.manifest.get("description", ""),
            "path": str(self.path),
            "folder": self.path.name,
            "created": self.manifest.get("created"),
            "updated": self.manifest.get("updated"),
            "template": self.manifest.get("template", "blank"),
            "demo": bool(self.manifest.get("demo")),
            "accent": self.manifest.get("accent", "#2563eb"),
            "settings": project_settings(self.manifest),
        }

    def close(self) -> None:
        self.store.close()


class ProjectManager:
    def __init__(self, root: str | Path, *, seed_demo: bool = True):
        self.root = Path(root).resolve()
        self.root.mkdir(parents=True, exist_ok=True)
        self.agent_token = self._ensure_token()
        self._active_id: str | None = None
        self._initial: Project | None = None
        if seed_demo and not self.list_projects():
            self._initial = self.create("PredActor", template="demo", demo=True,
                                        description="A demo research project: from claims to agent tasks.")

    def take_initial(self) -> Project | None:
        project, self._initial = self._initial, None
        return project

    def _ensure_token(self) -> str:
        path = self.root / ".agent-token"
        try:
            token = path.read_text(encoding="utf-8").strip()
            if len(token) >= 24:
                return token
        except OSError:
            pass
        token = secrets.token_urlsafe(32)
        try:
            path.write_text(token + "\n", encoding="utf-8")
        except OSError:
            pass
        return token

    # ---------------------------------------------------------------- discovery
    def list_projects(self) -> list[dict]:
        projects = []
        for folder in sorted(self.root.iterdir(), key=lambda p: p.stat().st_mtime if p.exists() else 0,
                             reverse=True):
            if not folder.is_dir() or folder.name.startswith("."):
                continue
            manifest = db.read_manifest(folder)
            if not manifest:
                continue
            projects.append(
                {
                    "id": manifest.get("id", folder.name),
                    "name": manifest.get("name", folder.stem),
                    "description": manifest.get("description", ""),
                    "path": str(folder),
                    "folder": folder.name,
                    "created": manifest.get("created"),
                    "updated": manifest.get("updated"),
                    "template": manifest.get("template", "blank"),
                    "demo": bool(manifest.get("demo")),
                    "accent": manifest.get("accent", "#2563eb"),
                }
            )
        projects.sort(key=lambda p: p.get("updated") or p.get("created") or "", reverse=True)
        return projects

    def open(self, ref: str) -> Project:
        ref = str(ref or "").strip()
        if not ref:
            raise ProjectError("project path is required")
        path = Path(ref)
        if not path.is_absolute():
            path = self.root / ref
        path = path.resolve()
        if not path.is_dir():
            raise ProjectError(f"project folder not found: {path}")
        manifest = db.read_manifest(path)
        if not manifest:
            raise ProjectError(f"not an ARK project (project.json missing): {path}")
        store = Store(path, manifest)
        project = Project(path, manifest, store)
        self._active_id = project.id
        return project

    def open_by_id(self, project_id: str) -> Project:
        for summary in self.list_projects():
            if summary["id"] == project_id or summary["folder"] == project_id:
                return self.open(summary["path"])
        raise ProjectError(f"project not found: {project_id}")

    # ------------------------------------------------------------------- create
    def create(self, name: str, *, description: str = "", template: str = "blank",
               demo: bool = False, accent: str = "#2563eb") -> Project:
        name = str(name or "").strip()
        if not name:
            raise ProjectError("project name is required")
        if len(name) > 80:
            raise ProjectError("project name is too long")
        template = template if template in {"blank", "paper", "demo"} else "blank"
        base = _slugify(name)
        folder = self.root / f"{base}.ark"
        n = 2
        while folder.exists():
            folder = self.root / f"{base}-{n}.ark"
            n += 1
        folder.mkdir(parents=True, exist_ok=False)
        for sub in ("assets", "exports", "logs"):
            (folder / sub).mkdir(exist_ok=True)
        now = db.utc_now()
        manifest = {
            "format": "ark-studio-project",
            "version": 1,
            "id": _new_id(),
            "name": name,
            "description": description,
            "created": now,
            "updated": now,
            "template": template,
            "demo": bool(demo),
            "accent": accent,
            "settings": {"graph": project_settings({})["graph"]},
        }
        db.write_manifest(folder, manifest)
        (folder / "README.md").write_text(
            f"# {name}\n\n"
            "This is an ARK Studio project folder. Open it from the workbench's "
            "project switcher, or run `python -m arkstudio serve --root <parent-folder>`.\n\n"
            "- `project.json` — identity and display metadata\n"
            "- `ark.sqlite3` — units, tags, links, media references, activity\n"
            "- `assets/` — local media (optional)\n"
            "- `exports/` — generated exports\n"
            "- `logs/activity.jsonl` — append-only activity mirror\n\n"
            "Copy the whole folder to back up or move the project.\n",
            encoding="utf-8",
        )
        store = Store(folder, manifest)
        store.set_meta("name", name)
        store.set_meta("created", now)
        project = Project(folder, manifest, store)
        store.log_activity("system", "project.create", None,
                           {"name": name, "template": template, "demo": bool(demo)})
        from . import seed
        if template == "demo" or demo:
            seed.seed_demo(store, folder)
        elif template == "paper":
            seed.seed_paper(store)
        else:
            seed.seed_blank(store)
        self._active_id = project.id
        return project

    def active(self) -> Project | None:
        if self._active_id:
            try:
                return self.open_by_id(self._active_id)
            except ProjectError:
                return None
        projects = self.list_projects()
        if projects:
            return self.open(projects[0]["path"])
        return None

    def delete(self, project_id: str) -> None:
        """Only for tests/CLI; the UI exposes archive, not folder deletion."""
        for summary in self.list_projects():
            if summary["id"] == project_id:
                path = Path(summary["path"])
                if path.parent.resolve() != self.root:
                    raise ProjectError("refusing to delete outside the project root")
                shutil.rmtree(path)
                return
        raise ProjectError(f"project not found: {project_id}")
