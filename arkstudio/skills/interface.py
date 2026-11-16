"""ARK agent interface v2: the normative contract for external agents.

The interface is deliberately small and machine-readable.  It answers four
questions an external coordinator (Codex, DSH, Claude Code, a research script)
must answer before it touches an ARK project:

1. **What can I do?**  ``describe()`` returns capabilities, auth, limits and
   the manifest schema.  No prose parsing, no guessing from the UI.
2. **Should this be a team?**  ``topologies`` and ``task_structures`` encode the
   rule that a team is earned by task shape, not by ambition.  Sequential,
   tightly coupled work stays ``solo``; breadth-first and sparse long-horizon
   work may use ``router-workers``; only genuinely adversarial review work
   justifies a heterogeneous ``peer-team``.
3. **How much context and compute may I spend?**  ``normalize_context`` and
   ``normalize_model_policy`` turn vague intent into explicit budgets.
4. **How will the result be trusted?**  ``normalize_verification`` requires an
   independent reviewer and a named method, and ``normalize_failure_taxonomy``
   forces a typed failure record instead of a vague "it broke".

The module is standard library only and must stay importable by the server,
the CLI and a coding agent running inside a task worktree.
"""
from __future__ import annotations

from typing import Any, Iterable, Mapping
from urllib.parse import urlparse

INTERFACE_VERSION = "2.0"

# --------------------------------------------------------------------------- #
# Vocabulary
# --------------------------------------------------------------------------- #

# Task structure is the first decision: it determines whether parallelism can
# pay for its coordination cost at all.
TASK_STRUCTURES: dict[str, dict] = {
    "sequential": {
        "summary": "Steps depend on each other; the next step needs the previous artefact.",
        "default_topology": "solo",
        "allowed_topologies": ["solo", "pipeline"],
        "why": "Parallel workers would duplicate or race on the same state.",
    },
    "breadth": {
        "summary": "Many independent directions are explored, then synthesised.",
        "default_topology": "router-workers",
        "allowed_topologies": ["pipeline", "router-workers", "peer-team"],
        "why": "Parallel context windows add evidence rather than coordination overhead.",
    },
    "long-horizon": {
        "summary": "A long argument or experiment with sparse dependencies between milestones.",
        "default_topology": "router-workers",
        "allowed_topologies": ["pipeline", "router-workers"],
        "why": "Durable checkpoints and a shared task list beat one huge context.",
    },
    "adversarial": {
        "summary": "Competing hypotheses or a proof/claim that must survive refutation.",
        "default_topology": "peer-team",
        "allowed_topologies": ["peer-team"],
        "why": "Independent, heterogeneous critics reduce anchoring and shared bias.",
    },
}

# Topology is the second decision: how many agents, and who coordinates them.
TOPOLOGIES: dict[str, dict] = {
    "solo": {
        "summary": "One agent owns the task end to end.",
        "min_agents": 1,
        "max_agents": 1,
        "coordination": "none",
        "best_for": ["sequential"],
        "requires": [],
    },
    "pipeline": {
        "summary": "Ordered stages with an explicit hand-off between them.",
        "min_agents": 2,
        "max_agents": 3,
        "coordination": "hand-off contract per stage",
        "best_for": ["sequential", "breadth", "long-horizon"],
        "requires": ["named hand-off contract"],
    },
    "router-workers": {
        "summary": "One router holds the plan; isolated workers execute and report back.",
        "min_agents": 2,
        "max_agents": 5,
        "coordination": "router + shared task list + artefact references",
        "best_for": ["breadth", "long-horizon"],
        "requires": ["shared task list", "router", "independent verifier"],
    },
    "peer-team": {
        "summary": "Peers share a task list and challenge each other directly.",
        "min_agents": 3,
        "max_agents": 5,
        "coordination": "shared task list + mailbox + debate protocol",
        "best_for": ["adversarial"],
        "requires": ["heterogeneous models or roles", "independent reviewer"],
    },
}

TEAM_MODES = tuple(TOPOLOGIES)

TOPOLOGY_BY_STRUCTURE = {
    structure: spec["default_topology"] for structure, spec in TASK_STRUCTURES.items()
}

FAILURE_CLASSES = (
    "spec",
    "tool",
    "context",
    "model",
    "environment",
    "review",
    "integration",
)

