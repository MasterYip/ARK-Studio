"""ARK's internal skills and protocol layer.

This package provides ARK-native, dependency-free skill modules:

* :mod:`arkstudio.skills.setup` — first-run binding of a project to its work
  environment (repositories, task spaces, compute, commands, authority);
* :mod:`arkstudio.skills.agent_team` — bounded task planning/execution on top
  of ARK task units;
* :mod:`arkstudio.skills.rrsi` — regularized harness search and evidence;
* :mod:`arkstudio.skills.design` — theme-first academic design and artifact
  verification.

The catalog is the discoverability surface used by ``GET /api/skills``, the
Settings panel and ``python -m arkstudio skills ...``.  Attribution and the
vendor-to-ARK source mapping live in ``docs/agents/skills-attribution.md``.
"""
from __future__ import annotations

from typing import Any, Mapping

from . import agent_team, catalog, design, interface, protocol, rrsi, setup, setup_layers
from .catalog import (
    CATALOG_VERSION,
    SKILLS,
    SkillError,
    agent_prompt,
    catalog_payload,
    get_skill,
    list_skills,
    render_template,
    validate_payload,
)
from .protocol import ProtocolError
from .rrsi import RrsiError
from .design import DesignError
from .setup import SetupError

__all__ = [
    "CATALOG_VERSION",
    "SKILLS",
    "SkillError",
    "ProtocolError",
    "RrsiError",
    "DesignError",
    "SetupError",
    "agent_team",
    "catalog",
    "design",
    "interface",
    "protocol",
    "rrsi",
    "setup",
    "agent_prompt",
    "catalog_payload",
    "get_skill",
    "list_skills",
    "render_template",
    "validate_payload",
]


def create_agent_task(store, task: Mapping[str, Any], *,
                      project: Mapping[str, Any] | None = None,
                      actor: str = "agent:skills") -> dict:
    """Convenience wrapper so callers need only ``import arkstudio.skills``."""
    return agent_team.create_task(store, task, project=project, actor=actor)


def validate_task(task: Mapping[str, Any], *, existing_ids=(),
                  project: Mapping[str, Any] | None = None) -> dict:
    return agent_team.prepare_task(task, existing_ids=existing_ids, project=project)


def select_candidate(**payload: Any) -> dict:
    """Apply RRSI selection to a round payload."""
    return rrsi.validate_round(payload)
