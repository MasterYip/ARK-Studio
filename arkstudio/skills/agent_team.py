"""Agent-team planning and execution contract, adapted to ARK projects.

The standalone ``agent-team-workflow`` skill (MIT) is a file-system workspace:
an external checkout holds ``agent_team/`` records and task worktrees, while
repositories stay read-only inputs.  ARK already has the durable record (a
project-owned SQLite database and portable ``.ark`` folder), so this module
maps the contract onto that model instead of creating a parallel workspace:

* a goal manifest is both a Markdown record and an ARK ``task`` unit body;
* task IDs keep the vendor rule that the numeric serial is project-global;
* task worktrees remain ``<repo>/.worktrees/<TASK-ID>`` for implementation;
* the phase/status split is preserved and projected onto ARK unit status;
* gates, evidence retention and reviewer independence are validated, not just
  described.

Everything is dependency-free and importable by the server, the CLI and tests.
"""
from __future__ import annotations

import re
from pathlib import Path, PurePosixPath
from typing import Any, Iterable, Mapping

from . import interface, protocol
from .protocol import ProtocolError

SKILL_ID = "agent-team"
TEMPLATES = ("goal_manifest.md", "inspection_report.md", "agent_brief.md")

_REQUIRED_TEXT = ("title", "objective", "owner", "reviewer", "repository", "base_revision")
_DRIVE_RE = re.compile(r"^[A-Za-z]:")


def _text(value: Any, *, limit: int = 4000) -> str:
    return str(value or "").strip()[:limit]


def _list_of_text(value: Any, *, limit: int = 200, item_limit: int = 500) -> list[str]:
    if value is None:
        return []
    items = value if isinstance(value, (list, tuple)) else [value]
    out = []
    for item in items[:limit]:
        text = _text(item, limit=item_limit)
        if text:
            out.append(text)
    return out


def _acceptance(value: Any) -> list[dict]:
    items = value if isinstance(value, (list, tuple)) else ([value] if value else [])
    out = []
    for raw in items[:50]:
        if isinstance(raw, Mapping):
            command = _text(raw.get("command"), limit=1000)
            threshold = _text(raw.get("threshold") or raw.get("expected"), limit=600)
            evidence = _text(raw.get("evidence"), limit=600)
        else:
            command, threshold, evidence = _text(raw, limit=1000), "", ""
        out.append({"command": command, "threshold": threshold, "evidence": evidence})
    return out


def _evidence(value: Any) -> dict:
    if isinstance(value, Mapping):
        retain = _list_of_text(value.get("retain_in_git") or value.get("retain"))
        external = _list_of_text(value.get("external") or value.get("outside_git"))
    else:
        retain, external = _list_of_text(value), []
    return {"retain_in_git": retain, "external": external}


def validate_worktree(worktree: str, task_id: str) -> None:
    """Require a repository-relative ``.worktrees/<TASK-ID>`` path.

    Sibling worktree roots and absolute paths are rejected because they break
    the portable layout the vendor contract standardises.  An explicit
    ``worktree_exception`` is still allowed, but it must be recorded on the
    manifest (the validator reports the warning instead of silently accepting).
    """
    raw = str(worktree or "").strip()
    if not raw:
        raise ProtocolError("worktree is required")
    if raw.startswith(("/", "\\")) or _DRIVE_RE.match(raw) or ".." in PurePosixPath(raw).parts:
        raise ProtocolError("worktree must be a repository-relative path")
    normalized = raw.replace("\\", "/").rstrip("/")
    expected = f".worktrees/{task_id}"
    if normalized != expected:
        raise ProtocolError(
            f"worktree must be '{expected}' (task worktrees live under .worktrees/)")


def _existing_ids_from_store(store) -> list[str]:
    """All task IDs, including archived tasks.

    ARK gives every unit type its own UID counter (``N-0001`` and ``T-0001``
    coexist), so the protocol's task namespace is the set of *task* UIDs, not
    every ARK node.  Within that namespace the vendor rule still holds: the
    numeric serial is project-global across task prefixes and is never reused.
    """
    try:
        rows = store.con.execute("SELECT uid FROM units WHERE type='task'").fetchall()
    except Exception:  # noqa: BLE001 - a custom store stub may not expose con
        rows = []
    return [str(row["uid"] if hasattr(row, "keys") else row[0]) for row in rows]


