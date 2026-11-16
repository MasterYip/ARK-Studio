"""SQLite storage for one ARK Studio project.

Text, tags, links and timestamps live in SQLite.  Binary media never enters the
database: a media row stores a URL (``http(s)://``, ``file://`` or a path relative
to the project folder) plus a kind and caption.
"""
from __future__ import annotations

import json
import re
import sqlite3
from datetime import datetime, timezone
from pathlib import Path

SCHEMA_VERSION = 12  # v12: topic_members.w (node-card width); v11: change_log triggers (v10: per-user chat_messages.user; v9: change_counter)

SCHEMA = """
CREATE TABLE IF NOT EXISTS meta(
  key TEXT PRIMARY KEY,
  value TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS units(
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  uid TEXT NOT NULL UNIQUE,
  type TEXT NOT NULL,
  title TEXT NOT NULL,
  body TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL DEFAULT 'open',
  importance TEXT NOT NULL DEFAULT 'p2',
  start_at TEXT,
  end_at TEXT,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  origin TEXT NOT NULL DEFAULT 'manual',
  source_ref TEXT,
  source_title TEXT,
  meta_json TEXT NOT NULL DEFAULT '{}',
  archived INTEGER NOT NULL DEFAULT 0,
  assignee TEXT
);
CREATE INDEX IF NOT EXISTS units_type ON units(type, archived);
CREATE INDEX IF NOT EXISTS units_status ON units(status, archived);
CREATE INDEX IF NOT EXISTS units_time ON units(start_at, end_at);
CREATE INDEX IF NOT EXISTS units_updated ON units(updated_at);
CREATE UNIQUE INDEX IF NOT EXISTS units_source_ref
  ON units(source_ref) WHERE source_ref IS NOT NULL;

CREATE TABLE IF NOT EXISTS unit_tags(
  unit_id INTEGER NOT NULL REFERENCES units(id) ON DELETE CASCADE,
  tag TEXT NOT NULL,
  PRIMARY KEY(unit_id, tag)
);
CREATE INDEX IF NOT EXISTS unit_tags_tag ON unit_tags(tag);

CREATE TABLE IF NOT EXISTS unit_links(
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  source_id INTEGER NOT NULL REFERENCES units(id) ON DELETE CASCADE,
  target_id INTEGER NOT NULL REFERENCES units(id) ON DELETE CASCADE,
  relation TEXT NOT NULL,
  note TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  created_by TEXT NOT NULL DEFAULT 'human',
  UNIQUE(source_id, target_id, relation)
);
CREATE INDEX IF NOT EXISTS links_source ON unit_links(source_id);
CREATE INDEX IF NOT EXISTS links_target ON unit_links(target_id);

CREATE TABLE IF NOT EXISTS unit_media(
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  unit_id INTEGER NOT NULL REFERENCES units(id) ON DELETE CASCADE,
  url TEXT NOT NULL,
  kind TEXT NOT NULL DEFAULT 'link',
  caption TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS media_unit ON unit_media(unit_id);

-- v4: comments are their own rows on a unit, not lines in its body, so they
-- survive body rewrites.  They are never deleted; resolving is reversible.
CREATE TABLE IF NOT EXISTS unit_comments(
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  unit_id INTEGER NOT NULL REFERENCES units(id) ON DELETE CASCADE,
  author TEXT NOT NULL,
  kind TEXT NOT NULL,
  body TEXT NOT NULL,
  created_at TEXT NOT NULL,
  reply_to INTEGER REFERENCES unit_comments(id),
  resolved_at TEXT,
  resolved_by TEXT
);
CREATE INDEX IF NOT EXISTS comments_unit ON unit_comments(unit_id, created_at);

CREATE TABLE IF NOT EXISTS activity(
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  at TEXT NOT NULL,
  actor TEXT NOT NULL,
  action TEXT NOT NULL,
  unit_id INTEGER,
  detail_json TEXT NOT NULL DEFAULT '{}'
);
CREATE INDEX IF NOT EXISTS activity_at ON activity(at);

CREATE TABLE IF NOT EXISTS chat_messages(
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  at TEXT NOT NULL,
  role TEXT NOT NULL,
  content TEXT NOT NULL,
  tools_json TEXT NOT NULL DEFAULT '[]',
  meta_json TEXT NOT NULL DEFAULT '{}',
  -- v10: the account that owns the message ('' = shared, written before
  -- chat history became per user).
  user TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS chat_at ON chat_messages(at);

-- v6: a topic unit holds a curated subgraph of other units (see
-- Store.design_topic).  include/exclude rows are explicit membership on top
-- of the topic's rule; every row also carries the member's narrative
-- position, outline parent (0 = top level, NULL = follow part_of), canvas x/y
-- and topic-local caption (layout rows only that).
CREATE TABLE IF NOT EXISTS topic_members(
  topic_id INTEGER NOT NULL REFERENCES units(id) ON DELETE CASCADE,
  unit_id INTEGER NOT NULL REFERENCES units(id) ON DELETE CASCADE,
  mode TEXT NOT NULL DEFAULT 'include' CHECK(mode IN ('include','layout','exclude')),
  position INTEGER NOT NULL DEFAULT 0,
  parent_id INTEGER,
  x REAL,
  y REAL,
  w REAL,
  caption TEXT NOT NULL DEFAULT '',
  added_at TEXT NOT NULL,
  added_by TEXT NOT NULL DEFAULT 'human',
  updated_at TEXT NOT NULL,
  PRIMARY KEY(topic_id, unit_id)
);
CREATE INDEX IF NOT EXISTS topic_members_unit ON topic_members(unit_id);

CREATE TABLE IF NOT EXISTS counters(
  prefix TEXT PRIMARY KEY,
  n INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS node_text_history(
  unit_id INTEGER PRIMARY KEY REFERENCES units(id) ON DELETE CASCADE,
  enabled INTEGER NOT NULL DEFAULT 0 CHECK(enabled IN (0,1)),
  head TEXT
);
CREATE TABLE IF NOT EXISTS node_text_pending(
  unit_id INTEGER PRIMARY KEY REFERENCES units(id) ON DELETE CASCADE,
  edited_at REAL NOT NULL,
  checked_at REAL NOT NULL DEFAULT 0,
  actor TEXT NOT NULL
);
"""

