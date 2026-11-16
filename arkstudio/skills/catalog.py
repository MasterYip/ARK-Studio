"""Discoverable catalog of ARK's internal skills.

Each entry is an *adapted* skill, not a pointer to a vendor checkout.  The
catalog is the single machine-readable source used by the HTTP API, the CLI, the
Settings panel and the tests.  It records what upstream asset was adapted,
under which license, and what was deliberately left referenced-only.
"""
from __future__ import annotations

import json
from pathlib import Path
from typing import Any, Mapping

from . import agent_team, design, interface, protocol, rrsi, setup

CATALOG_VERSION = "2.1.0"

_ASSETS = Path(__file__).resolve().parent / "assets"

SKILLS: tuple[dict, ...] = (
    {
        "id": setup.SKILL_ID,
        "name": "Environment Setup",
        "version": "ark.1",
        "summary": "First-run binding of an ARK project to its repositories, task spaces, compute, commands and authority policy.",
        "description": (
            "Run once when ARK is enabled in a new work environment, and again when the "
            "environment changes. Agents discover read-only (Git checkouts, existing "
            "agent-team folders, configured GPU server names, installed providers), "
            "draft a profile and ask the human the open questions incrementally. Only a "
            "human confirms the exact profile digest; verify then checks read-only that the "
            "recorded environment exists on this machine. Task work is blocked until the "
            "profile is confirmed. Secrets are stored as references, never values."
        ),
        "tags": ["setup", "onboarding", "environment", "human-in-the-loop", "compute",
                 "repositories", "authority"],
        "source": {
            "upstream": "agent-team-workflow",
            "upstream_ref": "vendored snapshot (no commit recorded)",
            "license": "MIT",
            "copyright": "Copyright (c) 2026 Raymon Yip",
            "adapted": [
                "mandatory setup gate: incremental questions, explicit unknowns, confirmation",
                "setup_answers.yaml / resources_manifest.yaml fields as one ARK environment profile",
                "secret references (env:NAME) instead of secret values",
                "commit/push/destructive/transfer/deployment authority policy",
            ],
            "referenced_only": [
                "init_agent_team.py folder bootstrapper (ARK stores the profile in the .ark project)",
                "SSH host/key configuration (stays in the private GPU server list and ~/.ssh/config)",
            ],
        },
        "documentation": ["SKILL.md"],
        "templates": [
            {"id": "environment.json", "title": "Environment profile", "kind": "json",
             "description": "Repositories, task spaces, compute, environments, storage, gate commands, secret references, team and authority policy."},
            {"id": "setup_summary.md", "title": "Setup summary", "kind": "markdown",
             "description": "The human-readable confirmation summary with digest, readiness and open questions."},
        ],
        "python_api": [
            "setup.discover", "setup.validate_profile", "setup.questions",
            "setup.readiness", "setup.save_draft", "setup.confirm", "setup.verify",
            "setup.record_verification", "setup.check_task", "setup.render_summary",
        ],
        "endpoints": [
            "GET /api/skills/setup/state",
            "POST /api/skills/setup/discover",
            "POST /api/skills/setup/validate",
            "POST /api/skills/setup/draft",
            "POST /api/skills/setup/confirm  (human only)",
            "POST /api/skills/setup/verify",
            "GET /api/agent/v1/setup",
            "POST /api/agent/v1/setup/{discover,draft,verify}",
        ],
        "agent_prompt": (
            "Before planning any work in an ARK project, call GET /api/agent/v1/setup with "
            "a stable X-ARK-Host for this device. Reuse the register and earlier answers. If "
            "readiness.ready is false or this device needs setup, run the setup skill: "
            "POST /api/agent/v1/setup/discover with user-provided paths (or an empty list). "
            "Read configured GPU server names and provider presets before asking questions. "
            "Discovery inspects the ARK server, not the caller's device; verify provided "
            "local paths locally and label observations by source machine. Show known facts "
            "for correction and ask only about missing, conflicting or changed details, "
            "reconciling generated questions with prior answers, one stage at a time (scope, code, "
            "team, compute, commands, authority), record answers and explicit unknowns, then "
            "POST /api/agent/v1/setup/draft with propose: true. Show the user summary_markdown "
            "and its digest and ask them to confirm it themselves (python -m arkstudio skills "
            "setup confirm --digest <digest>, or POST /api/skills/setup/confirm from the ARK "
            "UI). You cannot confirm on their behalf. "
            "After confirmation, POST /api/agent/v1/setup/verify and fix failures. Never "
            "store secret values, never probe GPU servers or run project commands during "
            "setup, and treat every policy marked 'ask' as requiring explicit user approval."
        ),
    },
    {
        "id": agent_team.SKILL_ID,
        "name": "Agent Team Workflow",
        "version": "ark.2",
        "summary": "Bounded tasks, execution shape, context budgets, gates and independent verification for ARK projects.",
        "description": (
            "Plan and execute work as a task graph: one measurable outcome per task, "
            "a project-global task ID, an implementation worktree, an explicit execution "
            "shape (solo, pipeline, router-workers or peer-team), a context budget, "
            "progressive gates, an independent reviewer and durable compact evidence. "
            "ARK task units are the canonical record; the contract never creates a "
            "parallel workspace."
        ),
        "tags": ["planning", "execution", "routing", "context", "gates", "evidence",
                 "verification", "handoff", "monitoring"],
        "source": {
            "upstream": "agent-team-workflow",
            "upstream_ref": "vendored snapshot (no commit recorded)",
            "license": "MIT",
            "copyright": "Copyright (c) 2026 Raymon Yip",
            "adapted": [
                "task ID uniqueness rule (project-global numeric serial)",
                "goal-manifest fields and reviewer independence",
                "phase/status vocabulary projected onto ARK unit status",
                "worktree layout .worktrees/<TASK-ID> and dirty-base guard",
                "progressive gates, evidence retention and inspection report",
                "roles, concurrency and monitoring/recovery contract",
            ],
            "referenced_only": [
                "init_agent_team.py bootstrapper (replaced by ARK project creation + prepare/create_task)",
                "validate_agent_team.py v1 folder validator (replaced by manifest validation)",
                "external workspace v2 modules (not applicable to ARK's portable .ark projects)",
            ],
        },
        "documentation": ["SKILL.md", "operating-scheme.md", "task-lifecycle.md",
                          "routing.md", "context.md", "verification.md"],
        "templates": [
            {"id": "goal_manifest.md", "title": "Goal manifest", "kind": "markdown",
             "description": "Task contract with objective, execution shape, context budget, owned paths, acceptance, gates and evidence."},
            {"id": "inspection_report.md", "title": "Inspection report", "kind": "markdown",
             "description": "Owner report plus independent reviewer decision, failure taxonomy and reproduction record."},
            {"id": "agent_brief.md", "title": "Agent brief", "kind": "markdown",
             "description": "Short delegation brief a router hands to one worker: objective, shape, budget, acceptance and hand-off."},
        ],
        "python_api": [
            "agent_team.prepare_task", "agent_team.create_task",
            "agent_team.validate_manifest", "agent_team.render_goal_manifest",
            "agent_team.render_inspection_report", "agent_team.render_agent_brief",
            "agent_team.task_status_row",
            "interface.describe", "interface.validate_brief",
            "interface.normalize_team", "interface.normalize_context",
            "protocol.next_task_id", "protocol.check_unique_task_id",
        ],
        "endpoints": [
            "GET /api/skills/agent-team",
            "GET /api/skills/interface",
            "POST /api/skills/agent-team/render",
            "POST /api/skills/agent-team/validate",
            "POST /api/skills/agent-team/brief",
            "POST /api/skills/agent-team/tasks",
        ],
        "agent_prompt": (
            "Read GET /api/agent/v1/interface first. Decide the execution shape before "
            "delegating: sequential work stays solo; breadth or sparse long-horizon work "
            "may use router-workers; only adversarial review earns a heterogeneous "
            "peer-team. Call POST /api/skills/agent-team/tasks with title, objective, "
            "owner, reviewer, repository, base_revision, deliverables, non_goals, "
            "owned_paths, acceptance, gates, stop_conditions, task_structure, team, "
            "context, model_policy, verification, failure_taxonomy and handoff (or use "
            "agent_team.prepare_task for a preview, and POST /api/skills/agent-team/brief "
            "to check a worker brief). Do not start implementation until the manifest is "
            "valid. Work in .worktrees/<TASK-ID>, stay inside owned_paths, respect the "
            "context budget, run gates cheapest-first, and record exact commands, "
            "revisions, failure classes and residual risks. A task owner reports "
            "completion; only the named reviewer accepts."
        ),
    },
    {
        "id": rrsi.SKILL_ID,
        "name": "RRSI Harness Search",
        "version": "ark.1",
        "summary": "Regularized harness improvement: edit budget, history, cost rule, noise floor and leakage screen.",
        "description": (
            "Improve prompts, control flow, tools, memory, skills or subagents without "
            "benchmark overfitting. Candidates carry tagged, hypothesised edits; the "
            "selection side applies a noise-adjusted floor, a token-cost rule, a shaped "
            "within-band rule and a structural novelty tie-break. Edit history, stall "
            "exploration and pruning are recorded as JSONL evidence."
        ),
        "tags": ["harness", "self-improvement", "selection", "experiments", "leakage"],
        "source": {
            "upstream": "rrsi",
            "upstream_ref": "google-research/rrsi @ be50316e1db05914068a973f322770ef08ed7ba1",
            "license": "Apache-2.0",
            "copyright": "Copyright 2026 The rrsi Authors / Google LLC",
            "adapted": [
                "rrsi/components.py: component vocabulary K, K_str, text-only recovery and novelty",
                "rrsi/schedule.py: annealed edit budget and budget table",
                "rrsi/selection.py: cost rule, noise floor, admissibility and round winner",
                "rrsi/history.py: tried set, stall flag, exploration and prune set (reimplemented)",
                "rrsi/critic.py denylist idea: ARK leakage scan",
                "rrsi/domain.py adapter split/metric/guard contract: DomainSpec + domain.json",
                "domain SKILL.md/PATTERNS.md role-brief structure, rewritten for ARK tasks",
            ],
            "referenced_only": [
                "benchmark adapters for Terminal-Bench, SWE-bench, Harvey LAB and EngDesign",
                "third_party/harbor_terminus2 and third_party/archipelago harness code",
                "Docker/Vertex/model clients and the multi-candidate git orchestration",
            ],
        },
        "documentation": ["SKILL.md", "PATTERNS.md"],
        "templates": [
            {"id": "proposer.md", "title": "Proposer brief", "kind": "markdown",
             "description": "Round context, edit budget, history, exploration and hard rules."},
            {"id": "analyst.md", "title": "Analyst brief", "kind": "markdown",
             "description": "Turn trials and traces into component-level evidence."},
            {"id": "critic.md", "title": "Critic brief", "kind": "markdown",
             "description": "Screen candidates for task-specific leakage before evaluation."},
            {"id": "domain.json", "title": "Domain descriptor", "kind": "json",
             "description": "Evolvable harness path, evolve/heldout/smoke splits, metric and guards."},
        ],
        "python_api": [
            "rrsi.edit_budget", "rrsi.budget_table", "rrsi.classify_diff",
            "rrsi.normalize_component", "rrsi.novelty", "rrsi.judge",
            "rrsi.select_round", "rrsi.History", "rrsi.scan_leakage",
            "rrsi.build_briefs", "rrsi.validate_round", "rrsi.DomainSpec",
            "rrsi.validate_domain",
        ],
        "endpoints": [
            "GET /api/skills/rrsi",
            "POST /api/skills/rrsi/render",
            "POST /api/skills/rrsi/validate",
            "POST /api/skills/rrsi/round",
            "POST /api/skills/rrsi/history",
        ],
        "agent_prompt": (
            "Use the ARK RRSI contract when changing your own harness. Declare each edit "
            "with component + hypothesis and keep within b_t = ceil(b_min + (b_max-b_min)/2 "
            "*(1+cos(pi*t/T))). Evaluate candidates; apply the noise-adjusted floor "
            "(S' >= S* - delta), the cost rule and the shaped within-band rule. Log "
            "accepted/rejected edits with their component, delta score and delta cost, and "
            "never encode benchmark answers, hidden tests or concrete task IDs."
        ),
    },
    {
        "id": design.SKILL_ID,
        "name": "Academic Design Workflow",
        "version": "ark.1",
        "summary": "Theme-first design protocol, semantic tokens, asset rules and artifact verification for ARK outputs.",
        "description": (
            "One versioned theme (JSON; YAML when PyYAML is present) is the source of "
            "truth for colour, typography, shapes, spacing, charts, web and motion. "
            "Artifacts consume semantic roles, not local magic values, and are verified "
            "for contrast, grayscale survival, self-containment and in-project paths "
            "before they are attached to an ARK artifact unit."
        ),
        "tags": ["design", "figures", "assets", "verification", "theme"],
        "source": {
            "upstream": "academic-design-workflow",
            "upstream_ref": "vendored snapshot v0.1.0",
            "license": "MIT",
            "copyright": "Academic Design Workflow contributors",
            "adapted": [
                "theme-schema.md: systems, semantic roles, variants and deep-merge inheritance",
                "theme.py reference-existence validator and media variant resolution",
                "overall-style-sop.md: semantics before aesthetics, complete style definition",
                "quality-gates.md: shared/paper/website/video gates as data",
                "prefer editable SVG/PDF and separate data processing from presentation",
            ],
            "referenced_only": [
                "pydantic/PyYAML/Matplotlib model and compiler (replaced by stdlib validation)",
                "styleboard generators, After Effects bridge and native Visio co-authoring",
                "vendored upstream themes beyond the example theme",
            ],
        },
        "documentation": ["SKILL.md", "quality-gates.md"],
        "templates": [
            {"id": "theme.json", "title": "Theme contract", "kind": "json",
             "description": "Complete semantic theme with all required systems and roles."},
            {"id": "design_brief.md", "title": "Design brief", "kind": "markdown",
             "description": "Capture audience, medium, qualities and anti-goals before styling."},
            {"id": "style_review.md", "title": "Style review", "kind": "markdown",
             "description": "Review artifacts against shared and medium-specific gates."},
        ],
        "python_api": [
            "design.validate_theme", "design.load_theme", "design.example_theme",
            "design.contrast_ratio", "design.validate_asset_path",
            "design.verify_artifact", "design.render_design_brief",
            "design.render_style_review",
        ],
        "endpoints": [
            "GET /api/skills/design",
            "POST /api/skills/design/render",
            "POST /api/skills/design/validate",
            "POST /api/skills/design/verify",
        ],
        "agent_prompt": (
            "Before producing a figure, website section or video for an ARK artifact, "
            "choose or extend one theme (semantic roles, never appearance names), validate "
            "it with POST /api/skills/design/validate, and create a low-cost structural "
            "candidate first. Keep generated files under the project's assets/, verify "
            "them with POST /api/skills/design/verify before attaching them to an artifact "
            "unit, and report the theme name + version in the inspection report."
        ),
    },
)

