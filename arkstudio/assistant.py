"""Local, deterministic research assistant.

No model call is hidden here.  These are graph checks a human would otherwise do
by hand: unlinked ideas, claims without evidence, active work without an owner,
experiments without a task, and pairs of ideas that share topic tags.  Every
suggestion is inspectable and every mutation is an ordinary link command.
"""
from __future__ import annotations

import hashlib
import re
from collections import defaultdict

from .store import Store, StoreError
from .tags import tag_namespace, tag_value

SEVERITY_ORDER = {"high": 0, "medium": 1, "low": 2}


def _sid(prefix: str, *parts) -> str:
    digest = hashlib.sha1("|".join(str(p) for p in parts).encode("utf-8")).hexdigest()[:10]
    return f"{prefix}-{digest}"


def _free_tags(unit: dict) -> dict[str, set[str]]:
    out: defaultdict[str, set[str]] = defaultdict(set)
    for tag in unit.get("tags", []):
        ns = tag_namespace(tag)
        if ns in {"type", "status", "importance"}:
            continue
        out[ns].add(tag_value(tag))
    return dict(out)


def insights(store: Store) -> list[dict]:
    units = store.units_with_tags()
    by_id = {u["id"]: u for u in units}
    links = store.con.execute("SELECT source_id,target_id,relation FROM unit_links").fetchall()
    linked: set[int] = set()
    pairs: set[tuple[int, int]] = set()
    incoming: defaultdict[int, list[tuple[int, str]]] = defaultdict(list)
    outgoing: defaultdict[int, list[tuple[int, str]]] = defaultdict(list)
    for row in links:
        a, b = row["source_id"], row["target_id"]
        linked.add(a)
        linked.add(b)
        pairs.add((min(a, b), max(a, b)))
        outgoing[a].append((b, row["relation"]))
        incoming[b].append((a, row["relation"]))

    suggestions: list[dict] = []

    # 1. Ideas that share two or more free tags but have no edge yet.
    ideas = [u for u in units if u["type"] in {"idea", "question", "claim"}]
    by_topic: defaultdict[str, list[dict]] = defaultdict(list)
    for unit in ideas:
        for value in _free_tags(unit).get("topic", set()):
            by_topic[value].append(unit)
    seen_pairs: set[tuple[int, int]] = set()
    for topic, group in by_topic.items():
        if len(group) < 2:
            continue
        for i, left in enumerate(group):
            for right in group[i + 1:]:
                key = (min(left["id"], right["id"]), max(left["id"], right["id"]))
                if key in pairs or key in seen_pairs:
                    continue
                shared = set(_free_tags(left).get("topic", set())) & set(_free_tags(right).get("topic", set()))
                if not shared:
                    continue
                seen_pairs.add(key)
                suggestions.append(
                    {
                        "id": _sid("share", *key),
                        "kind": "shared-topic",
                        "severity": "low",
                        "title": f"Connect “{left['title']}” and “{right['title']}”",
                        "detail": "They share " + ", ".join(sorted(shared)) + " and have no edge yet.",
                        "unit_ids": [left["id"], right["id"]],
                        "ops": [{"op": "link", "source": left["id"], "target": right["id"],
                                 "relation": "relates_to"}],
                        "action": {"tab": "ideas", "unit_id": left["id"]},
                    }
                )
                if len(suggestions) >= 3:
                    break
            if len(suggestions) >= 3:
                break
        if len(suggestions) >= 3:
            break

    # 2. Unlinked ideas.
    for unit in units:
        if unit["type"] == "idea" and unit["id"] not in linked:
            suggestions.append(
                {
                    "id": _sid("orphan", unit["id"]),
                    "kind": "orphan-idea",
                    "severity": "medium",
                    "title": f"Unlinked idea: {unit['title']}",
                    "detail": "No edge enters or leaves this idea. Connect it to a question, a claim, or an experiment.",
                    "unit_ids": [unit["id"]],
                    "ops": [],
                    "action": {"tab": "ideas", "unit_id": unit["id"]},
                }
            )

    # 3. Claims with no evidence / no experiment.
    for unit in units:
        if unit["type"] != "claim":
            continue
        has_evidence = any(relation in {"supports", "validates", "contradicts"}
                           for _, relation in incoming.get(unit["id"], []))
        has_experiment = any(relation in {"validates", "targets"}
                             for _, relation in incoming.get(unit["id"], []))
        if not has_evidence:
            suggestions.append(
                {
                    "id": _sid("claim-evidence", unit["id"]),
                    "kind": "claim-no-evidence",
                    "severity": "high",
                    "title": f"Claim lacks evidence: {unit['title']}",
                    "detail": "No evidence or experiment points at this claim. Link one before promoting it.",
                    "unit_ids": [unit["id"]],
                    "ops": [],
                    "action": {"tab": "map", "unit_id": unit["id"]},
                }
            )
        elif not has_experiment:
            suggestions.append(
                {
                    "id": _sid("claim-exp", unit["id"]),
                    "kind": "claim-no-experiment",
                    "severity": "medium",
                    "title": f"Claim has evidence but no experiment: {unit['title']}",
                    "detail": "An experiment should validate the claim, or the evidence needs a design record.",
                    "unit_ids": [unit["id"]],
                    "ops": [],
                    "action": {"tab": "map", "unit_id": unit["id"]},
                }
            )

    # 4. Active tasks without an agent.
    for unit in units:
        if unit["type"] != "task" or unit["status"] not in {"open", "active", "review"}:
            continue
        if not any(relation == "assigned_to" for _, relation in outgoing.get(unit["id"], [])):
            suggestions.append(
                {
                    "id": _sid("unassigned", unit["id"]),
                    "kind": "task-unassigned",
                    "severity": "medium",
                    "title": f"Task has no agent: {unit['title']}",
                    "detail": "Assign an owner before dispatch; otherwise the handoff has no accountability.",
                    "unit_ids": [unit["id"]],
                    "ops": [],
                    "action": {"tab": "tasks", "unit_id": unit["id"]},
                }
            )

    # 5. Active experiments with no task.
    for unit in units:
        if unit["type"] != "experiment" or unit["status"] not in {"active", "open"}:
            continue
        if not any(relation == "part_of" for _, relation in incoming.get(unit["id"], [])):
            suggestions.append(
                {
                    "id": _sid("exp-task", unit["id"]),
                    "kind": "experiment-no-task",
                    "severity": "medium",
                    "title": f"Experiment has no task: {unit['title']}",
                    "detail": "An active experiment needs at least one task that can be scheduled and stopped.",
                    "unit_ids": [unit["id"]],
                    "ops": [],
                    "action": {"tab": "plan", "sub": "experiment", "unit_id": unit["id"]},
                }
            )

    # 6. Blocked work with no recorded blocker.
    for unit in units:
        if unit["status"] != "blocked":
            continue
        has_blocker = any(relation in {"blocks", "depends_on"} for _, relation in incoming.get(unit["id"], [])) or any(
            relation in {"blocks", "depends_on"} for _, relation in outgoing.get(unit["id"], [])
        )
        if not has_blocker:
            suggestions.append(
                {
                    "id": _sid("blocked", unit["id"]),
                    "kind": "blocked-no-reason",
                    "severity": "high",
                    "title": f"Blocked without a blocker: {unit['title']}",
                    "detail": "Add the task or decision that must clear before this can move.",
                    "unit_ids": [unit["id"]],
                    "ops": [],
                    "action": {"tab": "map", "unit_id": unit["id"]},
                }
            )

    suggestions.sort(key=lambda s: (SEVERITY_ORDER.get(s["severity"], 9), s["kind"]))
    return suggestions[:18]


