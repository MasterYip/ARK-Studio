"""The small vocabulary that makes the node graph usable.

Everything that is *not* a reserved attribute is a free-form tag.  The reserved
namespaces are intentionally narrow: changing them changes what the workbench
can render, so they are validated at the single write boundary.

    type:idea           exactly one, one of KNOWN_TYPES
    status:active       optional, defaults to open
    importance:p1       optional, defaults to p2

All other tags are opaque.  A tag without a namespace is stored as ``tag:...``.
"""
from __future__ import annotations

import re
from collections import OrderedDict
from typing import Iterable

NAMESPACES = ("type", "status", "importance")

KNOWN_TYPES = OrderedDict(
    [
        ("idea", {"label": "Idea", "prefix": "I", "color": "#f59e0b"}),
        ("question", {"label": "Question", "prefix": "Q", "color": "#38bdf8"}),
        ("claim", {"label": "Claim", "prefix": "C", "color": "#a78bfa"}),
        ("evidence", {"label": "Evidence", "prefix": "E", "color": "#34d399"}),
        ("story", {"label": "Story", "prefix": "S", "color": "#fb7185"}),
        ("related_work", {"label": "Related work", "prefix": "RW", "color": "#2dd4bf"}),
        # A topic is a node that holds a curated subgraph of other nodes
        # (see Store.topic / the topic_members table).
        ("topic", {"label": "Topic", "prefix": "TP", "color": "#6366f1"}),
        ("method", {"label": "Method", "prefix": "M", "color": "#fb923c"}),
        ("experiment", {"label": "Experiment", "prefix": "X", "color": "#c084fc"}),
        ("run", {"label": "Run", "prefix": "RUN", "color": "#94a3b8"}),
        ("task", {"label": "Task", "prefix": "T", "color": "#60a5fa"}),
        ("milestone", {"label": "Milestone", "prefix": "MS", "color": "#818cf8"}),
        ("artifact", {"label": "Artifact", "prefix": "F", "color": "#22d3ee"}),
        ("resource", {"label": "Resource", "prefix": "R", "color": "#4ade80"}),
        ("agent", {"label": "Agent", "prefix": "A", "color": "#facc15"}),
        ("host", {"label": "Host", "prefix": "H", "color": "#a3a3a3"}),
        ("production", {"label": "Production", "prefix": "P", "color": "#e879f9"}),
        ("outreach", {"label": "Outreach", "prefix": "O", "color": "#f87171"}),
        ("decision", {"label": "Decision", "prefix": "D", "color": "#fcd34d"}),
        ("source", {"label": "Source", "prefix": "SRC", "color": "#7dd3fc"}),
        ("note", {"label": "Note", "prefix": "N", "color": "#cbd5e1"}),
    ]
)

KNOWN_STATUSES = OrderedDict(
    [
        ("inbox", {"label": "Inbox", "color": "#94a3b8"}),
        ("open", {"label": "Open", "color": "#60a5fa"}),
        ("active", {"label": "Active", "color": "#22c55e"}),
        ("review", {"label": "Review", "color": "#f59e0b"}),
        ("blocked", {"label": "Blocked", "color": "#ef4444"}),
        ("done", {"label": "Done", "color": "#14b8a6"}),
        ("abandoned", {"label": "Abandoned", "color": "#b45309"}),
        ("archived", {"label": "Archived", "color": "#64748b"}),
    ]
)

KNOWN_IMPORTANCE = OrderedDict(
    [
        ("p0", {"label": "P0 · critical", "color": "#ef4444"}),
        ("p1", {"label": "P1 · high", "color": "#f59e0b"}),
        ("p2", {"label": "P2 · normal", "color": "#3b82f6"}),
        ("p3", {"label": "P3 · low", "color": "#64748b"}),
    ]
)

DEFAULT_TYPE = "note"
DEFAULT_STATUS = "open"
DEFAULT_IMPORTANCE = "p2"

# Directed, closed set.  Reverse navigation is always available in the UI.
RELATIONS = OrderedDict(
    [
        ("part_of", {"label": "part of", "reverse": "contains"}),
        ("derived_from", {"label": "derived from", "reverse": "led to"}),
        ("supports", {"label": "supports", "reverse": "supported by"}),
        ("contradicts", {"label": "contradicts", "reverse": "contradicted by"}),
        ("depends_on", {"label": "depends on", "reverse": "required by"}),
        ("blocks", {"label": "blocks", "reverse": "blocked by"}),
        ("cites", {"label": "cites", "reverse": "cited by"}),
        ("inspires", {"label": "inspires", "reverse": "inspired by"}),
        ("targets", {"label": "targets", "reverse": "targeted by"}),
        ("produces", {"label": "produces", "reverse": "produced by"}),
        ("validates", {"label": "validates", "reverse": "validated by"}),
        ("assigned_to", {"label": "assigned to", "reverse": "assigned"}),
        ("runs_on", {"label": "runs on", "reverse": "runs"}),
        ("supersedes", {"label": "supersedes", "reverse": "superseded by"}),
        ("uses", {"label": "uses", "reverse": "used by"}),
        ("relates_to", {"label": "relates to", "reverse": "relates to"}),
        ("unset", {"label": "unset", "reverse": "unset"}),
    ]
)

# ``needs:`` records that a unit once waited on the human and how that ended.
# ARK owns it (see ``needs_transition``); client-supplied ``needs:`` tags are
# ignored.  A unit carries at most one, and it is never removed once set.
NEEDS_NS = "needs"
NEEDS_PENDING = ("review", "decision")
NEEDS_OUTCOMES = ("accepted", "sent-back", "decided", "dismissed")
NEEDS_VALUES = NEEDS_PENDING + NEEDS_OUTCOMES