FAILURE_CLASS_NOTES = {
    "spec": "the brief or acceptance was ambiguous or incomplete",
    "tool": "a tool was missing, misdescribed, or returned unusable output",
    "context": "required information was absent, stale, or lost in compaction",
    "model": "the model could not produce a correct step at the chosen effort",
    "environment": "runtime, dependency, permission or resource failure",
    "review": "an independent reviewer rejected the evidence or the reasoning",
    "integration": "the change conflicted with concurrent or upstream work",
}

ROUTING_MODES = ("fixed", "tiered", "auction")

VERIFICATION_METHODS = ("command", "rubric", "human", "mixed")

CONTEXT_LIMITS = {
    "min_tokens": 8_000,
    "max_tokens": 2_000_000,
    "default_tokens": 120_000,
    "warn_above_tokens": 500_000,
}

TEAM_LIMITS = {
    "recommended_max": 5,
    "absolute_max": 8,
    "heterogeneous_required_above": 5,
}

HANDOFF_KEYS = ("contract", "inputs", "outputs")

PRINCIPLES = (
    "ARK is the durable system of record; agents are ephemeral workers.",
    "Bind before acting: a human-confirmed environment profile precedes task work.",
    "Start solo. A team must be justified by task structure, not by ambition.",
    "Least privilege: a token, a task, an owned path and a tool catalog, all narrow.",
    "Evidence over assertion: commands, revisions, artefacts and checksums.",
    "Context is a budget: inline the hot set, spill the rest, reference artefacts.",
    "Route and budget explicitly; unbounded fan-out is a defect, not a strategy.",
    "Every failure gets a class, every retry a reason, every stop an escalation.",
    "Prefer a compiled skill over a new agent role when behaviour is static.",
)

