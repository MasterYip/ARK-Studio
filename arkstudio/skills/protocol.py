"""Shared protocol vocabulary for ARK's internal skills layer.

The vocabulary is adapted from the standalone ``agent-team-workflow`` skill
(MIT, Raymon Yip) and mapped onto ARK's existing unit/status model.  ARK keeps
the same separation the vendor skill insists on:

* ``phase`` describes workflow progression (proposed -> ... -> integrated);
* ``status`` describes the portfolio view (running / waiting / blocked / ...);
* ARK's unit status (open / active / review / blocked / done / abandoned / archived) is a
  projection that the workbench already renders, never a second source of
  truth.

Everything in this module is standard library only and safe to import from a
coding agent, the HTTP server, or a test.
"""
from __future__ import annotations

import re
import string
from datetime import datetime, timezone
from typing import Any, Iterable, Mapping

PROTOCOL_VERSION = 2

# Workflow progression, in order.  Adapted verbatim from
# vendor/agent-team-workflow/references/task-lifecycle.md.
PHASES = (
    "proposed",
    "ready",
    "active",
    "review",
    "evaluation",
    "accepted",
    "integrated",
)

# Portfolio view, deliberately orthogonal to phase.
STATUSES = (
    "running",
    "waiting",
    "blocked",
    "completed",
    "archived",
    "superseded",
)

# Protocol status -> ARK unit status.  The UI only understands the right column.
ARK_STATUS = {
    "running": "active",
    "waiting": "open",
    "blocked": "blocked",
    "completed": "done",
    "archived": "archived",
    "superseded": "archived",
}

# Protocol phase -> ARK unit status, used when a manifest has no explicit
# status yet.
ARK_STATUS_BY_PHASE = {
    "proposed": "open",
    "ready": "open",
    "active": "active",
    # Agent review and evaluation are still work in progress on the map. ARK's
    # `review` status is reserved for acceptance by the human user.
    "review": "active",
    "evaluation": "active",
    "accepted": "done",
    "integrated": "done",
}

ROLES = {
    "manager": "task graph, priorities, ownership, scope, acceptance, escalation",
    "task_owner": "declared implementation or analysis and self-verification",
    "experiment_operator": "dry-run, launch, monitoring, raw artifact handoff",
    "reviewer": "independent gates and acceptance recommendation",
    "integrator": "merge queue, integration revision, post-merge checks",
    "resource_steward": "environments, compute, reservations, import origins",
    "registrar": "shared experiment or result indexes",
    "docs_integrator": "high-conflict reports, tables, figures, claims",
}

# Gates run from cheapest to most expensive.  ``required_evidence`` names the
# record a reviewer expects when the gate is ticked.
GATES = (
    {
        "id": "scope",
        "title": "Scope",
        "required_evidence": ["changed_files", "owned_paths", "clean_diff"],
        "description": "declared files, ownership, clean diff, no secrets or bulk artifacts",
    },
    {
        "id": "static",
        "title": "Static",
        "required_evidence": ["command", "result"],
        "description": "parse, format, lint, syntax, types",
    },
    {
        "id": "unit",
        "title": "Unit",
        "required_evidence": ["command", "result", "test_count"],
        "description": "focused deterministic tests",
    },
    {
        "id": "component",
        "title": "Component smoke",
        "required_evidence": ["command", "result", "artifact"],
        "description": "bounded smoke and import-origin checks",
    },
    {
        "id": "integration",
        "title": "Integration",
        "required_evidence": ["command", "result", "revision"],
        "description": "controlled checkout/environment",
    },
    {
        "id": "experiment",
        "title": "Experiment",
        "required_evidence": ["config", "data", "seed", "metrics", "artifact", "terminal_marker"],
        "description": "config, data, seed, metrics, artifacts, terminal marker",
    },
    {
        "id": "publication",
        "title": "Publication / deployment",
        "required_evidence": ["claim_traceability", "authorized_checks"],
        "description": "claim traceability or authorized safety checks",
    },
)
GATE_IDS = tuple(gate["id"] for gate in GATES)

EVIDENCE_POLICY = {
    "retain_in_git": [
        "goal manifests",
        "inspection reports",
        "decision logs",
        "compact metrics and summaries",
        "checksums and artifact inventories",
        "selected figures",
    ],
    "keep_outside_git": [
        "bulk logs and raw traces",
        "datasets and checkpoints",
        "media collections and caches",
        "PID files and transfer bundles",
        "credentials and secret values",
    ],
}

_TASK_ID_RE = re.compile(r"^(?P<prefix>[A-Z][A-Z0-9]*(?:-[A-Z][A-Z0-9]*)*)-(?P<serial>\d{3,})$")
_SERIAL_RE = re.compile(r"-(\d{3,})$")


class ProtocolError(ValueError):
    """A protocol document violates the ARK skills contract."""