def prepare_task(task: Mapping[str, Any], *, existing_ids: Iterable[str] = (),
                 project: Mapping[str, Any] | None = None) -> dict:
    """Validate a task request and render its manifest without writing.

    Returns ``{ok, errors, warnings, manifest, markdown, unit, links,
    record_paths}``.  ``task_id`` is allocated from ``existing_ids`` when it is
    omitted; callers that persist the task should pass every task UID,
    including archived ones.
    """
    task = dict(task or {})
    existing = list(existing_ids)
    errors: list[str] = []
    warnings: list[str] = []

    task_id = _text(task.get("task_id") or task.get("id"), limit=80).upper()
    if task_id:
        try:
            protocol.check_unique_task_id(task_id, existing)
        except ProtocolError as exc:
            errors.append(str(exc))
    else:
        prefix = _text(task.get("prefix") or task.get("task_prefix") or "T", limit=40).upper()
        try:
            task_id = protocol.next_task_id(existing, prefix=prefix)
        except ProtocolError as exc:
            errors.append(str(exc) or "could not allocate a task ID")
            task_id = f"{prefix or 'T'}-001"

    project = dict(project or {})
    project_id = _text(task.get("project_id") or project.get("id") or "ark", limit=120)
    project_name = _text(task.get("project_name") or project.get("name") or project_id, limit=200)

    phase = _text(task.get("phase") or "proposed", limit=40).lower()
    status = _text(task.get("status") or "waiting", limit=40).lower()
    if phase not in protocol.PHASES:
        errors.append(f"phase must be one of: {', '.join(protocol.PHASES)}")
    if status not in protocol.STATUSES:
        errors.append(f"status must be one of: {', '.join(protocol.STATUSES)}")
    if phase in protocol.PHASES and status in protocol.STATUSES:
        try:
            ark_status = protocol.ark_status(phase, status)
        except ProtocolError as exc:
            errors.append(str(exc))
            ark_status = "open"
    else:
        ark_status = "open"

    for key in _REQUIRED_TEXT:
        if not _text(task.get(key)):
            errors.append(f"{key} is required")

    title = _text(task.get("title"), limit=240)
    objective = _text(task.get("objective") or task.get("goal"), limit=6000)
    owner = _text(task.get("owner"), limit=200)
    reviewer = _text(task.get("reviewer"), limit=200)
    # ARK account that owns the task node (units.assignee); optional.
    assignee = _text(task.get("assignee"), limit=64) or None
    repository = _text(task.get("repository"), limit=240)
    base_revision = _text(task.get("base_revision") or task.get("base"), limit=200)
    if owner and reviewer and owner.lower() == reviewer.lower():
        if not task.get("acceptance_exception"):
            errors.append(
                "owner and reviewer must differ unless acceptance_exception is declared")

    dependencies = [str(value).strip().upper() for value in
                    _list_of_text(task.get("dependencies")) if str(value).strip()]
    for dependency in dependencies:
        try:
            protocol.parse_task_id(dependency)
        except ProtocolError as exc:
            errors.append(f"dependency {dependency!r}: {exc}")

    worktree = _text(task.get("worktree") or f".worktrees/{task_id}", limit=300)
    worktree_exception = bool(task.get("worktree_exception"))
    if not worktree_exception:
        try:
            validate_worktree(worktree, task_id)
        except ProtocolError as exc:
            errors.append(str(exc))
    else:
        warnings.append("worktree_exception recorded; the default .worktrees/<TASK-ID> layout is bypassed")

    deliverables = _list_of_text(task.get("deliverables"))
    if not deliverables:
        errors.append("deliverables must declare at least one measurable output")
    non_goals = _list_of_text(task.get("non_goals"))
    if not non_goals:
        warnings.append("non_goals is empty; declare what this task will not do")
    owned_paths = _list_of_text(task.get("owned_paths"))
    if not owned_paths:
        errors.append("owned_paths must declare at least one file or glob")
    acceptance = _acceptance(task.get("acceptance"))
    if not acceptance or not any(item["command"] for item in acceptance):
        errors.append("acceptance must include at least one command or check")
    gates = _list_of_text(task.get("gates"))
    if not gates:
        gates = ["scope", "static", "unit"]
        warnings.append("gates omitted; defaulted to scope, static, unit")
    try:
        gates = protocol.ordered_gates(gates)
    except ProtocolError as exc:
        errors.append(str(exc))
        gates = []
    stop_conditions = _list_of_text(task.get("stop_conditions"))
    if not stop_conditions:
        warnings.append("stop_conditions is empty; declare when to stop and escalate")
    risks = _list_of_text(task.get("risks"))
    background = _text(task.get("background") or task.get("hypothesis"), limit=6000)
    evidence = _evidence(task.get("evidence"))
    importance = _text(task.get("importance") or "p2", limit=10).lower()
    if importance not in {"p0", "p1", "p2", "p3"}:
        errors.append("importance must be p0, p1, p2 or p3")
    budget = task.get("budget")
    if isinstance(budget, Mapping):
        budget_text = ", ".join(f"{key}={value}" for key, value in budget.items())
    else:
        budget_text = _text(budget, limit=600)

    acceptance_exception = bool(task.get("acceptance_exception"))

    task_structure = _text(task.get("task_structure") or "sequential", limit=40).lower()
    if task_structure not in interface.TASK_STRUCTURES:
        errors.append(
            f"task_structure must be one of: {', '.join(interface.TASK_STRUCTURES)}")
        task_structure = "sequential"

    team, team_errors, team_warnings = interface.normalize_team(
        task.get("team"), task_structure=task_structure, reviewer=reviewer)
    errors.extend(team_errors)
    warnings.extend(team_warnings)

    context_plan, context_errors, context_warnings = interface.normalize_context(
        task.get("context"))
    errors.extend(context_errors)
    warnings.extend(context_warnings)

    model_policy, policy_errors, policy_warnings = interface.normalize_model_policy(
        task.get("model_policy"), team_size=team["size"])
    errors.extend(policy_errors)
    warnings.extend(policy_warnings)

    verification, verification_errors, verification_warnings = interface.normalize_verification(
        task.get("verification"), gates=gates, acceptance=acceptance,
        acceptance_exception=acceptance_exception)
    errors.extend(verification_errors)
    warnings.extend(verification_warnings)

    failure_taxonomy, taxonomy_errors, taxonomy_warnings = interface.normalize_failure_taxonomy(
        task.get("failure_taxonomy"))
    errors.extend(taxonomy_errors)
    warnings.extend(taxonomy_warnings)

    handoff, handoff_errors, handoff_warnings = interface.normalize_handoff(
        task.get("handoff"), objective=objective, deliverables=deliverables,
        dependencies=dependencies)
    errors.extend(handoff_errors)
    warnings.extend(handoff_warnings)

    manifest = {
        "format": "ark-agent-team-task",
        "protocol_version": protocol.PROTOCOL_VERSION,
        "interface_version": interface.INTERFACE_VERSION,
        "task_id": task_id,
        "title": title,
        "project_id": project_id,
        "project_name": project_name,
        "ark_unit_uid": _text(task.get("ark_unit_uid"), limit=40) or task_id,
        "phase": phase,
        "status": status,
        "ark_status": ark_status,
        "owner": owner,
        "reviewer": reviewer,
        "assignee": assignee,
        "repository": repository,
        "worktree": worktree,
        "base_revision": base_revision,
        "dependencies": dependencies,
        "importance": importance,
        "budget": budget_text,
        "background": background,
        "objective": objective,
        "deliverables": deliverables,
        "non_goals": non_goals,
        "owned_paths": owned_paths,
        "acceptance": acceptance,
        "gates": gates,
        "gate_evidence": protocol.gate_evidence(gates) if gates else {},
        "evidence": evidence,
        "risks": risks,
        "stop_conditions": stop_conditions,
        "task_structure": task_structure,
        "team": team,
        "context": context_plan,
        "model_policy": model_policy,
        "verification": verification,
        "failure_taxonomy": failure_taxonomy,
        "handoff": handoff,
        "created_at": protocol.utc_now(),
    }

    tags = [
        "skills:agent-team",
        f"task:{task_id}",
        f"project:{project_id}",
    ]
    role = _text(task.get("role"), limit=40)
    if role:
        tags.append(f"skill-role:{role}")
    tags.append(f"topology:{team['mode']}")
    if phase in ("review", "evaluation"):
        # The unit stays `active` during agent review; the tag keeps the stage visible.
        tags.append(f"phase:{phase}")
    unit = {
        "title": f"{task_id} · {title}" if title else task_id,
        "type": "task",
        "status": ark_status,
        "importance": importance,
        "body": "",  # filled after the Markdown render below
        "tags": tags,
    }

    context = dict(manifest)
    context["deliverables"] = manifest["deliverables"]
    context["non_goals"] = manifest["non_goals"]
    context["owned_paths"] = manifest["owned_paths"]
    context["acceptance"] = manifest["acceptance"]
    context["gates"] = manifest["gates"]
    context["evidence"] = manifest["evidence"]
    context["risks"] = manifest["risks"]
    context["stop_conditions"] = manifest["stop_conditions"]
    context["dependencies"] = manifest["dependencies"]
    context["budget"] = budget_text or "_No explicit budget declared._"
    context["context_plan"] = manifest["context"]

    markdown = ""
    if not errors:
        try:
            markdown = render_goal_manifest(context)
            unit["body"] = markdown
        except ProtocolError as exc:
            errors.append(str(exc))

    record_paths = {
        "canonical_record": f"docs/agent-team/tasks/{task_id}/goal_manifest.md",
        "inspection_report": f"docs/agent-team/tasks/{task_id}/inspection_report.md",
        "worktree": worktree,
    }
    links = [
        {"target_task_id": dependency, "relation": "depends_on"}
        for dependency in dependencies
    ]
    return {
        "ok": not errors,
        "errors": errors,
        "warnings": warnings,
        "manifest": manifest,
        "markdown": markdown,
        "unit": unit,
        "links": links,
        "record_paths": record_paths,
    }


