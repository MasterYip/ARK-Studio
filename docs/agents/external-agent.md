# External agents and the ARK closed loop

ARK is intentionally an **API and data layer**, not the only orchestrator.
Codex and DeepSeek Harness (DSH) are better at planning, tool use and subagent
fan-out. The intended integration is:

```text
DSH / Codex coordinator
  ├─ read ARK project + tasks          (agent API)
  ├─ plan and dispatch native subagents (DSH subagents, Codex threads)
  ├─ collect reports / figures / code
  └─ write results back to ARK         (agent API + media)
         ├─ task status / evidence
         ├─ report artifact (Markdown + LaTeX)
         └─ rich-media rows (SVG/PNG)
```

**Use DSH's own subagent mechanism**, not ARK's `/api/agent-sessions` runner.
ARK task sessions are a simple managed-process fallback; they do not have the
planning, tool, context, or subagent capabilities of the DSH app.

## Bind the environment first

```http
GET /api/agent/v1/setup         # Authorization: Bearer <agent token>
```

If `readiness.ready` is false or this device needs setup, run the **setup**
skill before planning. Reuse the register and earlier answers, then call
`POST /api/agent/v1/setup/discover` with user-provided paths (or an empty list).
Show existing resources, configured GPU server names and provider presets;
ask only about missing, conflicting or changed details, stage by stage.
Discovery checks the ARK server, so verify this device's paths locally and
label observations by their source machine. Reconcile returned questions
with facts already known, then
`POST /api/agent/v1/setup/draft` with `propose: true`. Show the user
`summary_markdown` and the digest, and let them confirm it themselves (ARK UI
or `python -m arkstudio skills setup confirm`). Then
`POST /api/agent/v1/setup/verify`. After that, treat
`readiness.requires_human` (policies set to `ask`) as actions that need
explicit user approval every time.

## Read the interface first

```http
GET /api/agent/v1/interface     # Authorization: Bearer <agent token>
```

The descriptor is the machine-readable contract: capabilities, limits,
task structures, topologies, failure classes and the v2 manifest schema. Do not
infer behaviour from this document when the descriptor disagrees — the
descriptor wins. The normative human-readable version is
[`agent-interface.md`](agent-interface.md).

Before delegating, answer three questions explicitly:

1. **Shape** — `sequential`, `breadth`, `long-horizon` or `adversarial`? The
   first two decide whether a team is justified at all.
2. **Team** — `mode` and `size`. Start solo; `size > 1` needs a rationale,
   `size > 5` needs heterogeneity, `peer-team` needs heterogeneity and a critic.
3. **Budget** — `context.budget_tokens`, what to inline, what to spill, and
   which artefacts are referenced by path.

Then validate the worker brief with `POST /api/skills/agent-team/brief` and pass
the rendered `agent_brief.md` to the subagent.

## Documentation for agents

A starter prompt is short; ARK's full documentation is one request away.
Read the index, then fetch only the docs, or single sections, you need:

```http
GET /api/agent/v1/docs                                   # index (JSON)
GET /api/agent/v1/docs/guide/topics.md                   # one doc (text/markdown)
GET /api/agent/v1/docs/guide/topics.md?section=agent-workflow
GET /api/agent/v1/docs/agents/agent-interface.md?format=json
```

- **Index:** `{ok, format: "ark-agent-docs", version, base, source, usage,
  start_here: [{path, title, why, url}], endpoints: {interface, skills, setup,
  context, whoami}, sections, count, total_bytes, docs: [{path, title,
  summary, section, bytes, updated, url}]}`. `summary` is the first
  paragraph (at most 300 characters), `section` is `agents`, `guide`,
  `operations` or `development`, `updated` is the last change (UTC).
  `start_here` is the suggested reading order.
- **One doc:** `path` is the index's `path` (`.md` may be omitted). The reply
  is the Markdown as written; relative links such as `../guide/topics.md`
  resolve against the doc's URL. `?section=<slug>` returns one heading and
  everything under it; slugs are GitHub anchors (`## Agent workflow` ->
  `agent-workflow`; a repeated heading gets `-1`, `-2`).
- **`?format=json`** returns `{ok, path, title, section, updated, url,
  headings: [{level, text, slug}], heading?, markdown}`; in `markdown`,
  links to other served docs are rewritten to `/api/agent/v1/docs/...` paths
  (`#fragment` becomes `?section=fragment`).
