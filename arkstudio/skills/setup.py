"""ARK environment setup: bind a project to its work environment.

An ARK project is portable, but the work it coordinates is not: repositories,
existing task spaces, compute (local GPUs, SSH GPU servers), environments,
storage, gate commands and the human's authority policies all belong to one
machine and one team.  This module is the agent-facing contract for recording
that binding once, when ARK is first enabled in a new environment.

The lifecycle is human-in-the-loop by construction::

    unset -> draft -> proposed -> confirmed -> verified
                \\________________________/        |
                  any edit returns to draft     stale (moved/edited)

* Agents may **discover** (read-only probing), **draft** and **propose**.
* Only a human actor may **confirm**, and only the exact digest they reviewed.
* **verify** runs read-only checks; it never executes project commands,
  contacts GPU servers or reads secret values.

The profile is stored in ``<project>.ark/setup/environment.json`` with an
append-only ``history.jsonl``.  It holds secret *references* (``env:NAME``),
never secret values.
"""
from __future__ import annotations

import hashlib
import json
import os
import platform
import re
import socket
from pathlib import Path
from typing import Any, Callable, Iterable, Mapping

from . import protocol

SKILL_ID = "setup"
SETUP_VERSION = "1.0"
PROFILE_FORMAT = "ark-environment"
PROFILE_DIR = "setup"
PROFILE_FILE = "environment.json"
HISTORY_FILE = "history.jsonl"
TEMPLATES = ("environment.json", "setup_summary.md")

STATES = ("unset", "draft", "proposed", "confirmed", "verified", "stale")
READY_STATES = ("confirmed", "verified")

REPOSITORY_ROLES = ("primary", "code", "paper", "website", "data", "reference")
ACCESS = ("read-write", "read-only")
TASK_SPACE_KINDS = ("ark", "agent-team-folder", "issue-tracker", "other")
COMPUTE_KINDS = ("local", "ssh", "slurm", "cloud")
COMPUTE_USE = ("any", "reserved", "ask")
ENVIRONMENT_KINDS = ("conda", "venv", "uv", "poetry", "docker", "system", "other")
STORAGE_KINDS = ("artifacts", "datasets", "checkpoints", "logs", "media", "other")
POLICY_VALUES = ("allow", "ask", "deny")
POLICY_DEFAULTS = {
    "commit": "allow",
    "push": "ask",
    "merge": "ask",
    "destructive": "ask",
    "process_termination": "ask",
    "gpu_dispatch": "ask",
    "external_message": "ask",
    "deployment": "ask",
    "data_transfer": "ask",
    "purchase": "deny",
}
POLICY_NOTES = {
    "commit": "local commits of reviewed, task-scoped changes",
    "push": "pushing any branch to a remote",
    "merge": "integrating a reviewed task branch into the base",
    "destructive": "deleting or overwriting durable files, branches or worktrees",
    "process_termination": "killing agents, terminals or external jobs",
    "gpu_dispatch": "starting jobs on configured compute",
    "external_message": "email, chat, issue or review comments to other people",
    "deployment": "publishing websites, packages or services",
    "data_transfer": "uploading or copying data off this machine",
    "purchase": "spending money (cloud, APIs, services)",
}
SECRET_REF_RE = re.compile(r"^(env|alias|keychain|ssh-config):[A-Za-z0-9_.\-/]{1,120}$")
_ID_RE = re.compile(r"^[a-z0-9][a-z0-9._-]{0,47}$")
_SECRET_KEY_RE = re.compile(r"(pass(word)?|secret|token|api[_-]?key|private[_-]?key|credential)", re.I)
_SECRET_VALUE_PATTERNS = (
    re.compile(r"-----BEGIN [A-Z ]*PRIVATE KEY-----"),
    re.compile(r"\b(sk|rk|pk)-[A-Za-z0-9_\-]{16,}"),
    re.compile(r"\bgh[pousr]_[A-Za-z0-9]{20,}"),
    re.compile(r"\bAKIA[0-9A-Z]{16}\b"),
    re.compile(r"\bxox[abpr]-[A-Za-z0-9\-]{10,}"),
    re.compile(r"[a-z][a-z0-9+.\-]*://[^/\s:@]+:[^/\s@]+@", re.I),
)
_LOCAL_PATH_RE = re.compile(r"^([A-Za-z]:[\\/]|/|~)")
_PROJECT_MARKERS = (
    "pyproject.toml", "setup.py", "requirements.txt", "environment.yml",
    "environment.yaml", "package.json", "Makefile", "Cargo.toml", "CMakeLists.txt",
    "go.mod", "AGENTS.md", "CLAUDE.md", "README.md",
)
_TASK_SPACE_MARKERS = ("agent_team_init.md", "setup_answers.yaml", "task_status.md")
GIT_TIMEOUT = 15


class SetupError(ValueError):
    """Invalid setup request or lifecycle transition."""

    def __init__(self, message: str, status: int = 400, code: str = "SETUP_INVALID"):
        super().__init__(message)
        self.status = status
        self.code = code


# --------------------------------------------------------------------------- #
# Small helpers
# --------------------------------------------------------------------------- #

def _text(value: Any, limit: int = 2000) -> str:
    return str(value if value is not None else "").strip()[:limit]


def _list(value: Any) -> list:
    if value is None or value == "":
        return []
    if isinstance(value, (list, tuple)):
        return list(value)
    return [value]


def _choice(value: Any, choices: Iterable[str], default: str, field: str,
            errors: list[str]) -> str:
    text = _text(value, 40).lower() or default
    if text not in choices:
        errors.append(f"{field}: '{text}' is not one of {', '.join(choices)}")
        return default
    return text


def _ident(value: Any, fallback: str, field: str, errors: list[str]) -> str:
    text = _text(value, 48).lower() or fallback
    if not _ID_RE.match(text):
        errors.append(f"{field}: id must be lowercase letters, digits, '.', '_' or '-'")
    return text


def machine_fingerprint() -> str:
    """Stable, non-identifying label for 'this machine'."""
    raw = f"{socket.gethostname()}|{platform.system()}|{Path.home()}"
    return hashlib.sha256(raw.encode("utf-8")).hexdigest()[:12]


def is_local_path(value: str) -> bool:
    return bool(_LOCAL_PATH_RE.match(value or ""))


def sanitize_remote(url: str) -> str:
    """Drop user-info (credentials) from a remote URL."""
    return re.sub(r"(://)[^/@\s]+@", r"\1", _text(url, 400))


