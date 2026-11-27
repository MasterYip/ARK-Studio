# Topics: nodes that hold a subgraph

The whole-project graph plus filters answers "what is connected to what", but
it rarely tells one storyline well: a filter shows too much or too little, and
the layout changes with every filter. A **topic** is a node (`type: topic`,
uid prefix `TP`) that keeps its own curated subgraph, so a narrative, an
experiment process or a related-work map can be shown exactly as intended.

Because a topic is an ordinary node it has a title, body, status, comments and
links, appears on the Map (dashed halo with its member count) and can be a
member of another topic.

**Story, Related work, Method and Experiment are built-in topics.** Each
project gets them once (the first time someone with write access opens the
topic list): ordinary topic nodes whose rule selects one type
(`meta.default` = `story`, `related`, `method`, `experiment`). They lead the
topic list and keep the old addresses `#/research/story` etc. Archiving one
does not bring it back.

## What a topic stores

The Filters rail and keyword search in a topic workspace filter **the nodes
inside the open topic**, across Graph, Document, Timeline and the Outline. Counts and
filter options describe those members, not the topic node or the whole project.
Filtering does not change the saved membership, rule, outline or positions, and
does not hide the topic tabs. Clearing the filters restores the complete topic.

A topic's Graph opens in the Force layout. A layout picked in the graph
toolbar (Node: the topic's saved positions, Mind, Timeline graph) is kept for
every topic and research line of the project in this browser, so switching
topics keeps it.
Filter changes reuse the current graph and retain its layout, pan and zoom,
including when all members are temporarily filtered out. Node transitions use
the same animation setting as the Map, without a synchronous force warm-up.

A topic is a saved, hand-tuned filter over the project graph:

```
members = (nodes matching the rule + nodes added by hand) - excluded nodes
```

| Part | Meaning |
| --- | --- |
| Rule | Optional unit filter (`q`, `types`, `statuses`, `importance`, `tags`, `tags_unset`, `origins`, `assignees`, `roots`, `created_since`, `updated_since`, `start_from`, `start_to`). Matching nodes join automatically, including nodes created later. |
| Manual members | Nodes added by hand; they stay regardless of the rule. |
| Excluded | Rule matches the topic must not show. Removing a rule match from a topic excludes it. |
| Outline | Reading order and levels of the members (`1`, `1.2`, ...). A member's parent is the one set in the topic, otherwise its `part_of` target when that is a member too. |
| Layout | Position of each member in the Node layout, saved on the server. |
| Caption | Optional short note per member, kept for agents and exports. The UI does not show or edit it. |
| Links shown | Links are the real links between members. The topic chooses which to show: a relation whitelist (`relations`, null for all) and individually hidden links (`hidden_links`). |

Nothing in a topic changes the member nodes. Drawing a link on a topic graph
creates a real link. On a topic graph, Del and **Remove from topic** only take
the node out of the topic; **Delete node** in the right-click menu moves it to
the project's recycle bin (Settings > Project > Recycle bin). Members in the
recycle bin are hidden and come back when restored.

Storage: membership, order, outline parents, positions and captions are rows in the
`topic_members` table (schema v6, created automatically); the rule and link
choices are in the topic unit's `meta.subgraph`.

## Using topics

- **Research → Topics** shows the open topic: its title line (with the
  body under it when the body is short plain text that fits in two lines),
  then the views. `#/research/topic/<uid>` links to a topic. Switching topics
  or views replaces only the topic area.
- The **Topics** view (activity bar, left sidebar by default; it can be moved
  and hidden like any workbench view) has stacked sections like the Editor's
  Explorer, which fold, resize and maximize, with a filter box on top. It
  comes forward the first time Research → Topics opens in a session.
  - **Topics**: every topic, built-in ones first; a topic that is `part_of`
    another sits under it. **+** creates a topic. From any page a click
    opens the topic.
  - **Lines**: the lines of work (`branch:a/b/c` tags) as a tree with node
    counts. A line opens the line view: every node with its tag, the lines
    below it included unless **只看本研究线** is on. A line can be
    associated with a topic: a topic whose rule is the line's tag (made by
    **固定为专题**), or a topic that carries the tag itself. The line row then
    shows a **专题** chip that opens the topic, and the topic row shows a
    **研究线** badge that opens the line. A topic always opens with its own
    members. The gear opens the line settings.
  - **Outline**: the open topic's reading order and levels, under the topic
    itself (level 0); on other pages it is empty. A click selects
    the node in the graph (or the section in Document) and opens its details.
    Drag an item onto the top or bottom edge of another to put it before or
    after it, or onto its middle to nest it. Tab / Shift+Tab change the level,
    Alt+↑/↓ move an item with its children, ← / → and Space fold. While the
    Graph or Timeline is shown, Ctrl+K Ctrl+0 / J / 1–7 / [ / ] / L and
    Ctrl+Shift+[ / ] fold the outline.
  Its sections and folds are kept in this browser. Without the workbench
  sidebars (narrow windows, phones) it opens as a drawer from the button at
  the end of the topic page's head.
