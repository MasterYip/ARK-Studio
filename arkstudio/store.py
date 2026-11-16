"""Project store: units, tags, links, media, activity and projections."""
from __future__ import annotations

import hashlib
import json
import logging
import re
import sqlite3
import threading
import time
from collections import deque
from datetime import date, datetime, timedelta, timezone
from difflib import SequenceMatcher
from pathlib import Path
from typing import Any, Iterable

from . import db
from .node_history import HistoryError, NodeHistory
from .tags import (
    KNOWN_IMPORTANCE,
    KNOWN_STATUSES,
    KNOWN_TYPES,
    NEEDS_NS,
    RELATIONS,
    TagError,
    needs_transition,
    normalize_tags,
    tag_namespace,
    tag_value,
    type_prefix,
)

DATE_RE = re.compile(r"^\d{4}-\d{2}-\d{2}$")
MAX_TITLE = 240
MAX_BODY = 200_000
MAX_URL = 2048
MAX_CAPTION = 500
# Node-graph card width limits (topic member layout), canvas px; the web
# client clamps to the same range (web/js/node-card.js).
MIN_CARD_W = 140
MAX_CARD_W = 640
MAX_TOPIC_MEMBERS = 400
RESERVED = {"type", "status", "importance"}


def _without_needs(tags) -> list:
    """Drop client-supplied ``needs:`` tags; ARK sets that namespace itself."""
    out = []
    for tag in tags or []:
        text = str(tag or "").strip()
        if ":" in text and text.split(":", 1)[0].strip().lower() == NEEDS_NS:
            continue
        out.append(tag)
    return out


def _needs_of(tags) -> str | None:
    for tag in tags or []:
        if tag_namespace(tag) == NEEDS_NS:
            return tag_value(tag)
    return None
ASSIGNEE_MAX = 64
ASSIGNEE_RE = re.compile(r"^[A-Za-z0-9_.@+-]{1,64}$")
# Filter keywords for ``assignee=``: they never name an account.
ASSIGNEE_NONE = {"none", "unassigned"}
ASSIGNEE_ME = "me"
# Unit comments (db v4).  User kinds come from people (Home actions, the
# Comments section); ``agent_reply`` is the only kind an agent writes.
COMMENT_USER_KINDS = ("comment", "sent_back", "decided", "accepted")
COMMENT_KINDS = COMMENT_USER_KINDS + ("agent_reply",)
MAX_COMMENT = 8000



_MD_FENCE = re.compile(r"^\s*(```|~~~).*$")
_MD_IMAGE = re.compile(r"!\[([^\]]*)\]\([^)]*\)")
_MD_LINK = re.compile(r"\[([^\]]+)\]\([^)]*\)")
_MD_PREFIX = re.compile(r"^\s{0,3}(#{1,6}\s+|>\s?|[-*+]\s+|\d+[.)]\s+|\|)")
_MD_INLINE = re.compile(r"(\*\*|__|`|~~)")


# Legacy body note lines (before db v4 moved them into unit_comments).  An
# older agent may still write one, so they stay out of the excerpt.
_NOTE_LINE = re.compile(r"^\*\*(?:Sent back by user|Decided by user|User comment|Agent reply) \(\d{4}-\d{2}-\d{2}\):\*\* .*$", re.M)


def excerpt(body: str, limit: int = 240) -> str:
    """Plain-text preview of a Markdown body for cards and lists.

    Drops fenced blocks (code, mermaid, html), image syntax, heading/list/quote
    markers and emphasis, keeps link text, and joins lines with spaces.
    """
    lines, fenced, size = [], False, 0
    for line in str(body or "").replace(chr(13) + chr(10), chr(10)).split(chr(10)):
        if _MD_FENCE.match(line):
            fenced = not fenced
            continue
        if fenced or line.strip() in {"$$", "---", "***"}:
            continue
        line = _MD_IMAGE.sub(lambda m: m.group(1), line)
        line = _MD_LINK.sub(lambda m: m.group(1), line)
        line = _MD_PREFIX.sub("", line)
        line = _MD_INLINE.sub("", line).replace("|", " ")
        if line.strip():
            plain = re.sub(r"\s+", " ", line).strip()
            size += len(plain) + (1 if lines else 0)
            lines.append(plain)
            # Only the prefix is returned. Later Markdown cannot affect it;
            # avoid repeatedly parsing whole documents for graph/list excerpts.
            if limit >= 0 and size > limit:
                return " ".join(lines)[:limit].rstrip() + "…"
    text = " ".join(lines)
    return (text[:limit].rstrip() + "…") if len(text) > limit else text


_MD_IMAGE_ONLY = re.compile(r"^\s*!\[[^\]]*\]\([^)]*\)\s*$")


def markdown_excerpt(body: str, limit: int = 400) -> tuple[str, bool]:
    """Leading Markdown of a body, kept as Markdown, for the "Needs you" cards.

    Takes whole blocks (paragraphs, headings, lists) until about ``limit``
    characters, skipping fenced blocks and image-only lines (media is shown in
    its own strip).  Returns ``(text, truncated)``.
    """
    blocks, current, fenced = [], [], False
    for line in str(body or "").replace(chr(13) + chr(10), chr(10)).split(chr(10)):
        if _MD_FENCE.match(line):
            fenced = not fenced
            continue
        if fenced or _MD_IMAGE_ONLY.match(line):
            continue
        if line.strip():
            current.append(line.rstrip())
        elif current:
            blocks.append(chr(10).join(current))
            current = []
    if current:
        blocks.append(chr(10).join(current))
    out: list[str] = []
    size = 0
    for block in blocks:
        if out and size + len(block) > limit:
            return (chr(10) + chr(10)).join(out), True
        if size + len(block) > limit:
            cut = block[:limit]
            nl = cut.rfind(chr(10))
            cut = cut[:nl] if nl > limit // 2 else cut.rsplit(" ", 1)[0]
            return cut.rstrip() + " …", True
        out.append(block)
        size += len(block) + 2
    return (chr(10) + chr(10)).join(out), False


_IMAGE_EXT = (".png", ".jpg", ".jpeg", ".webp", ".gif", ".svg", ".avif", ".bmp")
_VIDEO_EXT = (".mp4", ".webm", ".mov", ".m4v", ".ogv")
# Links whose far end is good evidence to preview for a unit awaiting the user.
_EVIDENCE_IN = ("supports", "validates", "part_of")
_EVIDENCE_OUT = ("produces",)
_NO_PREVIEW_RELATIONS = ("assigned_to", "runs_on")
# Previews per Needs-you item.  The row shows a few thumbnails; the preview
# dialog pages through all of them, newest first.
NEEDS_MEDIA_LIMIT = 40


def _unique_previews(items: list[dict]) -> list[dict]:
    """Drop repeated files (the same video attached twice), keeping the first."""
    seen: set[str] = set()
    out = []
    for item in items:
        key = str(item.get("url") or item["id"])
        if key in seen:
            continue
        seen.add(key)
        out.append(item)
    return out


def media_preview_kind(media: dict) -> str:
    """'image' | 'video' | '' for a serialized media row."""
    kind = str(media.get("kind") or "").lower()
    path = str(media.get("url") or "").lower().split("?", 1)[0].split("#", 1)[0]
    if media.get("is_image") or kind == "image" or path.endswith(_IMAGE_EXT):
        return "image"
    if kind == "video" or path.endswith(_VIDEO_EXT):
        return "video"
    return ""

class StoreError(ValueError):
    pass


def _clean_date(value: Any, field: str) -> str | None:
    if value in (None, ""):
        return None
    text = str(value).strip()
    if not DATE_RE.match(text):
        raise StoreError(f"{field} must be YYYY-MM-DD")
    try:
        date.fromisoformat(text)
    except ValueError as exc:
        raise StoreError(f"{field} is not a real date: {text}") from exc
    return text


def _clean_text(value: Any, field: str, *, max_len: int, required: bool = False) -> str:
    text = "" if value is None else str(value)
    text = text.replace("\x00", "").strip() if required else text.replace("\x00", "")
    if required and not text:
        raise StoreError(f"{field} is required")
    if len(text) > max_len:
        raise StoreError(f"{field} is longer than {max_len} characters")
    return text


def _parse_detail(raw: str | None) -> dict:
    try:
        value = json.loads(raw or "{}")
        return value if isinstance(value, dict) else {}
    except ValueError:
        return {}


def _unit_brief(row: sqlite3.Row) -> dict:
    return {
        "id": row["id"],
        "uid": row["uid"],
        "type": row["type"],
        "title": row["title"],
        "status": row["status"],
        "importance": row["importance"],
        "start_at": row["start_at"],
        "end_at": row["end_at"],
    }