# v9, applied by connect() after migrate() so every table exists (tests build
# older databases from slices of SCHEMA, which must not carry these triggers).
REVISION_SCHEMA = """
-- change_counter.n goes up by one for every row inserted, updated or
-- deleted in a table that remote clients render.  Store.revision() includes
-- it, so a committed write always changes the revision even when it lands in
-- the same second as the previous one (updated_at has 1 s resolution) or only
-- moves a topic member.  The triggers live in the database file, so writes
-- from any process or ARK implementation count; a rolled-back transaction
-- rolls the counter back with it.
CREATE TABLE IF NOT EXISTS change_counter(
  id INTEGER PRIMARY KEY CHECK(id = 1),
  n INTEGER NOT NULL DEFAULT 0
);
INSERT OR IGNORE INTO change_counter(id, n) VALUES(1, 0);
"""
# Tables whose rows the web client shows.  Background bookkeeping (chat,
# counters, node_text_history/pending, activity, meta) is left out so the
# history worker does not make every client reload; activity has its own
# MAX(id) field in the revision.
REVISION_TABLES = ("units", "unit_tags", "unit_links", "unit_media", "unit_comments", "topic_members")
REVISION_SCHEMA += "".join(
    f"CREATE TRIGGER IF NOT EXISTS {table}_rev_{op[:3].lower()} AFTER {op} ON {table} "
    "BEGIN UPDATE change_counter SET n = n + 1 WHERE id = 1; END;\n"
    for table in REVISION_TABLES for op in ("INSERT", "UPDATE", "DELETE"))

