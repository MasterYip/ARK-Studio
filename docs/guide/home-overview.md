# Home: project overview

Home answers "what is the state of this project?" in one screen
(1440×900, no scrolling for the essentials).  Each card answers one
question, shows numbers first, and every count is a link to the filtered
view it summarises.

## Panels

| Panel | Question | Data | Click-through |
|---|---|---|---|
| Header | Which project, is it set up? | `S.project`, `overview.setup.state` | Edit, New, Open map, Import |
| Start row | How do I drive this with an agent? | `views/onboarding.js` | Expands the prompt cards; folds to one row once setup is confirmed |
| Pulse (KPI strip) | Is the project moving? | `overview.kpis` (now and net change vs 7 days ago), `overview.days` (14 days of created / completed) | Active, Blocked, In review, Open → Map by status; Overdue → Map with the new *Overdue* time filter; chart → Map, updated in the last 7 days |
| Needs you | What needs me now? | `needs_you` from `/api/project` (unchanged); count by kind, oldest wait, mine first | Accept / Send back / Comment / Decide / Open, View history (needs: tags) |
| Pipeline | Where is the work? | `overview.stages` (count, done, active, review, blocked per stage) | Map filtered to the stage's types, excluding archived and abandoned |
| Team | Who is doing what? | `overview.members` (open / active / review / overdue per effective assignee) | Map filtered by assignee |
| Due & blocked | What is next or late? | `overview.due.overdue`, `overview.due.soon`, `overview.blocked` (with `blocks` / `depends_on` blockers) | Opens the unit; header → Map, Overdue filter |
| Research | Is the argument supported? | `overview.research`: open questions, claims supported / contested / lacking evidence, experiments by status | Map by type and status; a lacking claim opens it |
| Running | What is running? | `overview.runs` (agent sessions), `overview.gpu` (queue + last probe) | Execute › Runs, Execute › GPU; empty hint when there is nothing |
| Activity | What changed? | `overview.activity`, grouped by day, actor display names | Opens the unit |

**Needs you filters and previews.** Under the panel head, **Types**, **Kind**
(Accept / Decide) and **Assignee** are drop-downs of capsule chips with an
**All** switch, like the Map filter rail. Values combine with AND across
groups. Only hidden values are stored (per project, in `localStorage`
`ark.needsFilter:<project>`), so a new type or assignee shows up. The count
reads `shown/total` while a filter is active. Each item carries up to 40
previews (`Store.NEEDS_MEDIA_LIMIT`), newest first by attach time. Borrowed
previews are sorted the same way across all linked nodes, and repeated files
are dropped. A row shows them all in one horizontal strip that the mouse
wheel scrolls sideways; at either end the wheel scrolls the page again.
Clicking a thumbnail opens the usual image viewer or video player. Videos
after the first four load when they scroll into view.

Removed: the **health ring** (an opaque score; replaced by concrete
Active / Blocked / Overdue / Review numbers), the **mini map** (a 236 px
force graph that cost a graph fetch and was too small to use; "Open map"
sits in the header) and **Next actions** (its items moved to the panel
they belong to: overdue and blocked → Due & blocked, claims without
evidence → Research).  The hero is now a one-line header.

## Data: `GET /api/project/overview`

One read-only round trip, computed in `Store.overview()` with aggregate SQL
(no per-unit queries).  Existing `/api/project` fields are unchanged.

```
{ today, generated_ms, timing, me,
  setup: {state},
  kpis: {active|blocked|review|overdue: {now, prev, delta}, open: {now},
         created: {week, prev}, completed: {week, prev}},
  days: [{date, created, completed, events}] × 14,
  stages: [{id, types, count, done, active, review, blocked, open}],
  members: [{username, member, open, active, review, blocked, overdue}],
  unassigned: {open, …},
  due: {overdue: [...], overdue_total, soon: [...], soon_total},
  blocked: {total, items: [{…unit, blockers: [{uid, title, status, relation}]}]},
  research: {questions: {open, total, latest: [...]},
             claims: {total, supported, contested, lacking, lacking_items: [...]},
             experiments: {total, by_status, latest: [...]}},
  activity: [... 40 meaningful items],
  runs: {total, active, counts, recent: [...]},
  gpu: {available, configured, enabled, queue_enabled, servers, jobs, free_gpus, probed_at} }
```