def render_goal_manifest(context: Mapping[str, Any]) -> str:
    template = _read_asset("agent-team", "templates", "goal_manifest.md")
    return protocol.substitute(template, context)


def render_inspection_report(context: Mapping[str, Any]) -> str:
    template = _read_asset("agent-team", "templates", "inspection_report.md")
    merged = {
        "task_id": "T-000",
        "owner": "",
        "reviewer": "",
        "phase": "review",
        "status": "waiting",
        "base_revision": "",
        "candidate_revision": "",
        "integration_revision": "",
        "deliverables": [],
        "changed_files": [],
        "commands": [],
        "results": "",
        "deviations": [],
        "residual_risks": [],
        "artifact_inventory": [],
        "handoff": [],
        "team": {"mode": "solo", "size": 1, "heterogeneous": False,
                 "rationale": "", "roles": []},
        "context_plan": {"budget_tokens": 120000, "inline": [], "spill": [],
                         "artifacts": []},
        "failure_taxonomy": list(interface.FAILURE_CLASSES),
        **dict(context or {}),
    }
    return protocol.substitute(template, merged)


def render_agent_brief(context: Mapping[str, Any]) -> str:
    """Render the short delegation brief a router hands to one worker."""
    template = _read_asset("agent-team", "templates", "agent_brief.md")
    merged = {
        "task_id": "T-000",
        "parent_task": "_standalone_",
        "owner": "",
        "reviewer": "",
        "task_structure": "sequential",
        "team": {"mode": "solo", "size": 1, "heterogeneous": False,
                 "rationale": "", "roles": []},
        "objective": "",
        "owned_paths": [],
        "context_plan": {"budget_tokens": 120000, "inline": [], "spill": [],
                         "artifacts": []},
        "acceptance": [],
        "verification": {"independent": True, "method": "command",
                         "evidence": [], "reviewer_reproduces": []},
        "handoff": {"contract": "", "inputs": [], "outputs": []},
        "stop_conditions": [],
        "failure_taxonomy": list(interface.FAILURE_CLASSES),
        **dict(context or {}),
    }
    return protocol.substitute(template, merged)