# v11: change_log records *which* rendered rows changed, so a client holding a
# cached graph snapshot can ask for the changes since its cursor (GET
# /api/changes) instead of downloading the whole snapshot again.  One row per
# affected unit ('unit', unit id) or link ('link', link id); ``tree`` marks a
# change that can alter inherited ownership below that unit (its assignee, or
# a part_of link from it).  Like change_counter, the triggers live in the
# database file, so writes from any process or ARK implementation are logged.
# Store.prune_change_log keeps the newest CHANGE_LOG_KEEP rows; a cursor older
# than the oldest row gets a full reload.  meta.change_log_id names this log;
# it is regenerated when the database file is replaced (snapshot restore), so
# a cursor from another copy of the database is never mistaken for this one.
CHANGE_LOG_KEEP = 20000
CHANGE_LOG_SCHEMA = """
CREATE TABLE IF NOT EXISTS change_log(
  seq INTEGER PRIMARY KEY AUTOINCREMENT,
  kind TEXT NOT NULL,
  ref INTEGER NOT NULL,
  tree INTEGER NOT NULL DEFAULT 0
);
"""


def _log(kind: str, ref: str, tree: str = "0") -> str:
    return f"INSERT INTO change_log(kind,ref,tree) VALUES('{kind}',{ref},{tree});"


_CHANGE_TRIGGERS = {
    ("units", "INSERT"): _log("unit", "NEW.id"),
    ("units", "UPDATE"): _log("unit", "NEW.id", "(OLD.assignee IS NOT NEW.assignee)"),
    ("units", "DELETE"): _log("unit", "OLD.id"),
    ("unit_links", "INSERT"): (_log("link", "NEW.id")
                               + _log("unit", "NEW.source_id", "(NEW.relation='part_of')")
                               + _log("unit", "NEW.target_id")),
    ("unit_links", "UPDATE"): (_log("link", "NEW.id")
                               + _log("unit", "OLD.source_id",
                                      "(OLD.relation='part_of' OR NEW.relation='part_of')")
                               + _log("unit", "NEW.source_id",
                                      "(OLD.relation='part_of' OR NEW.relation='part_of')")
                               + _log("unit", "OLD.target_id") + _log("unit", "NEW.target_id")),
    ("unit_links", "DELETE"): (_log("link", "OLD.id")
                               + _log("unit", "OLD.source_id", "(OLD.relation='part_of')")
                               + _log("unit", "OLD.target_id")),
    ("topic_members", "INSERT"): _log("unit", "NEW.topic_id") + _log("unit", "NEW.unit_id"),
    ("topic_members", "UPDATE"): (_log("unit", "OLD.topic_id") + _log("unit", "OLD.unit_id")
                                  + _log("unit", "NEW.topic_id") + _log("unit", "NEW.unit_id")),
    ("topic_members", "DELETE"): _log("unit", "OLD.topic_id") + _log("unit", "OLD.unit_id"),
}
for _table in ("unit_tags", "unit_media", "unit_comments"):
    _CHANGE_TRIGGERS[(_table, "INSERT")] = _log("unit", "NEW.unit_id")
    _CHANGE_TRIGGERS[(_table, "UPDATE")] = _log("unit", "OLD.unit_id") + _log("unit", "NEW.unit_id")
    _CHANGE_TRIGGERS[(_table, "DELETE")] = _log("unit", "OLD.unit_id")
CHANGE_LOG_SCHEMA += "".join(
    f"CREATE TRIGGER IF NOT EXISTS {table}_log_{op[:3].lower()} AFTER {op} ON {table} "
    f"BEGIN {body} END;\n"
    for (table, op), body in _CHANGE_TRIGGERS.items())
CHANGE_LOG_ID = "change_log_id"


def utc_now() -> str:
    return datetime.now(timezone.utc).isoformat(timespec="seconds")


def today() -> str:
    return datetime.now(timezone.utc).date().isoformat()


def connect(path: str | Path) -> sqlite3.Connection:
    path = Path(path)
    path.parent.mkdir(parents=True, exist_ok=True)
    con = sqlite3.connect(str(path), check_same_thread=False, timeout=30)
    con.row_factory = sqlite3.Row
    con.execute("PRAGMA journal_mode=WAL")
    con.execute("PRAGMA synchronous=NORMAL")
    con.execute("PRAGMA foreign_keys=ON")
    con.executescript(SCHEMA)
    migrate(con)
    con.executescript(REVISION_SCHEMA)
    con.executescript(CHANGE_LOG_SCHEMA)
    if not meta_get(con, CHANGE_LOG_ID):
        new_change_log_id(con)
    con.execute(
        "INSERT INTO meta(key,value) VALUES('schema_version',?) "
        "ON CONFLICT(key) DO UPDATE SET value=excluded.value",
        (str(SCHEMA_VERSION),),
    )
    con.commit()
    return con