* *Prev / delta* rebuild the state 7 days ago from the activity log: a
  unit created in the window did not exist; the first status change in the
  window gives its old status (`unit.update` now logs `status: {from, to}`).
* *Completed per day* counts `status → done` changes (older logs without
  the `from/to` detail fall back to "status changed and the unit is done").
* *Overdue* = not done/archived, `end_at` before today (any type).
* *Claims*: supported = an incoming `supports` / `validates` link;
  contested = an incoming `contradicts` link; lacking = neither.
* *Pipeline*: archived units and the `archived` / `abandoned` statuses are excluded
  from every stage's denominator and segments. Review has its own segment.
  A `status:blocked` tag takes precedence over active/review so segments do not
  double-count a node. `/api/project` stage statistics use the same rules.
* *GPU*: never probes over SSH; it reports the config, queue job counts and
  the last probe result cached in memory.

## Layout

A 12-column grid (`.ov-grid`, `gap: 12px`).  The breakpoints are **container
queries on the Home page width**, not viewport queries: the attributes
pane (Node details) takes about 430 px at every desktop width, so a 1440 px
window gives Home about 990 px, 1280 gives 830 and 1024 gives 580.

```
>= 1100 px of Home (no attributes pane)
┌──────────────────────────────────────────────────────────────────────────┐
│ Legged locomotion lab  desc · 195 nodes · 74 links · 3m                   │
│                                             [+ New] [Open map] [Import] ✎ │ header
│ START WITH AN AGENT  one line of context     [setup] Show starter prompts │ start row
├──────┬──────┬──────┬──────┬──────┬────────────────────────────────────────┤
│Active│Block.│Overd.│Review│ Open │ Last 14 days ▂▃▅▂▇▁▃ ■ created ■ done   │ pulse
├──────┴──────┴──────┴──────┴──────┴┬───────────────────────────────────────┤
│ NEEDS YOU 4  1 Decide · 3 Accept   │ PIPELINE  Capture ███░░  2/24         │
│ · 2 mine · oldest 19 days          │           Plan    ██▆░░ 10/34  …      │
│ [DECIDE] D-1 Title…  5d [actions]  ├───────────────────────────────────────┤
│ [ACCEPT] X-19 Title… 16d [actions] │ TEAM LOAD   open active review late   │
│ …                                  │ (OW) owner (you) 37 15 1 6  …         │
├────────────────┬─────────────────┬─┴──────────────┬────────────────────────┤
│ DUE & BLOCKED  │ RESEARCH        │ RUNNING        │ RECENT ACTIVITY        │
└────────────────┴─────────────────┴────────────────┴────────────────────────┘
  needs 7 columns | pipeline + team 5; lower row 3 + 3 + 3 + 3

900-1099 px (1440 with the pane): same rows; the deltas show as "+9"
(full text in the tooltip); the lower cards go 2 x 2.
760-899 px (1280 with the pane): the 14-day chart wraps under the tiles.
< 760 px (1024 with the pane): Needs you full width; pipeline | team side
by side; lower cards 2 x 2.  < 520 px: one column.

Phone (viewport <= 760 px, ark_studio/web/css/mobile.css): one column — header,
Needs you, pulse (3 + 2 tiles, chart full width), due & blocked, running,
pipeline, team, research, activity, start row; 44 px targets.
```

Home never sits under a pane.  With the workbench (> 980 px) the open
sidebars are drawn narrower on a narrow window so the workspace keeps at
least 480 px (`fitSidebars` in ark_studio/web/js/workbench-model.js; the stored widths
return on a wider window).  At 761-980 px a pinned Node details pane docks
beside the content at up to 46% of the window.  The two starter cards sit
side by side when there is room and stack otherwise.

At 1440 x 900 with the pane open, the header, start row, pulse, Needs you,
pipeline and team fit in the first screen; the four lower cards start just
below it.  Without the pane, everything fits.

The start row is folded by default once setup is confirmed or the project
has 20+ units; the user's Show / Hide choice wins.

The start row is one level: the title with one line of context, one setup
element (the setup state, or **Review & confirm** while a layer waits for
you) and, unfolded, two cards with a title, one line, **Copy prompt** and
**Preview**.  Everything else (the token note, the standing-rules note, the
ARK session id) is in the Preview dialog or the token dialog.  Preview
opens one dialog (another click switches its prompt in place), shows the
prompt as plain text and never creates a token; Copy runs one token flow at
a time (further clicks are ignored until its dialog closes).
tests/browser/home_start_panel.test.cjs clicks Preview ten times and Copy
four times and checks one dialog, one token, no task over 200 ms and no
keyboard handler left behind.