CAPABILITIES: tuple[dict, ...] = (
    {"id": "setup.read", "kind": "read", "method": "GET",
     "path": "/api/agent/v1/setup",
     "summary": "Your view of the setup: accessible and known-but-inaccessible resources, "
                "effective policies and readiness for this host (X-ARK-Host)."},
    {"id": "setup.discover", "kind": "read", "method": "POST",
     "path": "/api/agent/v1/setup/discover",
     "summary": "Read-only discovery of repositories, task spaces, compute and providers."},
    {"id": "setup.draft", "kind": "write", "method": "POST",
     "path": "/api/agent/v1/setup/draft",
     "summary": "Draft or propose your layer (layer=user) or the project layer; "
                "confirmation is human-only."},
    {"id": "setup.resource", "kind": "write", "method": "POST",
     "path": "/api/agent/v1/setup/resource",
     "summary": "Upsert one resource in your layer with visibility private, listed or shared."},
    {"id": "setup.location", "kind": "write", "method": "POST",
     "path": "/api/agent/v1/setup/location",
     "summary": "Register where an accessible resource lives on this host."},
    {"id": "setup.verify", "kind": "read", "method": "POST",
     "path": "/api/agent/v1/setup/verify",
     "summary": "Read-only checks that the confirmed environment exists here."},
    {"id": "interface.describe", "kind": "read", "method": "GET",
     "path": "/api/agent/v1/interface",
     "summary": "Capabilities, limits, topologies and manifest schema."},
    {"id": "whoami.read", "kind": "read", "method": "GET",
     "path": "/api/agent/v1/whoami",
     "summary": "Who the token acts for: user, scope and reachable projects."},
    {"id": "context.read", "kind": "read", "method": "GET",
     "path": "/api/agent/v1/context",
     "summary": "Project summary, counts and current work."},
    {"id": "gpu.jobs.read", "kind": "read", "method": "GET",
     "path": "/api/agent/v1/gpu/jobs",
     "summary": "Project GPU jobs with status,name_prefix,name,since,server,ids,limit,offset filters; returns total,has_more,next_offset. GET /{id}/logs?tail=20 for logs."},
    {"id": "gpu.jobs.write", "kind": "write", "method": "POST",
     "path": "/api/agent/v1/gpu/jobs",
     "summary": "Submit a GPU job; POST /{id}/cancel cancels. Requires current project write permission."},
    {"id": "project.palette.read", "kind": "read", "method": "GET",
     "path": "/api/agent/v1/project/palette",
     "summary": "Read the project colour palette and its preset catalogue."},
    {"id": "project.palette.write", "kind": "write", "method": "POST",
     "path": "/api/agent/v1/project/palette",
     "summary": "Change the project colour palette; presets or custom #RRGGBB values."},
    {"id": "units.read", "kind": "read", "method": "GET",
     "path": "/api/agent/v1/units",
     "summary": "Filtered unit list; use ids= for a bounded read, assignee=<user>|none|me for ownership."},
    {"id": "units.write", "kind": "write", "method": "POST",
     "path": "/api/agent/v1/units",
     "summary": "Create or update a unit; exactly one status tag; optional assignee (ARK username or null)."},
    {"id": "history.read", "kind": "read", "method": "GET",
     "path": "/api/agent/v1/units/{id}/history",
     "summary": "Opt-in Git title/body revisions, newest first; limit/offset paging. "
                "GET history/{hash} reads text; history/diff?from={hash}&to={hash} compares revisions."},
    {"id": "history.write", "kind": "write", "method": "POST",
     "path": "/api/agent/v1/units/{id}/history",
     "summary": "Set {enabled: true|false}. Off retains old history. "
                "POST history/restore with {revision, expected_hash} restores text as a new revision; "
                "POST history/checkpoint with {expected_hash} saves pending GUI edits now. "
                "POST history/apply with {from,to,file,change,expected_hash} applies one old change from diff.changes; "
                "omit change to apply the full file. To must match current text. "
                "GUI edits checkpoint after 5 minutes idle and sufficient changes; agent edits record immediately. "
                "expected_hash is current_hash from history GET. No media or database snapshots are committed."},
    {"id": "comments.read", "kind": "read", "method": "GET",
     "path": "/api/agent/v1/units/{id}/comments",
     "summary": "A unit's comments in time order and its open_user_comments count. "
                "GET /api/agent/v1/units/{id} includes them too. Read unresolved user "
                "comments before resuming or editing a node; they are instructions."},
    {"id": "comments.reply", "kind": "write", "method": "POST",
     "path": "/api/agent/v1/units/{id}/comments",
     "summary": "Answer with {body, reply_to}; agents only write kind agent_reply. "
                "Comments are never deleted or edited."},
    {"id": "comments.resolve", "kind": "write", "method": "POST",
     "path": "/api/agent/v1/units/{id}/comments/{comment_id}/resolve",
     "summary": "Resolve a comment once it is addressed ({resolved: false} reopens it)."},
    {"id": "topics.read", "kind": "read", "method": "GET",
     "path": "/api/agent/v1/units/{id}/members",
     "summary": "A topic unit's curated subgraph, a saved custom filter: members in narrative "
                "outline pre-order (step, path like 1.2, depth, parent, source manual|rule, canvas "
                "x/y, caption; ?full=1 adds bodies), links between them "
                "(hidden=true when the topic does not show one), excluded units and settings "
                "{rule, relations, hidden_links}."},
    {"id": "topics.write", "kind": "write", "method": "POST",
     "path": "/api/agent/v1/units/{id}/members",
     "summary": "Design a topic in one call: {add: [id | {unit, x, y, caption}], remove, exclude, "
                "restore, items: [{unit, x, y, caption, parent: id | null | 'auto'}], order: [ids], "
                "rule: {q, types, statuses, "
                "importance, tags, tags_unset, roots, ...} | null, relations: [relation] | null, "
                "hidden_links | hide_links | show_links: [link ids]}. Create the topic itself as a "
                "unit of type topic with the same design (POST /api/agent/v1/units {type: 'topic', "
                "title, body, design}). Unknown or archived refs fail the whole call. Workflow: "
                "docs/guide/topics.md#agent-workflow."},
    {"id": "links.write", "kind": "write", "method": "POST",
     "path": "/api/agent/v1/links",
     "summary": "Create typed links between units."},
    {"id": "media.read", "kind": "read", "method": "GET",
     "path": "/api/agent/v1/media/{id}/raw",
     "summary": "Stream project media without a browser session; supports HEAD, Range and "
                "?download=1. Personal tokens also accept existing /api/media/{id}/raw URLs."},
    {"id": "export.read", "kind": "read", "method": "GET",
     "path": "/api/agent/v1/export",
     "summary": "Download format=bundle&units=UID,...&depth=0..3 as linked Markdown ZIP with "
                "media; check=1 returns counts and bytes. Also supports json, markdown, csv and svg. "
                "Read scope and current project access required; no browser session."},
    {"id": "media.write", "kind": "write", "method": "POST",
     "path": "/api/agent/v1/media",
     "summary": "Attach a project-relative media path to a unit."},
    {"id": "media.upload", "kind": "write", "method": "POST",
     "path": "/api/agent/v1/media/upload",
     "summary": "Upload one embedded image as base64 and attach it to a unit."},
    {"id": "assets.upload", "kind": "write", "method": "POST",
     "path": "/api/agent/v1/assets/upload",
     "summary": "Upload any project asset as base64 (Markdown, PDF, data, images, ...)."},
    {"id": "sessions.status", "kind": "read", "method": "GET",
     "path": "/api/agent/v1/sessions/{id}",
     "summary": "Session-context upload state for an ark-ses-... id: uploads_enabled, received_bytes "
                "(the next offset) and limits. GET /api/agent/v1/sessions/watcher.py downloads the watcher."},
    {"id": "sessions.upload", "kind": "write", "method": "POST",
     "path": "/api/agent/v1/sessions/{id}/upload",
     "summary": "Only with the user's permission and when the project accepts uploads: append complete "
                "JSONL lines {offset, content, sha256?, mode: append|replace}; idempotent by offset+hash, "
                "secrets redacted. See docs/agents/session-context.md."},
    {"id": "skills.read", "kind": "read", "method": "GET",
     "path": "/api/agent/v1/skills",
     "summary": "Discover the adapted skills and their prompts."},
    {"id": "tasks.create", "kind": "write", "method": "POST",
     "path": "/api/skills/agent-team/tasks",
     "summary": "Validate a manifest and optionally create the ARK task unit."},
    {"id": "tasks.validate", "kind": "read", "method": "POST",
     "path": "/api/skills/agent-team/validate",
     "summary": "Validate a task, manifest, or agent brief without writing."},
    {"id": "brief.validate", "kind": "read", "method": "POST",
     "path": "/api/skills/agent-team/brief",
     "summary": "Validate a delegation brief before spawning a worker."},
    {"id": "rrsi.select", "kind": "read", "method": "POST",
     "path": "/api/skills/rrsi/round",
     "summary": "Apply the harness-search selection rule to a round."},
    {"id": "design.verify", "kind": "read", "method": "POST",
     "path": "/api/skills/design/verify",
     "summary": "Verify a project artefact before attaching it."},
)

