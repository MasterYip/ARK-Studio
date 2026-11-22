# Moving project content: import, split, merge

> 中文摘要：在项目之间整理内容有三种操作——**导入**（从另一个项目复制节点到本项目，来源不变）、**拆分**（把联系很少的分支移到新项目）、**合并**（把整个项目并入另一个，之后原项目只读）。agent 的流程固定为：**查看 → 提出计划 → 交给人在 Needs you 里批准 → 执行 → 汇报**。agent 不能自己批准，也不能在批准前执行（服务端会拒绝）。编号在目标里没有重复就保留，有重复则整批重编号，旧编号仍能找到新位置。下面是英文的完整说明。

ARK moves and copies nodes between projects with everything they carry:
links, attachments, tags, topic layout and, for moves, comments and text
history. There are three operations:

| Operation | What happens | Typical request |
| --- | --- | --- |
| **Import** (copy) | nodes of another project are copied into this one; the source is not changed | "bring the ideas about X from BrainStorm into this project" |
| **Split** | a few branches (a root and its descendants through `part_of`, else `derived_from`) move into a **new** project | "this project mixes two lines of work, split it" |
| **Merge** | every node of this project moves into another project; this project becomes read-only | "fold this project into Y" |

Moving some nodes into an existing project is not an operation: split them
off, then merge, or import them (a copy).

## The flow

Every operation follows the same five steps. Nothing changes before step 4.

1. **Inspect.** Read what would move or be copied (sections below).
2. **Plan.** `POST .../plans` returns a plan with its summary (nodes, new
   IDs, links, links cut by a split, attachments and their size). ARK opens a
   decision node with the summary in **Needs you**.
3. **Hand to the human.** Link the decision node to your task (`part_of`
   and `derived_from`), add a progress line ("waiting for approval of plan
   `pl_...`") and wait. Only a person approves; `/approve` and `/reject`
   refuse agent tokens with 403. The answer closes the decision node, so it
   reaches you as a `decided` item in the update feed.
4. **Apply.** When `GET .../plans/<id>` shows `state: "approved"`, call
   `POST .../plans/<id>/apply`. If the person chose "Approve and move" the
   state is already `applied`; do not apply again.
5. **Report.** In your task: what moved or was copied where (project and
   new IDs), the links a split cut and how they were handled, anything the
   summary flagged. Set the result to `review` as usual.

| `state` | meaning | what you do |
| --- | --- | --- |
| `proposed` | waiting for the person | nothing; keep watching |
| `approved` | approved, not applied yet | apply |
| `rejected` | rejected (`comment` says why) | stop, or propose a different plan if the comment asks for one |
| `applied` | done | report |
| `undone` | reversed | report |

`apply` before approval answers 409 `MOVE_NOT_APPROVED`. When what the plan
covers changed after it was made, `apply` answers 409 `MOVE_PLAN_STALE`:
propose a new plan; it needs a new approval. `POST .../plans/<id>/undo`
reverses an applied plan; undo only when the person asks.

Plans of a project: `GET /api/agent/v1/project-move/plans` (all kinds).
`GET`, `apply` and `undo` of one plan work under either base below.

## IDs and references

- When none of the nodes' IDs is used in the target, every ID stays (a
  split into a new project always keeps them). If one is used, the whole
  batch gets new numbers from the target's counters. `summary.units` lists
  `uid` → `new_uid`.
- After a move the old ID keeps leading to the new place (an alias). A write
  to a moved node answers 409 `UNIT_MOVED` with its new project and ID;
  agents working on it get an update item of kind `moved` with `moved_to`.
- `[[UID]]` and `media:<id>` references in the moved or copied text are
  rewritten. A `[[UID]]` of a node that stayed behind becomes
  `[[prj_xxx/UID]]`, which opens the other project. IDs written as plain
  text are not changed.
- Node details show where a node came from or went and its links to other
  projects ("Other projects").

## Import (copy)

Base: `/api/agent/v1/import-ark`, with `X-ARK-Project` the **target** (this
project). You need write access here and read access to the source.

- Inspect the source: `GET /graph?project=<source id>` takes the usual unit
  filters (`types`, `statuses`, `tags`, `q`, `assignee`, ...) and returns
  `nodes`, `edges`, `facets` and `imported` ({source uid: uid of the copy
  here}); `GET /unit?project=<source id>&uid=<uid>` reads one node.
- Plan: `POST /plans {"project": "<source id>", "units": ["I-0011", ...],
  "with_children": false, "note": "...", "lang": "en"}`. `with_children`
  also copies the descendants of each node.
- What a copy keeps: title, body, tags, attachments (hard-linked when the
  projects share a disk, else copied) and the links between the copied
  nodes. Comments and history stay in the source. Each copy ends with a line
  "Imported from <project> [[prj_xxx/UID]]". A node imported before is
  skipped (`summary.skipped`).
- Undo moves the copies to this project's recycle bin.

## Split

Base: `/api/agent/v1/project-move`, with `X-ARK-Project` the project to
split. You need admin access to it.

- Inspect: `GET /clusters` groups the branches by how much they link to each
  other: `clusters[]` with `id` (`c-<root uid>`), `units`, `cut_links` (links
  to other clusters), `hub_links` (links to a hub) and `roots`. A **hub** is a
  root that holds most of the project (the main task or storyline); it stays.
  A separate line of work cuts 0 to a handful of links. A cluster that cuts
  dozens belongs with the rest: say so instead of proposing it. Read the
  roots (`GET /api/agent/v1/units/<uid>`) and check they are the line the
  person meant.
- Plan: `POST /plans {"mode": "split", "clusters": ["c-S-0002"] or "roots":
  ["S-0002"], "new_project": {"name": "...", "description": "..."},
  "cross_links": "keep", "note": "...", "lang": "en"}`.
- `cross_links`: `keep` (default) records each cut link on both nodes as a
  reference to the other project; `drop` removes them (an undo restores
  them). Expect few of them.
- The new project is created on apply by the account that applies; only that
  account has access at first. Other people (and their agents) need a grant
  from its admin. To keep working there, use the new project's id in
  `X-ARK-Project`.

## Merge

Base: `/api/agent/v1/project-move`, with `X-ARK-Project` the project that
goes away. You need admin access to both projects.

- Plan: `POST /plans {"mode": "merge", "target_project": "prj_...", "note":
  "...", "lang": "en"}`. Every node moves, so no link is cut.
- After the merge this project is read-only (a new node answers 409
  `PROJECT_MERGED`); its nodes are pointers in the recycle bin and old IDs
  and links still lead to the new place. An undo within 30 days makes it
  writable again.

## Safety

Every move backs up both databases first (kept 30 days), writes a journal,
and is rolled back when a step fails, also when the server restarts in the
middle. Moves and merges can be undone for 30 days.
