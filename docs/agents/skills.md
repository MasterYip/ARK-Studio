# Skills and protocol layer

ARK ships an internal, dependency-free skills layer. Setup binds a project to
its environment; the other three were previously external vendor skills:

| Skill | Purpose | Status |
|---|---|---|
| `setup` | first-run binding to repositories, task spaces, compute, commands and authority; human-confirmed | ARK-native, adapted from the agent-team setup gate |
| `agent-team` | bounded task planning, execution shape, context budgets, gates, independent verification | adapted into ARK task units (interface v2) |
| `rrsi` | regularized harness improvement: budget, history, cost rule, leakage screen | adapted core algorithm |
| `design` | theme-first academic design, asset rules, artifact verification | adapted schema + verification |

The code lives in `ark_studio/server/internal/skillkit/` (templates, render, validate) and
`ark_studio/server/internal/skillbrief/`; the discoverable catalog is built from it.
Nothing here is a link to a vendor checkout and there is no parallel app.

## Agent interface v2

`agent-team` implements ARK's agent-side interface v2. Every task declares an
execution shape (`task_structure` + `team`), a context budget, a model policy,
independent verification, a failure taxonomy and a hand-off contract. The
normative rules are in [agent-interface.md](agent-interface.md); the practical
checklists are `SKILL.md`, `routing.md`, `context.md` and `verification.md`
inside the skill.

Machine-readable discovery:

- `GET /api/agent/v1/interface` (agent token) — capabilities, limits, topology
  rules and the v2 manifest schema.
- `GET /api/skills/interface` (loopback) — the same payload without a token.

## Environment setup (run first)

When ARK is enabled in a new work environment (new machine, repositories, task
space or GPU server), the agent runs the `setup` skill before any task work:

