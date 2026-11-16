# Agent Team Workflow · ARK adaptation (v2)

Adapted from the standalone `agent-team-workflow` skill (MIT, Copyright (c)
2026 Raymon Yip). ARK does not create a second workspace: the project's own
`.ark` folder and task units are the durable record. This document is exposed
by `GET /api/skills/agent-team` and rendered from the vendor contract, not a
link to a vendor checkout.

v2 adds the **agent interface** contract (`interface_version: "2.0"`): execution
shape, context budget, model policy, independent verification, a failure
taxonomy and an explicit hand-off. Read [`docs/agents/agent-interface.md`](../../../../docs/agents/agent-interface.md)
for the normative rules; this skill is the practical checklist.

## What the contract guarantees

- **One bounded outcome per task.** A task has a measurable objective,
  deliverables, explicit non-goals, owned paths, acceptance checks, gates,
  stop conditions and a named reviewer.
- **An explicit execution shape.** `task_structure` plus `team` say whether this
  is one agent, a pipeline, a router with workers, or a heterogeneous peer team.
  A team must be earned by the task structure, not by ambition.
- **A context budget.** `context.budget_tokens` with inline/spill/artefact
  lists; artefacts are referenced by project-relative path, never pasted.
- **Project-global task IDs.** Within the ARK task namespace the numeric
  serial is unique across every task prefix in the project's complete history.
  `FIG-015` prevents `WEB-015`; ARK's non-task units (`N-0001`, `C-0001`, ...)
  have their own type counters and are not task IDs. Retries, revisions and
  resumed owners reuse the original ID; new work takes the next unused serial.
- **Isolated execution.** Implementation happens in
  `<primary-checkout>/.worktrees/<TASK-ID>` on a task branch. Never create a
  worktree from a dirty or ambiguous base, and integrate reviewed commits, not
  uncommitted directories.
- **Independent acceptance.** The owner reports completion; only the reviewer
  accepts. Owner and reviewer must differ unless an `acceptance_exception` is
  recorded on the manifest. The reviewer is an agent by default. Ask the
  human user to accept only in the cases listed under *Status and human
  attention*.
- **Evidence over self-report.** Gates run cheapest-first and the inspection
  report carries exact commands, revisions, environment/data identity, seeds,
  deviations, failure classes, residual risks and rollback guidance.
- **Compact evidence stays in Git**, bulk logs/datasets/checkpoints/media stay
  in durable external storage with an inventory and checksums.

## Execution shape

| Task structure | Default topology | Size | Why |
|---|---|---|---|
| `sequential` | `solo` | 1 | parallel workers would race on the same state |
| `breadth` | `router-workers` | 2–5 | independent directions add evidence |
| `long-horizon` | `router-workers` | 2–5 | durable checkpoints beat one huge context |
| `adversarial` | `peer-team` | 3–5 | heterogeneity resists shared bias |

Rules: `size > 1` needs a rationale; `size > 5` requires `heterogeneous: true`;
`peer-team` requires heterogeneity and a critic/reviewer role; no nested teams.
See `routing.md` for the decision tree and anti-patterns.

## Context budget

- Inline the hot set: manifest, current unit, owned paths, last verified diff.
- Spill logs, datasets, transcripts and prior reports; cite the path.
- Reference artefacts; do not paste them into the unit body.
- Start at 120k tokens; above 500k is a warning sign.
- Compact at ~70% of the budget into a verified-state summary, then continue
  from that summary.

See `context.md` for the hand-off pattern.

## Verification

- Gates: `scope → static → unit → component → integration → experiment →
  publication`; a skipped gate is recorded as skipped.
- The reviewer reproduces the mandatory gates and records the decision.
- `verification.method` is `command`, `rubric`, `human` or `mixed`.
- Every failure gets one of seven classes: `spec`, `tool`, `context`, `model`,
  `environment`, `review`, `integration`.
- Retry in place for `tool`/`environment`; restart from a clean brief for
  `spec`/`context`. See `verification.md`.

## ARK mapping