def apply(store: Store, suggestion: dict, actor: str = "human:ui") -> dict:
    ops = suggestion.get("ops") or []
    applied = []
    for op in ops:
        if op.get("op") != "link":
            continue
        result = store.create_link(op.get("source"), op.get("target"),
                                   op.get("relation") or "relates_to",
                                   "Applied from assistant insight", actor=actor)
        applied.append(result)
    store.log_activity(actor, "assistant.apply", None,
                       {"suggestion": suggestion.get("id"), "kind": suggestion.get("kind"), "ops": len(applied)})
    return {"applied": applied}


def extract_ideas(store: Store, text: str, actor: str = "human:ui",
                  source: int | str | None = None, limit: int = 24) -> dict:
    """Deterministic outline parser: one bullet/paragraph becomes one idea.

    This is deliberately not an LLM. It is the bridge for pasted brainstorm
    notes: it keeps the user's words, adds provenance, and lets the graph do
    the grouping/connecting with the assistant checks.
    """
    text = str(text or "").replace("\r\n", "\n")
    if not text.strip():
        raise StoreError("text is required")
    raw_lines = [line.strip() for line in text.splitlines()]
    blocks: list[str] = []
    current: list[str] = []
    bullet_re = re.compile(r"^(?:[-*•]|\d+[.)])\s+")
    for line in raw_lines:
        if not line:
            if current:
                blocks.append(" ".join(current).strip())
                current = []
            continue
        if bullet_re.match(line):
            if current:
                blocks.append(" ".join(current).strip())
            current = [bullet_re.sub("", line, count=1)]
        elif re.match(r"^#{1,4}\s+", line):
            if current:
                blocks.append(" ".join(current).strip())
            current = []
        else:
            current.append(line)
    if current:
        blocks.append(" ".join(current).strip())
    blocks = [b for b in blocks if len(b) >= 2][:max(1, min(60, int(limit or 24)))]
    if not blocks:
        raise StoreError("no idea-like lines found")
    source_unit = None
    if source:
        source_unit = store.get_unit(source)
    created, skipped = [], 0
    for block in blocks:
        title = block if len(block) <= 120 else block[:117].rstrip() + "…"
        try:
            unit = store.create_unit(
                {
                    "type": "idea",
                    "title": title,
                    "body": block,
                    "tags": ["origin:extract", "stage:capture", "topic:intake"],
                    "importance": "p2",
                    "origin": "assistant",
                    "source_title": source_unit["title"] if source_unit else "Pasted outline",
                    "meta": {"extracted": True, "extractor": "outline-v1"},
                },
                actor=actor,
            )
        except StoreError:
            skipped += 1
            continue
        created.append(unit)
        if source_unit:
            try:
                store.create_link(unit["id"], source_unit["id"], "derived_from", "Extracted from source", actor=actor)
                unit = store.get_unit(unit["id"])
                created[-1] = unit
            except StoreError:
                pass
    store.log_activity(actor, "assistant.extract", None,
                       {"created": len(created), "skipped": skipped,
                        "source": source_unit["uid"] if source_unit else None})
    return {"created": created, "created_count": len(created), "skipped": skipped}