class Store:
    def __init__(self, path: str | Path, manifest: dict | None = None):
        self.path = Path(path).resolve()
        self.manifest = manifest or {}
        self.lock = threading.RLock()
        self.con = db.connect(self.path / "ark.sqlite3")
        self.text_history = NodeHistory(self.path / "history.git")
        self.log_path = self.path / "logs" / "activity.jsonl"
        self.log_path.parent.mkdir(parents=True, exist_ok=True)
        # ``assignee`` names an ARK account, which lives in the org database
        # outside this project.  The server installs a resolver that maps a
        # name to its canonical username or raises StoreError; without one
        # (CLI, seeding, tests) only the username format is checked.
        self.assignee_resolver = None
        # Returns the account making the current write (the signed-in user, or
        # the local owner without auth), or "" when there is none.  New units
        # created without an ``assignee`` key are assigned to it.
        self.creator_resolver = None
        self._creator_backfilled = False
        self._assignee_cache: tuple | None = None
        self._writes_since_prune = 0
        self._history_stop = threading.Event()
        self._history_worker = None
        if self.con.execute("SELECT 1 FROM node_text_pending LIMIT 1").fetchone():
            self._start_history_worker()

    # ------------------------------------------------------------------ basics
    def close(self) -> None:
        self._history_stop.set()
        if self._history_worker is not None:
            self._history_worker.join()
        with self.lock:
            try:
                self.con.close()
            except sqlite3.Error:
                pass

    def meta(self, key: str, default: str | None = None) -> str | None:
        with self.lock:
            return db.meta_get(self.con, key, default)

    def set_meta(self, key: str, value: str) -> None:
        with self.lock, self.con:
            db.meta_set(self.con, key, value)

    def touch_manifest(self) -> None:
        if not self.manifest:
            return
        self.manifest["updated"] = db.utc_now()
        try:
            db.write_manifest(self.path, self.manifest)
        except OSError:
            pass

    def _next_uid(self, type_name: str) -> str:
        prefix = type_prefix(type_name)
        row = self.con.execute("SELECT n FROM counters WHERE prefix=?", (prefix,)).fetchone()
        n = (row["n"] if row else 0) + 1
        while True:
            uid = f"{prefix}-{n:04d}"
            exists = self.con.execute("SELECT 1 FROM units WHERE uid=?", (uid,)).fetchone()
            if not exists:
                break
            n += 1
        self.con.execute(
            "INSERT INTO counters(prefix,n) VALUES(?,?) ON CONFLICT(prefix) DO UPDATE SET n=excluded.n",
            (prefix, n),
        )
        return uid

    def _unit_row(self, ref: int | str) -> sqlite3.Row:
        if isinstance(ref, int) or (isinstance(ref, str) and ref.isdigit()):
            row = self.con.execute("SELECT * FROM units WHERE id=?", (int(ref),)).fetchone()
        else:
            row = self.con.execute("SELECT * FROM units WHERE uid=?", (str(ref),)).fetchone()
        if not row:
            raise StoreError(f"unit not found: {ref}")
        return row

    # ------------------------------------------------------------- serialization
    def _tags_for(self, ids: list[int]) -> dict[int, list[str]]:
        out: dict[int, list[str]] = {i: [] for i in ids}
        if not ids:
            return out
        marks = ",".join("?" * len(ids))
        rows = self.con.execute(
            f"SELECT unit_id, tag FROM unit_tags WHERE unit_id IN ({marks}) ORDER BY tag", ids
        ).fetchall()
        for row in rows:
            out[row["unit_id"]].append(row["tag"])
        return out

    def _link_counts(self, ids: list[int]) -> tuple[dict[int, int], dict[int, int]]:
        out_n = {i: 0 for i in ids}
        in_n = {i: 0 for i in ids}
        if not ids:
            return out_n, in_n
        marks = ",".join("?" * len(ids))
        for row in self.con.execute(
            f"SELECT source_id, COUNT(*) n FROM unit_links WHERE source_id IN ({marks}) GROUP BY source_id", ids
        ):
            out_n[row["source_id"]] = row["n"]
        for row in self.con.execute(
            f"SELECT target_id, COUNT(*) n FROM unit_links WHERE target_id IN ({marks}) GROUP BY target_id", ids
        ):
            in_n[row["target_id"]] = row["n"]
        return out_n, in_n

    def _assignees(self, ids: list[int]) -> dict[int, list[dict]]:
        out: dict[int, list[dict]] = {i: [] for i in ids}
        if not ids:
            return out
        marks = ",".join("?" * len(ids))
        rows = self.con.execute(
            f"""SELECT l.source_id, u.id, u.uid, u.title, u.status, u.type
                FROM unit_links l JOIN units u ON u.id=l.target_id
                WHERE l.relation='assigned_to' AND l.source_id IN ({marks})""",
            ids,
        ).fetchall()
        for row in rows:
            out[row["source_id"]].append(
                {
                    "id": row["id"],
                    "uid": row["uid"],
                    "title": row["title"],
                    "status": row["status"],
                    "type": row["type"],
                }
            )
        return out

    # -------------------------------------------------------------- assignee
    def _clean_assignee(self, value: Any) -> str | None:
        if value is None:
            return None
        if not isinstance(value, str):
            raise StoreError("assignee must be a username string or null")
        text = value.strip()
        if not text:
            return None
        resolver = self.assignee_resolver
        if resolver is not None:
            return resolver(text)
        if len(text) > ASSIGNEE_MAX or not ASSIGNEE_RE.fullmatch(text):
            raise StoreError(f"assignee is not a valid username: {text!r}")
        return text

    def _creator_assignee(self, actor: str) -> str | None:
        """Default ``assignee`` for a new unit: the creating account, if any.

        System writes (seed, demo, migrations) never get one.  A creator the
        resolver refuses (e.g. not a project member) leaves the unit
        unassigned instead of failing the create.
        """
        if str(actor or "").startswith("system"):
            return None
        resolver = self.creator_resolver
        name = str((resolver() if resolver else "") or "").strip()
        if not name:
            return None
        try:
            return self._clean_assignee(name)
        except StoreError:
            return None

    def backfill_creator_assignees(self, is_account) -> int:
        """Run the v5 creator-assignee backfill once for this project."""
        if self._creator_backfilled:
            return 0
        with self.lock:
            if db.meta_get(self.con, db.CREATOR_ASSIGNEE_MIGRATED) == "1":
                self._creator_backfilled = True
                return 0
            with self.con:
                changed = db.backfill_creator_assignees(self.con, is_account)
                db.meta_set(self.con, db.CREATOR_ASSIGNEE_MIGRATED, "1")
            self._creator_backfilled = True
        if changed:
            self._after_write()
        return changed

    def _assignee_index(self) -> dict[int, tuple[str | None, int | None]]:
        """Effective assignee for every unit: ``{id: (username, from_id)}``.

        ``from_id`` is the ancestor the value was inherited from (``None`` when
        the unit is assigned itself or nobody is).  Inheritance follows
        ``child --part_of--> parent`` links breadth-first, so the *nearest*
        assigned ancestor wins; ties at the same depth go to the older
        part_of link.  A visited set makes cycles terminate.

        Two queries rebuild the index; it is cached until this connection or
        any other one commits (``total_changes`` / ``PRAGMA data_version``),
        so list, graph and unit reads never query per node.
        """
        key = (self.con.total_changes,
               self.con.execute("PRAGMA data_version").fetchone()[0])
        cached = self._assignee_cache
        if cached is not None and cached[0] == key:
            return cached[1]
        units = self.con.execute("SELECT id, assignee FROM units").fetchall()
        own = {row[0]: row[1] for row in units if row[1]}
        index: dict[int, tuple[str | None, int | None]] = {}
        if not own:
            index = {row[0]: (None, None) for row in units}
        else:
            parents: dict[int, list[int]] = {}
            children: dict[int, list[int]] = {}
            for row in self.con.execute(
                "SELECT source_id, target_id FROM unit_links WHERE relation='part_of' ORDER BY id"
            ):
                parents.setdefault(row[0], []).append(row[1])
                children.setdefault(row[1], []).append(row[0])
            # One reverse BFS finds the nearest assigned ancestor distance for
            # every node, including cycles. Resolve in distance order, taking
            # the first shortest-path parent to retain the old link-order ties.
            distance = {uid: 0 for uid in own}
            queue = deque(own)
            ordered = []
            while queue:
                uid = queue.popleft()
                ordered.append(uid)
                for child in children.get(uid, ()):
                    if child not in distance:
                        distance[child] = distance[uid] + 1
                        queue.append(child)
            index = {row[0]: (None, None) for row in units}
            for uid in ordered:
                if uid in own:
                    index[uid] = (own[uid], None)
                    continue
                parent = next(parent for parent in parents[uid]
                              if distance.get(parent) == distance[uid] - 1)
                name, source = index[parent]
                index[uid] = (name, source if source is not None else parent)
        if not self.con.in_transaction:
            self._assignee_cache = (key, index)
        return index

    def assignees_in_use(self) -> list[str]:
        with self.lock:
            return [row[0] for row in self.con.execute(
                "SELECT DISTINCT assignee FROM units WHERE assignee IS NOT NULL "
                "ORDER BY assignee COLLATE NOCASE")]

    def _assignee_filter_ids(self, f: dict) -> list[int] | None:
        """Unit ids matching ``assignees`` (effective value), or None for no filter."""
        wanted = [str(a).strip() for a in (f.get("assignees") or []) if str(a).strip()]
        if not wanted:
            return None
        want_none = False
        names: set[str] = set()
        for value in wanted:
            low = value.lower()
            if low in ASSIGNEE_NONE:
                want_none = True
            elif low == ASSIGNEE_ME:
                me = str(f.get("me") or "").strip().lower()
                if me:
                    names.add(me)
            else:
                names.add(low)
        index = self._assignee_index()
        return [uid for uid, (name, _src) in index.items()
                if (name is None and want_none) or (name is not None and name.lower() in names)]

    def _parents(self, ids: list[int]) -> dict[int, list[dict]]:
        out: dict[int, list[dict]] = {i: [] for i in ids}
        if not ids:
            return out
        marks = ",".join("?" * len(ids))
        # For `child --part_of--> parent`, the target is the parent.
        rows = self.con.execute(
            f"""SELECT l.source_id, u.id, u.uid, u.title, u.type, l.relation
                FROM unit_links l JOIN units u ON u.id=l.target_id
                WHERE l.relation='part_of' AND l.source_id IN ({marks})""",
            ids,
        ).fetchall()
        for row in rows:
            out[row["source_id"]].append(
                {"id": row["id"], "uid": row["uid"], "title": row["title"], "type": row["type"]}
            )
        return out

    def _media_preview(self, ids: list[int]) -> dict[int, list[dict]]:
        out: dict[int, list[dict]] = {i: [] for i in ids}
        if not ids:
            return out
        marks = ",".join("?" * len(ids))
        rows = self.con.execute(
            f"SELECT * FROM unit_media WHERE unit_id IN ({marks}) ORDER BY id", ids
        ).fetchall()
        for row in rows:
            out[row["unit_id"]].append(self._media_dict(row))
        return out

    def _media_dict(self, row: sqlite3.Row) -> dict:
        url = row["url"]
        lower = url.lower().split("?", 1)[0]
        image = lower.endswith((".png", ".jpg", ".jpeg", ".webp", ".gif", ".svg", ".avif"))
        return {
            "id": row["id"],
            "url": url,
            "kind": row["kind"],
            "caption": row["caption"],
            "created_at": row["created_at"],
            "is_image": image,
            "raw_url": f"/api/media/{row['id']}/raw",
        }

    def _serialize(self, row: sqlite3.Row, *, tags=None, out_n=0, in_n=0,
                   parents=None, media=None, assignees=None, include_body=True,
                   effective=None, comments=None) -> dict:
        own = row["assignee"] if "assignee" in row.keys() else None
        eff_name, eff_from = effective or (own, None)
        data = {
            "id": row["id"],
            "uid": row["uid"],
            "type": row["type"],
            "title": row["title"],
            "status": row["status"],
            "importance": row["importance"],
            "start_at": row["start_at"],
            "end_at": row["end_at"],
            "created_at": row["created_at"],
            "updated_at": row["updated_at"],
            "origin": row["origin"],
            "source_ref": row["source_ref"],
            "source_title": row["source_title"],
            "meta": _parse_detail(row["meta_json"]),
            "archived": bool(row["archived"]),
            "tags": tags or [],
            "out_count": out_n,
            "in_count": in_n,
            "parents": parents or [],
            "media": media or [],
            "assignees": assignees or [],
            # Account ownership (see docs in Store._assignee_index).  Distinct
            # from ``assignees`` above, which lists assigned_to link targets.
            "assignee": own,
            "assignee_effective": own or eff_name,
            "assignee_from": None if own else eff_from,
            # Comment rows (see Store.comments): user comments, and how many of
            # them nobody has resolved yet.  Agents read the open ones first.
            "comments_count": (comments or (0, 0))[0],
            "comments_open": (comments or (0, 0))[1],
        }
        if include_body:
            data["body"] = row["body"]
        else:
            body = row["body"]
            data["excerpt"] = excerpt(body)
        return data

    def hydrate(self, rows: list[sqlite3.Row], *, include_body=True) -> list[dict]:
        ids = [row["id"] for row in rows]
        tags = self._tags_for(ids)
        out_n, in_n = self._link_counts(ids)
        parents = self._parents(ids)
        media = self._media_preview(ids)
        assignees = self._assignees(ids)
        comments = self._comment_counts(ids)
        index = self._assignee_index()
        briefs: dict[int, dict] = {}
        sources = sorted({src for _n, src in (index.get(i, (None, None)) for i in ids) if src})
        if sources:
            marks = ",".join("?" * len(sources))
            for brow in self.con.execute(
                f"SELECT id, uid, title, type FROM units WHERE id IN ({marks})", sources
            ):
                briefs[brow["id"]] = {"id": brow["id"], "uid": brow["uid"],
                                      "title": brow["title"], "type": brow["type"]}

        def eff(unit_id: int):
            name, src = index.get(unit_id, (None, None))
            return name, (briefs.get(src) if src else None)

        member_counts = self._member_counts([row["id"] for row in rows if row["type"] == "topic"])
        out = [
            self._serialize(
                row,
                tags=tags[row["id"]],
                out_n=out_n[row["id"]],
                in_n=in_n[row["id"]],
                parents=parents[row["id"]],
                media=media[row["id"]],
                assignees=assignees[row["id"]],
                include_body=include_body,
                effective=eff(row["id"]),
                comments=comments.get(row["id"]),
            )
            for row in rows
        ]
        for unit in out:
            if unit["type"] == "topic":
                unit["member_count"] = member_counts.get(unit["id"], 0)
        return out

    # ---------------------------------------------------------------- filtering
    def _where(self, filters: dict | None) -> tuple[str, list]:
        f = filters or {}
        clauses = ["u.archived=0"]
        args: list = []
        if f.get("types_none"):
            clauses.append("0")

        q = str(f.get("q") or "").strip()
        if q:
            like = f"%{q}%"
            clauses.append("(u.title LIKE ? OR u.body LIKE ? OR u.uid LIKE ?)")
            args.extend([like, like, like])

        types = [t for t in (f.get("types") or []) if t]
        origins = [o for o in (f.get("origins") or []) if o]
        if types and origins:
            clauses.append(f"(u.type IN ({','.join('?' * len(types))}) OR u.origin IN ({','.join('?' * len(origins))}))")
            args.extend(types)
            args.extend(origins)
        elif types:
            clauses.append(f"u.type IN ({','.join('?' * len(types))})")
            args.extend(types)
        elif origins:
            clauses.append(f"u.origin IN ({','.join('?' * len(origins))})")
            args.extend(origins)

        statuses = [s for s in (f.get("statuses") or []) if s]
        if statuses:
            clauses.append(f"u.status IN ({','.join('?' * len(statuses))})")
            args.extend(statuses)

        importance = [i for i in (f.get("importance") or []) if i]
        if importance:
            clauses.append(f"u.importance IN ({','.join('?' * len(importance))})")
            args.extend(importance)

        tags = [t for t in (f.get("tags") or []) if t]
        tags_unset = [str(ns) for ns in (f.get("tags_unset") or []) if str(ns)]
        if tags or tags_unset:
            # A tag is "<namespace>:<value>".  Values inside one namespace are
            # alternatives (OR); different namespaces are independent facets
            # (AND).  "unset" is a virtual value for "this node has no tag in
            # that namespace"; it is never stored in unit_tags.
            groups: dict[str, list[str]] = {}
            for tag in dict.fromkeys(tags):
                groups.setdefault(tag_namespace(tag), []).append(tag)
            for ns in list(dict.fromkeys([*groups.keys(), *tags_unset])):
                parts: list[str] = []
                values = groups.get(ns, [])
                if values:
                    marks = ",".join("?" * len(values))
                    parts.append(
                        f"u.id IN (SELECT unit_id FROM unit_tags WHERE tag IN ({marks}))"
                    )
                if ns in tags_unset:
                    parts.append(
                        "NOT EXISTS (SELECT 1 FROM unit_tags t WHERE t.unit_id=u.id "
                        "AND substr(t.tag, 1, ?)=?)"
                    )
                if not parts:
                    continue
                clause = parts[0] if len(parts) == 1 else "(" + " OR ".join(parts) + ")"
                clauses.append(clause)
                if values:
                    args.extend(values)
                if ns in tags_unset:
                    args.extend([len(ns) + 1, ns + ":"])

        if f.get("roots"):
            # Top-level = no part_of parent.  Other link relations (relates_to,
            # supports, ...) do not make a node non-top-level.
            clauses.append(
                "u.id NOT IN (SELECT source_id FROM unit_links WHERE relation='part_of')"
            )

        ids = [int(i) for i in (f.get("ids") or [])]
        if ids:
            clauses.append(f"u.id IN ({','.join('?' * len(ids))})")
            args.extend(ids)

        # Timeline selection is endpoint membership: a unit is selected when
        # either its start or its end falls inside [from, to].  Unlike the old
        # overlap filter, a unit spanning the whole window is not selected just
        # because the window is in the middle of it.
        start_from = f.get("start_from")
        start_to = f.get("start_to")
        if start_from and start_to:
            clauses.append(
                "((u.start_at IS NOT NULL AND u.start_at BETWEEN ? AND ?) "
                "OR (u.end_at IS NOT NULL AND u.end_at BETWEEN ? AND ?))"
            )
            args.extend([start_from, start_to, start_from, start_to])
        elif start_from:
            clauses.append(
                "((u.start_at IS NOT NULL AND u.start_at >= ?) "
                "OR (u.end_at IS NOT NULL AND u.end_at >= ?))"
            )
            args.extend([start_from, start_from])
        elif start_to:
            clauses.append(
                "((u.start_at IS NOT NULL AND u.start_at <= ?) "
                "OR (u.end_at IS NOT NULL AND u.end_at <= ?))"
            )
            args.extend([start_to, start_to])

        if f.get("overdue"):
            # Past its end date and not finished (same rule as Home's Overdue).
            clauses.append("(u.end_at IS NOT NULL AND u.end_at < ? AND u.status NOT IN ('done','abandoned','archived'))")
            args.append(db.today())

        if f.get("has_dates"):
            clauses.append("(u.start_at IS NOT NULL OR u.end_at IS NOT NULL)")

        updated_since = f.get("updated_since")
        if updated_since:
            clauses.append("u.updated_at >= ?")
            args.append(str(updated_since))

        created_since = f.get("created_since")
        if created_since:
            clauses.append("u.created_at >= ?")
            args.append(str(created_since))

        # assignee=<user>|none|me matches the *effective* assignee, so a
        # filter on a user shows the whole branch they maintain.
        assignee_ids = self._assignee_filter_ids(f)
        if assignee_ids is not None:
            clauses.append("u.id IN (SELECT value FROM json_each(?))")
            args.append(json.dumps(assignee_ids))

        return " AND ".join(clauses), args

    def _order(self, sort: str) -> str:
        return {
            "updated": "u.updated_at DESC, u.id DESC",
            "created": "u.created_at DESC, u.id DESC",
            "title": "u.title COLLATE NOCASE ASC",
            "start": "CASE WHEN u.start_at IS NULL THEN 1 ELSE 0 END, u.start_at ASC, u.end_at ASC",
            "end": "CASE WHEN u.end_at IS NULL THEN 1 ELSE 0 END, u.end_at ASC, u.start_at ASC",
            "importance": "CASE u.importance WHEN 'p0' THEN 0 WHEN 'p1' THEN 1 WHEN 'p2' THEN 2 ELSE 3 END, u.updated_at DESC",
            "type": "u.type ASC, u.title COLLATE NOCASE ASC",
        }.get(sort or "updated", "u.updated_at DESC, u.id DESC")

    def list_units(self, filters: dict | None = None, *, include_facets=False) -> dict:
        f = filters or {}
        where, args = self._where(f)
        with self.lock:
            total = self.con.execute(f"SELECT COUNT(*) n FROM units u WHERE {where}", args).fetchone()["n"]
            limit = max(1, min(int(f.get("limit") or 200), 1000))
            offset = max(0, int(f.get("offset") or 0))
            rows = self.con.execute(
                f"SELECT u.* FROM units u WHERE {where} ORDER BY {self._order(str(f.get('sort') or 'updated'))} "
                f"LIMIT ? OFFSET ?",
                args + [limit, offset],
            ).fetchall()
            items = self.hydrate(rows)
            if f.get("slim"):
                for item in items:
                    body = str(item.pop("body", "") or "")
                    item["excerpt"] = excerpt(body)
                    item["body_truncated"] = len(body) > 240
            out = {"items": items, "total": total, "limit": limit, "offset": offset}
            if include_facets:
                out["facets"] = self.facets(f)
            return out

    def facets(self, filters: dict | None = None) -> dict:
        """Counts for the filter rail.  Tag counts ignore the tag filter itself,
        so a selected tag never hides the alternatives in its own namespace."""
        base = dict(filters or {})

        def facet_where(clear_keys):
            f = dict(base)
            for key in clear_keys:
                f[key] = [] if key in {"types", "statuses", "importance", "tags", "tags_unset"} else None
            return self._where(f)

        type_where, type_args = facet_where(["types", "types_none"])
        status_where, status_args = facet_where(["statuses", "statuses_none"])
        importance_where, importance_args = facet_where(["importance", "importance_none"])
        tag_where, tag_args = facet_where(["tags", "tags_unset"])
        with self.lock:
            # Each facet ignores its own filter, like a normal faceted search:
            # the type counts stay meaningful while a status filter is active,
            # and vice versa.  Counts from the three chip groups then describe
            # the same candidate set (apart from independently hidden values).
            types = {row["type"]: row["n"] for row in self.con.execute(
                f"SELECT u.type, COUNT(*) n FROM units u WHERE {type_where} GROUP BY u.type ORDER BY n DESC", type_args
            )}
            statuses = {row["status"]: row["n"] for row in self.con.execute(
                f"SELECT u.status, COUNT(*) n FROM units u WHERE {status_where} GROUP BY u.status", status_args
            )}
            importance = {row["importance"]: row["n"] for row in self.con.execute(
                f"SELECT u.importance, COUNT(*) n FROM units u WHERE {importance_where} GROUP BY u.importance", importance_args
            )}
            tag_counts: dict[str, int] = {}
            for row in self.con.execute(
                f"""SELECT t.tag, COUNT(DISTINCT t.unit_id) n
                    FROM unit_tags t JOIN units u ON u.id=t.unit_id
                    WHERE {tag_where} GROUP BY t.tag ORDER BY n DESC, t.tag ASC""",
                tag_args,
            ):
                if tag_namespace(row["tag"]) not in RESERVED:
                    tag_counts[row["tag"]] = row["n"]
        namespaces: dict[str, list[dict]] = {}
        for tag, count in tag_counts.items():
            namespaces.setdefault(tag_namespace(tag), []).append(
                {"tag": tag, "namespace": tag_namespace(tag), "value": tag_value(tag), "count": count}
            )
        for values in namespaces.values():
            values.sort(key=lambda item: (-item["count"], item["value"].lower()))
        unset_counts: dict[str, int] = {}
        for ns in namespaces:
            count = self.con.execute(
                "SELECT COUNT(*) n FROM units u WHERE "
                f"({tag_where}) AND NOT EXISTS (SELECT 1 FROM unit_tags t "
                "WHERE t.unit_id=u.id AND substr(t.tag, 1, ?)=?)",
                tag_args + [len(ns) + 1, ns + ":"],
            ).fetchone()["n"]
            unset_counts[ns] = count
        # Assignee counts ignore the assignee filter itself, like tags.
        fa = dict(filters or {})
        fa["assignees"] = []
        where_a, args_a = self._where(fa)
        index = self._assignee_index()
        assignee_counts: dict[str, int] = {}
        unassigned = 0
        for row in self.con.execute(f"SELECT u.id FROM units u WHERE {where_a}", args_a):
            name = index.get(row["id"], (None, None))[0]
            if name:
                assignee_counts[name] = assignee_counts.get(name, 0) + 1
            else:
                unassigned += 1
        # Stable time domain for the dual-ended range slider.  Derive it from
        # every active unit, not the filtered subset, so moving a handle does
        # not shrink the domain under the user's pointer.
        span = self.con.execute(
            """SELECT MIN(COALESCE(start_at, end_at)) AS min_date,
                      MAX(COALESCE(end_at, start_at)) AS max_date,
                      SUM(CASE WHEN start_at IS NULL AND end_at IS NULL THEN 1 ELSE 0 END) AS undated
                 FROM units WHERE archived=0"""
        ).fetchone()
        return {
            "types": types,
            "statuses": statuses,
            "importance": importance,
            "tags": [{"tag": k, "count": v} for k, v in tag_counts.items()],
            "namespaces": dict(sorted(namespaces.items())),
            "unset": unset_counts,
            "assignees": dict(sorted(assignee_counts.items(), key=lambda kv: kv[0].lower())),
            "unassigned": unassigned,
            "time": {
                "min": span["min_date"] if span else None,
                "max": span["max_date"] if span else None,
                "undated": int(span["undated"] or 0) if span else 0,
            },
        }

    # ------------------------------------------------------------------ reading
    def get_unit(self, ref: int | str, *, include_activity=True) -> dict:
        with self.lock:
            row = self._unit_row(ref)
            data = self.hydrate([row])[0]
            data["links_out"] = self._links_of(row["id"], outgoing=True)
            data["links_in"] = self._links_of(row["id"], outgoing=False)
            data["comments"] = self._comments_of(row["id"])
            data["topics"] = self.topics_of(row["id"])
            if include_activity:
                data["activity"] = self.activity(limit=40, unit_id=row["id"])
            data["raw_url"] = f"/api/units/{row['id']}"
            return data

    def _links_of(self, unit_id: int, *, outgoing: bool) -> list[dict]:
        if outgoing:
            rows = self.con.execute(
                """SELECT l.*, u.uid, u.title, u.type, u.status, u.importance
                   FROM unit_links l JOIN units u ON u.id=l.target_id
                   WHERE l.source_id=? ORDER BY l.id""",
                (unit_id,),
            ).fetchall()
            key = "target"
        else:
            rows = self.con.execute(
                """SELECT l.*, u.uid, u.title, u.type, u.status, u.importance
                   FROM unit_links l JOIN units u ON u.id=l.source_id
                   WHERE l.target_id=? ORDER BY l.id""",
                (unit_id,),
            ).fetchall()
            key = "source"
        out = []
        for row in rows:
            out.append(
                {
                    "id": row["id"],
                    "relation": row["relation"],
                    "note": row["note"],
                    "created_at": row["created_at"],
                    "created_by": row["created_by"],
                    key: {
                        "id": row["target_id"] if outgoing else row["source_id"],
                        "uid": row["uid"],
                        "title": row["title"],
                        "type": row["type"],
                        "status": row["status"],
                        "importance": row["importance"],
                    },
                }
            )
        return out

    def _snapshot_nodes(self, rows: list[sqlite3.Row], *, bodies: bool = True) -> list[dict]:
        nodes = []
        # Bound SQLite parameter lists even for very large projects.
        for offset in range(0, len(rows), 400):
            batch = rows[offset:offset + 400]
            hydrated = self.hydrate(batch, include_body=False)
            for row, node in zip(batch, hydrated):
                if bodies:
                    node["search_body"] = row["body"]
                node["body_truncated"] = len(row["body"] or "") > 240
            nodes.extend(hydrated)
        return nodes

    def graph_snapshot(self, *, bodies: bool = True) -> dict:
        """Complete filter data, loaded once per graph route rather than per click.

        ``bodies=False`` leaves out ``search_body`` (most of the bytes); a
        client loads them separately with ``graph_bodies`` when it needs
        full-text filtering.  ``cursor`` is the change-log position the
        snapshot reflects (see ``changes``).
        """
        with self.lock:
            cursor = self.change_cursor()
            rows = self.con.execute("SELECT * FROM units WHERE archived=0 ORDER BY updated_at DESC, id DESC").fetchall()
            nodes = self._snapshot_nodes(rows, bodies=bodies)
            edges = [dict(row) for row in self.con.execute(
                "SELECT l.id, l.source_id AS source, l.target_id AS target, l.relation, l.note "
                "FROM unit_links l JOIN units s ON s.id=l.source_id JOIN units t ON t.id=l.target_id "
                "WHERE s.archived=0 AND t.archived=0 ORDER BY l.id")]
            return {"nodes": nodes, "edges": edges, "total": len(nodes), "cursor": cursor,
                    "bodies": bool(bodies)}

    def graph_bodies(self) -> dict:
        """``search_body`` of every snapshot node, ``{"bodies": [[id, body], ...]}``."""
        with self.lock:
            cursor = self.change_cursor()
            rows = self.con.execute(
                "SELECT id, body FROM units WHERE archived=0 ORDER BY id").fetchall()
            return {"cursor": cursor, "bodies": [[row["id"], row["body"]] for row in rows]}

    # ------------------------------------------------------------------ changes
    # A cursor is "<change_log_id>.<seq>": the log identity of this database
    # file and the newest change_log row a client has seen (db.CHANGE_LOG_SCHEMA).
    CHANGES_ROW_LIMIT = 50_000
    CHANGES_UNIT_LIMIT = 2_000

    def change_cursor(self) -> str:
        with self.lock:
            log_id = db.meta_get(self.con, db.CHANGE_LOG_ID) or ""
            seq = self.con.execute("SELECT COALESCE(MAX(seq),0) FROM change_log").fetchone()[0]
            return f"{log_id}.{seq}"

    def reset_change_log_id(self) -> str:
        """New log identity after the database file was replaced (restore)."""
        with self.lock, self.con:
            return db.new_change_log_id(self.con)

    def prune_change_log(self, keep: int = db.CHANGE_LOG_KEEP) -> int:
        with self.lock, self.con:
            top = self.con.execute("SELECT COALESCE(MAX(seq),0) FROM change_log").fetchone()[0]
            return self.con.execute("DELETE FROM change_log WHERE seq <= ?",
                                    (top - max(1, int(keep)),)).rowcount

    def changes(self, cursor: str, *, bodies: bool = True,
                unit_limit: int | None = None) -> dict:
        """What changed in the graph snapshot since ``cursor``.

        ``units`` are complete snapshot nodes (upserts) and ``deleted`` ids are
        no longer in the snapshot (archived or deleted).  ``touched`` lists
        every unit whose snapshot node, detail or incident edges may differ:
        a client replaces all of its edges incident to a touched unit with
        ``edges``.  Applying the result to the snapshot taken at ``cursor``
        yields ``graph_snapshot()`` (Store tests check this).  ``reset: true``
        means the cursor is unknown, too old or the change is too large; the
        client reloads the snapshot instead.
        """
        unit_limit = self.CHANGES_UNIT_LIMIT if unit_limit is None else unit_limit
        with self.lock:
            current = self.change_cursor()
            log_id, _, seq_text = str(cursor or "").partition(".")
            out = {"cursor": current, "reset": False, "units": [], "deleted": [],
                   "edges": [], "touched": [], "bodies": bool(bodies)}
            if current == cursor:
                return out
            top = int(current.rsplit(".", 1)[1])
            floor = self.con.execute("SELECT MIN(seq) FROM change_log").fetchone()[0]
            try:
                since = int(seq_text)
            except ValueError:
                since = -1
            if (log_id != current.split(".", 1)[0] or since < 0 or since > top
                    or (floor is not None and since < floor - 1)):
                out["reset"] = True
                out["reason"] = "cursor"
                return out
            rows = self.con.execute(
                "SELECT kind, ref, tree FROM change_log WHERE seq > ? LIMIT ?",
                (since, self.CHANGES_ROW_LIMIT + 1)).fetchall()
            if len(rows) > self.CHANGES_ROW_LIMIT:
                out["reset"] = True
                out["reason"] = "size"
                return out
            changed = {row["ref"] for row in rows if row["kind"] == "unit"}
            tree = {row["ref"] for row in rows if row["kind"] == "unit" and row["tree"]}
            if not changed:
                return out
            touched = set(changed)
            # Neighbours show a changed unit's title/type/status (parents,
            # assignees, link lists); inherited owners show their source.
            for chunk in _chunks(sorted(changed), 400):
                marks = ",".join("?" * len(chunk))
                for row in self.con.execute(
                        f"SELECT source_id, target_id FROM unit_links "
                        f"WHERE source_id IN ({marks}) OR target_id IN ({marks})", chunk + chunk):
                    touched.add(row[0])
                    touched.add(row[1])
            index = self._assignee_index()
            touched.update(uid for uid, (_name, src) in index.items() if src in changed)
            if tree:
                children: dict[int, list[int]] = {}
                for row in self.con.execute(
                        "SELECT source_id, target_id FROM unit_links WHERE relation='part_of'"):
                    children.setdefault(row[1], []).append(row[0])
                queue = deque(tree)
                seen = set(tree)
                while queue:
                    for child in children.get(queue.popleft(), ()):
                        if child not in seen:
                            seen.add(child)
                            queue.append(child)
                touched.update(seen)
            # A topic's member count follows its rule over every unit.
            touched.update(row[0] for row in self.con.execute(
                "SELECT id FROM units WHERE type='topic'"))
            if len(touched) > unit_limit:
                out["reset"] = True
                out["reason"] = "size"
                return out
            ids = sorted(touched)
            live_rows = []
            for chunk in _chunks(ids, 400):
                marks = ",".join("?" * len(chunk))
                live_rows.extend(self.con.execute(
                    f"SELECT * FROM units WHERE archived=0 AND id IN ({marks}) "
                    "ORDER BY updated_at DESC, id DESC", chunk).fetchall())
            live = {row["id"] for row in live_rows}
            out["units"] = self._snapshot_nodes(live_rows, bodies=bodies)
            out["deleted"] = [uid for uid in ids if uid not in live]
            edges: dict[int, dict] = {}
            for chunk in _chunks(ids, 400):
                marks = ",".join("?" * len(chunk))
                for row in self.con.execute(
                        "SELECT l.id, l.source_id AS source, l.target_id AS target, l.relation, l.note "
                        "FROM unit_links l JOIN units s ON s.id=l.source_id "
                        "JOIN units t ON t.id=l.target_id WHERE s.archived=0 AND t.archived=0 "
                        f"AND (l.source_id IN ({marks}) OR l.target_id IN ({marks}))",
                        chunk + chunk):
                    edges[row["id"]] = dict(row)
            out["edges"] = [edges[key] for key in sorted(edges)]
            out["touched"] = ids
            return out

    def graph(self, filters: dict | None = None, *, context=False, limit=700, slim=False) -> dict:
        f = dict(filters or {})
        f["limit"] = limit
        f["offset"] = 0
        base = self.list_units(f)
        nodes = base["items"]
        selected = {n["id"] for n in nodes}

        extra_nodes: list[dict] = []
        if context and selected:
            marks = ",".join("?" * len(selected))
            rows = self.con.execute(
                f"""SELECT DISTINCT u.* FROM units u
                    WHERE u.archived=0 AND u.id NOT IN ({marks})
                    AND (u.id IN (SELECT target_id FROM unit_links WHERE source_id IN ({marks}))
                      OR u.id IN (SELECT source_id FROM unit_links WHERE target_id IN ({marks})))
                    LIMIT 120""",
                list(selected) + list(selected) + list(selected),
            ).fetchall()
            extra_nodes = self.hydrate(rows)
            for node in extra_nodes:
                node["context"] = True
            nodes = nodes + extra_nodes

        ids = {n["id"] for n in nodes}
        edges: list[dict] = []
        if ids:
            marks = ",".join("?" * len(ids))
            rows = self.con.execute(
                f"""SELECT l.*, s.uid suid, t.uid tuid
                    FROM unit_links l JOIN units s ON s.id=l.source_id JOIN units t ON t.id=l.target_id
                    WHERE l.source_id IN ({marks}) AND l.target_id IN ({marks})
                    ORDER BY l.id""",
                list(ids) + list(ids),
            ).fetchall()
            for row in rows:
                edges.append(
                    {
                        "id": row["id"],
                        "source": row["source_id"],
                        "target": row["target_id"],
                        "relation": row["relation"],
                        "note": row["note"],
                    }
                )

        if slim:
            # The graph canvas needs titles/tags/status, not full documents.
            # Keeping node bodies out of this response cuts the payload by
            # roughly two thirds; the inspector fetches the full unit on click.
            for node in nodes:
                body = str(node.pop("body", "") or "")
                node["excerpt"] = excerpt(body)
                node["body_truncated"] = len(body) > 240

        # Degree is a display property, not a stored one.
        degree: dict[int, int] = {i: 0 for i in ids}
        for edge in edges:
            degree[edge["source"]] = degree.get(edge["source"], 0) + 1
            degree[edge["target"]] = degree.get(edge["target"], 0) + 1
        for node in nodes:
            node["degree"] = degree.get(node["id"], 0)
        return {"nodes": nodes, "edges": edges, "context_count": len(extra_nodes), "total": base["total"]}

    def timeline(self, filters: dict | None = None) -> dict:
        f = dict(filters or {})
        f["has_dates"] = True
        f["limit"] = 1000
        data = self.list_units(f)
        items = [u for u in data["items"] if u.get("start_at") or u.get("end_at")]
        items.sort(key=lambda u: (u.get("start_at") or u.get("end_at") or "", u["title"]))
        return {"items": items, "total": len(items)}

    # ------------------------------------------------------------------- writes
    def create_unit(self, data: dict, actor: str = "human:ui") -> dict:
        title = _clean_text(data.get("title"), "title", max_len=MAX_TITLE, required=True)
        body = _clean_text(data.get("body"), "body", max_len=MAX_BODY)
        type_name = str(data.get("type") or "note").strip().lower()
        try:
            tags = normalize_tags(
                _without_needs(data.get("tags") or []),
                type_name=type_name,
                status=data.get("status"),
                importance=data.get("importance"),
            )
        except TagError as exc:
            raise StoreError(str(exc)) from exc
        type_name = next(tag_value(t) for t in tags if tag_namespace(t) == "type")
        status = next(tag_value(t) for t in tags if tag_namespace(t) == "status")
        importance = next(tag_value(t) for t in tags if tag_namespace(t) == "importance")
        if type_name == "topic":
            # Reject a bad design before the topic exists, so a failed
            # create leaves no empty topic behind.
            self._check_topic_design(data)
        needs = needs_transition(None, type_name, status)
        if needs:
            tags.append(f"{NEEDS_NS}:{needs}")
        start_at = _clean_date(data.get("start_at"), "start_at")
        end_at = _clean_date(data.get("end_at"), "end_at")
        if start_at and end_at and end_at < start_at:
            raise StoreError("end_at is before start_at")
        # Default dates (see default_dates): explicit values win; a default
        # that would contradict an explicit date is skipped.
        today = db.today()
        if start_at is None and not (end_at and end_at < today):
            start_at = today
        if end_at is None and status == "done" and not (start_at and today < start_at):
            end_at = today
        meta = data.get("meta") or {}
        if not isinstance(meta, dict):
            raise StoreError("meta must be an object")
        source_ref = _clean_text(data.get("source_ref"), "source_ref", max_len=400) or None
        # No ``assignee`` key: the creating account owns the unit.  An explicit
        # null/"" keeps it unassigned, so it inherits from its part_of parent.
        if "assignee" in data:
            assignee = self._clean_assignee(data.get("assignee"))
        else:
            assignee = self._creator_assignee(actor)
        now = db.utc_now()

        with self.lock, self.con:
            uid = self._next_uid(type_name)
            cur = self.con.execute(
                """INSERT INTO units(uid,type,title,body,status,importance,start_at,end_at,
                   created_at,updated_at,origin,source_ref,source_title,meta_json,assignee)
                   VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)""",
                (
                    uid,
                    type_name,
                    title,
                    body,
                    status,
                    importance,
                    start_at,
                    end_at,
                    now,
                    now,
                    str(data.get("origin") or "manual")[:40],
                    source_ref,
                    _clean_text(data.get("source_title"), "source_title", max_len=300) or None,
                    json.dumps(meta, ensure_ascii=False),
                    assignee,
                ),
            )
            unit_id = cur.lastrowid
            self._write_tags(unit_id, tags)
            detail = {"uid": uid, "title": title, "type": type_name, "status": status}
            if assignee:
                detail["assignee"] = assignee
            self.log_activity(actor, "unit.create", unit_id, detail, commit=False)
            if needs:
                self._log_needs(actor, unit_id, uid, title, None, needs)
        self._after_write()
        for link in data.get("links") or []:
            try:
                self.create_link(unit_id, link.get("target") or link.get("target_id"),
                                 link.get("relation") or "relates_to", link.get("note") or "", actor)
            except StoreError:
                pass
        for media in data.get("media") or []:
            try:
                self.add_media(unit_id, media.get("url"), media.get("kind") or "link",
                               media.get("caption") or "", actor)
            except StoreError:
                pass
        if type_name == "topic" and (data.get("members") or data.get("design")):
            design = data.get("design") if isinstance(data.get("design"), dict) else {}
            self.design_topic(unit_id, {**design, "add": data.get("members") or design.get("add") or []}, actor)
        return self.get_unit(unit_id)

    def update_unit(self, ref: int | str, patch: dict, actor: str = "human:ui", *,
                    history_message: str = "Save text") -> dict:
        with self.lock, self.con:
            row = self._unit_row(ref)
            unit_id = row["id"]
            changed: list[str] = []

            title = row["title"]
            if "title" in patch:
                title = _clean_text(patch.get("title"), "title", max_len=MAX_TITLE, required=True)
                changed.append("title")
            body = row["body"]
            if "body" in patch:
                body = _clean_text(patch.get("body"), "body", max_len=MAX_BODY)
                changed.append("body")
            start_at = row["start_at"]
            if "start_at" in patch:
                start_at = _clean_date(patch.get("start_at"), "start_at")
                changed.append("start_at")
            end_at = row["end_at"]
            if "end_at" in patch:
                end_at = _clean_date(patch.get("end_at"), "end_at")
                changed.append("end_at")
            if start_at and end_at and end_at < start_at:
                raise StoreError("end_at is before start_at")

            old_tags = [r["tag"] for r in self.con.execute(
                "SELECT tag FROM unit_tags WHERE unit_id=? ORDER BY tag", (unit_id,)
            )]
            want_tags = patch.get("tags")
            explicit_type = patch.get("type", row["type"])
            explicit_status = patch.get("status", row["status"])
            explicit_importance = patch.get("importance", row["importance"])
            if want_tags is None:
                # Keep all free tags, replace the reserved trio with the effective fields.
                free = [t for t in old_tags if tag_namespace(t) not in RESERVED]
                tag_source = free
            else:
                tag_source = list(want_tags or [])
            # ``needs:`` is ARK's: whatever the client sent, recompute it below.
            tag_source = _without_needs(tag_source)
            old_needs = _needs_of(old_tags)
            try:
                tags = normalize_tags(
                    tag_source,
                    type_name=explicit_type,
                    status=explicit_status,
                    importance=explicit_importance,
                )
            except TagError as exc:
                raise StoreError(str(exc)) from exc
            type_name = next(tag_value(t) for t in tags if tag_namespace(t) == "type")
            status = next(tag_value(t) for t in tags if tag_namespace(t) == "status")
            importance = next(tag_value(t) for t in tags if tag_namespace(t) == "importance")
            needs = needs_transition(old_needs, type_name, status, bool(row["archived"]))
            if needs:
                tags.append(f"{NEEDS_NS}:{needs}")
            if type_name != row["type"]:
                changed.append("type")
            if status != row["status"]:
                changed.append("status")
                # Completion records its date when none is set.  A planned
                # end_at (deadline) is kept, and reopening keeps end_at as the
                # last completion until the caller clears or changes it.
                today = db.today()
                if status == "done" and end_at is None and not (start_at and today < start_at):
                    end_at = today
                    changed.append("end_at")
            if importance != row["importance"]:
                changed.append("importance")
            if sorted(tags) != sorted(old_tags):
                changed.append("tags")

            meta = row["meta_json"]
            if "meta" in patch:
                if not isinstance(patch["meta"], dict):
                    raise StoreError("meta must be an object")
                meta = json.dumps(patch["meta"], ensure_ascii=False)
                changed.append("meta")

            old_assignee = row["assignee"]
            assignee = old_assignee
            if "assignee" in patch:
                raw = patch.get("assignee")
                # Echoing the stored value back is a no-op, even if that account
                # has since left the project (the resolver would refuse it).
                if not (isinstance(raw, str) and old_assignee
                        and raw.strip().lower() == old_assignee.lower()):
                    assignee = self._clean_assignee(raw)
                if assignee != old_assignee:
                    changed.append("assignee")

            if not changed:
                return self.get_unit(unit_id)
            self.con.execute(
                """UPDATE units SET title=?,body=?,type=?,status=?,importance=?,start_at=?,end_at=?,
                   meta_json=?,assignee=?,updated_at=? WHERE id=?""",
                (title, body, type_name, status, importance, start_at, end_at, meta, assignee,
                 db.utc_now(), unit_id),
            )
            if title != row["title"] or body != row["body"]:
                if history_message != "Save text" or not actor.startswith("human:"):
                    pending = self.con.execute("SELECT actor FROM node_text_pending WHERE unit_id=?", (unit_id,)).fetchone()
                    if pending:
                        self._record_text_history(row["id"], row["uid"], row["title"], row["body"],
                                                  pending["actor"], "Save pending human text")
                self._record_text_history(row["id"], row["uid"], title, body, actor, history_message)
            self._write_tags(unit_id, tags)
            detail = {"uid": row["uid"], "title": title, "fields": sorted(set(changed))}
            if "status" in changed:
                # Home's pulse rebuilds past states from these (overview.py).
                detail["status"] = {"from": row["status"], "to": status}
            if "assignee" in changed:
                detail["assignee"] = {"from": old_assignee, "to": assignee}
            self.log_activity(actor, "unit.update", unit_id, detail, commit=False)
            if needs != old_needs:
                self._log_needs(actor, unit_id, row["uid"], title, old_needs, needs)
        self._after_write()
        return self.get_unit(unit_id)

    @staticmethod
    def _text_hash(row) -> str:
        return hashlib.sha256(json.dumps([row["title"], row["body"]],
                                        ensure_ascii=False).encode("utf-8")).hexdigest()

    def _record_text_history(self, unit_id: int, uid: str, title: str, body: str,
                             actor: str, message: str = "Save text") -> None:
        state = self.con.execute("SELECT * FROM node_text_history WHERE unit_id=?", (unit_id,)).fetchone()
        if not state or not state["enabled"]:
            return
        if message == "Save text" and actor.startswith("human:"):
            self.con.execute("INSERT INTO node_text_pending(unit_id,edited_at,actor) VALUES(?,?,?) "
                             "ON CONFLICT(unit_id) DO UPDATE SET edited_at=excluded.edited_at,actor=excluded.actor",
                             (unit_id, time.time(), actor))
            self._start_history_worker()
            return
        try:
            head = self.text_history.record(uid, title, body, state["head"], actor, message)
        except HistoryError as exc:
            raise StoreError(str(exc)) from exc
        self.con.execute("UPDATE node_text_history SET head=? WHERE unit_id=?", (head, unit_id))
        self.con.execute("DELETE FROM node_text_pending WHERE unit_id=?", (unit_id,))

    def _start_history_worker(self) -> None:
        if self._history_worker is not None:
            return
        def run():
            while not self._history_stop.wait(15):
                try:
                    self.flush_node_versions()
                except Exception:
                    logging.getLogger(__name__).exception("Could not save pending node versions; will retry")
        self._history_worker = threading.Thread(target=run, name="ark-node-history", daemon=True)
        self._history_worker.start()

    def flush_node_versions(self, now: float | None = None) -> int:
        now = time.time() if now is None else now
        saved = 0
        with self.lock:
            pending = self.con.execute("SELECT p.unit_id FROM node_text_pending p "
                                       "JOIN node_text_history h ON h.unit_id=p.unit_id "
                                       "WHERE h.enabled=1 AND p.edited_at<=? "
                                       "ORDER BY p.checked_at,p.unit_id LIMIT 20", (now - 300,)).fetchall()
        for item in pending:
            if self._history_stop.is_set():
                break
            with self.lock, self.con:
                # Recheck after taking SQLite's writer lock: another process may edit/checkpoint.
                self.con.execute("UPDATE node_text_history SET head=head WHERE unit_id=?", (item["unit_id"],))
                pending_row = self.con.execute("SELECT * FROM node_text_pending WHERE unit_id=?",
                                               (item["unit_id"],)).fetchone()
                if not pending_row or pending_row["edited_at"] > now - 300:
                    continue
                row = self._unit_row(item["unit_id"])
                state = self.con.execute("SELECT * FROM node_text_history WHERE unit_id=?", (row["id"],)).fetchone()
                if not state["enabled"]:
                    continue
                self.con.execute("UPDATE node_text_pending SET checked_at=? WHERE unit_id=?", (now, row["id"]))
                baseline = self.node_revision(row["id"], state["head"])
                size = sum(len(baseline[field]) for field in ("title", "body"))
                threshold = min(100, max(1, (size + 9) // 10))
                changed = 0
                for field in ("title", "body"):
                    old, new = baseline[field], row[field]
                    # Trim unchanged ends first; keep character matching bounded for large documents.
                    start = 0
                    while start < min(len(old), len(new)) and old[start] == new[start]:
                        start += 1
                    old, new = old[start:], new[start:]
                    end = 0
                    while end < min(len(old), len(new)) and old[-1-end] == new[-1-end]:
                        end += 1
                    if end:
                        old, new = old[:-end], new[:-end]
                    if len(old) + len(new) > 20000:
                        changed += max(len(old), len(new))
                    else:
                        changed += sum(max(b-a, d-c) for tag, a, b, c, d in
                                       SequenceMatcher(None, old, new).get_opcodes() if tag != "equal")
                if changed == 0:
                    self.con.execute("DELETE FROM node_text_pending WHERE unit_id=?", (row["id"],))
                elif changed >= threshold:
                    self._record_text_history(row["id"], row["uid"], row["title"], row["body"],
                                              pending_row["actor"], "Save after editing pause")
                    saved += 1
        return saved

    def save_node_version(self, ref: int | str, expected_hash: str, actor: str = "human:ui") -> dict:
        with self.lock, self.con:
            row = self._unit_row(ref)
            self.con.execute("UPDATE node_text_history SET head=head WHERE unit_id=?", (row["id"],))
            row = self._unit_row(ref)
            if not expected_hash or self._text_hash(row) != expected_hash:
                raise StoreError("Node text changed; reload its history before saving a version")
            state = self.con.execute("SELECT enabled FROM node_text_history WHERE unit_id=?", (row["id"],)).fetchone()
            if not state or not state["enabled"]:
                raise StoreError("Enable history before saving a version")
            self._record_text_history(row["id"], row["uid"], row["title"], row["body"], actor, "Manual checkpoint")
        return self.node_history(ref)

    def node_history(self, ref: int | str, *, limit: int = 50, offset: int = 0) -> dict:
        if not isinstance(limit, int) or not 1 <= limit <= 200 or not isinstance(offset, int) or offset < 0:
            raise StoreError("History limit must be 1..200 and offset must be non-negative")
        with self.lock:
            row = self._unit_row(ref)
            state = self.con.execute("SELECT * FROM node_text_history WHERE unit_id=?", (row["id"],)).fetchone()
            head = state["head"] if state else None
            versions = []
            if head and self.text_history.available:
                try:
                    versions = self.text_history.versions(head, limit + 1, offset)
                except HistoryError as exc:
                    raise StoreError(str(exc)) from exc
            return {"id": row["id"], "uid": row["uid"], "enabled": bool(state and state["enabled"]),
                    "available": self.text_history.available, "head": head,
                    "media_kinds": {str(media["id"]): media_preview_kind(dict(media)) or media["kind"]
                                    for media in self.con.execute(
                                        "SELECT id,url,kind FROM unit_media WHERE unit_id=?", (row["id"],))},
                    "current_hash": self._text_hash(row), "versions": versions[:limit],
                    "pending": bool(self.con.execute("SELECT 1 FROM node_text_pending WHERE unit_id=?", (row["id"],)).fetchone()),
                    "has_more": len(versions) > limit, "limit": limit, "offset": offset}

    def set_node_history(self, ref: int | str, enabled: bool, actor: str = "human:ui") -> dict:
        if not isinstance(enabled, bool):
            raise StoreError("enabled must be a boolean")
        with self.lock, self.con:
            row = self._unit_row(ref)
            self.con.execute("INSERT OR IGNORE INTO node_text_history(unit_id) VALUES(?)", (row["id"],))
            row = self._unit_row(ref)
            self.con.execute("UPDATE node_text_history SET enabled=? WHERE unit_id=?", (int(enabled), row["id"]))
            if enabled:
                self._record_text_history(row["id"], row["uid"], row["title"], row["body"], actor, "Enable history")
            self.log_activity(actor, "unit.history", row["id"], {"uid": row["uid"], "enabled": enabled}, commit=False)
        self._after_write()
        return self.node_history(ref)

    def node_revision(self, ref: int | str, commit: str) -> dict:
        with self.lock:
            row = self._unit_row(ref)
            state = self.con.execute("SELECT head FROM node_text_history WHERE unit_id=?", (row["id"],)).fetchone()
            if not state or not state["head"]:
                raise StoreError("No text history for this node")
            try:
                return self.text_history.revision(state["head"], commit)
            except HistoryError as exc:
                raise StoreError(str(exc)) from exc

    def node_diff(self, ref: int | str, before: str | None = None, after: str | None = None) -> dict:
        with self.lock:
            row = self._unit_row(ref)
            state = self.con.execute("SELECT head,enabled FROM node_text_history WHERE unit_id=?", (row["id"],)).fetchone()
            if not state or not state["head"]:
                raise StoreError("No text history for this node")
            try:
                result = self.text_history.diff(state["head"], before, after)
                result.update(current_hash=self._text_hash(row),
                              can_apply=bool(state["enabled"] and result["to"] == state["head"] and result["from"]
                                             and not self.con.execute("SELECT 1 FROM node_text_pending WHERE unit_id=?", (row["id"],)).fetchone()))
                return result
            except HistoryError as exc:
                raise StoreError(str(exc)) from exc

    def restore_node_revision(self, ref: int | str, commit: str, expected_hash: str,
                              actor: str = "human:ui") -> dict:
        with self.lock, self.con:
            row = self._unit_row(ref)
            # Acquire SQLite's writer lock before checking the current text.
            self.con.execute("UPDATE node_text_history SET enabled=enabled WHERE unit_id=?", (row["id"],))
            row = self._unit_row(ref)
            if not expected_hash or self._text_hash(row) != expected_hash:
                raise StoreError("Node text changed; reload its history before restoring")
            state = self.con.execute("SELECT enabled FROM node_text_history WHERE unit_id=?", (row["id"],)).fetchone()
            if not state or not state["enabled"]:
                raise StoreError("Enable history before restoring a revision")
            revision = self.node_revision(ref, commit)
            # update_unit's transaction must finish here before its notifications.
            return self.update_unit(ref, {"title": revision["title"], "body": revision["body"]}, actor,
                                    history_message="Restore " + commit[:12])

    def apply_node_history_change(self, ref: int | str, before: str, after: str, file: str,
                                  change: int | None, expected_hash: str, actor: str = "human:ui") -> dict:
        if not isinstance(file, str) or file not in ("title.txt", "body.md"):
            raise StoreError("History file must be title.txt or body.md")
        if change is not None and (type(change) is not int or change < 0):
            raise StoreError("History change must be a non-negative integer")
        with self.lock, self.con:
            row = self._unit_row(ref)
            self.con.execute("UPDATE node_text_history SET enabled=enabled WHERE unit_id=?", (row["id"],))
            row = self._unit_row(ref)
            if not expected_hash or self._text_hash(row) != expected_hash:
                raise StoreError("Node text changed; reload its history before applying a change")
            state = self.con.execute("SELECT enabled FROM node_text_history WHERE unit_id=?", (row["id"],)).fetchone()
            if not state or not state["enabled"]:
                raise StoreError("Enable history before applying a change")
            original = self.node_revision(ref, before)
            target = self.node_revision(ref, after)
            if any(target[field] != row[field] for field in ("title", "body")):
                raise StoreError("Choose the current version as To before applying a change")
            field = "title" if file == "title.txt" else "body"
            text = original[field]
            if change is not None:
                blocks = self.node_diff(ref, before, after)["changes"]
                block = next((item for item in blocks if item["file"] == file and item["index"] == change), None)
                if block is None:
                    raise StoreError("History change not found; reload the diff")
                text = self.text_history.reapply_block(original[field], target[field], block)
            return self.update_unit(ref, {field: text}, actor,
                                    history_message=f"Apply {file} from {before[:12]}"
                                    + (f" change {change}" if change is not None else ""))

    def _write_tags(self, unit_id: int, tags: Iterable[str]) -> None:
        self.con.execute("DELETE FROM unit_tags WHERE unit_id=?", (unit_id,))
        self.con.executemany(
            "INSERT OR IGNORE INTO unit_tags(unit_id,tag) VALUES(?,?)", [(unit_id, tag) for tag in tags]
        )

    def _log_needs(self, actor: str, unit_id: int, uid: str, title: str,
                   old: str | None, new: str | None) -> None:
        self.log_activity(actor, "unit.needs", unit_id,
                          {"uid": uid, "title": title, "from": old, "to": new}, commit=False)

    def _sync_needs(self, row: sqlite3.Row, archived: bool, actor: str) -> None:
        """Recompute the ``needs:`` tag after an archive/restore (no field change)."""
        tags = [r["tag"] for r in self.con.execute(
            "SELECT tag FROM unit_tags WHERE unit_id=?", (row["id"],))]
        old = _needs_of(tags)
        new = needs_transition(old, row["type"], row["status"], archived)
        if new == old:
            return
        self.con.execute("DELETE FROM unit_tags WHERE unit_id=? AND substr(tag,1,?)=?",
                         (row["id"], len(NEEDS_NS) + 1, NEEDS_NS + ":"))
        if new:
            self.con.execute("INSERT OR IGNORE INTO unit_tags(unit_id,tag) VALUES(?,?)",
                             (row["id"], f"{NEEDS_NS}:{new}"))
        self._log_needs(actor, row["id"], row["uid"], row["title"], old, new)

    def archive_unit(self, ref: int | str, actor: str = "human:ui") -> dict:
        with self.lock, self.con:
            row = self._unit_row(ref)
            self.con.execute("UPDATE units SET archived=1, updated_at=? WHERE id=?", (db.utc_now(), row["id"]))
            self._sync_needs(row, True, actor)
            links = self.con.execute(
                "SELECT id, source_id, target_id, relation FROM unit_links WHERE source_id=? OR target_id=?",
                (row["id"], row["id"]),
            ).fetchall()
            self.con.execute("DELETE FROM unit_links WHERE source_id=? OR target_id=?", (row["id"], row["id"]))
            self.log_activity(actor, "unit.archive", row["id"],
                              {"uid": row["uid"], "title": row["title"], "links_deleted": len(links)},
                              commit=False)
        self._after_write()
        return {"id": row["id"], "uid": row["uid"], "archived": True,
                "links_deleted": len(links)}

    def restore_unit(self, ref: int | str, actor: str = "human:ui") -> dict:
        with self.lock, self.con:
            row = self._unit_row(ref)
            self.con.execute("UPDATE units SET archived=0, updated_at=? WHERE id=?",
                             (db.utc_now(), row["id"]))
            self._sync_needs(row, False, actor)
            self.log_activity(actor, "unit.restore", row["id"],
                              {"uid": row["uid"], "title": row["title"]}, commit=False)
        self._after_write()
        return {"id": row["id"], "uid": row["uid"], "archived": False}

    def insert_between(self, child_ref: int | str, title: str = "New node",
                       type_name: str = "note", actor: str = "human:ui") -> dict:
        """Create a node between an existing child and its part_of parent."""
        with self.lock:
            child = self._unit_row(child_ref)
            parent_link = self.con.execute(
                "SELECT * FROM unit_links WHERE source_id=? AND relation='part_of' "
                "ORDER BY id LIMIT 1",
                (child["id"],),
            ).fetchone()
            if not parent_link:
                raise StoreError("This node has no parent to insert between")
            parent = self._unit_row(parent_link["target_id"])
            unit = self.create_unit({
                "type": type_name,
                "title": title,
                "status": "open",
                "importance": "p2",
                "tags": ["origin:manual"],
                "origin": "manual",
            }, actor=actor)
            removed = self.delete_link(parent_link["id"], actor=actor)
            to_parent = self.create_link(unit["id"], parent["id"], "part_of",
                                         "Inserted between nodes", actor=actor)
            from_child = self.create_link(child["id"], unit["id"], "part_of",
                                          "Inserted between nodes", actor=actor)
            self.log_activity(actor, "unit.insert_between", child["id"],
                              {"child": child["uid"], "parent": parent["uid"],
                               "new": unit["uid"]})
        return {
            "unit": unit,
            "parent": {"id": parent["id"], "uid": parent["uid"], "title": parent["title"]},
            "child": {"id": child["id"], "uid": child["uid"], "title": child["title"]},
            "removed_link": removed,
            "links": [to_parent, from_child],
        }

    # -------------------------------------------------------------- links/media
    def create_link(self, source: int | str, target: int | str, relation: str, note: str = "",
                    actor: str = "human:ui") -> dict:
        relation = str(relation or "").strip()
        if relation not in RELATIONS:
            raise StoreError(f"unknown relation: {relation!r}")
        with self.lock, self.con:
            s = self._unit_row(source)
            t = self._unit_row(target)
            if s["id"] == t["id"]:
                raise StoreError("a unit cannot link to itself")
            existing = self.con.execute(
                "SELECT * FROM unit_links WHERE source_id=? AND target_id=? AND relation=?",
                (s["id"], t["id"], relation),
            ).fetchone()
            if existing:
                return {"id": existing["id"], "already": True}
            cur = self.con.execute(
                "INSERT INTO unit_links(source_id,target_id,relation,note,created_at,created_by) VALUES(?,?,?,?,?,?)",
                (s["id"], t["id"], relation, _clean_text(note, "note", max_len=500), db.utc_now(), actor),
            )
            link_id = cur.lastrowid
            self.log_activity(
                actor, "link.create", s["id"],
                {"from": s["uid"], "to": t["uid"], "relation": relation, "link_id": link_id}, commit=False,
            )
        self._after_write()
        return {"id": link_id, "source": s["uid"], "target": t["uid"], "relation": relation}

    def update_link(self, link_id: int, relation: str, actor: str = "human:ui") -> dict:
        relation = str(relation or "").strip()
        if relation not in RELATIONS:
            raise StoreError(f"unknown relation: {relation!r}")
        with self.lock, self.con:
            row = self.con.execute("SELECT * FROM unit_links WHERE id=?", (int(link_id),)).fetchone()
            if not row:
                raise StoreError(f"link not found: {link_id}")
            old = row["relation"]
            self.con.execute("UPDATE unit_links SET relation=? WHERE id=?", (relation, int(link_id)))
            self.log_activity(actor, "link.update", row["source_id"],
                              {"link_id": int(link_id), "from": old, "to": relation}, commit=False)
        self._after_write()
        return {"id": int(link_id), "relation": relation, "updated": True}

    def delete_link(self, link_id: int, actor: str = "human:ui") -> dict:
        with self.lock, self.con:
            row = self.con.execute(
                """SELECT l.*, s.uid suid, s.title stitle, t.uid tuid, t.title ttitle
                   FROM unit_links l JOIN units s ON s.id=l.source_id JOIN units t ON t.id=l.target_id
                   WHERE l.id=?""", (int(link_id),)
            ).fetchone()
            if not row:
                raise StoreError(f"link not found: {link_id}")
            self.con.execute("DELETE FROM unit_links WHERE id=?", (row["id"],))
            self.log_activity(actor, "link.delete", row["source_id"],
                              {"from": row["suid"], "to": row["tuid"], "relation": row["relation"]}, commit=False)
        self._after_write()
        return {"id": row["id"], "deleted": True}

    # ------------------------------------------------------------------ topics
    # A topic is an ordinary unit (type:topic) that also holds a curated
    # subgraph, i.e. a saved, hand-tuned filter over the whole graph:
    #
    #   members = (units matching the topic's rule + explicitly included)
    #             - explicitly excluded
    #
    # topic_members rows carry the explicit part (mode include / exclude) and
    # the presentation of every member: narrative position, canvas x/y and a
    # topic-local caption (a rule-matched member gets a ``layout`` row once it
    # is moved or captioned).  Edges are the real unit_links between members;
    # the topic only chooses which of them to show (``relations`` and
    # ``hidden_links`` in meta.subgraph).  Archived units never show, but their
    # rows stay so restoring brings them back.  Moving members is a layout
    # change, not an activity entry; the revision token still covers it.

    def _topic_row(self, ref: int | str) -> sqlite3.Row:
        row = self._unit_row(ref)
        if row["type"] != "topic":
            raise StoreError(f"{row['uid']} is not a topic")
        return row

    @staticmethod
    def _subgraph(row: sqlite3.Row) -> dict:
        raw = _parse_detail(row["meta_json"]).get("subgraph")
        raw = raw if isinstance(raw, dict) else {}
        relations = raw.get("relations")
        return {
            "rule": raw.get("rule") if isinstance(raw.get("rule"), dict) else None,
            "relations": [r for r in relations if r in RELATIONS] if isinstance(relations, list) else None,
            "hidden_links": sorted({int(i) for i in raw.get("hidden_links") or [] if str(i).isdigit()}),
        }

    @staticmethod
    def _clean_rule(rule: Any) -> dict | None:
        """A topic rule: the subset of unit filters that says *what* (no paging/sort)."""
        if rule in (None, False) or rule == {}:
            return None
        if not isinstance(rule, dict):
            raise StoreError("rule must be an object")

        def words(key: str) -> list[str]:
            value = rule.get(key) or []
            if isinstance(value, str):
                value = value.split(",")
            if not isinstance(value, list):
                raise StoreError(f"rule.{key} must be a list")
            return [str(v).strip()[:120] for v in value if str(v).strip()][:60]

        clean: dict = {}
        q = str(rule.get("q") or "").strip()[:200]
        if q:
            clean["q"] = q
        for key in ("types", "statuses", "importance", "tags", "tags_unset", "origins", "assignees"):
            values = words(key)
            if values:
                clean[key] = values
        unknown = [v for v in clean.get("types", []) if v not in KNOWN_TYPES]
        if unknown:
            raise StoreError(f"unknown type in rule: {unknown[0]}")
        if rule.get("roots"):
            clean["roots"] = True
        for key in ("created_since", "updated_since", "start_from", "start_to"):
            value = str(rule.get(key) or "").strip()[:40]
            if value:
                clean[key] = value
        return clean or None

    def _rule_ids(self, rule: dict | None, exclude_id: int) -> list[int]:
        if not rule:
            return []
        where, args = self._where({**rule, "me": ""})
        # Reading order for rule matches: a seq:N tag first, then creation order.
        ids = [r["id"] for r in self.con.execute(
            f"SELECT u.id FROM units u WHERE {where} AND u.id<>? "
            "ORDER BY u.created_at ASC, u.id ASC LIMIT ?",
            args + [exclude_id, MAX_TOPIC_MEMBERS])]
        seq: dict[int, int] = {}
        for offset in range(0, len(ids), 400):
            batch = ids[offset:offset + 400]
            marks = ",".join("?" * len(batch))
            for row in self.con.execute(
                    f"SELECT unit_id, tag FROM unit_tags WHERE unit_id IN ({marks}) AND tag LIKE 'seq:%'", batch):
                value = row["tag"][4:]
                if value.isdigit():
                    seq[row["unit_id"]] = min(int(value), seq.get(row["unit_id"], int(value)))
        index = {unit_id: n for n, unit_id in enumerate(ids)}
        return sorted(ids, key=lambda i: (i not in seq, seq.get(i, 0), index[i]))

    def _member_tree(self, resolved: list[tuple]) -> list[dict]:
        """The topic's outline: members in pre-order with parent, depth and path.

        A member's parent is the one set in the topic (``parent_id``; 0 means
        an explicit top-level item), otherwise its ``part_of`` target when
        that is a member too.  Siblings keep the narrative order; a cycle is
        cut by making the member top-level.
        """
        order = {m[0]: n for n, m in enumerate(resolved)}
        auto: dict[int, int] = {}
        if order:
            ids = list(order)
            for offset in range(0, len(ids), 400):
                batch = ids[offset:offset + 400]
                marks = ",".join("?" * len(batch))
                all_marks = ",".join("?" * len(ids))
                for row in self.con.execute(
                        f"""SELECT source_id, target_id FROM unit_links WHERE relation='part_of'
                            AND source_id IN ({marks}) AND target_id IN ({all_marks}) ORDER BY id""",
                        batch + ids):
                    auto.setdefault(row["source_id"], row["target_id"])
        parent: dict[int, int | None] = {}
        source: dict[int, str | None] = {}
        for unit_id, mrow, _src in resolved:
            explicit = mrow["parent_id"] if mrow is not None and "parent_id" in mrow.keys() else None
            if explicit == 0:
                parent[unit_id], source[unit_id] = None, "topic"
            elif explicit is not None and explicit in order and explicit != unit_id:
                parent[unit_id], source[unit_id] = explicit, "topic"
            elif auto.get(unit_id) in order and auto.get(unit_id) != unit_id:
                parent[unit_id], source[unit_id] = auto[unit_id], "link"
            else:
                parent[unit_id], source[unit_id] = None, None
        for unit_id in order:
            seen = {unit_id}
            cursor = parent[unit_id]
            while cursor is not None:
                if cursor in seen:
                    parent[unit_id], source[unit_id] = None, None
                    break
                seen.add(cursor)
                cursor = parent[cursor]
        children: dict[int | None, list[int]] = {}
        for unit_id in sorted(order, key=order.get):
            children.setdefault(parent[unit_id], []).append(unit_id)
        out: list[dict] = []

        def walk(node: int | None, depth: int, prefix: str) -> None:
            for n, child in enumerate(children.get(node, []), start=1):
                path = f"{prefix}{n}"
                out.append({"id": child, "parent": parent[child], "parent_source": source[child],
                            "depth": depth, "path": path,
                            "children": len(children.get(child, []))})
                walk(child, depth + 1, f"{path}.")

        walk(None, 0, "")
        return out

    def _resolve_members(self, topic: sqlite3.Row) -> tuple[list[tuple], list[int]]:
        """``([(unit_id, row|None, "manual"|"rule")] in narrative order, excluded ids)``."""
        settings = self._subgraph(topic)
        rows = self.con.execute(
            """SELECT tm.* FROM topic_members tm JOIN units u ON u.id=tm.unit_id
               WHERE tm.topic_id=? AND u.archived=0
               ORDER BY tm.position, tm.added_at, tm.unit_id""", (topic["id"],)).fetchall()
        excluded = [r["unit_id"] for r in rows if r["mode"] == "exclude"]
        has_row = {r["unit_id"] for r in rows}
        matched = [i for i in self._rule_ids(settings["rule"], topic["id"]) if i not in excluded]
        matched_set = set(matched)
        members: list[tuple] = []
        for r in rows:
            if r["mode"] == "include":
                members.append((r["unit_id"], r, "manual"))
            elif r["mode"] == "layout" and r["unit_id"] in matched_set:
                members.append((r["unit_id"], r, "rule"))
        members.extend((i, None, "rule") for i in matched if i not in has_row)
        return members, excluded

    def _member_counts(self, ids: list[int]) -> dict[int, int]:
        out: dict[int, int] = {}
        for topic_id in ids:
            row = self.con.execute("SELECT * FROM units WHERE id=?", (topic_id,)).fetchone()
            if row is not None:
                out[topic_id] = len(self._resolve_members(row)[0])
        return out

    def topics_of(self, unit_id: int) -> list[dict]:
        """Topics (not archived) that currently show ``unit_id``."""
        out = []
        for row in self.con.execute(
                "SELECT * FROM units WHERE archived=0 AND type='topic' AND id<>? ORDER BY title",
                (unit_id,)).fetchall():
            if any(member[0] == unit_id for member in self._resolve_members(row)[0]):
                out.append({"id": row["id"], "uid": row["uid"], "title": row["title"], "type": row["type"]})
        return out

    def _topic_edges(self, ids: list[int], settings: dict) -> list[dict]:
        if not ids:
            return []
        marks = ",".join("?" * len(ids))
        hidden_ids = set(settings["hidden_links"])
        relations = settings["relations"]
        edges = []
        for row in self.con.execute(
                f"""SELECT id, source_id AS source, target_id AS target, relation, note FROM unit_links
                    WHERE source_id IN ({marks}) AND target_id IN ({marks}) ORDER BY id""",
                list(ids) + list(ids)):
            edge = dict(row)
            edge["hidden"] = edge["id"] in hidden_ids or (
                relations is not None and edge["relation"] not in relations)
            edges.append(edge)
        return edges

    def topic(self, ref: int | str, *, full: bool = False) -> dict:
        """A topic: visible members as an outline (pre-order, with depth and
        path), the links between them (each flagged ``hidden`` when the topic
        does not show it), excluded units and the topic's rule/link settings.
        ``full`` includes member bodies (for the article view)."""
        with self.lock:
            row = self._topic_row(ref)
            topic = self.hydrate([row])[0]
            settings = self._subgraph(row)
            resolved, excluded = self._resolve_members(row)
            ids = [m[0] for m in resolved]
            units: dict[int, dict] = {}
            for offset in range(0, len(ids), 400):
                batch = ids[offset:offset + 400]
                marks = ",".join("?" * len(batch))
                rows = self.con.execute(f"SELECT * FROM units WHERE id IN ({marks})", batch).fetchall()
                units.update({u["id"]: u for u in self.hydrate(rows, include_body=full)})
            by_id = {m[0]: m for m in resolved}
            members = []
            for step, item in enumerate(self._member_tree(resolved), start=1):
                _unit_id, mrow, source = by_id[item["id"]]
                unit = units[item["id"]]
                unit["member"] = {
                    "step": step, "path": item["path"], "depth": item["depth"],
                    "parent": item["parent"], "parent_source": item["parent_source"],
                    "children": item["children"], "source": source,
                    "x": mrow["x"] if mrow else None, "y": mrow["y"] if mrow else None,
                    "w": mrow["w"] if mrow else None,
                    "caption": mrow["caption"] if mrow else "",
                }
                members.append(unit)
            ids = [unit["id"] for unit in members]
            edges = self._topic_edges(ids, settings)
            degree: dict[int, int] = {}
            for edge in edges:
                if not edge["hidden"]:
                    degree[edge["source"]] = degree.get(edge["source"], 0) + 1
                    degree[edge["target"]] = degree.get(edge["target"], 0) + 1
            for unit in members:
                unit["degree"] = degree.get(unit["id"], 0)
            excluded_units = []
            if excluded:
                marks = ",".join("?" * len(excluded))
                excluded_units = [_unit_brief(r) for r in self.con.execute(
                    f"SELECT * FROM units WHERE id IN ({marks}) ORDER BY title", excluded)]
            archived = self.con.execute(
                """SELECT COUNT(*) FROM topic_members tm JOIN units u ON u.id=tm.unit_id
                   WHERE tm.topic_id=? AND tm.mode='include' AND u.archived=1""",
                (row["id"],)).fetchone()[0]
            return {"topic": topic, "members": members, "edges": edges, "excluded": excluded_units,
                    "archived": archived, "subgraph": settings}

    def topics(self) -> dict:
        """Every topic with a small preview of its visible subgraph (for cards)."""
        with self.lock:
            rows = self.con.execute(
                "SELECT * FROM units WHERE archived=0 AND type='topic' ORDER BY updated_at DESC, id DESC"
            ).fetchall()
            items = self.hydrate(rows, include_body=False)
            rank = {key: n for n, (key, _t, _ty) in enumerate(self.DEFAULT_TOPICS)}
            pairs = sorted(zip(rows, items), key=lambda pair: rank.get(pair[1]["meta"].get("default"), 99))
            rows, items = [p[0] for p in pairs], [p[1] for p in pairs]
            for row, item in zip(rows, items):
                item["default"] = item["meta"].get("default") if item["meta"].get("default") in rank else None
                settings = self._subgraph(row)
                resolved = self._resolve_members(row)[0][:60]
                ids = [m[0] for m in resolved]
                briefs = {}
                if ids:
                    marks = ",".join("?" * len(ids))
                    briefs = {r["id"]: r for r in self.con.execute(
                        f"SELECT id, uid, title, type FROM units WHERE id IN ({marks})", ids)}
                item["preview"] = {
                    "members": [{"id": i, "uid": briefs[i]["uid"], "title": briefs[i]["title"],
                                 "type": briefs[i]["type"], "x": m["x"] if m else None,
                                 "y": m["y"] if m else None}
                                for i, m, _source in resolved if i in briefs],
                    "edges": [[e["source"], e["target"]] for e in self._topic_edges(ids, settings)
                              if not e["hidden"]][:200],
                    "rule": settings["rule"] is not None,
                }
            return {"items": items, "total": len(items)}

    @staticmethod
    def _member_items(items: Any, key: str = "members") -> list[dict]:
        if items is None:
            return []
        if not isinstance(items, list):
            raise StoreError(f"{key} must be a list")
        out = []
        for item in items:
            if isinstance(item, dict):
                out.append({**item, "unit": item.get("unit", item.get("id", item.get("uid")))})
            else:
                out.append({"unit": item})
        return out

    @staticmethod
    def _card_width(value: Any) -> float | None:
        """A node-graph card width in canvas px (null = automatic)."""
        if value is None or value == "":
            return None
        if isinstance(value, bool):
            raise StoreError("member w must be a number")
        try:
            number = float(value)
        except (TypeError, ValueError) as exc:
            raise StoreError("member w must be a number") from exc
        if number != number or not MIN_CARD_W <= number <= MAX_CARD_W:
            raise StoreError(f"member w must be between {MIN_CARD_W} and {MAX_CARD_W}")
        return round(number, 1)

    @staticmethod
    def _coord(value: Any) -> float | None:
        if value is None or value == "":
            return None
        try:
            number = float(value)
        except (TypeError, ValueError) as exc:
            raise StoreError("member x/y must be numbers") from exc
        if number != number or abs(number) > 1e7:
            raise StoreError("member x/y is out of range")
        return round(number, 2)

    def _check_topic_design(self, data: dict) -> None:
        """Validate the members/design given with a new topic (refs exist and are live)."""
        design = data.get("design")
        if design is not None and not isinstance(design, dict):
            raise StoreError("topic design must be an object")
        design = design or {}
        refs = self._member_items(data.get("members") or design.get("add"), "add")
        for key in ("remove", "exclude", "restore", "items"):
            refs += self._member_items(design.get(key), key)
        order = design.get("order")
        if order is not None and not isinstance(order, list):
            raise StoreError("order must be a list")
        refs += [{"unit": item} for item in order or []]
        refs += [{"unit": item["parent"]} for item in self._member_items(design.get("items"), "items")
                 if item.get("parent") not in (None, "", "auto")]
        with self.lock:
            for entry in refs:
                if entry.get("unit") in (None, ""):
                    raise StoreError("member unit is required")
                row = self._unit_row(entry["unit"])
                if row["archived"]:
                    raise StoreError(f"{row['uid']} is archived")
        if "rule" in design:
            self._clean_rule(design.get("rule"))

    def design_topic(self, ref: int | str, body: dict | None, actor: str = "human:ui") -> dict:
        """Change a topic's subgraph in one call (the UI and agents send the same shape).

        ``add``      units to include: ``[id | uid | {unit, x?, y?, caption?}]``
        ``remove``   units to drop (a rule match becomes ``exclude``)
        ``exclude``  units the rule must never show; ``restore`` undoes that
        ``items``    ``[{unit, x?, y?, caption?}]`` presentation of members
        ``order``    unit refs in narrative order (members not listed follow)
        ``rule``     unit filter (q, types, statuses, importance, tags,
                     tags_unset, origins, assignees, roots, created_since,
                     updated_since, start_from, start_to) or null to clear
        ``relations``     relation types to show, or null for all
        ``hidden_links``  link ids to hide (replaces the set); ``hide_links``
                          / ``show_links`` change it incrementally
        """
        body = body or {}
        if not isinstance(body, dict):
            raise StoreError("topic design must be an object")
        add = self._member_items(body.get("add"), "add")
        remove = self._member_items(body.get("remove"), "remove")
        exclude = self._member_items(body.get("exclude"), "exclude")
        restore = self._member_items(body.get("restore"), "restore")
        items = self._member_items(body.get("items"), "items")
        order = body.get("order")
        if order is not None and not isinstance(order, list):
            raise StoreError("order must be a list")
        report: dict = {"added": [], "skipped": [], "removed": [], "excluded": [], "restored": [],
                        "updated": 0, "reordered": False, "settings": False, "outline": False}
        changes: dict = {}
        with self.lock, self.con:
            topic = self._topic_row(ref)
            now = db.utc_now()
            settings = self._subgraph(topic)
            new_settings = dict(settings)
            if "rule" in body:
                new_settings["rule"] = self._clean_rule(body.get("rule"))
            if "relations" in body:
                relations = body.get("relations")
                if relations is not None:
                    if not isinstance(relations, list):
                        raise StoreError("relations must be a list or null")
                    unknown = [r for r in relations if r not in RELATIONS]
                    if unknown:
                        raise StoreError(f"unknown relation: {unknown[0]!r}")
                    relations = list(dict.fromkeys(relations))
                new_settings["relations"] = relations
            hidden = set(settings["hidden_links"])
            if "hidden_links" in body:
                if not isinstance(body.get("hidden_links") or [], list):
                    raise StoreError("hidden_links must be a list")
                hidden = {int(i) for i in body.get("hidden_links") or [] if str(i).isdigit()}
            for key, show in (("hide_links", False), ("show_links", True)):
                values = body.get(key) or []
                if not isinstance(values, list):
                    raise StoreError(f"{key} must be a list")
                for value in values:
                    if str(value).isdigit():
                        (hidden.discard if show else hidden.add)(int(value))
            new_settings["hidden_links"] = sorted(hidden)
            if new_settings != settings:
                meta = _parse_detail(topic["meta_json"])
                meta["subgraph"] = new_settings
                self.con.execute("UPDATE units SET meta_json=?, updated_at=? WHERE id=?",
                                 (json.dumps(meta, ensure_ascii=False), now, topic["id"]))
                topic = self._unit_row(topic["id"])
                report["settings"] = True
                changes["settings"] = [k for k in new_settings if new_settings[k] != settings[k]]

            rows = {r["unit_id"]: r for r in self.con.execute(
                "SELECT * FROM topic_members WHERE topic_id=?", (topic["id"],))}
            position = max([r["position"] for r in rows.values()], default=-1)

            def unit_of(entry: dict) -> sqlite3.Row:
                unit = self._unit_row(entry["unit"])
                if unit["id"] == topic["id"]:
                    raise StoreError("a topic cannot contain itself")
                return unit

            def put(unit_id: int, mode: str, entry: dict | None = None) -> None:
                nonlocal position
                entry = entry or {}
                existing = rows.get(unit_id)
                if existing is None:
                    position += 1
                    self.con.execute(
                        """INSERT INTO topic_members(topic_id,unit_id,mode,position,x,y,caption,
                           added_at,added_by,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?)""",
                        (topic["id"], unit_id, mode, position, self._coord(entry.get("x")),
                         self._coord(entry.get("y")),
                         _clean_text(entry.get("caption"), "caption", max_len=MAX_CAPTION),
                         now, actor, now))
                elif existing["mode"] != mode:
                    self.con.execute(
                        "UPDATE topic_members SET mode=?, updated_at=? WHERE topic_id=? AND unit_id=?",
                        (mode, now, topic["id"], unit_id))
                rows[unit_id] = self.con.execute(
                    "SELECT * FROM topic_members WHERE topic_id=? AND unit_id=?",
                    (topic["id"], unit_id)).fetchone()

            included = sum(1 for r in rows.values() if r["mode"] == "include")
            for entry in add:
                unit = unit_of(entry)
                if unit["archived"]:
                    raise StoreError(f"{unit['uid']} is archived")
                if unit["id"] in rows and rows[unit["id"]]["mode"] == "include":
                    report["skipped"].append(unit["uid"])
                    continue
                if included >= MAX_TOPIC_MEMBERS:
                    raise StoreError(f"a topic holds at most {MAX_TOPIC_MEMBERS} members")
                put(unit["id"], "include", entry)
                included += 1
                report["added"].append(unit["uid"])

            matched = set(self._rule_ids(new_settings["rule"], topic["id"]))
            for entry in remove:
                unit = unit_of(entry)
                row = rows.get(unit["id"])
                if unit["id"] in matched:
                    put(unit["id"], "exclude")
                elif row is not None and row["mode"] != "exclude":
                    self.con.execute("DELETE FROM topic_members WHERE topic_id=? AND unit_id=?",
                                     (topic["id"], unit["id"]))
                    rows.pop(unit["id"])
                else:
                    raise StoreError(f"{unit['uid']} is not in {topic['uid']}")
                report["removed"].append(unit["uid"])
            for entry in exclude:
                unit = unit_of(entry)
                put(unit["id"], "exclude")
                report["excluded"].append(unit["uid"])
            for entry in restore:
                unit = unit_of(entry)
                row = rows.get(unit["id"])
                if row is not None and row["mode"] == "exclude":
                    self.con.execute("DELETE FROM topic_members WHERE topic_id=? AND unit_id=?",
                                     (topic["id"], unit["id"]))
                    rows.pop(unit["id"])
                    report["restored"].append(unit["uid"])

            visible = {m[0] for m in self._resolve_members(topic)[0]}
            captions: list[str] = []
            for entry in items:
                unit = unit_of(entry)
                if unit["id"] not in visible:
                    raise StoreError(f"{unit['uid']} is not in {topic['uid']}")
                if unit["id"] not in rows:
                    put(unit["id"], "layout")
                fields, args = [], []
                for key in ("x", "y"):
                    if key in entry:
                        fields.append(f"{key}=?")
                        args.append(self._coord(entry.get(key)))
                if "w" in entry:
                    fields.append("w=?")
                    args.append(self._card_width(entry.get("w")))
                if "caption" in entry:
                    caption = _clean_text(entry.get("caption"), "caption", max_len=MAX_CAPTION)
                    if caption != rows[unit["id"]]["caption"]:
                        captions.append(unit["uid"])
                    fields.append("caption=?")
                    args.append(caption)
                if "parent" in entry:
                    # Outline parent inside this topic: a member, null/0 for
                    # top level, "auto" to follow the member's part_of link.
                    value = entry.get("parent")
                    if value == "auto":
                        parent_id = None
                    elif value in (None, 0, "0", ""):
                        parent_id = 0
                    else:
                        parent = self._unit_row(value)
                        if parent["id"] not in visible or parent["id"] == unit["id"]:
                            raise StoreError(f"{parent['uid']} is not in {topic['uid']}")
                        parent_id = parent["id"]
                    if parent_id != rows[unit["id"]]["parent_id"]:
                        report["outline"] = True
                    fields.append("parent_id=?")
                    args.append(parent_id)
                if fields:
                    self.con.execute(
                        f"UPDATE topic_members SET {', '.join(fields)}, updated_at=? "
                        "WHERE topic_id=? AND unit_id=?", (*args, now, topic["id"], unit["id"]))
                    report["updated"] += 1

            if order is not None:
                current = [m[0] for m in self._resolve_members(topic)[0]]
                wanted: list[int] = []
                for item in order:
                    unit = self._unit_row(item)
                    if unit["id"] in current and unit["id"] not in wanted:
                        wanted.append(unit["id"])
                final = wanted + [i for i in current if i not in wanted]
                if final != current:
                    report["reordered"] = True
                    for index, unit_id in enumerate(final):
                        if unit_id not in rows:
                            put(unit_id, "layout")
                        self.con.execute(
                            "UPDATE topic_members SET position=?, updated_at=? "
                            "WHERE topic_id=? AND unit_id=?", (index, now, topic["id"], unit_id))
            for key in ("added", "removed", "excluded", "restored"):
                if report[key]:
                    changes[key] = report[key]
            if captions:
                changes["captions"] = captions
            if report["reordered"]:
                changes["reordered"] = True
            if report["outline"]:
                changes["outline"] = True
            if changes:
                self.log_activity(actor, "topic.update", topic["id"],
                                  {"uid": topic["uid"], "title": topic["title"], **changes}, commit=False)
        self._after_write()
        return report

    DEFAULT_TOPICS = (
        ("story", "Story", "story"),
        ("related", "Related work", "related_work"),
        ("method", "Method", "method"),
        ("experiment", "Experiment", "experiment"),
    )
    DEFAULT_TOPICS_FLAG = "topics.defaults_v1"

    def ensure_default_topics(self) -> list[dict]:
        """Create the built-in topics (Story, Related work, Method, Experiment)
        once per project.  Each is an ordinary topic whose rule selects one
        type; archiving one does not bring it back."""
        with self.lock:
            if self.meta(self.DEFAULT_TOPICS_FLAG) == "1":
                return []
            present = {(_parse_detail(r["meta_json"]).get("default") or "") for r in self.con.execute(
                "SELECT meta_json FROM units WHERE type='topic'")}
        created = []
        for key, title, type_name in self.DEFAULT_TOPICS:
            if key in present:
                continue
            created.append(self.create_unit({
                "type": "topic", "title": title, "status": "active", "importance": "p1",
                "origin": "system", "assignee": None, "meta": {"default": key},
                "design": {"rule": {"types": [type_name]}},
            }, actor="system:ark"))
        self.set_meta(self.DEFAULT_TOPICS_FLAG, "1")
        return created

    def add_topic_members(self, ref: int | str, items: Any, actor: str = "human:ui") -> dict:
        report = self.design_topic(ref, {"add": items}, actor)
        return {"added": report["added"], "skipped": report["skipped"]}

    def remove_topic_member(self, ref: int | str, unit_ref: int | str, actor: str = "human:ui") -> dict:
        self.design_topic(ref, {"remove": [unit_ref]}, actor)
        return {"removed": self._unit_row(unit_ref)["uid"]}

    def add_media(self, ref: int | str, url: str, kind: str = "link", caption: str = "",
                  actor: str = "human:ui") -> dict:
        url = _clean_text(url, "url", max_len=MAX_URL, required=True)
        if not re.match(r"^(https?://|file://|\.{0,2}/|[A-Za-z]:[\\/])", url):
            # Bare relative paths stay project-local. Other schemes (zotero://,
            # obsidian://, lark://, ...) are stored as external handlers.
            if "://" in url:
                scheme = url.split("://", 1)[0].lower()
                if scheme in {"javascript", "data", "vbscript"} or not re.match(r"^[a-z][a-z0-9+.-]*$", scheme):
                    raise StoreError("media URL scheme is not allowed")
            # Bare relative paths are stored project-relative.
        kind = _clean_text(kind or "link", "kind", max_len=40)
        caption = _clean_text(caption, "caption", max_len=300)
        with self.lock, self.con:
            row = self._unit_row(ref)
            cur = self.con.execute(
                "INSERT INTO unit_media(unit_id,url,kind,caption,created_at) VALUES(?,?,?,?,?)",
                (row["id"], url, kind, caption, db.utc_now()),
            )
            media_id = cur.lastrowid
            self.log_activity(actor, "media.create", row["id"],
                              {"uid": row["uid"], "url": url, "kind": kind}, commit=False)
        self._after_write()
        return {"id": media_id, "url": url, "kind": kind, "caption": caption}

    def update_media(self, media_id: int, patch: dict, actor: str = "human:ui") -> dict:
        """Edit an attachment's caption (and optionally kind); the URL is fixed."""
        with self.lock, self.con:
            row = self.con.execute("SELECT * FROM unit_media WHERE id=?", (int(media_id),)).fetchone()
            if not row:
                raise StoreError(f"media not found: {media_id}")
            caption = row["caption"]
            kind = row["kind"]
            if "caption" in patch:
                caption = _clean_text(patch.get("caption"), "caption", max_len=300).strip()
            if "kind" in patch:
                kind = _clean_text(patch.get("kind") or "link", "kind", max_len=40)
            self.con.execute("UPDATE unit_media SET caption=?, kind=? WHERE id=?", (caption, kind, row["id"]))
            self.log_activity(actor, "media.update", row["unit_id"],
                              {"url": row["url"], "caption": caption}, commit=False)
        self._after_write()
        return {"id": row["id"], "url": row["url"], "kind": kind, "caption": caption}

    def delete_media(self, media_id: int, actor: str = "human:ui") -> dict:
        with self.lock, self.con:
            row = self.con.execute("SELECT * FROM unit_media WHERE id=?", (int(media_id),)).fetchone()
            if not row:
                raise StoreError(f"media not found: {media_id}")
            self.con.execute("DELETE FROM unit_media WHERE id=?", (row["id"],))
            self.log_activity(actor, "media.delete", row["unit_id"], {"url": row["url"]}, commit=False)
        self._after_write()
        return {"id": row["id"], "deleted": True}

    def media_row(self, media_id: int) -> sqlite3.Row | None:
        with self.lock:
            return self.con.execute("SELECT * FROM unit_media WHERE id=?", (int(media_id),)).fetchone()

    # ------------------------------------------------------------------ comments
    # A comment is its own row on a unit (db v4), so an agent rewriting the
    # body cannot lose it.  Comments are never edited or deleted; resolving is
    # reversible.  ``author`` is the person's username (or "user") for user
    # kinds and "agent:<name>" for agent replies.
    @staticmethod
    def _comment_dict(row: sqlite3.Row) -> dict:
        return {
            "id": row["id"],
            "unit_id": row["unit_id"],
            "author": row["author"],
            "author_kind": "agent" if row["kind"] == "agent_reply" else "user",
            "kind": row["kind"],
            "body": row["body"],
            "created_at": row["created_at"],
            "reply_to": row["reply_to"],
            "resolved": bool(row["resolved_at"]),
            "resolved_at": row["resolved_at"],
            "resolved_by": row["resolved_by"],
        }

    def _comments_of(self, unit_id: int) -> list[dict]:
        rows = self.con.execute(
            "SELECT * FROM unit_comments WHERE unit_id=? ORDER BY created_at, id", (unit_id,)
        ).fetchall()
        return [self._comment_dict(row) for row in rows]

    def _comment_counts(self, ids: list[int]) -> dict[int, tuple[int, int]]:
        """``{unit_id: (user comments, unresolved user comments)}``."""
        if not ids:
            return {}
        marks = ",".join("?" * len(ids))
        kinds = ",".join("?" * len(COMMENT_USER_KINDS))
        rows = self.con.execute(
            f"""SELECT unit_id, COUNT(*) n, SUM(CASE WHEN resolved_at IS NULL THEN 1 ELSE 0 END) open
                FROM unit_comments WHERE unit_id IN ({marks}) AND kind IN ({kinds})
                GROUP BY unit_id""",
            [*ids, *COMMENT_USER_KINDS],
        ).fetchall()
        return {row["unit_id"]: (row["n"], int(row["open"] or 0)) for row in rows}

    def comments(self, ref: int | str) -> list[dict]:
        with self.lock:
            return self._comments_of(self._unit_row(ref)["id"])

    def add_comment(self, ref: int | str, body: Any, kind: str = "comment", *,
                    reply_to: Any = None, author: str = "user", actor: str = "human:ui") -> dict:
        """Add one comment row.  Agents (``actor`` "agent:...") may only reply."""
        text = _clean_text(body, "comment", max_len=MAX_COMMENT).strip()
        if not text:
            raise StoreError("comment text is required")
        kind = str(kind or "comment").strip()
        if kind not in COMMENT_KINDS:
            raise StoreError(f"comment kind must be one of {', '.join(COMMENT_KINDS)}")
        is_agent = str(actor).startswith("agent:")
        if is_agent and kind != "agent_reply":
            raise StoreError("agents can only add agent_reply comments")
        if not is_agent and kind == "agent_reply":
            raise StoreError("agent_reply comments come from agents")
        if is_agent:
            author = actor
        author = _clean_text(author or "user", "author", max_len=80).strip() or "user"
        with self.lock, self.con:
            row = self._unit_row(ref)
            parent = None
            if reply_to not in (None, ""):
                try:
                    parent_id = int(reply_to)
                except (TypeError, ValueError):
                    raise StoreError("reply_to must be a comment id") from None
                parent = self.con.execute(
                    "SELECT * FROM unit_comments WHERE id=? AND unit_id=?", (parent_id, row["id"])
                ).fetchone()
                if not parent:
                    raise StoreError(f"comment not found on this unit: {parent_id}")
            cur = self.con.execute(
                "INSERT INTO unit_comments(unit_id,author,kind,body,created_at,reply_to) VALUES(?,?,?,?,?,?)",
                (row["id"], author, kind, text, db.utc_now(), parent["id"] if parent else None),
            )
            comment_id = cur.lastrowid
            self.log_activity(actor, "comment.create", row["id"], {
                "uid": row["uid"], "title": row["title"], "comment_id": comment_id, "kind": kind,
                "reply_to": parent["id"] if parent else None, "excerpt": excerpt(text, 120),
            }, commit=False)
            created = self.con.execute("SELECT * FROM unit_comments WHERE id=?", (comment_id,)).fetchone()
        self._after_write()
        return self._comment_dict(created)

    def resolve_comment(self, ref: int | str, comment_id: Any, resolved: bool = True, *,
                        author: str = "user", actor: str = "human:ui") -> dict:
        """Mark a comment resolved, or reopen it with ``resolved=False``."""
        try:
            comment_id = int(comment_id)
        except (TypeError, ValueError):
            raise StoreError("comment id must be an integer") from None
        by = actor if str(actor).startswith("agent:") else (author or "user")
        with self.lock, self.con:
            row = self._unit_row(ref)
            comment = self.con.execute(
                "SELECT * FROM unit_comments WHERE id=? AND unit_id=?", (comment_id, row["id"])
            ).fetchone()
            if not comment:
                raise StoreError(f"comment not found on this unit: {comment_id}")
            if bool(comment["resolved_at"]) != bool(resolved):
                self.con.execute(
                    "UPDATE unit_comments SET resolved_at=?, resolved_by=? WHERE id=?",
                    ((db.utc_now(), by) if resolved else (None, None)) + (comment_id,),
                )
                self.log_activity(actor, "comment.resolve" if resolved else "comment.reopen", row["id"], {
                    "uid": row["uid"], "title": row["title"], "comment_id": comment_id,
                    "kind": comment["kind"], "excerpt": excerpt(comment["body"], 120),
                }, commit=False)
            fresh = self.con.execute("SELECT * FROM unit_comments WHERE id=?", (comment_id,)).fetchone()
        self._after_write()
        return self._comment_dict(fresh)

    # ------------------------------------------------------------------ activity
    def log_activity(self, actor: str, action: str, unit_id: int | None, detail: dict,
                     *, commit: bool = True) -> None:
        at = db.utc_now()
        payload = json.dumps(detail, ensure_ascii=False)
        self.con.execute(
            "INSERT INTO activity(at,actor,action,unit_id,detail_json) VALUES(?,?,?,?,?)",
            (at, actor, action, unit_id, payload),
        )
        try:
            with self.log_path.open("a", encoding="utf-8") as handle:
                handle.write(json.dumps({"at": at, "actor": actor, "action": action,
                                         "unit_id": unit_id, "detail": detail}, ensure_ascii=False) + "\n")
        except OSError:
            pass
        if commit:
            self.con.commit()

    def activity(self, limit: int = 40, unit_id: int | None = None) -> list[dict]:
        with self.lock:
            if unit_id is None:
                rows = self.con.execute(
                    """SELECT a.*, u.uid, u.title, u.type FROM activity a
                       LEFT JOIN units u ON u.id=a.unit_id ORDER BY a.id DESC LIMIT ?""",
                    (max(1, min(int(limit), 400)),),
                ).fetchall()
            else:
                rows = self.con.execute(
                    """SELECT a.*, u.uid, u.title, u.type FROM activity a
                       LEFT JOIN units u ON u.id=a.unit_id WHERE a.unit_id=?
                       ORDER BY a.id DESC LIMIT ?""",
                    (int(unit_id), max(1, min(int(limit), 400))),
                ).fetchall()
        return [
            {
                "id": row["id"],
                "at": row["at"],
                "actor": row["actor"],
                "action": row["action"],
                "unit_id": row["unit_id"],
                "unit_uid": row["uid"],
                "unit_title": row["title"],
                "unit_type": row["type"],
                "detail": _parse_detail(row["detail_json"]),
            }
            for row in rows
        ]

    # --------------------------------------------------------------------- chat
    # Assistant chat history is per user and project.  ``user=None`` reads
    # every message (maintenance/tests); with a user, the thread is that
    # user's messages plus shared rows written before history became per
    # user, minus whatever that user cleared.
    @staticmethod
    def _chat_cleared_key(user: str) -> str:
        return f"chat_cleared:{user}"

    def chat_history(self, limit: int = 100, user: str | None = None) -> list[dict]:
        limit = max(1, min(int(limit), 400))
        with self.lock:
            if user is None:
                rows = self.con.execute(
                    "SELECT * FROM (SELECT * FROM chat_messages ORDER BY id DESC LIMIT ?) ORDER BY id ASC",
                    (limit,),
                ).fetchall()
            else:
                cleared = int(db.meta_get(self.con, self._chat_cleared_key(user)) or 0)
                rows = self.con.execute(
                    "SELECT * FROM (SELECT * FROM chat_messages WHERE (user=? OR user='') AND id>? "
                    "ORDER BY id DESC LIMIT ?) ORDER BY id ASC",
                    (str(user), cleared, limit),
                ).fetchall()
        return [self._chat_row(row) for row in rows]

    @staticmethod
    def _chat_row(row: sqlite3.Row) -> dict:
        try:
            tools = json.loads(row["tools_json"] or "[]")
        except ValueError:
            tools = []
        try:
            meta = json.loads(row["meta_json"] or "{}")
        except ValueError:
            meta = {}
        return {
            "id": row["id"],
            "at": row["at"],
            "role": row["role"],
            "content": row["content"],
            "tools": tools if isinstance(tools, list) else [],
            "meta": meta if isinstance(meta, dict) else {},
        }

    def add_chat(self, role: str, content: str, tools: list | None = None,
                 meta: dict | None = None, user: str = "") -> dict:
        with self.lock:
            cur = self.con.execute(
                "INSERT INTO chat_messages(at,role,content,tools_json,meta_json,user) VALUES(?,?,?,?,?,?)",
                (db.utc_now(), role, str(content), json.dumps(tools or [], ensure_ascii=False),
                 json.dumps(meta or {}, ensure_ascii=False), str(user or "")),
            )
            self.con.commit()
            row = self.con.execute("SELECT * FROM chat_messages WHERE id=?", (cur.lastrowid,)).fetchone()
        return self._chat_row(row)

    def clear_chat(self, user: str | None = None) -> int:
        """Clear chat history; returns how many messages left the view.

        With ``user`` only that user's thread is cleared: their own rows are
        deleted and older shared rows are hidden for them (other users keep
        everything).  ``user=None`` deletes every message.
        """
        with self.lock:
            if user is None:
                n = self.con.execute("SELECT COUNT(*) n FROM chat_messages").fetchone()["n"]
                self.con.execute("DELETE FROM chat_messages")
                self.con.commit()
                return n
            user = str(user)
            cleared = int(db.meta_get(self.con, self._chat_cleared_key(user)) or 0)
            n = self.con.execute(
                "SELECT COUNT(*) n FROM chat_messages WHERE (user=? OR user='') AND id>?",
                (user, cleared)).fetchone()["n"]
            last = self.con.execute("SELECT COALESCE(MAX(id), 0) m FROM chat_messages").fetchone()["m"]
            self.con.execute("DELETE FROM chat_messages WHERE user=?", (user,))
            db.meta_set(self.con, self._chat_cleared_key(user), str(last))
            self.con.commit()
        return n

    def _after_write(self) -> None:
        self.con.commit()
        self.touch_manifest()
        self._writes_since_prune += 1
        if self._writes_since_prune >= 500:
            self._writes_since_prune = 0
            try:
                self.prune_change_log()
            except sqlite3.Error:
                pass

    # -------------------------------------------------------------------- stats
    def revision(self) -> str:
        """Cheap cross-client change token for remote-project polling.

        Opaque to clients: compare for equality only.  The last field is the
        trigger-maintained change counter (db.REVISION_TABLES), which changes
        on every committed row write; the content fields before it keep the
        token meaningful for databases restored from a copy.
        """
        with self.lock:
            row = self.con.execute(
                "SELECT (SELECT COUNT(*) FROM units) units, "
                "(SELECT COALESCE(MAX(updated_at),'') FROM units) unit_time, "
                "(SELECT COUNT(*) FROM unit_links) links, "
                "(SELECT COALESCE(MAX(id),0) FROM unit_links) link_id, "
                "(SELECT COUNT(*) FROM unit_media) media, "
                "(SELECT COUNT(*) FROM unit_comments WHERE resolved_at IS NULL) comments_open, "
                "(SELECT COALESCE(MAX(id),0) FROM activity) activity, "
                "(SELECT COUNT(*) FROM topic_members) members, "
                "(SELECT COALESCE(MAX(updated_at),'') FROM topic_members) member_time, "
                "(SELECT COALESCE(MAX(n),0) FROM change_counter) changes"
            ).fetchone()
            settings_time = 0
            try:
                settings_time = (self.path / "project.json").stat().st_mtime_ns
            except OSError:
                pass
            return (f"{row['units']}:{row['unit_time']}:{row['links']}:{row['link_id']}:"
                    f"{row['media']}:{row['comments_open']}:{row['activity']}:{settings_time}:"
                    f"{row['members']}:{row['member_time']}:{row['changes']}")

    def stats(self) -> dict:
        with self.lock:
            by_type = {row["type"]: row["n"] for row in self.con.execute(
                "SELECT type, COUNT(*) n FROM units WHERE archived=0 GROUP BY type"
            )}
            by_status = {row["status"]: row["n"] for row in self.con.execute(
                "SELECT status, COUNT(*) n FROM units WHERE archived=0 GROUP BY status"
            )}
            by_importance = {row["importance"]: row["n"] for row in self.con.execute(
                "SELECT importance, COUNT(*) n FROM units WHERE archived=0 GROUP BY importance"
            )}
            links = self.con.execute("SELECT COUNT(*) n FROM unit_links").fetchone()["n"]
            media = self.con.execute("SELECT COUNT(*) n FROM unit_media").fetchone()["n"]
            total = sum(by_type.values())
            today = db.today()
            overdue = self.con.execute(
                """SELECT COUNT(*) n FROM units WHERE archived=0 AND type='task'
                   AND status NOT IN ('done','abandoned','archived') AND end_at IS NOT NULL AND end_at < ?""",
                (today,),
            ).fetchone()["n"]
            task_by_status = {row["status"]: row["n"] for row in self.con.execute(
                "SELECT status, COUNT(*) n FROM units WHERE archived=0 AND type='task' GROUP BY status"
            )}
            task_total = sum(task_by_status.values())
            task_open = sum(task_by_status.get(s, 0) for s in ("inbox", "open", "active", "review"))
            stage_counts = self.con.execute(
                """SELECT u.type, CASE WHEN u.status!='done' AND EXISTS
                       (SELECT 1 FROM unit_tags t WHERE t.unit_id=u.id AND t.tag='status:blocked')
                       THEN 'blocked' ELSE u.status END state, COUNT(*) n
                   FROM units u WHERE u.archived=0 AND u.status NOT IN ('abandoned','archived')
                   GROUP BY u.type, state"""
            ).fetchall()
        stage_map = {
            "capture": ("idea", "source"),
            "plan": ("story", "related_work", "method", "experiment"),
            "evidence": ("question", "claim", "evidence"),
            "execute": ("task", "milestone", "run"),
            "make": ("production", "artifact"),
            "share": ("outreach",),
        }
        stages = []
        for key, types in stage_map.items():
            stage = {"id": key, "types": list(types), "count": 0, "done": 0,
                     "active": 0, "review": 0, "blocked": 0, "open": 0}
            for row in stage_counts:
                if row["type"] not in types:
                    continue
                stage["count"] += row["n"]
                if row["state"] in ("done", "active", "review", "blocked"):
                    stage[row["state"]] += row["n"]
                if row["state"] != "done":
                    stage["open"] += row["n"]
            stages.append(stage)
        return {
            "total": total,
            "by_type": by_type,
            "by_status": by_status,
            "by_importance": by_importance,
            "links": links,
            "media": media,
            "overdue": overdue,
            "tasks": {"total": task_total, "open": task_open, "by_status": task_by_status},
            "stages": stages,
        }

    def project_payload(self) -> dict:
        stats = self.stats()
        return {
            "manifest": self.manifest,
            "path": str(self.path),
            "stats": stats,
            "activity": self.activity(limit=12),
            "next_actions": self.next_actions(),
            "needs_you": self.needs_you(),
            "updated_at": self.manifest.get("updated") if self.manifest else None,
        }

    def next_actions(self) -> list[dict]:
        today = db.today()
        actions: list[dict] = []
        with self.lock:
            overdue = self.con.execute(
                """SELECT * FROM units WHERE archived=0 AND type='task'
                   AND status NOT IN ('done','abandoned','archived') AND end_at IS NOT NULL AND end_at < ?
                   ORDER BY end_at ASC LIMIT 3""", (today,)
            ).fetchall()
            blocked = self.con.execute(
                "SELECT * FROM units WHERE archived=0 AND (status='blocked' OR EXISTS "
                "(SELECT 1 FROM unit_tags t WHERE t.unit_id=units.id AND t.tag='status:blocked')) "
                "ORDER BY updated_at ASC LIMIT 2"
            ).fetchall()
            orphan_ideas = self.con.execute(
                """SELECT u.* FROM units u WHERE u.archived=0 AND u.type='idea'
                   AND NOT EXISTS (SELECT 1 FROM unit_links l WHERE l.source_id=u.id OR l.target_id=u.id)
                   ORDER BY u.updated_at DESC LIMIT 2"""
            ).fetchall()
            claims_no_evidence = self.con.execute(
                """SELECT u.* FROM units u WHERE u.archived=0 AND u.type='claim'
                   AND NOT EXISTS (SELECT 1 FROM unit_links l JOIN units s ON s.id=l.source_id
                                   WHERE l.target_id=u.id AND l.relation IN ('supports','validates'))
                   ORDER BY u.updated_at DESC LIMIT 2"""
            ).fetchall()
            active_no_agent = self.con.execute(
                """SELECT u.* FROM units u WHERE u.archived=0 AND u.type='task'
                   AND u.status IN ('open','active','review')
                   AND NOT EXISTS (SELECT 1 FROM unit_links l WHERE l.source_id=u.id AND l.relation='assigned_to')
                   ORDER BY u.end_at ASC LIMIT 2"""
            ).fetchall()

        for row in overdue:
            actions.append({
                "kind": "overdue", "severity": "high", "unit": _unit_brief(row),
                "title": row["title"], "detail": f"Due {row['end_at']}",
                "target": {"tab": "tasks", "unit_id": row["id"]},
            })
        for row in blocked:
            actions.append({
                "kind": "blocked", "severity": "high", "unit": _unit_brief(row),
                "title": row["title"], "detail": "Marked blocked",
                "target": {"tab": "map", "unit_id": row["id"]},
            })
        for row in claims_no_evidence:
            actions.append({
                "kind": "claim", "severity": "medium", "unit": _unit_brief(row),
                "title": row["title"], "detail": "No supporting evidence linked yet",
                "target": {"tab": "map", "unit_id": row["id"]},
            })
        for row in active_no_agent:
            actions.append({
                "kind": "assign", "severity": "medium", "unit": _unit_brief(row),
                "title": row["title"], "detail": "Active task without an agent",
                "target": {"tab": "tasks", "unit_id": row["id"]},
            })
        for row in orphan_ideas:
            actions.append({
                "kind": "idea", "severity": "low", "unit": _unit_brief(row),
                "title": row["title"], "detail": "Idea has no connection yet",
                "target": {"tab": "ideas", "unit_id": row["id"]},
            })
        # A blocked + overdue task is one problem, not two.
        unique, seen = [], set()
        for action in actions:
            uid = action.get("unit", {}).get("id")
            if uid in seen:
                continue
            seen.add(uid)
            unique.append(action)
        return unique[:8]

    def needs_you(self, *, media_limit: int = NEEDS_MEDIA_LIMIT) -> list[dict]:
        """Units waiting on the human: ``review`` units and open ``decision`` units.

        ``review`` means "finished, waiting for the user to accept"; a decision
        stays open until it is ``done``.  Each item carries a Markdown excerpt
        and up to ``media_limit`` image/video previews, taken from the unit or,
        when it has none, from directly linked units (newest first).  A
        borrowed preview says where it comes from (``from``) and ``own_media``
        is then 0, so the UI never presents linked media as the node's own.
        """
        with self.lock:
            rows = self.con.execute(
                """SELECT * FROM units WHERE archived=0
                   AND (status='review' OR (type='decision' AND status!='done'))
                   ORDER BY CASE WHEN type='decision' THEN 0 ELSE 1 END, updated_at DESC, id DESC"""
            ).fetchall()
            items = []
            owners = self._assignee_index() if rows else {}
            counts = self._comment_counts([row["id"] for row in rows])
            previews = self._preview_media_batch([row["id"] for row in rows], media_limit)
            for row in rows:
                text, truncated = markdown_excerpt(row["body"])
                eff_name, eff_from = owners.get(row["id"], (None, None))
                media, own_media = previews[row["id"]]
                items.append({
                    **_unit_brief(row),
                    "updated_at": row["updated_at"],
                    "kind": "decision" if row["type"] == "decision" else "review",
                    "excerpt": text,
                    "excerpt_plain": excerpt(_NOTE_LINE.sub("", row["body"] or ""), 320),
                    "excerpt_truncated": truncated,
                    # User comment rows (comment / sent back / decided / accepted)
                    # and how many are still unresolved.
                    "comments": counts.get(row["id"], (0, 0))[0],
                    "comments_open": counts.get(row["id"], (0, 0))[1],
                    "media": media,
                    # Image/video attachments on the node itself.  0 means every
                    # preview in ``media`` is borrowed (each carries ``from``).
                    "own_media": own_media,
                    "assignee": row["assignee"],
                    "assignee_effective": row["assignee"] or eff_name,
                    "assignee_from": None if row["assignee"] or not eff_from else {"id": eff_from},
                })
            sources = sorted({item["assignee_from"]["id"] for item in items if item["assignee_from"]})
            if sources:
                marks = ",".join("?" * len(sources))
                briefs = {r["id"]: {"id": r["id"], "uid": r["uid"], "title": r["title"], "type": r["type"]}
                          for r in self.con.execute(
                              f"SELECT id, uid, title, type FROM units WHERE id IN ({marks})", sources)}
                for item in items:
                    if item["assignee_from"]:
                        item["assignee_from"] = briefs.get(item["assignee_from"]["id"])
        return items

    def _preview_media_batch(self, ids: list[int], limit: int) -> dict[int, tuple[list[dict], int]]:
        """Prefetch own/adjacent media once per batch instead of once per item."""
        media = {}
        def load_media(unit_ids):
            for start in range(0, len(unit_ids), 400):
                batch = unit_ids[start:start + 400]
                marks = ",".join("?" * len(batch))
                for row in self.con.execute(
                    f"SELECT * FROM unit_media WHERE unit_id IN ({marks}) ORDER BY created_at DESC,id DESC", batch
                ):
                    media.setdefault(row["unit_id"], []).append(row)
        load_media(ids)
        missing = [uid for uid in ids if not any(media_preview_kind(dict(row)) for row in media.get(uid, ()))]
        neighbours = {uid: [] for uid in missing}
        in_marks = ",".join("?" * len(_EVIDENCE_IN))
        out_marks = ",".join("?" * len(_EVIDENCE_OUT))
        skip_marks = ",".join("?" * len(_NO_PREVIEW_RELATIONS))
        for start in range(0, len(missing), 400):
            batch = missing[start:start + 400]
            marks = ",".join("?" * len(batch))
            for row in self.con.execute(
                f"""SELECT unit_id,id,uid,title,type,updated_at,MIN(rank) rank FROM (
                    SELECT l.target_id unit_id,u.id,u.uid,u.title,u.type,u.updated_at,
                           CASE WHEN l.relation IN ({in_marks}) THEN 0 ELSE 1 END rank
                    FROM unit_links l JOIN units u ON u.id=l.source_id
                    WHERE l.target_id IN ({marks}) AND u.archived=0 AND u.id!=l.target_id
                      AND l.relation NOT IN ({skip_marks})
                    UNION ALL
                    SELECT l.source_id unit_id,u.id,u.uid,u.title,u.type,u.updated_at,
                           CASE WHEN l.relation IN ({out_marks}) THEN 0 ELSE 1 END rank
                    FROM unit_links l JOIN units u ON u.id=l.target_id
                    WHERE l.source_id IN ({marks}) AND u.archived=0 AND u.id!=l.source_id
                      AND l.relation NOT IN ({skip_marks})
                ) GROUP BY unit_id,id ORDER BY rank,updated_at DESC,id DESC""",
                [*_EVIDENCE_IN, *batch, *_NO_PREVIEW_RELATIONS,
                 *_EVIDENCE_OUT, *batch, *_NO_PREVIEW_RELATIONS],
            ):
                neighbours[row["unit_id"]].append(row)
        adjacent_ids = sorted({row["id"] for items in neighbours.values() for row in items} - set(ids))
        load_media(adjacent_ids)
        return {uid: self._preview_media(uid, limit, with_own_count=True,
                                        media_index=media, neighbour_index=neighbours) for uid in ids}

    def _preview_media(self, unit_id: int, limit: int, *, with_own_count: bool = False,
                       media_index=None, neighbour_index=None):
        """Image/video previews for a unit, falling back to directly linked units.

        Borrowed items carry ``from`` = the linked unit.  With
        ``with_own_count`` the result is ``(previews, own previewable count)``.
        """
        def usable(rows, source=None):
            out = []
            for media_row in rows:
                item = self._media_dict(media_row)
                kind = media_preview_kind(item)
                if not kind:
                    continue
                item["preview_kind"] = kind
                item["thumb_url"] = item["raw_url"] if kind == "image" else None
                if source is not None:
                    item["from"] = {"id": source["id"], "uid": source["uid"],
                                    "title": source["title"], "type": source["type"]}
                out.append(item)
            return out

        own_rows = media_index.get(unit_id, ()) if media_index is not None else self.con.execute(
            "SELECT * FROM unit_media WHERE unit_id=? ORDER BY created_at DESC, id DESC", (unit_id,)
        ).fetchall()
        own = _unique_previews(usable(own_rows))
        if own:
            return (own[:limit], len(own)) if with_own_count else own[:limit]
        in_marks = ",".join("?" * len(_EVIDENCE_IN))
        out_marks = ",".join("?" * len(_EVIDENCE_OUT))
        skip_marks = ",".join("?" * len(_NO_PREVIEW_RELATIONS))
        neighbours = neighbour_index.get(unit_id, ()) if neighbour_index is not None else self.con.execute(
            f"""SELECT u.*, MIN(CASE
                    WHEN l.target_id=? AND l.relation IN ({in_marks}) THEN 0
                    WHEN l.source_id=? AND l.relation IN ({out_marks}) THEN 0
                    ELSE 1 END) AS rank
                FROM unit_links l
                JOIN units u ON u.id = CASE WHEN l.source_id=? THEN l.target_id ELSE l.source_id END
                WHERE (l.source_id=? OR l.target_id=?) AND u.id!=? AND u.archived=0
                  AND l.relation NOT IN ({skip_marks})
                  AND EXISTS (SELECT 1 FROM unit_media m WHERE m.unit_id=u.id)
                GROUP BY u.id
                ORDER BY rank, u.updated_at DESC, u.id DESC""",
            [unit_id, *_EVIDENCE_IN, unit_id, *_EVIDENCE_OUT, unit_id, unit_id, unit_id, unit_id,
             *_NO_PREVIEW_RELATIONS],
        ).fetchall()
        found: list[dict] = []
        for neighbour in neighbours:
            rows = sorted(media_index.get(neighbour["id"], ()), key=lambda row: row["id"], reverse=True) if media_index is not None else self.con.execute(
                "SELECT * FROM unit_media WHERE unit_id=? ORDER BY id DESC", (neighbour["id"],)
            ).fetchall()
            found.extend(usable(rows, neighbour))
            if len(found) >= limit * 4:
                break
        # Newest first across every linked node: a reviewer wants the latest
        # result, not whichever neighbour happens to rank first.
        found.sort(key=lambda item: (item.get("created_at") or "", item["id"]), reverse=True)
        found = _unique_previews(found)
        return (found[:limit], 0) if with_own_count else found[:limit]

    # ------------------------------------------------------------------ helpers
    def unit_count(self, type_name: str | None = None) -> int:
        with self.lock:
            if type_name:
                row = self.con.execute("SELECT COUNT(*) n FROM units WHERE archived=0 AND type=?",
                                       (type_name,)).fetchone()
            else:
                row = self.con.execute("SELECT COUNT(*) n FROM units WHERE archived=0").fetchone()
            return row["n"]

    def units_with_tags(self, type_name: str | None = None) -> list[dict]:
        with self.lock:
            if type_name:
                rows = self.con.execute(
                    "SELECT * FROM units WHERE archived=0 AND type=? ORDER BY updated_at DESC", (type_name,)
                ).fetchall()
            else:
                rows = self.con.execute(
                    "SELECT * FROM units WHERE archived=0 ORDER BY updated_at DESC"
                ).fetchall()
            return self.hydrate(rows)


def _chunks(items: list, size: int):
    for offset in range(0, len(items), size):
        yield items[offset:offset + size]