- Every topic has three views:
  - **Graph** (default): the topic's graph, Force layout until another one is
    picked (one choice for all topics). The Node layout keeps the saved positions; **Auto
    layout** re-arranges them.
  - **Document**: the topic as one document. Level 0 is the topic itself, its
    title and whole body (**Edit** opens it in Node details). Each member is a
    section with its
    body (Markdown and HTML embeds), attachments (images, video, audio, PDF,
    HTML) and children; a nested topic links to its own document. Folded
    sections show only the title. VS Code keys: Ctrl+K Ctrl+0 fold all,
    Ctrl+K Ctrl+J unfold all, Ctrl+K Ctrl+1–7 fold level N, Ctrl+K Ctrl+[ / ]
    fold / unfold the focused section recursively, Ctrl+K Ctrl+L toggle,
    Ctrl+Shift+[ / ] fold / unfold. The second key works with or without
    Ctrl.
  - **Timeline**: members with dates on a Gantt chart.
- **Add nodes** searches the project. On any graph, right-click nodes →
  **Add to topic…**, or create a new topic from the selection.
- A topic's own settings are in its attributes panel (click the topic title):
  **Nodes** (open, add), **Filter** (the rule) and **Links** (relations shown,
  **Choose links**). The filter rail's **⋯ → Save as topic…** turns the
  current filters into a new topic's rule.
- **Choose links** shows hidden links dashed. Click a link to hide or show it,
  or toggle whole relation types; **Done** leaves the mode.
- Double-click a topic on the Map or inside another topic to open it; the trail
  above the title leads back.
- The inspector of any node lists the topics that show it.

## API

UI and agents use the same request shape.

| Method | Path | Purpose |
| --- | --- | --- |
| `GET` | `/api/topics` | Topics with a small preview (member positions, visible links). |
| `GET` | `/api/units/{id}/members` | Members in outline pre-order with `member: {step, path, depth, parent, parent_source, children, source, x, y, caption}`, all links between them (`hidden: true` when not shown), `excluded`, `subgraph` settings. `?full=1` adds member bodies. |
| `POST` | `/api/units/{id}/members` | Design the topic (see below); `members` is accepted as an alias of `add`. |
| `PATCH` | `/api/units/{id}/members` | Same as POST. |
| `DELETE` | `/api/units/{id}/members/{unit}` | Remove one member. |
| `POST` | `/api/units` | Create a topic: `{"type": "topic", "title": ..., "members": [...], "design": {...}}`. |

Agents use the same operations under `/api/agent/v1/units/{id}/members`
(`topics.read`, `topics.write` in the interface catalogue) with their agent
token.

Design body (every key optional, applied in this order):

```json
{
  "rule": {"types": ["story"], "tags": ["topic:control"]},
  "relations": ["supports", "validates", "derived_from"],
  "hide_links": [42], "show_links": [], "hidden_links": null,
  "add": [17, "C-0003", {"unit": "X-0002", "caption": "main experiment", "x": 300, "y": 80}],
  "remove": ["N-0007"],
  "exclude": [], "restore": [],
  "items": [{"unit": 17, "caption": "opening", "x": 90, "y": 80},
            {"unit": "C-0003", "parent": 17}],
  "order": [17, "C-0003", "X-0002"]
}
```