CONFORMANCE = {
    "L0": {
        "name": "read-only observer",
        "requires": ["interface.describe", "setup.read", "context.read", "units.read"],
        "may_write": False,
    },
    "L1": {
        "name": "solo contributor",
        "requires": ["L0", "setup.discover", "setup.draft", "tasks.create", "units.write",
                     "media.write", "media.upload"],
        "may_write": True,
    },
    "L2": {
        "name": "team coordinator",
        "requires": ["L1", "brief.validate", "links.write"],
        "may_write": True,
        "notes": "Must declare topology, team size, context budget and an "
                 "independent verifier in every manifest.",
    },
}

DEPRECATIONS = (
    {
        "id": "implicit-team",
        "since": "2.0",
        "replacement": "declare team.mode and team.size, or stay solo",
        "detail": "A manifest without a team section is treated as solo; "
                  "silently spawning helpers is a conformance violation.",
    },
    {
        "id": "unbounded-context",
        "since": "2.0",
        "replacement": "context.budget_tokens with inline/spill lists",
        "detail": "Agents that read the whole project without a budget are "
                  "expected to regress on long tasks.",
    },
)


# --------------------------------------------------------------------------- #
# Normalisation helpers
# --------------------------------------------------------------------------- #

def _text(value: Any, limit: int = 2000) -> str:
    return str(value or "").strip()[:limit]


def _list(value: Any, limit: int = 200, item_limit: int = 1000) -> list[str]:
    if value is None:
        return []
    items = value if isinstance(value, (list, tuple)) else [value]
    out: list[str] = []
    for item in items[:limit]:
        text = _text(item, item_limit)
        if text:
            out.append(text)
    return out