def find_secret_values(value: Any, path: str = "") -> list[str]:
    """Return JSON paths whose content looks like a secret value."""
    hits: list[str] = []
    if isinstance(value, Mapping):
        for key, item in value.items():
            sub = f"{path}.{key}" if path else str(key)
            if (isinstance(item, str) and item and _SECRET_KEY_RE.search(str(key))
                    and not SECRET_REF_RE.match(item) and str(key) not in {"name"}):
                hits.append(sub)
                continue
            hits.extend(find_secret_values(item, sub))
    elif isinstance(value, list):
        for index, item in enumerate(value):
            hits.extend(find_secret_values(item, f"{path}[{index}]"))
    elif isinstance(value, str):
        if any(pattern.search(value) for pattern in _SECRET_VALUE_PATTERNS):
            hits.append(path or "<value>")
    return hits


# --------------------------------------------------------------------------- #
# Normalisation
# --------------------------------------------------------------------------- #

def _entries(raw: Any, field: str, errors: list[str],
             build: Callable[[Mapping, int], dict]) -> list[dict]:
    out, seen = [], set()
    for index, item in enumerate(_list(raw)[:50]):
        if not isinstance(item, Mapping):
            errors.append(f"{field}[{index}] must be an object")
            continue
        entry = build(item, index)
        if entry["id"] in seen:
            errors.append(f"{field}: duplicate id '{entry['id']}'")
        seen.add(entry["id"])
        out.append(entry)
    return out


# --------------------------------------------------------------------------- #
# Entry builders (shared by the flat 1.0 profile and 2.0 resources)
# --------------------------------------------------------------------------- #

def _build_repository(item: Mapping, i: int, errors: list[str], warnings: list[str]) -> dict:
    path = _text(item.get("path"), 500)
    if not path:
        errors.append(f"repositories[{i}].path is required")
    return {
        "id": _ident(item.get("id"), f"repo-{i + 1}", f"repositories[{i}]", errors),
        "path": path,
        "role": _choice(item.get("role"), REPOSITORY_ROLES, "code",
                        f"repositories[{i}].role", errors),
        "access": _choice(item.get("access"), ACCESS, "read-write",
                          f"repositories[{i}].access", errors),
        "default_branch": _text(item.get("default_branch"), 120),
        "worktree_root": _text(item.get("worktree_root"), 200) or ".worktrees",
        "remote": sanitize_remote(item.get("remote") or ""),
        "instructions": [_text(x, 300) for x in _list(item.get("instructions"))][:20],
        "note": _text(item.get("note"), 500),
    }

def _build_task_space(item: Mapping, i: int, errors: list[str], warnings: list[str]) -> dict:
    return {
        "id": _ident(item.get("id"), f"space-{i + 1}", f"task_spaces[{i}]", errors),
        "kind": _choice(item.get("kind"), TASK_SPACE_KINDS, "other",
                        f"task_spaces[{i}].kind", errors),
        "location": _text(item.get("location") or item.get("path"), 500),
        "access": _choice(item.get("access"), ACCESS, "read-only",
                          f"task_spaces[{i}].access", errors),
        "relation": _text(item.get("relation"), 300) or "reference",
        "note": _text(item.get("note"), 500),
    }

def _build_compute(item: Mapping, i: int, errors: list[str], warnings: list[str]) -> dict:
    kind = _choice(item.get("kind"), COMPUTE_KINDS, "local", f"compute[{i}].kind", errors)
    entry = {
        "id": _ident(item.get("id"), f"compute-{i + 1}", f"compute[{i}]", errors),
        "kind": kind,
        "gpu_server": _text(item.get("gpu_server"), 64),
        "gpus": _text(item.get("gpus"), 200),
        "use": _choice(item.get("use"), COMPUTE_USE, "ask", f"compute[{i}].use", errors),
        "scratch": _text(item.get("scratch"), 500),
        "note": _text(item.get("note"), 500),
    }
    if kind == "ssh" and not entry["gpu_server"]:
        warnings.append(f"compute[{i}]: ssh compute should name a gpu_server from "
                        "the private GPU server list (no hosts or keys here)")
    return entry

def _build_environment(item: Mapping, i: int, errors: list[str], warnings: list[str]) -> dict:
    return {
        "id": _ident(item.get("id"), f"env-{i + 1}", f"environments[{i}]", errors),
        "kind": _choice(item.get("kind"), ENVIRONMENT_KINDS, "other",
                        f"environments[{i}].kind", errors),
        "repository": _text(item.get("repository"), 48).lower(),
        "compute": _text(item.get("compute"), 48).lower(),
        "activate": _text(item.get("activate"), 500),
        "python": _text(item.get("python"), 40),
        "note": _text(item.get("note"), 500),
    }

def _build_storage(item: Mapping, i: int, errors: list[str], warnings: list[str]) -> dict:
    return {
        "id": _ident(item.get("id"), f"store-{i + 1}", f"storage[{i}]", errors),
        "kind": _choice(item.get("kind"), STORAGE_KINDS, "other",
                        f"storage[{i}].kind", errors),
        "location": _text(item.get("location") or item.get("path"), 500),
        "retention": _text(item.get("retention"), 300),
        "note": _text(item.get("note"), 500),
    }

def _build_command(item: Mapping, i: int, errors: list[str], warnings: list[str]) -> dict:
    gate = _text(item.get("gate"), 40).lower()
    if gate and gate not in protocol.GATE_IDS:
        errors.append(f"commands[{i}].gate: '{gate}' is not one of "
                      f"{', '.join(protocol.GATE_IDS)}")
    run = _text(item.get("run"), 1000)
    if not run:
        errors.append(f"commands[{i}].run is required")
    return {
        "id": _ident(item.get("id"), f"cmd-{i + 1}", f"commands[{i}]", errors),
        "run": run,
        "repository": _text(item.get("repository"), 48).lower(),
        "environment": _text(item.get("environment"), 48).lower(),
        "cwd": _text(item.get("cwd"), 300),
        "gate": gate,
        "mandatory": bool(item.get("mandatory", gate in {"static", "unit"})),
        "expensive": bool(item.get("expensive", gate in {"experiment", "publication"})),
        "proposed": bool(item.get("proposed", False)),
    }

def _build_secret(item: Mapping, i: int, errors: list[str], warnings: list[str]) -> dict:
    ref = _text(item.get("ref"), 160)
    if not SECRET_REF_RE.match(ref):
        errors.append(f"secrets[{i}].ref must be a reference such as env:NAME, "
                      "alias:name or ssh-config:host; never a value")
    return {
        "id": _ident(item.get("id"), f"secret-{i + 1}", f"secrets[{i}]", errors),
        "ref": ref,
        "purpose": _text(item.get("purpose"), 300),
    }