def utc_now() -> str:
    return datetime.now(timezone.utc).isoformat(timespec="seconds")


def parse_task_id(task_id: str) -> tuple[str, int, int]:
    """Return ``(prefix, serial, width)`` for ``PREFIX-001`` style IDs."""
    match = _TASK_ID_RE.match(str(task_id or "").strip())
    if not match:
        raise ProtocolError(
            "task IDs look like PREFIX-001 (uppercase prefix, dash, 3+ digit serial)")
    serial = int(match.group("serial"))
    if serial < 1:
        raise ProtocolError("task ID serial must be positive")
    return match.group("prefix"), serial, len(match.group("serial"))


def task_serial(task_id: str) -> int:
    """Terminal serial of any prefixed ID, or 0 when it has none."""
    match = _SERIAL_RE.search(str(task_id or "").strip())
    return int(match.group(1)) if match else 0


def next_task_id(existing_ids: Iterable[str], prefix: str = "T",
                 width: int | None = None) -> str:
    """Allocate the next project-global serial.

    The serial is unique across *all* prefixes, not just ``prefix``: a project
    that already contains ``FIG-015`` may not issue ``WEB-015``.  Historical and
    archived IDs must be passed in; the caller owns persistence.  The width
    defaults to ARK's four-digit UID format and grows if an existing ID already
    uses more digits.
    """
    prefix = str(prefix or "T").strip().upper()
    if not re.match(r"^[A-Z][A-Z0-9]*(?:-[A-Z][A-Z0-9]*)*$", prefix):
        raise ProtocolError("prefix must be uppercase letters/digits separated by dashes")
    existing_ids = list(existing_ids)
    serials = [task_serial(task_id) for task_id in existing_ids]
    highest = max(serials, default=0)
    widest = max((len(match.group(1)) for task_id in existing_ids
                  for match in [_SERIAL_RE.search(str(task_id))] if match), default=0)
    digits = max(4, int(width or 0), widest)
    serial = highest + 1
    return f"{prefix}-{serial:0{digits}d}"


def check_unique_task_id(task_id: str, existing_ids: Iterable[str]) -> None:
    target = task_serial(task_id)
    if not target:
        raise ProtocolError(f"task ID has no numeric serial: {task_id}")
    for other in existing_ids:
        if str(other).strip() == str(task_id).strip():
            raise ProtocolError(f"task ID already exists: {task_id}")
        if task_serial(other) == target:
            raise ProtocolError(
                f"task serial {target} is already used by {other}; serials are project-global")
    if task_serial(task_id) < 1:
        raise ProtocolError("task ID serial must be positive")


def ark_status(phase: str, status: str | None = None) -> str:
    """Project a protocol (phase, status) pair onto an ARK unit status."""
    if status:
        status = str(status).strip().lower()
        if status not in ARK_STATUS:
            raise ProtocolError(f"unknown protocol status: {status}")
        return ARK_STATUS[status]
    phase = str(phase or "").strip().lower()
    if phase not in ARK_STATUS_BY_PHASE:
        raise ProtocolError(f"unknown protocol phase: {phase}")
    return ARK_STATUS_BY_PHASE[phase]


def gate(gate_id: str) -> dict:
    for item in GATES:
        if item["id"] == gate_id:
            return dict(item)
    raise ProtocolError(f"unknown gate: {gate_id}")


def ordered_gates(gate_ids: Iterable[str]) -> list[str]:
    """Validate and order a gate list cheaply-first; unknown gates raise."""
    wanted = [str(g).strip().lower() for g in gate_ids]
    for gate_id in wanted:
        gate(gate_id)
    unique = list(dict.fromkeys(wanted))
    return [gate_id for gate_id in GATE_IDS if gate_id in unique]


def gate_evidence(gate_ids: Iterable[str]) -> dict:
    """Required evidence names for the requested gates."""
    return {gate_id: gate(gate_id)["required_evidence"] for gate_id in ordered_gates(gate_ids)}


def _as_list(value: Any) -> list:
    if value is None:
        return []
    if isinstance(value, (list, tuple)):
        return list(value)
    return [value]


def _bullets(items: Iterable[Any], empty: str = "_None declared._") -> str:
    lines = [f"- {str(item).strip()}" for item in items if str(item).strip()]
    return "\n".join(lines) if lines else empty


def _numbered(items: Iterable[Any], empty: str = "_None declared._") -> str:
    lines = [f"{index}. {str(item).strip()}"
             for index, item in enumerate(items, start=1) if str(item).strip()]
    return "\n".join(lines) if lines else empty


