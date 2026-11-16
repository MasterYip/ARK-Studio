"""Home overview: one read-only aggregate of the project state.

``project_overview(store)`` backs ``GET /api/project/overview``.  Everything is
computed with a handful of aggregate queries (no per-unit queries), so a
1000-unit project answers in a few milliseconds.  See docs/guide/home-overview.md.
"""
from __future__ import annotations

import json
import time
from datetime import date, datetime, timedelta, timezone
from typing import Any, Iterable

# Statuses that still need work, and the ones that do not.
CLOSED = ("done", "abandoned", "archived")
OPEN_STATUSES = ("inbox", "open", "active", "review", "blocked")

# Pipeline stages and the unit types that make them up (same as Store.stats).
STAGES = (
    ("capture", ("idea", "source")),
    ("plan", ("story", "related_work", "method", "experiment")),
    ("evidence", ("question", "claim", "evidence")),
    ("execute", ("task", "milestone", "run")),
    ("make", ("production", "artifact")),
    ("share", ("outreach",)),
)

SUPPORT_RELATIONS = ("supports", "validates")
REFUTE_RELATIONS = ("contradicts",)

# Activity that is bookkeeping, not work, on the Home feed.
QUIET_ACTIONS = ("link.create", "link.delete", "link.update", "media.update")

DAYS = 14
LIST_LIMIT = 6


def _brief(row, owners: dict | None = None) -> dict:
    out = {
        "id": row["id"], "uid": row["uid"], "type": row["type"], "title": row["title"],
        "status": row["status"], "importance": row["importance"],
        "start_at": row["start_at"], "end_at": row["end_at"],
        "updated_at": row["updated_at"],
    }
    if owners is not None:
        own = row["assignee"]
        out["assignee_effective"] = own or (owners.get(row["id"]) or (None, None))[0]
    return out


def _marks(values: Iterable) -> str:
    return ",".join("?" * len(list(values)))


def _detail(raw: str | None) -> dict:
    try:
        value = json.loads(raw or "{}")
    except ValueError:
        return {}
    return value if isinstance(value, dict) else {}


def _status_change(detail: dict) -> tuple[str | None, str | None]:
    status = detail.get("status")
    if isinstance(status, dict):
        return status.get("from"), status.get("to")
    return None, None