| Vendor concept | ARK implementation |
|---|---|
| `agent_team/tasks/<ID>/goal_manifest.md` | task unit body rendered from `goal_manifest.md` |
| `inspection_report.md` | `inspection_report.md` template + reviewer decision |
| `agent brief` | `agent_brief.md` template, validated with `POST /api/skills/agent-team/brief` |
| external workspace status index | `GET /api/skills/agent-team` + task unit filters |
| repository worktree | `.worktrees/<TASK-ID>` relative path on the manifest |
| validator script | `agent_team.prepare_task` / `validate_manifest` / `interface.validate_brief` |
| task record | ARK task unit (`type=task`, tags `skills:agent-team`, `task:<ID>`, `topology:<mode>`) |

## Workflow

0. **Check the environment.** `GET /api/agent/v1/setup`. If
   `readiness.ready` is false, run the `setup` skill first. Take the
   repository, base branch and gate commands from the confirmed profile.
1. **Read the interface.** `GET /api/agent/v1/interface`; note limits and the
   current `interface_version`.
2. **Classify and shape.** Pick `task_structure`, then `team.mode` and size.
   Decompose the goal into bounded tasks and allocate IDs with
   `protocol.next_task_id` or by omitting `task_id` in the API call.
3. **Write the manifest.** Preview and validate with
   `POST /api/skills/agent-team/validate` (kind `task`). Do not implement until
   it is valid.
4. **Brief every worker.** Validate the short brief with
   `POST /api/skills/agent-team/brief` before spawning; pass the rendered
   `agent_brief.md`.
5. **Create the task.** `POST /api/skills/agent-team/tasks` with
   `create: true` writes the rendered manifest into a real ARK task unit.
6. **Prepare the worktree.** `git worktree add .worktrees/<TASK-ID> -b
   <branch> <base>`. Keep the base revision in the manifest.
7. **Execute.** Stay inside `owned_paths` and the context budget; assign
   high-conflict files and mutable registries to one writer at a time.
8. **Gate.** Run `scope → static → unit → component → integration → experiment
   → publication`, recording commands and results.
9. **Report.** Render `inspection_report.md`, include the candidate commit,
   gate evidence and failure classes.
10. **Review.** The named reviewer reproduces mandatory gates and records the
    decision. Failed work returns to the same task ID.
11. **Integrate and retain.** Merge the reviewed commit, record the integration
    revision, release resources, and keep the compact manifest/report.

## Commands and API

```text
GET  /api/agent/v1/interface
GET  /api/skills/interface
GET  /api/skills/agent-team
POST /api/skills/agent-team/render       {template, context}
POST /api/skills/agent-team/validate     {kind: "task" | "manifest" | "brief", ...}
POST /api/skills/agent-team/brief        {objective, task_structure, team, ...}
POST /api/skills/agent-team/tasks        {task fields, create?: bool}
```

Python:

```python
from arkstudio.skills import agent_team, interface, protocol

task_id = protocol.next_task_id(existing_ids, prefix="T")
preview = agent_team.prepare_task({...}, existing_ids=existing_ids, project=project)
brief = interface.validate_brief({"objective": "...", "owned_paths": ["..."],
                                  "acceptance": ["..."], "stop_conditions": ["..."]})
agent_team.create_task(store, {...}, project=project.summary())
```

## Monitoring and recovery

Track the agent process and its external job separately. Record `agent_state`,
`external_job_state`, `last_verified_at`, `next_check_at`, resource reservation,
process/job ID, progress marker and the next gate. A timed-out agent does not
imply a stopped external job; verify read-only before resuming, relaunching or
killing anything. A replacement owner is created only when the canonical thread
is unavailable and ownership transfer is explicit.

## GPU jobs (ARK queue)

