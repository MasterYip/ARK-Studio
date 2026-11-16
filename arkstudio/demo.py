"""Built-in, disposable ARK Studio tutorial project.

The demo is a real ARK project folder (same project.json + SQLite schema) so
every feature works, but it lives under ``<root>/.demo/<user-hash>``.  Opening
it copies a fresh template; switching away and back replaces the copy, so edits
are deliberately not persisted.  Every signed-in account may open it without a
project grant.

The template itself is under ``<root>/.demo-template``.  Both directories start
with a dot, so ProjectManager.list_projects() ignores them.

The tutorial text lives in :mod:`arkstudio.demo_content`; its figures and
sample files live in ``demo-assets/tutorial`` (built by
``scripts/dev/build_demo_assets.py``) and are copied to ``assets/tutorial`` inside
the project.  ``project.json`` carries ``demo_version``: when it differs from
:data:`DEMO_VERSION`, the cached template is rebuilt, and a per-user copy is
replaced the next time it is opened.
"""
from __future__ import annotations

import hashlib
import json
import os
import re
import shutil
import tempfile
import threading
from datetime import datetime, timezone
from pathlib import Path

from . import demo_content as content
from .demo_content import DEMO_VERSION
from .store import Store

DEMO_ID = "ark-demo"
DEMO_NAME = "ARK Studio Demo"
DEMO_DESCRIPTION = (
    "A disposable, hands-on tutorial: one illustrated chapter per workspace "
    "(Home, Research, Map, Execute, Output, Editor) plus agents, skills, "
    "settings and mobile, all built around one small example study. "
    "Edits are local to this visit and reset when you switch projects."
)
ASSET_SOURCE = Path(__file__).resolve().parent.parent / "demo-assets" / "tutorial"
ASSET_PREFIX = "assets/tutorial"
_PLACEHOLDER = re.compile(r"\{\{(uid|raw|html):([^}]+)\}\}")
_DEMO_LOCK = threading.Lock()


def _utcnow() -> str:
    return datetime.now(timezone.utc).isoformat(timespec="seconds")


def _manifest() -> dict:
    now = _utcnow()
    return {
        "format": "ark-studio-project",
        "version": 1,
        "id": DEMO_ID,
        "name": DEMO_NAME,
        "description": DEMO_DESCRIPTION,
        "created": now,
        "updated": now,
        "accent": "#2563eb",
        "demo": True,
        "demo_version": DEMO_VERSION,
    }


def template_path(root: str | Path) -> Path:
    return Path(root).resolve() / ".demo-template"


def user_path(root: str | Path, username: str) -> Path:
    key = hashlib.sha256(str(username or "local").encode("utf-8")).hexdigest()[:16]
    return Path(root).resolve() / ".demo" / key


def summary(root: str | Path, username: str) -> dict:
    path = user_path(root, username)
    return {
        "id": DEMO_ID,
        "name": DEMO_NAME,
        "description": DEMO_DESCRIPTION,
        "path": str(path),
        "folder": path.name,
        "created": None,
        "updated": None,
        "demo": True,
        "temporary": True,
        "permission": "write",
    }


# ------------------------------------------------------------------ seeding
def raw_url(path: str) -> str:
    """The Markdown link target for a tutorial asset: a project-relative path.

    ARK's renderer resolves relative paths to the project file (through
    ``/api/workspace/raw``) and embeds images, video and audio by extension.
    """
    return f"{ASSET_PREFIX}/{path}"


def _copy_assets(project: Path) -> set[str]:
    """Copy ``demo-assets/tutorial`` into ``<project>/assets/tutorial``."""
    copied: set[str] = set()
    if not ASSET_SOURCE.is_dir():
        return copied
    target_root = project / ASSET_PREFIX
    for source in sorted(ASSET_SOURCE.rglob("*")):
        if not source.is_file():
            continue
        rel = source.relative_to(ASSET_SOURCE).as_posix()
        target = target_root / rel
        target.parent.mkdir(parents=True, exist_ok=True)
        shutil.copyfile(source, target)
        copied.add(rel)
    return copied


def _goal_manifest() -> str:
    """The example task body: a goal manifest from the agent-team templates."""
    path = ASSET_SOURCE / "execute" / "goal_manifest.md"
    if path.is_file():
        return path.read_text(encoding="utf-8")
    from .skills import agent_team
    result = agent_team.prepare_task(agent_team.example_task(
        {"id": DEMO_ID, "name": DEMO_NAME, "next_task_id": "T-0001"}))
    return result["markdown"]


