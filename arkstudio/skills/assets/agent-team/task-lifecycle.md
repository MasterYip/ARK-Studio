# Task Lifecycle and Records (ARK)

Adapted from `references/task-lifecycle.md` of the vendor skill.

## Task IDs are immutable and globally unique

A logical task ID is unique across the project's complete task history. The
terminal numeric serial is project-global across task prefixes, not
prefix-local: allocate one monotonically increasing, zero-padded sequence across
all task classes. ARK's other node types (`N-0001`, `C-0001`, ...) have their
own UID counters and are not task IDs. Before issuing an ID, search the ARK
project for both the full task ID and the serial, including archived tasks.
Never recycle an ID or serial from completed, archived, superseded, rejected or
cancelled work.

A retry, revision, resumed agent or replacement owner continues under the
original ID. When the objective is materially different, create a new ID and
record `depends_on`, `supersedes` or another explicit relationship.

Before resuming or editing a node, read its unresolved user comments
(`comments_open`, `GET /api/agent/v1/units/{id}/comments`). They are
instructions. Answer each with an `agent_reply` comment and resolve it only
once it is addressed; see SKILL.md, *User comments*.

If a duplicate is discovered, stop new assignment, preserve both records, and
reconcile with an explicit, approved legacy-to-canonical mapping rather than
silently renumbering accepted history.

## Phase and status are separate

`phase` tracks workflow progression:

`proposed → ready → active → review → evaluation → accepted → integrated`

`status` tracks the portfolio view:

| Status | Meaning |
|---|---|
| `running` | an agent or external job is actively advancing the task |
| `waiting` | ready or partially complete but queued for a resource, reviewer or decision |
| `blocked` | no meaningful progress is possible until a named condition changes |
| `completed` | contracted deliverables and owner checks are complete |
| `archived` | accepted work is preserved with no active action |
| `superseded` | replaced or rejected work retained for provenance |

Examples: a task may be `phase: review` and `status: waiting`; an external run
may leave `phase: active` and `status: running` while its thread waits.

The `review` and `evaluation` phases are agent work. They keep the ARK unit
`active` until the result is accepted (`done`), with a `phase:review` or
`phase:evaluation` tag so the stage stays visible on the Tasks board. ARK's own `review` status is
reserved for acceptance by the human user (SKILL.md, *Status and human
attention*).

ARK projects status into the existing unit vocabulary:

| Protocol status | ARK unit status |
|---|---|
| `running` | `active` |
| `waiting` | `open` |
| `blocked` | `blocked` |
| `completed` | `done` |
| `archived` | `archived` |
| `superseded` | `archived` |

The unit's dates follow the same transitions (SKILL.md, *Research map →
Dates*): `start_at` when the task really starts (`running`), a planned
`end_at` as its deadline, and the actual completion date on `completed`.
When a task is reopened, clear or reset `end_at` in the same update; when it
is superseded, leave its dates on the record and give the replacement its own.

## Goal manifest

Create it before assignment. It must record task ID, title, phase, status,
owner, reviewer, repository, branch/worktree, base revision, dependencies,
priority and budget; a measurable objective, background, in-scope deliverables
and non-goals; owned paths; instruction/data/environment identities; acceptance
requirements with command, threshold and evidence location; the staged plan,
risks, stop/escalation conditions; decision log; and ready-to-start approval.

Undeclared file or external-system changes are unauthorized scope expansion
until the manager amends the manifest.

## Inspection report

Require the owner-reported outcome and independent acceptance state; base,
candidate, nested and integration revisions; deliverables and changed files;
exact verification commands and all results (including skipped/failed checks);
reproduction record (revisions, dirty state, environment/import origins,
hardware, dataset identity, resolved config, seeds, command, output/run IDs);
acceptance comparison, deviations, residual risks and artifact inventory;
integration and non-destructive rollback guidance; and the independent
evaluator's context, evidence and decision.

An owner may report `complete`; only the designated acceptor records `accepted`.

## Canonical task index

Maintain exactly one row per task ID, grouped by status, with a local timestamp
and totals. Each row contains the task ID and compact evidence-backed headline,
phase, owner, external job state, key result/revision/progress marker,
deviations or blockers, one bounded next gate, and durable repository-relative
links to the manifest and latest report. In ARK the index is the task-unit list
filtered by `skills:agent-team`; do not use temporary worktree paths as
canonical evidence links.

## Live agent and resource status

Track one current owner per task with agent, task, agent state, external job
state, evidence, resource, last verified, next check and next gate/blocker.
Historical, timed-out, errored or superseded instances belong in task evidence,
not the live table.

## Evidence rules

- Use exact revisions, resolved configs, checksummed data, seeds, environment
  fingerprints, run IDs, raw metrics, aggregation scripts and regenerated
  figures for research claims.
- Label preliminary, diagnostic, validated, accepted and negative results
  distinctly.
- Record exclusions and failed runs, not only the best result.
- Keep compact evidence in Git and bulk evidence in durable storage with an
  inventory and checksums.
- Record every scope, dataset, baseline, threshold, budget or protocol change in
  the decision log.
