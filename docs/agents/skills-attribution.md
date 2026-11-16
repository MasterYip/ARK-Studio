# Skills attribution and source mapping

This document records exactly what was adapted, from where, under which
license, and what was deliberately left out. It is the canonical source map for
the skill modules (`arkstudio/skills/` in the original Python adaptation, ported
to `ark_studio/server/internal/skillkit/` and `skillbrief/` in ark-server; the
module names below are the Python ones).

Vendor snapshots of `agent-team-workflow`, `rrsi` and
`academic-design-workflow` were read during integration.
They are not copied into the ARK tree. ARK ships adapted code, templates and
documentation, plus the required license texts under
`ark_studio/server/internal/skillkit/LICENSES/`.

## Summary

| ARK skill | Upstream | Version / revision | License | Adaptation |
|---|---|---|---|---|
| `agent-team` | `agent-team-workflow` | vendored snapshot (no git metadata) | MIT, Copyright (c) 2026 Raymon Yip | contract, roles, task IDs, worktrees, gates, evidence, templates reimplemented for ARK task units |
| `rrsi` | `google-research/rrsi` | `be50316e1db05914068a973f322770ef08ed7ba1` (2026-09-23) | Apache-2.0, Copyright 2026 The rrsi Authors / Google LLC | component/budget/history/selection/critic core reimplemented with the standard library |
| `design` | `academic-design-workflow` | vendored snapshot v0.1.0 | MIT, Academic Design Workflow contributors | theme schema, semantic-role checks, source/asset rules, quality gates and artifact verification reimplemented with the standard library |

## `agent-team` source mapping

| Vendor asset | ARK implementation | What changed |
|---|---|---|
| `SKILL.md` | `arkstudio/skills/assets/agent-team/SKILL.md` | external workspace model replaced by ARK project + task units; API/CLI workflow added |
| `references/operating-scheme.md` | `assets/agent-team/operating-scheme.md` | narrowed to one portable ARK project; role table and gates kept |
| `references/task-lifecycle.md` | `assets/agent-team/task-lifecycle.md` | phase/status kept; added ARK unit-status projection |
| `assets/repo-template/agent_team/tasks/_template/goal_manifest.md` | `assets/agent-team/templates/goal_manifest.md` + `agent_team.prepare_task` | fields validated; rendered into a task unit body |
| `assets/repo-template/agent_team/tasks/_template/inspection_report.md` | `assets/agent-team/templates/inspection_report.md` + `render_inspection_report` | reviewer decision and ARK artifact inventory added |
| `scripts/init_agent_team.py` | not copied | ARK project creation is the workspace initializer |
| `scripts/validate_agent_team.py` | `agent_team.validate_manifest` / `prepare_task` | rules reimplemented in Python, no folder mutation |
| `scripts/workspace_core.py` and its workspace adapters | not copied | external-workspace concerns do not apply to `.ark` projects |

Adapted verbatim rules: the terminal numeric serial is unique across all task
prefixes and across the project's complete history; owner and reviewer are
independent; worktrees live at `.worktrees/<TASK-ID>` and are never created from
a dirty base; gates run cheapest-first; compact evidence stays in Git while bulk
payloads stay in durable external storage.

## `rrsi` source mapping

| Vendor asset | ARK implementation | What changed |
|---|---|---|
| `rrsi/components.py` | `arkstudio/skills/rrsi.py` (`COMPONENTS`, `STRUCTURAL_COMPONENTS`, `text_only`, `classify_diff`, `has_evidence`, `normalize_component`, `novelty`) | same vocabulary and recovery logic; ARK path signals added |
| `rrsi/schedule.py` | `edit_budget`, `budget_table` | same cosine annealing formula |
| `rrsi/selection.py` | `SearchConfig`, `Candidate`, `Decision`, `cost_rule`, `judge`, `select_round` | same floor/cost/shaped/admissibility rules; JSON-safe decisions |
| `rrsi/history.py` | `EditRecord`, `History`, `history_snapshot` | reimplemented in-memory + JSONL, with stall/exploration/prune semantics |
| `rrsi/critic.py` | `scan_leakage`, `DEFAULT_LEAKAGE_PATTERNS` | regex denylist adapted to ARK coding tasks; no model review |
| `rrsi/domain.py` | `DomainSpec`, `validate_domain`, `assets/rrsi/domain.json` | split/metric/guard contract kept; benchmark runner and harness checkout dropped |
| `domains/*/SKILL.md`, `PATTERNS.md` | `assets/rrsi/SKILL.md`, `PATTERNS.md`, `proposer.md`, `analyst.md`, `critic.md` | benchmark-specific prose removed; ARK task language used |
| `rrsi/loop.py`, benchmark runners, `third_party/` | not copied | benchmark adapters, Docker harnesses, Archipelago/Terminus code, and model clients are out of scope |

The paper citation:

> Xia et al., *RRSI: Regularized Recursive Self-Improvement of Agent Harnesses*,
> arXiv:2609.24972, 2026. https://arxiv.org/abs/2609.24972

## `design` source mapping

| Vendor asset | ARK implementation | What changed |
|---|---|---|
| `docs/theme-schema.md` | `arkstudio/skills/design.py` schema constants + `validate_theme` | pydantic replaced by stdlib validation; JSON is canonical |
| `src/academic_design_workflow/theme.py` | `deep_merge`, `_resolve_variant`, cross-role reference checks | same merge/variant/reference invariants; contrast and grayscale checks added |
| `references/overall-style-sop.md` | `assets/design/design_brief.md`, `SKILL.md` | adapted to ARK assets/artifact units |
| `references/quality-gates.md` | `QUALITY_GATES`, `assets/design/quality-gates.md` | same gates, exposed as data |
| `references/paper-figure-sop.md`, `website-sop.md`, `video-sop.md`, `visio-sop.md` | summarized in `SKILL.md` and the review template | medium-specific detail kept as reviewer checklists, not copied prose |
| `themes/*.yaml` | `assets/design/theme.json` | one original `academic-clean` theme; vendor themes are not copied |
| `src/.../styleboard.py`, `after_effects.py`, `visio/*`, `scripts/visio/*` | not copied | renderers and Windows/After Effects/Visio bridges are out of scope |
| `examples/visio/*` | not copied | host-specific automation, not portable |

The example theme is an original ARK asset built to satisfy the adapted
schema; it is not a copy of a vendor theme.

## Licenses

Full license texts are shipped in `ark_studio/server/internal/skillkit/LICENSES/`:

- `agent-team-MIT.txt` — MIT, Copyright (c) 2026 Raymon Yip.
- `rrsi-APACHE-2.0.txt` — Apache License 2.0, Copyright 2026 The rrsi Authors /
  Google LLC.
- `academic-design-MIT.txt` — MIT, Copyright (c) 2026 Academic Design Workflow
  contributors. The vendor snapshot declares MIT in `pyproject.toml` but ships
  no standalone LICENSE file; the standard MIT text is reproduced here with
  that copyright notice.

These notices apply to the adapted portions. ARK's own code remains under the
repository's license. No upstream project endorses ARK, and the names
`agent-team-workflow`, `RRSI` and `academic-design-workflow` are used only to
describe provenance.