_BY_ID = {skill["id"]: skill for skill in SKILLS}
_DOC_NAMES = {"SKILL.md", "operating-scheme.md", "task-lifecycle.md",
              "routing.md", "context.md", "verification.md",
              "PATTERNS.md", "quality-gates.md"}


class SkillError(ValueError):
    """Unknown skill, template or invalid render/validation request."""


def list_skills() -> list[dict]:
    """Summaries for UI/API discovery; no document bodies."""
    out = []
    for skill in SKILLS:
        out.append({
            "id": skill["id"],
            "name": skill["name"],
            "version": skill["version"],
            "summary": skill["summary"],
            "description": skill["description"],
            "tags": list(skill["tags"]),
            "source": {
                "upstream": skill["source"]["upstream"],
                "upstream_ref": skill["source"]["upstream_ref"],
                "license": skill["source"]["license"],
                "copyright": skill["source"]["copyright"],
                "adapted": list(skill["source"]["adapted"]),
                "referenced_only": list(skill["source"]["referenced_only"]),
            },
            "templates": [dict(item) for item in skill["templates"]],
            "python_api": list(skill["python_api"]),
            "endpoints": list(skill["endpoints"]),
            "agent_prompt": skill["agent_prompt"],
        })
    return out


def catalog_payload() -> dict:
    return {
        "format": "ark-skills-catalog",
        "version": CATALOG_VERSION,
        "protocol_version": protocol.PROTOCOL_VERSION,
        "interface": interface.describe(skill_ids=(skill["id"] for skill in SKILLS)),
        "skills": list_skills(),
        "protocol": protocol.protocol_summary(),
        "attribution": "docs/agents/skills-attribution.md",
        "documentation": "docs/agents/skills.md",
        "agent_interface_documentation": "docs/agents/agent-interface.md",
    }