def dispatch_prompt(store: Store, task_ref: int | str) -> dict:
    task = store.get_unit(task_ref)
    if task["type"] != "task":
        raise StoreError("dispatch requires a task unit")
    links_out = task.get("links_out", [])
    links_in = task.get("links_in", [])
    claim = next((l["target"] for l in links_out if l["relation"] == "targets"
                  and l["target"]["type"] in {"claim", "question"}), None)
    experiment = next((l["target"] for l in links_out if l["relation"] == "part_of"
                       and l["target"]["type"] == "experiment"), None)
    agent = next((l["target"] for l in links_out if l["relation"] == "assigned_to"), None)
    host = next((l["target"] for l in links_out if l["relation"] == "runs_on"), None)
    gates = [t for t in task["tags"] if tag_namespace(t) in {"gate", "risk", "protocol"}]
    lines = [
        f"# ARK dispatch · {task['uid']} — {task['title']}",
        "",
        "You are receiving one task from an ARK research project. Do not expand scope.",
        "",
        "## Task",
        f"- UID: {task['uid']}",
        f"- Status: {task['status']}",
        f"- Importance: {task['importance']}",
        f"- Window: {task['start_at'] or '—'} → {task['end_at'] or '—'}",
        f"- Body: {task['body'] or '—'}",
        "",
        "## Trace",
        f"- Claim / question: {claim['uid'] + ' · ' + claim['title'] if claim else 'MISSING — stop and ask'}",
        f"- Experiment: {experiment['uid'] + ' · ' + experiment['title'] if experiment else '—'}",
        f"- Agent: {agent['uid'] + ' · ' + agent['title'] if agent else 'UNASSIGNED'}",
        f"- Host: {host['uid'] + ' · ' + host['title'] if host else '—'}",
        f"- Gates: {', '.join(gates) if gates else '—'}",
        "",
        "## Contract",
        "1. Work only in the assigned worktree/host; never touch another task's artifacts.",
        "2. Write a report with command, commit, seeds, budget, artifact path and stop condition.",
        "3. If a gate fails, stop and report the residual; do not retry silently.",
        "4. Return the report path and a one-paragraph verdict to the manager.",
        "",
        "## Suggested CLI",
        "```bash",
        f"python -m arkstudio agent context --project \"{store.path}\"",
        "```",
        "",
        "_Generated locally by ARK Studio. No message was sent._",
    ]
    return {
        "task": {k: task[k] for k in ("id", "uid", "title", "status", "importance", "body")},
        "claim": claim,
        "experiment": experiment,
        "agent": agent,
        "host": host,
        "gates": gates,
        "prompt": "\n".join(lines),
    }


def context(store: Store) -> dict:
    units = store.units_with_tags()
    stats = store.stats()
    active_tasks = [u for u in units if u["type"] == "task" and u["status"] in {"open", "active", "review"}]
    active_experiments = [u for u in units if u["type"] == "experiment" and u["status"] in {"open", "active"}]
    open_claims = [u for u in units if u["type"] == "claim" and u["status"] in {"open", "active"}]
    return {
        "project": store.manifest,
        "counts": stats,
        "active_tasks": [
            {"uid": u["uid"], "title": u["title"], "status": u["status"], "importance": u["importance"],
             "end_at": u["end_at"], "assignees": [a["uid"] for a in u["assignees"]]}
            for u in active_tasks[:24]
        ],
        "active_experiments": [
            {"uid": u["uid"], "title": u["title"], "status": u["status"], "end_at": u["end_at"]}
            for u in active_experiments[:16]
        ],
        "open_claims": [
            {"uid": u["uid"], "title": u["title"], "status": u["status"], "importance": u["importance"]}
            for u in open_claims[:16]
        ],
        "activity": store.activity(limit=15),
        "links": "GET /api/agent/v1/units?q=&type=&limit=100",
        "write_contract": {
            "create": "POST /api/agent/v1/units",
            "update": "PATCH /api/agent/v1/units/{id}",
            "link": "POST /api/agent/v1/links",
            "comment_reply": "POST /api/agent/v1/units/{id}/comments {body, reply_to}",
            "comment_resolve": "POST /api/agent/v1/units/{id}/comments/{comment_id}/resolve",
            "auth": "Authorization: Bearer <agent token>",
        },
    }