ENTRY_BUILDERS = {
    "repository": _build_repository, "task_space": _build_task_space,
    "compute": _build_compute, "environment": _build_environment,
    "storage": _build_storage, "command": _build_command, "secret": _build_secret,
}
# 1.0 list field per resource kind.
KIND_FIELDS = {
    "repository": "repositories", "task_space": "task_spaces", "compute": "compute",
    "environment": "environments", "storage": "storage", "command": "commands",
    "secret": "secrets",
}


def normalize_entry(kind: str, item: Mapping, index: int = 0) -> tuple[dict, list[str], list[str]]:
    """Normalise one 1.0-shaped entry of ``kind``; returns ``(entry, errors, warnings)``."""
    errors: list[str] = []
    warnings: list[str] = []
    entry = ENTRY_BUILDERS[kind](item, index, errors, warnings)
    return entry, errors, warnings


def normalize_profile(profile: Mapping[str, Any] | None) -> tuple[dict, list[str], list[str]]:
    """Return ``(profile, errors, warnings)`` with defaults applied."""
    profile = dict(profile or {})
    errors: list[str] = []
    warnings: list[str] = []

    repositories = _entries(profile.get("repositories"), "repositories", errors,
        lambda item, i: _build_repository(item, i, errors, warnings))
    out = {
        "format": PROFILE_FORMAT,
        "setup_version": SETUP_VERSION,
        "objective": _text(profile.get("objective"), 4000),
        "non_goals": [_text(x, 500) for x in _list(profile.get("non_goals"))][:30],
        "repositories": repositories,
        "task_spaces": _entries(profile.get("task_spaces"), "task_spaces", errors,
        lambda item, i: _build_task_space(item, i, errors, warnings)),
        "compute": _entries(profile.get("compute"), "compute", errors,
        lambda item, i: _build_compute(item, i, errors, warnings)),
        "environments": _entries(profile.get("environments"), "environments", errors,
        lambda item, i: _build_environment(item, i, errors, warnings)),
        "storage": _entries(profile.get("storage"), "storage", errors,
        lambda item, i: _build_storage(item, i, errors, warnings)),
        "commands": _entries(profile.get("commands"), "commands", errors,
        lambda item, i: _build_command(item, i, errors, warnings)),
        "secrets": _entries(profile.get("secrets"), "secrets", errors,
        lambda item, i: _build_secret(item, i, errors, warnings)),
    }

    agents = profile.get("agents") if isinstance(profile.get("agents"), Mapping) else {}
    concurrency = agents.get("concurrency", 3)
    if str(concurrency).lower() != "unlimited":
        try:
            concurrency = max(1, min(int(concurrency), 1000))
        except (TypeError, ValueError):
            errors.append("agents.concurrency must be a positive integer or 'unlimited'")
            concurrency = 3
    else:
        concurrency = "unlimited"
    try:
        context_tokens = max(8_000, min(int(agents.get("default_context_tokens") or 120_000),
                                        2_000_000))
    except (TypeError, ValueError):
        errors.append("agents.default_context_tokens must be an integer")
        context_tokens = 120_000
    out["agents"] = {
        "coordinator": _text(agents.get("coordinator"), 60),
        "providers": [_text(x, 40) for x in _list(agents.get("providers"))][:20],
        "concurrency": concurrency,
        "default_reviewer": _text(agents.get("default_reviewer"), 80),
        "default_context_tokens": context_tokens,
    }

    raw_policies = profile.get("policies") if isinstance(profile.get("policies"), Mapping) else {}
    policies, defaulted = {}, []
    for key, default in POLICY_DEFAULTS.items():
        if key in raw_policies:
            policies[key] = _choice(raw_policies.get(key), POLICY_VALUES, default,
                                    f"policies.{key}", errors)
        else:
            policies[key] = default
            defaulted.append(key)
    for key in raw_policies:
        if key not in POLICY_DEFAULTS:
            warnings.append(f"policies.{key} is not a known policy and is ignored")
    out["policies"] = policies
    out["policies_defaulted"] = defaulted

    cadence = profile.get("cadence") if isinstance(profile.get("cadence"), Mapping) else {}
    try:
        minutes = max(1, min(int(cadence.get("status_minutes") or 10), 1440))
    except (TypeError, ValueError):
        errors.append("cadence.status_minutes must be an integer")
        minutes = 10
    out["cadence"] = {
        "status_minutes": minutes,
        "task_trigger": _text(cadence.get("task_trigger"), 500),
    }
    ids = profile.get("task_ids") if isinstance(profile.get("task_ids"), Mapping) else {}
    prefixes = [_text(x, 20).upper() for x in _list(ids.get("prefixes"))][:30] or ["T"]
    for prefix in prefixes:
        if not re.match(r"^[A-Z][A-Z0-9]*(?:-[A-Z][A-Z0-9]*)*$", prefix):
            errors.append(f"task_ids.prefixes: '{prefix}' is not a valid prefix")
    out["task_ids"] = {"prefixes": prefixes, "scope": "project-global"}

    unknowns = []
    for index, item in enumerate(_list(profile.get("unknowns"))[:50]):
        if isinstance(item, Mapping):
            unknowns.append({"field": _text(item.get("field"), 120),
                             "blocks": _text(item.get("blocks"), 300),
                             "owner": _text(item.get("owner"), 80) or "user"})
        else:
            unknowns.append({"field": _text(item, 120), "blocks": "", "owner": "user"})
    out["unknowns"] = unknowns
    skipped = []
    for item in _list(profile.get("skipped")):
        stage = _text(item, 20).lower()
        if stage not in STAGE_IDS:
            errors.append(f"skipped: '{stage}' is not one of {', '.join(STAGE_IDS)}")
        elif stage not in skipped:
            skipped.append(stage)
    out["skipped"] = skipped
    out["notes"] = _text(profile.get("notes"), 4000)

    # Cross references.
    repo_ids = {item["id"] for item in repositories}
    env_ids = {item["id"] for item in out["environments"]}
    compute_ids = {item["id"] for item in out["compute"]}
    for item in out["environments"]:
        if item["repository"] and item["repository"] not in repo_ids:
            errors.append(f"environments.{item['id']}: unknown repository '{item['repository']}'")
        if item["compute"] and item["compute"] not in compute_ids:
            errors.append(f"environments.{item['id']}: unknown compute '{item['compute']}'")
    for item in out["commands"]:
        if item["repository"] and item["repository"] not in repo_ids:
            errors.append(f"commands.{item['id']}: unknown repository '{item['repository']}'")
        if item["environment"] and item["environment"] not in env_ids:
            errors.append(f"commands.{item['id']}: unknown environment '{item['environment']}'")
    primaries = [item for item in repositories if item["role"] == "primary"]
    for item in primaries:
        if item["access"] == "read-only":
            warnings.append(f"repositories.{item['id']}: this primary repository is read-only; "
                            "agents will not be able to implement changes there")

    leaked = find_secret_values(profile)
    for hit in leaked:
        errors.append(f"{hit}: looks like a secret value; store a reference (env:NAME) instead")
    return out, errors, warnings