def _figure_check(project: Path) -> str:
    """Run the design skill's artifact check on Figure 1 and describe it."""
    from .skills import design
    rel = f"{ASSET_PREFIX}/output/fig-main-result.svg"
    report = design.verify_artifact(rel, project_root=project)
    rows = []
    for check in report["checks"]:
        detail = str(check.get("detail") or "")
        detail = detail.replace(str(project), "<project>").replace("\\", "/").replace("|", "/")
        rows.append(f"| `{check['id']}` | {'pass' if check['ok'] else 'FAIL'} | {detail[:70]} |")
    verdict = "passes" if report["ok"] else "fails"
    return "\n".join([
        "The main result as a publication figure, in the academic-clean theme.",
        "",
        f"![Figure 1: mean training loss with and without warm-up]({raw_url('output/fig-main-result.svg')})",
        "",
        "## Design check",
        "",
        "The design skill's `verify_artifact` ran on this file when the tutorial",
        f"was built, and it **{verdict}**:",
        "",
        "| Check | Result | Detail |",
        "|---|---|---|",
        *rows,
        "",
        "Run the same check yourself: `POST /api/skills/design/verify` with",
        f'`{{"path": "{rel}"}}`.',
        "",
        "Derived from {{uid:evidence}}; used by the poster and the X thread.",
        "",
    ])


_BLOCK_START = re.compile(r"^\s*(#{1,6}\s|[-*•]\s|\d+[.)]\s|\||>|!\[|\$\$|```)")


def unwrap(markdown: str) -> str:
    """Join hard-wrapped prose lines so ARK's renderer shows real paragraphs.

    The content module is wrapped at ~80 columns for review, but ARK's
    Markdown renderer keeps every newline.  Lines that continue a paragraph
    or a list item are joined with a space; fences, tables, headings, quotes,
    images and display maths are left alone.
    """
    out: list[str] = []
    in_fence = in_math = False
    joinable = False  # may the next line be appended to out[-1]?
    for line in markdown.split("\n"):
        stripped = line.strip()
        if in_fence:
            out.append(line)
            in_fence = not stripped.startswith("```")
            continue
        if in_math:
            out.append(line)
            in_math = not stripped.endswith("$$")
            continue
        if stripped.startswith("```"):
            out.append(line)
            in_fence, joinable = True, False
            continue
        if stripped.startswith("$$") and not (len(stripped) > 2 and stripped.endswith("$$")):
            out.append(line)
            in_math, joinable = True, False
            continue
        if not stripped:
            out.append(line)
            joinable = False
            continue
        if joinable and not _BLOCK_START.match(line):
            out[-1] = out[-1].rstrip() + " " + stripped
            continue
        if (stripped.startswith(">") and out and out[-1].lstrip().startswith(">")
                and stripped[1:].strip()):
            out[-1] = out[-1].rstrip() + " " + stripped[1:].strip()
            continue
        out.append(line)
        joinable = not re.match(r"^\s*(#{1,6}\s|\||>|!\[|\$\$)", line)
    return "\n".join(out)