def migrate(con: sqlite3.Connection) -> None:
    """Bring an older project database up to the current schema in place.

    Idempotent: every step checks the live table shape first, so it is safe on
    new databases, on databases created by an older ARK and on demo template
    copies.  Schema steps only add columns or indexes.  Data steps (v4 notes,
    v5 dates) run once per database behind a meta flag, only fill empty
    values, log each change as ``system:migration`` and keep ``updated_at``.
    """
    history_columns = {row[1] for row in con.execute("PRAGMA table_info(node_text_pending)")}
    if "checked_at" not in history_columns:
        con.execute("ALTER TABLE node_text_pending ADD COLUMN checked_at REAL NOT NULL DEFAULT 0")
    columns = {row[1] for row in con.execute("PRAGMA table_info(units)")}
    if "assignee" not in columns:
        # v3: units.assignee, the ARK account that owns/maintains the node.
        con.execute("ALTER TABLE units ADD COLUMN assignee TEXT")
    con.execute("CREATE INDEX IF NOT EXISTS units_assignee ON units(assignee)")
    chat_columns = {row[1] for row in con.execute("PRAGMA table_info(chat_messages)")}
    if chat_columns and "user" not in chat_columns:
        # v10: per-user Assistant chat history (old rows stay shared).
        con.execute("ALTER TABLE chat_messages ADD COLUMN user TEXT NOT NULL DEFAULT ''")
    con.execute("CREATE INDEX IF NOT EXISTS chat_user ON chat_messages(user, id)")
    member_columns = {row[1] for row in con.execute("PRAGMA table_info(topic_members)")}
    if member_columns and "parent_id" not in member_columns:
        # v6 development builds created topic_members before outlines existed.
        con.execute("ALTER TABLE topic_members ADD COLUMN parent_id INTEGER")
    if member_columns and "w" not in member_columns:
        # v12: a member's node-graph card width (null = automatic).
        con.execute("ALTER TABLE topic_members ADD COLUMN w REAL")
    # v4: the unit_comments table comes from SCHEMA (CREATE IF NOT EXISTS);
    # body note lines written before it existed move into it once.
    if meta_get(con, BODY_NOTES_MIGRATED) != "1":
        migrate_body_notes(con)
        meta_set(con, BODY_NOTES_MIGRATED, "1")
    if meta_get(con, NEEDS_TAGS_MIGRATED) != "1":
        backfill_needs_tags(con)
        meta_set(con, NEEDS_TAGS_MIGRATED, "1")
    # v5: units have dates by default (start_at on creation, end_at on done).
    # The creator-assignee half of v5 needs the org accounts, so the server
    # runs it later (``backfill_creator_assignees``).
    if meta_get(con, DEFAULT_DATES_MIGRATED) != "1":
        backfill_default_dates(con)
        meta_set(con, DEFAULT_DATES_MIGRATED, "1")


# --------------------------------------------------------- v4 body notes
# Before v4 the Home "Needs you" actions appended one line per note to the unit
# body and agents answered the same way.  These are the formats written then.
BODY_NOTES_MIGRATED = "migration.body_notes_v4"
NOTE_KINDS = {
    "User comment": "comment",
    "Sent back by user": "sent_back",
    "Decided by user": "decided",
    "Agent reply": "agent_reply",
}
NOTE_LINE = re.compile(
    r"^\*\*(User comment|Sent back by user|Decided by user|Agent reply) "
    r"\((\d{4}-\d{2}-\d{2})\):\*\* (.*)$")
MIGRATION_ACTOR = "system:migration"