def validate_profile(profile: Mapping[str, Any] | None) -> dict:
    normalized, errors, warnings = normalize_profile(profile)
    return {
        "ok": not errors,
        "errors": errors,
        "warnings": warnings,
        "profile": normalized,
        "digest": digest(normalized),
        "questions": questions(normalized),
        "readiness": readiness(normalized, "draft"),
    }


def digest(profile: Mapping[str, Any]) -> str:
    """Content digest the human confirms; independent of key order."""
    data = json.dumps(profile, sort_keys=True, ensure_ascii=False, separators=(",", ":"))
    return hashlib.sha256(data.encode("utf-8")).hexdigest()[:16]


# --------------------------------------------------------------------------- #
# Questions and readiness
# --------------------------------------------------------------------------- #

STAGES = (
    ("scope", "Objective and non-goals"),
    ("code", "Repositories, branches and integration"),
    ("team", "Task spaces, roles and concurrency"),
    ("compute", "Compute, environments, storage and secret references"),
    ("commands", "Setup, test, smoke, experiment and deploy commands"),
    ("authority", "Commit, push, destructive, dispatch and external-action policy"),
)
STAGE_IDS = tuple(stage for stage, _ in STAGES)
SKIP_EFFECT = {
    "scope": ("plan", "objective skipped by the user"),
    "code": ("implement", "codebase setup skipped by the user"),
    "team": (None, "team defaults apply (agents ask the user to review)"),
    "compute": ("gpu", "compute setup skipped by the user"),
    "commands": ("gates", "gate commands skipped; agents must ask for test commands per task"),
    "authority": (None, "authority defaults apply (every risky action asks the user)"),
}


def questions(profile: Mapping[str, Any]) -> list[dict]:
    """Ordered, incremental questions for the human; answered ones are omitted."""
    out: list[dict] = []

    def ask(stage, field, prompt, blocks, required=True):
        out.append({"stage": stage, "field": field, "prompt": prompt,
                    "blocks": blocks, "required": required})

    if not profile.get("objective"):
        ask("scope", "objective", "What is the measurable research objective for this environment?",
            "planning and every task manifest")
    if not profile.get("non_goals"):
        ask("scope", "non_goals", "What is explicitly out of scope?",
            "scope gates and owned-path review", required=False)
    repositories = profile.get("repositories") or []
    if not repositories:
        ask("code", "repositories", "Which repositories/codebases does this project use, and which "
            "ones are primary (where implementation tasks land)?", "worktrees and implementation tasks")
    elif not any(item.get("role") == "primary" for item in repositories):
        ask("code", "repositories[].role", "Which repository is primary?",
            "default task repository and integration", required=False)
    for item in repositories:
        if not item.get("default_branch"):
            ask("code", f"repositories.{item.get('id')}.default_branch",
                f"Which branch is the integration base for '{item.get('id')}'?",
                "base_revision in task manifests")
    if not profile.get("task_spaces"):
        ask("team", "task_spaces", "Are there existing task spaces (agent-team folders, issue "
            "trackers, other ARK projects) to import or reference?",
            "task-ID uniqueness against historical work", required=False)
    agents = profile.get("agents") or {}
    if not agents.get("providers"):
        ask("team", "agents.providers", "Which coding agents will work on this project (DSH, Codex, "
            "Claude Code, OpenCode...), and with which models or permission levels? ARK's "
            "Settings -> Providers shows what is installed here.",
            "task-session launch and model routing", required=False)
    if not agents.get("coordinator"):
        ask("team", "agents.coordinator", "Which agent coordinates the team (DSH, Codex, Claude Code, "
            "a human)?", "delegation and monitoring", required=False)
    if not agents.get("default_reviewer"):
        ask("team", "agents.default_reviewer", "Who accepts work by default (a person or an "
            "independent reviewer agent)?", "independent acceptance")
    if not (profile.get("cadence") or {}).get("task_trigger"):
        ask("team", "cadence.task_trigger", "When should an agent open a formal task record?",
            "task creation", required=False)
    if not profile.get("compute"):
        ask("compute", "compute", "Which compute may agents use (this machine, GPU servers, a "
            "cluster)? GPU servers come from ARK's private server list ($ARK_GPU_SERVERS_CONFIG, "
            "$ARK_PRIVATE_DIR/gpu_servers.yaml or ~/.ark/gpu_servers.yaml; see docs/operations/gpu.md). "
            "Name servers from that list; never paste hosts with credentials or keys.",
            "experiment gates and GPU dispatch", required=False)
    if not profile.get("environments"):
        ask("compute", "environments", "How is each codebase's environment activated "
            "(conda/venv/uv/docker)?", "reproducible gate commands", required=False)
    if not any(item.get("kind") == "artifacts" for item in profile.get("storage") or []):
        ask("compute", "storage", "Where do bulk artifacts (logs, checkpoints, media) live "
            "outside Git?", "evidence retention", required=False)
    commands = profile.get("commands") or []
    if not any(item.get("gate") == "unit" for item in commands):
        ask("commands", "commands[gate=unit]", "What is the fast deterministic test command?",
            "the unit gate for implementation tasks")
    if any(item.get("proposed") for item in commands):
        ask("commands", "commands[].proposed", "Confirm or correct the commands ARK discovered "
            "(marked proposed).", "gates", required=False)
    defaulted = profile.get("policies_defaulted") or []
    if defaulted:
        ask("authority", "policies", "Confirm the authority policy defaults: " + ", ".join(
            f"{key}={POLICY_DEFAULTS[key]}" for key in defaulted) + ".",
            "any action beyond local edits")
    skipped = set(profile.get("skipped") or [])
    return [item for item in out if item["stage"] not in skipped]