def get_skill(skill_id: str, *, include_docs: bool = True) -> dict:
    skill = _BY_ID.get(str(skill_id or "").strip().lower())
    if not skill:
        raise SkillError(f"unknown skill: {skill_id}")
    detail = dict(skill)
    detail["source"] = dict(skill["source"])
    detail["templates"] = [dict(item) for item in skill["templates"]]
    detail["python_api"] = list(skill["python_api"])
    detail["endpoints"] = list(skill["endpoints"])
    if include_docs:
        detail["docs"] = {
            name: _read_doc(skill["id"], name) for name in skill["documentation"]
        }
    detail["protocol"] = protocol.protocol_summary()
    return detail


def _read_doc(skill_id: str, name: str) -> str:
    if name not in _DOC_NAMES:
        raise SkillError(f"document is not exposed: {name}")
    path = _ASSETS / skill_id / name
    try:
        return path.read_text(encoding="utf-8")
    except OSError as exc:
        raise SkillError(f"missing skill document: {skill_id}/{name}") from exc


def _default_context(skill_id: str, template: str,
                     project: Mapping[str, Any] | None = None) -> dict:
    now = protocol.utc_now()
    if skill_id == setup.SKILL_ID:
        return setup.example_profile()
    if skill_id == agent_team.SKILL_ID:
        example = agent_team.example_task(project)
        example.update({"created_at": now, "ark_unit_uid": example.get("task_id"),
                        "project_name": example.get("project_name") or "ARK project",
                        "interface_version": interface.INTERFACE_VERSION,
                        "context_plan": example.get("context")})
        if template == "agent_brief.md":
            example["parent_task"] = example.get("task_id")
        return example
    if skill_id == rrsi.SKILL_ID:
        return {
            "task_title": (project or {}).get("name") or "Improve the ARK coding-agent harness",
            "incumbent_score": "0.6200", "incumbent_cost": "120000",
            "s_star": "0.6620", "delta": "0.017", "round_index": "0", "budget": "2",
            "components": ", ".join(rrsi.COMPONENTS),
            "structural_components": ", ".join(rrsi.STRUCTURAL_COMPONENTS),
            "prune_set": "_none_", "untried_components": "prompt, control_flow, config",
            "reserved_component": "_none_", "accepted_counts": "{\"skill\": 1}",
            "round_evidence": "candidate-a 0.668 / 122000 tokens; candidate-b 0.640 / 110000 tokens",
            "diff": "+ prompt = render_ark_context(unit)\n- prompt = system_prompt",
        }
    if skill_id == design.SKILL_ID:
        return {
            "project_name": (project or {}).get("name") or "ARK project",
            "audience": "researchers and maintainers",
            "venue": "internal report",
            "mediums": ["paper figure", "project website"],
            "qualities": ["precise", "calm", "technical"],
            "anti_goals": ["decorative gradients", "low-contrast gray text"],
            "theme_name": "academic-clean", "theme_version": "1",
            "artifact": "assets/figure.svg", "mode": "paper",
            "shared_checks": design.QUALITY_GATES["shared"],
            "mode_checks": design.QUALITY_GATES["paper"],
            "verification": "verify_artifact('assets/figure.svg', project_root=...)",
        }
    return {}