def parse_body_notes(body: str) -> tuple[list[dict], str]:
    """Split note lines out of a unit body.

    Returns ``(notes, new_body)``.  Each note is ``{kind, day, text, remove}``
    in body order.  A note is removed from the body only when its line is the
    whole paragraph (the next line is blank, another note, or the end): an
    exact line match.  A note followed by more text on the next lines (an
    agent reply written as a paragraph) is copied with that text but the body
    keeps it, so nothing the user can see is lost.
    """
    lines = (body or "").split("\n")
    notes: list[dict] = []
    drop: set[int] = set()
    i = 0
    while i < len(lines):
        match = NOTE_LINE.match(lines[i].rstrip("\r"))
        if not match:
            i += 1
            continue
        tail: list[str] = []
        j = i + 1
        while j < len(lines) and lines[j].strip() and not NOTE_LINE.match(lines[j].rstrip("\r")):
            tail.append(lines[j].strip())
            j += 1
        text = " ".join([match.group(3).strip(), *tail]).strip()
        exact = not tail
        if text:
            notes.append({"kind": NOTE_KINDS[match.group(1)], "day": match.group(2),
                          "text": text, "remove": exact})
        if exact:
            drop.add(i)
        i = j
    if not drop:
        return notes, body or ""
    out: list[str] = []
    skip_blank = False
    for index, line in enumerate(lines):
        if index in drop:
            # The blank line that separated the note paragraph goes with it.
            if not out or not out[-1].strip():
                skip_blank = True
            continue
        if skip_blank and not line.strip():
            continue
        skip_blank = False
        out.append(line)
    while out and not out[-1].strip():
        out.pop()
    new_body = "\n".join(out)
    if new_body and (body or "").endswith("\n"):
        new_body += "\n"
    return notes, new_body


def migrate_body_notes(con: sqlite3.Connection) -> int:
    """Move body note lines into ``unit_comments`` rows; returns rows added.

    Idempotent twice over: it runs once per database (meta flag), and a note
    that already has an identical row (unit, kind, author, text, date) is not
    added again.  Agent replies thread under the closest earlier user note.
    ``updated_at`` is left alone: this is a storage change, not an edit.
    """
    added = 0
    inserted_now: set[int] = set()
    rows = con.execute(
        "SELECT id, uid, title, body FROM units WHERE body LIKE '%**%(____-__-__):** %'"
    ).fetchall()
    for row in rows:
        notes, new_body = parse_body_notes(row["body"])
        if not notes:
            continue
        last_user: int | None = None
        moved = 0
        for note in notes:
            author = "agent" if note["kind"] == "agent_reply" else "user"
            created = f"{note['day']}T00:00:00+00:00"
            existing = con.execute(
                "SELECT id FROM unit_comments WHERE unit_id=? AND kind=? AND author=? AND body=? AND created_at=?",
                (row["id"], note["kind"], author, note["text"], created),
            ).fetchone()
            if existing and existing["id"] not in inserted_now:
                comment_id = existing["id"]
            else:
                reply_to = last_user if note["kind"] == "agent_reply" else None
                comment_id = con.execute(
                    "INSERT INTO unit_comments(unit_id,author,kind,body,created_at,reply_to) VALUES(?,?,?,?,?,?)",
                    (row["id"], author, note["kind"], note["text"], created, reply_to),
                ).lastrowid
                inserted_now.add(comment_id)
                added += 1
                moved += 1
            if note["kind"] != "agent_reply":
                last_user = comment_id
        if new_body != row["body"]:
            con.execute("UPDATE units SET body=? WHERE id=?", (new_body, row["id"]))
        if moved:
            con.execute(
                "INSERT INTO activity(at,actor,action,unit_id,detail_json) VALUES(?,?,?,?,?)",
                (utc_now(), MIGRATION_ACTOR, "comment.migrate", row["id"], json.dumps(
                    {"uid": row["uid"], "title": row["title"], "count": moved,
                     "removed_from_body": sum(1 for n in notes if n["remove"])}, ensure_ascii=False)),
            )
    return added


# ------------------------------------------------------- needs: backfill
NEEDS_TAGS_MIGRATED = "migration.needs_tags_v1"