List project jobs with `GET /api/agent/v1/gpu/jobs?limit=100`, using a personal
agent token (read scope is enough) and `X-ARK-Project: <project ID>`.
Get accessible project IDs from `/api/agent/v1/whoami`.
Filter the list with `status=running,errored`, `name_prefix=...`, `since=<ISO
timestamp with timezone>` or `server=...`. Use `name=<exact job name>` to find
every prior attempt, including cancellations and retries. Batch reads use
`ids=<id1>,<id2>`. Always follow `next_offset` while `has_more` is true;
`total` counts all matches and `count` counts this page (limit 1-500).
Read failures from `error` / cached `log_tail` first; explicit
`GET /api/agent/v1/gpu/jobs/<id>/logs?tail=20` may fetch a remote log.

Submit GPU work through the ARK GPU queue (`POST /api/gpu/jobs`, one
submission per job; status via `GET /api/gpu/jobs/{id}`). Don't pick GPUs by
hand. Before submitting, check `GET /api/gpu/queue` (policy and path map) and
the job list, then:

- **Submit in the project that owns the work.** A job is filed under the
  project selected by the submitting session, not the one named in the job.
  `/api/gpu/*` needs a signed-in session; a personal `arka_` token alone is
  not accepted. A client or transport written for one project, opening that
  project by path, files every job there. Before submitting, check that
  `GET /api/project` returns this project, and make the client assert it.
- **Spread across servers.** The queue is FIFO. A job pinned to a full server
  waits even when other servers are idle. Pin to a server with free GPUs, or
  leave it unpinned if every server can launch it.
- **Tags must be a subset of the server's tags.** A job tag the server lacks
  makes the job unschedulable, and it stays `queued` with no error. Use
  `"tags": []` unless you mean to restrict servers.
- **Paths.** `queue.path_map` rewrites the job's command, env and setup per
  server (for example `/data/<user>` → `/mnt/<host>/<user>`). Write commands
  with the canonical path and let the queue map it.
- **Limits.** `timeout_minutes` ≤ 1440. Set `min_free_memory_mb` to the real
  claim. A server whose recent jobs fail with "could not launch remote job"
  should be avoided (pin elsewhere) and reported.
- **Watch it.** If a job stays `queued` while GPUs are idle, check its tags and
  pin before assuming the dispatcher is stuck.

## Research map

The ARK map is how a human follows the research, so keep it compact,
progress-visible and human friendly. Read it as the project's table of
contents, not its lab notebook.

**Structure.** Each research branch has one root story. Under it go:

- the open question(s);
- one task node per *meaningful* step;
- one experiment node per result a human would want to inspect;
- one claim node per conclusion.

Link them with `part_of`, `depends_on`, `uses` and `supports`/`refutes`, so
the graph reads left to right as question → tasks → results → conclusion.

**Growth.** The map grows when the research moves. Add a node when a step
starts, a result lands or a conclusion is reached, then update its status
(`open` → `active` → `done`, or `blocked`). A human should see progress
without opening a task folder.

**Status and human attention.** Statuses mean one thing each:

| Status | Meaning |
|---|---|
| `open` | planned, not started |
| `active` | in progress, including agent review and evaluation (tag `phase:review` or `phase:evaluation` while an agent reviewer works; remove it when done) |
| `done` | finished and accepted. Agent review is enough by default |
| `blocked` | waiting on something outside the team's control; the body says what |
| `review` | **reserved for the human user**: finished, agent-verified, waiting for the user to accept |

- An agent never puts a node in `review` to wait for another agent. When an
  agent reviewer can accept the work, it goes straight to `done`.
- Use `review` only when the user must accept the result:
  - a branch's conclusion;
  - a merge into a shared branch;
  - a deviation from an approved protocol or budget;
  - material for submission or publication.
- A question only the user can answer gets a `decision` node (prefix `D`), not
  just a chat message. The body states the question, the options, a
  recommendation and the default if there is no answer. Link it with `blocks`
  to the work it holds up. The user closes it (`done`) and their answer arrives
  as a `decided` comment.
- ARK sets the `needs:` tag (`needs:review`, `needs:accepted`, ...) on every
  node that waits on the user; never add, edit or remove it.
- `review` and `decision` nodes appear in the user's Home "Needs you" panel.
  Write them for a human: lead with the conclusion in one or two lines, with
  denominators and uncertainty.