def create_task(store, task: Mapping[str, Any], *, project: Mapping[str, Any] | None = None,
                actor: str = "agent:skills") -> dict:
    """Create (or preview) an ARK task unit from the agent-team contract.

    When ``create`` is false the function is read-only and returns the same
    preview as :func:`prepare_task`.  When true, an ARK task unit is created
    first (so the unit UID can become the immutable task ID), then the rendered
    manifest is written into its body.  This makes the ARK unit the canonical
    record, exactly like the vendor's ``goal_manifest.md``.
    """
    request = dict(task or {})
    create = bool(request.pop("create", False))
    project = dict(project or {})
    existing = _existing_ids_from_store(store)
    preview = prepare_task(request, existing_ids=existing, project=project)
    if not preview["ok"]:
        raise ProtocolError("; ".join(preview["errors"]))

    preview["created"] = False
    preview["ark_unit"] = None
    if not create:
        return preview

    task_id = preview["manifest"]["task_id"]
    if task_id in existing:
        raise ProtocolError(f"task ID already exists in the ARK project: {task_id}")
    data = {
        "title": f"{task_id} · {preview['manifest']['title']}",
        "type": "task",
        "status": preview["unit"]["status"],
        "importance": preview["unit"]["importance"],
        "body": preview["markdown"],
        "tags": preview["unit"]["tags"],
    }
    # Without a manifest assignee the store assigns the creating account.
    if preview["manifest"].get("assignee"):
        data["assignee"] = preview["manifest"]["assignee"]
    unit = store.create_unit(data, actor=actor)
    uid = unit["uid"]
    # The first creation reserves a UID; the protocol ID is normally equal to
    # it.  If a caller supplied a different immutable ID, keep both mappings
    # explicit instead of rewriting accepted history.
    preview["manifest"]["ark_unit_uid"] = uid
    if uid != task_id:
        preview["warnings"].append(
            f"ARK unit {uid} differs from protocol task ID {task_id}; both are recorded")
        marker = f"\n\n> ARK unit: `{uid}`\n"
        preview["markdown"] = preview["markdown"].rstrip() + marker
    updated = store.update_unit(uid, {
        "body": preview["markdown"],
        "tags": preview["unit"]["tags"],
    }, actor=actor)
    preview["created"] = True
    preview["ark_unit"] = updated
    preview["manifest"]["ark_unit_uid"] = uid
    preview["record_paths"]["ark_unit"] = uid
    return preview