def backfill_needs_tags(con: sqlite3.Connection) -> int:
    """Tag units that wait on the human now with ``needs:review`` / ``needs:decision``.

    Runs once per database (meta flag) and skips any unit that already has a
    ``needs:`` tag, so a second run adds nothing.  History for units that are
    already resolved is unknown and is not guessed.
    """
    rows = con.execute(
        """SELECT id, uid, title, CASE WHEN status='review' THEN 'review' ELSE 'decision' END AS need
           FROM units u WHERE archived=0
             AND (status='review' OR (type='decision' AND status NOT IN ('done','abandoned','archived')))
             AND NOT EXISTS (SELECT 1 FROM unit_tags t WHERE t.unit_id=u.id
                             AND substr(t.tag,1,6)='needs:')"""
    ).fetchall()
    for row in rows:
        con.execute("INSERT OR IGNORE INTO unit_tags(unit_id,tag) VALUES(?,?)",
                    (row["id"], f"needs:{row['need']}"))
        con.execute(
            "INSERT INTO activity(at,actor,action,unit_id,detail_json) VALUES(?,?,?,?,?)",
            (utc_now(), MIGRATION_ACTOR, "unit.needs", row["id"], json.dumps(
                {"uid": row["uid"], "title": row["title"], "from": None, "to": row["need"],
                 "backfill": True}, ensure_ascii=False)),
        )
    return len(rows)


# ------------------------------------------------------ v5: default dates
DEFAULT_DATES_MIGRATED = "migration.default_dates_v5"
CREATOR_ASSIGNEE_MIGRATED = "migration.creator_assignee_v5"
BACKFILL_ACTION = "unit.backfill"
_DAY = re.compile(r"^\d{4}-\d{2}-\d{2}$")
# Plain activity actors that are ARK roles, never account names.  Actors with
# a colon ("human:ui", "agent:x", "system:migration") cannot be usernames.
PSEUDO_ACTORS = frozenset({"system", "human", "agent", "assistant", "import", "user", "ui", "api",
                           "migration", "seed", "demo"})


def _day(value: str | None) -> str | None:
    text = str(value or "")[:10]
    if not _DAY.match(text):
        return None
    try:
        datetime.strptime(text, "%Y-%m-%d")
    except ValueError:
        return None
    return text


def _status_change_days(con: sqlite3.Connection, ids: list[int]) -> dict[int, str]:
    """Date of the last ``unit.update`` that changed ``status``, per unit.

    The update entry lists the changed field names, not the new value; for a
    unit that is ``done`` now, its last status change is the one that made it
    done.  Malformed detail JSON is skipped, never fatal.
    """
    out: dict[int, str] = {}
    for start in range(0, len(ids), 500):
        chunk = ids[start:start + 500]
        marks = ",".join("?" * len(chunk))
        rows = con.execute(
            f"SELECT unit_id, at, detail_json FROM activity WHERE action='unit.update' "
            f"AND unit_id IN ({marks}) ORDER BY id", chunk).fetchall()
        for row in rows:
            try:
                fields = json.loads(row["detail_json"] or "{}").get("fields") or []
            except (ValueError, AttributeError):
                continue
            day = _day(row["at"])
            if day and isinstance(fields, list) and "status" in fields:
                out[row["unit_id"]] = day  # later rows overwrite: the last one wins
    return out


def backfill_default_dates(con: sqlite3.Connection) -> int:
    """Fill the v5 date defaults on existing units; returns units changed.

    * ``start_at`` null -> date of ``created_at``.
    * ``done`` with ``end_at`` null -> date of the last activity entry that
      changed the status, else the date of ``updated_at``.

    Archived units follow the same rules (only nulls are filled).  A default
    that would put ``end_at`` before ``start_at`` is skipped, so no unit ends
    up with an invalid range.  Each changed unit gets one ``unit.backfill``
    activity row by ``system:migration``; ``updated_at`` is left alone.
    Idempotent: a filled field is no longer null, so a rerun changes nothing.
    """
    rows = con.execute(
        "SELECT id, uid, title, status, start_at, end_at, created_at, updated_at FROM units "
        "WHERE start_at IS NULL OR (status='done' AND end_at IS NULL)").fetchall()
    done_ids = [row["id"] for row in rows if row["status"] == "done" and row["end_at"] is None]
    completed = _status_change_days(con, done_ids)
    changed = 0
    for row in rows:
        start, end = row["start_at"], row["end_at"]
        detail: dict = {"uid": row["uid"], "title": row["title"], "fields": [], "backfill": True}
        if start is None:
            day = _day(row["created_at"])
            if day and not (end and end < day):
                start = day
                detail["fields"].append("start_at")
                detail["start_at"] = {"from": None, "to": day, "source": "created_at"}
        if row["status"] == "done" and end is None:
            source = "activity" if row["id"] in completed else "updated_at"
            day = completed.get(row["id"]) or _day(row["updated_at"])
            if day and not (start and day < start):
                end = day
                detail["fields"].append("end_at")
                detail["end_at"] = {"from": None, "to": day, "source": source}
        if not detail["fields"]:
            continue
        con.execute("UPDATE units SET start_at=?, end_at=? WHERE id=?", (start, end, row["id"]))
        con.execute(
            "INSERT INTO activity(at,actor,action,unit_id,detail_json) VALUES(?,?,?,?,?)",
            (utc_now(), MIGRATION_ACTOR, BACKFILL_ACTION, row["id"], json.dumps(detail, ensure_ascii=False)))
        changed += 1
    return changed


