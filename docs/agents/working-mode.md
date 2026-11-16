# Recommended agent working mode

How a long-running coordinator agent should use ARK day to day: plan in a task
node before working, keep progress and status honest, publish every result as
a review node with its own media, and record every user decision.

This is the working mode the Home starter prompts ask for in their standing
rules (R1-R9). The prompts stay short; this page has the detail. Everything
below uses ARK's official agent HTTP API (`/api/agent/v1/...`) with a personal
agent token; see [External agents](external-agent.md) for authentication and
[Agent interface](agent-interface.md) for the contract.

## In short

1. **Plan in a node first.** Before work that takes more than a few steps,
   create (or update) a task node with the plan, and link it to exactly one
   `derived_from` parent.
2. **Keep the record current.** Dated progress lines in the task body, an
   honest `status`, resource paths on the task and in the setup register.
3. **Publish results for review.** Each result is its own node with status
   `review`, its own media attached, and each figure embedded beside the
   table row or claim it supports.
4. **Record decisions.** Every decision the user makes becomes a `decision`
   node.
5. **Ask before** irreversible or outward actions, and record the approval.

## 1. Access and basic operations

### The client

ARK's official interface for agents is the HTTP agent API. Send
`Authorization: Bearer arka_...` (a personal agent token) and
`X-ARK-Project: <project id>` with every call. ARK does not ship a client
library; any HTTP client works (`curl`, Python `urllib`, `fetch`). The
`ark-server` binary adds a few helpers, none of which writes units:

| Command | Purpose |
| --- | --- |
| `ark-server agent context --project <folder>` | compact project context, read locally from a project folder on the ARK machine |
| `ark-server session-watch --id ark-ses-...` | opt-in upload of the agent's session JSONL ([Session context](session-context.md)) |
| `ark-server skills ...` | discover skills, render and validate their templates ([Skills](skills.md)) |

Start a session with `GET /api/agent/v1/whoami`, `GET /api/agent/v1/setup`
and `GET /api/agent/v1/context`.

### Operations used daily