def task_status_row(manifest: Mapping[str, Any]) -> dict:
    """One canonical task-index row, derived from a validated manifest."""
    return {
        "task_id": manifest.get("task_id"),
        "headline": manifest.get("title"),
        "phase": manifest.get("phase"),
        "status": manifest.get("status"),
        "ark_status": manifest.get("ark_status"),
        "owner": manifest.get("owner"),
        "next_gate": (manifest.get("gates") or [None])[0],
        "blocker": "; ".join(manifest.get("stop_conditions") or []) or "",
        "worktree": manifest.get("worktree"),
        "topology": (manifest.get("team") or {}).get("mode"),
        "team_size": (manifest.get("team") or {}).get("size"),
        "context_budget": (manifest.get("context") or {}).get("budget_tokens"),
        "evidence": f"docs/agent-team/tasks/{manifest.get('task_id')}/goal_manifest.md",
    }


def validate_manifest(document: Mapping[str, Any], *,
                      existing_ids: Iterable[str] = ()) -> dict:
    """Re-validate an already-normalized goal manifest.

    Useful for a reviewer or CI job receiving a manifest from another agent:
    the same rules apply and the rendered Markdown must still match.
    """
    payload = dict(document or {})
    payload.pop("format", None)
    payload.pop("protocol_version", None)
    payload.pop("ark_status", None)
    payload.pop("gate_evidence", None)
    payload.pop("created_at", None)
    own_id = str(payload.get("task_id") or "").strip().upper()
    if payload.get("project_id") and not payload.get("project_name"):
        payload["project_name"] = payload.get("project_id")
    return prepare_task(
        payload,
        existing_ids=[value for value in existing_ids if str(value).upper() != own_id],
        project={"id": payload.get("project_id"), "name": payload.get("project_name")})