def backfill_creator_assignees(con: sqlite3.Connection, is_account) -> int:
    """Assign unassigned units to the account that created them; returns units changed.

    The creator is the actor of the unit's ``unit.create`` activity entry.  It
    counts only when it is a plain name (no ``role:`` prefix, not a reserved
    role such as ``system``) and ``is_account(name)`` returns the canonical
    username of a real ARK account.  Anything else leaves the unit null, so it
    keeps inheriting from its parent.  Before v5 the store logged the role
    ("human:ui", "agent:<name>"), not the account, so on most databases this
    finds nothing.  One ``unit.backfill`` row per change; ``updated_at`` is
    left alone.  Callers run it once per database (meta flag).
    """
    rows = con.execute(
        """SELECT u.id, u.uid, u.title,
                  (SELECT a.actor FROM activity a WHERE a.unit_id=u.id AND a.action='unit.create'
                   ORDER BY a.id LIMIT 1) AS creator
           FROM units u WHERE u.assignee IS NULL""").fetchall()
    changed = 0
    for row in rows:
        creator = str(row["creator"] or "").strip()
        if not creator or ":" in creator or creator.lower() in PSEUDO_ACTORS:
            continue
        name = is_account(creator)
        if not name:
            continue
        con.execute("UPDATE units SET assignee=? WHERE id=? AND assignee IS NULL", (name, row["id"]))
        con.execute(
            "INSERT INTO activity(at,actor,action,unit_id,detail_json) VALUES(?,?,?,?,?)",
            (utc_now(), MIGRATION_ACTOR, BACKFILL_ACTION, row["id"], json.dumps(
                {"uid": row["uid"], "title": row["title"], "fields": ["assignee"], "backfill": True,
                 "assignee": {"from": None, "to": name, "source": "unit.create"}}, ensure_ascii=False)))
        changed += 1
    return changed


def read_manifest(path: Path) -> dict | None:
    manifest = path / "project.json"
    if not manifest.is_file():
        return None
    try:
        data = json.loads(manifest.read_text(encoding="utf-8"))
    except (OSError, ValueError):
        return None
    if data.get("format") not in (None, "ark-studio-project"):
        return None
    return data


def write_manifest(path: Path, data: dict) -> None:
    path.mkdir(parents=True, exist_ok=True)
    tmp = path / "project.json.tmp"
    tmp.write_text(json.dumps(data, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    tmp.replace(path / "project.json")


def meta_get(con: sqlite3.Connection, key: str, default: str | None = None) -> str | None:
    row = con.execute("SELECT value FROM meta WHERE key=?", (key,)).fetchone()
    return row["value"] if row else default


def meta_set(con: sqlite3.Connection, key: str, value: str) -> None:
    con.execute(
        "INSERT INTO meta(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value",
        (key, value),
    )


def new_change_log_id(con: sqlite3.Connection) -> str:
    """Give this database file a new change-log identity (see CHANGE_LOG_SCHEMA)."""
    import secrets
    value = secrets.token_hex(8)
    meta_set(con, CHANGE_LOG_ID, value)
    return value