def project_overview(store, *, members: Iterable[str] = (), today: str | None = None,
                     now: datetime | None = None) -> dict:
    """Aggregate project state for Home.

    ``members`` are project member usernames (shown in Team load even when
    they have nothing assigned).  ``today`` / ``now`` exist for tests.
    """
    started = time.perf_counter()
    now = now or datetime.now(timezone.utc)
    today = today or now.date().isoformat()
    today_d = date.fromisoformat(today)
    week_ago_d = today_d - timedelta(days=7)
    week_ago = week_ago_d.isoformat()
    two_weeks = (today_d - timedelta(days=14)).isoformat()
    first_day = (today_d - timedelta(days=DAYS - 1)).isoformat()
    soon_end = (today_d + timedelta(days=7)).isoformat()
    closed = _marks(CLOSED)

    with store.lock:
        con = store.con
        owners = store._assignee_index()

        # ---------------------------------------------------------- live units
        live = con.execute(
            "SELECT id, type, status, end_at, assignee FROM units WHERE archived=0"
        ).fetchall()
        graph_nodes = [{"id": row["id"], "type": row["type"], "status": row["status"]}
                       for row in live]
        live_ids = {row["id"] for row in live}
        graph_edges = [{"source": row[0], "target": row[1], "relation": row[2]}
                       for row in con.execute("SELECT source_id, target_id, relation FROM unit_links")
                       if row[0] in live_ids and row[1] in live_ids]
        degree = {node["id"]: 0 for node in graph_nodes}
        for edge in graph_edges:
            degree[edge["source"]] += 1
            degree[edge["target"]] += 1
        for node in graph_nodes:
            node["degree"] = degree[node["id"]]
        blocked_tag_ids = {row[0] for row in con.execute(
            "SELECT unit_id FROM unit_tags WHERE tag='status:blocked'")}

        # ------------------------------------------------- 7-day state rebuild
        # The first status change logged in the window gives a unit's status
        # 7 days ago; a unit created in the window did not exist then; one
        # archived (restored) in the window was live (archived) then.
        prior: dict[int, Any] = {}
        archived_then: dict[int, bool] = {}
        for row in con.execute(
            "SELECT unit_id, action, detail_json FROM activity WHERE at >= ? AND unit_id IS NOT NULL "
            "AND action IN ('unit.create','unit.update','unit.archive','unit.restore') ORDER BY id",
            (week_ago,),
        ):
            uid, action = row[0], row[1]
            if action == "unit.create":
                prior.setdefault(uid, None)
                continue
            if action == "unit.archive":
                archived_then.setdefault(uid, False)
                continue
            if action == "unit.restore":
                archived_then.setdefault(uid, True)
                continue
            old, _new = _status_change(_detail(row[2]))
            if old and uid not in prior:
                prior[uid] = old
        archived_rows = {row[0]: row for row in con.execute(
            "SELECT id, type, status, end_at FROM units WHERE archived=1")} if archived_then else {}

        # ----------------------------------------------------------- per day
        created_days = {row[0]: row[1] for row in con.execute(
            "SELECT substr(created_at,1,10) d, COUNT(*) FROM units WHERE created_at >= ? GROUP BY d",
            (two_weeks,))}
        completed_days: dict[str, set] = {}
        events_days: dict[str, int] = {}
        for row in con.execute(
            "SELECT substr(a.at,1,10) d, a.action, a.unit_id, a.detail_json, u.status "
            "FROM activity a LEFT JOIN units u ON u.id=a.unit_id WHERE a.at >= ?",
            (two_weeks,),
        ):
            day, action = row[0], row[1]
            if action not in QUIET_ACTIONS:
                events_days[day] = events_days.get(day, 0) + 1
            if action != "unit.update" or row[2] is None:
                continue
            detail = _detail(row[3])
            _old, new = _status_change(detail)
            if new is not None:
                done = new == "done"
            else:  # older logs: fields only
                done = "status" in (detail.get("fields") or []) and row[4] == "done"
            if done:
                completed_days.setdefault(day, set()).add(row[2])

        # ------------------------------------------------------ due / blocked
        brief_cols = ("id, uid, type, title, status, importance, start_at, end_at, "
                      "updated_at, assignee")
        overdue_total = con.execute(
            f"SELECT COUNT(*) FROM units WHERE archived=0 AND status NOT IN ({closed}) "
            "AND end_at IS NOT NULL AND end_at < ?", (*CLOSED, today)).fetchone()[0]
        overdue_rows = con.execute(
            f"SELECT {brief_cols} FROM units WHERE archived=0 AND status NOT IN ({closed}) "
            "AND end_at IS NOT NULL AND end_at < ? ORDER BY end_at ASC, id LIMIT ?",
            (*CLOSED, today, LIST_LIMIT)).fetchall()
        soon_total = con.execute(
            f"SELECT COUNT(*) FROM units WHERE archived=0 AND status NOT IN ({closed}) "
            "AND end_at >= ? AND end_at <= ?", (*CLOSED, today, soon_end)).fetchone()[0]
        soon_rows = con.execute(
            f"SELECT {brief_cols} FROM units WHERE archived=0 AND status NOT IN ({closed}) "
            "AND end_at >= ? AND end_at <= ? ORDER BY end_at ASC, id LIMIT ?",
            (*CLOSED, today, soon_end, LIST_LIMIT)).fetchall()
        blocked_where = ("archived=0 AND status NOT IN ('done','abandoned','archived') AND (status='blocked' OR id IN "
                         "(SELECT unit_id FROM unit_tags WHERE tag='status:blocked'))")
        blocked_total = con.execute(f"SELECT COUNT(*) FROM units WHERE {blocked_where}").fetchone()[0]
        blocked_rows = con.execute(
            f"SELECT {brief_cols} FROM units WHERE {blocked_where} ORDER BY updated_at ASC LIMIT ?",
            (LIST_LIMIT,)).fetchall()
        blockers: dict[int, list[dict]] = {}
        ids = [row["id"] for row in blocked_rows]
        if ids:
            marks = _marks(ids)
            for row in con.execute(
                f"""SELECT l.target_id unit, 'blocks' relation, u.uid, u.title, u.status, u.type
                      FROM unit_links l JOIN units u ON u.id=l.source_id
                     WHERE l.relation='blocks' AND l.target_id IN ({marks})
                    UNION ALL
                    SELECT l.source_id unit, 'depends_on' relation, u.uid, u.title, u.status, u.type
                      FROM unit_links l JOIN units u ON u.id=l.target_id
                     WHERE l.relation='depends_on' AND l.source_id IN ({marks})""",
                (*ids, *ids),
            ):
                blockers.setdefault(row["unit"], []).append({
                    "uid": row["uid"], "title": row["title"], "status": row["status"],
                    "type": row["type"], "relation": row["relation"]})

        # ------------------------------------------------------------ research
        q_counts = con.execute(
            f"SELECT COUNT(*), SUM(CASE WHEN status NOT IN ({closed}) THEN 1 ELSE 0 END) "
            "FROM units WHERE archived=0 AND type='question'", CLOSED).fetchone()
        q_latest = con.execute(
            f"SELECT {brief_cols} FROM units WHERE archived=0 AND type='question' "
            f"AND status NOT IN ({closed}) ORDER BY updated_at DESC, id DESC LIMIT 3",
            CLOSED).fetchall()
        sup, ref = _marks(SUPPORT_RELATIONS), _marks(REFUTE_RELATIONS)
        claims = con.execute(
            f"""SELECT {', '.join('u.' + c.strip() for c in brief_cols.split(','))},
                   EXISTS(SELECT 1 FROM unit_links l WHERE l.target_id=u.id AND l.relation IN ({sup})) sup,
                   EXISTS(SELECT 1 FROM unit_links l WHERE l.target_id=u.id AND l.relation IN ({ref})) ref
                FROM units u WHERE u.archived=0 AND u.type='claim'
                ORDER BY u.updated_at DESC, u.id DESC""",
            (*SUPPORT_RELATIONS, *REFUTE_RELATIONS)).fetchall()
        exp_status = {row[0]: row[1] for row in con.execute(
            "SELECT status, COUNT(*) FROM units WHERE archived=0 AND type='experiment' GROUP BY status")}
        exp_latest = con.execute(
            f"SELECT {brief_cols} FROM units WHERE archived=0 AND type='experiment' "
            "ORDER BY updated_at DESC, id DESC LIMIT 3").fetchall()

        # ------------------------------------------------------------ activity
        quiet = _marks(QUIET_ACTIONS)
        activity_rows = con.execute(
            f"""SELECT a.id, a.at, a.actor, a.action, a.unit_id, a.detail_json, u.uid, u.title, u.type
                  FROM activity a LEFT JOIN units u ON u.id=a.unit_id
                 WHERE a.action NOT IN ({quiet}) ORDER BY a.at DESC, a.id DESC LIMIT 40""",
            QUIET_ACTIONS).fetchall()

    # ------------------------------------------------------------- KPIs now
    def is_blocked(uid: int, status: str) -> bool:
        return status == "blocked" or (uid in blocked_tag_ids and status not in CLOSED)

    counts_now = {"active": 0, "blocked": 0, "review": 0, "overdue": 0, "open": 0}
    counts_prev = {"active": 0, "blocked": 0, "review": 0, "overdue": 0}
    stage_of = {t: key for key, types in STAGES for t in types}
    stages = {key: {"id": key, "types": list(types), "count": 0, "done": 0, "active": 0,
                    "review": 0, "blocked": 0, "open": 0} for key, types in STAGES}
    load: dict[str, dict] = {}
    unassigned = {"open": 0, "active": 0, "review": 0, "blocked": 0, "overdue": 0}

    def bump_prev(uid: int, status: str | None, end_at: str | None, changed: bool) -> None:
        if status is None:
            return
        if status == "active":
            counts_prev["active"] += 1
        if status == "review":
            counts_prev["review"] += 1
        # A logged old status is authoritative; the blocked tag is today's.
        if status == "blocked" if changed else is_blocked(uid, status):
            counts_prev["blocked"] += 1
        if status not in CLOSED and end_at and end_at < week_ago:
            counts_prev["overdue"] += 1

    for row in live:
        uid, type_name, status, end_at = row[0], row[1], row[2], row[3]
        open_ = status not in CLOSED
        overdue = open_ and bool(end_at) and end_at < today
        blocked = is_blocked(uid, status)
        if status == "active":
            counts_now["active"] += 1
        if status == "review":
            counts_now["review"] += 1
        if blocked:
            counts_now["blocked"] += 1
        if overdue:
            counts_now["overdue"] += 1
        if open_:
            counts_now["open"] += 1
        stage = stages.get(stage_of.get(type_name, ""))
        if stage is not None and status not in ("abandoned", "archived"):
            stage["count"] += 1
            stage["done"] += status == "done"
            stage["active"] += status == "active" and not blocked
            stage["review"] += status == "review" and not blocked
            stage["blocked"] += blocked
            stage["open"] += open_
        if open_:
            name = row[4] or (owners.get(uid) or (None, None))[0]
            bucket = load.setdefault(name.lower(), {"username": name, "open": 0, "active": 0, "review": 0,
                                                     "blocked": 0, "overdue": 0}) if name else unassigned
            bucket["open"] += 1
            bucket["active"] += status == "active"
            bucket["review"] += status == "review"
            bucket["blocked"] += blocked
            bucket["overdue"] += overdue
        if archived_then.get(uid) is True:
            continue  # restored in the window: archived (not live) a week ago
        bump_prev(uid, prior[uid] if uid in prior else status, end_at, uid in prior)
    for uid, was_archived in archived_then.items():
        row = archived_rows.get(uid)
        if row is not None and not was_archived:
            bump_prev(uid, prior[uid] if uid in prior else row[2], row[3], uid in prior)

    kpis: dict[str, Any] = {
        key: {"now": counts_now[key], "prev": counts_prev[key], "delta": counts_now[key] - counts_prev[key]}
        for key in ("active", "blocked", "review", "overdue")
    }
    kpis["open"] = {"now": counts_now["open"]}

    days = []
    for offset in range(DAYS - 1, -1, -1):
        day = (today_d - timedelta(days=offset)).isoformat()
        days.append({"date": day, "created": created_days.get(day, 0),
                     "completed": len(completed_days.get(day, ())),
                     "events": events_days.get(day, 0)})

    def window(source: dict, start: str, end: str, size=lambda v: v) -> int:
        return sum(size(v) for d, v in source.items() if start <= d < end)

    tomorrow = (today_d + timedelta(days=1)).isoformat()
    week_start = (today_d - timedelta(days=6)).isoformat()
    kpis["created"] = {"week": window(created_days, week_start, tomorrow),
                       "prev": window(created_days, (today_d - timedelta(days=13)).isoformat(), week_start)}
    kpis["completed"] = {"week": window(completed_days, week_start, tomorrow, len),
                         "prev": window(completed_days, (today_d - timedelta(days=13)).isoformat(),
                                        week_start, len)}

    # ---------------------------------------------------------- members
    seen = set(load)
    for name in members:
        if name and name.lower() not in seen:
            seen.add(name.lower())
            load[name.lower()] = {"username": name, "open": 0, "active": 0, "review": 0,
                                  "blocked": 0, "overdue": 0}
    member_set = {str(m).lower() for m in members if m}
    member_rows = []
    for bucket in load.values():
        member_rows.append({**bucket, "member": bucket["username"].lower() in member_set})
    member_rows.sort(key=lambda m: (-m["open"], -m["active"], m["username"].lower()))

    supported = sum(1 for row in claims if row["sup"])
    contested = sum(1 for row in claims if row["ref"])
    lacking_rows = [row for row in claims if not row["sup"] and not row["ref"] and row["status"] not in CLOSED]
    research = {
        "questions": {"total": q_counts[0] or 0, "open": q_counts[1] or 0,
                      "latest": [_brief(row, owners) for row in q_latest]},
        "claims": {
            "total": len(claims), "supported": supported, "contested": contested,
            "lacking": sum(1 for row in claims if not row["sup"] and not row["ref"]),
            "lacking_open": len(lacking_rows),
            "lacking_items": [_brief(row, owners) for row in lacking_rows[:5]],
        },
        "experiments": {"total": sum(exp_status.values()), "by_status": exp_status,
                        "latest": [_brief(row, owners) for row in exp_latest]},
    }

    activity = [{
        "id": row["id"], "at": row["at"], "actor": row["actor"], "action": row["action"],
        "unit_id": row["unit_id"], "unit_uid": row["uid"], "unit_title": row["title"],
        "unit_type": row["type"], "detail": _detail(row["detail_json"]),
    } for row in activity_rows]

    return {
        "today": today,
        "graph": {"nodes": graph_nodes, "edges": graph_edges},
        "kpis": kpis,
        "days": days,
        "stages": [stages[key] for key, _ in STAGES],
        "members": member_rows,
        "unassigned": unassigned,
        "due": {
            "overdue": [_brief(row, owners) for row in overdue_rows], "overdue_total": overdue_total,
            "soon": [_brief(row, owners) for row in soon_rows], "soon_total": soon_total,
        },
        "blocked": {"total": blocked_total, "items": [
            {**_brief(row, owners), "blockers": blockers.get(row["id"], [])} for row in blocked_rows]},
        "research": research,
        "activity": activity,
        "store_ms": round((time.perf_counter() - started) * 1000, 2),
    }