def _int(value: Any, default: int) -> int:
    if isinstance(value, bool):
        return default
    try:
        return int(value)
    except (TypeError, ValueError):
        return default


def _relative_path(value: str) -> bool:
    raw = str(value or "").strip().replace("\\", "/")
    if not raw or raw.startswith("/") or ".." in raw.split("/"):
        return False
    parsed = urlparse(raw)
    return not parsed.scheme and not parsed.netloc


def normalize_team(value: Any, *, task_structure: str = "sequential",
                   reviewer: str = "") -> tuple[dict, list[str], list[str]]:
    """Validate the team block and return ``(team, errors, warnings)``."""
    errors: list[str] = []
    warnings: list[str] = []
    raw = dict(value) if isinstance(value, Mapping) else {}
    mode = _text(raw.get("mode") or "solo", 40).lower()
    if mode not in TOPOLOGIES:
        errors.append(f"team.mode must be one of: {', '.join(TEAM_MODES)}")
        mode = "solo"
    spec = TOPOLOGIES[mode]

    size = _int(raw.get("size"), 1)
    if size < 1:
        errors.append("team.size must be at least 1")
        size = 1
    if size > TEAM_LIMITS["absolute_max"]:
        errors.append(
            f"team.size {size} exceeds the absolute maximum "
            f"{TEAM_LIMITS['absolute_max']}")
    if size < spec["min_agents"] or size > spec["max_agents"]:
        warnings.append(
            f"team.size {size} is outside the recommended range "
            f"{spec['min_agents']}-{spec['max_agents']} for {mode}")

    heterogeneous = bool(raw.get("heterogeneous"))
    if size > TEAM_LIMITS["heterogeneous_required_above"] and not heterogeneous:
        errors.append(
            "team.size above "
            f"{TEAM_LIMITS['heterogeneous_required_above']} requires "
            "heterogeneous=true (homogeneous agents hit a hard ceiling)")
    if mode == "peer-team" and not heterogeneous:
        errors.append("peer-team requires heterogeneous=true (models or roles)")

    rationale = _text(raw.get("rationale"), 600)
    if size > 1 and not rationale:
        errors.append("team.size > 1 requires a rationale explaining why the "
                      "task structure earns a team")
    if mode == "solo" and size != 1:
        errors.append("solo requires team.size = 1")
    if size == 1 and mode != "solo":
        errors.append(f"{mode} requires more than one agent; use solo instead")

    roles = _list(raw.get("roles"), limit=12, item_limit=80)
    if mode == "peer-team":
        lowered = {role.lower() for role in roles}
        if not ({"reviewer", "critic", "verifier"} & lowered):
            errors.append("peer-team requires a reviewer, critic or verifier role")

    if size > 1 and not _text(reviewer):
        warnings.append("team tasks should name an independent reviewer")

    allowed = TASK_STRUCTURES.get(task_structure, TASK_STRUCTURES["sequential"])[
        "allowed_topologies"]
    if mode not in allowed:
        warnings.append(
            f"topology {mode} is unusual for a {task_structure} task; "
            f"consider one of: {', '.join(allowed)}")

    team = {
        "mode": mode,
        "size": max(1, size),
        "heterogeneous": heterogeneous,
        "rationale": rationale,
        "roles": roles,
    }
    return team, errors, warnings


def normalize_context(value: Any) -> tuple[dict, list[str], list[str]]:
    errors: list[str] = []
    warnings: list[str] = []
    raw = dict(value) if isinstance(value, Mapping) else {}

    budget = _int(raw.get("budget_tokens"), CONTEXT_LIMITS["default_tokens"])
    if budget < CONTEXT_LIMITS["min_tokens"]:
        errors.append(
            f"context.budget_tokens must be at least {CONTEXT_LIMITS['min_tokens']}")
    if budget > CONTEXT_LIMITS["max_tokens"]:
        errors.append(
            f"context.budget_tokens must not exceed {CONTEXT_LIMITS['max_tokens']}")
    if budget > CONTEXT_LIMITS["warn_above_tokens"]:
        warnings.append("a very large context budget is likely to be wasted; "
                        "spill artefacts instead of inlining them")

    inline = _list(raw.get("inline")) or [
        "task manifest", "current unit", "owned paths"]
    spill = _list(raw.get("spill")) or [
        "full logs", "datasets", "prior reports"]
    artifacts = _list(raw.get("artifacts"))
    for path in artifacts:
        if not _relative_path(path):
            errors.append(f"context.artifacts path must be project-relative: {path}")
    if not spill:
        warnings.append("context.spill is empty; everything would be inlined")

    plan = {
        "budget_tokens": max(CONTEXT_LIMITS["min_tokens"], budget),
        "inline": inline,
        "spill": spill,
        "artifacts": artifacts,
    }
    return plan, errors, warnings