def render_template(skill_id: str, template: str, context: Mapping[str, Any] | None = None,
                    *, project: Mapping[str, Any] | None = None) -> dict:
    """Render a skill template with defaults + caller context.

    Returns ``{skill, template, kind, text, context}`` so an agent can inspect
    exactly what was rendered and reuse the context.
    """
    skill = _BY_ID.get(str(skill_id or "").strip().lower())
    if not skill:
        raise SkillError(f"unknown skill: {skill_id}")
    template = str(template or "").strip()
    known = {item["id"]: item for item in skill["templates"]}
    if template not in known:
        raise SkillError(f"unknown template for {skill['id']}: {template}")
    if context is not None and not isinstance(context, Mapping):
        raise SkillError("context must be an object")
    merged = _default_context(skill["id"], template, project)
    if context:
        merged.update(dict(context))
    kind = known[template]["kind"]

    if skill["id"] == setup.SKILL_ID:
        profile, _, _ = setup.normalize_profile(merged)
        merged = profile
        if template == "environment.json":
            text = json.dumps(profile, indent=2, ensure_ascii=False)
        else:
            text = setup.render_summary({"state": "draft", "profile": profile,
                                         "digest": setup.digest(profile)})
    elif skill["id"] == agent_team.SKILL_ID:
        if template == "goal_manifest.md":
            text = agent_team.render_goal_manifest(merged)
        elif template == "agent_brief.md":
            text = agent_team.render_agent_brief(merged)
        else:
            text = agent_team.render_inspection_report(merged)
    elif skill["id"] == rrsi.SKILL_ID:
        if template == "domain.json":
            domain = rrsi.example_domain()
            if context:
                domain.update(dict(context))
            text = json.dumps(domain, indent=2, ensure_ascii=False)
        else:
            role = template.removesuffix(".md")
            text = rrsi.build_briefs(merged)[role]
    else:
        if template == "theme.json":
            if context:
                text = json.dumps(design.deep_merge(design.example_theme(), dict(context)),
                                  indent=2, ensure_ascii=False)
            else:
                text = json.dumps(design.example_theme(), indent=2, ensure_ascii=False)
        elif template == "design_brief.md":
            text = design.render_design_brief(merged)
        else:
            text = design.render_style_review(merged)
    return {
        "skill": skill["id"],
        "template": template,
        "kind": kind,
        "text": text,
        "context": merged,
    }