- **Media on the node itself.** Every node in `review` and every `decision`
  node carries its own key figure or video. When the evidence lives on an
  experiment node, attach a copy of that media to the review/decision node too
  (`POST /api/agent/v1/media` with the same project-relative path). Media on
  linked nodes does not count: Home shows it only as a labelled "from X-0034"
  fallback with a "no media on this node" hint.
- When the user accepts, the node becomes `done`. When the user sends it back,
  it returns to `active` with the reason as a `sent_back` comment; address it
  on the same node.
- **User comments.** The user writes to you with comments: separate entries
  on the node, not text in its body. Kinds: `comment` (status unchanged),
  `sent_back` (status back to `active`), `decided` (decision closed) and
  `accepted` (node accepted, optional note). They come with the unit:
  `GET /api/agent/v1/units/{id}` returns `comments` and `comments_open` (the
  number of unresolved user comments); `GET /api/agent/v1/units/{id}/comments`
  returns the list with `open_user_comments`. Unit lists carry
  `comments_open` too, and the task-agent context lists `open_comments`.
  - Before you resume work on a node or edit it, read its unresolved user
    comments. Treat them as instructions from the user and act on them.
  - Answer on the same node with an agent reply:
    `POST /api/agent/v1/units/{id}/comments` with
    `{"body": "...", "reply_to": <comment id>}` (kind `agent_reply`, the only
    kind agents write). Say what you changed or why you did not.
  - Resolve a comment only once it has been addressed:
    `POST /api/agent/v1/units/{id}/comments/{comment_id}/resolve`. If it is
    only partly done, reply and leave it open.
  - Never delete or rewrite a user comment, and never copy comments into the
    body; rewriting the body does not touch them. Do not write the old
    `**User comment (...):**` / `**Agent reply (...):**` body lines.
  - A comment on a `review` node does not accept it; leave the status as it is
    unless the comment asks for a change.

**Dates (`start_at`, `end_at`).** The Gantt and timeline are drawn from
these two dates, so keep them true. ARK fills defaults: `start_at` is the
creation date when you omit it, and `end_at` is the completion date when a
node becomes `done` with no `end_at`. An open node with a `start_at` and no
`end_at` shows as ongoing up to today. Explicit values always win.
- `start_at` is when the step really starts. For a node planned ahead, set the
  planned start (or update `start_at` when work begins); don't leave the
  creation date on a step that started weeks later.
- Set a planned `end_at` as the deadline when one is known. ARK keeps it when
  the node is completed; if the step finished on another day, set `end_at` to
  the real completion date in the same update that sets `done`.
- Completing without a deadline: send just `status: done` and ARK records
  today. Completing on behalf of an earlier day (a run that finished
  overnight, a result found late): send that date as `end_at`.
- Reopening a `done` node keeps its `end_at` (the last completion). Clear it
  (`end_at: null`) or set the new deadline in the same update that reopens
  it; otherwise the next completion keeps the old date.
- A superseded or archived node keeps the dates of the work it records. Set
  `end_at` to when it stopped if it is empty, and give the replacing node its
  own `start_at`.

**Ownership (`assignee`).** Every node has an optional `assignee`: the ARK
account username that owns or maintains it, or `null`. A node without its own
value inherits `assignee_effective` from its nearest `part_of` ancestor, so
assigning a branch root assigns the whole branch. This is separate from
`assigned_to` links, which attach agent or person *units* inside the graph.
- A new node is assigned to the creating account by default: the account the
  write belongs to (the user who signed in or launched your session; the local
  owner on a server without accounts). This applies to
  `POST /api/agent/v1/units`, task creation with `create: true`, imports and
  insert-between.
- Set `assignee` explicitly when the owner differs from that account, for
  example a coordinator working for user X sets `assignee: X` on the branch
  roots and task nodes it creates (or `assignee` in the task manifest).
- Send `assignee: null` on a child that should inherit from its branch root
  instead of carrying its own value. Without it the child gets the creating
  account, which is the same person in the common case.