def normalize_model_policy(value: Any, *, team_size: int = 1) -> tuple[dict, list[str], list[str]]:
    errors: list[str] = []
    warnings: list[str] = []
    raw = dict(value) if isinstance(value, Mapping) else {}
    routing = _text(raw.get("routing") or "fixed", 40).lower()
    if routing not in ROUTING_MODES:
        errors.append(f"model_policy.routing must be one of: {', '.join(ROUTING_MODES)}")
        routing = "fixed"
    policy = {
        "routing": routing,
        "planner": _text(raw.get("planner") or "inherit", 120),
        "worker": _text(raw.get("worker") or "inherit", 120),
        "verifier": _text(raw.get("verifier") or "inherit", 120),
    }
    if team_size > 1 and policy["verifier"].lower() == "inherit":
        warnings.append("team tasks should use an independent verifier model or "
                        "role rather than inheriting the planner's model")
    return policy, errors, warnings


def normalize_verification(value: Any, *, gates: Iterable[str],
                           acceptance: Iterable[Mapping[str, Any]],
                           acceptance_exception: bool = False) -> tuple[dict, list[str], list[str]]:
    errors: list[str] = []
    warnings: list[str] = []
    raw = dict(value) if isinstance(value, Mapping) else {}
    gate_list = list(gates)

    independent = raw.get("independent", True)
    independent = bool(independent)
    if not independent and not acceptance_exception:
        errors.append("verification.independent must be true unless "
                      "acceptance_exception is declared")

    method = _text(raw.get("method") or "command", 40).lower()
    if method not in VERIFICATION_METHODS:
        errors.append(
            f"verification.method must be one of: {', '.join(VERIFICATION_METHODS)}")
        method = "command"

    evidence = _list(raw.get("evidence"))
    if not evidence:
        evidence = [str(item.get("evidence") or item.get("command") or "").strip()
                    for item in acceptance]
        evidence = [item for item in evidence if item]

    reproduces = [gate for gate in _list(raw.get("reviewer_reproduces"))
                  if gate in gate_list]
    if not reproduces:
        reproduces = ["unit"] if "unit" in gate_list else gate_list[:1]

    if method == "human" and not evidence:
        warnings.append("human verification without named evidence is weak")

    verification = {
        "independent": independent,
        "method": method,
        "evidence": evidence,
        "reviewer_reproduces": reproduces,
    }
    return verification, errors, warnings


def normalize_failure_taxonomy(value: Any) -> tuple[list[str], list[str], list[str]]:
    errors: list[str] = []
    warnings: list[str] = []
    items = _list(value)
    if not items:
        return list(FAILURE_CLASSES), errors, warnings
    unknown = [item for item in items if item not in FAILURE_CLASSES]
    if unknown:
        errors.append(
            f"failure_taxonomy has unknown classes: {', '.join(unknown)}; "
            f"allowed: {', '.join(FAILURE_CLASSES)}")
    ordered = [item for item in FAILURE_CLASSES if item in items]
    return ordered, errors, warnings


def normalize_handoff(value: Any, *, objective: str, deliverables: Iterable[str],
                      dependencies: Iterable[str]) -> tuple[dict, list[str], list[str]]:
    errors: list[str] = []
    warnings: list[str] = []
    raw = dict(value) if isinstance(value, Mapping) else {}
    contract = _text(raw.get("contract") or objective, 2000)
    inputs = _list(raw.get("inputs")) or list(dependencies)
    outputs = _list(raw.get("outputs")) or list(deliverables)
    if not contract:
        errors.append("handoff.contract is required (state what the next agent receives)")
    if not outputs:
        warnings.append("handoff.outputs is empty; the next agent cannot verify delivery")
    handoff = {"contract": contract, "inputs": inputs, "outputs": outputs}
    return handoff, errors, warnings