- Errors: unknown or excluded paths and anything with `..` are `404
  DOC_NOT_FOUND`; an unknown slug is `404 SECTION_NOT_FOUND`; another
  `format` is `400`.

Authentication is the same as for every `/api/agent/v1` route (personal or
server agent token; a browser session or a paired phone alone is not
enough). The set covers the user guide, the agent docs, operations, and the
design and development notes; maintainer-only files (release process,
performance notes, the Go port bookkeeping, archives) are left out. The
server serves the copy embedded at build time, or `docs/` from disk in a
development checkout. These routes exist on `ark-server` only.

## Authentication

Use a **personal agent token**. The user creates it in ARK under
Settings -> Personal -> Agent access, or copies a Home starter prompt with
"Copy with my token", which puts the token, the ARK URL and the project id
into the prompt. Send it as:

```text
Authorization: Bearer arka_...
X-ARK-Project: <project id>     # account mode; otherwise the user's default project
```

The token acts as that user: it reaches only the user's granted projects,
capped by the token scope (`read` / `write`), and writes are attributed
`agent:<username>` (or `agent:<username>/<X-Agent-Name>`). A coordinator
working for user X therefore sets `assignee` X. Start with
`GET /api/agent/v1/whoami` to learn the user, scope and reachable projects.
Never write the token into files, units or reports.

The shared server token in `runtime/projects/.agent-token` is the legacy,
admin-only credential. It still works (attributed `agent:api`), but prefer a
personal token. Task-session read tokens are deliberately read-only and must
not be used by an external coordinator.

## Session id and optional transcript upload

Home starter prompts include an ARK session id (`ark-ses-…`). Quote it in your
first reply and in reports. If the project accepts session uploads and **the
user agrees**, upload your session JSONL periodically. You can run the watcher
(`GET /api/agent/v1/sessions/watcher.py`, or
`ark-server session-watch --id ark-ses-…`), or post complete lines
yourself:

```http
GET  /api/agent/v1/sessions/{id}          # uploads_enabled, received_bytes (next offset), limits
POST /api/agent/v1/sessions/{id}/upload   # {offset, content, sha256?, mode: append|replace}
```

The endpoint returns `403 SESSION_UPLOADS_DISABLED` while the project setting is
off. Never upload without the user's permission. See
[Session context management](session-context.md).

## Read

```http
GET /api/agent/v1/context
GET /api/agent/v1/units?limit=500
GET /api/agent/v1/units/{uid}
GET /api/agent/v1/insights
GET /api/agent/v1/media/{id}/raw?download=1
GET /api/agent/v1/export?format=bundle&units=F-0045&depth=1
GET /api/agent/v1/export?format=project
```