def readiness(profile: Mapping[str, Any], state: str) -> dict:
    """What an agent may do now, and what is blocked and why."""
    blocked: list[dict] = []
    confirmed = state in READY_STATES
    skipped = [stage for stage in profile.get("skipped") or [] if stage in SKIP_EFFECT]
    for stage in skipped:
        capability, reason = SKIP_EFFECT[stage]
        if capability:
            blocked.append({"capability": capability, "reason": reason, "skipped": True})
    repositories = profile.get("repositories") or []
    primaries = [item for item in repositories if item.get("role") == "primary"]
    primary = primaries[0] if primaries else (repositories[0] if repositories else None)
    if not confirmed:
        blocked.append({"capability": "tasks", "reason": f"setup is '{state}'; a human must "
                        "confirm the environment profile before task work starts"})
    if not profile.get("objective"):
        blocked.append({"capability": "plan", "reason": "no objective recorded"})
    if not primaries:
        blocked.append({"capability": "implement", "reason": "no primary repository registered"})
    elif not any(item.get("access") == "read-write" for item in primaries):
        blocked.append({"capability": "implement", "reason": "all primary repositories are read-only"})
    if not any(item.get("gate") == "unit" for item in profile.get("commands") or []):
        blocked.append({"capability": "gates", "reason": "no unit-gate command recorded"})
    policies = profile.get("policies") or {}
    if not profile.get("compute"):
        blocked.append({"capability": "gpu", "reason": "no compute registered"})
    elif policies.get("gpu_dispatch") == "deny":
        blocked.append({"capability": "gpu", "reason": "gpu_dispatch policy is deny"})
    capabilities = ("plan", "tasks", "implement", "gates", "gpu")
    # A skipped area reports only its skip reason, not the follow-on "missing" reason.
    seen: set[str] = set()
    blocked = [item for item in blocked
               if not (item["capability"] in seen or seen.add(item["capability"]))]
    blocked_ids = {item["capability"] for item in blocked}
    return {
        "state": state,
        # Ready once confirmed and nothing essential is *missing*; a skip is a choice.
        "ready": confirmed and not any(item["capability"] in {"plan", "tasks"}
                                       and not item.get("skipped") for item in blocked),
        "skipped": skipped,
        "notes": [SKIP_EFFECT[stage][1] for stage in skipped if not SKIP_EFFECT[stage][0]],
        "allowed": [cap for cap in capabilities if cap not in blocked_ids],
        "blocked": blocked,
        "requires_human": {key: value for key, value in policies.items() if value == "ask"},
        "denied": sorted(key for key, value in policies.items() if value == "deny"),
        "primary_repository": primary.get("id") if primary else "",
        "primary_repositories": [item.get("id") for item in primaries if item.get("id")],
    }


# --------------------------------------------------------------------------- #
# Discovery (read-only)
# --------------------------------------------------------------------------- #

def _git(args: list[str], cwd: Path) -> dict:
    from ..worktrees import run_git_command
    try:
        return run_git_command(args, cwd)
    except Exception as exc:  # noqa: BLE001 - discovery never raises on git errors
        return {"returncode": 1, "stdout": "", "stderr": str(exc)}


def _git_out(args: list[str], cwd: Path) -> str:
    result = _git(args, cwd)
    return str(result.get("stdout") or "").strip() if result.get("returncode") == 0 else ""


def inspect_repository(path: str | Path) -> dict:
    """Read-only facts about one candidate codebase."""
    target = Path(str(path)).expanduser()
    info: dict[str, Any] = {"path": str(target), "exists": target.is_dir(), "git": False}
    if not target.is_dir():
        return info
    top = _git_out(["rev-parse", "--show-toplevel"], target)
    markers = [name for name in _PROJECT_MARKERS if (target / name).exists()]
    info.update({"markers": markers, "has_tests": (target / "tests").is_dir()})
    if not top:
        return info
    root = Path(top)
    status = _git_out(["status", "--porcelain"], root)
    worktrees = _git_out(["worktree", "list", "--porcelain"], root)
    exclude = root / ".git" / "info" / "exclude"
    try:
        excluded = ".worktrees" in exclude.read_text(encoding="utf-8", errors="replace")
    except OSError:
        excluded = False
    remote = _git_out(["remote", "get-url", "origin"], root)
    default = _git_out(["symbolic-ref", "--short", "refs/remotes/origin/HEAD"], root)
    info.update({
        "git": True,
        "root": str(root),
        "name": root.name,
        "branch": _git_out(["rev-parse", "--abbrev-ref", "HEAD"], root),
        "head": _git_out(["rev-parse", "--short", "HEAD"], root),
        "default_branch": default.split("/", 1)[-1] if default else "",
        "dirty_files": len([line for line in status.splitlines() if line.strip()]),
        "worktrees": worktrees.count("worktree "),
        "worktrees_excluded": excluded,
        "remote": sanitize_remote(remote),
        "markers": [name for name in _PROJECT_MARKERS if (root / name).exists()],
        "has_tests": (root / "tests").is_dir(),
    })
    return info


def suggest_commands(info: Mapping[str, Any], repo_id: str) -> list[dict]:
    """Heuristic, *proposed* gate commands; a human confirms them."""
    root = Path(str(info.get("root") or info.get("path") or ""))
    markers = set(info.get("markers") or [])
    out: list[dict] = []
    if "pyproject.toml" in markers or "setup.py" in markers or "requirements.txt" in markers:
        out.append({"id": f"{repo_id}-compile", "run": "python -m compileall -q .",
                    "repository": repo_id, "gate": "static", "proposed": True})
        if info.get("has_tests"):
            pytest = False
            try:
                pytest = "pytest" in (root / "pyproject.toml").read_text(
                    encoding="utf-8", errors="replace")
            except OSError:
                pass
            run = "python -m pytest -q" if pytest else "python -m unittest discover -s tests"
            out.append({"id": f"{repo_id}-unit", "run": run, "repository": repo_id,
                        "gate": "unit", "proposed": True})
    if "package.json" in markers:
        try:
            scripts = json.loads((root / "package.json").read_text(encoding="utf-8")).get(
                "scripts") or {}
        except (OSError, ValueError):
            scripts = {}
        for name, gate in (("lint", "static"), ("test", "unit"), ("build", "component")):
            if name in scripts:
                out.append({"id": f"{repo_id}-{name}", "run": f"npm run {name}",
                            "repository": repo_id, "gate": gate, "proposed": True})
    if "Makefile" in markers:
        try:
            makefile = (root / "Makefile").read_text(encoding="utf-8", errors="replace")
        except OSError:
            makefile = ""
        if re.search(r"^test:", makefile, re.M) and not any(c["gate"] == "unit" for c in out):
            out.append({"id": f"{repo_id}-unit", "run": "make test", "repository": repo_id,
                        "gate": "unit", "proposed": True})
    if "Cargo.toml" in markers:
        out.append({"id": f"{repo_id}-unit", "run": "cargo test", "repository": repo_id,
                    "gate": "unit", "proposed": True})
    return out