# --------------------------------------------------------------------------- #
# Discovery
# --------------------------------------------------------------------------- #

def describe(*, skill_ids: Iterable[str] = ()) -> dict:
    """Return the machine-readable interface descriptor."""
    return {
        "format": "ark-agent-interface",
        "interface_version": INTERFACE_VERSION,
        "ark_role": "durable project system (units, links, media, tasks)",
        "principles": list(PRINCIPLES),
        "auth": {
            "scheme": "Bearer",
            "personal_token": {
                "prefix": "arka_",
                "where": "ARK Settings -> Personal -> Agent access",
                "acts_as": "the token owner; writes are attributed agent:<username>",
                "scopes": ["read", "write"],
                "project_header": "X-ARK-Project",
                "whoami": "/api/agent/v1/whoami",
            },
            "token_path": "runtime/projects/.agent-token",
            "token_path_note": "legacy shared server token (admin, attributed agent:api)",
            "scopes": {
                "read": ["/api/agent/v1/whoami", "/api/agent/v1/setup", "/api/agent/v1/context",
                         "/api/agent/v1/project/palette",
                         "/api/agent/v1/units", "/api/agent/v1/units/{id}/comments",
                         "/api/agent/v1/units/{id}/members",
                         "/api/agent/v1/media/{id}/raw", "/api/agent/v1/export",
                         "/api/media/{id}/raw", "/api/export",
                         "/api/agent/v1/insights", "/api/agent/v1/skills"],
                "write": ["/api/agent/v1/units", "/api/agent/v1/units/{id}/comments",
                          "/api/agent/v1/units/{id}/members", "/api/agent/v1/links",
                          "/api/agent/v1/media", "/api/agent/v1/media/upload", "/api/media/upload",
                          "/api/agent/v1/assets/upload", "/api/agent/v1/project/palette",
                          "/api/agent/v1/setup/draft"],
                "task": ["/api/skills/agent-team/tasks"],
            },
            "notes": "Task-session read tokens are read-only and must not be "
                     "used by a coordinator.",
        },
        "bootstrap": {
            "first_call": "GET /api/agent/v1/setup",
            "rule": "If readiness.ready is false or the state is stale, run the setup "
                    "skill (discover -> ask -> draft/propose) and stop before task work.",
            "states": ["unset", "draft", "proposed", "confirmed", "verified", "stale"],
            "human_gate": "POST /api/skills/setup/confirm (agents receive 403)",
            "skill": "setup",
        },
        "capabilities": [dict(item) for item in CAPABILITIES],
        "task_structures": {
            key: dict(value) for key, value in TASK_STRUCTURES.items()
        },
        "topologies": {
            key: dict(value) for key, value in TOPOLOGIES.items()
        },
        "limits": {
            "context": dict(CONTEXT_LIMITS),
            "team": dict(TEAM_LIMITS),
        },
        "failure_classes": [
            {"id": key, "note": FAILURE_CLASS_NOTES[key]} for key in FAILURE_CLASSES
        ],
        "manifest_v2": {
            "top_level": [
                "interface_version", "task_id", "title", "project_id", "phase",
                "status", "owner", "reviewer", "assignee", "repository", "worktree",
                "base_revision", "dependencies", "importance", "budget",
                "objective", "background", "deliverables", "non_goals",
                "owned_paths", "acceptance", "gates", "evidence", "risks",
                "stop_conditions",
            ],
            "new_in_v2": [
                "task_structure", "team", "context", "model_policy",
                "verification", "failure_taxonomy", "handoff",
            ],
            "team": ["mode", "size", "heterogeneous", "rationale", "roles"],
            "context": ["budget_tokens", "inline", "spill", "artifacts"],
            "model_policy": ["routing", "planner", "worker", "verifier"],
            "verification": ["independent", "method", "evidence",
                             "reviewer_reproduces"],
        },
        "ownership": {
            "field": "assignee",
            "value": "ARK account username or null (unassigned)",
            "resolved": "assignee_effective: own assignee, else nearest part_of ancestor's, else null",
            "filter": "assignee=<user>|none|me on units and graph reads (matches assignee_effective)",
            "rule": ("A coordinator working for user X sets assignee X on the branch roots and "
                     "tasks it creates, and does not edit nodes whose assignee_effective is "
                     "another user, apart from linking to them."),
            "distinct_from": "assigned_to links, which attach agent/person units inside the graph",
            "default": ("A create without an assignee key is assigned to the creating account "
                        "(the signed-in user; the local owner without auth). Send assignee "
                        "explicitly when the owner differs, or null to leave it unassigned "
                        "and inheriting."),
        },
        "dates": {
            "start_at": "Defaults to the creation date (UTC) when omitted or null on create.",
            "end_at": ("Defaults to the completion date when status becomes done and end_at is "
                       "empty; a planned end_at (deadline) is kept. Reopening keeps end_at as "
                       "the last completion; clear or change it explicitly."),
            "precedence": "Explicit dates always win; a default that would contradict one is skipped.",
        },
        "conformance": {key: dict(value) for key, value in CONFORMANCE.items()},
        "deprecations": [dict(item) for item in DEPRECATIONS],
        "skills": list(skill_ids),
        "documentation": ["docs/agents/agent-interface.md", "docs/agents/skills.md",
                          "docs/agents/external-agent.md", "docs/guide/node-history.md"],
    }


