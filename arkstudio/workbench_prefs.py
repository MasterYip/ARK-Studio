"""Personal workbench layout presets stored on the account profile.

``PATCH /api/me`` with ``{"prefs": {"workbench": {...} | null}}`` replaces the
user's saved layouts (``null`` clears them).  Shape::

    {"presets": [{"id": "u:<slug>", "name": str, "layout": {...}}],
     "default": "" | "<builtin id>" | "<user preset id>"}

``layout`` is the client's workbench arrangement (``ark.workbench.v1``):
``{"v": 1, "containers": {"left"|"right"|"bottom": {"views": [...],
"active": str, "visible": bool, "size": int, "maximized"?: bool,
"sections"?: [{"views": [...], "active": str, "size": int,
"collapsed"?: bool, "maximized"?: bool}]}}}``.

``sections`` (optional; stacked view sections, top to bottom in the
sidebars, left to right in the bottom panel) must list exactly the
container's ``views`` in order, each section non-empty.  Layouts without it
mean one section holding every view.  The server checks the shape and
bounds only; the client keeps unknown view ids out (normalizeWorkbench).
"""
from __future__ import annotations

import re

BUILTIN_PRESETS = ("default", "focus", "research", "writing", "review")
CONTAINERS = ("left", "right", "bottom")
MAX_PRESETS = 20
MAX_NAME = 60
MAX_VIEWS = 24
VIEW_ID_RE = re.compile(r"^[a-z][a-z0-9-]{0,31}$")
PRESET_ID_RE = re.compile(r"^u:[a-z0-9-]{1,40}$")
SIZE_RANGE = (80, 2000)
SECTION_SIZE_RANGE = (40, 4000)


class WorkbenchPrefsError(ValueError):
    pass


def _fail() -> None:
    raise WorkbenchPrefsError("invalid workbench layout preferences")


def _layout(value) -> dict:
    if not isinstance(value, dict) or value.get("v") != 1 or not isinstance(value.get("containers"), dict):
        _fail()
    containers = value["containers"]
    if set(containers) - set(CONTAINERS):
        _fail()
    out = {}
    seen: set[str] = set()
    for name in CONTAINERS:
        box = containers.get(name)
        if box is None:
            continue
        if not isinstance(box, dict) or set(box) - {"views", "active", "visible", "size", "maximized", "sections"}:
            _fail()
        views = box.get("views", [])
        if not isinstance(views, list) or len(views) > MAX_VIEWS:
            _fail()
        clean = []
        for view in views:
            if not isinstance(view, str) or not VIEW_ID_RE.match(view) or view in seen:
                _fail()
            seen.add(view)
            clean.append(view)
        active = box.get("active", "")
        visible = box.get("visible", True)
        size = box.get("size", 300)
        maximized = box.get("maximized", False)
        if (not isinstance(active, str) or (active and active not in clean)
                or not isinstance(visible, bool) or not isinstance(maximized, bool)
                or isinstance(size, bool) or not isinstance(size, (int, float))
                or not SIZE_RANGE[0] <= size <= SIZE_RANGE[1]):
            _fail()
        out[name] = {"views": clean, "active": active, "visible": visible, "size": int(size)}
        if maximized:
            out[name]["maximized"] = True
        if "sections" in box:
            out[name]["sections"] = _sections(box["sections"], clean)
    return {"v": 1, "containers": out}


def _number_in(value, bounds) -> bool:
    return (not isinstance(value, bool) and isinstance(value, (int, float))
            and bounds[0] <= value <= bounds[1])


def _sections(value, views: list[str]) -> list[dict]:
    if not isinstance(value, list) or len(value) > MAX_VIEWS:
        _fail()
    out = []
    for section in value:
        if not isinstance(section, dict) or set(section) - {"views", "active", "size", "collapsed", "maximized"}:
            _fail()
        ids = section.get("views")
        active = section.get("active", "")
        size = section.get("size", 240)
        collapsed = section.get("collapsed", False)
        maximized = section.get("maximized", False)
        if (not isinstance(ids, list) or not ids or not all(isinstance(item, str) for item in ids)
                or not isinstance(active, str) or (active and active not in ids)
                or not _number_in(size, SECTION_SIZE_RANGE)
                or not isinstance(collapsed, bool) or not isinstance(maximized, bool)):
            _fail()
        item = {"views": list(ids), "active": active, "size": int(size)}
        if collapsed:
            item["collapsed"] = True
        if maximized:
            item["maximized"] = True
        out.append(item)
    if [view for section in out for view in section["views"]] != views:
        _fail()
    return out


def workbench_prefs(value, *, strict: bool = False) -> dict:
    """Validated copy; with ``strict=False`` (reading stored data) anything
    invalid yields an empty value instead of an error."""
    try:
        if not isinstance(value, dict) or set(value) - {"presets", "default"}:
            _fail()
        presets = value.get("presets", [])
        if not isinstance(presets, list) or len(presets) > MAX_PRESETS:
            _fail()
        out_presets = []
        ids: set[str] = set()
        for preset in presets:
            if not isinstance(preset, dict) or set(preset) != {"id", "name", "layout"}:
                _fail()
            pid, name = preset["id"], preset["name"]
            if (not isinstance(pid, str) or not PRESET_ID_RE.match(pid) or pid in ids
                    or not isinstance(name, str) or not name.strip() or len(name) > MAX_NAME):
                _fail()
            ids.add(pid)
            out_presets.append({"id": pid, "name": name.strip(), "layout": _layout(preset["layout"])})
        default = value.get("default", "")
        if not isinstance(default, str) or (default and default not in BUILTIN_PRESETS and default not in ids):
            _fail()
        return {"presets": out_presets, "default": default}
    except WorkbenchPrefsError:
        if strict:
            raise
        return {}