Media and export downloads need no browser session when using a personal
agent token, including tokens with `read` scope. Use `X-ARK-Project` to select
an authorized project. Existing media `raw_url` values (`/api/media/{id}/raw`)
and GET/HEAD `/api/export` also accept personal tokens. Bundle `check=1`
returns counts and bytes instead of the ZIP. See
[Markdown bundles](../guide/markdown-bundles.md#downloading-with-an-agent-token) for
download commands and media access rules.

## Write tasks and links

```http
POST /api/agent/v1/units
POST /api/agent/v1/units/{uid}
POST /api/agent/v1/links
POST /api/agent/v1/media
```

Example:

```json
POST /api/agent/v1/units
{
  "title": "FP-1 · Derive Fokker–Planck from Langevin",
  "type": "task",
  "status": "open",
  "importance": "p1",
  "tags": ["program:fp-sde-validation", "step:1"]
}
```

Update with a complete status/tag set, not both `status:open` and
`status:done` tags. ARK validates that a unit has exactly one status.

### Topics

A topic (`type: topic`) is a node that shows a chosen subgraph of the project:
a storyline in reading order on its own canvas. Create it with
`POST /api/agent/v1/units` (`"type": "topic"` plus a `design`); revise and
read it with `POST`/`GET /api/agent/v1/units/{uid}/members`. See the
[agent workflow](../guide/topics.md#agent-workflow).

### Selected-node Git history

Tracking is off by default. Enable it before editing a node whose title and
Markdown body need an audit trail. All requests below use the personal bearer
token and `X-ARK-Project` header described above. Reads require read scope;
enabling, disabling and restoring require write scope and project write access.

```http
POST /api/agent/v1/units/F-0001/history
Content-Type: application/json

{"enabled": true}
```

This records the initial text. Subsequent normal unit updates automatically
record changed titles and bodies. Read the versions, compare them and inspect
their text with:

```http
GET /api/agent/v1/units/F-0001/history?limit=50&offset=0
GET /api/agent/v1/units/F-0001/history/diff?from=<full-hash>&to=<full-hash>
GET /api/agent/v1/units/F-0001/history/<full-hash>
```

Use `versions[].id` for revision hashes, and `has_more` for paging. To restore,
first read the latest history state and use its `current_hash` as the concurrent
edit guard:

```http
POST /api/agent/v1/units/F-0001/history/restore
Content-Type: application/json

{"revision": "<full-hash>", "expected_hash": "<current_hash>"}
```

Restoring appends a new revision. If the current text changed in the meantime,
refresh the history and review the difference before retrying. Disable future
recording with `{"enabled": false}`; existing revisions remain readable.

GUI edits are grouped after five minutes idle and sufficient cumulative changes;
agent/token updates still record immediately. `pending` in the history response
indicates human edits not yet checkpointed. To checkpoint saved text immediately,
POST `history/checkpoint` with `{"expected_hash":"<current_hash>"}`. To apply an
old change block, POST `history/apply` with `from`, `to`, `file` (`body.md` or
`title.txt`), `change` (the per-file index from `diff.changes`) and `expected_hash`.
Omit `change` to apply the full file. To must match current text; re-fetch after
each change. Both actions require write access and enabled tracking and append
a revision. See [node text history](../guide/node-history.md) for payload examples.

The Git repository is part of the project at `<project>.ark/history.git/` and
tracks only `title.txt` and `body.md`. Media and the SQLite database are not Git
snapshots. See [node text history](../guide/node-history.md) for GUI instructions, the full
API contract and whole-project backup requirements. Use the API for edits;
direct SQL changes bypass version recording.

## Dispatch with DSH subagents

A DSH coordinator should start its native subagents (the same mechanism this
session uses) with one prompt per ARK task, passing:

- the task UID and requirement text,
- the output directory / artifact path,
- the expected evidence (Markdown, table, SVG),
- the context budget and the artefact paths to read instead of pasting,
- the failure classes to report if the unit stalls,
- an explicit "do not modify ARK DB" instruction.

The coordinator then writes the returned artifacts back to ARK. This keeps
analysis and fan-out in DSH while ARK remains the durable project system.

`scripts/agents/external_research_agent.py` is a DB helper and an **explicit ARK
session fallback** (`--ark-session-fallback`); it is not the recommended
subagent path.

## Skills and protocol for external agents

ARK exposes its adapted agent-team, RRSI and design contracts to a token-holding
coordinator:

```http
GET /api/agent/v1/skills
GET /api/agent/v1/skills/{id}
GET /api/agent/v1/interface
```

Use `GET /api/skills/interface` (no token, same loopback origin) for the
descriptor, `GET /api/skills/{id}/prompt` for the copy-ready prompt, and
`POST /api/skills/agent-team/brief` to validate a delegation brief.
`POST /api/skills/agent-team/tasks` creates a real task unit with a rendered
goal manifest. RRSI selection is available at `POST /api/skills/rrsi/round`;
design verification at `POST /api/skills/design/verify`. See
[`skills.md`](skills.md) for payloads and the protocol contract, and
[`agent-interface.md`](agent-interface.md) for the v2 rules.

## Reports and rich media

1. Copy SVG/PNG files into the project's `assets/`.
2. Create an `artifact` unit with the Markdown report in `body` (LaTeX `$...$`
   and fenced code are rendered by ARK).
3. Add media rows with relative paths:

```json
POST /api/agent/v1/media
{
  "unit": "F-0005",
  "url": "assets/validation-summary.svg",
  "kind": "image",
  "caption": "OU density and diffusion scaling"
}
```

The report's Markdown can also use the raw endpoint:

```markdown
![OU density](/api/media/17/raw)
```

The inspector has a **Rendered report** view and the media gallery opens the
raw files. Task results remain linked through `part_of` / `relates_to`.

## Validation experiment

The included experiment creates:

- one program (`Validation Program · Fokker–Planck, SDE & Diffusion`),
- three tasks (`T-0019` FP derivation, `T-0020` OU Monte Carlo, `T-0021`
  diffusion scaling),
- a report artifact and two SVG figures.

The three analyses are dispatched to **DSH subagents**, then published back
through the agent API. That proves the complete loop:
read → dispatch → report/figures → DB write-back → open in ARK.