def find_task_spaces(path: str | Path, *, depth: int = 3) -> list[str]:
    """Locate existing agent-team folders under ``path`` (bounded walk)."""
    root = Path(str(path)).expanduser()
    found: list[str] = []
    if not root.is_dir():
        return found
    skip = {".git", "node_modules", ".worktrees", "__pycache__", ".venv", "venv"}

    def walk(folder: Path, level: int) -> None:
        if level > depth or len(found) >= 20:
            return
        try:
            names = {child.name for child in folder.iterdir()}
        except OSError:
            return
        if any(marker in names for marker in _TASK_SPACE_MARKERS):
            found.append(str(folder))
            return
        for name in sorted(names):
            child = folder / name
            if name not in skip and not name.startswith(".") and child.is_dir():
                walk(child, level + 1)

    walk(root, 0)
    return found


def discover(paths: Iterable[str] = (), *, gpu_servers: Mapping[str, Any] | None = None,
             providers: Mapping[str, Any] | None = None,
             project: Mapping[str, Any] | None = None) -> dict:
    """Probe candidate paths read-only and return a draft profile plus questions.

    Nothing is written, executed in a project, or sent over the network.  GPU
    servers come from the already-loaded private list (names only).
    """
    observations: list[dict] = []
    repositories: list[dict] = []
    task_spaces: list[dict] = []
    commands: list[dict] = []
    seen_roots: set[str] = set()
    candidates = [str(p) for p in paths if str(p or "").strip()][:20]
    project_path = str((project or {}).get("path") or "")
    if project_path and not candidates:
        candidates = [str(Path(project_path).parent)]
    for raw in candidates:
        info = inspect_repository(raw)
        observations.append({"kind": "path", **info})
        if info.get("git"):
            root = info["root"]
            if root not in seen_roots:
                seen_roots.add(root)
                repo_id = re.sub(r"[^a-z0-9._-]+", "-", info["name"].lower()).strip("-")[:48] or \
                    f"repo-{len(repositories) + 1}"
                repositories.append({
                    "id": repo_id, "path": root,
                    "role": "primary" if not repositories else "code",
                    "access": "read-write",
                    "default_branch": info.get("default_branch") or info.get("branch") or "",
                    "remote": info.get("remote", ""),
                    "instructions": [name for name in ("AGENTS.md", "CLAUDE.md")
                                     if name in info.get("markers", [])],
                })
                commands.extend(suggest_commands(info, repo_id))
                if info.get("dirty_files"):
                    observations.append({"kind": "warning", "detail":
                                         f"{repo_id} has {info['dirty_files']} uncommitted "
                                         "file(s); do not branch task worktrees from it yet"})
                if info.get("worktrees", 0) > 1 and not info.get("worktrees_excluded"):
                    observations.append({"kind": "warning", "detail":
                                         f"{repo_id}: .worktrees/ is not in .git/info/exclude"})
        for folder in find_task_spaces(raw):
            if all(item["location"] != folder for item in task_spaces):
                task_spaces.append({"id": f"space-{len(task_spaces) + 1}",
                                    "kind": "agent-team-folder", "location": folder,
                                    "access": "read-only", "relation": "import-history"})
    compute = [{"id": "local", "kind": "local", "use": "any",
                "note": f"{platform.system()} host running ARK"}]
    for server in (gpu_servers or {}).get("servers") or []:
        name = str(server.get("name") or "").strip()
        if name:
            compute.append({"id": re.sub(r"[^a-z0-9._-]+", "-", name.lower())[:48],
                            "kind": "ssh", "gpu_server": name, "use": "ask",
                            "note": ", ".join(server.get("tags") or [])})
    gpu_note = None
    if gpu_servers is not None and not (gpu_servers.get("servers") or []):
        gpu_note = gpu_servers.get("hint") or "no GPU servers configured"
        observations.append({"kind": "gpu", "detail": gpu_note})
    installed = []
    for pid, item in ((providers or {}).get("providers") or {}).items():
        if item.get("installed"):
            installed.append(pid)
    draft = {
        "repositories": repositories,
        "task_spaces": task_spaces,
        "compute": compute,
        "commands": commands,
        "agents": {"providers": installed},
        "unknowns": [],
    }
    normalized, errors, warnings = normalize_profile(draft)
    return {
        "ok": not errors,
        "errors": errors,
        "warnings": warnings,
        "observations": observations,
        "draft": normalized,
        "questions": questions(normalized),
        "note": "Discovery is read-only and heuristic. Commands are marked proposed; "
                "a human must confirm the profile before task work starts.",
    }


# --------------------------------------------------------------------------- #
# Verification (read-only)
# --------------------------------------------------------------------------- #

def verify(profile: Mapping[str, Any], *, gpu_servers: Mapping[str, Any] | None = None,
           environ: Mapping[str, str] | None = None) -> dict:
    """Read-only checks that the recorded environment still exists here."""
    environ = os.environ if environ is None else environ
    checks: list[dict] = []

    def check(cid: str, status: str, detail: str, mandatory: bool = True) -> None:
        checks.append({"id": cid, "status": status, "detail": detail, "mandatory": mandatory})

    for repo in profile.get("repositories") or []:
        cid = f"repository:{repo['id']}"
        info = inspect_repository(repo["path"])
        if not info.get("exists"):
            check(cid, "fail", f"path does not exist: {repo['path']}")
            continue
        if not info.get("git"):
            check(cid, "fail" if repo.get("access") == "read-write" else "warn",
                  "not a Git checkout", mandatory=repo.get("access") == "read-write")
            continue
        check(cid, "pass", f"{info['name']} @ {info.get('branch')} {info.get('head')}")
        branch = repo.get("default_branch")
        if branch:
            ok = _git(["rev-parse", "--verify", "--quiet", branch], Path(info["root"]))
            check(f"{cid}:branch", "pass" if ok.get("returncode") == 0 else "fail",
                  f"base branch '{branch}'" + ("" if ok.get("returncode") == 0 else " not found"))
        if repo.get("access") == "read-write":
            check(f"{cid}:worktrees-excluded",
                  "pass" if info.get("worktrees_excluded") else "warn",
                  ".worktrees/ in .git/info/exclude" if info.get("worktrees_excluded")
                  else "add '.worktrees/' to .git/info/exclude before the first task worktree",
                  mandatory=False)
            if info.get("dirty_files"):
                check(f"{cid}:clean", "warn", f"{info['dirty_files']} uncommitted file(s) "
                      "in the primary checkout", mandatory=False)
    for space in profile.get("task_spaces") or []:
        location = space.get("location") or ""
        if is_local_path(location):
            exists = Path(location).expanduser().exists()
            check(f"task_space:{space['id']}", "pass" if exists else "warn",
                  location if exists else f"missing: {location}", mandatory=False)
    names = {str(item.get("name")) for item in (gpu_servers or {}).get("servers") or []}
    for item in profile.get("compute") or []:
        if item.get("kind") == "ssh":
            if gpu_servers is None:
                check(f"compute:{item['id']}", "skip", "GPU server list not consulted",
                      mandatory=False)
            elif item.get("gpu_server") in names:
                check(f"compute:{item['id']}", "pass",
                      f"'{item['gpu_server']}' is in the private server list (not probed)")
            else:
                check(f"compute:{item['id']}", "fail",
                      f"'{item.get('gpu_server')}' is not in the private GPU server list")
    for item in profile.get("storage") or []:
        location = item.get("location") or ""
        if is_local_path(location):
            exists = Path(location).expanduser().is_dir()
            check(f"storage:{item['id']}", "pass" if exists else "warn",
                  location if exists else f"missing directory: {location}", mandatory=False)
    for item in profile.get("secrets") or []:
        ref = item.get("ref") or ""
        if ref.startswith("env:"):
            present = bool(environ.get(ref[4:]))
            check(f"secret:{item['id']}", "pass" if present else "warn",
                  f"{ref} is {'set' if present else 'not set'} (value not read)", mandatory=False)
    check("commands", "skip", "verify never executes project commands; run the smoke "
          "or unit gate inside a task", mandatory=False)
    failed = [item for item in checks if item["status"] == "fail" and item["mandatory"]]
    return {"ok": not failed, "checks": checks, "machine": machine_fingerprint(),
            "verified_at": protocol.utc_now()}