- Don't edit nodes whose `assignee_effective` is another user. Link to them
  instead (`depends_on`, `uses`, `relates_to`); that is the only change.
- Find your branch with `GET /api/agent/v1/units?assignee=X`; `assignee=none`
  lists unowned nodes.

**Compactness.**
- No node per job, retry, partition, receipt, review pass or smoke test. These
  belong in the task record and go on the node only as a link or a line.
- Group related runs or ablations into one experiment node with one result
  table.
- Archive superseded, routine or intermediate nodes instead of deleting them.
- Aim for a handful of active nodes per branch.
- **Split a node that holds more than one thing.** A node holds one step, one
  result, one conclusion or one decision. Split it into its own nodes when:
  - it covers several results, protocols or drafts;
  - its body has many dated progress sections;
  - a human would need to scroll to find the current state.

  Typical splits: a frozen protocol into an `artifact` (`part_of` the task,
  `uses` from the experiment); each separate result table into its own
  experiment node; a draft for another team into an `artifact` linked
  `supports` to the node it serves; a conclusion into a `claim` (`supports` from its experiment).
  Move progress logs and job ledgers into the task record file and link the
  path. Keep the parent's body to the goal, the current result and links to
  the split nodes. Superseded parts go to status `abandoned`, not deleted.
- **Visible node types.** The Map and Tasks views hide type `note` by
  default. Anything a human should find on the map must be a visible type:
  `task`, `experiment`, `claim`, `decision`, `artifact`, `question` or `story`.
- **Grow with the research.** When a result raises a new question or a
  reviewer note changes the plan, add the next task, result or claim node in
  the same update instead of appending to an existing node.

**Readability.**
- Short titles that state the content, e.g. "Dense DAgger vs matched BC: −0.03
  AUC", not internal IDs.
- Bodies of a few lines: the result with denominators and uncertainty, the
  current status and the next step.
- Attach the key figure or video to the node it supports.
- Put operational detail (hashes, PIDs, job IDs, receipts) in linked task
  records, not in the visible body.
- Don't touch other branches' nodes; connect to them with at most one link.
- Explain short names. Arm, policy and method short names (for example
  `BC+10k`, `DaggerFull`, `Bnorm_full`, `v2 warm`) are not self-explanatory
  to a human. When a node uses one, define it in a plain sentence on first use
  in that node: what was trained, from which start, on what data and for how
  long, and what it is the control for. Keep one project glossary note or
  file that lists every short name. Attach it to every `review` and
  `decision` node that uses short names, and update it when a new name
  appears.

**Topics for storylines.** The Map shows everything; a *topic*
(`type: topic`, uid `TP-...`) shows one storyline: chosen nodes in reading
order, on their own canvas, with only the links that matter. Make one when a
branch or result needs to be read as a story, for example a branch's
question → method → experiments → claims, or a related-work thread.
- Create the nodes and real links first; a topic only selects and arranges
  them. Look for an existing topic before making one
  (`GET /api/agent/v1/units?types=topic&q=...`).
- Create the topic and its design in one call:
  `POST /api/agent/v1/units` with `{"type": "topic", "title": ..., "body": ...,
  "design": {"add": [...], "order": [...], "items": [{"unit", "parent"}],
  "relations": [...]}}`.
- Revise with `POST /api/agent/v1/units/{topic}/members` (`add`, `remove`,
  `items`, `order`, `rule`, `relations`, `hide_links`/`show_links`) and read
  it back with `GET` on the same path. Removing a member never deletes the
  node.
- Keep a topic to 5–30 members in reading order; for a whole programme, nest
  topics (a topic can be a member of another). Don't change the rule of the
  built-in Story, Related work, Method and Experiment topics.
- The full workflow, including canvas positions, is in `docs/guide/topics.md`
  ("Agent workflow").

## Visuals

Prefer Mermaid for task graphs, dependency graphs, ownership boundaries and
recovery sequences, and LaTeX when an objective or acceptance formula needs
precise notation. Visuals explain; commands, revisions and measured results
remain the acceptance record.