def validate_payload(skill_id: str, kind: str, payload: Mapping[str, Any] | None,
                     *, project_root: str | Path | None = None,
                     project: Mapping[str, Any] | None = None) -> dict:
    """Dispatch validation to the owning module; always returns diagnostics."""
    skill_id = str(skill_id or "").strip().lower()
    kind = str(kind or "").strip().lower()
    payload = dict(payload or {})
    if skill_id not in _BY_ID:
        raise SkillError(f"unknown skill: {skill_id}")
    try:
        if skill_id == setup.SKILL_ID:
            if kind in {"environment", "profile"}:
                profile = payload.get("profile") if isinstance(payload.get("profile"), Mapping) else payload
                return setup.validate_profile(profile)
            raise SkillError(f"unknown setup validation kind: {kind}")
        if skill_id == agent_team.SKILL_ID:
            if kind in {"task", "request"}:
                return agent_team.prepare_task(
                    payload, existing_ids=payload.get("existing_ids") or [],
                    project=project or {"id": payload.get("project_id"),
                                        "name": payload.get("project_name")})
            if kind in {"manifest", "protocol"}:
                existing = [value for value in (payload.get("existing_ids") or [])
                            if str(value) != str(payload.get("task_id"))]
                return agent_team.validate_manifest(payload, existing_ids=existing)
            if kind in {"brief", "delegation"}:
                return interface.validate_brief(payload)
            raise SkillError(f"unknown agent-team validation kind: {kind}")
        if skill_id == rrsi.SKILL_ID:
            if kind in {"round", "selection"}:
                return {"ok": True, "result": rrsi.validate_round(payload)}
            if kind in {"history", "edits"}:
                return {"ok": True, "result": rrsi.history_snapshot(payload)}
            if kind in {"edit"}:
                return {"ok": True, "result": rrsi.validate_edit(payload)}
            if kind in {"leakage", "leak"}:
                findings = rrsi.scan_leakage(str(payload.get("text") or payload.get("diff") or ""))
                return {"ok": not findings, "findings": findings}
            if kind in {"domain"}:
                return rrsi.validate_domain(payload)
            raise SkillError(f"unknown rrsi validation kind: {kind}")
        if skill_id == design.SKILL_ID:
            if kind in {"theme", "theme.json"}:
                theme = payload.get("theme") if isinstance(payload.get("theme"), Mapping) else payload
                return design.validate_theme(theme, variant=payload.get("variant"))
            if kind == "artifact":
                return design.verify_artifact(
                    payload.get("path") or "", project_root=project_root,
                    kind=str(payload.get("artifact_kind") or "auto"))
            if kind == "asset":
                return design.validate_asset_path(
                    payload.get("path") or "", project_root=project_root)
            raise SkillError(f"unknown design validation kind: {kind}")
        raise SkillError(f"unknown skill: {skill_id}")
    except (protocol.ProtocolError, rrsi.RrsiError, design.DesignError,
            setup.SetupError, ValueError) as exc:
        return {"ok": False, "errors": [str(exc)], "warnings": []}


def agent_prompt(skill_id: str) -> str:
    skill = _BY_ID.get(str(skill_id or "").strip().lower())
    if not skill:
        raise SkillError(f"unknown skill: {skill_id}")
    return skill["agent_prompt"]