`parent` in `items` places a member in the outline: a member ref, `null` for
the top level, or `"auto"` to follow its `part_of` link again. Send the new
pre-order as `order` when moving subtrees.

The response is the topic view plus a `report` (`added`, `skipped`,
`removed`, `excluded`, `restored`, `updated`, `reordered`, `settings`). Moving
members only changes the layout and is not written to the activity log;
membership, order, caption and rule changes are logged as `topic.update`.

## Agent workflow

How an agent builds a topic through `/api/agent/v1` with its personal token
(`Authorization: Bearer arka_...`, plus `X-ARK-Project` in account mode). The
interface catalogue lists the operations as `topics.read` and `topics.write`.
A topic shows one storyline; the Map still shows everything. Make a topic
when a human should be able to follow a narrative, an experiment process or a
literature thread without filtering the Map themselves.

### 1. Make sure the nodes and links exist

A topic only selects and arranges existing nodes and links; it creates
neither. Create or find the nodes first (`POST /api/agent/v1/units`,
`GET /api/agent/v1/units?q=...&types=claim`) and connect them with real
relations (`POST /api/agent/v1/links`). The links a topic can show are the
links between its members.

### 2. Check for an existing topic

```http
GET /api/agent/v1/units?types=topic&q=warm-up
GET /api/agent/v1/units/{uid}          # unit.topics: the topics showing a node
```

Extend a topic that already tells this story instead of creating a parallel
one. The built-in Story, Related work, Method and Experiment topics
(`meta.default`) select their type by rule. Only add nodes of another type
that belong in that list, and do not change their rule.

### 3. Create the topic with its design

One call creates the topic node and applies a design:

```http
POST /api/agent/v1/units
{
  "type": "topic",
  "title": "Warm-up study, step by step",
  "status": "active",
  "body": "Question, method, the two experiments and the claims they support.",
  "design": {
    "add": ["Q-0001", "M-0001", "X-0001", "X-0002", "C-0001"],
    "order": ["Q-0001", "M-0001", "X-0001", "X-0002", "C-0001"],
    "items": [{"unit": "X-0001", "parent": "M-0001"},
              {"unit": "X-0002", "parent": "M-0001"}],
    "relations": ["uses", "supports", "validates", "derived_from"]
  }
}
```

The response is the new unit (`uid` `TP-...`, `member_count`). A top-level
`members: [...]` is a shortcut for `design.add`.

- **Title and body** are for a human: what the storyline shows, in a line or
  two. The topic is a node, so it appears on the Map and in search.
- **`order`** is the reading order (outline pre-order). Members not listed
  keep their relative order after the listed ones.
- **`items[].parent`** nests a member under another member (`1.1`, `1.2`):
  a member ref, `null` for the top level, or `"auto"` to follow its `part_of`
  link. Without an explicit parent, a member is nested under its `part_of`
  target when that target is a member too.
- **`relations`** limits the links shown to these relation types (`null` shows
  all). Hide single links with `hide_links: [link ids]` and show them again
  with `show_links`.
- **`rule`** (optional) makes the topic follow a filter: matching nodes join
  automatically, including nodes created later
  (`{"types": ["decision"], "tags": ["topic:warmup"]}`). Use a rule for
  "everything of this kind" and hand-picked members for a curated story. A
  topic may have both.

### 4. Read it back and check it

```http
GET /api/agent/v1/units/TP-0007/members
```

Returns `members` in outline order with
`member: {step, path, depth, parent, parent_source, source, x, y}`
(`source` is `manual` or `rule`), `edges` between members (`hidden: true` for
links the topic does not show), `excluded` and
`subgraph: {rule, relations, hidden_links}`. Check that the paths read as the
intended story and that the links a reader needs are not hidden.

### 5. Revise in place

Every later change is one `POST /api/agent/v1/units/{topic}/members` with any
of these keys, applied in this order:
`rule`, `relations`, `hidden_links`/`hide_links`/`show_links`, `add`, `remove`,
`exclude`, `restore`, `items`, `order`.

```json
{
  "add": [{"unit": "D-0003", "x": 650, "y": 80}],
  "remove": ["N-0007"],
  "items": [{"unit": "C-0001", "parent": null}],
  "hide_links": [42]
}
```