# --------------------------------------------------------------------------- #
# Durable record and lifecycle
# --------------------------------------------------------------------------- #

def _layers():
    from . import setup_layers  # local import: setup_layers builds on this module
    return setup_layers


def _cli_viewer():
    """The local terminal / in-process caller: project-wide view, trusted as admin."""
    return _layers().Viewer(user="", admin=True, token_kind="cli")


def load(project_path: str | Path) -> dict:
    """1.0-compatible project-wide record (format 2.0 project layer, flat view).

    ``profile`` is the project-wide view for this machine: shared project
    resources with their location here.  Multi-user callers use
    :func:`arkstudio.skills.setup_layers.payload` instead.
    """
    layers = _layers()
    view = layers.resolve(project_path, layers.legacy_viewer())
    flat, _ = layers.flat_view(view)
    record = view["project"]
    out = {key: record.get(key) for key in ("format", "setup_version", "state", "digest",
                                           "confirmation", "verification", "updated_at",
                                           "updated_by", "stale_reason")
           if key in record}
    out["profile"] = flat
    return out


def state_payload(project_path: str | Path) -> dict:
    layers = _layers()
    return layers.payload(project_path, layers.legacy_viewer())


def save_draft(project_path: str | Path, profile: Mapping[str, Any] | None, *,
               actor: str, propose: bool = False) -> dict:
    """Draft (or propose) the project layer from a 1.0 flat profile."""
    layers = _layers()
    result = layers.save_layer(project_path, profile, viewer=layers.legacy_viewer(),
                               layer="project", actor=actor, propose=propose)
    payload = state_payload(project_path)
    payload.update({"ok": result["ok"], "errors": result["errors"],
                    "warnings": result["warnings"]})
    return payload


def is_human_actor(actor: str) -> bool:
    return str(actor or "").startswith("human:")


def confirm(project_path: str | Path, *, actor: str, digest_value: str,
            note: str = "") -> dict:
    """Human-only: confirm exactly the project-layer digest that was reviewed.

    In-process / CLI callers are trusted as project admins; the HTTP server
    checks admin permission itself and uses ``setup_layers.confirm_layer``.
    """
    if not is_human_actor(actor):
        raise SetupError("only a human can confirm the environment profile; agents may "
                         "draft and propose", 403, "SETUP_HUMAN_REQUIRED")
    layers = _layers()
    layers.confirm_layer(project_path, viewer=_cli_viewer(), layer="project",
                         actor=actor, digest_value=digest_value, note=note)
    return state_payload(project_path)


def record_verification(project_path: str | Path, result: Mapping[str, Any], *,
                        actor: str) -> dict:
    """Persist a verify result on the project layer (confirmed -> verified)."""
    layers = _layers()
    layers.record_project_verification(project_path, result, actor=actor)
    payload = state_payload(project_path)
    payload["verify"] = dict(result)
    return payload


def check_task(profile: Mapping[str, Any], state: str, task: Mapping[str, Any]) -> list[str]:
    """Warnings for an agent-team task that does not match the confirmed setup."""
    warnings: list[str] = []
    if state not in READY_STATES:
        warnings.append(f"environment setup is '{state}'; run the setup skill and get human "
                        "confirmation before starting task work")
        return warnings
    repositories = profile.get("repositories") or []
    repository = _text(task.get("repository"), 500)
    if repository and "code" not in (profile.get("skipped") or []):
        known = {item["id"] for item in repositories} | {
            Path(item["path"]).name for item in repositories} | {
            item["path"] for item in repositories}
        if repository not in known:
            warnings.append(f"repository '{repository}' is not registered in the environment "
                            "profile")
        elif any(item.get("access") == "read-only" and repository in
                 {item["id"], item["path"], Path(item["path"]).name} for item in repositories):
            warnings.append(f"repository '{repository}' is registered read-only")
    commands = profile.get("commands") or []
    gates = {item.get("gate") for item in commands}
    if "commands" in (profile.get("skipped") or []):
        return warnings
    for gate in _list(task.get("gates")):
        gate_id = gate.get("id") if isinstance(gate, Mapping) else gate
        if gate_id in {"static", "unit", "component"} and gate_id not in gates:
            warnings.append(f"gate '{gate_id}' has no recorded command in the environment profile")
    return warnings


# --------------------------------------------------------------------------- #
# Rendering
# --------------------------------------------------------------------------- #

def _table(rows: list[list[str]], header: list[str]) -> str:
    if not rows:
        return "_None recorded._"
    clean = lambda value: str(value or "—").replace("|", "\\|").replace("\n", " ")
    lines = ["| " + " | ".join(header) + " |", "|" + "---|" * len(header)]
    lines += ["| " + " | ".join(clean(cell) for cell in row) + " |" for row in rows]
    return "\n".join(lines)