### The starter prompts

Home offers **two** starter prompts, each with one copy action
(ark_studio/web/js/starter-prompts.js).  They follow the interface language (English or
Chinese; one {en, zh} pair per line, so both versions keep the same steps).

- **Set up this environment** (`setup`): once per machine or new repository.
  The agent runs the setup flow, waits for the human to confirm in ARK (it
  never confirms), verifies, then explains how to use ARK and offers five
  example requests ("Plan this idea: ...", "Start a research team on ...",
  "Give me a status check", "Record what we just did", "Import my existing
  task folder ...").
- **Start / continue research team** (`team`): pasted into each new agent
  session.  The agent loads the research context (context, confirmed setup
  profile, open and blocked tasks with their comments, the agent-team skill),
  summarises where the project stands in under 15 lines, offers what to do
  next ("Start a research team on ...", "Continue <task>", "Plan this idea:
  ...", "Give me a status check", "Review the results of <task>") and, on
  the user's choice, coordinates the work with the agent-team skill.  If setup
  is not confirmed it says so and points to the Setup prompt.

Both prompts share their beginning, in this order:

1. the ARK address, project and token, and where to read the documentation:
   `GET /api/agent/v1/docs` first (index with a "start here" order), then
   `GET /api/agent/v1/docs/<path>` on demand; on an older server that answers
   404, `GET /api/agent/v1/interface` and `/skills` instead.  The prompts
   stay short and leave detail to the docs;
2. **core rules** (C1-C10 and a self-check) that hold for the whole
   conversation: read context, setup and the whole working mode before the
   first write; one request is one task node with the plan, linked to
   exactly one `derived_from` parent; dated progress lines in the task body,
   never in comments; each result as its own `review` node with its own
   media; every hand-off (result, plan to approve, choice, question) in
   **Needs you** as a `review` node or an open decision; every user decision
   as a `decision` node; check updates and reply to comments; presence while
   working; the task folder as an openable `file:` link on the node; no
   setup-register writes during task work; one summary comment when the
   session ends. Agents follow these rules more consistently when they are in
   the prompt itself than when they sit only in the docs (an evaluation
   with Claude Code and Codex found agents read only part of long docs). The
   rules point to [Recommended agent working mode](../agents/working-mode.md)
   for detail;

then their own steps, and last the session block (ARK session id, optional
JSONL upload).

tests/js/starter_prompts.test.mjs checks the order, the rules, both prompts' steps, and that every
API path, header, JSON field and interpolation of the English text appears
verbatim in the Chinese one.

### More prompts (the prompt library)

**More prompts** under the cards opens the prompt library.  Home shows the
*pinned* prompts; by default those are the two starter prompts.  Each entry
has a title, one line, **Copy prompt**, **Preview** and **Pin**; pinned
entries can be moved up and down, and **Restore default pins** goes back to
the two starter prompts.  Pins are kept per person and per project on the
server (`POST /api/prompts {action: "pins"}`); an older server keeps them in
the browser.  Copy works like the starter prompts: the same personal token,
the project's composed agent rules (with their version stamp) first, then the
prompt, then the session block.

Built-in prompts (English and Chinese, short): **Organize or move project
content** (reads `docs/agents/project-content.md`; inspect, plan, approval,
apply, report), **Sort the review backlog**, **Continue a task** (a unit ID
field in the library fills `{{unit}}`), **Plan an idea**, **Check the project
status** (read only), **Resume interrupted work**, **Set up this device** and
**Add a custom prompt**.  The last one tells the agent how to write a prompt
with the person and save it with `POST /api/agent/v1/prompts`.

**Custom prompts** have a title, a one-line description and a body with the
placeholders `{project}` (name and id), `{unit}` (entered when copying) and
`{user}`; other braces stay as written.  *My prompts* are the person's own
(they need only read access to the project); *Project prompts* are shown to
everyone in the project and only project admins add or change them.
**Export** writes both lists as JSON (`{"format": "ark-prompts", "prompts":
[{title, description, body}]}`) and **Import** adds such a file to My prompts,
skipping prompts that are already there.  Storage: `<project>/prompts.json`
and `<private dir>/prompts/<user>.json` (which also holds the pins), with a
content version per prompt (`p-…`) and for the library (`l-…`).

| Route | |
| --- | --- |
| `GET /api/prompts` | My and project prompts, my pins for the project (`null` = the defaults), `can_edit_project`, `rules_version` |
| `POST /api/prompts` | `{action: "save", scope, prompt: {id?, title, description, body}}`, `{action: "delete", scope, id}`, `{action: "import", scope, prompts}`, `{action: "pins", pins}` (people only) |
| `GET /api/agent/v1/prompts` | the token user's and the project's custom prompts |
| `POST /api/agent/v1/prompts` | `{title, description, body, scope?, id?}` or `{delete: id, scope?}`; personal by default, project scope only for a project admin; needs a token that may write |

tests/js/prompt_library.test.mjs covers the library, pins, placeholders and
export; tests/browser/prompt_library.test.cjs drives the dialog in both
languages.

### Review & confirm

The setup chip's **Review & confirm** (Home) and the Review buttons in
Settings > Resources open one paged dialog: **1/2 Project setup**, then
**2/2 My resources**, with Back / Next, each page showing its summary, digest
and its own Confirm.  Only project admins can confirm page 1; others see it
read-only with a note.  A page with nothing proposed says so.  Finish shows
the combined result per layer.  The paging rules live in
ark_studio/web/js/setup-review-model.js (tests/js/setup_review_model.test.mjs); the
dialog uses the existing `/api/skills/setup/state` and `/confirm` endpoints.

Charts are plain HTML/CSS bars (no library), with `role="img"`, an
`aria-label` summary and a per-day tooltip.  Created / done use
`--ov-created` / `--ov-done`, validated for CVD separation and contrast on
the light and dark surfaces.

## Performance

The project card retains its original gradient and grid, with the actual
research graph rendered as a transparent force-layout background. Its gravity
centre is at the card's right midpoint and its node envelope radius is 1.2
times the card height. It reuses the Map's active physics and degree-based
node sizing at native scale, so nodes stay legible as the background shrinks.
The card clips the graph; a left-to-right opacity mask keeps
text readable. It has no labels, keyboard focus or pointer interactions, and
respects reduced-motion preferences. The overview response includes minimal
live node/link data, avoiding a separate graph request.

`Store`-side aggregation is about 3 ms on the 200-unit project and about
19 ms on a 1000-unit project with 2000 activity rows.  The whole request is
about 26 ms on the server in account mode (members come from one org query;
runs from `SessionManager.brief()`); the payload's `timing` field shows the
split.

## Home shows (large projects)

The header graph draws the nodes of the person's **Home shows** filter for
the project: the newest N nodes by update time (200 / 500 / 1,000 / 2,000 /
all; default 500), optionally only some types and statuses and only nodes
updated in the last 7 / 30 / 90 days.  The filter is saved on the account
(`prefs.home_filter[project id]`, `PATCH /api/me`; `null` restores the
default) and applied by the server, so `graph` holds only those nodes:

```
graph: {nodes: [{id, type, status, degree}] newest first, edges, total, matched, limit,
        filter: {limit, types, statuses, days}}
```

`graph_limit`, `graph_types`, `graph_statuses` and `graph_days` query
parameters override the saved filter (a read-only device keeps its filter in
the browser and sends them).  The header simulates at most 1,000 nodes (the
newest); when it leaves nodes out, the line under the title says so
("Showing the newest 500 nodes · 12,340 in total · Change").  The same
fields are in Settings › Display.  The numbers and lists on Home always cover
the whole project.

The overview is cached per project revision, day, members and filter;
a limited graph reads only its own nodes' links.  Needs you builds four rows
and, after "Show all", 20 more each time the list end comes near the screen;
a row whose item did not change is reused by the next render.  Previews are
small JPEGs from `GET /api/media/{id}/thumb` (images; video poster frames
when ffmpeg is installed), and a `<video>` element is only made for a video
without a poster once it is on screen.  Home does not load the project's
graph snapshot for a project over 3,000 nodes (the Map loads it), nor does
the Local graph panel while nothing is selected.

Measured in headless Edge on a 30,000-node synthetic project (2,154 items
waiting in Needs you): first render 11.5 s → 2.3 s, the longest task 5.2 s →
0.25 s, none after the first render; the overview reply 6.4 MB → 80 KB.
