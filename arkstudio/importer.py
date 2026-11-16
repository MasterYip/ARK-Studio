"""Read-only InfoHub import.

InfoHub owns its database.  ARK opens it read-only, copies the *structure* that
is useful for research (units, tags, parent links), and keeps a durable source
key so every imported idea can point back to the exact InfoHub unit.  Import is
idempotent on ``source_ref``.
"""
from __future__ import annotations

import os
import sqlite3
import uuid
from datetime import datetime, timezone
from pathlib import Path

from .store import Store, StoreError

KIND_TO_TYPE = {
    "topic": "idea",
    "idea": "idea",
    "task": "task",
    "todo": "task",
    "event": "milestone",
    "deadline": "milestone",
    "note": "note",
    "digest": "note",
    "announcement": "note",
    "question": "question",
    "decision": "decision",
    "paper": "related_work",
    "resource": "resource",
}

MAX_SAMPLE = 8


def _date_from_epoch(value) -> str | None:
    if value in (None, ""):
        return None
    try:
        stamp = int(value)
        if stamp > 10_000_000_000:
            stamp //= 1000
        return datetime.fromtimestamp(stamp, tz=timezone.utc).date().isoformat()
    except (ValueError, OSError, OverflowError):
        return None


def _short(text: str, n: int = 72) -> str:
    text = " ".join(str(text or "").split())
    return text if len(text) <= n else text[: n - 1] + "…"