def example_task(project: Mapping[str, Any] | None = None) -> dict:
    """A complete, valid sample used by the UI and docs."""
    project = dict(project or {})
    task_id = str(project.get("next_task_id") or "T-0100")
    return {
        "create": False,
        "task_id": task_id,
        "title": "Ship the skills/protocol layer",
        "project_id": project.get("id") or "ark-demo",
        "project_name": project.get("name") or "ARK project",
        "phase": "ready",
        "status": "waiting",
        "owner": "agent:codex",
        "reviewer": "human:maintainer",
        "repository": "ARK-Studio",
        "worktree": f".worktrees/{task_id}",
        "base_revision": "main@0000000",
        "dependencies": [],
        "importance": "p1",
        "budget": "one worktree, no new runtime dependency",
        "background": "The team protocol currently lives in a standalone checkout.",
        "objective": "Expose an ARK-native, tested skills layer and keep it discoverable in Settings.",
        "deliverables": [
            "arkstudio/skills package with catalog, templates and validators",
            "Settings > Skills panel and read-only JSON API",
            "tests and protocol documentation",
        ],
        "non_goals": [
            "replacing the ARK UI or building a second standalone app",
            "copying vendor benchmark harnesses or model credentials",
        ],
        "owned_paths": ["arkstudio/skills/**", "tests/python/agents/test_skills.py", "docs/agents/skills.md"],
        "acceptance": [
            {"command": "python -B -m unittest tests.python.agents.test_skills -v",
             "threshold": "all tests pass", "evidence": "test log"},
            {"command": "node --check web/js/views/settings.js",
             "threshold": "syntax check passes", "evidence": "shell output"},
        ],
        "gates": list(protocol.GATE_IDS[:3]),
        "task_structure": "long-horizon",
        "team": {
            "mode": "router-workers",
            "size": 3,
            "heterogeneous": False,
            "rationale": "three independent evidence routes share one task graph",
            "roles": ["router", "worker", "reviewer"],
        },
        "context": {
            "budget_tokens": 120000,
            "inline": ["task manifest", "current unit", "owned paths"],
            "spill": ["full logs", "datasets", "prior reports"],
            "artifacts": ["assets/"],
        },
        "model_policy": {
            "routing": "tiered",
            "planner": "inherit",
            "worker": "inherit",
            "verifier": "independent",
        },
        "verification": {
            "independent": True,
            "method": "command",
            "evidence": ["test log", "diff"],
            "reviewer_reproduces": ["unit"],
        },
        "failure_taxonomy": list(interface.FAILURE_CLASSES),
        "handoff": {
            "contract": "Reviewer receives the diff, test log and residual risks.",
            "inputs": ["main@0000000"],
            "outputs": ["green focused tests", "skills documentation"],
        },
        "evidence": {
            "retain_in_git": ["goal manifest", "test log", "source mapping"],
            "external": [],
        },
        "risks": ["Settings tab regressions from a parallel GUI task"],
        "stop_conditions": ["a test or a required file is owned by another active task"],
    }


def _read_asset(*parts: str) -> str:
    path = Path(__file__).resolve().parent / "assets" / Path(*parts)
    try:
        return path.read_text(encoding="utf-8")
    except OSError as exc:
        raise ProtocolError(f"missing skill asset: {Path(*parts).as_posix()}") from exc
