# Recommended agent working mode

How a long-running coordinator agent should use ARK day to day: plan in a task
node before working, keep progress and status honest, publish every result as
a review node with its own media, and record every user decision.

This is the working mode the Home starter prompts ask for in their core
rules (C1-C10). The prompts carry the rules; this page has the detail. Everything
below uses ARK's official agent HTTP API (`/api/agent/v1/...`) with a personal
agent token; see [External agents](external-agent.md) for authentication and
[Agent interface](agent-interface.md) for the contract.

## In short

These rules are complete on their own; the sections below add the detail.
The starter prompt carries the same rules as numbered lines (C rules: ARK's
protocol; G1: how a plan is laid out as nodes; P rules: the project's and
the user's preferences, set in Settings), with a rules version. `GET /api/agent/v1/rules` returns the
current ones: when its version differs from the one in your prompt, the
user changed a preference, so follow the new lines.
Read this whole page before your first write. It is about 35 KB: fetch it in
parts if your tools limit output, but do not stop at the first part.

1. **Plan in a node first.** One request is one task node with the plan in
   its body, `part_of` the line, topic or parent task it belongs to (and
   `derived_from` the question it comes from). Create it when the work starts; if the user asked to see
   the plan first, put it in the task with status `review` and wait. A
   plan for an idea is a small tree of nodes: question → plan task →
   1–3 claims → 1–3 tasks per claim (see [Planning graph](#planning-graph)).
2. **Progress goes in the task body.** A dated line per step, written with
   the read, compare, write procedure. Never post progress, plans or answers
   as comments.
3. **Hand-offs go to Needs you.** Whenever you deliver something or need
   the user (a result, a plan to approve, versions to choose from), set that
   node's status to `review` so it appears in **Needs you**, with a
   `review_note` in the same request: one to three sentences on what to
   check and what changed, at least one anchor into the body or its media,
   and after a send-back what was fixed for each comment. The server
   refuses `review` from an agent without it (422 `REVIEW_NOTE_REQUIRED`).
   A question is a `decision` node left open. Do not leave it only in chat, a comment or a
   body, and never set a `review` node to `done` yourself: the user accepts
   or sends it back.
4. **One result, one node.** `type: "experiment"` or `"artifact"`, status
   `review`, `part_of` and `derived_from` the task, its media attached to it
   and embedded beside the claim it supports.
5. **Record decisions.** Every decision the user makes (approving a plan,
   choosing an option, setting a scope) becomes a `decision` node, status
   `done`, `part_of` and `derived_from` the task.
6. **Watch for replies.** Check `GET /api/agent/v1/updates` (or
   `ark-server agent watch --once`) between steps, before every reply to
   the user and at least every 10 minutes during long jobs; act on comments,
   reply with `reply_to` and resolve them.
7. **Files only under this device's task space.** `GET
   /api/agent/v1/setup/paths` (with `X-ARK-Host`) returns this device's
   task space, repositories, storage and artifacts paths and what is
   `missing`. Everything outside the code repository (task folders,
   intermediate and resource folders, artifacts, downloads, renders) goes
   in the task's folder under the task space; no new top-level folders
   anywhere else. Without a task space, do not choose one: ask with
   `POST /api/agent/v1/setup/task-space-decision` and wait (see
   [Task space](#task-space)). Put the task folder on the task node as an
   absolute path with the device it is on, `Resource paths (device <device
   id> / <device name>): [Task folder](file:///<absolute path>)`, in a
   "Resource paths" section. Do not write the setup register during task work (it
   sends setup back for confirmation) and never confirm setup yourself.
8. **Announce presence** (`POST /api/agent/v1/presence`) while you work on
   a node, and post **one summary comment** on the main task when the
   session ends.
9. **Ask before** irreversible or outward actions, and record the approval.

Before every reply to the user, check: is everything they must look at in
`review` or an open decision, and does every hand-off to review carry an
anchored `review_note`? Is progress in the body, not in comments? Is
every new node linked to its task? Is no finished work left `active`? Is
every file you made under the task space (or the OS temp dir), with its
absolute path and device written in ARK?

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
| `ark-server agent watch` | print the user's replies and other updates as they arrive ([Watch for replies](#watch-for-replies)) |
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
| Ask for a review | `POST /api/agent/v1/units/<uid>` `{status: "review", review_note}` ([Review requests](#review-requests)) |
| Review a delivery of several units | `POST /api/agent/v1/reviews` `{title, summary, review_note, members}` ([Review groups](#review-groups)) |
| Review rounds and changes | `GET /api/agent/v1/units/<uid>/review` |
| Save a prompt for the person | `POST /api/agent/v1/prompts` `{title, description, body}` (personal; `scope: "project"` only for a project admin); `GET /api/agent/v1/prompts` lists them ([Prompt library](../guide/home-overview.md#more-prompts-the-prompt-library)) |
| Link two units | `POST /api/agent/v1/links` `{source, target, relation, note?}` |
| List a unit's links | `GET /api/agent/v1/units/<uid>/links` (`?direction=in\|out&relation=`): each with its `id` |
| Remove a link | `DELETE /api/agent/v1/links` `{source, target, relation?}`, or `DELETE /api/agent/v1/links/<id>` |
| Change a link's relation | `PATCH /api/agent/v1/links/<id>` `{relation}` |
| Attach a file or link | `POST /api/agent/v1/media/upload` (images) and `POST /api/agent/v1/assets/upload` (any file), both JSON `{unit, filename, data: <base64>, caption}`, not multipart; `POST /api/agent/v1/media` `{unit, url, kind, caption}` (an existing project path or URL) |
| Reply to / resolve a comment | `POST /api/agent/v1/units/<uid>/comments` `{body, reply_to}`; `POST /api/agent/v1/units/<uid>/comments/<id>/resolve` |
| Turn on text history | `POST /api/agent/v1/units/<uid>/history` `{enabled: true}` |
| Current agent rules | `GET /api/agent/v1/rules` (`?lang=zh`): `{version, lines, rules}`, the rules composed from ARK's protocol and this project's and your preferences; the starter prompt carries the same lines and their version |
| Topic members | `GET` / `POST /api/agent/v1/units/<uid>/members` ([Topics](../guide/topics.md#agent-workflow)) |
| Setup register | `POST /api/agent/v1/setup/resource`, `POST /api/agent/v1/setup/location` ([Shared project setup](../operations/multi-user-setup.md#setup-api)) |
| This device's paths | `GET /api/agent/v1/setup/paths` with `X-ARK-Host`: task space, repositories, storage, artifacts, `missing`; `POST /api/agent/v1/setup/task-space-decision` when the task space is missing ([Task space](#task-space)) |
| Move recorded paths | `POST /api/agent/v1/setup/relocate` `{from, to}` or `{logical: true}`, dry run unless `apply: true`; `{undo: "<id>"}` ([Task space](#task-space)) |
| Location sizes | `POST /api/agent/v1/setup/location-stats` `{items: [{resource, path_or_ref, bytes, files}]}` with this device's `X-ARK-Host`: optional, e.g. after registering or verifying a path ([Location sizes](../operations/multi-user-setup.md#location-sizes)) |
| GPU queue | `GET` / `POST /api/agent/v1/gpu/jobs`, `GET .../<id>/logs`, `POST .../<id>/cancel` ([GPU](../operations/gpu.md#queue-monitoring-api)) |

Notes on the fields, as ARK implements them:

- **Links** use the field `relation`; it defaults to `relates_to`. The
  relations used below are `part_of`, `derived_from`, `relates_to`,
  `supports` and `targets`; the full list is in the interface descriptor.
  To remove a wrong link, delete it by `source`, `target` and `relation`
  (without `relation` ARK answers 409 and lists the relations when the pair
  has several) or by the `id` from `GET /api/agent/v1/units/<uid>/links`;
  details in [Links](agent-interface.md#links).
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

### Task space

Each device has its own path for the project's task space (setup is per
person and per device). Read it before you create any file outside the
code repository:

```bash
curl -s -H "Authorization: Bearer $ARK_TOKEN" -H "X-ARK-Project: $PRJ"   -H "X-ARK-Host: $(ark-server agent host-id)" "$ARK/api/agent/v1/setup/paths"
```

The reply has `task_space` (`{id, key, path}` or null), `task_spaces`,
`repositories`, `storage`, `artifacts`, `writable` (every place registered
for this device: `[{kind, key, name, path}]`), `missing` (for example
`["task_space"]`), `task_folder` and `next`. With `?unit=<uid>`, or from a
session ARK launched (`X-ARK-Session`), `task_folder` also names the exact
folder of that unit's work: `{path, kind, owner, task, unit, unit_type,
exists, note}`. For a task it is the task's own folder; for any other unit
(an idea, a question, an experiment, a result) it is the folder of the
nearest task the unit belongs to (`part_of` / `derived_from` upwards), and
when no task is above it, the unit's own folder
(`<YYYYMMDD>_<its ID>-<name>/`, `kind: "unit"`). Asking for a unit on the
device the server runs on creates the folder. Folders follow the unit of
work, not the session: a session that moves on to another node or task asks
again (`?unit=<UID>`, or `ark-server agent folder <UID>`, which prints the
path). A task created for the same work as a node without a task above it
keeps that node's folder. Sessions ARK launches get the starting unit's
folder, created at launch, in their Start section, in `ARK-TASK.md` and as
`ARK_TASK_FOLDER`; a session started without a node has no folder until
the agent picks or creates one.

- Write or change files only in the places in `writable`; anything else
  needs the user's decision first.
- Task-related intermediate files and results go in the folder of the
  unit they belong to (`task_folder.path` for `?unit=<UID>`), with
  `artifacts/`, `downloads/`, `renders/`, `logs/` as needed: the task's
  folder `<task space>/<YYYYMMDD>_<task ID>-<name>/`, or, for a node with
  no task above it, the node's own folder. No other top-level folders: not
  `C:/Temp/...`, `C:/scratch/...` or a new `*-tasks` folder.
- Your own scratch and temporary downloads (API request bodies, probes,
  copies) may stay in your session workspace or the OS temp directory;
  anything a task needs later is moved into its folder. The session
  workspace is not kept.
- When `missing` has `task_space`, do not pick a folder and do not start
  the work: ask before your first download or file. Options in the OS temp
  directory or in a session workspace are refused. Look at the
  device (drives, free space, where the repository is, how the project's
  other devices lay it out) and ask:

```json
POST /api/agent/v1/setup/task-space-decision
{"options": [
   {"path": "C:/ark-tasks/my-project", "free_bytes": 812000000000, "note": "largest drive"},
   {"path": "C:/work/my-project-tasks", "free": "120 GB", "note": "next to the repository"}],
 "recommended": 0, "part_of": "T-0117", "lang": "en"}
```

  ARK opens the decision "Choose the task space on <device>" in Needs you.
  The user picks an option (or types another absolute path); ARK saves it
  in their own setup layer for that device and appends the outcome to the
  decision. The decision update wakes you; read `setup/paths` again and
  continue. This needs a personal agent token and `X-ARK-Host`.
- For another missing path (a repository on this device), ask in an open
  decision and register the answer with `POST /api/agent/v1/setup/location`.

**Paths in ARK are absolute, with their device.** Task spaces are not
necessarily synced between devices, so a folder on one computer may not
exist on another. Write the absolute path and the device it is on:

```text
Resource paths (device c59c4a65af13 / PC): C:\ark-tasks\20261124_T-0117-render\
```

You may add a `task:<relative path>` link next to it
(`[Task folder](task:20261124_T-0117/)`; a second task space is named with
`?space=<id>`). ARK resolves it with each device's own task space path. It
is an addition, never a replacement for the absolute path.

**Moving paths later.** `POST /api/agent/v1/setup/relocate` with
`{"from": "C:/old/tasks", "to": "C:/new/tasks"}` lists every unit body,
attachment link and setup location that would change (a dry run); add
`"apply": true` to write them (unit bodies in one transaction) and keep the
returned `relocation` id: `{"undo": "<id>"}` puts everything back.
`{"logical": true}` converts absolute paths inside the task space to
`task:` paths; it is optional and not recommended by default. Fenced code blocks, comments and session records are left
alone; files are not moved (move them first, then relocate). Ask the user
before applying either to a project. Settings › Setup › Devices has the
same tool, and `ark-server agent relocate` runs it from a shell.

## 2. Plan in a node first

Before a new research branch, or any request that takes more than a few
steps:

1. **Create the task node first** (`type: "task"`, status `active`; status
   `review` when the user asked to approve the plan first, and nothing runs
   until they do) and write the plan into its body. One request is one task
   node. Start with a short bullet summary, then:
   - the goal, and the user's request verbatim;
   - questions or hypotheses;
   - planned steps, ranked by expected gain against cost;
   - the evaluation protocol and acceptance rule;
   - the compute budget;
   - decision gates (what result changes the plan);
   - the timeline against any deadline: what lands before it, what after;
   - risks.
2. **Link it.** Exactly one `part_of` parent: the line, topic or parent task
   the new task belongs to. The Mind and tree views follow `part_of` only, so
   a task without it shows up on its own (Graph checks list it as "Task has
   no parent"). Add `derived_from` the question or idea it comes from, for
   provenance. This keeps the research lineage a tree that a person can
   follow back: line → task → the task's results and decisions (each
   `part_of` and `derived_from` the task). A plan for an idea adds its
   claims and their tasks below the plan task (next section); a result
   that bears on a claim is linked to it with `supports` or `contradicts`.
   Add `part_of` the storyline it belongs to when the project has one. When branches change, update the
   project's lineage or story topic
   (`POST /api/agent/v1/units/<uid>/members`).
3. **Lay the plan out as a planning graph** (G1) when the request is to plan
   an idea; see [Planning graph](#planning-graph) below.
4. **Create a task folder** for the files, inside a registered task space.
   A task space is a folder listed under `task_spaces` in the confirmed
   setup (`GET /api/agent/v1/setup`) that holds one task folder per task
   node, side by side:

   ```text
   <task space>/
     20261117_WEB-ARK-SITE-175/      one task node's files
     20261117_VIDEO-ARK-PROMO-176/   another task node's files
     archived/                       finished tasks, by class
   ```

   Each folder name starts with the creation date, `YYYYMMDD_`, followed by
   the task's ID (for an agent-team folder `<PREFIX>-<NAME>-<serial>`, with
   the serial unique across the whole task space). One task node has exactly
   one folder; a retry or revision reuses it. As soon as the folder exists,
   write it into the task node's "Resource paths" section as a link,
   `[Task folder](file:///data/me/tasks/20261117_T-0042)`, so a person can
   open it from the node: ARK opens `file:` links on the person's computer
   (or offers the path to copy when the files are on the server); a plain
   path in the body is not clickable. Keep a frozen
   protocol file and a running state file there. Everything needed to
   reproduce or continue the work lives there too: scripts and generators
   (render, capture, analysis), configs, prompts, reports and selected
   figures, kept in version control. Outputs those files can regenerate
   (renders, screenshots, builds, caches) go in the task's git-ignored
   `artifacts/` folder; only such reproducible outputs may be dropped.
   Never keep task files in a temporary directory or any other path that is
   not registered; caches and scratch data are the only exception. When the
   project has no task space yet, propose one
   (`POST /api/agent/v1/setup/resource` with `kind: "task_space"`) and ask
   the user to confirm it before writing files.
5. **Pre-register** the compared arms, metrics and the ranking or acceptance
   rule in the protocol before running anything. Later changes are dated
   amendments, and anything decided after seeing results says "post hoc".
6. **Record decisions** as decision nodes (see section 3).

### Planning graph

When you plan an idea, build a small tree a person can read from the top
(core rule G1):

```text
question
  plan task            the request's task node, the whole plan in its body
    claim              one testable statement and what would settle it
      task             one piece of work that tests it, status open
      task
    claim
      task
    decision           the user's approval of the plan
```

- **Question**: the open question the plan answers (`type: "question"`;
  create it if there is none). When the project has a line or topic, the
  question is `part_of` it.
- **Plan task**: the request's task node (C2), `derived_from` the question
  and `part_of` its line, topic or parent task; when there is none, `part_of`
  the question. Its body holds the whole plan.
- **Claims**: 1–3, one per independent part of the question (one for a
  narrow yes/no question). Each states what result would support or refute
  it.
- **Tasks under a claim**: 1–3 per claim, each one piece of work with a
  measurable outcome, status `open` until it runs. Steps of a task stay in
  its body, not in nodes of their own.
- Every claim and task below the plan task is `part_of` and `derived_from`
  its parent. A first plan has 6–15 nodes.
- Later, each result is `part_of` and `derived_from` the task that produced
  it (C4) and links to the claim it bears on with `supports` or
  `contradicts`.

Before you hand the plan over, read the graph back
(`GET /api/agent/v1/units`) and check: every node has its parent; no two
nodes say the same thing; every task tests a claim and every claim has at
least one task; no node without a purpose (no node per plan step, no empty
placeholder).

## 3. Progress while working

- **Dated progress lines** in the task body, newest last, for example
  `2026-11-15 09:45 UTC · smoke run finished (job gpu_ab12), full run queued,
  ETA 14:00; next: evaluate`. Say what ran, job ids, ETA, problems and the
  next step. Use the body edit procedure above.
- **Announce what you work on.** For work on one node that takes longer
  than a minute, send `POST /api/agent/v1/presence {"unit": "<uid>",
  "state": "working", "note": "<what>"}` and `"state": "done"` when you
  finish; the project's graphs show your badge on the node meanwhile.
- **Honest status.** `active` while work runs, `review` when the user must
  look, `blocked` with the reason in the body, `done` only after acceptance.
  Never leave finished work `active`.
- **Resource paths.** Keep a "Resource paths" section on the task: the task
  folder link, data, checkpoints, run and evaluation folders, and any moved
  data with the link left behind. A task folder inside a registered task
  space needs no setup change. Do not write the setup register during task
  work: adding or changing a resource (`POST /api/agent/v1/setup/location`
  or `/setup/resource`) returns your setup layer to `draft`, and
  `readiness.ready` stays false, which blocks task work, until a person
  confirms it again in ARK. List the new resources for the user instead,
  or, when they ask you to register them, batch them, send
  `propose: true` on the last write (or `POST /api/agent/v1/setup/propose
  {}`) and ask them to confirm. Never confirm a setup layer yourself.
- **Comments are instructions.** Read user comments on the nodes you own
  (`open_user_comments`). Do what they ask, reply to the comment with what was
  done (`reply_to`), resolve it, and record any decision it contains. New
  comments reach you through the update feed ([Watch for
  replies](#watch-for-replies)).
- **Decision nodes.** When the user decides something, in chat, in a comment
  or in reply to a question, create a `decision` node with the decision and
  its reason, `part_of` and `derived_from` the task. A decision node waits
  in **Needs you** as "Decide" until its status is `done` (or it is
  archived); when you record a decision the user has already made, set its
  status to `done`. For a question only the user can answer, create the
  decision node open and let them decide it there.

## Watch for replies

The user answers in ARK, not in your chat: a comment on a node, **Send back**
or **Accept** on a review node in Needs you, an answer to a decision, a new
task assigned to them, a confirmed setup layer. ARK does not interrupt you,
so check for these yourself.

### When to check

- **Between steps**, before you start the next one.
- **At least every 10 minutes** while a long job runs (training, a sweep, a
  large GPU queue): keep a watcher running in the background, or check from
  the loop that waits for the job.

### Work that ends wakes you too

You do not need to poll for your own long work:

- **A GPU queue job** you queue (`POST /api/agent/v1/gpu/jobs`) wakes you
  when it ends (completed, errored, cancelled, timed out or lost). A session
  ARK launched gets an `ARK UPDATE` message with the job id, name, server,
  state, exit code, duration, the last log lines of a failed job and the log
  link; send `"session": "$ARK_SESSION_ID"` with the job when that variable
  is set. Any other agent gets a `gpu_job_done` item in its update feed, so
  its watcher wakes; only the token that queued the job sees it.
- **Background commands and sub-agents** of a Claude Code session ARK
  launched: Claude Code takes up a finished background command itself; when
  it does not within about 15 seconds, ARK sends one `ARK UPDATE` message
  listing what finished, with exit codes and the output tail. Codex ends its
  background terminals with the turn: queue long runs on the GPU queue.

These messages come between turns, never during one. So do not poll
`gpu/jobs` or wait in `sleep` loops; stop any polling loop at the first
`401` (a revoked or expired token).
- **Before you report done**, and before you set a task to `review` or
  `done`: an answer may already be waiting.
- **After a pause** (a new session, a restart): run the watcher once before
  doing anything else.

### Start a watcher that wakes you

Checking between steps covers the time you work.  Once you end your turn
and sit idle, nobody reads the feed, so a reply the user leaves in ARK
waits until they write to you in chat.  Start a watcher in the background
first thing after setup: it waits for news for you (a person's comment,
reply, Send back, Accept or decision on your units, or a change of the
agent rules in Settings), prints it and exits.  The exit is what wakes you.

**The token stays out of command lines.**  Every process on a machine can
read every other process's command line, and the watcher runs for hours.
Save the token once in a file only you can read and let the watcher read it
there:

- `~/.ark/tokens/<project id>` (Windows
  `%USERPROFILE%\.ark\tokens\<project id>`), the token alone on one line,
  mode 600.  Write it with your file-writing tool, not with a shell command
  that contains it.  This is the one place a token may be written.
- Or, where ARK launched the session, use `ARK_TOKEN` from the environment
  it was given.

**Use `ark-server agent watch` where you can.**  It is a plain program, so
nothing is downloaded and run, which antivirus software tends to flag:

```bash
ark-server agent watch --until-update --url "$URL" --project prj_... --name <your name>
```

It reads the token file (or `ARK_TOKEN`; `--token` would put it on the
command line), long-polls `GET /api/agent/v1/updates?mine=1&for_you=1&wait=50` with a
saved cursor (`.ark-watch-cursor`), so it costs one quiet request a minute,
and gives up after `--max` seconds (default 6900, under two hours) with the
line "no updates"; then start it again.  A refused token (HTTP 401 or 403)
stops it with exit status 3.  Where to find it:

| Where | `ark-server` |
| --- | --- |
| Sessions ARK launched | on the PATH |
| Windows desktop app | `%LOCALAPPDATA%\ARK Studio\ark-server.exe` (installer for the current user), `C:\Program Files\ARK Studio\ark-server.exe` (MSI), beside `ark-studio.exe` (portable zip) |
| macOS desktop app | `/Applications/ARK Studio.app/Contents/MacOS/ark-server` |
| Linux desktop `.deb` | `/usr/bin/ark-server` |
| Server packages | `/usr/bin/ark-server` (`.deb`) or where you unpacked `ark-server-<version>-<os>-<arch>` |

Try `ark-server version` on the PATH first, then these paths.  The Linux
AppImage keeps it inside the image: use the server package there.

**Without ark-server**, use the script ARK serves: read `GET
/api/agent/v1/watcher.sh` (Windows: `watcher.ps1`; no token needed), save
it with your file-writing tool as `ark-watch.sh` (`ark-watch.ps1`) and run
that file.  Never pipe a download into a shell (`curl ... | sh`, `iex (irm
...)`).  The scripts take their settings from the environment:

```bash
ARK_URL="$URL" ARK_PROJECT=prj_... ARK_AGENT_LABEL=<your name> sh ark-watch.sh
```

```powershell
$env:ARK_URL=$url; $env:ARK_PROJECT='prj_...'; $env:ARK_AGENT_LABEL='<your name>'
powershell -NoProfile -ExecutionPolicy Bypass -File ark-watch.ps1
```

`watcher.ps1` makes its requests with `curl.exe` (headers on its standard
input).
`ARK_WATCH_MAX` sets the time limit (`0` checks once).

**It belongs to the agent that started it.**  The watcher walks up its own
parent processes to the nearest Codex or Claude Code process (or the node
process of a CLI wrapper) and stops within a minute after that process
ends.  It reads only parent process ids and names.  `--owner-pid`
(`ARK_OWNER_PID`) is optional; if you pass it, give your agent's process,
not the shell's: in a POSIX shell that is `$PPID`, never PowerShell's
`$PID`, which is the short-lived shell that ran the command.  A given
process that is gone, or is only the shell, is replaced by the nearest
agent, and the watcher says so.  Every message it prints or queues starts with `ARK UPDATE for
<agent>, ARK project <name> (<id>)`, as do the reminders ARK sends the
sessions it launched.  A message that does not name your own project and
session is not for you: ignore it.  Never read other processes' command
lines or environment to find a token or a session id.

How each agent waits:

- **Claude Code**: run the command with the Bash tool, `run_in_background:
  true` and `timeout: 7200000`.  When it exits, the task notification
  wakes you even after your turn ended.  Read what it printed (or `GET
  /api/agent/v1/updates`), act on it under the rules (reply with `reply_to`,
  resolve, record decisions, set `review`), then start the watcher again.
  The watcher never calls `codex queue` from Claude Code, whatever
  `CODEX_THREAD_ID` the environment happens to hold.
- **Codex**: a background terminal does not wake Codex when it exits, but
  `codex queue --thread <id> --message <text>` delivers a message to the
  running session and starts a turn even when it is idle.  Ask for it
  explicitly with your own thread id and process:

  ```bash
  ark-server agent watch --until-update --url "$URL" --project prj_... --name <your name> --wake codex --codex-thread "$CODEX_THREAD_ID"
  ```

  (PowerShell: `--codex-thread $env:CODEX_THREAD_ID`; script:
  `ARK_WAKE=codex ARK_CODEX_THREAD="$CODEX_THREAD_ID"`).  The watcher
  queues only when the owner process is Codex, no Claude Code environment
  is present, and the thread id is the one Codex gave this command.  An
  inherited `CODEX_THREAD_ID` alone never wakes anything.  When it cannot
  wake you it prints `cannot wake codex: <reason>; <what to do>` and exits
  with status 2: fix that and start it again, or check between steps as
  below.  It then starts itself again on its own (Codex ends a
  command's process group when the command returns), keeps watching with no
  time limit unless one is set, stops when that Codex process ends, and on
  news queues a message listing the updates.  When it arrives, act on it
  and start the watcher again.  In Codex's sandbox each command runs in its
  own process namespace that ends with the command, so the background
  watcher needs a session without the sandbox (full access); otherwise
  check between steps as below.
- **Other agents** without a way to be woken: run `ark-server agent watch
  --once` (or the script with `ARK_WATCH_MAX=0`) between steps and before
  you end every turn, and say in your last message that replies left in ARK
  are read when the user writes to you again.
- **Sessions ARK launched** (Run panel, session sidebar) get
  `ARK_SESSION_ID`, and `ARK_WAKE=ark` where ARK wakes them itself (Claude
  Code and Codex runs).  Then start no watcher: ARK sends a running, idle
  session one short input turn listing people's new updates, and continues
  a finished run (`claude -p`, `codex exec`, within a day and natively
  resumable) with that turn.  The watcher scripts and `ark-server agent
  watch --until-update` exit at once there.  Processes the session starts
  (sub-agents, helper runs) inherit both variables and start no watcher
  either: they are part of the session, ARK's turn goes to the session ARK
  launched, and it passes the work on.  Without `ARK_WAKE=ark` (other
  platforms) use the watcher as above.  ARK does not remind a session whose
  own watcher is waiting on the feed, so each update wakes a session once.
- **The token of a session ARK launched** is its own: `ARKT` (also
  `ARK_AGENT_TOKEN` and `ARK_TOKEN`), write scope with full access, read
  scope otherwise, revoked when the run ends.  Use it for every agent API
  call and send `X-ARK-Session: $ARK_SESSION_ID`; ignore a token pasted into
  the chat and the files in `~/.ark/tokens`, and save no token file.  Writes
  made this way appear on the session in the support panel instead of as a
  separate agent.  `ark-server agent ...` and the watcher scripts prefer the
  session's token and send the header by themselves.
- **The first step of a session ARK launched** is the Start section at the
  end of its session block; no Home prompt needs to be pasted.  The person
  picks it in the Run dialog (Start with: any prompt of the Home library, or
  a free conversation); the default is setup when the project's or the
  person's setup is not confirmed, registering this device's paths when it
  lacks them, continuing the chosen task (with its open send-back comments
  listed) when one is chosen, and otherwise following the first message or
  waiting for it.  A later run of the same session starts with Resume.
  `GET /api/agent/v1/context` repeats the start as `session_start` for a
  request with the session's token or `X-ARK-Session`.

### The watcher

`ark-server agent watch` follows the update feed and prints one line per
update. It reads the token from `ARK_TOKEN` (or `--token-env NAME`, or
`--token`) and keeps its cursor in `.ark-watch-cursor` in the working
directory, so each run prints only what is new. The first run prints nothing
and starts the feed at "now".

```bash
export ARK_TOKEN=arka_...            # your personal agent token
export ARK_URL=https://ark.example.org
export ARK_PROJECT=prj_...

# Between steps: print what is new, then exit.
ark-server agent watch --once

# In the background while a long job runs: long-polls, prints as updates arrive.
ark-server agent watch >> ark-updates.log 2>&1 &

# For a script: one JSON object per line.
ark-server agent watch --once --json --kinds comment,reply,sent_back,decided
```

| Option | Meaning |
| --- | --- |
| `--url`, `--project` | ARK server and project (env `ARK_URL`, `ARK_PROJECT`) |
| `--token`, `--token-env` | the agent token, or the variable that holds it (default `ARK_TOKEN`) |
| `--name` | your agent name, sent as `X-Agent-Name`; a comment that writes `@<name>` mentions you |
| `--wait S` | long-poll seconds per request, at most 60 (default 50; 0 with `--once`) |
| `--interval S` | seconds between requests with `--wait 0`, and after a network error (default 30) |
| `--once` | one check, then exit |
| `--json` | print each update as one JSON line (the item format below) |
| `--kinds a,b` | only these kinds |
| `--mine` | only units you or your user created, or that are assigned to you |
| `--for-you` | only updates meant for you (`for_you`); always on with `--until-update` |
| `--state FILE` | cursor file (default `.ark-watch-cursor`) |

Exit status: 0 normal, 1 a network or server error with `--once` (without
`--once` it retries every `--interval`), 2 a usage error, 3 the token was
refused (401 or 403). A long-running watcher stops on Ctrl+C.

A default line looks like this:

```text
11-17 14:02  sent_back        alice sent back T-0042 "Ablation": add the seed-2 baseline  [comment 318]  /api/agent/v1/units/T-0042/comments
11-17 14:05  >>> MESSAGE #4 from alice: pause the sweep after this epoch
             reply: POST /api/agent/v1/messages/4/reply {"body": "..."}
```

### The HTTP route

Without `ark-server`, poll the feed directly:

```bash
# Start: the reply has a cursor and no items.
curl -s -H "Authorization: Bearer $ARK_TOKEN" -H "X-ARK-Project: $ARK_PROJECT" \
  "$ARK_URL/api/agent/v1/updates"
# Later: what is new since the cursor; wait up to 50 s for something to arrive.
curl -s -H "Authorization: Bearer $ARK_TOKEN" -H "X-ARK-Project: $ARK_PROJECT" \
  "$ARK_URL/api/agent/v1/updates?since=$CURSOR&wait=50"
```

`GET /api/agent/v1/updates` takes:

| Parameter | Meaning |
| --- | --- |
| `since` | the `cursor` of the previous reply; omit it on the first call |
| `wait` | seconds (0–60) to hold the request until something arrives; 0 returns at once |
| `kinds` | comma-separated kinds to return |
| `mine=1` | only units you or your user created or that are assigned to you |
| `for_you=1` | only the items meant for you (`for_you: true`, below) |
| `limit` | activity rows read per call (1–500, default 100) |

The reply is `{cursor, items, count, more, reset, mine, kinds, waited_ms,
for_you_count}`; `for_you_count` counts the items meant for you.
Save `cursor` and send it as `since` next time. `more: true` means more
updates are waiting: call again at once. `reset: true` means the cursor is
not valid for this project any more (a foreign or malformed cursor, or a
restored snapshot); the reply starts again at "now", so read the open
comments of your units once (`GET /api/agent/v1/units/<uid>/comments`). With
`wait`, one token may hold at most 4 waiting requests (more get 429
`AGENT_WAIT_LIMIT`). The cursor is opaque: store it, do not build or compare
it.

Each item:

```json
{"id": "a1832", "kind": "sent_back", "unit": 412, "uid": "T-0042", "title": "Ablation",
 "type": "task", "status": "active", "actor": "alice", "at": "2026-11-17T14:02:11+00:00",
 "summary": "alice sent back T-0042 \"Ablation\": add the seed-2 baseline",
 "comment": {"id": 318, "body": "add the seed-2 baseline", "kind": "sent_back", "reply_to": null, "resolved": false},
 "change": {"field": "status", "from": "review", "to": "active"},
 "link": "/api/agent/v1/units/T-0042/comments",
 "for_you": true, "reason": "you submitted T-0042", "audience": "session", "target_sessions": ["s_20261117abcd1234"]}
```

**Act on the items with `for_you: true`; the others are context only.**
Every agent of the project sees every update, so it can follow what happens
around it, but an update is meant for the agent that wrote the unit:

- A person's comment, Send back, Accept, decision, status or importance
  change, delete or move on a unit goes to the agents that last handed it to
  review, created it, or last wrote it (`audience: "session"`, with their
  ARK session or agent session ids in `target_sessions`). `reason` says why:
  `you submitted T-0042`, `you created T-0042`, `you last edited T-0042`.
- A reply goes to the agent that wrote the comment it answers
  (`reply to your comment #142`).
- When no writer of the unit is known, or the ARK sessions that wrote it
  have ended, the update goes to the project as before (`audience:
  "project"`, `reason: "fallback: no owner"`): it is for you when it
  concerns units of your account.
- Messages, finished GPU jobs, mentions, tasks assigned to you and setup
  confirmations are for you as before.

ARK tells agents apart by what their writes carry: the ARK session
(`X-ARK-Session`, or a session's own token), else your own session id in
`X-ARK-Agent-Session` (any stable id of your Claude Code or Codex session;
the watchers send `ARK_AGENT_SESSION`), else the token and device
(`X-ARK-Host`). Send the same headers on your writes and on the watcher,
or two agents that share a token on one device both get each other's
updates. The watchers and the wake-ups of sessions ARK launched wake you
only for items meant for you.

Items come in the order things happened. `comment`, `change`, `layer` and
`message` appear only for the kinds that have them; `status` is the unit's
status now. Only people's actions are in the feed: writes made with agent
tokens (yours and other agents') are left out.

### What to do with each kind

| Kind | What happened | What to do |
| --- | --- | --- |
| `comment` | a person commented on a unit | treat it as an instruction: act on it, reply with `reply_to` set to `comment.id`, resolve it |
| `reply` | a person replied to one of your comments | read the thread, act, reply, resolve |
| `mention` | a comment says `@agent`, `@agents` or `@<your --name>` | as `comment` |
| `sent_back` | Send back on a review node (its status is `active` again) | the comment says what is missing: fix it, add a dated progress line, reply to the comment, publish again with status `review` |
| `accepted` | Accept on a review node (status `done`) | note it on the task, take any remark in the comment as an instruction, go on to the next step |
| `sent_back` / `accepted` with `review` | a person decided on one of your review groups (`review.members` has each member's outcome) | members sent back also get a `sent_back` comment: fix them, then resubmit the group; accepted members are done |
| `comment` with `review` | a person commented on one of your review groups | answer with `POST /api/agent/v1/reviews/<id>/comments` `{body}` and act on it |
| `decided` | a decision node was answered (status `done`) | follow the decision; if it changes the plan, update the task body and say so in a progress line |
| `comment_reopened` | a resolved comment was reopened | the user is not satisfied: read it again and act |
| `status`, `assignee`, `importance` | a person changed one of your units (`change` has from and to) | follow the change: stop work on a unit set `blocked` or `abandoned`, pick up a unit set `active`, re-plan by importance |
| `archived` | a person deleted one of your units | stop work on it; ask if the reason is unclear |
| `moved` | one of your units moved to another project (`moved_to`: project, uid) | continue there with that project's id; see [Moving project content](project-content.md) |
| `assigned` | a unit was assigned to your user (on create: tasks only) | read it; start it when the current work allows, or say when you will |
| `decision` | a person created a decision node | if it concerns your work, give it the facts it needs, and wait for the answer before the step it decides |
| `setup_confirmed` | a setup layer was confirmed (`layer`) | read `GET /api/agent/v1/setup` again; work that waited for setup can start |
| `message` | a person sent you a message (experimental) | answer it with `POST /api/agent/v1/messages/<id>/reply` `{body}`, or mark it read with `.../ack` |
| `gpu_job_done` | a GPU job you queued ended (`job`: id, name, server, status, exit_code, duration_s, error, log_path) | read the log (`link`), record the result in the task's progress line, go on with the step that waited for it |

Record a decision that arrives in a comment as a decision node (section 3).
When you need the user again (a question back), reply on the comment rather
than in your chat, so the answer stays in ARK.

### What the feed does not have

- **Comment edits.** ARK comments cannot be edited; a correction arrives as a
  new comment or a reopened one.
- **Project-wide instructions.** ARK has no project-level instruction field;
  the user writes instructions as comments or in the task body.
- **Body edits** of your units are not items; read the unit again before you
  write it (the body edit procedure above).

## 4. Results go to review nodes with their media

- **Every hand-off goes to Needs you.** When you deliver something or need
  the user (a result, a plan to approve, versions to choose from, a
  report), set the node that holds it to `review`; a question only the user
  can answer is a `decision` node left open. Both appear in **Needs you**,
  where the user looks first. Text in chat, in a comment or in a body that
  is not in `review` is easy to miss. Only the user closes a `review` node
  (Accept or Send back); a "looks good" in chat is a decision to record,
  not a reason to set the node `done` yourself.
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

### Review requests

Every time a unit enters review, ARK opens a review round: Needs you shows
the round number, what the reviewer said in the previous round (and whether
it is resolved), and what changed in the body and attachments since the
reviewer last accepted or sent the unit back.

- **Say what to look at.** Every hand-off to review carries a
  `review_note`, sent together with `status: "review"`: one to three
  sentences on what the person should check and what changed, with at
  least one [anchor](#review-anchors) into the unit body or its media
  (20 to 500 characters). After a send-back, say what you fixed for each
  comment. Example: `{"status": "review", "review_note": "Check
  [Results](#results): acc is 0.61 with three seeds; the baseline row in
  [Baseline](#baseline) is new."}`.
- **The server refuses a hand-off without it.** An agent write (agent API,
  personal or session token, including `POST /api/units/batch` and units
  created in review) that puts a unit into review with no note, a note under
  20 characters, or a note without an anchor when the body has headings fails
  with 422 `REVIEW_NOTE_REQUIRED` and changes nothing; the message says what
  to send and lists the body's headings. The same holds for submitting or
  resubmitting a [review group](#review-groups). People in the web UI are
  only reminded.
- The card shows the note as
  "Review this" instead of the start of the body; without it the card says
  that nothing was said and shows the newest part of the body. Set it again
  while the unit is in review to replace it (no new round). A note without
  status `review` is rejected.
- **The reply lists anchors that do not resolve** in `review_warnings`
  (`[{anchor, problem}]`); fix the note or the body.
- `GET /api/agent/v1/units/<uid>/review` returns the rounds, the current
  note with its anchors, the previous round's comments and the line diff
  (`diff.hunks`, `media_new`) the reviewer sees.

### Review anchors

An anchor is the target of a Markdown link in a note (or a bare media
reference). Clicking it opens the unit in the side panel scrolled to the
place and highlights it; media open in the viewer, videos at the time.

| Anchor | Points to |
| --- | --- |
| `[label](#slug)` | a heading (or a `{#id}` block) in the reviewed unit's body |
| `[label](T-0042)` | another unit |
| `[label](T-0042#slug)` | a heading or block in another unit |
| `[label](media:421)`, `media:421@0:08`, `media:421@0:08-0:12` | an attachment; a video from 0:08 (to 0:12) |

A heading's slug is its text, lower-cased, with every run of characters that
are not letters or digits replaced by `-` and none at either end:
`## 时间重排 (v2)` is `#时间重排-v2`, `### Results: seed 2` is
`#results-seed-2`. The second heading with the same slug gets `-1`, the third
`-2`. To anchor a paragraph, or to keep an anchor stable when a heading
changes, end the heading or paragraph with `{#id}` (the marker is not
shown): `Contact rate rises to 0.67. {#contact-rate}`. Use ATX headings
(`#`).

When a heading changes after the note was written, a `#slug` that no longer
matches opens the closest heading: one whose text equals the slug or the
link text once trailing parentheticals such as `(v2)` or `（DEX-1c）`,
punctuation, case and width are ignored, else the clearly most similar
heading text. The anchor then has `approx` (`"normalized"` or `"fuzzy"`),
`slug` is the heading it opens, `written_slug` the slug as written, and
`review_warnings` suggests the current slug. An anchor with no plausible
heading stays broken.

### Review groups

When one delivery puts several units up for review (a set of evidence, the
results of one run), submit one review group instead of setting each unit to
review. A group is only for that: a single unit is set to review with a
`review_note`; a group of one member is rejected.

Create a group only when it is needed: one delivery, one review.  Do not
also set the members to `review` (the group is the review request), and do
not create a group for a plan or a single result.  This is the project
preference *One delivery, one review*, on by default; with it off, set each
unit to review with its own anchored `review_note`.

```
POST /api/agent/v1/reviews
{"title": "HOI-215 B2: GPU simulation results",
 "summary": "Eight measurements from agent/HOI-215-b2-gpusim @ 0035966; independent review: accepted with fixes.",
 "review_note": "Start with [latency](E-0050#results) and [the replay video](media:421@0:08-0:12)",
 "members": [{"uid": "E-0050", "note": "[the table](#results)"}, "E-0051", "E-0052"]}
```

- The reply is `{review, warnings}`; `review.id` is `RG-<n>`. Members keep
  their own status (they need not be in review). The group is one card in
  Needs you with a row per member (claim, headline number, independent
  review verdict, changes since the last review).
- The person accepts the group, sends it back, or decides per member. A
  member in status `review` is set `done` or `active` with the group. Every
  member sent back gets a `sent_back` comment that names the group.
- After a send back, fix the members and resubmit:
  `POST /api/agent/v1/reviews/<id>` `{status: "review", review_note}` (a new
  round; accepted members stay accepted). The resubmission needs a new
  `review_note` that says what was fixed for each comment; without one, or
  with the previous note unchanged, it fails with 422
  `REVIEW_NOTE_REQUIRED`. `{status: "closed"}` drops a group
  you will not resubmit. Title, summary, note and `members` can be changed
  the same way.
- Comments on the group: `POST /api/agent/v1/reviews/<id>/comments`
  `{body}`; resolve with `.../comments/<cid>/resolve`.
- When every member is accepted the group is removed. It never appears in
  the graph, the map, search, counts or exports; the activity log and each
  member's review rounds keep the record ("accepted in review group ...").
- `GET /api/agent/v1/reviews` lists the open groups, `GET .../reviews/<id>`
  one group with each member's diff.

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
  record each returned job id in the task's progress line. The job's end
  wakes you ("Work that ends wakes you too"); do not poll. The queue has no
  job-to-job dependencies; make a dependent step a job whose command checks
  its input before it starts, so nothing waits on a laptop.

## 7. Ask before

Ask the user before deploying or restarting services, actuating hardware,
editing user-owned documents, deleting or overwriting data, administrative
changes on servers and pushing. The setup register's
`readiness.requires_human` lists the actions this project's policy marks
`ask`. Record an approval given in chat in ARK, as a decision node or a
progress line.

Copying nodes from another project, splitting a project or merging two is
always approved in ARK: read [Moving project content](project-content.md)
and follow its inspect, plan, approve, apply, report flow.

## Known gaps

- **Updates are pulled.** ARK does not push to an agent running outside ARK;
  run the watcher (above). Agents started from ARK's Execute view can also be
  steered directly, and people can send messages that the feed delivers (an
  experimental option, see
  [Agent messaging](../development/agent-messaging.md)).

- **No guarded unit write.** `POST /api/agent/v1/units/<uid>` has no
  `If-Match` or expected-hash field. Use the compare-and-write procedure
  above; only node history actions take `expected_hash`.
- **No paging on the agent unit list.** `GET /api/agent/v1/units` accepts
  `limit` (at most 500) but ignores `offset`.
- **No job dependencies in the GPU queue.**
- **Adding a resource to the setup register** needs a new human
  confirmation before `readiness.ready` is true again.