class InfoHubSource:
    def __init__(self, path: str | Path):
        self.path = Path(path).expanduser().resolve()
        if not self.path.is_file():
            raise StoreError(f"InfoHub database not found: {self.path}")
        try:
            uri = self.path.as_uri() + "?mode=ro"
            self.con = sqlite3.connect(uri, uri=True, timeout=5)
            self.con.row_factory = sqlite3.Row
            self.con.execute("SELECT 1 FROM units LIMIT 1")
        except sqlite3.Error as exc:
            raise StoreError(f"not a readable InfoHub database: {exc}") from exc
        self.columns = {row["name"] for row in self.con.execute("PRAGMA table_info(units)")}

    def close(self) -> None:
        try:
            self.con.close()
        except sqlite3.Error:
            pass

    def summary(self) -> dict:
        units = self.con.execute("SELECT COUNT(*) n FROM units").fetchone()["n"]
        tags = self.con.execute("SELECT COUNT(*) n FROM unit_tags").fetchone()["n"]
        links = self.con.execute("SELECT COUNT(*) n FROM unit_links").fetchone()["n"]
        kinds = [
            {"kind": row["kind"] or "unknown", "count": row["n"]}
            for row in self.con.execute(
                "SELECT kind, COUNT(*) n FROM units GROUP BY kind ORDER BY n DESC"
            )
        ]
        sample = [
            {"key": row["key"], "kind": row["kind"], "title": row["title"],
             "summary": _short(row["summary"] if "summary" in self.columns else "", 160)}
            for row in self.con.execute(
                "SELECT key, kind, title, summary FROM units "
                "ORDER BY CASE WHEN kind='topic' THEN 0 ELSE 1 END, id LIMIT ?", (MAX_SAMPLE,)
            )
        ] if units else []
        return {
            "path": str(self.path),
            "exists": True,
            "units": units,
            "topics": next((k["count"] for k in kinds if k["kind"] == "topic"), 0),
            "tags": tags,
            "links": links,
            "kinds": kinds,
            "sample": sample,
            "mtime": datetime.fromtimestamp(self.path.stat().st_mtime, tz=timezone.utc).isoformat(timespec="seconds"),
        }

    KIND_COLORS = {
        "topic": "#3b82f6", "todo": "#ef4444", "event": "#a855f7",
        "decision": "#f59e0b", "question": "#eab308", "announcement": "#14b8a6",
        "resource": "#10b981", "digest": "#64748b", "note": "#94a3b8",
    }

    @staticmethod
    def _split(value) -> list[str]:
        if isinstance(value, (list, tuple)):
            return [str(item).strip() for item in value if str(item).strip()]
        return [item.strip() for item in str(value or "").split(",") if item.strip()]

    def _query_rows(self, *, q: str = "", kinds=(), tags=(), status: str = "",
                    days: int = 0, mine: bool = False, roots: bool = False,
                    order: str = "activity", limit: int = 60, offset: int = 0):
        clauses, args = [], []
        if kinds:
            clauses.append(f"kind IN ({','.join('?' * len(kinds))})")
            args.extend(kinds)
        if status and status != "all":
            clauses.append("status=?")
            args.append(status)
        elif "status" in self.columns:
            clauses.append("status != 'dropped'")
        if "for_me" in self.columns and mine:
            clauses.append("for_me=1")
        if "last_activity_at" in self.columns and days:
            clauses.append("COALESCE(last_activity_at, updated_at, 0) >= ?")
            args.append(int(__import__("time").time() - float(days) * 86400))
        if roots:
            clauses.append("id NOT IN (SELECT child_id FROM unit_links)")
        # The Explorer groups tags by namespace: OR inside a namespace, AND across.
        groups = {}
        for tag in tags:
            groups.setdefault(tag.split(":", 1)[0], []).append(tag)
        for group in groups.values():
            clauses.append(f"id IN (SELECT unit_id FROM unit_tags WHERE tag IN ({','.join('?' * len(group))}))")
            args.extend(group)
        for term in str(q or "").split():
            pattern = f"%{term}%"
            cols = ["title", "key"]
            if "summary" in self.columns:
                cols.append("summary")
            clauses.append("(" + " OR ".join(f"{col} LIKE ?" for col in cols) + ")")
            args.extend([pattern] * len(cols))
        where = ("WHERE " + " AND ".join(clauses)) if clauses else ""
        total = self.con.execute(f"SELECT COUNT(*) n FROM units {where}", args).fetchone()["n"]
        limit = max(1, min(int(limit or 60), 2000))
        offset = max(0, int(offset or 0))
        order_sql = {
            "activity": "COALESCE(last_activity_at, updated_at, 0) DESC, id DESC",
            "oldest": "COALESCE(last_activity_at, updated_at, 0) ASC, id ASC",
            "due": "COALESCE(due_at, start_at) IS NULL, COALESCE(due_at, start_at), id DESC",
            "title": "title COLLATE NOCASE ASC, id ASC",
            "created": "created_at DESC, id DESC",
        }.get(order, "COALESCE(updated_at, 0) DESC, id DESC")
        if "updated_at" not in self.columns:
            order_sql = "id DESC"
        rows = self.con.execute(f"SELECT * FROM units {where} ORDER BY {order_sql} LIMIT ? OFFSET ?",
                                args + [limit, offset]).fetchall()
        return total, rows

    def _items(self, rows, store: Store | None = None) -> list[dict]:
        imported = set()
        if store is not None:
            imported = {row["source_ref"] for row in store.con.execute(
                "SELECT source_ref FROM units WHERE source_ref LIKE 'infohub:%'"
            )}
        direct = {}
        try:
            direct = {row["unit_id"]: row["n"] for row in self.con.execute(
                "SELECT unit_id, COUNT(*) n FROM unit_messages GROUP BY unit_id"
            )}
        except sqlite3.Error:
            pass
        items = []
        for row in rows:
            key = row["key"] or f"id-{row['id']}"
            tags = self._tags_for(row["id"])
            items.append({
                "id": row["id"],
                "key": key,
                "kind": (row["kind"] or "unknown"),
                "title": row["title"] or "Untitled",
                "summary": _short(row["summary"] if "summary" in self.columns else "", 240),
                "status": row["status"] if "status" in self.columns else "",
                "priority": row["priority"] if "priority" in self.columns else "",
                "due_at": row["due_at"] if "due_at" in self.columns else None,
                "for_me": bool(row["for_me"]) if "for_me" in self.columns else False,
                "n_messages": direct.get(row["id"], 0),
                "tags": tags[:10],
                "source": next((tag.split(":", 1)[1] for tag in tags if tag.startswith("source:")), ""),
                "updated_at": row["updated_at"] if "updated_at" in self.columns else None,
                "imported": f"infohub:{key}" in imported,
            })
        return items

    def browse(self, store: Store | None = None, *, q: str = "", kind: str = "",
               tag: str = "", tags="", status: str = "", days: int = 0,
               mine: bool = False, roots: bool = False, order: str = "activity",
               limit: int = 60, offset: int = 0) -> dict:
        """One read-only filter contract shared by the list and graph panes."""
        kinds = self._split(kind)
        selected_tags = self._split(tags) or self._split(tag)
        total, rows = self._query_rows(q=q, kinds=kinds, tags=selected_tags, status=status,
                                       days=days, mine=mine, roots=roots, order=order,
                                       limit=limit, offset=offset)
        kinds_facet = [
            {"kind": row["kind"] or "unknown", "count": row["n"]}
            for row in self.con.execute("SELECT kind, COUNT(*) n FROM units WHERE status != 'dropped' GROUP BY kind ORDER BY n DESC")
        ]
        tag_facet = [
            {"tag": row["tag"], "count": row["n"]}
            for row in self.con.execute(
                "SELECT tag, COUNT(*) n FROM unit_tags GROUP BY tag ORDER BY n DESC"
            )
        ]
        time_cols = [col for col in ("last_activity_at", "updated_at", "created_at") if col in self.columns]
        oldest = None
        if time_cols:
            try:
                expression = "MIN(COALESCE(" + ",".join(time_cols) + "))"
                where = " WHERE status != 'dropped'" if "status" in self.columns else ""
                oldest = self.con.execute(f"SELECT {expression} value FROM units{where}").fetchone()["value"]
            except sqlite3.Error:
                oldest = None
        age_days = max(1.0, (__import__("time").time() - oldest) / 86400) if oldest else 365.0
        return {
            "path": str(self.path),
            "total": total,
            "items": self._items(rows, store),
            "kinds": kinds_facet,
            "tags": tag_facet,
            "max_days": round(min(3650.0, age_days), 1),
            "offset": max(0, int(offset or 0)),
            "limit": max(1, min(int(limit or 60), 2000)),
        }

    def graph(self, store: Store | None = None, *, q: str = "", kind: str = "",
              tag: str = "", tags="", status: str = "", days: int = 0,
              mine: bool = False, roots: bool = False, order: str = "activity",
              limit: int = 160, connected: bool = True, context: bool = False) -> dict:
        data = self.browse(store, q=q, kind=kind, tag=tag, tags=tags, status=status,
                           days=days, mine=mine, roots=roots, order=order, limit=limit)
        items = data["items"]
        by_key = {item["key"]: item for item in items}
        ids = {item["id"] for item in items}
        seed_ids = set(ids)
        if context and ids:
            marks = ",".join("?" * len(ids))
            neighbor_rows = self.con.execute(
                f"SELECT * FROM units WHERE status != 'dropped' AND id IN ("
                f"SELECT parent_id FROM unit_links WHERE child_id IN ({marks}) UNION "
                f"SELECT child_id FROM unit_links WHERE parent_id IN ({marks})) LIMIT ?",
                list(ids) + list(ids) + [int(limit)],
            ).fetchall()
            for item in self._items(neighbor_rows, store):
                if item["id"] not in ids:
                    item["seed"] = False
                    items.append(item)
                    ids.add(item["id"])
        for item in items:
            item.setdefault("seed", True)
            by_key[item["key"]] = item

        # Total messages include every descendant branch, matching the Explorer.
        direct, children = {}, {}
        try:
            direct = {row["unit_id"]: row["n"] for row in self.con.execute(
                "SELECT unit_id, COUNT(*) n FROM unit_messages GROUP BY unit_id"
            )}
        except sqlite3.Error:
            pass
        for row in self.con.execute("SELECT parent_id, child_id FROM unit_links"):
            children.setdefault(row["parent_id"], []).append(row["child_id"])
        memo = {}

        def message_total(unit_id, seen=None):
            if unit_id in memo:
                return memo[unit_id]
            seen = seen or set()
            if unit_id in seen:
                return 0
            seen.add(unit_id)
            total = direct.get(unit_id, 0)
            for child in children.get(unit_id, []):
                total += message_total(child, seen)
            memo[unit_id] = total
            return total

        max_messages = max([message_total(item["id"]) for item in items] or [1])
        nodes = []
        for item in items:
            kind_value = (item["kind"] or "unknown").lower()
            total_messages = message_total(item["id"])
            ratio = max(0, total_messages) / max(1, max_messages)
            nodes.append({
                "id": item["id"],
                "uid": item["key"],
                "key": item["key"],
                "kind": kind_value,
                "type": KIND_TO_TYPE.get(kind_value, "idea"),
                "title": item["title"],
                "status": item["status"] or "open",
                "importance": "p2",
                "tags": item["tags"],
                "summary": item["summary"],
                "imported": item["imported"],
                "for_me": item.get("for_me", False),
                "seed": item.get("seed", True),
                "n_messages": total_messages,
                "radius": 5 + 21 * (ratio ** 0.5),
                "color": self.KIND_COLORS.get(kind_value, "#94a3b8"),
                "source": item.get("source", ""),
                "due_at": item.get("due_at"),
                "degree": 0,
            })
        edges = []
        if ids:
            marks = ",".join("?" * len(ids))
            rows = self.con.execute(
                f"SELECT parent_id, child_id, relation FROM unit_links "
                f"WHERE parent_id IN ({marks}) AND child_id IN ({marks})",
                list(ids) + list(ids),
            ).fetchall()
            for index, row in enumerate(rows):
                relation = row["relation"] or "part_of"
                if relation == "part_of":
                    source, target = row["child_id"], row["parent_id"]
                else:
                    source, target = row["parent_id"], row["child_id"]
                if source not in ids or target not in ids or source == target:
                    continue
                edges.append({"id": index + 1, "source": source, "target": target, "relation": relation})
        if connected and edges:
            linked = {edge["source"] for edge in edges} | {edge["target"] for edge in edges}
            nodes = [node for node in nodes if node["id"] in linked]
            edges = [edge for edge in edges if edge["source"] in linked and edge["target"] in linked]
        degree = {}
        for edge in edges:
            degree[edge["source"]] = degree.get(edge["source"], 0) + 1
            degree[edge["target"]] = degree.get(edge["target"], 0) + 1
        for node in nodes:
            node["degree"] = degree.get(node["id"], 0)
        return {
            "path": data["path"],
            "total": data["total"],
            "matched_count": len(seed_ids),
            "context_count": len(nodes) - len(seed_ids & {node["id"] for node in nodes}),
            "nodes": nodes,
            "edges": edges,
            "kinds": data["kinds"],
            "tags": data["tags"],
            "max_days": data["max_days"],
        }


    @staticmethod
    def _iso(value):
        if value in (None, ""):
            return None
        try:
            return datetime.fromtimestamp(float(value), timezone.utc).isoformat(timespec="seconds")
        except (TypeError, ValueError, OSError):
            return None

    def unit(self, key: str, store: Store | None = None) -> dict:
        """Read-only InfoHub detail: full unit, all tags, messages and relations."""
        raw = str(key)
        numeric = raw[3:] if raw.startswith("id-") else raw
        row = self.con.execute("SELECT * FROM units WHERE key=? OR CAST(id AS TEXT)=? OR id=?",
                               (raw, numeric, numeric)).fetchone()
        if not row:
            raise StoreError(f"InfoHub unit not found: {key}")
        unit_id = row["id"]
        tags = self._tags_for(unit_id)
        messages = []
        try:
            for msg in self.con.execute(
                "SELECT m.* FROM unit_messages um JOIN messages m ON m.id=um.message_id "
                "WHERE um.unit_id=? ORDER BY COALESCE(m.sent_at,0) ASC, m.id ASC LIMIT 500",
                (unit_id,),
            ).fetchall():
                keys = msg.keys()
                messages.append({
                    "id": msg["id"],
                    "source": msg["source"] if "source" in keys else "",
                    "channel": msg["channel"] if "channel" in keys else "",
                    "channel_name": msg["channel_name"] if "channel_name" in keys else "",
                    "sender": msg["sender"] if "sender" in keys else "",
                    "sender_name": msg["sender_name"] if "sender_name" in keys else "",
                    "sent_at": msg["sent_at"] if "sent_at" in keys else None,
                    "sent_at_iso": self._iso(msg["sent_at"] if "sent_at" in keys else None),
                    "kind": msg["kind"] if "kind" in keys else "",
                    "text": (msg["text"] if "text" in keys else "") or "",
                    "link": msg["link"] if "link" in keys else "",
                    "mentions_me": bool(msg["mentions_me"]) if "mentions_me" in keys else False,
                    "from_me": bool(msg["from_me"]) if "from_me" in keys else False,
                    "priority": msg["priority"] if "priority" in keys else "",
                    "labels": msg["labels"] if "labels" in keys else "",
                })
        except sqlite3.Error:
            pass
        # Counts for the recursive InfoHub cards: direct messages plus all
        # descendants, and the number of immediate children.
        direct_counts, child_map = {}, {}
        try:
            direct_counts = {r["unit_id"]: r["n"] for r in self.con.execute(
                "SELECT unit_id, COUNT(*) n FROM unit_messages GROUP BY unit_id")}
            for r in self.con.execute("SELECT parent_id, child_id FROM unit_links"):
                child_map.setdefault(r["parent_id"], []).append(r["child_id"])
        except sqlite3.Error:
            pass
        memo = {}

        def message_total(uid, seen=None):
            if uid in memo:
                return memo[uid]
            seen = seen or set()
            if uid in seen:
                return 0
            seen.add(uid)
            total = direct_counts.get(uid, 0)
            for child in child_map.get(uid, []):
                total += message_total(child, seen)
            memo[uid] = total
            return total

        def child_info(r, relation):
            child_keys = r.keys()
            child_tags = self._tags_for(r["id"])
            return {
                "key": r["key"] or "id-" + str(r["id"]),
                "title": r["title"] or "Untitled",
                "kind": r["kind"] or "unknown",
                "status": r["status"] if "status" in child_keys else "",
                "relation": relation or "part_of",
                "summary": _short(r["summary"] if "summary" in child_keys else "", 280),
                "tags": child_tags,
                "n_children": len(child_map.get(r["id"], [])),
                "n_messages": direct_counts.get(r["id"], 0),
                "n_messages_total": message_total(r["id"]),
                "last_activity_at_iso": self._iso(r["last_activity_at"] if "last_activity_at" in child_keys else None),
                "updated_at_iso": self._iso(r["updated_at"] if "updated_at" in child_keys else None),
            }

        parents, children = [], []
        try:
            parents = [child_info(r, r["relation"]) for r in self.con.execute(
                "SELECT u.*, l.relation FROM unit_links l JOIN units u ON u.id=l.parent_id "
                "WHERE l.child_id=? ORDER BY u.title", (unit_id,))]
            children = [child_info(r, r["relation"]) for r in self.con.execute(
                "SELECT u.*, l.relation FROM unit_links l JOIN units u ON u.id=l.child_id "
                "WHERE l.parent_id=? ORDER BY u.title", (unit_id,))]
        except sqlite3.Error:
            pass
        keys = row.keys()
        source_key = row["key"] or "id-" + str(unit_id)
        source_ref = "infohub:" + source_key
        imported = False
        if store is not None:
            imported = store.con.execute("SELECT 1 FROM units WHERE source_ref=?", (source_ref,)).fetchone() is not None
        return {
            "id": unit_id, "key": source_key,
            "kind": row["kind"] or "unknown", "title": row["title"] or "Untitled",
            "summary": row["summary"] if "summary" in keys else "",
            "status": row["status"] if "status" in keys else "",
            "priority": row["priority"] if "priority" in keys else "",
            "origin": row["origin"] if "origin" in keys else "",
            "confidence": row["confidence"] if "confidence" in keys else None,
            "for_me": bool(row["for_me"]) if "for_me" in keys else False,
            "polished": bool(row["polished"]) if "polished" in keys else False,
            "due_at": row["due_at"] if "due_at" in keys else None,
            "start_at": row["start_at"] if "start_at" in keys else None,
            "end_at": row["end_at"] if "end_at" in keys else None,
            "created_at": row["created_at"] if "created_at" in keys else None,
            "updated_at": row["updated_at"] if "updated_at" in keys else None,
            "last_activity_at": row["last_activity_at"] if "last_activity_at" in keys else None,
            "created_at_iso": self._iso(row["created_at"] if "created_at" in keys else None),
            "updated_at_iso": self._iso(row["updated_at"] if "updated_at" in keys else None),
            "last_activity_at_iso": self._iso(row["last_activity_at"] if "last_activity_at" in keys else None),
            "due_at_iso": self._iso(row["due_at"] if "due_at" in keys else None),
            "tags": tags,
            "source": next((tag.split(":", 1)[1] for tag in tags if tag.startswith("source:")), ""),
            "messages": messages, "n_messages": len(messages),
            "n_messages_total": message_total(unit_id),
            "n_children": len(children),
            "parents": parents, "children": children,
            "imported": imported, "source_ref": source_ref,
        }

    def _import_body(self, row) -> str:
        summary = self._import_summary(row).strip()
        channel = row["channel_name"] if "channel_name" in self.columns else ""
        lines = []
        try:
            messages = self.con.execute(
                "SELECT m.* FROM unit_messages um JOIN messages m ON m.id=um.message_id "
                "WHERE um.unit_id=? ORDER BY COALESCE(m.sent_at,0) ASC, m.id ASC LIMIT 80",
                (row["id"],),
            ).fetchall()
        except sqlite3.Error:
            messages = []
        seen = set()
        for msg in messages:
            keys = msg.keys()
            text = " ".join(str(msg["text"] if "text" in keys else "").split())
            if not text or text in seen:
                continue
            seen.add(text)
            sender = (msg["sender_name"] if "sender_name" in keys else "") or (msg["sender"] if "sender" in keys else "") or "unknown"
            lines.append(f"- {sender}: {text[:180]}")
        # Keep the body useful but compact: summary plus a handful of plain-text
        # conversation lines. Full transcripts remain in InfoHub.
        tail = lines[-8:]
        parts = []
        if summary:
            parts.append(summary[:1200])
        if tail:
            parts.append("Conversation (condensed):\n" + "\n".join(tail))
        if channel:
            parts.append(f"InfoHub: {row['key'] if 'key' in row.keys() else row['id']} · {channel}")
        return "\n\n".join(parts)[:6000]

    def _import_summary(self, row) -> str:
        return (row["summary"] if "summary" in self.columns else "") or ""
    def import_key(self, store: Store, key: str, actor: str = "import:infohub",
                   as_idea: bool = False, copy: bool = False) -> dict:
        raw = str(key)
        numeric = raw[3:] if raw.startswith("id-") else raw
        row = self.con.execute(
            "SELECT * FROM units WHERE key=? OR CAST(id AS TEXT)=? OR id=?",
            (raw, numeric, numeric),
        ).fetchone()
        if not row:
            raise StoreError(f"InfoHub unit not found: {key}")
        source_key = row["key"] or f"id-{row['id']}"
        source_ref = f"infohub:{source_key}"
        existing = None if copy else store.con.execute(
            "SELECT id FROM units WHERE source_ref=?", (source_ref,)
        ).fetchone()
        if copy:
            source_ref = f"{source_ref}#copy-{uuid.uuid4().hex[:10]}"
        if existing:
            current = store.get_unit(existing["id"])
            patch = {}
            if as_idea and current.get("type") not in {"idea", "question", "source"}:
                patch["type"] = "idea"
            current_body = str(current.get("body") or "").strip()
            if not current_body or current_body.startswith("InfoHub source:"):
                body = self._import_body(row)
                if body:
                    patch["body"] = body
            if patch:
                try:
                    store.update_unit(existing["id"], patch, actor=actor)
                    current = store.get_unit(existing["id"])
                except StoreError:
                    pass
            return {"unit": current, "imported": False, "skipped": True}
        kind = (row["kind"] or "topic").lower()
        tags = self._tags_for(row["id"])
        tags.extend(["origin:infohub", "system:infohub", f"ih-kind:{_tag_value(kind)}"])
        start = _date_from_epoch(row["start_at"] if "start_at" in self.columns else None)
        due = _date_from_epoch(row["due_at"] if "due_at" in self.columns else None)
        end = _date_from_epoch(row["end_at"] if "end_at" in self.columns else None) or due
        body = self._import_body(row)
        channel = row["channel_name"] if "channel_name" in self.columns else ""
        mapped_type = "idea" if as_idea else KIND_TO_TYPE.get(kind, "idea")
        unit = store.create_unit({
            "type": mapped_type,
            "title": (row["title"] or "Untitled")[:240],
            "body": (body or "")[:200_000],
            "tags": tags,
            "status": "done" if (row["status"] or "") == "done" else "open",
            "importance": self._importance(row),
            "start_at": start,
            "end_at": end,
            "origin": "infohub",
            "source_ref": source_ref,
            "source_title": f"InfoHub · {channel}" if channel else "InfoHub",
            "meta": {"infohub": {"key": source_key, "kind": kind, "status": row["status"],
                                          "summary": self._import_summary(row)[:4000]}},
        }, actor=actor)
        # Keep a parent link when the parent has already been imported. The
        # explorer import remains single-node; this is provenance, not a bulk copy.
        if "parent_id" in self.columns and row["parent_id"]:
            parent = self.con.execute("SELECT key FROM units WHERE id=?", (row["parent_id"],)).fetchone()
            if parent:
                parent_ref = f"infohub:{parent['key']}"
                found = store.con.execute("SELECT id FROM units WHERE source_ref=?", (parent_ref,)).fetchone()
                if found:
                    try:
                        store.create_link(unit["id"], found["id"], "part_of", "Imported hierarchy", actor=actor)
                    except StoreError:
                        pass
        store.log_activity(actor, "import.infohub.unit", unit["id"],
                           {"path": str(self.path), "key": source_key, "kind": kind})
        return {"unit": unit, "imported": True, "skipped": False}

    # ------------------------------------------------------------------ import
    def import_into(self, store: Store, *, mode: str = "topics", update: bool = False,
                    actor: str = "import:infohub") -> dict:
        if mode not in {"topics", "all"}:
            raise StoreError("mode must be topics or all")
        where = "WHERE kind='topic'" if mode == "topics" else ""
        rows = self.con.execute(f"SELECT * FROM units {where} ORDER BY id").fetchall()
        imported, skipped, updated = 0, 0, 0
        old_to_new: dict[int, int] = {}

        for row in rows:
            old_id = row["id"]
            source_key = row["key"] or f"id-{old_id}"
            source_ref = f"infohub:{source_key}"
            existing = store.con.execute("SELECT id FROM units WHERE source_ref=?", (source_ref,)).fetchone()
            if existing:
                if update:
                    store.update_unit(
                        existing["id"],
                        {
                            "title": row["title"] or "Untitled",
                            "body": row["summary"] or "",
                            "status": "done" if row["status"] == "done" else "open",
                            "importance": self._importance(row),
                        },
                        actor=actor,
                    )
                    old_to_new[old_id] = existing["id"]
                    updated += 1
                else:
                    old_to_new[old_id] = existing["id"]
                    skipped += 1
                continue

            kind = (row["kind"] or "topic").lower()
            type_name = KIND_TO_TYPE.get(kind, "idea")
            tags = self._tags_for(old_id)
            tags.extend(["origin:infohub", "system:infohub", f"ih-kind:{_tag_value(kind)}"])
            status = "done" if (row["status"] or "") == "done" else "open"
            start = _date_from_epoch(row["start_at"] if "start_at" in self.columns else None)
            due = _date_from_epoch(row["due_at"] if "due_at" in self.columns else None)
            end = _date_from_epoch(row["end_at"] if "end_at" in self.columns else None) or due
            body = row["summary"] or ""
            channel = row["channel_name"] if "channel_name" in self.columns else ""
            if channel:
                body = f"{body}\n\nImported from InfoHub channel: {channel}".strip()
            try:
                unit = store.create_unit(
                    {
                        "type": type_name,
                        "title": (row["title"] or "Untitled")[:240],
                        "body": (body or "")[:200_000],
                        "tags": tags,
                        "status": status,
                        "importance": self._importance(row),
                        "start_at": start,
                        "end_at": end,
                        "origin": "infohub",
                        "source_ref": source_ref,
                        "source_title": f"InfoHub · {channel}" if channel else "InfoHub",
                        "meta": {"infohub": {"key": source_key, "kind": kind, "status": row["status"]}},
                    },
                    actor=actor,
                )
            except StoreError:
                # A malformed tag or date in one row must not abort the whole import.
                skipped += 1
                continue
            old_to_new[old_id] = unit["id"]
            imported += 1

        # Parent links and evidence counts are part of the imported structure.
        links = self.con.execute("SELECT * FROM unit_links").fetchall()
        link_count = 0
        for link in links:
            relation = link["relation"] or "part_of"
            if relation == "part_of":
                # InfoHub stores parent -> child; ARK stores child --part_of--> parent.
                source, target = old_to_new.get(link["child_id"]), old_to_new.get(link["parent_id"])
            else:
                source, target = old_to_new.get(link["parent_id"]), old_to_new.get(link["child_id"])
            if not source or not target or source == target:
                continue
            try:
                made = store.create_link(source, target, relation,
                                        "Imported from InfoHub", actor=actor)
                if not made.get("already"):
                    link_count += 1
            except StoreError:
                pass

        store.log_activity(
            actor, "import.infohub", None,
            {"path": str(self.path), "mode": mode, "imported": imported,
             "skipped": skipped, "updated": updated, "links": link_count},
        )
        return {
            "imported": imported,
            "skipped": skipped,
            "updated": updated,
            "links": link_count,
            "total": len(rows),
            "mode": mode,
        }

    def _importance(self, row: sqlite3.Row) -> str:
        value = (row["priority"] if "priority" in self.columns else "") or ""
        value = str(value).lower().strip()
        if value in {"p0", "p1", "p2", "p3"}:
            return value
        if value in {"high", "critical", "urgent"}:
            return "p0"
        if value in {"medium", "normal"}:
            return "p2"
        if value in {"low"}:
            return "p3"
        return "p2"

    def _tags_for(self, unit_id: int) -> list[str]:
        rows = self.con.execute("SELECT tag FROM unit_tags WHERE unit_id=?", (unit_id,)).fetchall()
        tags = []
        for row in rows:
            tag = str(row["tag"] or "").strip()
            if not tag or tag.split(":", 1)[0] in {"type", "status", "importance"}:
                continue
            tag = tag.replace(" ", "-")
            if ":" not in tag:
                tag = "tag:" + tag
            tags.append(tag[:96])
        return tags


def _tag_value(value: str) -> str:
    text = "".join(ch if ch.isalnum() or ch in "-_" else "-" for ch in str(value).lower())
    return text.strip("-")[:48] or "unknown"


def default_candidates() -> list[dict]:
    candidates = []
    env = os.environ.get("INFOHUB_DB")
    paths = []
    if env:
        paths.append(Path(env))
    roots = [
        Path.cwd(),
        Path.cwd() / "info-hub",
        Path.cwd().parent / "info-hub",
        Path.home() / "info-hub",
    ]
    for root in roots:
        paths.append(root / "data" / "hub.db")
        paths.append(root / "infohub.db")
    seen = set()
    for path in paths:
        try:
            key = str(path.resolve()).lower()
        except OSError:
            continue
        if key in seen:
            continue
        seen.add(key)
        if path.is_file():
            candidates.append({"path": str(path.resolve()), "size": path.stat().st_size})
    return candidates


def preview(path: str) -> dict:
    source = InfoHubSource(path)
    try:
        return source.summary()
    finally:
        source.close()