def validate_brief(payload: Mapping[str, Any] | None) -> dict:
    """Validate a delegation brief before a coordinator spawns a worker.

    A brief is intentionally lighter than a goal manifest: it is what a router
    hands to a worker, not the durable task record.  The same structure and
    budget rules still apply, because most multi-agent failures start here.
    """
    data = dict(payload or {})
    errors: list[str] = []
    warnings: list[str] = []

    objective = _text(data.get("objective") or data.get("goal"), 4000)
    if not objective:
        errors.append("objective is required")

    structure = _text(data.get("task_structure") or "sequential", 40).lower()
    if structure not in TASK_STRUCTURES:
        errors.append(
            f"task_structure must be one of: {', '.join(TASK_STRUCTURES)}")
        structure = "sequential"

    team, team_errors, team_warnings = normalize_team(
        data.get("team") or {"mode": data.get("topology") or "solo",
                             "size": data.get("team_size") or 1,
                             "heterogeneous": data.get("heterogeneous"),
                             "rationale": data.get("team_rationale")},
        task_structure=structure, reviewer=_text(data.get("reviewer")))
    errors.extend(team_errors)
    warnings.extend(team_warnings)

    plan, plan_errors, plan_warnings = normalize_context(data.get("context"))
    errors.extend(plan_errors)
    warnings.extend(plan_warnings)

    owned_paths = _list(data.get("owned_paths"))
    if not owned_paths:
        errors.append("owned_paths must declare at least one file or glob")
    acceptance = data.get("acceptance")
    acceptance_items = acceptance if isinstance(acceptance, (list, tuple)) else (
        [acceptance] if acceptance else [])
    if not acceptance_items:
        errors.append("acceptance must include at least one command or check")
    stop_conditions = _list(data.get("stop_conditions"))
    if not stop_conditions:
        errors.append("stop_conditions must say when the worker stops and escalates")

    verification, verification_errors, verification_warnings = normalize_verification(
        data.get("verification"), gates=data.get("gates") or ["scope", "unit"],
        acceptance=[item if isinstance(item, Mapping) else {"command": item}
                    for item in acceptance_items])
    errors.extend(verification_errors)
    warnings.extend(verification_warnings)

    taxonomy, taxonomy_errors, taxonomy_warnings = normalize_failure_taxonomy(
        data.get("failure_taxonomy"))
    errors.extend(taxonomy_errors)
    warnings.extend(taxonomy_warnings)

    handoff, handoff_errors, handoff_warnings = normalize_handoff(
        data.get("handoff"), objective=objective,
        deliverables=data.get("deliverables") or [],
        dependencies=data.get("dependencies") or [])
    errors.extend(handoff_errors)
    warnings.extend(handoff_warnings)

    brief = {
        "format": "ark-agent-brief",
        "interface_version": INTERFACE_VERSION,
        "objective": objective,
        "task_structure": structure,
        "team": team,
        "context": plan,
        "owned_paths": owned_paths,
        "acceptance": acceptance_items,
        "stop_conditions": stop_conditions,
        "verification": verification,
        "failure_taxonomy": taxonomy,
        "handoff": handoff,
    }
    return {"ok": not errors, "errors": errors, "warnings": warnings,
            "brief": brief}