| Operation | Request |
| --- | --- |
| List units | `GET /api/agent/v1/units?limit=500` (filters: `types`, `statuses`, `q`, `tags`, `updated_since`, `slim=1`) |
| Read a unit | `GET /api/agent/v1/units/<uid>`: body, status, `media`, `links_in`, `links_out`, `comments`, `topics`, `updated_at` |
| Read comments | `GET /api/agent/v1/units/<uid>/comments` (`comments`, `open_user_comments`) |
| Create a unit | `POST /api/agent/v1/units` `{type, title, body, status, importance?, tags?}` |
| Update a unit | `POST /api/agent/v1/units/<uid>` with only the fields to change (`body`, `status`, `title`, ...) |
| Link two units | `POST /api/agent/v1/links` `{source, target, relation, note?}` |
| Attach a file or link | `POST /api/agent/v1/media/upload` (images), `POST /api/agent/v1/assets/upload` (any file), `POST /api/agent/v1/media` `{unit, url, kind, caption}` (an existing project path or URL) |
| Reply to / resolve a comment | `POST /api/agent/v1/units/<uid>/comments` `{body, reply_to}`; `POST /api/agent/v1/units/<uid>/comments/<id>/resolve` |
| Turn on text history | `POST /api/agent/v1/units/<uid>/history` `{enabled: true}` |
| Topic members | `GET` / `POST /api/agent/v1/units/<uid>/members` ([Topics](../guide/topics.md#agent-workflow)) |
| Setup register | `POST /api/agent/v1/setup/resource`, `POST /api/agent/v1/setup/location` ([Shared project setup](../operations/multi-user-setup.md#setup-api)) |
| GPU queue | `GET` / `POST /api/agent/v1/gpu/jobs`, `GET .../<id>/logs`, `POST .../<id>/cancel` ([GPU](../operations/gpu.md#queue-monitoring-api)) |

Notes on the fields, as ARK implements them:

- **Links** use the field `relation`; it defaults to `relates_to`. The
  relations used below are `part_of`, `derived_from`, `relates_to`,
  `supports` and `targets`; the full list is in the interface descriptor.
- **Statuses** are `inbox`, `open`, `active`, `review`, `blocked`, `done`,
  `abandoned` and `archived`. A unit has exactly one.
- **Decisions** are units of `type: "decision"` (uid prefix `D-`).
- **Media embeds.** A unit's body embeds one of its attachments with
  `![Caption](media:<id>)`, where `<id>` is the media row id returned by the
  upload. `/api/media/<id>/raw` also works. `POST /api/agent/v1/media`
  defaults `kind` to `link`; pass `kind: "image"` or `"video"` (or use a file
  with an image or video extension) so ARK previews it.

### Body edits: read, build, compare, write

`POST /api/agent/v1/units/<uid>` replaces the whole body and has **no
concurrency guard**: there is no `If-Match`, revision or expected-hash field,
so a write made from a stale read silently overwrites a person's or another
agent's edit. Until ARK adds one, edit bodies like this:

1. **Read** the unit (`GET /api/agent/v1/units/<uid>`) and keep its `body`
   and `updated_at`.
2. **Back up** the old body to a file in the task folder. For important
   nodes, also turn on [text history](../guide/node-history.md)
   (`POST /api/agent/v1/units/<uid>/history` `{enabled: true}`); every agent
   write is then recorded as a revision and can be restored.
3. **Build** the new body offline from the body you read.
4. **Compare**, just before writing: read the unit again. If `body` or
   `updated_at` changed, stop, rebuild from the new text (or ask) and repeat.
5. **Write** only the changed fields, then **read back** and compare with what
   you sent.

The compare step narrows the race but does not close it. Node history does
have a real guard, but only for its own actions: `restore`, `apply` and
`checkpoint` take an `expected_hash` (the `current_hash` from
`GET /api/agent/v1/units/<uid>/history`) and refuse to run if the text
changed.

### Large lists

`GET /api/agent/v1/units` returns at most 500 units per call. Fetch the list
once, cache it to a file in the task folder and work from the cache; refresh
with `updated_since=<timestamp>` rather than re-reading everything. Use
`slim=1` (a 240-character excerpt instead of the body) when you only need
titles and status, and the filters (`types`, `statuses`, `q`) to fetch less.
The agent route currently ignores `offset` (the reply's `offset` is always
0), so a project with more than 500 units cannot be paged there; split the
fetch by filter instead.

## 2. Plan in a node first

Before a new research branch, or any request that takes more than a few
steps:

1. **Create the task node first** (`type: "task"`, status `active`) and write
   the plan into its body. Start with a short bullet summary, then:
   - the goal, and the user's request verbatim;
   - questions or hypotheses;
   - planned steps, ranked by expected gain against cost;
   - the evaluation protocol and acceptance rule;
   - the compute budget;
   - decision gates (what result changes the plan);
   - the timeline against any deadline: what lands before it, what after;
   - risks.
2. **Link it.** Exactly one `derived_from` parent: the core problem or parent
   task it comes from. This keeps the research lineage a tree that a person
   can follow back. Add `part_of` the storyline it belongs to and
   `relates_to` for other context. When branches change, update the
   project's lineage or story topic
   (`POST /api/agent/v1/units/<uid>/members`).
3. **Create a task folder** for the files: a folder in the primary
   repository, registered as a `task_space` resource in the setup register.
   Keep a frozen protocol file and a running state file there; reports and
   scripts go there too, never only in a temporary directory.
4. **Pre-register** the compared arms, metrics and the ranking or acceptance
   rule in the protocol before running anything. Later changes are dated
   amendments, and anything decided after seeing results says "post hoc".
5. **Record decisions** as decision nodes (see section 3).

## 3. Progress while working

- **Dated progress lines** in the task body, newest last, for example
  `2026-11-15 09:45 UTC · smoke run finished (job gpu_ab12), full run queued,
  ETA 14:00; next: evaluate`. Say what ran, job ids, ETA, problems and the
  next step. Use the body edit procedure above.
- **Honest status.** `active` while work runs, `review` when the user must
  look, `blocked` with the reason in the body, `done` only after acceptance.
  Never leave finished work `active`.
- **Resource paths.** Keep a "Resource paths" section on the task: data,
  checkpoints, run and evaluation folders, and any moved data with the link
  left behind. Record the same paths in the setup register:
  `POST /api/agent/v1/setup/location` adds a path to an existing resource;
  `POST /api/agent/v1/setup/resource` adds a new resource. Adding or changing
  a resource returns your setup layer to `draft` (`proposed` with
  `propose: true`), and `readiness.ready` stays false until you confirm it
  again in ARK. So batch new resources, send `propose: true` and tell the
  user to confirm.
- **Comments are instructions.** Read user comments on the nodes you own
  (`open_user_comments`). Do what they ask, reply to the comment with what was
  done (`reply_to`), resolve it, and record any decision it contains.
- **Decision nodes.** When the user decides something, in chat, in a comment
  or in reply to a question, create a `decision` node with the decision and
  its reason, `part_of` and `derived_from` the task. A decision node waits
  in **Needs you** as "Decide" until its status is `done` (or it is
  archived); when you record a decision the user has already made, set its
  status to `done`. For a question only the user can answer, create the
  decision node open and let them decide it there.

## 4. Results go to review nodes with their media

- **One result, one node.** Each result (an experiment, a figure set, a video
  grid, an audit, a progress check) is its own node, usually
  `type: "experiment"` or `"artifact"`, with status `review`, `part_of` and
  `derived_from` its task. Every unit with status `review` appears in **Needs
  you** on Home, where the user accepts it (status `done`) or sends it back.
  The parent task can stay `active` meanwhile.
- **Attach the media to that node itself.** Upload or attach each figure and
  video with the result node's uid. To reuse a file already attached
  elsewhere, `POST /api/agent/v1/media` with its project-relative `url` and
  the new `unit`. Needs you previews a node's own image and video
  attachments; only when it has none does it borrow previews from linked
  nodes, and an image referenced only from another node's body is never
  shown.
- **Figures beside their claims.** Embed each figure or video in the body
  next to the table row or claim it supports (`![Caption](media:<id>)`),
  followed by an italic caption line. Write captions for the reviewer: plain
  words, no internal codes, run ids or round numbers. Then one or two lines of
  reading: what it shows, the caveats (seeds, protocol differences) and what
  it does not show.
- **Numbers.** Headline numbers in a compact table. State negative results
  plainly.

## 5. Editing important documents

For documents the user owns (replies, paper text, reports):

- Show a **before/after preview** first and apply only after the user
  approves.
- **Snapshot first.** Turn on [text history](../guide/node-history.md) for the
  node, or create a dated version node with an identical body and the same
  media re-attached (the `media:<id>` references remapped to the new rows),
  and verify it. Then make the edit with the body edit procedure above, and
  check numbering and citations afterwards.
- Keep internal notices (`[CHECK]`, `[PENDING]`, `[NOT DONE]`) in ARK; strip
  them only in the copy that leaves ARK. Record each new writing rule on the
  node that holds the document's rules.

## 6. Delegation and monitoring

- One sub-agent per branch, with a validated brief
  (`POST /api/skills/agent-team/brief`; see
  [External agents](external-agent.md#dispatch-with-dsh-subagents)). The
  brief names the ARK unit ids, the `derived_from` parent, the task folder,
  the exact deliverables to put in ARK, and the rules: compute only through
  the queue, back up before editing, no edits to user-owned documents, ask
  before outward or irreversible actions.
- Pass the user's decisions to the owning agent at once and record them in
  ARK. Keep checking the queue and the agents, and report changes to the
  user.
- **Compute through the ARK GPU queue** (`POST /api/agent/v1/gpu/jobs`), not
  on the user's own machine. Pin the `server` when the data lives there, and
  record each returned job id in the task's progress line. The queue has no
  job-to-job dependencies; make a dependent step a job whose command checks
  its input before it starts, so nothing waits on a laptop.

## 7. Ask before

Ask the user before deploying or restarting services, actuating hardware,
editing user-owned documents, deleting or overwriting data, administrative
changes on servers and pushing. The setup register's
`readiness.requires_human` lists the actions this project's policy marks
`ask`. Record an approval given in chat in ARK, as a decision node or a
progress line.

## Known gaps

- **No guarded unit write.** `POST /api/agent/v1/units/<uid>` has no
  `If-Match` or expected-hash field. Use the compare-and-write procedure
  above; only node history actions take `expected_hash`.
- **No paging on the agent unit list.** `GET /api/agent/v1/units` accepts
  `limit` (at most 500) but ignores `offset`.
- **No job dependencies in the GPU queue.**
- **Adding a resource to the setup register** needs a new human
  confirmation before `readiness.ready` is true again.