def _acceptance_items(value: Any) -> list[dict]:
    items = []
    for raw in _as_list(value):
        if isinstance(raw, Mapping):
            item = {
                "command": str(raw.get("command") or "").strip(),
                "threshold": str(raw.get("threshold") or raw.get("expected") or "").strip(),
                "evidence": str(raw.get("evidence") or "").strip(),
            }
        else:
            item = {"command": str(raw or "").strip(), "threshold": "", "evidence": ""}
        items.append(item)
    return items


def render_acceptance(value: Any) -> str:
    lines = []
    for index, item in enumerate(_acceptance_items(value), start=1):
        parts = [f"`{item['command']}`" if item["command"] else "(command not declared)"]
        if item["threshold"]:
            parts.append(f"threshold: {item['threshold']}")
        if item["evidence"]:
            parts.append(f"evidence: {item['evidence']}")
        lines.append(f"{index}. " + " · ".join(parts))
    return "\n".join(lines) if lines else "_None declared._"


def render_gates(value: Any) -> str:
    gate_ids = ordered_gates(_as_list(value)) if value else []
    if not gate_ids:
        return "_None declared._"
    lines = []
    for gate_id in gate_ids:
        item = gate(gate_id)
        lines.append(f"- **{item['title']}** (`{gate_id}`) — {item['description']}")
    return "\n".join(lines)


def render_evidence(value: Any) -> str:
    if isinstance(value, Mapping):
        retain = _as_list(value.get("retain_in_git") or value.get("retain"))
        external = _as_list(value.get("external") or value.get("outside_git"))
    else:
        retain, external = _as_list(value), []
    return "\n".join([
        "**Retained in Git**",
        _bullets(retain or EVIDENCE_POLICY["retain_in_git"]),
        "",
        "**Kept in durable external storage with inventory + checksums**",
        _bullets(external or EVIDENCE_POLICY["keep_outside_git"]),
    ])


def _render_mapping(value: Any) -> str:
    """Render a small mapping as a Markdown definition list."""
    if not isinstance(value, Mapping):
        return "" if value is None else str(value)
    lines = []
    for key, item in value.items():
        label = str(key).replace("_", " ")
        if isinstance(item, bool):
            rendered = "yes" if item else "no"
        elif isinstance(item, (list, tuple)):
            rendered = ", ".join(str(entry) for entry in item) or "_none_"
        else:
            rendered = str(item or "_none_")
        lines.append(f"- **{label}**: {rendered}")
    return "\n".join(lines) if lines else "_None declared._"


def render_context_value(key: str, value: Any) -> str:
    """Render one template value, turning lists and mappings into Markdown."""
    if key == "acceptance":
        return render_acceptance(value)
    if key == "gates":
        return render_gates(value)
    if key == "evidence":
        return render_evidence(value)
    if key == "dependencies":
        return _bullets(value)
    if key in {"team", "context_plan", "model_policy", "verification", "handoff"}:
        return _render_mapping(value)
    if key == "failure_taxonomy":
        return _bullets(value) if value else "_None declared._"
    if key in {"deliverables", "non_goals", "owned_paths", "stop_conditions",
               "risks", "commands", "changed_files", "artifact_inventory",
               "residual_risks", "deviations", "mediums", "qualities",
               "anti_goals", "shared_checks", "mode_checks"}:
        return _bullets(value) if key not in {"commands"} else _numbered(value)
    return "" if value is None else str(value)


_TEMPLATE_PATH = re.compile(r"^[a-z0-9][a-z0-9_-]*\.md$")


def validate_template_name(name: str) -> str:
    name = str(name or "").strip()
    if not _TEMPLATE_PATH.match(name):
        raise ProtocolError("template names must be lowercase file names ending in .md")
    return name


def substitute(template_text: str, context: Mapping[str, Any]) -> str:
    """Render ``$placeholder`` Markdown, raising on missing values.

    ``string.Template`` is used instead of ``str.format`` because the protocol
    documents contain literal braces and JSON examples.  A missing value is an
    error rather than a silent blank: a goal manifest with an empty reviewer is
    worse than no manifest.
    """
    mapping = {key: render_context_value(key, value) for key, value in context.items()}
    try:
        return string.Template(template_text).substitute(**mapping)
    except KeyError as exc:
        missing = str(exc).strip("'")
        raise ProtocolError(f"missing template value: {missing}") from exc
    except ValueError as exc:
        raise ProtocolError(f"invalid template: {exc}") from exc


def protocol_summary() -> dict:
    """JSON-safe description of the shared contract."""
    return {
        "protocol_version": PROTOCOL_VERSION,
        "phases": list(PHASES),
        "statuses": list(STATUSES),
        "ark_status_map": dict(ARK_STATUS),
        "roles": dict(ROLES),
        "gates": [dict(item) for item in GATES],
        "evidence_policy": {
            "retain_in_git": list(EVIDENCE_POLICY["retain_in_git"]),
            "keep_outside_git": list(EVIDENCE_POLICY["keep_outside_git"]),
        },
    }