The response is the topic view plus a `report` (`added`, `skipped`,
`removed`, `excluded`, `restored`, `updated`, `reordered`, `settings`).
`skipped` lists nodes that were already members. A request that names an
unknown or archived node, a non-member in `items`/`remove`, or the topic
itself fails as a whole (HTTP 400 with the reason) and changes nothing.

- `remove` takes a hand-added member out. Removing a rule match records it in
  `excluded`, so the rule does not bring it back; `restore` undoes that.
- Removing never deletes or archives the node itself.
- Membership, order and rule changes are logged as `topic.update` with the
  agent as actor.

### 6. The node layout (topic map)

The Graph's Node layout shows members at their saved positions. Members without one are
placed automatically, left to right along the links (columns 280 px apart,
rows 100 px apart, starting at x 90, y 80). Usually leave the layout to the
automatic placement or to the user. Set positions only when the arrangement
carries meaning, for example a process from left to right with alternatives
stacked:

```json
{"items": [
  {"unit": "Q-0001", "x": 90,  "y": 80},
  {"unit": "M-0001", "x": 370, "y": 80},
  {"unit": "X-0001", "x": 650, "y": 20},
  {"unit": "X-0002", "x": 650, "y": 140},
  {"unit": "C-0001", "x": 930, "y": 80}
]}
```

`x` and `y` are the card's top-left corner in canvas pixels; a card is about
200 × 64. Keep steps about 280 px apart horizontally and 100–120 px
vertically. The user can drag cards or use **Auto layout**, which replaces the
saved positions.

Node graph behaviour (topic Graph and the Map's *Node graph* layout):

- Without saved positions the graph is laid out in layers, left to right
  along the links (sources left, targets right), with crossings minimised and
  long links kept straight. Unconnected groups are packed side by side.
- Saved positions are kept. A card without a position (for example a new
  member) is placed next to a linked card without moving the others.
- **Auto layout** re-lays out every card, animates to the new arrangement and
  can be undone (Ctrl+Z).
- Links are smooth curves from the right (output) side of a card to the left
  (input) side, routed around other cards. Several links on one side get
  their own slots; drag a link's handle (edit mode) to bend it.
- Hovering a card highlights its incoming and outgoing links. Zoomed in, the
  relation names appear at each input port.
- Cards are the same as Mind cards: the title wraps (up to six lines; the card
  grows and ports follow the real height), the footer shows the status dot,
  markers (Settings › Appearance › Graphs › Mind applies) and "UID · type", and image
  media appear as thumbnails. Right-click a thumbnail to **pin it as the main
  media** (a larger image on a chosen side; drag it to another side or by a
  corner to resize), unpin it or open it. A pinned image can be large; use
  **Auto layout** to make room for it.
- In edit mode, drag a card's bottom-right grip to change its width (140–640
  px); double-click the grip for the automatic width. Both are undoable.
  Widths are kept with the layout: per graph in this browser on the Map, and
  per member for topics (`w` below). Auto layout keeps widths.
- Zoomed far out (below 42%) cards are drawn as plain blocks; thumbnails load
  only for cards on screen at a readable zoom.

A member's card width is part of the topic layout. `PATCH
/api/units/{topic}/members` items accept `w` (canvas px, 140 to 640; `null`
for the automatic width) alongside `x`/`y`, and `GET` returns it as
`member.w`. It is stored in `topic_members.w` (project schema v12; older
databases gain the column on open).

### 7. Nest topics for larger maps

A topic can be a member of another topic. For a long programme, make one
topic per branch or chapter and an overview topic whose members are those
topics plus the key claims. Readers double-click a nested topic to open it,
and the trail above the title leads back.

### Rules of thumb

- One storyline per topic, 5–30 members. Above that, split it into nested
  topics.
- Order members as a reader should meet them: question → method → experiments
  → results → claims → open decisions.
- Let the links between members tell the story rather than a long topic body.
- Don't add a node to a topic its owner (`assignee_effective`) would not
  expect, and don't rearrange another person's topic. Create your own and
  link it to theirs with `relates_to`.
- A topic's status follows the storyline: `active` while it grows, `done` when
  it is complete. Archive a superseded topic; its members are not affected.
