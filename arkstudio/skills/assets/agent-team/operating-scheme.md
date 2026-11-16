# Agent-Team Operating Scheme (ARK)

Adapted from `references/operating-scheme.md` of the vendor skill and narrowed
to ARK's single-project, local-first model.

## Plan as a task graph

Decompose the project into bounded tasks with one primary verb and a measurable
deliverable. Record dependencies and gates. Split exploratory work from
production integration and split unrelated ownership surfaces.

Assign every logical task one project-lifetime unique ID. The terminal serial is
unique across all prefixes, so `FIG-001` and `WEB-001` conflict. Check active,
archived and historical records for the full ID and serial before issuing one.

## Roles

| Role | Accountability |
|---|---|
| Manager | task graph, priorities, ownership, scope, acceptance, escalation |
| Task owner | declared implementation or analysis and self-verification |
| Experiment operator | dry-run, launch, monitoring, raw artifact handoff |
| Reviewer/evaluator | independent gates and acceptance recommendation |
| Integrator | merge queue, integration revision, post-merge checks |
| Resource steward | environments, compute, reservations, import origins |
| Registrar | shared experiment or result indexes |
| Documentation integrator | high-conflict reports, tables, figures, claims |

One agent may hold several roles. A substantive task owner must not be the sole
acceptor unless the manifest records an `acceptance_exception`.

## Isolate work

- One task branch and one implementation worktree per code/config/paper task.
- Worktrees live under the primary checkout at `.worktrees/<TASK-ID>`.
- Do not use sibling roots such as `../<repo>-worktrees`.
- Record base and candidate revisions; never branch from a dirty base.
- Integrate reviewed commits, never uncommitted worktree state.

## Control concurrency

- One owner per task and one writer per high-conflict file, mutable dataset,
  registry, resource reservation or integration checkout.
- Parallelize independent review, analysis and immutable runs with unique
  output paths.
- Serialize canonical environment mutation, integration checkout changes,
  shared index edits and in-place dataset changes.
- Do not run a long job from an unrecorded mutable branch head.

## Monitor at intervals

1. Reconcile one current owner per task.
2. Inspect external jobs read-only through process, scheduler, log, artifact or
   heartbeat evidence.
3. Update `agent_state` and `external_job_state` separately.
4. Record `last_verified_at`, `next_check_at`, progress marker, resource and
   next gate.
5. Refresh the ARK task record's durable status and totals.
6. Escalate stale, conflicting, over-budget or unsafe conditions.

Do not describe an agent as running solely because an old PID exists, or a task
as stopped solely because its agent thread timed out.

## Recover safely

1. Inspect thread, job, resource, log, output and terminal marker read-only.
2. Resume the canonical thread with the exact task ID and current evidence.
3. If the external job is healthy, monitor without relaunching.
4. If it failed, preserve partial artifacts and diagnose before a bounded retry.
5. Obtain approval before termination, deletion, overwrite, transfer or
   deployment when the setup contract requires it.
6. Use a new run ID and output path for a retry; record its relationship.

## Gate progressively

Run the applicable gates in this order: `scope → static → unit → component →
integration → experiment → publication`. Distinguish mandatory gates from
diagnostics and record waivers with approver, reason, risk, expiration and
follow-up.

## Hand off and retain

Require a clean declared diff, candidate commits, inspection report,
reproduction record, artifact inventory, residual risks and rollback approach.
The reviewer reproduces mandatory gates in a controlled environment. Keep
compact manifests, decisions, checksums, aggregate metrics, reports and selected
figures in Git; keep bulk payloads in durable external storage.