Start from the existing register and earlier answers. Read-only discovery
also supplies configured GPU server names and provider presets; inspect those
before asking the user about compute or agents. Show the known facts and ask
only about missing, conflicting or changed details, one area at a time.
Do not repeat answered questions or ask someone to configure resources again.
Discovery inspects the ARK server, even with `X-ARK-Host`; verify user-provided
paths on the caller's device locally. See the repeated-setup workflow in
[multi-user-setup.md](../operations/multi-user-setup.md#repeated-setup-starts-from-existing-configuration).

```text
GET  /api/agent/v1/setup                      -> state, readiness, questions, summary_markdown
POST /api/agent/v1/setup/discover {paths}     -> read-only draft + observations + questions
POST /api/agent/v1/setup/draft {layer, resources | profile, propose: true}
POST /api/agent/v1/setup/location {resource, path_or_ref} -> this host's location
POST /api/agent/v1/setup/propose {}           -> every changed layer; returns pending
POST /api/skills/setup/confirm {layers: [{layer, digest}]} -> human only (agents: 403)
POST /api/agent/v1/setup/verify               -> read-only checks; confirmed -> verified
```

Device identity is explicit: use a stable `X-ARK-Host` per computer and a
readable host label (normally the machine name).  Locations are attached to
that host; other computers add their own locations instead of replacing the
existing paths.

A project may register **more than one `primary` repository** when its work
spans a code repo and a paper repo.  `readiness.primary_repositories` lists all
of them; `primary_repository` remains the first for older clients.  A task
manifest names the repository it lands in, so the paper and code branches stay
explicit.  At least one primary must be read-write for `implement` to be
allowed.

Setup is stored in layers: the project layer (`<project>.ark/setup/project.json`,
confirmed by a project admin) and one layer per person (`setup/users/`,
confirmed by that person), with an append-only `history.jsonl`. Each
repository, task space, GPU server, storage location, command or secret
reference is a resource with locations per host. Every project member and
their agents see all resources and paths. Repeated setup merges contributions;
knowing a path does not grant access to its machine. See
[multi-user-setup.md](../operations/multi-user-setup.md). A format 1.0
`environment.json` is migrated on first load. It stores secret references (`env:NAME`), never values. ARK
rejects token-shaped strings and strips user-info from remotes. The CLI
provides the same lifecycle, and the terminal user is the human actor:

```powershell
ark-server skills setup discover --project runtime/projects/my.ark --paths E:/code/repo
ark-server skills setup summary  --project runtime/projects/my.ark
ark-server skills setup confirm  --project runtime/projects/my.ark --digest <digest>
ark-server skills setup verify   --project runtime/projects/my.ark
```

Known limit: with authentication disabled (the local default), a local process
that calls `/api/skills/setup/confirm` without an agent token or an
`X-Agent-Name` header is treated as the human UI. The gate catches honest
agents and token holders, not a hostile local process. Enable ARK
authentication when that matters.

## Discover it

- **Settings → Skills** lists the catalog, renders every template, copies the
  agent prompt, and can create a real agent-team task unit.
- **HTTP** `GET /api/skills` returns the catalog plus the protocol summary;
  `GET /api/skills/interface` returns the agent interface descriptor.
- **CLI** `ark-server skills list|show|render|validate`.
- **Python** `from arkstudio.skills import list_skills, render_template, interface`.

```powershell
ark-server skills list
ark-server skills show agent-team
ark-server skills render agent-team goal_manifest.md
ark-server skills render design theme.json --out build/theme.json
ark-server skills validate rrsi round --payload round.json
```

## HTTP API

Read endpoints are available to the Settings UI. Agent-token holders also get
`/api/agent/v1/skills` and `/api/agent/v1/skills/{id}`.

| Method | Path | Purpose |
|---|---|---|
| GET | `/api/skills` | catalog, protocol, attribution links |
| GET | `/api/skills/interface` | agent interface descriptor (v2) |
| GET | `/api/skills/{id}` | full skill detail + documents (`?docs=0` to omit) |
| GET | `/api/skills/{id}/prompt` | copy-ready coding-agent prompt |
| POST | `/api/skills/{id}/render` | `{template, context}` → rendered text |
| POST | `/api/skills/{id}/validate` | `{kind, payload}` → diagnostics |
| POST | `/api/skills/agent-team/brief` | validate a delegation brief |
| POST | `/api/skills/agent-team/tasks` | validate + optionally create an ARK task unit |
| POST | `/api/skills/rrsi/round` | apply RRSI selection to a round |
| POST | `/api/skills/rrsi/history` | stall/exploration/prune snapshot |
| POST | `/api/skills/rrsi/leakage` | leakage scan of a diff |
| POST | `/api/skills/design/verify` | verify an artifact inside the project |
| POST | `/api/skills/design/asset` | validate a project-relative asset path |

Validation and verification return `200` with `ok: true/false` and an `errors`
list, so an agent can act on diagnostics without treating them as transport
errors. Malformed requests return `400` with `{ok:false,error:{message}}`.

A personal agent token can call this endpoint directly:

```http
Authorization: Bearer arka_...
X-ARK-Project: prj_...
```

It acts as its owner with the token's `write` scope. Use `create: false` to
preview and `create: true` to create the ARK task unit; this is a task-manifest
writer, not a file upload endpoint. Use
`/api/agent/v1/assets/upload` for Markdown, PDF and other assets.

### Create a task

```json
POST /api/skills/agent-team/tasks
{
  "create": true,
  "title": "Integrate the skills layer",
  "objective": "Expose ARK-native skills and keep them discoverable.",
  "owner": "agent:codex",
  "reviewer": "human:maintainer",
  "repository": "ARK-Studio",
  "base_revision": "main@77ddc89",
  "deliverables": ["arkstudio/skills package", "Settings > Skills panel"],
  "non_goals": ["building a second standalone app"],
  "owned_paths": ["arkstudio/skills/**", "tests/python/agents/test_skills.py"],
  "acceptance": [
    {"command": "python -B -m unittest tests.python.agents.test_skills -v",
     "threshold": "all tests pass", "evidence": "test log"}
  ],
  "gates": ["scope", "static", "unit"],
  "stop_conditions": ["a required file is owned by another active task"]
}
```

Omit `task_id` and ARK allocates the next project-global serial. The preview
returns the rendered Markdown, the ARK unit payload, dependency links and
suggested record paths. With `create: true` the rendered goal manifest becomes
the body of a real `task` unit tagged `skills:agent-team`, `task:<ID>`,
`project:<ID>` and `topology:<mode>`.

### Execution shape and budget (v2)

A manifest also declares:

| Block | Fields | Rule |
|---|---|---|
| `task_structure` | `sequential`, `breadth`, `long-horizon`, `adversarial` | decides the default topology |
| `team` | `mode`, `size`, `heterogeneous`, `rationale`, `roles` | `size > 1` needs a rationale; `> 5` or `peer-team` needs heterogeneity |
| `context` | `budget_tokens`, `inline`, `spill`, `artifacts` | 8k–2M tokens; project-relative artefacts |
| `model_policy` | `routing`, `planner`, `worker`, `verifier` | `fixed`, `tiered` or `auction`; independent verifier for teams |
| `verification` | `independent`, `method`, `evidence`, `reviewer_reproduces` | independent unless `acceptance_exception` |
| `failure_taxonomy` | seven classes | defaults to all; used in the inspection report |
| `handoff` | `contract`, `inputs`, `outputs` | required contract; what the next agent receives |

Invalid combinations are rejected before the task is created: a solo team with
size 3, a peer-team without heterogeneity, an absolute context artefact path, or
a non-independent verifier without an exception.

### Validate a delegation brief

A brief is what a router hands to one worker; it is lighter than a manifest and
is validated before spawning:

```json
POST /api/skills/agent-team/brief
{
  "objective": "Extract the three strongest counterexamples from the unit set.",
  "task_structure": "breadth",
  "team": {"mode": "solo", "size": 1},
  "owned_paths": ["analysis/counterexamples/**"],
  "acceptance": ["python -B -m unittest tests.test_analysis -v"],
  "stop_conditions": ["fewer than two counterexamples survive review"],
  "handoff": {"contract": "Return a table + one SVG per counterexample."}
}
```

`POST /api/skills/agent-team/validate` with `kind: "brief"` is equivalent, and
`interface.validate_brief(payload)` is the Python entry point.

### Validate or select

```json
POST /api/skills/agent-team/validate
{"kind": "task", "title": "...", "objective": "...", "...": "..."}
```

```json
POST /api/skills/rrsi/round
{
  "round": 0,
  "incumbent": {"score": 0.62, "cost": 120000},
  "s_star": 0.662,
  "accepted_counts": {"skill": 1},
  "candidates": [
    {"variant": "candidate-a", "score": 0.668, "cost": 122000,
     "edits": [{"component": "prompt", "hypothesis": "inject the current unit"}]}
  ]
}
```

### Verify an artifact

```json
POST /api/skills/design/verify
{"path": "assets/figure.svg", "artifact_kind": "svg"}
```

The result contains `checks`, `errors`, `warnings` and the shared/paper/web/
video quality gates.

## Agent-team contract

A goal manifest records: task ID, title, project, phase, status, owner,
reviewer, repository, worktree, base revision, dependencies, priority, budget,
objective, background, deliverables, non-goals, owned paths, acceptance,
ordered gates, evidence retention, risks and stop conditions.

Within the ARK task namespace, the numeric serial is project-global across task
prefixes; ARK's non-task units (`N-0001`, `C-0001`, ...) keep their own type
counters and are not task IDs. New tasks derive their ID from existing task
UIDs, including archived tasks.

- **Phase** is workflow progression: `proposed → ready → active → review →
  evaluation → accepted → integrated`.
- **Status** is the portfolio view: `running`, `waiting`, `blocked`,
  `completed`, `archived`, `superseded`.
- ARK projects status onto unit status (`running → active`, `waiting → open`,
  `completed → done`, ...).
- Worktrees are repository-relative `.worktrees/<TASK-ID>`; absolute paths and
  sibling roots are rejected unless `worktree_exception` is recorded.
- Owner and reviewer must differ unless `acceptance_exception` is recorded.
- Gates run `scope → static → unit → component → integration → experiment →
  publication`; evidence retention keeps compact records in Git and bulk
  payloads in durable external storage.

Templates: `goal_manifest.md`, `inspection_report.md`, `agent_brief.md`.

## RRSI contract

Adapted from `google-research/rrsi` (Apache-2.0): component vocabulary and
diff recovery, annealed edit budget, cost rule, noise-adjusted floor, shaped
within-band rule, novelty tie-break, edit history, stall/exploration/pruning,
and a leakage critic. The benchmark adapters, Docker harnesses and model
clients are intentionally not bundled; ARK supplies the decision logic and the
durable evidence format.

Templates: `proposer.md`, `analyst.md`, `critic.md`, `domain.json`.

The domain descriptor is the dependency-light part of the vendor's
`domains/<name>/adapter.py`: it names the evolvable harness path, the
`evolve`/`heldout`/`smoke` task splits, the metric and non-compensatory guards.
Validate it with `POST /api/skills/rrsi/validate` and `kind: "domain"`.

## Design contract

The theme is one versioned JSON document (YAML is optional via PyYAML) covering
`meta`, `color`, `typography`, `shape`, `spacing`, `layout`, `chart`, `web`,
`motion`, `video` and validated media variants. Shape/stroke references must
resolve to semantic colour roles; text contrast is checked; categorical
palettes are checked for grayscale collapse; artifacts are checked for
existence, kind, in-project paths, SVG well-formedness, self-containment and
image/PDF magic bytes.

Templates: `theme.json`, `design_brief.md`, `style_review.md`.

Put the theme at `assets/design/theme.json` and generated artifacts under
`assets/` so they can be attached to an ARK `artifact` unit.

Project colours are shared with the GUI and other materials through
`settings.palette` (`GET /api/agent/v1/project/palette`).  Use the palette's
semantic values as the starting point for the theme's `color.roles`; do not
invent a separate local palette for each artifact.

## Python API

```python
from arkstudio.skills import (
    list_skills, get_skill, render_template, validate_payload,
    agent_team, rrsi, design,
)

skills = list_skills()
goal = render_template("agent-team", "goal_manifest.md")
preview = agent_team.prepare_task(task, existing_ids=task_uids, project=project.summary())
selection = rrsi.validate_round(round_payload)
theme = design.validate_theme(design.example_theme())
artifact = design.verify_artifact("assets/figure.svg", project_root=project.path)
```

`agent_team.create_task(store, task, project=..., actor=...)` is the write path
used by the HTTP endpoint.

## Tests

```powershell
python -B -m unittest tests.python.agents.test_skills tests.python.agents.test_setup_skill -v
python -B -m unittest discover -s tests -v
```

## Limitations

- Skills are code-shipped, not user-installable plugins. Extending the catalog
  means adding a module, assets and tests. The agent interface descriptor is
  generated from the catalog, so new capabilities must be declared there too.
- The agent-team layer validates and records contracts; it does not create git
  worktrees, run experiment schedulers or monitor external jobs. The agent runs
  `git worktree` explicitly and records job evidence in the task unit.
- RRSI does not ship benchmark adapters or a model client. Agents bring their
  own trials; ARK supplies selection, history and leakage checks.
- Design validation covers the contract and mechanical artifact checks; it does
  not render stylesheets, Matplotlib output, After Effects or Visio files.
- YAML themes require PyYAML; JSON is the canonical format.
- The Settings panel is a browser-local view of a server catalog; it is not a
  skill editor.