def runs_summary(listing: dict, limit: int = 5) -> dict:
    """Compact agent-session summary from ``SessionManager.list()`` output."""
    sessions = list(listing.get("sessions") or [])
    active_states = {"starting", "running", "stopping"}
    active = [s for s in sessions if str(s.get("status") or "") in active_states]
    recent = sorted(sessions, key=lambda s: str(s.get("last_activity_at") or s.get("created_at") or ""),
                    reverse=True)
    pick = active + [s for s in recent if s not in active]
    return {
        "total": len(sessions),
        "active": len(active),
        "counts": dict(listing.get("counts") or {}),
        "recent": [{
            "id": s.get("id"), "title": s.get("title") or "", "status": s.get("status") or "",
            "agent": s.get("agent") or s.get("platform") or "", "task_ref": s.get("task_ref") or "",
            "last_activity_at": s.get("last_activity_at") or s.get("created_at"),
        } for s in pick[:limit]],
    }


def gpu_summary(gpu, *, project: str = "") -> dict:
    """Cheap GPU state: config, queue counts and the last cached probe.

    Never probes over SSH (that is an explicit action in Execute › GPU).
    """
    out: dict[str, Any] = {"available": False, "configured": False, "enabled": False,
                           "queue_enabled": False, "servers": 0, "jobs": {}, "free_gpus": None,
                           "gpus": None, "probed_at": None}
    if gpu is None:
        return out
    try:
        state = gpu._inspect()
    except Exception:  # noqa: BLE001 - Home must render without GPU support
        return out
    config = state.get("config") or None
    out["available"] = True
    out["configured"] = bool(state.get("present")) and not state.get("error")
    out["enabled"] = bool(config) and gpu._enabled(config)
    out["servers"] = len((config or {}).get("servers") or [])
    queue = (config or {}).get("queue") or {}
    out["queue_enabled"] = bool(queue.get("enabled"))
    last = getattr(gpu, "last_probe_summary", None)
    if isinstance(last, dict):
        out["free_gpus"] = last.get("free_gpus")
        out["gpus"] = last.get("gpus")
        out["probed_at"] = last.get("probed_at")
    if out["queue_enabled"] and out["enabled"]:
        try:
            jobs = gpu.queue_list(project=project, limit=200).get("jobs") or []
        except Exception:  # noqa: BLE001
            jobs = []
        counts: dict[str, int] = {}
        for job in jobs:
            key = str(job.get("status") or job.get("state") or "unknown")
            counts[key] = counts.get(key, 0) + 1
        out["jobs"] = counts
    return out