def render_summary(record: Mapping[str, Any]) -> str:
    """The confirmation summary a human reviews before confirming the digest."""
    profile = record.get("profile") or {}
    ready = readiness(profile, record.get("state") or "draft")
    parts = [
        "# ARK Environment Setup",
        "",
        f"- State: **{record.get('state')}**  ·  digest `{record.get('digest') or digest(profile)}`",
        f"- Updated: {record.get('updated_at') or '—'} by {record.get('updated_by') or '—'}",
    ]
    confirmation = record.get("confirmation") or {}
    if confirmation:
        parts.append(f"- Confirmed: {confirmation.get('at')} by {confirmation.get('by')}")
    parts += ["", "## Objective", "", profile.get("objective") or "_Not recorded._", ""]
    if profile.get("non_goals"):
        parts += ["Non-goals:", ""] + [f"- {item}" for item in profile["non_goals"]] + [""]
    parts += ["## Repositories", "", _table(
        [[r["id"], r["role"], r["access"], r["path"], r["default_branch"], r["worktree_root"]]
         for r in profile.get("repositories") or []],
        ["id", "role", "access", "path", "base", "worktrees"]), ""]
    parts += ["## Task spaces", "", _table(
        [[s["id"], s["kind"], s["access"], s["location"], s["relation"]]
         for s in profile.get("task_spaces") or []],
        ["id", "kind", "access", "location", "relation"]), ""]
    parts += ["## Compute and environments", "", _table(
        [[c["id"], c["kind"], c["gpu_server"], c["gpus"], c["use"]]
         for c in profile.get("compute") or []],
        ["id", "kind", "gpu server", "gpus", "use"]), "", _table(
        [[e["id"], e["kind"], e["repository"], e["activate"]]
         for e in profile.get("environments") or []],
        ["id", "kind", "repository", "activate"]), ""]
    parts += ["## Storage", "", _table(
        [[s["id"], s["kind"], s["location"], s["retention"]] for s in profile.get("storage") or []],
        ["id", "kind", "location", "retention"]), ""]
    parts += ["## Gate commands", "", _table(
        [[c["id"], c["gate"], c["repository"], f"`{c['run']}`",
          "yes" if c["mandatory"] else "no", "proposed" if c["proposed"] else "confirmed"]
         for c in profile.get("commands") or []],
        ["id", "gate", "repository", "run", "mandatory", "source"]), ""]
    parts += ["## Secret references", "", _table(
        [[s["id"], s["ref"], s["purpose"]] for s in profile.get("secrets") or []],
        ["id", "ref", "purpose"]), ""]
    policies = profile.get("policies") or {}
    defaulted = set(profile.get("policies_defaulted") or [])
    parts += ["## Authority", "", _table(
        [[key, value, POLICY_NOTES.get(key, ""), "default" if key in defaulted else "set"]
         for key, value in policies.items()],
        ["action", "policy", "covers", "source"]), ""]
    agents = profile.get("agents") or {}
    cadence = profile.get("cadence") or {}
    parts += ["## Team", "",
              f"- Coordinator: {agents.get('coordinator') or '—'}",
              f"- Providers: {', '.join(agents.get('providers') or []) or '—'}",
              f"- Concurrency: {agents.get('concurrency')}",
              f"- Default reviewer: {agents.get('default_reviewer') or '—'}",
              f"- Status cadence: every {cadence.get('status_minutes')} min",
              f"- Task trigger: {cadence.get('task_trigger') or '—'}",
              f"- Task ID prefixes: {', '.join((profile.get('task_ids') or {}).get('prefixes') or [])}"
              " (project-global serial)", ""]
    if profile.get("skipped"):
        parts += ["## Skipped by the user", ""]
        parts += [f"- `{stage}`: {SKIP_EFFECT[stage][1]}" for stage in profile["skipped"]]
        parts += [""]
    if profile.get("unknowns"):
        parts += ["## Unknowns", "", _table(
            [[u["field"], u["blocks"], u["owner"]] for u in profile["unknowns"]],
            ["field", "blocks", "owner"]), ""]
    parts += ["## Readiness", "",
              f"- Allowed now: {', '.join(ready['allowed']) or 'nothing'}"]
    parts += [f"- Blocked `{item['capability']}`: {item['reason']}" for item in ready["blocked"]]
    open_questions = questions(profile)
    if open_questions:
        parts += ["", "## Open questions", ""]
        parts += [f"{i}. [{q['stage']}] {q['prompt']} _(blocks: {q['blocks']})_"
                  for i, q in enumerate(open_questions, 1)]
    return "\n".join(parts).rstrip() + "\n"


def example_profile() -> dict:
    return normalize_profile({
        "objective": "Evaluate whether method X improves Y on benchmark Z within 2 weeks.",
        "non_goals": ["no changes to the evaluation harness"],
        "repositories": [
            {"id": "code", "path": "~/work/method-x", "role": "primary",
             "default_branch": "main", "instructions": ["AGENTS.md"]},
            {"id": "paper", "path": "~/work/method-x-paper", "role": "paper",
             "default_branch": "main"},
        ],
        "task_spaces": [{"id": "legacy-team", "kind": "agent-team-folder",
                         "location": "~/work/method-x/agent_team", "relation": "import-history"}],
        "compute": [{"id": "local", "kind": "local", "use": "any"},
                    {"id": "gpu-a", "kind": "ssh", "gpu_server": "gpu-a", "gpus": "8x H200",
                     "use": "ask", "scratch": "/data/scratch/method-x"}],
        "environments": [{"id": "py", "kind": "conda", "repository": "code",
                          "activate": "conda activate method-x", "python": "3.11"}],
        "storage": [{"id": "artifacts", "kind": "artifacts", "location": "~/artifacts/method-x",
                     "retention": "keep checkpoints of accepted runs"}],
        "commands": [
            {"id": "lint", "run": "ruff check .", "repository": "code", "environment": "py",
             "gate": "static"},
            {"id": "unit", "run": "python -m pytest -q tests", "repository": "code",
             "environment": "py", "gate": "unit"},
            {"id": "smoke", "run": "python train.py --steps 20", "repository": "code",
             "environment": "py", "gate": "component"},
        ],
        "secrets": [{"id": "wandb", "ref": "env:WANDB_API_KEY", "purpose": "experiment logging"}],
        "agents": {"coordinator": "dsh", "providers": ["dsh", "codex"], "concurrency": 4,
                   "default_reviewer": "user"},
        "policies": {"commit": "allow", "push": "ask", "gpu_dispatch": "ask"},
        "cadence": {"status_minutes": 10,
                    "task_trigger": "the user requests a task or substantial multi-file work"},
        "task_ids": {"prefixes": ["EXP", "DOC", "FIG"]},
    })[0]