def needs_pending(type_name: str, status: str, archived: bool = False) -> str | None:
    """``review`` / ``decision`` when the unit is waiting on the human now."""
    if archived:
        return None
    if status == "review":
        return "review"
    if type_name == "decision" and status not in ("done", "abandoned", "archived"):
        return "decision"
    return None


def needs_transition(current: str | None, type_name: str, status: str,
                     archived: bool = False) -> str | None:
    """The ``needs:`` value a unit should carry after a write.

    ``current`` is the stored value (or None).  A pending unit carries its
    pending value; when it stops pending the value becomes the outcome; a
    resolved outcome stays until the unit is pending again.
    """
    pending = needs_pending(type_name, status, archived)
    if pending:
        return pending
    if current not in NEEDS_PENDING:
        return current
    if archived or status == "archived":
        return "dismissed"
    if status == "done":
        return "accepted" if current == "review" else "decided"
    if current == "review":
        return "sent-back"
    # An open decision that stopped being a decision (type changed).
    return "dismissed"


TAG_RE = re.compile(r"^[^\s\x00-\x1f]{1,72}$")
# Namespaces may be Chinese (InfoHub uses 大话题:) as well as the reserved ASCII names.
NS_RE = re.compile(r"^[^\W\d_][\w-]{0,23}$", re.UNICODE)


class TagError(ValueError):
    pass


def split_tag(raw: str) -> tuple[str, str]:
    text = str(raw or "").strip()
    if not text:
        raise TagError("empty tag")
    if ":" in text:
        ns, value = text.split(":", 1)
        ns, value = ns.strip().lower(), value.strip()
    else:
        ns, value = "tag", text
    if not NS_RE.match(ns):
        raise TagError(f"bad tag namespace: {ns!r}")
    if not TAG_RE.match(value):
        raise TagError(f"bad tag value: {value!r}")
    return ns, value


def normalize_tags(
    tags: Iterable[str] | None,
    *,
    type_name: str | None = None,
    status: str | None = None,
    importance: str | None = None,
) -> list[str]:
    """Return the canonical tag list for a unit.

    ``type_name`` / ``status`` / ``importance`` are the explicit fields from the
    API.  They must agree with any reserved tag already present.  Defaults are
    inserted so every unit has exactly one type, status and importance tag.
    """
    collected: dict[str, str] = {}
    type_values: list[str] = []
    status_values: list[str] = []
    importance_values: list[str] = []

    for raw in tags or []:
        ns, value = split_tag(raw)
        if ns == "type":
            value = value.lower()
            if value not in KNOWN_TYPES:
                raise TagError(f"unknown type: {value!r}")
            type_values.append(value)
        elif ns == "status":
            value = value.lower()
            if value not in KNOWN_STATUSES:
                raise TagError(f"unknown status: {value!r}")
            status_values.append(value)
        elif ns == "importance":
            value = value.lower()
            if value not in KNOWN_IMPORTANCE:
                raise TagError(f"unknown importance: {value!r}")
            importance_values.append(value)
        else:
            collected[f"{ns}:{value}"] = f"{ns}:{value}"

    if type_name is not None:
        type_name = type_name.strip().lower()
        if type_name not in KNOWN_TYPES:
            raise TagError(f"unknown type: {type_name!r}")
        type_values.append(type_name)
    if status is not None:
        status = status.strip().lower()
        if status not in KNOWN_STATUSES:
            raise TagError(f"unknown status: {status!r}")
        status_values.append(status)
    if importance is not None:
        importance = importance.strip().lower()
        if importance not in KNOWN_IMPORTANCE:
            raise TagError(f"unknown importance: {importance!r}")
        importance_values.append(importance)

    def one(values: list[str], default: str, label: str) -> str:
        unique = sorted(set(values))
        if len(unique) > 1:
            raise TagError(f"a unit has exactly one {label}; got {', '.join(unique)}")
        return unique[0] if unique else default

    final_type = one(type_values, DEFAULT_TYPE, "type")
    final_status = one(status_values, DEFAULT_STATUS, "status")
    final_importance = one(importance_values, DEFAULT_IMPORTANCE, "importance")

    ordered = [f"type:{final_type}", f"status:{final_status}", f"importance:{final_importance}"]
    ordered.extend(sorted(collected))
    # Keep a stable, de-duplicated order.
    seen: set[str] = set()
    return [tag for tag in ordered if not (tag in seen or seen.add(tag))]


def tag_namespace(tag: str) -> str:
    return tag.split(":", 1)[0] if ":" in tag else "tag"


def tag_value(tag: str) -> str:
    return tag.split(":", 1)[1] if ":" in tag else tag


def type_color(type_name: str) -> str:
    return KNOWN_TYPES.get(type_name, {}).get("color", "#94a3b8")


def type_prefix(type_name: str) -> str:
    return KNOWN_TYPES.get(type_name, {}).get("prefix", "U")


def public_vocabulary() -> dict:
    return {
        "types": [{"id": key, **value} for key, value in KNOWN_TYPES.items()],
        "statuses": [{"id": key, **value} for key, value in KNOWN_STATUSES.items()],
        "importance": [{"id": key, **value} for key, value in KNOWN_IMPORTANCE.items()],
        "relations": [{"id": key, **value} for key, value in RELATIONS.items()],
        "namespaces": list(NAMESPACES),
        # Server-owned ``needs:`` values (pending first, then outcomes).
        "needs": list(NEEDS_VALUES),
    }