def _seed(store: Store, project: Path | None = None) -> None:
    """Seed the tutorial from :mod:`demo_content`: chapter hubs plus examples."""
    project = Path(project) if project is not None else Path(store.path)
    copied = _copy_assets(project)
    keys: dict[str, str] = {}
    bodies: dict[str, str] = {}
    actor = "demo:ark"

    def create(key: str, data: dict, media: list) -> None:
        data = {**data, "body": unwrap(data["body"])}
        unit = store.create_unit(data, actor=actor)
        keys[key] = unit["uid"]
        bodies[key] = data["body"]
        for path, kind, caption in media:
            if path in copied:
                store.add_media(unit["uid"], f"{ASSET_PREFIX}/{path}", kind, caption,
                                actor=actor)

    chapters = sorted(content.CHAPTERS, key=lambda item: item["seq"])
    for item in chapters:
        create(item["key"], {
            "title": item["title"], "type": "story", "body": item["body"],
            "status": item["status"], "importance": item["importance"],
            "tags": ["tutorial:chapter", f"seq:{item['seq']:02d}"],
        }, item["media"])

    special = {"{{manifest}}": _goal_manifest,
               "{{figure_check}}": lambda: _figure_check(project)}
    for item in content.EXAMPLES:
        body = item["body"]
        if body in special:
            body = special[body]()
        data = {"title": item["title"], "type": item["type"], "body": body,
                "status": item["status"], "importance": item["importance"],
                "tags": ["tutorial:example", f"chapter:{item['chapter']}", *item["tags"]]}
        for field in ("start_at", "end_at"):
            if item.get(field):
                data[field] = item[field]
        create(item["key"], data, item["media"])

    # Chapters hang off Start here (the Story outline orders them by seq:);
    # each example hangs off its chapter hub.
    for item in chapters:
        if item["key"] != "start":
            store.create_link(keys[item["key"]], keys["start"], "part_of",
                              note="Tutorial chapter", actor=actor)
    for item in content.EXAMPLES:
        store.create_link(keys[item["key"]], keys[item["chapter"]], "part_of",
                          note="Example for this chapter", actor=actor)
    for a, b, relation, note in content.LINKS:
        store.create_link(keys[a], keys[b], relation, note=note, actor=actor)
    for key, topic in content.TOPICS.items():
        store.design_topic(keys[key], {
            **topic.get("design", {}),
            "add": [{"unit": keys[member], "caption": caption} for member, caption in topic["members"]],
        }, actor=actor)

    def resolve(match: re.Match) -> str:
        kind, value = match.group(1), match.group(2).strip()
        if kind == "uid":
            return keys[value]
        if kind == "raw":
            return raw_url(value)
        path = ASSET_SOURCE / value
        return path.read_text(encoding="utf-8").rstrip("\n") if path.is_file() else ""

    for key, body in bodies.items():
        resolved = _PLACEHOLDER.sub(resolve, body)
        if resolved != body:
            store.update_unit(keys[key], {"body": resolved}, actor=actor)


# ------------------------------------------------------------- versioning
def folder_version(path: str | Path) -> int | None:
    """The ``demo_version`` stamped in a demo folder's project.json, if any."""
    try:
        data = json.loads((Path(path) / "project.json").read_text(encoding="utf-8"))
    except (OSError, ValueError):
        return None
    value = data.get("demo_version")
    return value if isinstance(value, int) and not isinstance(value, bool) else None


def is_current(path: str | Path) -> bool:
    """True when the folder was built from the current tutorial version."""
    return folder_version(path) == DEMO_VERSION


def _stamp_version(path: Path) -> None:
    manifest_path = path / "project.json"
    data = json.loads(manifest_path.read_text(encoding="utf-8"))
    if data.get("demo_version") != DEMO_VERSION:
        data["demo_version"] = DEMO_VERSION
        manifest_path.write_text(json.dumps(data, ensure_ascii=False, indent=2) + "\n",
                                 encoding="utf-8")


def _build_template(root: Path) -> Path:
    template = template_path(root)
    if is_current(template):
        return template
    root.mkdir(parents=True, exist_ok=True)
    temp = Path(tempfile.mkdtemp(prefix=".demo-build-", dir=str(root)))
    try:
        for sub in ("assets", "exports", "logs"):
            (temp / sub).mkdir(exist_ok=True)
        manifest = _manifest()
        (temp / "project.json").write_text(
            json.dumps(manifest, ensure_ascii=False, indent=2) + "\n",
            encoding="utf-8")
        store = Store(temp, manifest)
        try:
            store.set_meta("name", DEMO_NAME)
            store.set_meta("created", manifest["created"])
            _seed(store, temp)
        finally:
            store.close()
        _stamp_version(temp)  # the Store may have rewritten project.json
        if template.exists():
            shutil.rmtree(template, ignore_errors=True)
        os.replace(temp, template)
    finally:
        if temp.exists():
            shutil.rmtree(temp, ignore_errors=True)
    return template


def ensure_template(root: str | Path) -> Path:
    with _DEMO_LOCK:
        return _build_template(Path(root).resolve())


def ensure_demo(root: str | Path, username: str, *, reset: bool = False) -> Path:
    """Return the user's demo folder, copying the template when needed.

    The copy is replaced on ``reset`` and whenever it was made from an older
    tutorial version, so an upgraded server shows the new tutorial on the
    next open without a manual reset.
    """
    template = ensure_template(root)
    target = user_path(root, username)
    with _DEMO_LOCK:
        if (reset or not is_current(target)) and target.exists():
            shutil.rmtree(target, ignore_errors=True)
        if not (target / "project.json").is_file():
            if target.exists():
                shutil.rmtree(target, ignore_errors=True)
            target.parent.mkdir(parents=True, exist_ok=True)
            shutil.copytree(template, target)
    return target
